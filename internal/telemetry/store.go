package telemetry

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMaxEvents = 2000

	// defaultMaxFileBytes bounds the JSONL before it rotates. The VPS this runs
	// on has 30GB of disk shared with five containers and their images, so the
	// history is deliberately small: it is a debugging aid, not an archive.
	defaultMaxFileBytes = 32 << 20 // 32MB

	// writeQueue is how many events may be waiting for the disk. Sized for a
	// burst (a busy channel produces a few events per second at most); when it
	// fills, Record drops rather than blocks — see the package comment.
	writeQueue = 256
)

// Config is resolved once at startup. Unlike most settings in this project it is
// not re-read per call: the ring is allocated from MaxEvents and the file handle
// is held open, so changing either means building a new Store.
type Config struct {
	MaxEvents    int
	File         string // empty: memory only, nothing is written to disk
	MaxFileBytes int64
}

// ConfigFromEnv reads TELEMETRY_MAX_EVENTS, TELEMETRY_FILE and
// TELEMETRY_MAX_FILE_MB, falling back to the defaults on anything unparseable.
// A bad value must not disable telemetry silently, so it is logged and ignored.
func ConfigFromEnv() Config {
	cfg := Config{
		MaxEvents:    defaultMaxEvents,
		File:         strings.TrimSpace(os.Getenv("TELEMETRY_FILE")),
		MaxFileBytes: defaultMaxFileBytes,
	}
	if v := strings.TrimSpace(os.Getenv("TELEMETRY_MAX_EVENTS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxEvents = n
		} else {
			log.Printf("[telemetry] TELEMETRY_MAX_EVENTS=%q is not a positive number, using %d", v, cfg.MaxEvents)
		}
	}
	if v := strings.TrimSpace(os.Getenv("TELEMETRY_MAX_FILE_MB")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxFileBytes = int64(n) << 20
		} else {
			log.Printf("[telemetry] TELEMETRY_MAX_FILE_MB=%q is not a positive number, using %dMB", v, cfg.MaxFileBytes>>20)
		}
	}
	return cfg
}

// Store holds the recent events in memory and, optionally, appends them to a
// JSONL file. The in-memory ring is what the admin API serves; the file exists
// so a restart — or a crash, which is exactly when the history matters — does
// not take the evidence with it.
type Store struct {
	mu   sync.RWMutex
	ring []Event
	head int // next write position
	n    int // filled entries, <= len(ring)

	recorded atomic.Int64
	dropped  atomic.Int64

	writes chan Event
	wg     sync.WaitGroup

	// Owned by the writer goroutine alone, so no lock.
	cfg  Config
	file *os.File
	size int64
}

// New builds a Store and starts its disk writer. A file that cannot be opened is
// reported once and then ignored: losing the on-disk copy must not cost us the
// in-memory history as well, and it must certainly not stop the bot.
func New(cfg Config) *Store {
	if cfg.MaxEvents <= 0 {
		cfg.MaxEvents = defaultMaxEvents
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = defaultMaxFileBytes
	}

	s := &Store{
		ring:   make([]Event, cfg.MaxEvents),
		writes: make(chan Event, writeQueue),
		cfg:    cfg,
	}

	if cfg.File != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.File), 0o755); err != nil {
			log.Printf("[telemetry] cannot create directory for %s: %v (memory only)", cfg.File, err)
		} else if f, err := os.OpenFile(cfg.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err != nil {
			log.Printf("[telemetry] cannot open %s: %v (memory only)", cfg.File, err)
		} else {
			s.file = f
			if st, err := f.Stat(); err == nil {
				s.size = st.Size()
			}
		}
	}

	s.wg.Add(1)
	go s.writeLoop()
	return s
}

// Close stops the writer and flushes what is already queued.
func (s *Store) Close() {
	close(s.writes)
	s.wg.Wait()
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

// Record files an event. It never blocks: the ring write is a short lock, and
// the disk hand-off is a non-blocking send that drops the event if the writer
// has fallen behind. Callers are on the voice receive loop, where waiting means
// losing Opus packets — a lost telemetry line is much cheaper than that.
func (s *Store) Record(ev Event) {
	if s == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}

	s.mu.Lock()
	s.ring[s.head] = ev
	s.head = (s.head + 1) % len(s.ring)
	if s.n < len(s.ring) {
		s.n++
	}
	s.mu.Unlock()
	s.recorded.Add(1)

	select {
	case s.writes <- ev:
	default:
		s.dropped.Add(1)
	}
}

