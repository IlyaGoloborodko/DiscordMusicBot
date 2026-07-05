package aiService

// PlayerTools is the set of playback capabilities the bot exposes to the agent.
// The agent (running in the AI service) reads these from the request and returns
// tool_calls; the bot executes them. Adding a capability here — not in the AI
// service — is all it takes to teach the agent a new action.
func PlayerTools() []Tool {
	tracksArg := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tracks": map[string]any{
				"type":        "array",
				"description": "Tracks to act on, resolved by your own music search.",
				"items":       trackSchema(),
			},
		},
		"required": []string{"tracks"},
	}
	noArgs := map[string]any{"type": "object", "properties": map[string]any{}}

	return []Tool{
		{Name: ActionPlay, Description: "Play these tracks now, ahead of the current queue.", InputSchema: tracksArg},
		{Name: ActionEnqueue, Description: "Append these tracks to the end of the queue.", InputSchema: tracksArg},
		{Name: ActionReplaceQueue, Description: "Replace the entire queue with these tracks.", InputSchema: tracksArg},
		{Name: ActionPause, Description: "Pause playback (keeps the queue).", InputSchema: noArgs},
		{Name: ActionResume, Description: "Resume paused playback.", InputSchema: noArgs},
		{Name: ActionSkip, Description: "Skip the current track.", InputSchema: noArgs},
		{Name: ActionStop, Description: "Stop playback and clear the queue.", InputSchema: noArgs},
		{Name: ActionVolumeUp, Description: "Turn the volume up by one step (louder).", InputSchema: noArgs},
		{Name: ActionVolumeDown, Description: "Turn the volume down by one step (quieter).", InputSchema: noArgs},
	}
}

func trackSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"provider": map[string]any{"type": "string"},
			"id":       map[string]any{"type": "string"},
			"title":    map[string]any{"type": "string"},
			"uploader": map[string]any{"type": "string"},
			"url":      map[string]any{"type": "string"},
			"duration": map[string]any{"type": "number"},
		},
		"required": []string{"id", "title"},
	}
}
