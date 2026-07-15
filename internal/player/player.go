package player

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"discordAudio/internal/aiService"
	"discordAudio/internal/logger"
	"discordAudio/internal/music"
	"discordAudio/internal/stream"

	"github.com/bwmarrin/discordgo"
)

// duckedGain is the extra music volume multiplier while the AI is "thinking".
const duckedGain = 0.25

// Volume scale: 1-10, default 5. The gain multiplier is level/10, so 10 is the
// source volume and there is headroom to turn up from the default.
const (
	defaultVolume = 5
	minVolume     = 1
	maxVolume     = 10
)

// TTS stream format produced by the AI service /tts endpoint: raw headerless
// s16le PCM. OpenAI (gpt-4o-mini-tts, TTS_FORMAT=pcm) emits 24kHz mono; the old
// Piper voice was 22050. Must match the service or playback shifts in pitch/speed.
const (
	ttsSampleRate = 24000
	ttsChannels   = 1
)

func djBreakEvery() int {
	if v := strings.TrimSpace(os.Getenv("DJ_BREAK_EVERY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 3
}

type cmdKind int

const (
	cmdAgent cmdKind = iota // apply an agent result (speak + action + tracks)
	cmdSkip
	cmdStop
	cmdSnapshot
)

type snapshot struct {
	nowPlaying string
	queue      []string
}

type command struct {
	kind          cmdKind
	speak         string
	display       string
	clarification string
	action        string
	tracks        []aiService.Track
	reply         chan snapshot
}

// preempts reports whether the command should interrupt whatever is currently
// playing. Only starting new music (play/replace) or skip/stop interrupts;
// enqueue, pause/resume, and clarify/none keep the current track going (so the
// AI "doing nothing" leaves the music untouched).
func (c command) preempts() bool {
	switch c.kind {
	case cmdSkip, cmdStop:
		return true
	case cmdAgent:
		switch c.action {
		case aiService.ActionPlay, aiService.ActionReplaceQueue, aiService.ActionSkip, aiService.ActionStop:
			return true
		}
		return false
	default:
		return false
	}
}

// Player owns the queue and playback for a single guild. All queue mutation
// happens on its run goroutine; callers interact only through the command
// channel, so no locks are needed on the queue itself.
type Player struct {
	guildID string
	session *discordgo.Session
	ai      *aiService.Client

	cmdCh chan command
	done  chan struct{}

	// Live playback controls, read by the streaming goroutine.
	duckDepth atomic.Int32 // >0 while the AI is thinking -> music is ducked
	paused    atomic.Bool  // playback held (position preserved)
	volume    atomic.Int32 // 1-10 user volume level

	// binding (vc/channel) can change on rejoin -> guarded.
	bmu       sync.RWMutex
	vc        *discordgo.VoiceConnection
	channelID string

	// run-loop-owned state:
	queue        []aiService.Track
	pending      []string // spoken lines to play before the next track
	nowPlaying   aiService.Track
	sinceBreak   int
	djEvery      int
	autoplay     bool // when the queue empties, ask the agent for more
	emptyRefills int  // consecutive autoplay refills without a track playing
}

// maxEmptyRefills caps autoplay attempts that never result in a playable track,
// so a string of failed resolves can't spin the loop forever.
const maxEmptyRefills = 3

func newPlayer(s *discordgo.Session, vc *discordgo.VoiceConnection, guildID, channelID string) *Player {
	p := &Player{
		guildID:   guildID,
		session:   s,
		ai:        aiService.NewClient(),
		cmdCh:     make(chan command, 8),
		done:      make(chan struct{}),
		vc:        vc,
		channelID: channelID,
		djEvery:   djBreakEvery(),
	}
	p.volume.Store(defaultVolume)
	go p.run()
	return p
}

func (p *Player) bind(vc *discordgo.VoiceConnection, channelID string) {
	p.bmu.Lock()
	defer p.bmu.Unlock()
	p.vc = vc
	if channelID != "" {
		p.channelID = channelID
	}
}

func (p *Player) conn() *discordgo.VoiceConnection {
	p.bmu.RLock()
	defer p.bmu.RUnlock()
	return p.vc
}

func (p *Player) chID() string {
	p.bmu.RLock()
	defer p.bmu.RUnlock()
	return p.channelID
}

// ---- live playback controls (safe from any goroutine) ----

// Duck/Unduck lower the music volume while the AI is thinking. They nest, so
// concurrent requests don't un-duck early; music restores when all clear.
func (p *Player) Duck() { p.duckDepth.Add(1) }
func (p *Player) Unduck() {
	if p.duckDepth.Add(-1) < 0 {
		p.duckDepth.Store(0)
	}
}

// gain is the current music volume multiplier, read per frame by the streamer:
// the user volume level (1-10 -> 0.1-1.0), further reduced while ducking.
func (p *Player) gain() float64 {
	g := float64(p.Volume()) / float64(maxVolume)
	if p.duckDepth.Load() > 0 {
		g *= duckedGain
	}
	return g
}

// Volume returns the current volume level (1-10).
func (p *Player) Volume() int {
	v := int(p.volume.Load())
	if v < minVolume {
		return defaultVolume
	}
	return v
}

// SetVolume sets the volume level, clamped to 1-10, and returns the applied level.
func (p *Player) SetVolume(level int) int {
	if level < minVolume {
		level = minVolume
	} else if level > maxVolume {
		level = maxVolume
	}
	p.volume.Store(int32(level))
	return level
}

func (p *Player) isPaused() bool { return p.paused.Load() }

// SetPaused pauses/resumes music playback (position is preserved).
func (p *Player) SetPaused(v bool) { p.paused.Store(v) }

// TogglePause flips pause and returns the new state.
func (p *Player) TogglePause() bool {
	v := !p.paused.Load()
	p.paused.Store(v)
	return v
}

// ---- public command API ----

func (p *Player) send(c command) {
	select {
	case p.cmdCh <- c:
	case <-p.done:
	}
}

// ApplyAgent submits an agent result to the player, reducing its tool calls (or
// the legacy action) to a single queue/transport effect.
func (p *Player) ApplyAgent(r *aiService.AgentResponse) {
	// Volume is an independent, atomic control — apply it directly (it takes
	// effect on the currently playing stream within a frame).
	if d := r.VolumeDelta(); d != 0 {
		p.SetVolume(p.Volume() + d)
	}
	action, tracks := r.PrimaryEffect()
	p.send(command{
		kind:          cmdAgent,
		speak:         r.SpokenAnswer,
		display:       r.DisplayText,
		clarification: r.Clarification,
		action:        action,
		tracks:        tracks,
	})
}

// Enqueue appends tracks to the queue (used by the direct /play command).
func (p *Player) Enqueue(tracks []aiService.Track) {
	p.send(command{kind: cmdAgent, action: aiService.ActionEnqueue, tracks: tracks})
}

func (p *Player) Skip() { p.send(command{kind: cmdSkip}) }
func (p *Player) Stop() { p.send(command{kind: cmdStop}) }

// Snapshot returns the current track title and the queued titles.
func (p *Player) Snapshot() (string, []string) {
	reply := make(chan snapshot, 1)
	p.send(command{kind: cmdSnapshot, reply: reply})
	select {
	case s := <-reply:
		return s.nowPlaying, s.queue
	case <-p.done:
		return "", nil
	}
}

// ---- run loop ----

func (p *Player) run() {
	defer close(p.done)
	for {
		// 1. Speak any pending lines (acks / DJ breaks) first, interruptibly.
		if len(p.pending) > 0 {
			line := p.pending[0]
			p.pending = p.pending[1:]
			if pc := p.playTTS(line); pc != nil {
				p.handle(*pc)
			}
			continue
		}

		// 2. DJ break due and there is something queued to bridge to.
		if len(p.queue) > 0 && p.djEvery > 0 && p.sinceBreak >= p.djEvery {
			p.sinceBreak = 0
			if line := p.djLine(); line != "" {
				p.pending = append(p.pending, line)
			}
			continue
		}

		// 3. Queue empty: in autoplay, ask the agent for the next batch + a
		//    comment; otherwise wait for a command.
		if len(p.queue) == 0 {
			if p.autoplay {
				if p.emptyRefills < maxEmptyRefills && p.requestMore() {
					continue
				}
				p.autoplay = false // give up auto-continue; wait for the user
			}
			p.handle(<-p.cmdCh)
			continue
		}

		// 4. Play the next track.
		track := p.queue[0]
		p.queue = p.queue[1:]
		p.nowPlaying = track

		url, err := music.GetStreamURL(track.ID)
		if err != nil {
			p.announce(fmt.Sprintf("⚠️ Не удалось получить поток: %s", track.Title))
			continue
		}
		p.emptyRefills = 0 // a track resolved, autoplay is healthy again

		p.announce("🎧 Играем: " + display(track))
		if pc := p.playURL(url); pc != nil {
			p.handle(*pc)
			continue
		}
		p.sinceBreak++
	}
}

// handle applies a command's effect on queue/state. Called from the idle wait
// and after a preempting command interrupted playback.
func (p *Player) handle(c command) {
	switch c.kind {
	case cmdSnapshot:
		c.reply <- p.snapshotNow()
	case cmdSkip:
		// Current playback was already cancelled; the loop advances.
	case cmdStop:
		p.queue = nil
		p.pending = nil
		p.sinceBreak = 0
		p.autoplay = false
		p.nowPlaying = aiService.Track{}
	case cmdAgent:
		p.applyAgent(c)
	}
}

func (p *Player) applyAgent(c command) {
	switch c.action {
	case aiService.ActionReplaceQueue:
		p.queue = append([]aiService.Track{}, c.tracks...)
		p.sinceBreak = 0
	case aiService.ActionPlay:
		// Play now: jump these to the front of the queue.
		p.queue = append(append([]aiService.Track{}, c.tracks...), p.queue...)
		p.sinceBreak = 0
	case aiService.ActionEnqueue:
		p.queue = append(p.queue, c.tracks...)
	case aiService.ActionSkip:
		// Current playback was already cancelled (preempt); the loop advances.
	case aiService.ActionStop:
		p.queue = nil
		p.pending = nil
		p.sinceBreak = 0
		p.autoplay = false
		p.nowPlaying = aiService.Track{}
	case aiService.ActionPause:
		p.SetPaused(true)
	case aiService.ActionResume:
		p.SetPaused(false)
	case aiService.ActionClarify, aiService.ActionNone:
		// no queue change
	}
	// Any user request that brought music (re)arms autoplay and resumes.
	if len(c.tracks) > 0 {
		p.autoplay = true
		p.emptyRefills = 0
		p.SetPaused(false)
	}
	if s := strings.TrimSpace(c.speak); s != "" {
		p.pending = append(p.pending, s)
	}
	// Announce the display text, falling back to the clarification question when
	// there is no display text (in tool-calling mode a question arrives with no
	// action and just a clarification). Single place this is posted.
	msg := strings.TrimSpace(c.display)
	if msg == "" {
		msg = strings.TrimSpace(c.clarification)
	}
	if msg != "" {
		p.announce(msg)
	}
}

// playURL / playTTS run an item to completion in a child goroutine while the
// loop stays responsive to commands. They return a preempting command if one
// arrived, or nil if the item finished on its own.
func (p *Player) playURL(url string) *command {
	return p.playItem(func(ctx context.Context) error {
		return stream.StartStreaming(ctx, p.conn(), url, stream.Controls{
			Gain:   p.gain,
			Paused: p.isPaused,
		})
	})
}

func (p *Player) playTTS(text string) *command {
	return p.playItem(func(ctx context.Context) error {
		audio, err := p.ai.Tts(ctx, text)
		if err != nil {
			return err
		}
		defer audio.Close()
		// The AI's own voice plays at full volume and ignores pause.
		return stream.StartStreamingPCMReader(ctx, p.conn(), audio, ttsSampleRate, ttsChannels, stream.Controls{})
	})
}

func (p *Player) playItem(play func(ctx context.Context) error) *command {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- play(ctx) }()

	for {
		select {
		case err := <-done:
			if err != nil && err != context.Canceled {
				logger.Send(fmt.Sprintf("player playback error (guild %s): %v", p.guildID, err))
			}
			return nil
		case c := <-p.cmdCh:
			if c.kind == cmdSnapshot {
				c.reply <- p.snapshotNow()
				continue
			}
			if !c.preempts() {
				p.applyAgent(c) // enqueue: keep playing, just grow the queue
				continue
			}
			cancel()
			<-done
			return &c
		}
	}
}

func (p *Player) snapshotNow() snapshot {
	titles := make([]string, 0, len(p.queue))
	for _, t := range p.queue {
		titles = append(titles, t.Title)
	}
	return snapshot{nowPlaying: p.nowPlaying.Title, queue: titles}
}

// djLine asks the agent for a short spoken DJ transition. Runs on the loop
// goroutine, so reading queue/nowPlaying here is safe.
func (p *Player) djLine() string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := p.ai.Agent(ctx, aiService.AgentRequest{
		Session: aiService.AgentSession{GuildID: p.guildID, ChannelID: p.chID()},
		Message: "[dj_break] Say one short, upbeat DJ transition line to bridge into the next song. " +
			"Do not search and do not change the queue.",
		Context: map[string]any{
			"now_playing": p.nowPlaying.Title,
			"queue_len":   len(p.queue),
		},
	})
	if err != nil {
		logger.Send(fmt.Sprintf("dj break error (guild %s): %v", p.guildID, err))
		return ""
	}
	return resp.SpokenAnswer
}

