package player

import (
	"context"
	"testing"
	"time"

	"discordAudio/internal/aiService"
)

// A player whose run loop never answers must time out, not hang. This is the
// whole reason State takes a context: it is called from an HTTP handler, and one
// wedged player would otherwise pin a request — and then the panel — forever.
//
// The bare &Player{} here has no run goroutine and a nil command channel, which
// is exactly the shape of "nobody is listening".
func TestStateGivesUpInsteadOfHanging(t *testing.T) {
	p := &Player{}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	var ok bool
	go func() {
		defer close(done)
		_, ok = p.State(ctx)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("State ignored its context and blocked — an admin request would hang forever")
	}
	if ok {
		t.Error("State reported success although no run loop ever answered")
	}
}

// Snapshot is the older, narrower view and three call sites outside this package
// still use it. Widening the reply must not have changed what they see.
func TestSnapshotStillProjectsTitles(t *testing.T) {
	p := &Player{
		nowPlaying: aiService.Track{ID: "a", Title: "the cure"},
		queue: []aiService.Track{
			{ID: "b", Title: "Earrings"},
			{ID: "c", Title: "drop dead"},
		},
	}

	s := p.snapshotNow()
	if s.nowPlaying.Title != "the cure" {
		t.Errorf("nowPlaying = %q, want %q", s.nowPlaying.Title, "the cure")
	}
	if len(s.queue) != 2 || s.queue[0].Title != "Earrings" || s.queue[1].Title != "drop dead" {
		t.Errorf("queue = %v, want the two queued tracks in order", s.queue)
	}
}

// snapshotNow runs on the loop goroutine and its result is read by another one.
// Handing over the live slice would let the reader see the queue being mutated
// underneath it — a data race that would surface as a rare, unreproducible wrong
// answer rather than as a crash.
func TestSnapshotCopiesTheQueue(t *testing.T) {
	p := &Player{queue: []aiService.Track{{ID: "b", Title: "Earrings"}}}

	s := p.snapshotNow()
	p.queue[0] = aiService.Track{ID: "z", Title: "replaced"}
	p.queue = append(p.queue, aiService.Track{ID: "y", Title: "appended"})

	if len(s.queue) != 1 || s.queue[0].Title != "Earrings" {
		t.Errorf("the snapshot changed when the run loop mutated its queue: %v", s.queue)
	}
}

// An empty queue must marshal as [] rather than null: the panel iterates it, and
// null is a special case every consumer would have to remember.
func TestStateQueueIsNeverNil(t *testing.T) {
	p := &Player{}
	st, _ := p.stateFrom(snapshot{})

	if st.Queue == nil {
		t.Error("State.Queue is nil; it must be an empty slice so it encodes as []")
	}
	if st.NowPlaying != nil {
		t.Error("State.NowPlaying should be omitted when nothing is playing")
	}
}

func TestStateReportsNowPlaying(t *testing.T) {
	p := &Player{guildID: "g1"}
	st, _ := p.stateFrom(snapshot{
		nowPlaying: aiService.Track{ID: "a", Title: "the cure"},
		queue:      []aiService.Track{{ID: "b", Title: "Earrings"}},
	})

	if st.NowPlaying == nil || st.NowPlaying.Title != "the cure" {
		t.Fatalf("NowPlaying = %+v, want the current track", st.NowPlaying)
	}
	if st.QueueLen != 1 {
		t.Errorf("QueueLen = %d, want 1", st.QueueLen)
	}
	if st.GuildID != "g1" {
		t.Errorf("GuildID = %q, want g1", st.GuildID)
	}
}
