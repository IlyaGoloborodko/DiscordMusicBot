package voice

import (
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// testSession builds a session whose cached guild puts the given users in the
// bot's voice channel. discordgo maintains VoiceStates from gateway events, so
// this is the same shape the live code reads.
func testSession(t *testing.T, present ...*discordgo.VoiceState) *discordgo.Session {
	t.Helper()

	s, err := discordgo.New("")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	s.State = discordgo.NewState()
	s.State.User = &discordgo.User{ID: "bot", Bot: true}
	if err := s.State.GuildAdd(&discordgo.Guild{ID: "g", VoiceStates: present}); err != nil {
		t.Fatalf("seeding guild state: %v", err)
	}
	return s
}

func testConn() *discordgo.VoiceConnection {
	return &discordgo.VoiceConnection{GuildID: "g", ChannelID: "voice-1"}
}

func TestChannelIsEmpty(t *testing.T) {
	cases := []struct {
		name    string
		present []*discordgo.VoiceState
		empty   bool
	}{
		{"nobody at all", nil, true},
		{
			"only the bot itself",
			[]*discordgo.VoiceState{{UserID: "bot", ChannelID: "voice-1"}},
			true,
		},
		{
			"a listener is there",
			[]*discordgo.VoiceState{{UserID: "human", ChannelID: "voice-1"}},
			false,
		},
		// Somebody in a *different* voice channel of the same guild is not in
		// the room with the bot; the guild-wide VoiceStates list holds them too.
		{
			"a listener in another channel does not count",
			[]*discordgo.VoiceState{{UserID: "human", ChannelID: "voice-2"}},
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := &voiceListener{session: testSession(t, c.present...)}
			empty, known := l.channelIsEmpty(testConn())
			if !known {
				t.Fatal("channelIsEmpty() reported the state as unreadable")
			}
			if empty != c.empty {
				t.Errorf("channelIsEmpty() = %v, want %v", empty, c.empty)
			}
		})
	}
}

// An unreadable state must not read as an empty room — that would disconnect a
// channel full of people.
func TestUnknownStateIsNotEmptiness(t *testing.T) {
	l := &voiceListener{} // no session at all
	if _, known := l.channelIsEmpty(testConn()); known {
		t.Error("channelIsEmpty() claimed to know the answer with no session")
	}
	if l.aloneTooLong(testConn()) {
		t.Error("aloneTooLong() wanted to leave on a state it cannot read")
	}
}

func TestAloneTooLong(t *testing.T) {
	timeout := aloneTimeout()
	l := &voiceListener{session: testSession(t)}
	vc := testConn()

	// First tick only starts the clock.
	if l.aloneTooLong(vc) {
		t.Fatal("aloneTooLong() left immediately; a brief reconnect would cut the music")
	}
	if l.aloneSince.Load() == 0 {
		t.Fatal("the alone clock was not started")
	}

	// Still inside the grace period.
	l.aloneSince.Store(time.Now().Add(-timeout / 2).UnixNano())
	if l.aloneTooLong(vc) {
		t.Errorf("aloneTooLong() left after half of %s", timeout)
	}

	l.aloneSince.Store(time.Now().Add(-timeout - time.Second).UnixNano())
	if !l.aloneTooLong(vc) {
		t.Errorf("aloneTooLong() stayed past %s in an empty channel", timeout)
	}
}

func TestAloneTimeoutFromEnv(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"unset falls back to ten minutes", "", 10 * time.Minute},
		{"bare seconds", "60", time.Minute},
		{"zero disables the check", "0", 0},
		{"garbage falls back to the default", "десять минут", 10 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("VOICE_ALONE_TIMEOUT", c.env)
			if got := aloneTimeout(); got != c.want {
				t.Errorf("aloneTimeout() = %v, want %v", got, c.want)
			}
		})
	}
}

// Zero means "play to an empty channel for as long as you like" — the check has
// to be off, not firing on the first tick.
func TestZeroAloneTimeoutNeverLeaves(t *testing.T) {
	t.Setenv("VOICE_ALONE_TIMEOUT", "0")

	l := &voiceListener{session: testSession(t)}
	l.aloneSince.Store(time.Now().Add(-24 * time.Hour).UnixNano())

	if l.aloneTooLong(testConn()) {
		t.Error("aloneTooLong() left with the check disabled")
	}
}

// Someone walking back in restarts the grace period from scratch.
func TestSomeoneReturningResetsTheClock(t *testing.T) {
	l := &voiceListener{
		session: testSession(t, &discordgo.VoiceState{UserID: "human", ChannelID: "voice-1"}),
	}
	l.aloneSince.Store(time.Now().Add(-aloneTimeout() - time.Second).UnixNano())

	if l.aloneTooLong(testConn()) {
		t.Fatal("aloneTooLong() left a channel that has a listener in it")
	}
	if l.aloneSince.Load() != 0 {
		t.Error("the alone clock kept running with somebody present")
	}
}
