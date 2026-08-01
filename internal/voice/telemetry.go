package voice

import "discordAudio/internal/telemetry"

// speechAllowed reports whether transcripts may be kept in telemetry.
//
// It reuses STT_LOG_LEVEL instead of adding a switch of its own. That variable
// already decides which transcripts are worth writing down at all, and a second
// knob could disagree with it — leaving somebody who set STT_LOG_LEVEL=0 for
// privacy with their speech still being recorded somewhere else.
func speechAllowed() bool { return sttLogLevel() > sttLogSilent }

// recordSTT files a speech-pipeline event, stripping the transcripts when they
// are not allowed to be kept. The operational fields — how long the utterance
// was, what the gate decided, what came of it — are recorded either way: they
// are what makes the history useful and none of them is anybody's words.
func recordSTT(ev telemetry.Event) {
	if !speechAllowed() {
		ev = ev.Redacted()
	}
	telemetry.Record(ev)
}
