package adminapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discordAudio/internal/telemetry"
	"discordAudio/internal/voice"
)

const testToken = "s3cret-service-token"

func newTestServer(t *testing.T) (*Server, *telemetry.Store) {
	t.Helper()

	events := telemetry.New(telemetry.Config{MaxEvents: 100})
	t.Cleanup(events.Close)

	s, err := New("127.0.0.1:0", testToken, Deps{
		Events:      events,
		VoiceStatus: func() []voice.VoiceStatus { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, events
}

func get(t *testing.T, s *Server, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestRequiresServiceToken(t *testing.T) {
	s, _ := newTestServer(t)

	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no headers at all", nil, http.StatusUnauthorized},
		{"wrong token", map[string]string{HeaderToken: "guess"}, http.StatusUnauthorized},
		{"empty token", map[string]string{HeaderToken: ""}, http.StatusUnauthorized},
		{"valid token", map[string]string{HeaderToken: testToken}, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := get(t, s, "/admin/health", c.headers).Code; got != c.want {
				t.Errorf("GET /admin/health = %d, want %d", got, c.want)
			}
		})
	}
}

// The identity headers are set by the gateway AFTER it authenticated the user.
// On the wire they are just headers, so anything that reaches this port must not
// be able to declare itself owner by sending one.
func TestRoleHeaderIsWorthlessWithoutTheToken(t *testing.T) {
	s, _ := newTestServer(t)

	rec := get(t, s, "/admin/events", map[string]string{
		HeaderRole:   "owner",
		HeaderUserID: "210198522245545986",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a request with a forged owner role and no token got %d, want 401", rec.Code)
	}
}

func TestUnknownRoleFallsBackToViewer(t *testing.T) {
	for _, role := range []string{"", "superadmin", "OWNER ", "admin", "moderator"} {
		got := ParseRole(role)
		want := RoleViewer
		if role == "OWNER " {
			want = RoleOwner // trimmed and lowercased, so this one IS an owner
		}
		if role == "moderator" {
			want = RoleModerator
		}
		if got != want {
			t.Errorf("ParseRole(%q) = %v, want %v", role, got, want)
		}
	}
}

// Transcripts are what people said in a voice channel. Only the owner role sees
// them; everyone else gets the same event with the decisions intact.
func TestTranscriptsAreOwnerOnly(t *testing.T) {
	s, events := newTestServer(t)
	events.Record(telemetry.Event{
		Kind: telemetry.KindSTTCommand, GuildID: "g1", UserID: "u1",
		SpeechMs: 1120, Gate: "марина привет", Text: "Марина, привет.",
		Command: "привет", Outcome: telemetry.OutcomeDelivered,
	})

	for _, c := range []struct {
		role      string
		wantWords bool
	}{
		{"owner", true},
		{"moderator", false},
		{"viewer", false},
		{"", false},
	} {
		t.Run("role="+c.role, func(t *testing.T) {
			rec := get(t, s, "/admin/events", map[string]string{
				HeaderToken: testToken, HeaderRole: c.role,
			})
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}

			var resp eventsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if len(resp.Events) != 1 {
				t.Fatalf("got %d events, want 1", len(resp.Events))
			}
			e := resp.Events[0]

			if hasWords := e.Text != "" || e.Gate != "" || e.Command != ""; hasWords != c.wantWords {
				t.Errorf("transcripts present = %v, want %v (event: %+v)", hasWords, c.wantWords, e)
			}
			if resp.Redacted == c.wantWords {
				t.Errorf("redacted flag = %v, which contradicts the body", resp.Redacted)
			}
			// The operational facts must survive redaction, or the endpoint is
			// useless to exactly the people who use it most.
			if e.SpeechMs != 1120 || e.Outcome != telemetry.OutcomeDelivered || e.UserID != "u1" {
				t.Errorf("redaction removed the decision, not just the words: %+v", e)
			}
		})
	}
}

func TestSTTStatsCountsOutcomes(t *testing.T) {
	s, events := newTestServer(t)

	rec := func(kind, outcome string) {
		events.Record(telemetry.Event{Kind: kind, Outcome: outcome})
	}
	// Three utterances the gate threw away, two it nominated; of those one was a
	// real command and one a false alarm.
	rec(telemetry.KindSTTGate, telemetry.OutcomeDropped)
	rec(telemetry.KindSTTGate, telemetry.OutcomeDropped)
	rec(telemetry.KindSTTGate, telemetry.OutcomeDropped)
	rec(telemetry.KindSTTGate, telemetry.OutcomeNominated)
	rec(telemetry.KindSTTGate, telemetry.OutcomeNominated)
	rec(telemetry.KindSTTCommand, telemetry.OutcomeDelivered)
	rec(telemetry.KindSTTCommand, telemetry.OutcomeFalseAlarm)

	var resp sttStatsResponse
	body := get(t, s, "/admin/stats/stt", map[string]string{HeaderToken: testToken}).Body
	if err := json.Unmarshal(body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if resp.Gate.Total != 5 || resp.Gate.Dropped != 3 || resp.Gate.Nominated != 2 {
		t.Errorf("gate stats = %+v, want total 5 / dropped 3 / nominated 2", resp.Gate)
	}
	if resp.Command.Delivered != 1 || resp.Command.FalseAlarm != 1 {
		t.Errorf("command stats = %+v, want 1 delivered and 1 false alarm", resp.Command)
	}
	if resp.FalseAlarmRate != 0.5 {
		t.Errorf("FalseAlarmRate = %v, want 0.5 (one false alarm out of two nominations)", resp.FalseAlarmRate)
	}
}

func TestAgentStatsSeparatesErrorsAndTimesThemToo(t *testing.T) {
	s, events := newTestServer(t)

	events.Record(telemetry.Event{Kind: telemetry.KindAICall, Trigger: "user", Outcome: telemetry.OutcomeOK, LatencyMs: 100, Action: "play"})
	events.Record(telemetry.Event{Kind: telemetry.KindAICall, Trigger: "user", Outcome: telemetry.OutcomeOK, LatencyMs: 200})
	events.Record(telemetry.Event{Kind: telemetry.KindAICall, Trigger: "autoplay", Outcome: telemetry.OutcomeError, LatencyMs: 9000, Err: "connection refused"})

	var resp agentStatsResponse
	body := get(t, s, "/admin/stats/agent", map[string]string{HeaderToken: testToken}).Body
	if err := json.Unmarshal(body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if resp.Total != 3 || resp.OK != 2 || resp.Errors != 1 {
		t.Errorf("total/ok/errors = %d/%d/%d, want 3/2/1", resp.Total, resp.OK, resp.Errors)
	}
	if resp.ByTrigger["user"] != 2 || resp.ByTrigger["autoplay"] != 1 {
		t.Errorf("ByTrigger = %v", resp.ByTrigger)
	}
	if resp.ByAction["play"] != 1 {
		t.Errorf("ByAction = %v, want play counted once", resp.ByAction)
	}
	// The 9s timeout must be in the latency numbers: dropping failed calls would
	// make the graph look best exactly when the service is worst.
	if resp.Latency.Max != 9000 {
		t.Errorf("Latency.Max = %d, want 9000 — failed calls are timed too", resp.Latency.Max)
	}
}

// Aggregates are computed over a fixed-size ring, so a response that does not
// say how far back it reached would imply it counted everything.
func TestStatsReportTheirWindow(t *testing.T) {
	events := telemetry.New(telemetry.Config{MaxEvents: 2})
	t.Cleanup(events.Close)
	s, err := New("127.0.0.1:0", testToken, Deps{Events: events})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 0; i < 5; i++ {
		events.Record(telemetry.Event{Kind: telemetry.KindSTTGate, Outcome: telemetry.OutcomeDropped})
	}

	var resp sttStatsResponse
	body := get(t, s, "/admin/stats/stt", map[string]string{HeaderToken: testToken}).Body
	if err := json.Unmarshal(body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !resp.Window.Truncated {
		t.Error("Window.Truncated is false although the ring overflowed — the panel would present a partial count as complete")
	}
	if resp.Gate.Total != 2 {
		t.Errorf("counted %d gate events, want 2 (the ring only holds 2)", resp.Gate.Total)
	}
}

func TestFilterParsing(t *testing.T) {
	s, events := newTestServer(t)
	events.Record(telemetry.Event{Kind: telemetry.KindSTTGate, GuildID: "g1"})
	events.Record(telemetry.Event{Kind: telemetry.KindAICall, GuildID: "g2"})

	cases := []struct {
		query      string
		wantStatus int
		wantEvents int
	}{
		{"", http.StatusOK, 2},
		{"?kind=stt_gate", http.StatusOK, 1},
		{"?guild_id=g2", http.StatusOK, 1},
		{"?since=1h", http.StatusOK, 2},
		{"?since=nonsense", http.StatusBadRequest, 0},
		{"?limit=-1", http.StatusBadRequest, 0},
		{"?limit=notanumber", http.StatusBadRequest, 0},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			rec := get(t, s, "/admin/events"+c.query, map[string]string{
				HeaderToken: testToken, HeaderRole: "owner",
			})
			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, c.wantStatus, rec.Body)
			}
			if c.wantStatus != http.StatusOK {
				return
			}
			var resp eventsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decoding: %v", err)
			}
			if len(resp.Events) != c.wantEvents {
				t.Errorf("got %d events, want %d", len(resp.Events), c.wantEvents)
			}
		})
	}
}

