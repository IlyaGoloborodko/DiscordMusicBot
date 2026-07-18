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

	// pauseTimeout: finalize a speaker's utterance after this much silence. It is
	// dead time on every single command, and in a lively channel a longer wait
	// also means segments keep running into maxSegmentSamples — measured live at
	// 3s, utterances came back at 9.8-10.2s constantly, so a command could sit in
	// the buffer for nine seconds before anything even looked at it.
	//
	// Splitting an utterance at a mid-sentence pause is handled rather than
	// avoided: "Марина" alone arms the speaker, and the next segment is taken as
	// the command (see wakeFollowupWindow).
	pauseTimeout = time.Second

	// Segment bounds in interleaved-sample counts (48kHz * 2ch).
	maxSegmentSamples = recvSampleRate * recvChannels * 10     // ~10s hard cap
	minSegmentSamples = recvSampleRate * recvChannels * 4 / 10 // ~0.4s floor (skip noise)

	// Silence floor, applied to the 16kHz-mono clip. Deliberately permissive: it
	// only rejects digital silence so we don't spend a Vosk call on it. A strict
	// gate here is worse than a useless one — it drops a quiet speaker's whole
	// utterance and the bot just never answers.
	minSpeechRMS = 40

	// wakeFollowupWindow: after hearing the wake word alone, keep listening this
	// long for the command in the following segment(s).
	wakeFollowupWindow = 10 * time.Second

	// streamIdleTimeout: how long a silent speaker keeps their streaming gate
	// connection. Each one holds a recognizer on the Vosk server, so idle
	// speakers should not sit on them; reconnecting costs one dial.
	streamIdleTimeout = 60 * time.Second

	// capOverlapSamples is the audio kept when the segment cap is reached in the
	// middle of continuous speech. It has to outlast the gate's decoding lag,
	// measured at roughly 2s on this model — the wake word can be spoken and not
	// yet visible in the transcript when the cap fires, and a shorter tail would
	// throw away the very audio that proves it.
	capOverlapSamples = recvSampleRate * recvChannels * 4 // ~4s
)

// listeners holds the live listener per voice connection, so a repeat /join can
// update where it talks instead of being ignored, and a disconnect can stop it.
var (
	listenMu  sync.Mutex
	listeners = map[*discordgo.VoiceConnection]*voiceListener{}
)

type voiceListener struct {
	session *discordgo.Session

	// chMu guards channelID: the text channel the bot answers in. It follows the
	// most recent command instead of being frozen at the first /join — the AI
	// service keys its conversation memory by guild+channel, so a channel id that
	// silently disagrees with the player's is a lost history, not a cosmetic bug.
	chMu      sync.RWMutex
	channelID string

	armed sync.Map      // uint32 ssrc -> time.Time: command follow-up window after a wake word
	done  chan struct{} // closed on disconnect to stop run()
}

// chID returns the text channel the listener currently answers in.
func (l *voiceListener) chID() string {
	l.chMu.RLock()
	defer l.chMu.RUnlock()
	return l.channelID
}

func (l *voiceListener) setChID(channelID string) {
	if channelID == "" {
		return
	}
	l.chMu.Lock()
	l.channelID = channelID
	l.chMu.Unlock()
}

type speaker struct {
	dec  *gopus.Decoder
	last time.Time

	// mu guards buf. In streaming mode the receive loop appends to it while the
	// Vosk reader goroutine takes it away at end of utterance.
	mu  sync.Mutex
	buf []int16

	// Streaming mode only (STT_VOSK_STREAM): one live gate connection per
	// speaker, plus the filter state the incremental downmix needs.
	stream       *voskStream
	dm           downmixer
	streamFailed bool // dial failed; fall back to the batch path for this speaker
}

func (s *speaker) append(pcm []int16) {
	s.mu.Lock()
	s.buf = append(s.buf, pcm...)
	s.mu.Unlock()
}

// take removes and returns the audio buffered so far.
func (s *speaker) take() []int16 {
	s.mu.Lock()
	seg := s.buf
	s.buf = nil
	s.mu.Unlock()
	return seg
}

// keepTail discards all but the last n samples, bounding memory while leaving
// enough audio that a wake word spoken just before the cut is still there.
func (s *speaker) keepTail(n int) {
	s.mu.Lock()
	if len(s.buf) > n {
		s.buf = append(s.buf[:0], s.buf[len(s.buf)-n:]...)
	}
	s.mu.Unlock()
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
	if l, ok := listeners[vc]; ok {
		listenMu.Unlock()
		// Already listening: point it at the channel the command came from rather
		// than dropping the update, or it keeps answering in the first /join
		// channel forever.
		l.setChID(channelID)
		return
	}
	l := &voiceListener{session: s, channelID: channelID, done: make(chan struct{})}
	listeners[vc] = l
	listenMu.Unlock()

	go l.run(vc)
}

