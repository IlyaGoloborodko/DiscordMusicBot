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

	// wakeFollowupWindow: after hearing the wake word alone, keep listening this
	// long for the command in the following segment(s).
	wakeFollowupWindow = 10 * time.Second
)

var (
	listenMu      sync.Mutex
	listenStarted = map[*discordgo.VoiceConnection]bool{}
)

type voiceListener struct {
	session   *discordgo.Session
	channelID string
	armed     sync.Map // uint32 ssrc -> time.Time: command follow-up window after a wake word
}

type speaker struct {
	dec  *gopus.Decoder
	buf  []int16
	last time.Time
}

// arm/disarm/isArmed track speakers who just said the wake word alone, so the
// next segment from them is treated as the command (within wakeFollowupWindow).
func (l *voiceListener) arm(ssrc uint32)    { l.armed.Store(ssrc, time.Now().Add(wakeFollowupWindow)) }
func (l *voiceListener) disarm(ssrc uint32) { l.armed.Delete(ssrc) }
func (l *voiceListener) isArmed(ssrc uint32) bool {
	v, ok := l.armed.Load(ssrc)
	if !ok {
		return false
	}
	if until, _ := v.(time.Time); time.Now().Before(until) {
		return true
	}
	l.armed.Delete(ssrc)
	return false
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
				go l.process(vc, seg, userID, ssrc)
			}
		}
	}
}

func (l *voiceListener) process(vc *discordgo.VoiceConnection, pcm []int16, userID string, ssrc uint32) {
	lvl := sttLogLevel()

	// 48k stereo -> 16k mono, then drop leading/trailing silence so the models
	// aren't fed the up-to-3s pause (the main hallucination trigger).
	mono := downmixTo16kMono(pcm)
	speech := trimSilence(mono)
	speechMs := len(speech) * 1000 / sttSampleRate

	// Skip segments with no real speech (silence/noise).
	if len(speech) < minSpeechSamples || rms(speech) < minSpeechRMS {
		if lvl >= sttLogAll {
			log.Printf("[stt] user=%s SKIP gate (speechMs=%d rms=%.0f)", userID, speechMs, rms(speech))
		}
		return
	}
	if l.channelID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Without a Vosk gate: run whisper on every segment and look for the wake word
	// in its output (original behavior).
	if voskServerAddr() == "" {
		raw, err := transcribe(ctx, speech)
		if err != nil {
			if lvl >= sttLogCommands {
				log.Println("[stt] whisper error:", err)
			}
			return
		}
		text := cleanTranscript(raw)
		after, wake := stripWakeWord(text)
		if lvl >= sttLogAll {
			log.Printf("[stt] user=%s speechMs=%d WHISPER=%q wake=%v", userID, speechMs, raw, wake)
		}
		// Send the full text (wake word included) to the AI, but only when there
		// is an actual command after the wake word.
		if wake && after != "" {
			l.handleAI(vc, userID, text)
		}
		return
	}

	// With a Vosk gate: cheap wake-word detection first, heavy whisper only for
	// the command.
	gate, err := voskTranscribe(ctx, speech)
	if err != nil {
		if lvl >= sttLogCommands {
			log.Println("[stt] vosk error:", err)
		}
		return
	}
	voskCmd, wake := stripWakeWord(gate)
	armed := l.isArmed(ssrc)
	if lvl >= sttLogAll {
		log.Printf("[stt] user=%s speechMs=%d VOSK=%q wake=%v armed=%v", userID, speechMs, gate, wake, armed)
	}

	switch {
	case wake && voskCmd == "":
		// Only the wake word this segment -> listen for the command next (<=10s).
		l.arm(ssrc)
		if lvl >= sttLogCommands {
			log.Printf("[stt] user=%s wake word heard, waiting for command", userID)
		}
		return
	case wake || armed:
		// Command present: same segment as the wake word, or the armed follow-up.
		l.disarm(ssrc)
	default:
		return // no wake, not armed -> command model not run
	}

	// Vosk-only: use the wake-word pass's own transcript as the command (no
	// whisper). Better at Russian, just without punctuation. Full text incl. the
	// wake word is sent to the AI.
	if voskOnly() {
		command := cleanTranscript(gate)
		if lvl >= sttLogCommands {
			log.Printf("[stt] user=%s VOSK command=%q", userID, command)
		}
		if command != "" {
			l.handleAI(vc, userID, command)
		}
		return
	}

	// Otherwise transcribe the command with the main whisper model; the full text
	// (wake word included) goes to the AI.
	raw, err := transcribe(ctx, speech)
	if err != nil {
		if lvl >= sttLogCommands {
			log.Println("[stt] whisper error:", err)
		}
		return
	}
	command := cleanTranscript(raw)
	if lvl >= sttLogAll {
		log.Printf("[stt] user=%s WHISPER=%q command=%q", userID, raw, command)
	}
	if command != "" {
		l.handleAI(vc, userID, command)
	}
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

	// Duck the music while the AI thinks; restore when the response lands (if it
	// starts new playback that stream begins un-ducked).
	p := playerManager.Get(l.session, vc, vc.GuildID, l.channelID)
	p.Duck()
	defer p.Unduck()

	// Give the agent the current queue so it can answer / voice questions about
	// what is playing and what is next.
	now, queue := p.Snapshot()

	ai := aiService.NewClient()
	resp, err := ai.Agent(ctx, aiService.AgentRequest{
		Session: l.agentSession(vc.GuildID, userID),
		Message: message,
		Context: map[string]any{
			"now_playing": now,
			"queue":       queue,
			"queue_len":   len(queue),
		},
		Tools: aiService.PlayerTools(),
	})
	if err != nil {
		if lvl >= sttLogCommands {
			log.Println("[stt] AI error:", err)
		}
		return
	}
	if lvl >= sttLogCommands {
		action, tracks := resp.PrimaryEffect()
		log.Printf("[stt] <- AI tool_calls=%d action=%q tracks=%d display=%q clarify=%q",
			len(resp.ToolCalls), action, len(tracks), resp.DisplayText, resp.Clarification)
	}

	// The player announces the display/clarification text (single source),
	// applies the action and handles playback.
	p.ApplyAgent(resp)
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