// There must be no configuration in which the API serves without a token.
func TestServerRefusesToServeWithoutAToken(t *testing.T) {
	if _, err := New("127.0.0.1:0", "", Deps{}); err == nil {
		t.Error("New accepted an empty token — an unauthenticated admin endpoint must be impossible to configure")
	}
	if _, err := New("127.0.0.1:0", "   ", Deps{}); err == nil {
		t.Error("New accepted a whitespace-only token")
	}

	// No address and no token means the feature was not asked for at all.
	t.Setenv("ADMIN_ADDR", "")
	t.Setenv("ADMIN_API_KEY", "")
	s, err := NewFromEnv(Deps{})
	if s != nil || err != nil {
		t.Errorf("NewFromEnv with nothing set returned (%v, %v), want (nil, nil)", s, err)
	}

	// An address without a token is a misconfiguration worth failing loudly on.
	t.Setenv("ADMIN_ADDR", "0.0.0.0:8090")
	if _, err := NewFromEnv(Deps{}); err == nil {
		t.Error("NewFromEnv accepted ADMIN_ADDR with no ADMIN_API_KEY")
	}
}

func TestHealthReportsUptimeAndTelemetryWindow(t *testing.T) {
	s, events := newTestServer(t)
	events.Record(telemetry.Event{Kind: telemetry.KindSTTGate})

	var resp healthResponse
	body := get(t, s, "/admin/health", map[string]string{HeaderToken: testToken}).Body
	if err := json.Unmarshal(body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("Status = %q", resp.Status)
	}
	if resp.Telemetry.Buffered != 1 || resp.Telemetry.Capacity != 100 {
		t.Errorf("telemetry info = %+v, want 1 buffered of 100", resp.Telemetry)
	}
	if resp.StartedAt.IsZero() {
		t.Error("StartedAt is zero")
	}
}

