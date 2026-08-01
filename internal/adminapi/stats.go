package adminapi

import (
	"net/http"
	"sort"

	"discordAudio/internal/telemetry"
)

// allEvents is the widest query the ring can answer. Aggregates are computed
// over whatever it currently holds, which is why every response carries a
// window saying how far back that reaches.
func (s *Server) allEvents(f telemetry.Filter) []telemetry.Event {
	f.Limit = s.deps.Events.Info().Capacity
	if f.Limit <= 0 {
		f.Limit = 1
	}
	return s.deps.Events.Query(f)
}

type gateStats struct {
	Total     int `json:"total"`
	Dropped   int `json:"dropped"`
	Nominated int `json:"nominated"`
}

type commandStats struct {
	Total      int `json:"total"`
	Delivered  int `json:"delivered"`
	Wake       int `json:"wake"`
	FalseAlarm int `json:"false_alarm"`
	Empty      int `json:"empty"`
	Errors     int `json:"errors"`
}

type sttStatsResponse struct {
	Window  window       `json:"window"`
	Gate    gateStats    `json:"gate"`
	Command commandStats `json:"command"`

	// FalseAlarmRate is false alarms over everything the gate nominated: the
	// share of paid transcriptions that turned out not to be the wake word. This
	// is the number that says whether the loose first stage is tuned sanely —
	// zero means it is probably too strict and is missing real wake words, which
	// no other statistic here would reveal.
	FalseAlarmRate float64 `json:"false_alarm_rate"`
}

func (s *Server) handleSTTStats(w http.ResponseWriter, r *http.Request, _ Identity) {
	f, err := parseFilter(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	f.Kind = "" // aggregate needs both gate and command events

	var resp sttStatsResponse
	for _, e := range s.allEvents(f) {
		switch e.Kind {
		case telemetry.KindSTTGate:
			resp.Gate.Total++
			switch e.Outcome {
			case telemetry.OutcomeDropped:
				resp.Gate.Dropped++
			case telemetry.OutcomeNominated:
				resp.Gate.Nominated++
			}
		case telemetry.KindSTTCommand:
			resp.Command.Total++
			switch e.Outcome {
			case telemetry.OutcomeDelivered:
				resp.Command.Delivered++
			case telemetry.OutcomeWake:
				resp.Command.Wake++
			case telemetry.OutcomeFalseAlarm:
				resp.Command.FalseAlarm++
			case telemetry.OutcomeEmpty:
				resp.Command.Empty++
			case telemetry.OutcomeError:
				resp.Command.Errors++
			}
		}
	}
	if resp.Gate.Nominated > 0 {
		resp.FalseAlarmRate = float64(resp.Command.FalseAlarm) / float64(resp.Gate.Nominated)
	}
	resp.Window = windowOf(s.deps.Events.Info())

	writeJSON(w, http.StatusOK, resp)
}

type latencyStats struct {
	P50 int64 `json:"p50_ms"`
	P95 int64 `json:"p95_ms"`
	Max int64 `json:"max_ms"`
}

type agentStatsResponse struct {
	Window    window         `json:"window"`
	Total     int            `json:"total"`
	OK        int            `json:"ok"`
	Errors    int            `json:"errors"`
	ByTrigger map[string]int `json:"by_trigger"`
	ByAction  map[string]int `json:"by_action"`
	Latency   latencyStats   `json:"latency"`
}

func (s *Server) handleAgentStats(w http.ResponseWriter, r *http.Request, _ Identity) {
	f, err := parseFilter(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	f.Kind = telemetry.KindAICall

	resp := agentStatsResponse{
		ByTrigger: map[string]int{},
		ByAction:  map[string]int{},
	}
	var latencies []int64
	for _, e := range s.allEvents(f) {
		resp.Total++
		if e.Trigger != "" {
			resp.ByTrigger[e.Trigger]++
		}
		switch e.Outcome {
		case telemetry.OutcomeError:
			resp.Errors++
		default:
			resp.OK++
			if e.Action != "" {
				resp.ByAction[e.Action]++
			}
		}
		// Failed calls are timed too: a timeout is a latency fact, and dropping
		// them would make the numbers look best exactly when things are worst.
		if e.LatencyMs > 0 {
			latencies = append(latencies, e.LatencyMs)
		}
	}
	resp.Latency = percentiles(latencies)
	resp.Window = windowOf(s.deps.Events.Info())

	writeJSON(w, http.StatusOK, resp)
}

// percentiles uses nearest-rank on the sorted samples. Exact, no interpolation,
// and correct for the handful of samples a ring of this size holds — an
// interpolating estimator would invent precision that is not there.
func percentiles(v []int64) latencyStats {
	if len(v) == 0 {
		return latencyStats{}
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })

	at := func(p float64) int64 {
		i := int(p * float64(len(v)))
		if i >= len(v) {
			i = len(v) - 1
		}
		return v[i]
	}
	return latencyStats{P50: at(0.50), P95: at(0.95), Max: v[len(v)-1]}
}
