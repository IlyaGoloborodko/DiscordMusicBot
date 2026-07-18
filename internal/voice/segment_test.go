package voice

import "testing"

// keepTail is what stops the cap from cutting a wake word in half. Hitting the
// cap is not an utterance ending — it lands wherever the speaker happens to be —
// so the audio around it has to survive.
func TestKeepTail(t *testing.T) {
	sp := &speaker{}
	sp.append(make([]int16, 1000))
	for i := 0; i < 500; i++ {
		sp.buf[500+i] = int16(i + 1) // mark the last 500 samples
	}

	sp.keepTail(500)

	sp.mu.Lock()
	got := sp.buf
	sp.mu.Unlock()

	if len(got) != 500 {
		t.Fatalf("keepTail(500) left %d samples, want 500", len(got))
	}
	// It must keep the END, not the start: the wake word is at the cut, not
	// back at the beginning of the buffer.
	if got[0] != 1 || got[499] != 500 {
		t.Errorf("kept the wrong slice: first=%d last=%d, want first=1 last=500",
			got[0], got[499])
	}
}

func TestKeepTailShorterThanLimitIsUntouched(t *testing.T) {
	sp := &speaker{}
	sp.append([]int16{1, 2, 3})
	sp.keepTail(500)

	sp.mu.Lock()
	got := len(sp.buf)
	sp.mu.Unlock()
	if got != 3 {
		t.Errorf("keepTail truncated a buffer already under the limit: %d samples, want 3", got)
	}
}

// The tail has to outlast the gate's decoding lag — measured around 2s, so the
// wake word can be spoken and still be missing from the transcript when the cap
// fires. A tail shorter than that would discard the audio that proves it.
func TestCapOverlapOutlastsDecoderLag(t *testing.T) {
	const measuredLagMs = 2000
	overlapMs := capOverlapSamples / recvChannels * 1000 / recvSampleRate
	if overlapMs < measuredLagMs {
		t.Errorf("capOverlapSamples is %dms, shorter than the %dms the gate lags by",
			overlapMs, measuredLagMs)
	}
	// And it must stay well under the cap, or trimming would free nothing and the
	// buffer would sit pinned at its maximum.
	capMs := maxSegmentSamples / recvChannels * 1000 / recvSampleRate
	if overlapMs >= capMs {
		t.Errorf("overlap %dms >= cap %dms; trimming would reclaim nothing", overlapMs, capMs)
	}
}
