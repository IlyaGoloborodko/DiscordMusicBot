package aiService

import "encoding/json"

// Agent action values. These double as the client tool names, so the bot's tool
// registry and the legacy `action` field share one vocabulary.
const (
	ActionPlay         = "play"
	ActionEnqueue      = "enqueue"
	ActionReplaceQueue = "replace_queue"
	ActionPause        = "pause"
	ActionResume       = "resume"
	ActionSkip         = "skip"
	ActionStop         = "stop"
	ActionVolumeUp     = "volume_up"
	ActionVolumeDown   = "volume_down"
	ActionSetVolume    = "set_volume"
	ActionClarify      = "clarify"
	ActionNone         = "none"
)

// Track mirrors the AI/search service Track schema. The bot resolves the actual
// stream URL just-in-time (via the search service) before playback.
type Track struct {
	Provider string  `json:"provider"`
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Uploader string  `json:"uploader"`
	URL      string  `json:"url"`
	Duration float64 `json:"duration"`
}

type AgentSession struct {
	GuildID   string `json:"guild_id,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	UserName  string `json:"user_name,omitempty"`
}

// Tool describes a client capability the agent may invoke. The bot declares the
// tools it can execute so the AI service stays decoupled from Discord.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// ToolCall is a tool the agent chose to invoke, with its arguments.
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// What caused a call to /agent. The service treats everything except
// TriggerUser as the bot acting on its own, and then refuses to re-serve music
// that just played: repeating the last hour is what a listener asks for, not
// what autoplay should decide by itself.
//
// This is a contract, unlike the "[autoplay]" prefix in Message — that prefix is
// our own formatting and we are free to reword it, which would break a service
// that had come to depend on reading it.
const (
	TriggerUser     = "user"     // a person spoke or typed at the bot
	TriggerAutoplay = "autoplay" // the queue ran dry and the bot asked for more
	TriggerDJBreak  = "dj_break" // the periodic DJ line between tracks
)

type AgentRequest struct {
	Session AgentSession   `json:"session"`
	Message string         `json:"message"`
	Trigger string         `json:"trigger,omitempty"`
	Context map[string]any `json:"context,omitempty"`
	Tools   []Tool         `json:"tools,omitempty"`
}

type AgentResponse struct {
	SpokenAnswer  string     `json:"spoken_answer"`
	DisplayText   string     `json:"display_text"`
	ToolCalls     []ToolCall `json:"tool_calls"`
	Clarification string     `json:"clarification"`

	// Legacy single-action fields, used as a fallback when tool_calls is empty
	// (e.g. autoplay/DJ prompts that don't send tools).
	Action string  `json:"action"`
	Tracks []Track `json:"tracks"`
}

// VolumeDelta returns +1/-1 if the agent asked to raise/lower the volume by one
// step, or 0 if no relative volume tool was called. Applied independently of the
// queue/transport action.
//
// Relative changes are a delta on purpose: the agent used to be asked for the
// resulting level and got the arithmetic wrong (it answered 6 for "потише" from
// 5, and 8 for "тише на 2" from 5). The bot does the sums; the agent only names
// a direction. See VolumeLevel for the case where the *user* names the number.
func (r *AgentResponse) VolumeDelta() int {
	for _, tc := range r.ToolCalls {
		switch tc.Name {
		case ActionVolumeUp:
			return 1
		case ActionVolumeDown:
			return -1
		}
	}
	return 0
}

// VolumeLevel returns the exact level the agent was told to set, and ok=false if
// set_volume wasn't called. Unlike a relative change this asks nothing of the
// agent's arithmetic — the user said the number out loud ("поставь громкость на
// 6") and the agent just carries it across. The player clamps the result, so an
// out-of-range level is harmless.
func (r *AgentResponse) VolumeLevel() (level int, ok bool) {
	for _, tc := range r.ToolCalls {
		if tc.Name != ActionSetVolume {
			continue
		}
		var a struct {
			Level *int `json:"level"`
		}
		if err := json.Unmarshal(tc.Arguments, &a); err != nil || a.Level == nil {
			continue // malformed call — treat as no volume request at all
		}
		return *a.Level, true
	}
	return 0, false
}

// PrimaryEffect reduces the response to the single queue/transport action the
// player applies plus its tracks. Tool calls take precedence over the legacy
// action field.
func (r *AgentResponse) PrimaryEffect() (action string, tracks []Track) {
	for _, tc := range r.ToolCalls {
		switch tc.Name {
		case ActionPlay, ActionEnqueue, ActionReplaceQueue:
			var a struct {
				Tracks []Track `json:"tracks"`
			}
			_ = json.Unmarshal(tc.Arguments, &a)
			return tc.Name, a.Tracks
		case ActionPause, ActionResume, ActionSkip, ActionStop:
			return tc.Name, nil
		}
	}
	if r.Action != "" {
		return r.Action, r.Tracks
	}
	return ActionNone, nil
}