// requestMore asks the agent for the next batch of tracks plus a spoken comment
// when the queue runs dry. Returns true if it enqueued anything. Runs on the
// loop goroutine, so touching queue/pending here is safe.
func (p *Player) requestMore() bool {
	p.emptyRefills++

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := p.ai.Agent(ctx, aiService.AgentRequest{
		Session: aiService.AgentSession{GuildID: p.guildID, ChannelID: p.chID()},
		Message: "[autoplay] The queue just ended. Give a short spoken DJ comment and " +
			"pick the next set of tracks to keep the same vibe going.",
		Context: map[string]any{
			"last_played": p.nowPlaying.Title,
			"queue_len":   0,
		},
	})
	if err != nil {
		logger.Send(fmt.Sprintf("autoplay error (guild %s): %v", p.guildID, err))
		return false
	}

	if len(resp.Tracks) == 0 {
		return false
	}

	p.queue = append(p.queue, resp.Tracks...)
	p.sinceBreak = 0 // the autoplay comment counts as the break
	if s := strings.TrimSpace(resp.SpokenAnswer); s != "" {
		p.pending = append(p.pending, s)
	}
	if d := strings.TrimSpace(resp.DisplayText); d != "" {
		p.announce(d)
	}
	return true
}

func (p *Player) announce(text string) {
	ch := p.chID()
	if ch == "" || strings.TrimSpace(text) == "" {
		return
	}
	_, _ = p.session.ChannelMessageSend(ch, text)
}

func display(t aiService.Track) string {
	if t.URL != "" {
		if t.Uploader != "" {
			return fmt.Sprintf("[%s](%s) — %s", t.Title, t.URL, t.Uploader)
		}
		return fmt.Sprintf("[%s](%s)", t.Title, t.URL)
	}
	if t.Title != "" {
		return t.Title
	}
	return t.ID
}
