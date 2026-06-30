package aiService

// Agent action values returned by POST /agent.
const (
	ActionPlay         = "play"
	ActionEnqueue      = "enqueue"
	ActionReplaceQueue = "replace_queue"
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

type AgentRequest struct {
	Session AgentSession   `json:"session"`
	Message string         `json:"message"`
	Context map[string]any `json:"context,omitempty"`
}

type AgentResponse struct {
	SpokenAnswer  string  `json:"spoken_answer"`
	DisplayText   string  `json:"display_text"`
	Action        string  `json:"action"`
	Tracks        []Track `json:"tracks"`
	Clarification string  `json:"clarification"`
}
