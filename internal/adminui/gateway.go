package adminui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config is the gateway's whole configuration. Everything comes from the
// environment; there is no config file and no database.
type Config struct {
	Addr      string // ADMIN_UI_ADDR, default :8080
	Domain    string // ADMIN_DOMAIN: set -> automatic TLS via Let's Encrypt
	PublicURL string // ADMIN_PUBLIC_URL, the origin browsers reach us on

	ClientID     string // DISCORD_CLIENT_ID
	ClientSecret string // DISCORD_CLIENT_SECRET

	SessionSecret string // ADMIN_SESSION_SECRET; empty -> random, logins die on restart
	CertCache     string // ADMIN_CERT_CACHE, where autocert keeps certificates

	BotAddr  string // BOT_ADMIN_ADDR
	BotToken string // ADMIN_API_KEY
	AIAddr   string // AI_ADMIN_ADDR
	AIToken  string // AI_ADMIN_API_KEY, falling back to ADMIN_API_KEY
}

// ConfigFromEnv reads the configuration, applying the defaults that make a
// compose deployment work with as little in .env as possible.
func ConfigFromEnv() Config {
	cfg := Config{
		Addr:          envOr("ADMIN_UI_ADDR", ":8080"),
		Domain:        strings.TrimSpace(os.Getenv("ADMIN_DOMAIN")),
		PublicURL:     strings.TrimSpace(os.Getenv("ADMIN_PUBLIC_URL")),
		ClientID:      strings.TrimSpace(os.Getenv("DISCORD_CLIENT_ID")),
		ClientSecret:  strings.TrimSpace(os.Getenv("DISCORD_CLIENT_SECRET")),
		SessionSecret: os.Getenv("ADMIN_SESSION_SECRET"),
		CertCache:     envOr("ADMIN_CERT_CACHE", "/data/certs"),
		BotAddr:       envOr("BOT_ADMIN_ADDR", "http://bot:8090"),
		BotToken:      strings.TrimSpace(os.Getenv("ADMIN_API_KEY")),
		AIAddr:        envOr("AI_ADMIN_ADDR", "http://ai:8000"),
		AIToken:       strings.TrimSpace(os.Getenv("AI_ADMIN_API_KEY")),
	}
	if cfg.AIToken == "" {
		cfg.AIToken = cfg.BotToken
	}
	// With a domain and no explicit public URL, the origin is knowable: that is
	// what the certificate will be for.
	if cfg.PublicURL == "" && cfg.Domain != "" {
		cfg.PublicURL = "https://" + cfg.Domain
	}
	return cfg
}

func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

// Gateway serves the panel, authenticates people against Discord, and proxies
// the panel's requests to the backends.
type Gateway struct {
	cfg    Config
	access Access
	signer *signer
	http   *http.Client

	// secureCookies marks cookies Secure. Tied to actually serving TLS, because
	// a Secure cookie over plain HTTP is simply never sent — which presents as
	// "login does nothing" during local development.
	secureCookies bool

	mux *http.ServeMux
	srv *http.Server
}

// New validates the configuration and builds the gateway.
//
// It refuses to start with an empty access list. A panel nobody can enter is
// useless, but the alternative reading — "no list means let anyone in" — would
// put an admin interface for a Discord bot on the open internet, and a mistake
// there is not recoverable by editing a file afterwards.
func New(cfg Config) (*Gateway, error) {
	access := LoadAccess()
	if access.Empty() {
		return nil, errors.New("no ADMIN_OWNER_IDS/ADMIN_MODERATOR_IDS/ADMIN_VIEWER_IDS are set: refusing to start a panel nobody can use")
	}
	if cfg.BotToken == "" {
		return nil, errors.New("ADMIN_API_KEY is empty: the gateway has no way to authenticate to the services")
	}

	sign, persistent, err := NewSigner(cfg.SessionSecret)
	if err != nil {
		return nil, fmt.Errorf("session signer: %w", err)
	}

	g := &Gateway{
		cfg:           cfg,
		access:        access,
		signer:        sign,
		http:          &http.Client{Timeout: 15 * time.Second},
		secureCookies: cfg.Domain != "",
		mux:           http.NewServeMux(),
	}

	if !persistent {
		g.logf("[adminui] ADMIN_SESSION_SECRET is not set: using a random key, so everyone is logged out on restart")
	}
	if access.NoOwners() {
		g.logf("[adminui] WARNING: no ADMIN_OWNER_IDS — nobody can edit prompts or settings, or read transcripts")
	}
	if g.oauthUnconfigured() {
		g.logf("[adminui] WARNING: DISCORD_CLIENT_ID/SECRET/ADMIN_PUBLIC_URL incomplete — login will not work")
	}

	if err := g.routes(); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *Gateway) routes() error {
	botURL, err := url.Parse(g.cfg.BotAddr)
	if err != nil {
		return fmt.Errorf("BOT_ADMIN_ADDR %q: %w", g.cfg.BotAddr, err)
	}
	aiURL, err := url.Parse(g.cfg.AIAddr)
	if err != nil {
		return fmt.Errorf("AI_ADMIN_ADDR %q: %w", g.cfg.AIAddr, err)
	}

	g.mux.HandleFunc("GET /auth/login", g.handleLogin)
	g.mux.HandleFunc("GET /auth/callback", g.handleCallback)
	g.mux.HandleFunc("POST /auth/logout", g.handleLogout)
	g.mux.HandleFunc("GET /api/me", g.requireSession(g.handleMe))

	botProxy := g.newProxy(botURL, g.cfg.BotToken, "/api/bot")
	aiProxy := g.newProxy(aiURL, g.cfg.AIToken, "/api/ai")
	g.mux.Handle("/api/bot/", g.requireSession(botProxy.ServeHTTP))
	g.mux.Handle("/api/ai/", g.requireSession(aiProxy.ServeHTTP))

	g.mux.Handle("/", g.staticHandler())
	return nil
}

type ctxKey int

const sessionKey ctxKey = 0

func sessionFrom(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey).(Session)
	return s, ok
}

// requireSession rejects anonymous callers before the handler runs, and puts the
// session where the proxy can find it.
//
// 401 rather than a redirect: everything behind this is fetched by the panel's
// JavaScript, and redirecting an API call to a login page produces an HTML body
// where JSON was expected — which surfaces as a parse error instead of "you are
// signed out".
func (g *Gateway) requireSession(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := g.session(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not signed in"})
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	}
}

// handleMe tells the panel who it is talking as, so it can hide what this role
// cannot use. That is presentation only — every backend enforces the role again,
// and hiding a button is not a permission check.
func (g *Gateway) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":  sess.UserID,
		"username": sess.UserName,
		"role":     sess.Role.String(),
	})
}

func (g *Gateway) logf(format string, args ...any) { log.Printf(format, args...) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Handler exposes the routing table, for tests and for wrapping.
func (g *Gateway) Handler() http.Handler { return g.mux }
