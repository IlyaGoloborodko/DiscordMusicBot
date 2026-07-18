package player

import (
	"encoding/json"
	"testing"

	"discordAudio/internal/aiService"
)

// The exact response the service sent on 2026-07-19 00:47:32, after the user
// said "Марина, поставь громкость на пять" at volume 1. The tool call and its
// arguments are correct, so if the volume did not move the fault is on this side
// of the wire.
const liveSetVolumeResponse = `{"spoken_answer":"","display_text":"Ставлю громкость на 5 🔊","clarification":null,` +
	`"tool_calls":[{"name":"set_volume","arguments":{"level":5}}]}`

func TestApplyAgentAppliesLiveSetVolume(t *testing.T) {
	var resp aiService.AgentResponse
	if err := json.Unmarshal([]byte(liveSetVolumeResponse), &resp); err != nil {
		t.Fatalf("decoding the recorded response: %v", err)
	}

	if level, ok := resp.VolumeLevel(); !ok || level != 5 {
		t.Fatalf("VolumeLevel() = (%d, %v), want (5, true) — the tool call did not survive decoding", level, ok)
	}

	p := &Player{
		guildID: "g1",
		cmdCh:   make(chan command, 8),
		done:    make(chan struct{}),
		ai:      aiService.NewClient(),
	}
	p.volume.Store(1) // where the user was

	p.ApplyAgent(&resp)

	if got := p.Volume(); got != 5 {
		t.Errorf("Volume() = %d after applying set_volume{level:5}, want 5", got)
	}
}

// gain has to move with the level, or the number changes and nothing is heard.
func TestGainFollowsVolume(t *testing.T) {
	p := &Player{}
	p.volume.Store(1)
	atOne := p.gain()
	p.volume.Store(5)
	atFive := p.gain()

	if atFive <= atOne {
		t.Fatalf("gain did not rise with volume: %v at 1, %v at 5", atOne, atFive)
	}
	if ratio := atFive / atOne; ratio < 4.9 || ratio > 5.1 {
		t.Errorf("gain ratio 1->5 is %.2f, want ~5", ratio)
	}
}
