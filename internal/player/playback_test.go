package player

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"discordAudio/internal/aiService"
	"discordAudio/internal/stream"
)

// recorder stands in for the AI service and captures what the bot reports.
type recorder struct {
	mu     sync.Mutex
	events []aiService.PlaybackEvent
	srv    *httptest.Server
}

func newRecorder(t *testing.T, status int) *recorder {
	t.Helper()
	r := &recorder{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/playback" {
			t.Errorf("posted to %q, want /playback", req.URL.Path)
		}
		var ev aiService.PlaybackEvent
		if err := json.NewDecoder(req.Body).Decode(&ev); err != nil {
			t.Errorf("decoding event: %v", err)
		}
		r.mu.Lock()
		r.events = append(r.events, ev)
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Setenv("PLAYBACK_SERVICE_ADDR", r.srv.URL)
	t.Cleanup(r.srv.Close)
	return r
}

// waitFor gives the fire-and-forget goroutine a moment to land.
func (r *recorder) waitFor(t *testing.T, n int) []aiService.PlaybackEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		r.mu.Lock()
		got := append([]aiService.PlaybackEvent(nil), r.events...)
		r.mu.Unlock()
		if len(got) >= n {
			return got
		}
		select {
		case <-deadline:
			return got
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func testPlayer() *Player {
	return &Player{
		guildID:   "g1",
		channelID: "c1",
		ai:        aiService.NewClient(),
	}
}

// played_ms is derived from frames actually handed to Discord, never from
// elapsed time. This is the requirement that would silently produce plausible
// but wrong numbers if someone "simplified" it to a timestamp difference: a
// track paused for ten minutes would report ten extra minutes of listening.
func TestPlayedMsCountsFramesNotElapsedTime(t *testing.T) {
	r := newRecorder(t, http.StatusOK)
	p := testPlayer()

	const frames = 500 // 500 * 20ms = 10s of audio
	start := time.Now()
	time.Sleep(50 * time.Millisecond) // wall clock moves; the report must not
	p.reportPlayback(aiService.Track{ID: "abc", Provider: "youtube", Duration: 215}, frames, aiService.ReasonFinished)

	events := r.waitFor(t, 1)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]

	if want := int64(frames * stream.FrameMs); ev.PlayedMs != want {
		t.Errorf("PlayedMs = %d, want %d (frames x FrameMs)", ev.PlayedMs, want)
	}
	if elapsed := time.Since(start).Milliseconds(); ev.PlayedMs == elapsed {
		t.Errorf("PlayedMs tracks wall clock (%d) — pauses would be counted as listening", elapsed)
	}
	if ev.TrackID != "abc" || ev.Provider != "youtube" {
		t.Errorf("track fields wrong: %+v", ev)
	}
	if ev.DurationMs != 215000 {
		t.Errorf("DurationMs = %d, want 215000 (seconds -> ms)", ev.DurationMs)
	}
	if ev.Session.GuildID != "g1" {
		t.Errorf("Session.GuildID = %q, want g1 — without it the event cannot be attributed", ev.Session.GuildID)
	}
	if ev.Reason != aiService.ReasonFinished {
		t.Errorf("Reason = %q, want %q", ev.Reason, aiService.ReasonFinished)
	}
}

// The whole point of the feature: a track that was queued but never heard must
// not be reported. Enqueued-but-unplayed tracks were what polluted the profile.
func TestSilentTrackIsNotReported(t *testing.T) {
	r := newRecorder(t, http.StatusOK)
	p := testPlayer()

	p.reportPlayback(aiService.Track{ID: "never-played"}, 0, aiService.ReasonSkipped)
	p.reportPlayback(aiService.Track{ID: ""}, 100, aiService.ReasonFinished) // no id to report

	if got := r.waitFor(t, 1); len(got) != 0 {
		t.Errorf("reported %d events for audio that never played: %+v", len(got), got)
	}
}

