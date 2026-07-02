package voice

import (
	"context"
	"fmt"
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
	ssrcUser  sync.Map // uint32 -> userID
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
	vc.AddHandler(func(_ *discordgo.VoiceConnection, vs *discordgo.VoiceSpeakingUpdate) {
		l.ssrcUser.Store(uint32(vs.SSRC), vs.UserID)
	})

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

				userID := ""
				if u, ok := l.ssrcUser.Load(ssrc); ok {
					userID, _ = u.(string)
				}
				go l.process(seg, userID)
			}
		}
	}
}

func (l *voiceListener) process(pcm []int16, userID string) {
	// 48k stereo -> 16k mono, then drop leading/trailing silence so whisper isn't
	// fed the up-to-3s pause (the main hallucination trigger).
	mono := downmixTo16kMono(pcm)
	speech := trimSilence(mono)

	// Skip segments with no real speech (silence/noise) -> avoids hallucinations.
	if len(speech) < minSpeechSamples || rms(speech) < minSpeechRMS {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	raw, err := transcribe(ctx, speech)
	if err != nil {
		log.Println("stt: transcribe error:", err)
		return
	}

	text := cleanTranscript(raw)
	if text == "" || !containsWakeWord(text) || l.channelID == "" {
		return
	}

	content := "🎙️ " + text
	if userID != "" {
		content = fmt.Sprintf("🎙️ <@%s>: %s", userID, text)
	}
	_, _ = l.session.ChannelMessageSend(l.channelID, content)
}
