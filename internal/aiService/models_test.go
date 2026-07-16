package aiService

import (
	"encoding/json"
	"testing"
)

func call(name, args string) ToolCall {
	return ToolCall{Name: name, Arguments: json.RawMessage(args)}
}

// Volume has two paths on purpose, and the split is the whole point:
//
//	relative ("погромче")        -> volume_up/volume_down, the BOT does the sums
//	absolute ("громкость на 6")  -> set_volume, the USER named the number
//
// The agent used to be asked for the resulting level and got it wrong (6 for
// "потише" from 5). Don't merge these back into one "agent decides the level".
func TestVolumeLevel(t *testing.T) {
	cases := []struct {
		name      string
		calls     []ToolCall
		wantLevel int
		wantOK    bool
	}{
		{"exact level", []ToolCall{call(ActionSetVolume, `{"level":6}`)}, 6, true},
		{"no volume tool", []ToolCall{call(ActionPause, `{}`)}, 0, false},
		{"relative is not an exact level", []ToolCall{call(ActionVolumeUp, `{}`)}, 0, false},
		// The player clamps, so out-of-range must pass through rather than be
		// silently dropped — dropping it would leave the volume untouched while
		// the bot says it changed.
		{"out of range passes through to the clamp", []ToolCall{call(ActionSetVolume, `{"level":99}`)}, 99, true},
		{"zero is a real level, not 'unset'", []ToolCall{call(ActionSetVolume, `{"level":0}`)}, 0, true},
		{"missing level is not a request", []ToolCall{call(ActionSetVolume, `{}`)}, 0, false},
		{"malformed arguments are not a request", []ToolCall{call(ActionSetVolume, `{"level":"шесть"}`)}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &AgentResponse{ToolCalls: c.calls}
			level, ok := r.VolumeLevel()
			if ok != c.wantOK || level != c.wantLevel {
				t.Errorf("VolumeLevel() = (%d, %v), want (%d, %v)", level, ok, c.wantLevel, c.wantOK)
			}
		})
	}
}

func TestVolumeDelta(t *testing.T) {
	cases := []struct {
		name  string
		calls []ToolCall
		want  int
	}{
		{"up", []ToolCall{call(ActionVolumeUp, `{}`)}, 1},
		{"down", []ToolCall{call(ActionVolumeDown, `{}`)}, -1},
		{"set_volume is not a delta", []ToolCall{call(ActionSetVolume, `{"level":6}`)}, 0},
		{"none", []ToolCall{call(ActionSkip, `{}`)}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &AgentResponse{ToolCalls: c.calls}
			if got := (&AgentResponse{ToolCalls: r.ToolCalls}).VolumeDelta(); got != c.want {
				t.Errorf("VolumeDelta() = %d, want %d", got, c.want)
			}
		})
	}
}

// Volume rides alongside the queue action rather than replacing it: "поставь
// громкость на 3 и пропусти трек" must do both.
func TestVolumeIsIndependentOfPrimaryEffect(t *testing.T) {
	r := &AgentResponse{ToolCalls: []ToolCall{
		call(ActionSetVolume, `{"level":3}`),
		call(ActionSkip, `{}`),
	}}
	if level, ok := r.VolumeLevel(); !ok || level != 3 {
		t.Errorf("VolumeLevel() = (%d, %v), want (3, true)", level, ok)
	}
	if action, _ := r.PrimaryEffect(); action != ActionSkip {
		t.Errorf("PrimaryEffect() = %q, want %q", action, ActionSkip)
	}
}

// set_volume must be advertised, or the agent can never call it.
func TestSetVolumeIsAdvertised(t *testing.T) {
	var tool *Tool
	for i, tl := range PlayerTools() {
		if tl.Name == ActionSetVolume {
			tool = &PlayerTools()[i]
			break
		}
	}
	if tool == nil {
		t.Fatalf("PlayerTools() does not advertise %q", ActionSetVolume)
	}
	props, _ := tool.InputSchema["properties"].(map[string]any)
	if _, ok := props["level"]; !ok {
		t.Errorf("%q schema has no 'level' property: %v", ActionSetVolume, tool.InputSchema)
	}
	req, _ := tool.InputSchema["required"].([]string)
	if len(req) != 1 || req[0] != "level" {
		t.Errorf("%q must require 'level', got %v", ActionSetVolume, req)
	}
}
