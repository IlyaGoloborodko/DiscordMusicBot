package aiService

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

// Reasons a track stopped playing. Passed through as observed — the service
// interprets them, the bot does not. In particular a skip is not a dislike: a
// track can be skipped precisely because it played recently.
const (
	ReasonFinished     = "finished"
	ReasonSkipped      = "skipped"
	ReasonStopped      = "stopped"
	ReasonDisconnected = "disconnected"
)

// PlaybackEvent reports that a track actually played. The AI service records a
// track when it hands it over, which over-counts: a queue of five tracks that
// the listener skipped after two still logged five. Only playback knows what was
// really heard, so only the bot can send this.
type PlaybackEvent struct {
	Session    AgentSession `json:"session"`
	TrackID    string       `json:"track_id"`
	Provider   string       `json:"provider,omitempty"`
	PlayedMs   int64        `json:"played_ms"`
	DurationMs int64        `json:"duration_ms,omitempty"`
	Reason     string       `json:"reason,omitempty"`
}

// playbackAddr is where playback reports go. It defaults to AI_SERVICE_ADDR so
// there is no silent misconfiguration, but the endpoint may live on a different
// port than /agent — set PLAYBACK_SERVICE_ADDR when it does.
func playbackAddr() string {
	if a := strings.TrimSpace(os.Getenv("PLAYBACK_SERVICE_ADDR")); a != "" {
		return a
	}
	return strings.TrimSpace(os.Getenv("AI_SERVICE_ADDR"))
}

// ReportPlayback posts a single playback event (POST /playback). The response
// body carries nothing, so it is discarded.
//
// Callers treat this as fire-and-forget: analytics must never delay or break
// playback, and a dropped event is acceptable. There is deliberately no retry
// or queue.
func (c *Client) ReportPlayback(ctx context.Context, ev PlaybackEvent) error {
	addr := playbackAddr()
	if addr == "" {
		return fmt.Errorf("PLAYBACK_SERVICE_ADDR/AI_SERVICE_ADDR is not set")
	}

	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if aiDebug() {
		log.Printf("[ai] -> POST /playback %s", body)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(addr, "/")+"/playback", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("playback report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("playback report: %s", resp.Status)
	}
	return nil
}