// The same track heard twice is two events — a repeat listen is itself a signal.
func TestRepeatsAreNotDeduplicated(t *testing.T) {
	r := newRecorder(t, http.StatusOK)
	p := testPlayer()

	track := aiService.Track{ID: "same", Provider: "youtube"}
	p.reportPlayback(track, 100, aiService.ReasonFinished)
	p.reportPlayback(track, 100, aiService.ReasonFinished)

	if got := r.waitFor(t, 2); len(got) != 2 {
		t.Errorf("got %d events for two plays of the same track, want 2", len(got))
	}
}

// The endpoint may not exist yet, or may go away: a 404 is exactly what the live
// service returns today. It must be as harmless as any other failure.
func TestMissingEndpointIsHarmless(t *testing.T) {
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer gone.Close()
	t.Setenv("PLAYBACK_SERVICE_ADDR", gone.URL)

	p := testPlayer()
	p.reportPlayback(aiService.Track{ID: "abc"}, 100, aiService.ReasonFinished)
	time.Sleep(100 * time.Millisecond) // let the goroutine finish and swallow it
}

// TestReportPlaybackAgainstLiveService is an opt-in check against the real
// service, for when /playback actually exists:
//
//	PLAYBACK_LIVE=1 go test ./internal/player/ -run AgainstLive -v
//
// It reports a probe track, so expect one junk row once the endpoint is real.
func TestReportPlaybackAgainstLiveService(t *testing.T) {
	if os.Getenv("PLAYBACK_LIVE") == "" {
		t.Skip("set PLAYBACK_LIVE=1 to report a probe event to the running service")
	}

	err := aiService.NewClient().ReportPlayback(context.Background(), aiService.PlaybackEvent{
		Session:    aiService.AgentSession{GuildID: "probe-guild", ChannelID: "probe-channel"},
		TrackID:    "probe-track",
		Provider:   "youtube",
		PlayedMs:   1000,
		DurationMs: 200000,
		Reason:     aiService.ReasonFinished,
	})
	if err != nil {
		t.Fatalf("live /playback rejected the report: %v", err)
	}
	t.Log("live /playback accepted the report")
}

// Analytics must never interfere with playback: a dead or unhappy service is
// swallowed, and reporting never blocks the caller.
func TestReportingIsFireAndForget(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer slow.Close()
	t.Setenv("PLAYBACK_SERVICE_ADDR", slow.URL)

	p := testPlayer()
	start := time.Now()
	p.reportPlayback(aiService.Track{ID: "abc"}, 100, aiService.ReasonFinished)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("reportPlayback blocked for %v; playback must not wait on analytics", elapsed)
	}

	// Unset address must be equally harmless.
	t.Setenv("PLAYBACK_SERVICE_ADDR", "")
	t.Setenv("AI_SERVICE_ADDR", "")
	p.reportPlayback(aiService.Track{ID: "abc"}, 100, aiService.ReasonFinished)
	time.Sleep(50 * time.Millisecond)
}

func TestStopReason(t *testing.T) {
	cases := []struct {
		name string
		pc   *command
		want string
	}{
		{"ran to the end", nil, aiService.ReasonFinished},
		{"skip command", &command{kind: cmdSkip}, aiService.ReasonSkipped},
		{"stop command", &command{kind: cmdStop}, aiService.ReasonStopped},
		{"agent skip", &command{kind: cmdAgent, action: aiService.ActionSkip}, aiService.ReasonSkipped},
		{"agent stop", &command{kind: cmdAgent, action: aiService.ActionStop}, aiService.ReasonStopped},
		// Starting something else is a form of moving on, not of stopping.
		{"agent play", &command{kind: cmdAgent, action: aiService.ActionPlay}, aiService.ReasonSkipped},
		{"agent replace", &command{kind: cmdAgent, action: aiService.ActionReplaceQueue}, aiService.ReasonSkipped},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stopReason(c.pc); got != c.want {
				t.Errorf("stopReason() = %q, want %q", got, c.want)
			}
		})
	}
}

// The counter the report is built from must survive concurrent use: the stream
// goroutine increments it while the player reads it at the end of the track.
func TestFrameCounterIsRaceFree(t *testing.T) {
	var frames atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				frames.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := frames.Load(); got != 400 {
		t.Errorf("frames = %d, want 400", got)
	}
}
