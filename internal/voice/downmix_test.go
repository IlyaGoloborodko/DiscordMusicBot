package voice

import (
	"math"
	"math/rand"
	"testing"
)

func streamAll(pcm []int16, chunkSamples int) []int16 {
	var d downmixer
	var out []int16
	for off := 0; off < len(pcm); off += chunkSamples {
		end := off + chunkSamples
		if end > len(pcm) {
			end = len(pcm)
		}
		out = append(out, d.write(pcm[off:end])...)
	}
	return append(out, d.flush()...)
}

// The streaming downmixer must be indistinguishable from the batch one, whatever
// the packet sizes. If it is not, the difference lands exactly at packet edges —
// a periodic click through the fricative band, which is where "марина" and
// "машина" are told apart.
func TestDownmixerMatchesBatch(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	pcm := make([]int16, 48000*2) // 1s of 48k stereo
	for i := range pcm {
		pcm[i] = int16(rng.Intn(20000) - 10000)
	}

	want := downmixTo16kMono(pcm)

	// 1920 = a real 20ms Opus frame; the rest are awkward on purpose (odd, tiny,
	// not multiples of the decimation factor).
	for _, chunk := range []int{1920, 2, 6, 100, 961, 4801, len(pcm)} {
		got := streamAll(pcm, chunk)
		if len(got) != len(want) {
			t.Errorf("chunk=%d: got %d samples, want %d", chunk, len(got), len(want))
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("chunk=%d: sample %d = %d, want %d (streaming diverges from batch)",
					chunk, i, got[i], want[i])
				break
			}
		}
	}
}

// A tone must survive with its shape intact: if the carry-over state were wrong,
// packet edges would show up as broadband noise on a pure input.
func TestDownmixerKeepsToneClean(t *testing.T) {
	const freq = 440.0
	pcm := make([]int16, 48000*2)
	for i := 0; i < len(pcm)/2; i++ {
		v := int16(8000 * math.Sin(2*math.Pi*freq*float64(i)/48000))
		pcm[i*2] = v
		pcm[i*2+1] = v
	}

	got := streamAll(pcm, 1920)
	want := downmixTo16kMono(pcm)
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %d, want %d — packet boundary artefact on a pure tone",
				i, got[i], want[i])
		}
	}
}

func TestDownmixerHandlesEmptyAndShortInput(t *testing.T) {
	var d downmixer
	if got := d.write(nil); got != nil {
		t.Errorf("write(nil) = %v, want nil", got)
	}
	if got := d.flush(); got != nil {
		t.Errorf("flush of an empty downmixer = %v, want nil", got)
	}

	// A single stereo frame is not enough to fill a window; it must come out on
	// flush rather than be silently dropped.
	var d2 downmixer
	d2.write([]int16{100, 100})
	if got := d2.flush(); len(got) != 1 {
		t.Errorf("flush after one stereo frame produced %d samples, want 1", len(got))
	}
}
