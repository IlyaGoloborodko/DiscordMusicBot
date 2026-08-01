// Package telemetry keeps a short operational history of what the bot decided:
// the wake-word gate's verdicts, the transcripts behind them, and the outcome of
// every AI call.
//
// It exists because the bot kept nothing at all. Diagnosing "why did it ignore
// me" meant grepping hours of `docker logs` for `[stt]` lines by eye — hundreds
// of them per hour from a single open microphone. The same facts recorded as
// data are answerable in one query.
//
// Two rules shape the whole package:
//
//  1. Recording must never slow the caller down. Record() is called from the
//     voice receive loop, where a delay means dropped Opus packets. Events go
//     into memory under a short lock and to disk through a buffered channel;
//     when that channel is full the event is DROPPED rather than waited on. This
//     mirrors the fire-and-forget rule already established for playback reports
//     (internal/player/player.go): analytics must never delay the music.
//
//  2. Transcripts are users' speech. Whether they are captured at all follows
//     STT_LOG_LEVEL, the knob that already decides which transcripts get logged;
//     who may read them back is decided by the admin API, which serves them to
//     the owner role only.
package telemetry

import "time"

// Kind classifies an event. Kept as a small closed set of strings: they end up
// in a JSONL file that is meant to stay readable with grep, and an integer enum
// would make that file useless to a human.
const (
	// KindSTTGate is one verdict of the cheap Vosk gate: what it heard and
	// whether that was close enough to the wake word to pay for the accurate
	// model. This is the event that replaces reading `[stt] … VOSK=…` by eye.
	KindSTTGate = "stt_gate"

	// KindSTTCommand is the accurate transcript and what became of it — a real
	// command, a bare wake word that armed the speaker, or a gate false alarm.
	KindSTTCommand = "stt_command"

	// KindAICall is one round trip to POST /agent. Recorded here rather than in
	// the AI service on purpose: the bot sees what the server cannot, namely the
	// calls that never arrived (connection refused, timeouts).
	KindAICall = "ai_call"

	// KindVoice covers joining, leaving and losing a voice connection.
	KindVoice = "voice"
)

// Outcomes. Deliberately descriptive of what the bot DID, not of what it
// concluded about the user — "false_alarm" is the gate being wrong, never the
// speaker.
const (
	OutcomeDropped    = "dropped"     // nothing near the wake word; stopped at the gate
	OutcomeNominated  = "nominated"   // gate thought it heard the name; sent on to the accurate model
	OutcomeFalseAlarm = "false_alarm" // gate nominated it, the accurate model disagreed
	OutcomeWake       = "wake"        // bare wake word; speaker armed for a follow-up
	OutcomeDelivered  = "delivered"   // a command reached the AI
	OutcomeEmpty      = "empty"       // transcript came back with nothing usable
	OutcomeOK         = "ok"
	OutcomeError      = "error"
)

// Event is one recorded fact. It is a flat union rather than a per-kind type so
// that the JSONL stays one line per event with no nesting, and so a filter can
// be written against any field without knowing the kind. Everything optional is
// omitempty, which keeps lines short and greppable.
type Event struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	GuildID string    `json:"guild_id,omitempty"`
	UserID  string    `json:"user_id,omitempty"`

	// Speech recognition. Gate/Text/Command are transcripts — see the package
	// comment for how they are gated on the way in and on the way out.
	SpeechMs int    `json:"speech_ms,omitempty"`
	Gate     string `json:"gate,omitempty"`
	Near     bool   `json:"near,omitempty"`
	Armed    bool   `json:"armed,omitempty"`
	Text     string `json:"text,omitempty"`
	Command  string `json:"command,omitempty"`

	// AI round trip.
	Trigger   string `json:"trigger,omitempty"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
	ToolCalls int    `json:"tool_calls,omitempty"`
	Action    string `json:"action,omitempty"`

	Outcome string `json:"outcome,omitempty"`
	Err     string `json:"err,omitempty"`
}

// Redacted returns a copy with the speech transcripts removed. The admin API
// hands this to anyone below the owner role: the operational facts — that the
// gate fired, how long the utterance was, what the bot decided — are what a
// moderator needs, and none of them require reading what somebody said.
func (e Event) Redacted() Event {
	e.Gate, e.Text, e.Command = "", "", ""
	return e
}

// Filter selects events for a query. A zero Filter matches everything.
type Filter struct {
	Kind    string
	GuildID string
	UserID  string
	Since   time.Time
	Limit   int
	Offset  int
}

func (f Filter) matches(e Event) bool {
	if f.Kind != "" && e.Kind != f.Kind {
		return false
	}
	if f.GuildID != "" && e.GuildID != f.GuildID {
		return false
	}
	if f.UserID != "" && e.UserID != f.UserID {
		return false
	}
	if !f.Since.IsZero() && e.At.Before(f.Since) {
		return false
	}
	return true
}