// A nil Players manager must not panic: main can wire the API before anything
// has ever played, and a diagnostic that crashes the bot is worse than no
// diagnostic.
func TestStateSurvivesEmptyDeps(t *testing.T) {
	s, _ := newTestServer(t)
	if rec := get(t, s, "/admin/state", map[string]string{HeaderToken: testToken}); rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/state = %d, body %s", rec.Code, rec.Body)
	}
}

// The handler tests above call ServeHTTP directly. This one goes over a real
// socket with a real client, which is the only way the routing patterns, the
// status codes and the JSON encoding are all exercised the way the gateway will
// exercise them.
func TestOverARealSocket(t *testing.T) {
	s, events := newTestServer(t)
	events.Record(telemetry.Event{
		Kind: telemetry.KindSTTGate, GuildID: "569258780152430592", UserID: "210198522245545986",
		SpeechMs: 1120, Gate: "марина привет", Near: true, Outcome: telemetry.OutcomeNominated,
	})
	events.Record(telemetry.Event{
		Kind: telemetry.KindAICall, GuildID: "569258780152430592",
		Trigger: "user", LatencyMs: 412, ToolCalls: 0, Action: "none", Outcome: telemetry.OutcomeOK,
	})

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	for _, path := range []string{"/admin/health", "/admin/state", "/admin/events", "/admin/stats/stt", "/admin/stats/agent"} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatalf("%s: building request: %v", path, err)
		}
		req.Header.Set(HeaderToken, testToken)
		req.Header.Set(HeaderRole, "owner")

		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (%s)", path, resp.StatusCode, body)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("GET %s Content-Type = %q, want JSON", path, ct)
		}
		if !json.Valid(body) {
			t.Errorf("GET %s returned invalid JSON: %s", path, body)
		}
		t.Logf("GET %s\n%s", path, body)
	}

	// And the same socket must reject an unauthenticated caller.
	resp, err := srv.Client().Get(srv.URL + "/admin/events")
	if err != nil {
		t.Fatalf("unauthenticated request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET /admin/events = %d, want 401", resp.StatusCode)
	}
}
