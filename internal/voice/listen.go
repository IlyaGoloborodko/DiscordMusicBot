package voice

import (
	"context"
	"discordAudio/internal/aiService"
	"log"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gopus"
)

const (
	// maxDecodeSamples is the per-channel output buffer for opus_decode. It must be
	// >= the largest possible Opus frame (120ms = 5760 @48kHz), otherwise packets
	// with longer frames fail to decode and get dropped.
	maxDecodeSamples = 5760

	// pauseTimeout: finalize a speaker's utterance after this much silence.
	pauseTimeout = 3 * time.Second

	// Segment bounds in interleaved-sample counts (48kHz * 2ch).
	maxSegmentSamples = recvSampleRate * recvChannels * 10     // ~10s hard cap
	minSegmentSamples = recvSampleRate * recvChannels * 4 / 10 // ~0.4s floor (skip noise)

	// Speech gate, applied to the trimmed 16kHz-mono clip before whisper.
	minSpeechSamples = sttSampleRate * 3 / 10 // ~0.3s of audible content
	minSpeechRMS     = 250                    // skip quiet/near-silent clips
)

var (
	listenMu      sync.Mutex
	listenStarted = map[*discordgo.VoiceConnection]bool{}
)

type voiceListener struct {
	session   *discordgo.Session
	channelID string
}

type speaker struct {
	dec  *gopus.Decoder
	buf  []int16
	last time.Time
}

// StartVoiceListener captures voice in the channel: it decodes each speaker's
// opus to PCM, segments utterances on a 3s pause (or 10s cap), transcribes them
// with whisper, and — if a wake word is present — posts the transcript to the
// text channel. Idempotent per voice connection.
func StartVoiceListener(s *discordgo.Session, vc *discordgo.VoiceConnection, channelID string) {
	if vc == nil {
		return
	}

	listenMu.Lock()
	if listenStarted[vc] {
		listenMu.Unlock()
		return
	}
	listenStarted[vc] = true
	listenMu.Unlock()

	l := &voiceListener{session: s, channelID: channelID}

	go l.run(vc)
}

func (l *voiceListener) run(vc *discordgo.VoiceConnection) {
	speakers := make(map[uint32]*speaker)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case pkt, ok := <-vc.OpusRecv:
			if !ok {
				return
			}
			if pkt == nil || len(pkt.Opus) == 0 {
				continue
			}

			sp := speakers[pkt.SSRC]
			if sp == nil {
				dec, err := gopus.NewDecoder(recvSampleRate, recvChannels)
				if err != nil {
					log.Println("opus decoder:", err)
					continue
				}
				sp = &speaker{dec: dec}
				speakers[pkt.SSRC] = sp
			}

			pcm, err := sp.dec.Decode(pkt.Opus, maxDecodeSamples, false)
			if err != nil {
				continue
			}
			sp.buf = append(sp.buf, pcm...)
			sp.last = time.Now()

		case <-ticker.C:
			now := time.Now()

			for ssrc, sp := range speakers {
				if len(sp.buf) == 0 {
					continue
				}
				if now.Sub(sp.last) < pauseTimeout && len(sp.buf) < maxSegmentSamples {
					continue
				}

				seg := sp.buf
				sp.buf = nil
				if len(seg) < minSegmentSamples {
					continue
				}

				// The SSRC->user mapping lives in the library: it is populated from
				// Speaking (OP5) events starting with the burst Discord sends at
				// connection start — before this listener even exists.
				userID, _ := vc.SSRCUser(ssrc)
				go l.process(vc, seg, userID)
			}
		}
	}
}

func (l *voiceListener) process(vc *discordgo.VoiceConnection, pcm []int16, userID string) {
	// 48k stereo -> 16k mono, then drop leading/trailing silence so whisper isn't
	// fed the up-to-3s pause (the main hallucination trigger).
	lvl := sttLogLevel()

	mono := downmixTo16kMono(pcm)
	speech := trimSilence(mono)
	speechMs := len(speech) * 1000 / sttSampleRate

	// Skip segments with no real speech (silence/noise) -> avoids hallucinations.
	if len(speech) < minSpeechSamples || rms(speech) < minSpeechRMS {
		if lvl >= sttLogAll {
			log.Printf("[stt] user=%s SKIP gate (speechMs=%d rms=%.0f, need >=%dms & rms>=%d)",
				userID, speechMs, rms(speech), minSpeechSamples*1000/sttSampleRate, minSpeechRMS)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	raw, err := transcribe(ctx, speech)
	if err != nil {
		if lvl >= sttLogCommands {
			log.Println("[stt] transcribe error:", err)
		}
		return
	}

	// Only act on utterances addressed to the assistant ("Арсен ..."). Everything
	// after the wake word is the command. Each speaker is transcribed on its own
	// SSRC, so we only ever act on the person who actually said the wake word.
	text := cleanTranscript(raw)
	command, wake := stripWakeWord(text)

	// Level 2: log every transcribed utterance (full whisper output).
	if lvl >= sttLogAll {
		log.Printf("[stt] user=%s speechMs=%d rms=%.0f wake=%v raw=%q clean=%q cmd=%q",
			userID, speechMs, rms(speech), wake, raw, text, command)
	}

	if !wake || l.channelID == "" {
		return
	}
	if command == "" {
		if lvl >= sttLogCommands {
			log.Printf("[stt] user=%s wake word only, no command", userID)
		}
		return
	}
	l.handleAI(vc, userID, command)
}

// handleAI routes a spoken command to the AI agent (same path as /prompt) and
// applies its response: posts the answer and hands playback to the player.
func (l *voiceListener) handleAI(vc *discordgo.VoiceConnection, userID, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	lvl := sttLogLevel()
	if lvl >= sttLogCommands {
		log.Printf("[stt] -> AI user=%s msg=%q", userID, message)
	}

	ai := aiService.NewClient()
	resp, err := ai.Agent(ctx, aiService.AgentRequest{
		Session: l.agentSession(vc.GuildID, userID),
		Message: message,
	})
	if err != nil {
		if lvl >= sttLogCommands {
			log.Println("[stt] AI error:", err)
		}
		return
	}
	if lvl >= sttLogCommands {
		log.Printf("[stt] <- AI action=%q tracks=%d display=%q clarify=%q",
			resp.Action, len(resp.Tracks), resp.DisplayText, resp.Clarification)
	}

	ack := resp.DisplayText
	if resp.Action == aiService.ActionClarify && resp.Clarification != "" {
		ack = resp.Clarification
	}
	if ack != "" {
		l.send(ack)
	}

	playerManager.Get(l.session, vc, vc.GuildID, l.channelID).ApplyAgent(resp)
}

// agentSession builds the AI session for a spoken command, resolving the
// speaker's username from the guild member (falling back to the user object).
func (l *voiceListener) agentSession(guildID, userID string) aiService.AgentSession {
	sess := aiService.AgentSession{
		GuildID:   guildID,
		ChannelID: l.channelID,
		UserID:    userID,
	}
	if userID != "" {
		if m, err := l.session.State.Member(guildID, userID); err == nil && m.User != nil {
			sess.UserName = m.User.Username
		} else if u, err := l.session.User(userID); err == nil {
			sess.UserName = u.Username
		}
	}
	return sess
}

// send posts a message to the listener's text channel without pinging anyone.
func (l *voiceListener) send(content string) {
	if l.channelID == "" {
		return
	}
	_, _ = l.session.ChannelMessageSendComplex(l.channelID, &discordgo.MessageSend{
		Content:         content,
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
	})
}
