package config

import "strings"

const (
	SupportCommands = true
)

// DebugGuildIDs is the raw DEBUG_GUIID value, set from the environment at
// startup: a comma-separated list of guild IDs to register slash commands in.
var DebugGuildIDs string

// CommandGuildIDs returns the guilds to register slash commands in; a single
// empty string means "register globally".
//
// The two modes trade reach against speed, which is why this is configuration
// rather than a build-time flag:
//
//   - listed guilds — commands appear instantly, but only there. Right while
//     developing, wrong the moment the bot joins a second server: it shows up,
//     joins voice, and has no commands.
//   - empty (global) — every server the bot is in, but Discord takes up to an
//     hour to propagate.
func CommandGuildIDs() []string {
	var out []string
	for _, id := range strings.Split(DebugGuildIDs, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}
