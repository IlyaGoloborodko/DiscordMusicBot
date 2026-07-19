package logger

import (
	"os"
	"strings"
)

// Two separate levels, both from .env, mirroring the AI service:
//
//	LOG_LEVEL=INFO      what you see in the console
//	TG_LOG_LEVEL=ERROR  what gets sent to your phone
//
// Telegram is for "something broke", not a live feed, so it defaults to ERROR
// and above. The console defaults to INFO.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	// LevelOff is only reachable by asking for it: TG_LOG_LEVEL=OFF silences
	// Telegram without having to unset the token.
	LevelOff
)

var levelNames = map[Level]string{
	LevelDebug: "DEBUG",
	LevelInfo:  "INFO",
	LevelWarn:  "WARN",
	LevelError: "ERROR",
	LevelOff:   "OFF",
}

func (l Level) String() string {
	if s, ok := levelNames[l]; ok {
		return s
	}
	return "INFO"
}

// ParseLevel reads a level name. ok is false for anything unrecognised, so the
// caller can fall back to its default rather than a typo in .env deciding what
// gets logged.
func ParseLevel(s string) (Level, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return LevelDebug, true
	case "INFO":
		return LevelInfo, true
	// WARNING is what Python's logging calls it, and this .env sits next to a
	// service that uses that spelling.
	case "WARN", "WARNING":
		return LevelWarn, true
	case "ERROR":
		return LevelError, true
	case "OFF", "NONE":
		return LevelOff, true
	}
	return LevelInfo, false
}

// levelFromEnv reads a level, falling back to def when unset or misspelled.
func levelFromEnv(name string, def Level) Level {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	lvl, ok := ParseLevel(raw)
	if !ok {
		// Not through the logger: this runs while the logger is being set up.
		os.Stderr.WriteString("[log] " + name + "=" + raw + " is not a level, using " + def.String() + "\n")
		return def
	}
	return lvl
}
