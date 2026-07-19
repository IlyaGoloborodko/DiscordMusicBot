package voice

import (
	"testing"
	"time"
)

func TestIdleTimeoutFromEnv(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"unset falls back to an hour", "", time.Hour},
		{"explicit duration", "30m", 30 * time.Minute},
		{"seconds, for testing", "5s", 5 * time.Second},
		// Zero is the documented way to keep the bot in the channel forever.
		{"zero disables leaving", "0", 0},
		// A typo must not silently turn the feature off or leave instantly.
		{"garbage falls back to the default", "часик", time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("VOICE_IDLE_TIMEOUT", c.env)
			if got := idleTimeout(); got != c.want {
				t.Errorf("idleTimeout() = %v, want %v", got, c.want)
			}
		})
	}
}

// touch is what speech and commands call; idleFor is what the check reads.
func TestTouchResetsIdleClock(t *testing.T) {
	var l voiceListener
	l.lastActive.Store(time.Now().Add(-2 * time.Hour).UnixNano())

	if idle := l.idleFor(); idle < time.Hour {
		t.Fatalf("idleFor() = %v, want at least an hour", idle)
	}
	l.touch()
	if idle := l.idleFor(); idle > time.Second {
		t.Errorf("idleFor() = %v right after touch(), want ~0", idle)
	}
}

// A listener that never touched the clock must not read as idle since the epoch
// and disconnect on its first tick.
func TestFreshListenerIsNotImmediatelyIdle(t *testing.T) {
	t.Setenv("VOICE_IDLE_TIMEOUT", "1h")

	vc := newTestVC()
	defer StopVoiceListener(vc)
	StartVoiceListener(nil, vc, "channel-a")

	listenMu.Lock()
	l := listeners[vc]
	listenMu.Unlock()

	if idle := l.idleFor(); idle > time.Second {
		t.Errorf("a just-started listener reports %v idle — it would leave straight away", idle)
	}
}
