package voice

import (
	"slices"
	"strings"
	"testing"
)

func TestWakeWordsFromEnv(t *testing.T) {
	t.Setenv("WAKE_WORDS", "Алиса, Алис")

	got := wakeWords()
	if !slices.Equal(got, []string{"алиса", "алис"}) {
		t.Fatalf("wakeWords() = %v, want the normalized env list", got)
	}
	// Normalization is the point: .env is written by a human, transcripts are
	// not, and "Алиса" must match "алиса," in a sentence.
	if !containsWakeWord("Алиса, включи музыку") {
		t.Error("a wake word set in .env with a capital letter did not match a real transcript")
	}
	if containsWakeWord("Марина, включи музыку") {
		t.Error("the old default still triggers after WAKE_WORDS was set")
	}
}

func TestNearWakeWordsFromEnv(t *testing.T) {
	t.Setenv("WAKE_WORDS_NEAR", "алис, лис, alice")

	if !containsNearWake("а лис бегает") {
		t.Error("the loose stage did not match a word from WAKE_WORDS_NEAR")
	}
	if containsNearWake("машина сломалась") {
		t.Error("the old default near-list is still in use")
	}
}

// An empty or comma-only value must not leave the bot deaf to everything.
func TestEmptyWakeListsFallBackToDefaults(t *testing.T) {
	for _, v := range []string{"", "   ", ",,,", " , "} {
		t.Setenv("WAKE_WORDS", v)
		if !containsWakeWord("Марина, привет") {
			t.Errorf("WAKE_WORDS=%q silenced the wake word entirely instead of keeping the defaults", v)
		}
	}
}

func TestPromptFromEnv(t *testing.T) {
	t.Setenv("STT_PROMPT", "Беседа с ботом Алисой. Алиса, включи музыку.")

	if got := whisperPrompt(); !strings.HasPrefix(got, "Беседа") {
		t.Errorf("whisperPrompt() = %q, want the STT_PROMPT value", got)
	}
	// The echo defence follows the prompt instead of being a second copy of it.
	if got := promptEcho(); got != "беседа с ботом алисой" {
		t.Errorf("promptEcho() = %q, want the prompt's first sentence normalized", got)
	}
	if got := cleanTranscript("Беседа с ботом Алисой."); got != "" {
		t.Errorf("cleanTranscript() kept the echoed prompt as %q — it would reach the AI as a command", got)
	}
	// A real utterance that merely mentions the bot is not an echo.
	if got := cleanTranscript("Алиса, включи музыку"); got == "" {
		t.Error("cleanTranscript() dropped a real command")
	}
}

// The default prompt and the default echo marker used to be two hand-kept copies
// of one sentence; deriving one from the other is what keeps them in step.
func TestDefaultPromptEchoIsStillCaught(t *testing.T) {
	if got := cleanTranscript("Разговор с ботом Мариной."); got != "" {
		t.Errorf("cleanTranscript() kept the default prompt echo as %q", got)
	}
}

// Renaming the bot in WAKE_WORDS while leaving WAKE_WORDS_NEAR alone leaves a
// bot that hears its name and does nothing — silently. The startup check is the
// only thing standing between that config and a confusing evening.
func TestWakeConfigCheckCatchesAStrandedRename(t *testing.T) {
	t.Setenv("WAKE_WORDS", "алиса")
	t.Setenv("WAKE_WORDS_NEAR", "мари, машин")

	stranded := false
	for _, w := range wakeWords() {
		if containsNearWake(w) {
			stranded = true
		}
	}
	if stranded {
		t.Fatal("the test config is not actually stranded; it proves nothing")
	}

	// The defaults must of course pass the same check.
	t.Setenv("WAKE_WORDS", "")
	t.Setenv("WAKE_WORDS_NEAR", "")
	ok := false
	for _, w := range wakeWords() {
		if containsNearWake(w) {
			ok = true
		}
	}
	if !ok {
		t.Error("the shipped defaults fail their own consistency check")
	}
}
