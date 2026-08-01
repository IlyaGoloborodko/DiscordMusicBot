package adminapi

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"discordAudio/internal/player"
	"discordAudio/internal/telemetry"
	"discordAudio/internal/voice"
)

type healthResponse struct {
	Status        string         `json:"status"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	StartedAt     time.Time      `json:"started_at"`
	Guilds        int            `json:"guilds"`
	VoiceChannels int            `json:"voice_channels"`
	Telemetry     telemetry.Info `json:"telemetry"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request, _ Identity) {
	resp := healthResponse{
		Status:        "ok",
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		StartedAt:     s.startedAt,
		Telemetry:     s.deps.Events.Info(),
	}
	if s.deps.Players != nil {
		resp.Guilds = len(s.deps.Players.All())
	}
	resp.VoiceChannels = len(s.voiceStatus())
	writeJSON(w, http.StatusOK, resp)
}

// guildState joins what the player knows with what the voice listener knows.
// Either side can be missing and that is not an error: a player outlives the
// voice connection that created it (nothing ever destroys one), and a listener
// can exist in a guild where nothing has been played yet.
type guildState struct {
	GuildID string             `json:"guild_id"`
	Player  *player.State      `json:"player,omitempty"`
	Voice   *voice.VoiceStatus `json:"voice,omitempty"`

	// Unavailable means the player's run loop did not answer in time. Reported
	// rather than rendered as an idle player: "not responding" and "nothing
	// playing" look identical in a UI but mean opposite things.
	Unavailable bool `json:"unavailable,omitempty"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request, _ Identity) {
	byGuild := map[string]*guildState{}
	at := func(guildID string) *guildState {
		if g, ok := byGuild[guildID]; ok {
			return g
		}
		g := &guildState{GuildID: guildID}
		byGuild[guildID] = g
		return g
	}

	if s.deps.Players != nil {
		for _, p := range s.deps.Players.All() {
			g := at(p.GuildID())
			ctx, cancel := context.WithTimeout(r.Context(), playerStateTimeout)
			st, ok := p.State(ctx)
			cancel()
			if !ok {
				g.Unavailable = true
				continue
			}
			g.Player = &st
		}
	}

	for _, vs := range s.voiceStatus() {
		g := at(vs.GuildID)
		g.Voice = &vs
	}

	out := make([]*guildState, 0, len(byGuild))
	for _, g := range byGuild {
		out = append(out, g)
	}
	// Stable order: map iteration would otherwise reshuffle the panel on every
	// refresh, which reads as flicker rather than as data.
	sort.Slice(out, func(i, j int) bool { return out[i].GuildID < out[j].GuildID })

	writeJSON(w, http.StatusOK, map[string]any{"guilds": out})
}

type eventsResponse struct {
	Events   []telemetry.Event `json:"events"`
	Window   window            `json:"window"`
	Redacted bool              `json:"redacted"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, id Identity) {
	f, err := parseFilter(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	events := s.deps.Events.Query(f)

	// Transcripts are what people said in a voice channel. Below owner, the
	// decisions are served without the words — everything an operator needs to
	// tell why the bot did or did not react, and nothing of the conversation.
	redacted := id.Role < RoleOwner
	if redacted {
		for i := range events {
			events[i] = events[i].Redacted()
		}
	}

	writeJSON(w, http.StatusOK, eventsResponse{
		Events:   events,
		Window:   windowOf(s.deps.Events.Info()),
		Redacted: redacted,
	})
}

// window describes how much history the numbers are based on. Reported with
// every aggregate because the buffer is a fixed-size ring: once it is full the
// answer to "how many times did this happen" silently becomes "within the last
// N events", and a panel that does not say so is lying by omission.
type window struct {
	Events    int       `json:"events"`
	Capacity  int       `json:"capacity"`
	Oldest    time.Time `json:"oldest,omitempty"`
	Truncated bool      `json:"truncated"`
}

func windowOf(info telemetry.Info) window {
	return window{
		Events:    info.Buffered,
		Capacity:  info.Capacity,
		Oldest:    info.Oldest,
		Truncated: info.Buffered >= info.Capacity,
	}
}

func (s *Server) voiceStatus() []voice.VoiceStatus {
	if s.deps.VoiceStatus == nil {
		return nil
	}
	return s.deps.VoiceStatus()
}

// parseFilter reads the query string. since accepts either an RFC3339 timestamp
// or a relative duration ("15m", "2h"), because by hand the relative form is the
// one anybody actually types.
func parseFilter(r *http.Request) (telemetry.Filter, error) {
	q := r.URL.Query()
	f := telemetry.Filter{
		Kind:    q.Get("kind"),
		GuildID: q.Get("guild_id"),
		UserID:  q.Get("user_id"),
	}

	if v := q.Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			f.Since = time.Now().Add(-d)
		} else if ts, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = ts
		} else {
			return f, badRequest("since must be a duration like 15m or an RFC3339 timestamp")
		}
	}
	for _, p := range []struct {
		name string
		dst  *int
	}{{"limit", &f.Limit}, {"offset", &f.Offset}} {
		if v := q.Get(p.name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return f, badRequest(p.name + " must be a non-negative number")
			}
			*p.dst = n
		}
	}
	return f, nil
}

type badRequest string

func (e badRequest) Error() string { return string(e) }
