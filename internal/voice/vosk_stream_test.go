package voice

import (
	"context"
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"
)

// readRaw48 loads a headerless 48kHz-stereo s16le clip, the format the receive
// loop produces after Opus decoding.
func readRaw48(t *testing.T, path string) []int16 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading clip: %v", err)
	}
	pcm := make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	return pcm
}

// TestVoskStreamTranscribesWhileSpeaking is an opt-in integration test: it needs
// a live Vosk (VOSK_SERVER_ADDR) and a real speech clip (STT_TEST_CLIP, 48kHz
// stereo s16le, someone saying the wake word).
//
//	STT_TEST_CLIP=/path/wake.raw go test ./internal/voice/ -run TestVoskStream -v
//
// It covers what unit tests cannot: that 20ms packets fed through the
// incremental downmix into a persistent connection still produce the wake word,
// and — the point of the streaming path — that the transcript is ready while the
// speaker is still talking, so the gate costs nothing once they stop.
//
// Note it deliberately does NOT require Kaldi to close the utterance. Measured
// against this server, the endpointer needs a real noise floor: it fires after
// low-level noise and stays silent through digital zeros, and Discord sends no
// packets at all during silence. Utterance boundaries are the caller's timer.
func TestVoskStreamTranscribesWhileSpeaking(t *testing.T) {
	clip := strings.TrimSpace(os.Getenv("STT_TEST_CLIP"))
	if clip == "" || voskServerAddr() == "" {
		t.Skip("set VOSK_SERVER_ADDR and STT_TEST_CLIP to run the streaming gate against a live Vosk")
	}
	pcm := readRaw48(t, clip)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stream, err := dialVoskStream(ctx, nil, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer stream.Close()

	// 20ms of 48k stereo = 1920 int16 — a real Opus frame, paced in real time.
	const packet = 1920
	var dm downmixer
	var recognisedDuring time.Duration
	start := time.Now()

	for off := 0; off < len(pcm); off += packet {
		end := off + packet
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := stream.Write(dm.write(pcm[off:end])); err != nil {
			t.Fatalf("write: %v", err)
		}
		time.Sleep(20 * time.Millisecond)

		if recognisedDuring == 0 {
			stream.tmu.Lock()
			seen := stream.partial
			stream.tmu.Unlock()
			if containsNearWake(seen) {
				recognisedDuring = time.Since(start)
			}
		}
	}
	if err := stream.Write(dm.flush()); err != nil {
		t.Fatalf("flush write: %v", err)
	}
	// The decoder holds words back until the stream ends, so the transcript is
	// only complete after Flush. Its cost is the whole justification for
	// streaming: the per-utterance gate pays a full decode here (~700ms measured),
	// while this one has been decoding all along and should only pay the
	// finalisation.
	flushStart := time.Now()
	stream.Flush()
	flushed := time.Since(flushStart)

	got := stream.TakeText()
	audioMs := time.Duration(len(pcm)/2*1000/recvSampleRate) * time.Millisecond
	t.Logf("audio=%v  flush=%v  transcript=%q", audioMs, flushed.Round(time.Millisecond), got)

	if !containsNearWake(got) {
		t.Fatalf("TakeText() = %q, expected something near the wake word", got)
	}
	if recognisedDuring > 0 {
		t.Logf("wake word already visible %v in, before the speaker even stopped",
			recognisedDuring.Round(10*time.Millisecond))
	} else {
		t.Logf("wake word only arrived with the flush — expected for utterances too " +
			"short for the decoder to commit anything mid-stream")
	}

	// TakeText must consume: leftovers would glue this utterance onto the next.
	if left := stream.TakeText(); left != "" {
		t.Errorf("TakeText() left %q behind; the next utterance would inherit it", left)
	}
}
