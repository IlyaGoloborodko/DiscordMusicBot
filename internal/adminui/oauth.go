package adminui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Discord's OAuth2 endpoints. The authorize URL is the browser-facing one; the
// other two are called server to server with the client secret.
const (
	discordAuthorizeURL = "https://discord.com/oauth2/authorize"
	discordTokenURL     = "https://discord.com/api/v10/oauth2/token"
	discordUserURL      = "https://discord.com/api/v10/users/@me"

	// identify is the whole scope. It returns the user id and display name and
	// nothing else — no email, no guild list, no messages. The panel needs to
	// know who is knocking, and that is all it asks for.
	discordScope = "identify"
)

// handleLogin starts the OAuth2 flow.
//
// The state parameter is the CSRF defence: a random value is put in a cookie and
// echoed through Discord, and the callback accepts nothing that does not match.
// Without it, an attacker could feed somebody a callback URL carrying their own
// authorization code and quietly log that person into the attacker's account.
func (g *Gateway) handleLogin(w http.ResponseWriter, r *http.Request) {
	if g.oauthUnconfigured() {
		http.Error(w, "Discord OAuth2 is not configured on this gateway", http.StatusServiceUnavailable)
		return
	}

	state, err := randomToken()
	if err != nil {
		http.Error(w, "cannot start login", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   g.secureCookies,
		SameSite: http.SameSiteLaxMode,
		// Short: it only has to survive the round trip to Discord and back.
		Expires: time.Now().Add(10 * time.Minute),
	})

	q := url.Values{
		"client_id":     {g.cfg.ClientID},
		"redirect_uri":  {g.redirectURI()},
		"response_type": {"code"},
		"scope":         {discordScope},
		"state":         {state},
	}
	http.Redirect(w, r, discordAuthorizeURL+"?"+q.Encode(), http.StatusFound)
}

// handleCallback finishes the flow: verify state, trade the code for a token,
// ask Discord who it belongs to, and check that person against the access lists.
func (g *Gateway) handleCallback(w http.ResponseWriter, r *http.Request) {
	if g.oauthUnconfigured() {
		http.Error(w, "Discord OAuth2 is not configured on this gateway", http.StatusServiceUnavailable)
		return
	}

	want, err := r.Cookie(stateCookie)
	got := r.URL.Query().Get("state")
	if err != nil || got == "" || want.Value != got {
		// Deliberately vague to the browser; the reason is in the log.
		g.logf("[adminui] login rejected: state mismatch from %s", r.RemoteAddr)
		http.Error(w, "login expired or invalid, please try again", http.StatusBadRequest)
		return
	}
	g.clearCookie(w, stateCookie)

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "no authorization code", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	token, err := g.exchangeCode(ctx, code)
	if err != nil {
		g.logf("[adminui] token exchange failed: %v", err)
		http.Error(w, "could not complete the Discord login", http.StatusBadGateway)
		return
	}
	user, err := g.discordUser(ctx, token)
	if err != nil {
		g.logf("[adminui] fetching the Discord user failed: %v", err)
		http.Error(w, "could not complete the Discord login", http.StatusBadGateway)
		return
	}

	role, allowed := g.access.RoleOf(user.ID)
	if !allowed {
		// Logged, because "I can't get in" is otherwise unanswerable — this line
		// carries the exact id to add to the env lists.
		g.logf("[adminui] access denied for Discord user %s (%s): not in any ADMIN_*_IDS list", user.ID, user.Username)
		http.Error(w, "This Discord account does not have access to the panel.", http.StatusForbidden)
		return
	}

	sess := Session{
		UserID:   user.ID,
		UserName: user.Username,
		Role:     role,
		Expires:  time.Now().Add(sessionTTL).Unix(),
	}
	if err := g.setSession(w, sess); err != nil {
		http.Error(w, "cannot start session", http.StatusInternalServerError)
		return
	}
	g.logf("[adminui] %s (%s) signed in as %s", user.Username, user.ID, role)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (g *Gateway) handleLogout(w http.ResponseWriter, r *http.Request) {
	g.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/", http.StatusFound)
}

type discordUserInfo struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

func (g *Gateway) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{
		"client_id":     {g.cfg.ClientID},
		"client_secret": {g.cfg.ClientSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {g.redirectURI()},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, discordTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := g.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		// The body can echo the request, so it is not logged verbatim: that is
		// how a client secret ends up in a log file.
		return "", fmt.Errorf("discord token endpoint returned %s", resp.Status)
	}

	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("discord returned no access token")
	}
	return out.AccessToken, nil
}

func (g *Gateway) discordUser(ctx context.Context, token string) (discordUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discordUserURL, nil)
	if err != nil {
		return discordUserInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := g.http.Do(req)
	if err != nil {
		return discordUserInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return discordUserInfo{}, fmt.Errorf("discord user endpoint returned %s", resp.Status)
	}
	var u discordUserInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil {
		return discordUserInfo{}, err
	}
	if u.ID == "" {
		return discordUserInfo{}, fmt.Errorf("discord returned a user with no id")
	}
	return u, nil
}

// redirectURI must match one registered in the Discord developer portal exactly,
// including scheme and trailing path.
func (g *Gateway) redirectURI() string {
	return strings.TrimRight(g.cfg.PublicURL, "/") + "/auth/callback"
}

func (g *Gateway) oauthUnconfigured() bool {
	return g.cfg.ClientID == "" || g.cfg.ClientSecret == "" || g.cfg.PublicURL == ""
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