// Query returns matching events, newest first. The window is the ring, not all
// of history — Info reports how far back it actually reaches, so a caller can
// say so honestly rather than implying it counted everything.
func (s *Store) Query(f Filter) []Event {
	if s == nil {
		return nil
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	size := len(s.ring)
	out := make([]Event, 0, min(limit, s.n))
	skipped := 0
	for i := 0; i < s.n; i++ {
		e := s.ring[((s.head-1-i)%size+size)%size]
		if !f.matches(e) {
			continue
		}
		if skipped < f.Offset {
			skipped++
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Info describes the buffer itself. Dropped is not an error to hide: it says the
// disk writer fell behind, which is the one thing that would make the JSONL an
// incomplete record of the ring.
type Info struct {
	Buffered int       `json:"buffered"`
	Capacity int       `json:"capacity"`
	Recorded int64     `json:"recorded"`
	Dropped  int64     `json:"dropped_to_disk"`
	Oldest   time.Time `json:"oldest,omitempty"`
	File     string    `json:"file,omitempty"`
}

func (s *Store) Info() Info {
	if s == nil {
		return Info{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	info := Info{
		Buffered: s.n,
		Capacity: len(s.ring),
		Recorded: s.recorded.Load(),
		Dropped:  s.dropped.Load(),
		File:     s.cfg.File,
	}
	if s.n > 0 {
		size := len(s.ring)
		info.Oldest = s.ring[((s.head-s.n)%size+size)%size].At
	}
	return info
}

func (s *Store) writeLoop() {
	defer s.wg.Done()
	for ev := range s.writes {
		if s.file == nil {
			continue // memory-only: drain the channel so Record never blocks
		}
		s.appendLine(ev)
	}
}

// appendLine writes one JSON object per line, unbuffered. Buffering would be
// faster and would lose the tail on a crash — which is precisely the history
// worth having. Events arrive a few per second at most, so the cost is noise.
func (s *Store) appendLine(ev Event) {
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	line = append(line, '\n')

	if s.size+int64(len(line)) > s.cfg.MaxFileBytes {
		s.rotate()
	}
	n, err := s.file.Write(line)
	s.size += int64(n)
	if err != nil {
		log.Printf("[telemetry] write failed, giving up on the file: %v", err)
		_ = s.file.Close()
		s.file = nil
	}
}

// rotate keeps exactly one previous generation. More would need a retention
// policy, and this file is a debugging aid on a small disk, not an archive.
func (s *Store) rotate() {
	if err := s.file.Close(); err != nil {
		log.Printf("[telemetry] closing %s before rotation: %v", s.cfg.File, err)
	}
	if err := os.Rename(s.cfg.File, s.cfg.File+".1"); err != nil {
		log.Printf("[telemetry] cannot rotate %s: %v", s.cfg.File, err)
	}

	f, err := os.OpenFile(s.cfg.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("[telemetry] cannot reopen %s after rotation: %v (memory only)", s.cfg.File, err)
		s.file = nil
		return
	}
	s.file, s.size = f, 0
}

// ---- package-level default ----

// def is the store the bot records into. It is a package global for the same
// reason the track cache and player manager are: the call sites are deep inside
// the voice pipeline, and threading a store through every one of them would add
// a parameter to a dozen functions that have nothing else to do with it.
//
// An atomic pointer rather than a plain var so tests can swap it without racing
// the goroutines that record.
var def atomic.Pointer[Store]

// Init installs the process-wide store.
func Init(s *Store) { def.Store(s) }

// Default returns the process-wide store, which may be nil before Init. Every
// method on *Store tolerates a nil receiver, so callers need not check.
func Default() *Store { return def.Load() }

// Record files an event with the process-wide store. A no-op before Init, which
// is what keeps tests and any code path that runs without telemetry working.
func Record(ev Event) { Default().Record(ev) }
