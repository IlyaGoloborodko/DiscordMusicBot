package adminapi

import (
	"crypto/subtle"
	"net/http"

	"discordAudio/internal/adminauth"
)

// The header names and roles live in adminauth, a leaf package with no
// dependencies, because the gateway needs them too and reaching them through
// this package would drag the cgo opus bindings into a binary that has no audio
// in it. Re-exported here as aliases so callers of this package see one
// coherent API.
const (
	HeaderToken    = adminauth.HeaderToken
	HeaderUserID   = adminauth.HeaderUserID
	HeaderUserName = adminauth.HeaderUserName
	HeaderRole     = adminauth.HeaderRole
)

type Role = adminauth.Role

const (
	RoleViewer    = adminauth.RoleViewer
	RoleModerator = adminauth.RoleModerator
	RoleOwner     = adminauth.RoleOwner
)

// ParseRole maps a header value to a role; anything unrecognised is the weakest.
func ParseRole(s string) Role { return adminauth.ParseRole(s) }

// Identity is who the gateway says is calling.
type Identity struct {
	UserID   string
	UserName string
	Role     Role
}

// authenticate validates the service token and only then reads the identity
// headers.
//
// The order is the whole point. The identity and role headers are set by the
// gateway after it has authenticated the user through Discord OAuth2; they are
// unauthenticated hints on the wire. Reading them without a valid token would
// let anything that reaches this port inside the compose network — a
// compromised sibling container, a mistaken port publish — declare itself owner
// and read everyone's transcripts.
func (s *Server) authenticate(r *http.Request) (Identity, bool) {
	got := r.Header.Get(HeaderToken)
	// Constant time: the token is a fixed secret compared on every request, so a
	// naive == would leak it a byte at a time to anyone able to measure.
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		return Identity{}, false
	}
	return Identity{
		UserID:   r.Header.Get(HeaderUserID),
		UserName: r.Header.Get(HeaderUserName),
		Role:     ParseRole(r.Header.Get(HeaderRole)),
	}, true
}
