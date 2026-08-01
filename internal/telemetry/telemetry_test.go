package telemetry

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The ring must overwrite oldest-first and read back newest-first: the panel
// shows "what just happened", and an off-by-one in the wrap arithmetic would
// silently reorder history rather than fail loudly.
func TestRingWrapsAndReadsNewestFirst(t *testing.T) {
	s := New(Config{MaxEvents: 3, MaxFileBytes: 1 << 20})
	defer s.Close()

	for _, id := range []string{"1", "2", "3", "4", "5"} {
		s.Record(Event{Kind: KindSTTGate, UserID: id})
	}

	got := s.Query(Filter{})
	if len(got) != 3 {
		t.Fatalf("Query returned %d events, want 3 (the ring holds 3)", len(got))
	}
	for i, want := range []string{"5", "4", "3"} {
		if got[i].UserID != want {
			t.Errorf("event %d is user %q, want %q — order is newest-first: %v", i, got[i].UserID, want, ids(got))
		}
	}
}

// Record is called from the voice receive loop. If it ever waits on the disk
// writer, Opus packets are lost — so a full write queue must drop the event and
// return, and the in-memory ring must keep it regardless.
func TestRecordDropsInsteadOfBlocking(t *testing.T) {
	// Built by hand with no writer goroutine, so the queue genuinely fills up.
	s := &Store{ring: make([]Event, 16), writes: make(chan Event, 2)}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			s.Record(Event{Kind: KindSTTGate})
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Record blocked when the write queue was full — this stalls the voice receive loop")
	}

	if s.dropped.Load() == 0 {
		t.Error("nothing was counted as dropped, so the queue never actually filled — the test proves nothing")
	}
	if n := len(s.Query(Filter{Limit: 50})); n != 10 {
		t.Errorf("in-memory ring holds %d events, want all 10: dropping applies to the FILE, not the ring", n)
	}
}

// The file is the part that survives a crash, so it has to be complete and
// parseable one object per line.
func TestJSONLRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	s := New(Config{MaxEvents: 10, File: path, MaxFileBytes: 1 << 20})

	s.Record(Event{Kind: KindSTTGate, UserID: "u1", Gate: "марина привет", Near: true, SpeechMs: 1120})
	s.Record(Event{Kind: KindAICall, Trigger: "user", LatencyMs: 412, Outcome: OutcomeOK})
	s.Close() // flushes the queue

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening the telemetry file: %v", err)
	}
	defer f.Close()

	var got []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line is not valid JSON (%q): %v", sc.Text(), err)
		}
		got = append(got, e)
	}
	if len(got) != 2 {
		t.Fatalf("file holds %d events, want 2", len(got))
	}
	if got[0].Gate != "марина привет" || got[0].SpeechMs != 1120 || !got[0].Near {
		t.Errorf("gate event did not survive the round trip: %+v", got[0])
	}
	if got[1].LatencyMs != 412 || got[1].Trigger != "user" {
		t.Errorf("ai event did not survive the round trip: %+v", got[1])
	}
	if got[0].At.IsZero() {
		t.Error("At was not stamped — events must be timestamped even when the caller omits it")
	}
}

func TestRotationKeepsOneGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	s := New(Config{MaxEvents: 100, File: path, MaxFileBytes: 200})

	for i := 0; i < 40; i++ {
		s.Record(Event{Kind: KindSTTGate, UserID: "0123456789", Gate: "padding to push past the size cap"})
	}
	s.Close()

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("no rotated file at %s.1: %v", path, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("current file missing after rotation: %v", err)
	}
	if st.Size() == 0 {
		t.Error("current file is empty after rotation — writing did not resume")
	}
}

// Transcripts are users' speech; everyone below owner sees the decision without
// the words. The operational fields must survive, or redaction would make the
// event useless instead of merely private.
func TestRedactedKeepsTheFactsAndDropsTheWords(t *testing.T) {
	e := Event{
		Kind: KindSTTCommand, UserID: "u1", SpeechMs: 1120,
		Gate: "марина привет", Text: "Марина, привет.", Command: "привет",
		Near: true, Outcome: OutcomeDelivered,
	}
	r := e.Redacted()

	if r.Gate != "" || r.Text != "" || r.Command != "" {
		t.Errorf("transcripts survived redaction: %+v", r)
	}
	if r.SpeechMs != 1120 || r.Outcome != OutcomeDelivered || !r.Near || r.UserID != "u1" {
		t.Errorf("redaction destroyed the operational facts: %+v", r)
	}
	if e.Gate == "" {
		t.Error("Redacted mutated the original event instead of returning a copy")
	}
}

func TestFilters(t *testing.T) {
	s := New(Config{MaxEvents: 20, MaxFileBytes: 1 << 20})
	defer s.Close()

	base := time.Now().Add(-time.Hour)
	s.Record(Event{At: base, Kind: KindSTTGate, GuildID: "g1", UserID: "u1"})
	s.Record(Event{At: base.Add(time.Minute), Kind: KindAICall, GuildID: "g1", UserID: "u2"})
	s.Record(Event{At: time.Now(), Kind: KindSTTGate, GuildID: "g2", UserID: "u1"})

	cases := []struct {
		name string
		f    Filter
		want int
	}{
		{"all", Filter{}, 3},
		{"by kind", Filter{Kind: KindSTTGate}, 2},
		{"by guild", Filter{GuildID: "g1"}, 2},
		{"by user", Filter{UserID: "u1"}, 2},
		{"by kind and guild", Filter{Kind: KindSTTGate, GuildID: "g1"}, 1},
		{"since", Filter{Since: base.Add(30 * time.Minute)}, 1},
		{"limit", Filter{Limit: 2}, 2},
		{"offset skips the newest", Filter{Offset: 1}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := len(s.Query(c.f)); got != c.want {
				t.Errorf("Query(%+v) returned %d events, want %d", c.f, got, c.want)
			}
		})
	}
}

// Everything must tolerate a nil store: Record is called from the voice pipeline
// in tests and in any run where telemetry was never initialised, and a panic
// there would take the bot down over an analytics feature.
func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	s.Record(Event{Kind: KindSTTGate})
	if got := s.Query(Filter{}); got != nil {
		t.Errorf("Query on a nil store returned %v, want nil", got)
	}
	if (s.Info() != Info{}) {
		t.Error("Info on a nil store returned something")
	}

	Init(nil)
	Record(Event{Kind: KindSTTGate}) // package-level, before Init
}

func TestInfoReportsTheWindow(t *testing.T) {
	s := New(Config{MaxEvents: 2, MaxFileBytes: 1 << 20})
	defer s.Close()

	oldest := time.Now().Add(-time.Minute)
	s.Record(Event{At: oldest, Kind: KindSTTGate})
	s.Record(Event{Kind: KindSTTGate})
	s.Record(Event{Kind: KindSTTGate}) // evicts the first

	info := s.Info()
	if info.Buffered != 2 || info.Capacity != 2 {
		t.Errorf("Buffered/Capacity = %d/%d, want 2/2", info.Buffered, info.Capacity)
	}
	if info.Recorded != 3 {
		t.Errorf("Recorded = %d, want 3 — it counts everything ever recorded, not what is retained", info.Recorded)
	}
	if info.Oldest.Equal(oldest) {
		t.Error("Oldest still points at the evicted event, so the reported window is wrong")
	}
}

func ids(evs []Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.UserID)
	}
	return out
}
