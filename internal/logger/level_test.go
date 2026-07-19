package logger

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want Level
		ok   bool
	}{
		{"DEBUG", LevelDebug, true},
		{"info", LevelInfo, true},
		{" Error ", LevelError, true},
		// The AI service's .env sits next to this one and spells it the Python way.
		{"WARNING", LevelWarn, true},
		{"WARN", LevelWarn, true},
		{"OFF", LevelOff, true},
		{"", LevelInfo, false},
		{"громко", LevelInfo, false},
	}
	for _, c := range cases {
		got, ok := ParseLevel(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// A misspelled level must not silence logging — it falls back to the default and
// says so on stderr.
func TestLevelFromEnvFallsBackOnGarbage(t *testing.T) {
	t.Setenv("TG_LOG_LEVEL", "ошибки")
	if got := TelegramLevel(); got != LevelError {
		t.Errorf("TelegramLevel() = %v, want ERROR", got)
	}
}

func TestDefaultLevels(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("TG_LOG_LEVEL", "")

	if got := ConsoleLevel(); got != LevelInfo {
		t.Errorf("ConsoleLevel() = %v, want INFO", got)
	}
	// Telegram is for "something broke", not a live feed.
	if got := TelegramLevel(); got != LevelError {
		t.Errorf("TelegramLevel() = %v, want ERROR", got)
	}
}

// captureConsole redirects the standard logger for one call.
func captureConsole(t *testing.T, fn func()) string {
	t.Helper()

	var buf bytes.Buffer
	flags, writer := log.Flags(), log.Writer()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(writer)
		log.SetFlags(flags)
	}()

	fn()
	return buf.String()
}

func TestConsoleLevelFilters(t *testing.T) {
	t.Setenv("LOG_LEVEL", "WARN")

	out := captureConsole(t, func() {
		Debugf("a debug line")
		Infof("an info line")
		Warnf("a warn line")
		Errorf("an error line")
	})

	for _, quiet := range []string{"a debug line", "an info line"} {
		if strings.Contains(out, quiet) {
			t.Errorf("%q logged below LOG_LEVEL=WARN:\n%s", quiet, out)
		}
	}
	for _, loud := range []string{"WARN a warn line", "ERROR an error line"} {
		if !strings.Contains(out, loud) {
			t.Errorf("%q missing at LOG_LEVEL=WARN:\n%s", loud, out)
		}
	}
}

// Formatting is the point of the f-suffix: a call site passing args must not end
// up with a raw %s on the phone.
func TestArgsAreFormatted(t *testing.T) {
	t.Setenv("LOG_LEVEL", "DEBUG")

	out := captureConsole(t, func() { Errorf("tts error (guild %s): %v", "42", "boom") })
	if !strings.Contains(out, "tts error (guild 42): boom") {
		t.Errorf("arguments were not formatted:\n%s", out)
	}
}

// Send predates the levels and is still used all over the bot; it must keep
// behaving like an error, and must not stop the caller when Telegram is absent.
func TestSendIsAnError(t *testing.T) {
	t.Setenv("LOG_LEVEL", "DEBUG")

	var out string
	err := error(nil)
	out = captureConsole(t, func() { err = Send("search service STREAM ERROR: nope") })

	if err != nil {
		t.Errorf("Send() returned %v with no Telegram configured; call sites treat that as fatal", err)
	}
	if !strings.Contains(out, "ERROR search service STREAM ERROR: nope") {
		t.Errorf("Send() did not log at ERROR:\n%s", out)
	}
}

// With no Telegram configured, logging must still be harmless — this is the
// path every test and every local run without a token takes.
func TestLoggingWithoutTelegramDoesNotPanic(t *testing.T) {
	t.Setenv("TG_LOG_LEVEL", "DEBUG") // forward everything, with nowhere to send it
	captureConsole(t, func() { Errorf("boom") })
}
