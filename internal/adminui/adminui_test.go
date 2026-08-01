package adminui

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"discordAudio/internal/adminauth"
)

// backend records what the gateway actually forwarded.
type backend struct {
	*httptest.Server
	got chan http.Header
	url string
}

func newBackend(t *testing.T) *backend {
	t.Helper()
	b := &backend{got: make(chan http.Header, 8)}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.got <- r.Header.Clone()
		_ = json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path})
	}))
	t.Cleanup(b.Close)
	b.url = b.URL
	return b
}

func (b *backend) lastHeaders(t *testing.T) http.Header {
	t.Helper()
	select {
	case h := <-b.got:
		return h
	case <-time.After(2 * time.Second):
		t.Fatal("the backend was never called")
		return nil
	}
}

func newGateway(t *testing.T, botURL string) *Gateway {
	t.Helper()
	t.Setenv("ADMIN_OWNER_IDS", "111")
	t.Setenv("ADMIN_MODERATOR_IDS", "222")
	t.Setenv("ADMIN_VIEWER_IDS", "333")

	g, err := New(Config{
		Addr:          "127.0.0.1:0",
		BotAddr:       botURL,
		AIAddr:        botURL,
		BotToken:      "service-token",
		SessionSecret: "test-signing-secret",
		PublicURL:     "http://localhost:8080",
		ClientID:      "cid",
		ClientSecret:  "csecret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

// login returns a request carrying a valid session cookie for the given user.
func (g *Gateway) testCookie(t *testing.T, userID, name string, role adminauth.Role) *http.Cookie {
	t.Helper()
	value, err := g.signer.sign(Session{
		UserID: userID, UserName: name, Role: role,
		Expires: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("signing a session: %v", err)
	}
	return &http.Cookie{Name: sessionCookie, Value: value}
}

// THE test for this package. A browser can send any header it likes; if one
// called X-Admin-Role survived the proxy alongside our service token, the
// backend would believe it and hand a viewer everyone's transcripts.
func TestForgedAdminHeadersAreStripped(t *testing.T) {
	be := newBackend(t)
	g := newGateway(t, be.url)

	req := httptest.NewRequest(http.MethodGet, "/api/bot/events", nil)
	req.AddCookie(g.testCookie(t, "333", "viewer-user", adminauth.RoleViewer))
	// Everything an attacker might try.
	req.Header.Set("X-Admin-Role", "owner")
	req.Header.Set("x-admin-role", "owner")
	req.Header.Set("X-Admin-Token", "stolen")
	req.Header.Set("X-Admin-User-Id", "111")

	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("proxy returned %d: %s", rec.Code, rec.Body)
	}
	h := be.lastHeaders(t)

	if got := h.Get(adminauth.HeaderRole); got != "viewer" {
		t.Errorf("forwarded role = %q, want %q — the client's forged header won", got, "viewer")
	}
	if got := h.Get(adminauth.HeaderUserID); got != "333" {
		t.Errorf("forwarded user id = %q, want the session's 333", got)
	}
	if got := h.Get(adminauth.HeaderToken); got != "service-token" {
		t.Errorf("forwarded token = %q, want the gateway's own", got)
	}
}

// The service token authenticates the gateway, not the user. If it ever reached
// the browser, a stolen session could be replayed straight at the backends.
func TestServiceTokenNeverReachesTheBrowser(t *testing.T) {
	be := newBackend(t)
	g := newGateway(t, be.url)

	req := httptest.NewRequest(http.MethodGet, "/api/bot/state", nil)
	req.AddCookie(g.testCookie(t, "111", "owner-user", adminauth.RoleOwner))
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	for name, values := range rec.Header() {
		for _, v := range values {
			if strings.Contains(v, "service-token") {
				t.Errorf("response header %s leaked the service token", name)
			}
		}
	}
	if strings.Contains(rec.Body.String(), "service-token") {
		t.Error("response body leaked the service token")
	}
}

func TestPathIsRewrittenToTheBackendNamespace(t *testing.T) {
	be := newBackend(t)
	g := newGateway(t, be.url)

	req := httptest.NewRequest(http.MethodGet, "/api/bot/stats/stt?since=15m", nil)
	req.AddCookie(g.testCookie(t, "111", "owner", adminauth.RoleOwner))
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	var body struct{ Path string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v (%s)", err, rec.Body)
	}
	if body.Path != "/admin/stats/stt" {
		t.Errorf("backend saw %q, want /admin/stats/stt", body.Path)
	}
}

func TestAnonymousRequestsAreRejected(t *testing.T) {
	be := newBackend(t)
	g := newGateway(t, be.url)

	for _, path := range []string{"/api/me", "/api/bot/state", "/api/ai/sessions"} {
		rec := httptest.NewRecorder()
		g.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session = %d, want 401", path, rec.Code)
		}
		// It must answer JSON, not a redirect to an HTML login page: the panel
		// fetches these and would report a parse error instead of "signed out".
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("GET %s Content-Type = %q, want JSON", path, ct)
		}
	}
}

func TestTamperedSessionCookieIsRefused(t *testing.T) {
	be := newBackend(t)
	g := newGateway(t, be.url)

	good := g.testCookie(t, "333", "viewer", adminauth.RoleViewer)
	body, sig, _ := strings.Cut(good.Value, ".")

	// The actual attack: rewrite the payload to claim owner, keep the signature
	// that was issued for the old one. Editing the base64 text directly would
	// not do — "333" does not appear in it literally, so a naive string replace
	// silently changes nothing and the test passes without proving anything.
	forged, err := json.Marshal(Session{
		UserID: "111", UserName: "viewer", Role: adminauth.RoleOwner,
		Expires: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	forgedBody := base64.RawURLEncoding.EncodeToString(forged)

	for _, c := range []struct{ name, value string }{
		{"payload swapped, old signature", forgedBody + "." + sig},
		{"signature dropped", body},
		{"signature garbage", body + ".AAAA"},
		{"empty", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.value})
			rec := httptest.NewRecorder()
			g.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("a tampered cookie was accepted (%d)", rec.Code)
			}
		})
	}
}

