package voice

import "testing"

// The wake-word gate is a two-stage cascade and the two stages have opposite
// jobs, which is easy to "simplify" back into being broken:
//
//	stage 1 (Vosk, containsNearWake)   — high recall, low precision. The big Vosk
//	                                     model's language model prefers "машина"
//	                                     over the name "марина", so it must accept
//	                                     the confusions too.
//	stage 2 (accurate STT, containsWakeWord) — precision. It throws out whatever
//	                                     stage 1 only misheard as the name.
//
// These cases are transcripts observed from the real gate (or generated through
// it), not invented strings.
func TestWakeCascade(t *testing.T) {
	cases := []struct {
		name      string
		voskHeard string // what the cheap gate emits
		accurate  string // what the accurate model returns for the same audio
		wantNear  bool   // stage 1: spend the accurate model on it?
		wantWake  bool   // stage 2: actually act on it?
	}{
		{
			name:      "misheard as машина is still nominated and then confirmed",
			voskHeard: "я машина как дела",
			accurate:  "Марина, как дела?",
			wantNear:  true,
			wantWake:  true,
		},
		{
			name:      "clean hit",
			voskHeard: "марина привет как дела",
			accurate:  "Марина, привет, как дела?",
			wantNear:  true,
			wantWake:  true,
		},
		{
			name:      "inflected name",
			voskHeard: "но что-то марины как-то",
			accurate:  "Ну а чё там, Марина?",
			wantNear:  true,
			wantWake:  true,
		},
		{
			name:      "a real car is nominated but rejected by the accurate model",
			voskHeard: "моя машина сломалась вчера",
			accurate:  "Моя машина сломалась вчера",
			wantNear:  true,
			wantWake:  false,
		},
		{
			name:      "the band must not wake the bot",
			voskHeard: "включи машину времени",
			accurate:  "Включи Машину времени",
			wantNear:  true,
			wantWake:  false,
		},
		{
			name:      "unrelated chatter never reaches the accurate model",
			voskHeard: "не не необорим что-то херня какая-то",
			accurate:  "",
			wantNear:  false,
			wantWake:  false,
		},
		{
			name:      "silence",
			voskHeard: "",
			accurate:  "",
			wantNear:  false,
			wantWake:  false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := containsNearWake(c.voskHeard); got != c.wantNear {
				t.Errorf("containsNearWake(%q) = %v, want %v", c.voskHeard, got, c.wantNear)
			}
			if got := containsWakeWord(c.accurate); got != c.wantWake {
				t.Errorf("containsWakeWord(%q) = %v, want %v", c.accurate, got, c.wantWake)
			}
		})
	}
}

// Stage 1 must never be narrower than stage 2, or a real wake word could pass the
// strict check while never being nominated for it in the first place.
func TestNearWakeCoversEveryWakeWord(t *testing.T) {
	for _, w := range wakeWords() {
		if !containsNearWake(w) {
			t.Errorf("wake word %q passes containsWakeWord but not containsNearWake — "+
				"stage 1 would drop it before stage 2 ever saw it", w)
		}
	}
}

func TestStripWakeWord(t *testing.T) {
	cases := []struct {
		text        string
		wantCommand string
		wantOK      bool
	}{
		{"Марина, включи музыку", "включи музыку", true},
		{"Марина", "", true}, // name only -> arm
		{"Ну а чё там, Марина, какая-то херня, да?", "какая-то херня, да?", true},
		{"Моя машина сломалась вчера", "", false},
		{"Включи Машину времени", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		cmd, ok := stripWakeWord(c.text)
		if ok != c.wantOK || cmd != c.wantCommand {
			t.Errorf("stripWakeWord(%q) = (%q, %v), want (%q, %v)",
				c.text, cmd, ok, c.wantCommand, c.wantOK)
		}
	}
}

// The prompt is echoed back verbatim as a "transcript" when the model is fed
// non-speech. It contains the wake word, so without this guard the echo becomes a
// phantom command. The OpenAI backend has no vad_filter, so this is the defence.
func TestPromptEchoIsNotACommand(t *testing.T) {
	if got := cleanTranscript("Разговор с ботом Мариной."); got != "" {
		t.Errorf("prompt echo survived cleanTranscript as %q — whisperPrompt's opening "+
			"sentence must stay blacklisted in hallucinationMarkers", got)
	}
}
