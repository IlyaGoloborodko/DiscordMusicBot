// Package adminui is the gateway in front of the admin panel: it authenticates
// people with Discord, serves the panel, and proxies its requests to the bot and
// the AI service.
//
// It is the only component of the stack meant to be reachable from outside the
// compose network, which is why the security-relevant decisions all live here:
// who may log in (access.go), how that survives a page load (session.go), and
// what is attached to a proxied request (proxy.go).
package adminui

import (
	"os"
	"strings"

	"discordAudio/internal/adminauth"
)

// Access decides who may use the panel and as what.
//
// The lists come from the environment rather than a database. Adding somebody is
// a .env edit and a restart, which for "occasionally I let someone look" is less
// machinery than a user store, a bootstrap path and a way to avoid removing the
// last owner. If granting access from inside the panel is ever wanted, this is
// the piece to replace — nothing else knows where the roles came from.
type Access struct {
	owners     map[string]bool
	moderators map[string]bool
	viewers    map[string]bool
}

// LoadAccess reads the three ID lists. Values are Discord user ids: 17-19 digit
// snowflakes, kept as strings because they overflow a float64 and JavaScript
// would silently round them.
func LoadAccess() Access {
	return Access{
		owners:     idSet(os.Getenv("ADMIN_OWNER_IDS")),
		moderators: idSet(os.Getenv("ADMIN_MODERATOR_IDS")),
		viewers:    idSet(os.Getenv("ADMIN_VIEWER_IDS")),
	}
}

func idSet(raw string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		if id := strings.TrimSpace(part); id != "" {
			out[id] = true
		}
	}
	return out
}

// RoleOf returns the role for a Discord user id, and whether they have any
// access at all.
//
// Checked strongest first, so a user listed twice gets the higher role rather
// than whichever list happened to be consulted first — a duplicate is a typo,
// and silently demoting somebody because of it is the more confusing failure.
func (a Access) RoleOf(userID string) (adminauth.Role, bool) {
	switch {
	case userID == "":
		return adminauth.RoleViewer, false
	case a.owners[userID]:
		return adminauth.RoleOwner, true
	case a.moderators[userID]:
		return adminauth.RoleModerator, true
	case a.viewers[userID]:
		return adminauth.RoleViewer, true
	}
	return adminauth.RoleViewer, false
}

// Empty reports whether nobody is allowed in. Worth refusing to start on: a
// panel with no owners cannot be fixed from inside itself, and one that accepted
// any Discord login instead would be a very bad way to find that out.
func (a Access) Empty() bool {
	return len(a.owners) == 0 && len(a.moderators) == 0 && len(a.viewers) == 0
}

// NoOwners reports whether access is configured but nobody can change anything.
func (a Access) NoOwners() bool { return len(a.owners) == 0 }
