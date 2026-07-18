package discordgo

import (
	"testing"
	"time"
)

// This client cannot process an MLS commit, so on an epoch change it asks to be
// re-added and waits for a Welcome. If that Welcome never comes, both audio
// directions break silently — the stale sender key is rejected by everyone and
// the stale receive keys turn incoming frames into noise (receive skips
// authentication, so a wrong key produces garbage rather than an error). The
// watchdog is the only thing that notices, so its bookkeeping has to be exact.
func TestReWelcomeWatchdogStandsDownOnWelcome(t *testing.T) {
	v := &VoiceConnection{}

	v.watchReWelcome(50 * time.Millisecond)
	if v.reWelcomeReq != 1 {
		t.Fatalf("reWelcomeReq = %d after one request, want 1", v.reWelcomeReq)
	}

	v.noteWelcomeHandled()

	v.RLock()
	stuck := v.welcomeOK < v.reWelcomeReq
	v.RUnlock()
	if stuck {
		t.Errorf("welcomeOK=%d reWelcomeReq=%d — a delivered Welcome must clear the request, "+
			"otherwise the watchdog reconnects a perfectly healthy session",
			v.welcomeOK, v.reWelcomeReq)
	}

	// Let the timer fire; Ready is false so it must bail out without reconnecting
	// (reconnect would panic here — there is no session).
	time.Sleep(120 * time.Millisecond)
}

// A Welcome for an earlier request must not satisfy a later one: epoch changes
// can arrive back to back, and the last one is the one that matters.
func TestReWelcomeWatchdogRearmsOnNextEpoch(t *testing.T) {
	v := &VoiceConnection{}

	v.watchReWelcome(time.Hour) // long: we only care about the counters here
	v.noteWelcomeHandled()      // first epoch resolved

	v.watchReWelcome(time.Hour) // second epoch change

	v.RLock()
	req, ok := v.reWelcomeReq, v.welcomeOK
	v.RUnlock()

	if req != 2 {
		t.Fatalf("reWelcomeReq = %d after two epoch changes, want 2", req)
	}
	if ok >= req {
		t.Errorf("welcomeOK=%d reWelcomeReq=%d — the stale Welcome from the first epoch "+
			"is being credited to the second, so a stuck session would go unnoticed", ok, req)
	}
}

// Ready=false means the connection is already being torn down or rebuilt; that
// path re-handshakes on its own and the watchdog must keep out of it.
func TestReWelcomeWatchdogSkipsUnreadyConnection(t *testing.T) {
	v := &VoiceConnection{}
	v.Ready = false

	v.watchReWelcome(20 * time.Millisecond)

	// If the watchdog ignored Ready it would call reconnect() on a VoiceConnection
	// with no session and panic, failing this test.
	time.Sleep(80 * time.Millisecond)
}
