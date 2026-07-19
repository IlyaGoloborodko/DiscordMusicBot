package logger

import (
	"fmt"
	"log"
)

// ConsoleLevel and TelegramLevel are read per call rather than cached at start,
// so a level can be changed by restarting the process alone — and so tests can
// set them. Both are one os.Getenv; nothing here is on the audio path.
func ConsoleLevel() Level  { return levelFromEnv("LOG_LEVEL", LevelInfo) }
func TelegramLevel() Level { return levelFromEnv("TG_LOG_LEVEL", LevelError) }

// Debugf, Infof, Warnf, Errorf write one line to the console and, if it is
// severe enough, forward the same line to Telegram. This is the only place that
// decides where a log line ends up; callers just say how bad it is.
func Debugf(format string, args ...any) { logAt(LevelDebug, format, args...) }
func Infof(format string, args ...any)  { logAt(LevelInfo, format, args...) }
func Warnf(format string, args ...any)  { logAt(LevelWarn, format, args...) }
func Errorf(format string, args ...any) { logAt(LevelError, format, args...) }

func logAt(lvl Level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)

	if lvl >= ConsoleLevel() {
		log.Printf("%s %s", lvl, msg)
	}
	if tgLevel := TelegramLevel(); tgLevel != LevelOff && lvl >= tgLevel {
		// Errors are dropped on purpose: an unreachable Telegram (or no
		// Telegram configured at all) must not turn into a second failure at
		// every call site, and the console has the line either way.
		_ = send(lvl.String() + " — " + msg)
	}
}
