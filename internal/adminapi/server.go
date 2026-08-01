// Package adminapi serves the bot's side of the admin panel: what it is playing
// right now, and the operational history of the speech pipeline.
//
// It is read-only. Nothing here can change playback, settings or state — the
// panel is something to look at while diagnosing, and the first version has no
// way to make things worse by being looked at.
//
// Exposure model: this listens on the loopback interface by default and is
// never published outside the compose network. User authentication (Discord
// OAuth2) belongs to the gateway; what arrives here is a service token plus the
// identity headers the gateway attached. See auth.go for why that order is not
// negotiable.
package adminapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"discordAudio/internal/logger"
	"discordAudio/internal/player"
	"discordAudio/internal/telemetry"
	"discordAudio/internal/voice"
)

// defaultAddr binds loopback only. A default of ":8090" would publish the panel
// on every interface of the host the moment somebody set nothing at all.
const defaultAddr = "127.0.0.1:8090"

// playerStateTimeout bounds how long a request waits for one player's run loop.
// A player busy fetching a stream answers in milliseconds; one that never
// answers is a bug worth surfacing as such, not worth hanging the panel on.
const playerStateTimeout = 2 * time.Second

// Deps are the live pieces of the bot the API reports on.
//
// VoiceStatus is injected as a function rather than read from the voice package
// directly so tests can supply a fixture: the real registry is a package global
// populated by actual Discord connections, which a test cannot produce.
type Deps struct {
	Players     *player.Manager
	Events      *telemetry.Store
	VoiceStatus func() []voice.VoiceStatus
}

type Server struct {
	deps      Deps
	token     string
	startedAt time.Time
	srv       *http.Server
}

// New builds the server. addr empty means the API is disabled.
//
// An empty token is refused rather than treated as "no auth needed": this
// process ends up behind a gateway that is reachable from the internet, and an
// unauthenticated admin endpoint is not a configuration anybody should be able
// to reach by leaving a variable unset.
func New(addr, token string, deps Deps) (*Server, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, nil
	}
	if strings.TrimSpace(token) == "" {
		return nil, errNoToken
	}

	s := &Server{deps: deps, token: token, startedAt: time.Now()}
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s, nil
}

type configError string

func (e configError) Error() string { return string(e) }

const errNoToken = configError("ADMIN_API_KEY is empty: refusing to serve the admin API without a token")

// NewFromEnv builds the server from ADMIN_ADDR and ADMIN_API_KEY. A nil server
// with a nil error means the API was not asked for.
//
// The API is switched on by supplying a secret, not by default. Three cases,
// and the middle one is the point:
//
//   - no ADMIN_API_KEY, no ADMIN_ADDR: off, silently. An unconfigured bot should
//     not log an error about a feature nobody asked for.
//   - ADMIN_ADDR set, no key: an error. Somebody asked for the API and left the
//     token out, and quietly serving that would be the worst possible reading of
//     their intent.
//   - key set: serve, on ADMIN_ADDR or the loopback default.
//
// There is deliberately no combination that serves without a token.
func NewFromEnv(deps Deps) (*Server, error) {
	token := strings.TrimSpace(os.Getenv("ADMIN_API_KEY"))
	addr := strings.TrimSpace(os.Getenv("ADMIN_ADDR"))

	if token == "" {
		if addr != "" {
			return nil, errNoToken
		}
		return nil, nil
	}
	if addr == "" {
		addr = defaultAddr
	}
	return New(addr, token, deps)
}

// Handler is the routing table. net/http's own mux has matched methods and
// paths since Go 1.22, so there is no router dependency here — and adding one
// would be the first new module in a go.mod that has stayed at six direct
// requirements, on a box where images are built with a single core.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/health", s.guard(s.handleHealth))
	mux.HandleFunc("GET /admin/state", s.guard(s.handleState))
	mux.HandleFunc("GET /admin/events", s.guard(s.handleEvents))
	mux.HandleFunc("GET /admin/stats/stt", s.guard(s.handleSTTStats))
	mux.HandleFunc("GET /admin/stats/agent", s.guard(s.handleAgentStats))
	return mux
}

// guard authenticates before the handler runs, so no route can be added that
// forgets to.
func (s *Server) guard(h func(http.ResponseWriter, *http.Request, Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.authenticate(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r, id)
	}
}

// Start serves in the background. Failures are logged, never fatal: the panel
// is a diagnostic, and a port collision must not stop the music.
func (s *Server) Start() {
	if s == nil {
		return
	}
	go func() {
		logger.Infof("[admin] listening on %s", s.srv.Addr)
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("[admin] server stopped: %v", err)
		}
	}()
}

// Shutdown stops accepting requests and waits briefly for the ones in flight.
func (s *Server) Shutdown(ctx context.Context) {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		log.Printf("[admin] shutdown: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[admin] writing response: %v", err)
	}
}
