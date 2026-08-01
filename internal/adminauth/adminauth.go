// Package adminauth holds the vocabulary the admin panel's three parts agree
// on: the header names a request carries between them, and the roles those
// headers can name.
//
// It exists as its own package because it must have NO dependencies. The
// gateway (cmd/adminui) needs these names, but it used to reach them through
// internal/adminapi, which imports internal/voice, which imports the cgo opus
// bindings — so the gateway could not be built with CGO_ENABLED=0 and its
// container image would have failed to build at all. A leaf package with
// nothing but constants keeps the gateway a static binary.
package adminauth

import "strings"

// Header names. All three components must agree on these exact strings, so they
// are defined once here rather than spelled out at each use.
const (
	HeaderToken    = "X-Admin-Token"
	HeaderUserID   = "X-Admin-User-Id"
	HeaderUserName = "X-Admin-User-Name"
	HeaderRole     = "X-Admin-Role"
)

// Role is what a caller is allowed to see. The order matters: comparisons are
// "at least this role", so a new role must be inserted at the right rank rather
// than appended.
type Role int

const (
	RoleViewer Role = iota
	RoleModerator
	RoleOwner
)

func (r Role) String() string {
	switch r {
	case RoleOwner:
		return "owner"
	case RoleModerator:
		return "moderator"
	default:
		return "viewer"
	}
}

// ParseRole maps a header value to a role. Anything unrecognised — an empty
// string, a typo, a value from a newer gateway — becomes the WEAKEST role.
// Failing open here would mean one typo silently handing out transcripts.
func ParseRole(s string) Role {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "owner":
		return RoleOwner
	case "moderator":
		return RoleModerator
	default:
		return RoleViewer
	}
}
