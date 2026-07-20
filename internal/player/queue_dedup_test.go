package player

import (
	"testing"

	"discordAudio/internal/aiService"
)

// The five tracks from the reported session, in order.
func sessionTracks() []aiService.Track {
	return []aiService.Track{
		{ID: "B402rKl4bUg", Title: "the cure"},
		{ID: "a4tdS3IB294", Title: "Earrings"},
		{ID: "78wrful9cVU", Title: "drop dead"},
		{ID: "v1t4MTqdfyI", Title: "hate that i made you love me"},
		{ID: "oOEGRpfitAg", Title: "what's wrong"},
	}
}

func queueIDs(p *Player) []string {
	ids := make([]string, 0, len(p.queue))
	for _, t := range p.queue {
		ids = append(ids, t.ID)
	}
	return ids
}

// advance simulates what the run loop does between commands: it takes the head
// of the queue and makes it the current track. Without this the trace cannot be
// reproduced — the 4-vs-5 arithmetic only works out once a track has been
// pulled out of the queue to play.
func advance(p *Player) {
	if len(p.queue) == 0 {
		return
	}
	p.nowPlaying = p.queue[0]
	p.queue = p.queue[1:]
}

// TestReportedDuplicateTrace replays the exact session that was reported:
// play(5) -> no-op -> replace_queue(5) -> play(5), with the same five tracks
// every time. The queue must never hold the same track twice.
func TestReportedDuplicateTrace(t *testing.T) {
	p := &Player{}

	// 1. play, 5 tracks -> one plays, four queued.
	p.applyAgent(command{action: aiService.ActionPlay, tracks: sessionTracks()})
	advance(p)
	if len(p.queue) != 4 {
		t.Fatalf("step 1: queue_len = %d, want 4 (%v)", len(p.queue), queueIDs(p))
	}

	// 2. a reply with no tool calls changes nothing.
	p.applyAgent(command{action: aiService.ActionNone})
	if len(p.queue) != 4 {
		t.Fatalf("step 2: queue_len = %d, want 4 (%v)", len(p.queue), queueIDs(p))
	}

	// 3. replace_queue with the same five: the queue is rebuilt, not appended to.
	p.applyAgent(command{action: aiService.ActionReplaceQueue, tracks: sessionTracks()})
	advance(p)
	if len(p.queue) != 4 {
		t.Fatalf("step 3: replace_queue left queue_len = %d, want 4 (%v)", len(p.queue), queueIDs(p))
	}

	// 4. play with the same five again. This is where the report saw 8.
	p.applyAgent(command{action: aiService.ActionPlay, tracks: sessionTracks()})
	advance(p)
	if len(p.queue) != 4 {
		t.Errorf("step 4: queue_len = %d, want 4 — the same tracks were added twice (%v)",
			len(p.queue), queueIDs(p))
	}
	assertNoDuplicates(t, p)
}

func assertNoDuplicates(t *testing.T, p *Player) {
	t.Helper()
	seen := map[string]bool{p.nowPlaying.ID: p.nowPlaying.ID != ""}
	for _, tr := range p.queue {
		if tr.ID == "" {
			continue
		}
		if seen[tr.ID] {
			t.Errorf("track %q (%s) appears twice in now_playing+queue: %v",
				tr.Title, tr.ID, queueIDs(p))
		}
		seen[tr.ID] = true
	}
}

// enqueue is the other way tracks enter the queue, and it has the same hazard.
func TestEnqueueDoesNotDuplicate(t *testing.T) {
	p := &Player{}

	p.applyAgent(command{action: aiService.ActionEnqueue, tracks: sessionTracks()})
	p.applyAgent(command{action: aiService.ActionEnqueue, tracks: sessionTracks()})

	if len(p.queue) != 5 {
		t.Errorf("queue_len = %d after enqueueing the same five twice, want 5 (%v)",
			len(p.queue), queueIDs(p))
	}
	assertNoDuplicates(t, p)
}

// A track that is currently playing must not be re-queued behind itself: the
// listener would hear it twice in a row, and /playback would report two plays.
func TestNowPlayingIsNotRequeued(t *testing.T) {
	p := &Player{nowPlaying: aiService.Track{ID: "B402rKl4bUg", Title: "the cure"}}

	p.applyAgent(command{action: aiService.ActionEnqueue, tracks: sessionTracks()})

	for _, tr := range p.queue {
		if tr.ID == p.nowPlaying.ID {
			t.Errorf("the currently playing track was queued again: %v", queueIDs(p))
		}
	}
}

// replace_queue must genuinely replace. The trace showed it behaving, and this
// pins that down so a dedup change cannot quietly turn it into a merge.
func TestReplaceQueueClearsFirst(t *testing.T) {
	p := &Player{queue: []aiService.Track{
		{ID: "old-1", Title: "old one"},
		{ID: "old-2", Title: "old two"},
	}}

	p.applyAgent(command{action: aiService.ActionReplaceQueue, tracks: sessionTracks()})

	if len(p.queue) != 5 {
		t.Fatalf("queue_len = %d, want exactly the 5 new tracks (%v)", len(p.queue), queueIDs(p))
	}
	for _, tr := range p.queue {
		if tr.ID == "old-1" || tr.ID == "old-2" {
			t.Errorf("replace_queue kept a previous track: %v", queueIDs(p))
		}
	}
}

// Distinct tracks must still all get in — dedup must not collapse a real batch.
func TestDistinctTracksAllQueue(t *testing.T) {
	p := &Player{}

	p.applyAgent(command{action: aiService.ActionEnqueue, tracks: sessionTracks()})

	if len(p.queue) != 5 {
		t.Errorf("queue_len = %d, want all 5 distinct tracks (%v)", len(p.queue), queueIDs(p))
	}
}
