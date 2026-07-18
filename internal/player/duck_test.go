package player

import (
	"math"
	"sync"
	"testing"
)

// Ducking drops the music to a quarter while the assistant thinks or speaks.
// That is a bigger step than any single volume level, so if a duck were ever
// left on, the music would stay quiet and every volume change after it would
// look like it did nothing — which is exactly what a stuck duck feels like from
// the outside.
func TestDuckRestoresGain(t *testing.T) {
	p := &Player{}
	p.volume.Store(5)

	plain := p.gain()

	p.Duck()
	if ducked := p.gain(); ducked >= plain {
		t.Fatalf("gain %v while ducked, want below %v", ducked, plain)
	}
	p.Unduck()

	if got := p.gain(); math.Abs(got-plain) > 1e-9 {
		t.Errorf("gain = %v after Duck/Unduck, want %v restored", got, plain)
	}
}

// Ducks nest: the AI thinking and then speaking over the music both duck, and
// the first one finishing must not un-duck while the second is still going.
func TestNestedDucksRestoreOnlyWhenAllClear(t *testing.T) {
	p := &Player{}
	p.volume.Store(5)
	plain := p.gain()

	p.Duck()
	p.Duck()
	p.Unduck()
	if got := p.gain(); got >= plain {
		t.Errorf("gain %v after one of two ducks cleared, want still ducked", got)
	}
	p.Unduck()
	if got := p.gain(); math.Abs(got-plain) > 1e-9 {
		t.Errorf("gain = %v once all ducks cleared, want %v", got, plain)
	}
}

// Concurrent duck/unduck must not leave the music permanently quiet.
func TestDuckIsRaceFree(t *testing.T) {
	p := &Player{}
	p.volume.Store(5)
	plain := p.gain()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.Duck()
			p.Unduck()
		}()
	}
	wg.Wait()

	if got := p.gain(); math.Abs(got-plain) > 1e-9 {
		t.Errorf("gain = %v after balanced concurrent ducking, want %v — a stuck duck "+
			"leaves the music quiet and makes volume changes look ineffective", got, plain)
	}
}

// A raised level really is audible once nothing is ducking — the numbers behind
// the confusing case, kept so the comparison is not lost.
func TestDuckedFiveSoundsLikePlainOne(t *testing.T) {
	p := &Player{}

	p.volume.Store(1)
	plainOne := p.gain()

	p.volume.Store(5)
	p.Duck()
	duckedFive := p.gain()
	p.Unduck()
	plainFive := p.gain()

	if ratio := duckedFive / plainOne; ratio < 1.1 {
		t.Logf("ducked 5 (%.3f) vs plain 1 (%.3f): ratio %.2f", duckedFive, plainOne, ratio)
	}
	if duckedFive > plainOne*2 {
		t.Errorf("ducked 5 is %.3f against plain 1 at %.3f — the premise that they are "+
			"hard to tell apart no longer holds, revisit the guidance", duckedFive, plainOne)
	}
	if plainFive <= plainOne*4 {
		t.Errorf("plain 5 (%.3f) is not clearly above plain 1 (%.3f)", plainFive, plainOne)
	}
}