// StopVoiceListener ends the listener for a voice connection. The fork never
// closes OpusRecv, so without this the run goroutine would block on a dead
// channel forever and its registry entry would keep the connection alive.
func StopVoiceListener(vc *discordgo.VoiceConnection) {
	if vc == nil {
		return
	}
	listenMu.Lock()
	l, ok := listeners[vc]
	delete(listeners, vc)
	listenMu.Unlock()
	if ok {
		close(l.done)
	}
}

func (l *voiceListener) run(vc *discordgo.VoiceConnection) {
	speakers := make(map[uint32]*speaker)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	defer func() {
		for _, sp := range speakers {
			if sp.stream != nil {
				sp.stream.Close()
			}
		}
	}()

	for {
		select {
		case <-l.done:
			return
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
			sp.last = time.Now()

			if voskStreaming() && !sp.streamFailed {
				if sp.stream == nil {
					l.openStream(sp)
				}
				if sp.stream != nil {
					// Keep the original 48k stereo for the accurate model, and feed
					// the gate its 16k mono as it arrives.
					sp.append(pcm)
					if err := sp.stream.Write(sp.dm.write(pcm)); err != nil {
						if sttLogLevel() >= sttLogCommands {
							log.Println("[stt] vosk stream write:", err)
						}
						sp.stream.Close()
						sp.stream = nil
						sp.dm = downmixer{}
					}
					continue
				}
			}
			sp.append(pcm)

		case <-ticker.C:
			now := time.Now()

			for ssrc, sp := range speakers {
				sp.mu.Lock()
				pending := len(sp.buf)
				sp.mu.Unlock()

				if sp.stream != nil {
					if pending == 0 {
						// Reclaim the connection from a speaker who has gone quiet;
						// each one holds a recognizer on the server.
						if now.Sub(sp.last) > streamIdleTimeout {
							sp.stream.Close()
							sp.stream = nil
							sp.dm = downmixer{}
						}
						continue
					}
					// The cap bounds memory: without it an unbroken monologue grows
					// the buffer without limit (14.7s segments were seen live).
					hitCap := pending >= maxSegmentSamples
					if now.Sub(sp.last) < pauseTimeout && !hitCap {
						continue
					}

					// Hitting the cap is not an utterance ending, so cutting there
					// would split a wake word across two segments and lose it. When
					// nothing near the name has been decoded, this audio will never be
					// used — drop it and leave the stream running, keeping a tail long
					// enough to cover the decoder's lag in case the name is being said
					// right now. Only a real wake candidate is worth cutting for, and
					// then it is cut exactly like any other utterance, so a command can
					// never be delivered twice.
					if hitCap && !containsNearWake(sp.stream.Peek()) {
						sp.keepTail(capOverlapSamples)
						continue
					}

					seg := sp.take()
					stream := sp.stream
					// Recycle: the recognizer keeps decoder state across utterances,
					// so without a fresh connection the next transcript would still
					// carry this one — and a single "Марина" would keep the gate open
					// for every sentence after it.
					sp.stream = nil
					sp.dm = downmixer{}

					if len(seg) < minSegmentSamples {
						go stream.Close()
						continue
					}
					userID, _ := vc.SSRCUser(ssrc)
					speechMs := len(seg) / recvChannels * 1000 / recvSampleRate
					// Off the receive loop: Flush waits on the decoder, and this
					// goroutine must not hold up incoming packets from anyone else.
					go func() {
						stream.Flush()
						gate := stream.TakeText()
						stream.Close()
						l.decide(vc, seg, gate, userID, ssrc, speechMs)
					}()
					continue
				}

				if pending == 0 {
					continue
				}
				if now.Sub(sp.last) < pauseTimeout && pending < maxSegmentSamples {
					continue
				}

				seg := sp.take()
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

	// Vosk's websocket protocol wants 16kHz mono, so downmix for the gate only.
	// Whisper gets the original 48k stereo — resampling is the server's job.
	mono := downmixTo16kMono(pcm)
	speechMs := len(mono) * 1000 / sttSampleRate

	if level := rms(mono); level < minSpeechRMS {
		if lvl >= sttLogAll {
			log.Printf("[stt] user=%s SKIP silence (speechMs=%d rms=%.0f)", userID, speechMs, level)
		}
		return
	}
	if l.chID() == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Without a Vosk gate: run whisper on every segment and look for the wake word
	// in its output (original behavior).
	if voskServerAddr() == "" {
		raw, err := transcribe(ctx, pcm)
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

	// First stage: the cheap gate nominates segments. It is matched loosely on
	// purpose — its open-vocabulary language model prefers "машина" over the name
	// "марина", so demanding an exact hit here is what made the bot ignore people.
	// It decides only "worth paying the good model for", never "act on this".
	gate, err := voskTranscribe(ctx, mono)
	if err != nil {
		if lvl >= sttLogCommands {
			log.Println("[stt] vosk error:", err)
		}
		return
	}
	l.decide(vc, pcm, gate, userID, ssrc, speechMs)
}

// openStream gives a speaker their own live gate connection. On failure the
// speaker falls back to the batch path for the rest of the session rather than
// retrying on every packet — a dead Vosk should degrade the bot, not flood it.
func (l *voiceListener) openStream(sp *speaker) {
	// Short: this dial sits on the receive loop, which serves every speaker. A
	// Vosk slow enough to miss it is one we are better off not waiting for — the
	// per-utterance path below still works.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// No callbacks: the stream accumulates its transcript and the receive loop
	// collects it at the utterance boundary it already decides.
	stream, err := dialVoskStream(ctx, nil, nil)
	if err != nil {
		sp.streamFailed = true
		if sttLogLevel() >= sttLogCommands {
			log.Println("[stt] vosk stream unavailable, using per-segment gate:", err)
		}
		return
	}
	sp.stream = stream
	sp.dm = downmixer{}
}

// decide runs the second half of the cascade: everything from "the cheap gate
// has spoken" onwards. Split out so the streaming gate, which already has the
// text, joins the same path instead of duplicating it.
func (l *voiceListener) decide(vc *discordgo.VoiceConnection, pcm []int16, gate, userID string, ssrc uint32, speechMs int) {
	lvl := sttLogLevel()

	armed := l.isArmed(ssrc)
	near := containsNearWake(gate)
	if lvl >= sttLogAll {
		log.Printf("[stt] user=%s speechMs=%d VOSK=%q near=%v armed=%v", userID, speechMs, gate, near, armed)
	}
	if !near && !armed {
		return // nothing like the name, and no command is expected -> stop here
	}
	if l.chID() == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Vosk-only: no stronger model to appeal to, so the gate's own strict reading
	// is the verdict and the loose match above cannot be confirmed.
	if voskOnly() {
		l.applyVoskOnly(vc, userID, ssrc, gate, armed, lvl)
		return
	}

	raw, err := transcribe(ctx, pcm)
	if err != nil {
		if lvl >= sttLogCommands {
			log.Println("[stt] transcribe error:", err)
		}
		return
	}
	command := cleanTranscript(raw)
	if lvl >= sttLogAll {
		log.Printf("[stt] user=%s STT=%q command=%q", userID, raw, command)
	}
	if command == "" {
		return
	}

	// Second stage: the accurate transcript decides. Everything the gate merely
	// misheard as the name ("включи Машину времени") is rejected here, having cost
	// one transcription and no wrong action. Skipped when armed — in a follow-up
	// the name was said in the previous segment, so the command stands alone.
	if !armed {
		after, wake := stripWakeWord(command)
		if !wake {
			if lvl >= sttLogCommands {
				log.Printf("[stt] user=%s gate false alarm: VOSK=%q -> STT=%q", userID, gate, command)
			}
			return
		}
		if after == "" {
			// Just the name -> listen for the command in the next segment (<=10s).
			l.arm(ssrc)
			if lvl >= sttLogCommands {
				log.Printf("[stt] user=%s wake word heard, waiting for command", userID)
			}
			return
		}
	}

	l.disarm(ssrc)
	l.handleAI(vc, userID, command)
}

// applyVoskOnly is the STT_VOSK_ONLY path: Vosk produces the command text itself,
// so there is no accurate model to confirm the wake word with and the strict
// match has to be trusted. Kept as a fallback for running without a command model.
func (l *voiceListener) applyVoskOnly(vc *discordgo.VoiceConnection, userID string, ssrc uint32, gate string, armed bool, lvl int) {
	voskCmd, wake := stripWakeWord(gate)
	if !wake && !armed {
		return
	}
	if wake && voskCmd == "" {
		l.arm(ssrc)
		if lvl >= sttLogCommands {
			log.Printf("[stt] user=%s wake word heard, waiting for command", userID)
		}
		return
	}
	l.disarm(ssrc)

	command := cleanTranscript(gate)
	if lvl >= sttLogCommands {
		log.Printf("[stt] user=%s VOSK command=%q", userID, command)
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
	p := playerManager.Get(l.session, vc, vc.GuildID, l.chID())
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
			"volume":      p.Volume(),
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
		ChannelID: l.chID(),
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
