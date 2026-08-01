package adminui

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"discordAudio/internal/adminauth"
)

const (
	sessionCookie = "admin_session"
	stateCookie   = "admin_oauth_state"

	// sessionTTL is how long a login lasts. Long enough not to be re-prompted
	// mid-investigation, short enough that removing somebody from the env lists
	// takes effect within a day even if their browser is still open.
	sessionTTL = 12 * time.Hour
)

// Session is what the gateway remembers about a logged-in person. It lives
// entirely in a signed cookie: there is no server-side session table, so the
// gateway holds no state and a restart costs nothing but a re-login.
type Session struct {
	UserID   string         `json:"uid"`
	UserName string         `json:"name"`
	Role     adminauth.Role `json:"role"`
	Expires  int64          `json:"exp"`
}

func (s Session) expired() bool { return time.Now().Unix() > s.Expires }

// signer signs and verifies cookies. Ephemeral by default: with no configured
// secret a random one is generated at startup, which is secure but logs everyone
// out on restart — see NewSigner.
type signer struct{ key []byte }

// NewSigner builds the cookie signer. An empty secret yields a random key, and
// the caller is expected to say so: sessions that silently stop surviving
// restarts look like a bug in the login flow rather than a missing setting.
func NewSigner(secret string) (*signer, bool, error) {
	if s := strings.TrimSpace(secret); s != "" {
		return &signer{key: []byte(s)}, true, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, false, err
	}
	return &signer{key: key}, false, nil
}

var errBadCookie = errors.New("cookie is not valid")

// sign encodes the payload and appends an HMAC over it.
//
// The payload is signed, not encrypted: it holds a Discord user id, a display
// name and a role, none of which is a secret. What matters is that the browser
// cannot CHANGE it — without the signature anyone could hand themselves the
// owner role by editing a cookie.
func (s *signer) sign(v any) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	b := base64.RawURLEncoding.EncodeToString(payload)
	return b + "." + base64.RawURLEncoding.EncodeToString(s.mac([]byte(b))), nil
}

func (s *signer) verify(value string, dst any) error {
	body, sig, ok := strings.Cut(value, ".")
	if !ok {
		return errBadCookie
	}
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return errBadCookie
	}
	// Constant time: a byte-at-a-time comparison would let an attacker search
	// for a valid signature by timing the rejections.
	if !hmac.Equal(want, s.mac([]byte(body))) {
		return errBadCookie
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return errBadCookie
	}
	return json.Unmarshal(payload, dst)
}

func (s *signer) mac(b []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write(b)
	return m.Sum(nil)
}

// setSession writes the login cookie.
//
// HttpOnly so a script cannot read it; SameSite=Lax so it is not sent on
// cross-site POSTs, which is what keeps another site from acting through a
// logged-in browser; Secure whenever the panel is served over TLS.
func (g *Gateway) setSession(w http.ResponseWriter, sess Session) error {
	value, err := g.signer.sign(sess)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   g.secureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(sess.Expires, 0),
	})
	return nil
}

func (g *Gateway) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   g.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// session reads and validates the login cookie.
//
// The role is re-checked against the current access lists on every request
// rather than trusted from the cookie: removing somebody from .env has to take
// effect at their next request, not whenever their 12-hour session happens to
// run out. The cookie proves who they are; the lists decide what that is worth.
func (g *Gateway) session(r *http.Request) (Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Session{}, false
	}
	var sess Session
	if err := g.signer.verify(c.Value, &sess); err != nil {
		return Session{}, false
	}
	if sess.expired() {
		return Session{}, false
	}

	role, allowed := g.access.RoleOf(sess.UserID)
	if !allowed {
		return Session{}, false
	}
	sess.Role = role
	return sess, true
}