// The cookie proves identity; the env lists decide what it is worth. Revoking
// somebody must not wait for their 12-hour session to lapse.
func TestRoleIsRecheckedNotTrustedFromTheCookie(t *testing.T) {
	be := newBackend(t)
	g := newGateway(t, be.url)

	// A cookie that claims owner, for a user who is only a viewer.
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(g.testCookie(t, "333", "viewer", adminauth.RoleOwner))
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	var me struct{ Role string }
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decoding: %v (%s)", err, rec.Body)
	}
	if me.Role != "viewer" {
		t.Errorf("role = %q, want viewer — the cookie's claim was trusted over the access list", me.Role)
	}

	// And a user removed from every list loses access outright.
	g.access = Access{}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(g.testCookie(t, "333", "viewer", adminauth.RoleViewer))
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a revoked user still got in (%d)", rec.Code)
	}
}

func TestExpiredSessionIsRefused(t *testing.T) {
	be := newBackend(t)
	g := newGateway(t, be.url)

	value, err := g.signer.sign(Session{
		UserID: "111", Role: adminauth.RoleOwner,
		Expires: time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("an expired session was accepted (%d)", rec.Code)
	}
}

// The state parameter is what stops somebody feeding a victim a callback URL
// carrying the attacker's authorization code.
func TestCallbackRejectsAStateMismatch(t *testing.T) {
	be := newBackend(t)
	g := newGateway(t, be.url)

	for _, c := range []struct {
		name   string
		cookie *http.Cookie
		query  string
	}{
		{"no cookie", nil, "?code=abc&state=xyz"},
		{"no state in the query", &http.Cookie{Name: stateCookie, Value: "xyz"}, "?code=abc"},
		{"they disagree", &http.Cookie{Name: stateCookie, Value: "xyz"}, "?code=abc&state=other"},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/callback"+c.query, nil)
			if c.cookie != nil {
				req.AddCookie(c.cookie)
			}
			rec := httptest.NewRecorder()
			g.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("callback returned %d, want 400", rec.Code)
			}
		})
	}
}

func TestLoginRedirectsToDiscordWithState(t *testing.T) {
	be := newBackend(t)
	g := newGateway(t, be.url)

	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, discordAuthorizeURL) {
		t.Errorf("redirect went to %q", loc)
	}
	for _, want := range []string{"client_id=cid", "state=", "scope=identify", "response_type=code"} {
		if !strings.Contains(loc, want) {
			t.Errorf("authorize URL is missing %q: %s", want, loc)
		}
	}
	// The client secret must never appear in a browser-facing URL.
	if strings.Contains(loc, "csecret") {
		t.Error("the client secret leaked into the authorize redirect")
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), stateCookie) {
		t.Error("no state cookie was set, so the callback has nothing to compare against")
	}
}

func TestAccessListsPickTheStrongestRole(t *testing.T) {
	t.Setenv("ADMIN_OWNER_IDS", "111, 999")
	t.Setenv("ADMIN_MODERATOR_IDS", "222,999") // 999 listed twice: a typo
	t.Setenv("ADMIN_VIEWER_IDS", "333")
	a := LoadAccess()

	for _, c := range []struct {
		id      string
		want    adminauth.Role
		allowed bool
	}{
		{"111", adminauth.RoleOwner, true},
		{"222", adminauth.RoleModerator, true},
		{"333", adminauth.RoleViewer, true},
		{"999", adminauth.RoleOwner, true}, // strongest wins over the duplicate
		{"444", adminauth.RoleViewer, false},
		{"", adminauth.RoleViewer, false},
	} {
		got, allowed := a.RoleOf(c.id)
		if got != c.want || allowed != c.allowed {
			t.Errorf("RoleOf(%q) = (%v, %v), want (%v, %v)", c.id, got, allowed, c.want, c.allowed)
		}
	}
}

// A panel nobody can enter is useless; one that let anyone in would be worse.
func TestRefusesToStartWithoutAccessListsOrToken(t *testing.T) {
	t.Setenv("ADMIN_OWNER_IDS", "")
	t.Setenv("ADMIN_MODERATOR_IDS", "")
	t.Setenv("ADMIN_VIEWER_IDS", "")
	if _, err := New(Config{BotToken: "t"}); err == nil {
		t.Error("New accepted an empty access list")
	}

	t.Setenv("ADMIN_OWNER_IDS", "111")
	if _, err := New(Config{BotToken: ""}); err == nil {
		t.Error("New accepted an empty ADMIN_API_KEY")
	}
}

func TestStripAdminHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-Admin-Role", "owner")
	h.Set("X-Admin-Something-New", "x")
	h.Set("Authorization", "Bearer keep-me")
	h.Set("Content-Type", "application/json")

	stripAdminHeaders(h)

	if len(h) != 2 || h.Get("Authorization") == "" || h.Get("Content-Type") == "" {
		t.Errorf("stripAdminHeaders removed the wrong things: %v", h)
	}
	// By prefix, not by a list of names: a header added later is covered without
	// anyone having to remember this function exists.
	if h.Get("X-Admin-Something-New") != "" {
		t.Error("an unknown X-Admin-* header survived")
	}
}

func TestPanelIsServedAnonymously(t *testing.T) {
	be := newBackend(t)
	g := newGateway(t, be.url)

	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200 (the sign-in page must be reachable)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "auth/login") {
		t.Error("the page does not offer a way to sign in")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}
