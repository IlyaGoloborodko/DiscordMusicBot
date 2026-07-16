package config

import (
	"testing"
)

// The empty string is meaningful here: discordgo's ApplicationCommandCreate
// treats an empty guild id as "global". So "no guilds configured" must come back
// as []string{""} — one global pass — and never as an empty slice, which would
// register nothing at all and leave every server without commands.
func TestCommandGuildIDs(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want []string
	}{
		{"unset means global", "", []string{""}},
		{"blank means global", "   ", []string{""}},
		{"commas only means global", " , ,", []string{""}},
		{"single guild", "569258780152430592", []string{"569258780152430592"}},
		{"several guilds", "111,222", []string{"111", "222"}},
		{"whitespace is trimmed", " 111 , 222 ", []string{"111", "222"}},
		{"trailing comma is not an empty guild", "111,", []string{"111"}},
	}

	old := DebugGuildIDs
	defer func() { DebugGuildIDs = old }()

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			DebugGuildIDs = c.env
			got := CommandGuildIDs()
			if len(got) != len(c.want) {
				t.Fatalf("CommandGuildIDs() = %q, want %q", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("CommandGuildIDs() = %q, want %q", got, c.want)
				}
			}
		})
	}
}

// A trailing comma must not smuggle in a global registration alongside the guild
// ones: that would publish every command twice, and users would see duplicates.
func TestCommandGuildIDsNeverMixesGlobalWithGuilds(t *testing.T) {
	old := DebugGuildIDs
	defer func() { DebugGuildIDs = old }()

	DebugGuildIDs = "111,,222,"
	got := CommandGuildIDs()
	for _, g := range got {
		if g == "" {
			t.Fatalf("CommandGuildIDs() = %q — contains a global entry next to real guilds", got)
		}
	}
}
