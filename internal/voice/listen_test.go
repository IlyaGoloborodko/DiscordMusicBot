package voice

import (
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func newTestVC() *discordgo.VoiceConnection {
	return &discordgo.VoiceConnection{OpusRecv: make(chan *discordgo.Packet)}
}

// The listener used to freeze the text channel at the first /join and ignore
// every later command, because startup was gated on "already listening?". The AI
// service keys conversation memory by guild+channel, so a channel id that drifts
// from the one the player uses silently splits one conversation into two — the
// bot "forgets" what was just said.
func TestStartVoiceListenerUpdatesChannel(t *testing.T) {
	vc := newTestVC()
	defer StopVoiceListener(vc)

	StartVoiceListener(nil, vc, "channel-a")

	listenMu.Lock()
	l, ok := listeners[vc]
	listenMu.Unlock()
	if !ok {
		t.Fatal("listener was not registered")
	}
	if got := l.chID(); got != "channel-a" {
		t.Fatalf("chID() = %q, want %q", got, "channel-a")
	}

	// A second command from another channel must retarget the existing listener,
	// not be dropped and not start a second one.
	StartVoiceListener(nil, vc, "channel-b")

	listenMu.Lock()
	l2, count := listeners[vc], len(listeners)
	listenMu.Unlock()
	if l2 != l {
		t.Error("a second listener was started for the same connection")
	}
	if count != 1 {
		t.Errorf("registry holds %d listeners for one connection, want 1", count)
	}
	if got := l.chID(); got != "channel-b" {
		t.Errorf("chID() = %q after a command from channel-b, want %q", got, "channel-b")
	}
}

// An empty channel id means "no channel given", not "forget where you talk".
func TestSetChIDIgnoresEmpty(t *testing.T) {
	vc := newTestVC()
	defer StopVoiceListener(vc)

	StartVoiceListener(nil, vc, "channel-a")
	StartVoiceListener(nil, vc, "")

	listenMu.Lock()
	l := listeners[vc]
	listenMu.Unlock()
	if got := l.chID(); got != "channel-a" {
		t.Errorf("chID() = %q, want the previous channel to survive an empty update", got)
	}
}

// The fork never closes OpusRecv, so run() cannot notice a disconnect on its own:
// without an explicit stop the goroutine blocks on a dead channel forever and the
// registry entry pins the connection.
func TestStopVoiceListenerStopsTheGoroutine(t *testing.T) {
	vc := newTestVC()
	StartVoiceListener(nil, vc, "channel-a")

	listenMu.Lock()
	l := listeners[vc]
	listenMu.Unlock()

	StopVoiceListener(vc)

	select {
	case <-l.done:
	case <-time.After(time.Second):
		t.Fatal("done was not closed, so run() is still blocked on OpusRecv")
	}

	listenMu.Lock()
	_, still := listeners[vc]
	listenMu.Unlock()
	if still {
		t.Error("listener is still registered after StopVoiceListener")
	}

	// Stopping twice must not panic on a double close.
	StopVoiceListener(vc)
}

// Rejoining after a disconnect has to produce a working listener again.
func TestRestartAfterStop(t *testing.T) {
	vc := newTestVC()
	StartVoiceListener(nil, vc, "channel-a")
	StopVoiceListener(vc)

	StartVoiceListener(nil, vc, "channel-b")
	defer StopVoiceListener(vc)

	listenMu.Lock()
	l, ok := listeners[vc]
	listenMu.Unlock()
	if !ok {
		t.Fatal("no listener after rejoin — the bot would sit in voice and hear nothing")
	}
	if got := l.chID(); got != "channel-b" {
		t.Errorf("chID() = %q, want %q", got, "channel-b")
	}
}

// chID is read from the transcription goroutines while commands update it.
func TestChannelIDIsRaceFree(t *testing.T) {
	vc := newTestVC()
	defer StopVoiceListener(vc)
	StartVoiceListener(nil, vc, "channel-a")

	listenMu.Lock()
	l := listeners[vc]
	listenMu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); l.setChID("channel-b") }()
		go func() { defer wg.Done(); _ = l.chID() }()
	}
	wg.Wait()
}
