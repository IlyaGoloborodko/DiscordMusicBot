package stream

import (
	"bytes"
	"context"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"layeh.com/gopus"
)

// TestPauseIsNotCountedAsPlayed drives the real streaming loop with ffmpeg and a
// stand-in Discord sink, pausing halfway.
//
// This is the acceptance case that fails quietly if played_ms is ever derived
// from elapsed time: the numbers stay plausible, just inflated by however long
// the listener paused. Counting frames makes it structurally impossible, and
// this proves the loop really does stop emitting while paused.
func TestPauseIsNotCountedAsPlayed(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}

	var frames atomic.Int64
	var paused atomic.Bool

	// 10s of silence, generated locally so the test needs no network.
	cmd := exec.Command("ffmpeg",
		"-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-t", "10",
		"-f", "s16le", "-ar", "48000", "-ac", "2", "pipe:1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting ffmpeg: %v", err)
	}

	enc, err := gopus.NewEncoder(48000, 2, gopus.Audio)
	if err != nil {
		t.Fatalf("encoder: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- pumpFrames(ctx, cmd, stdout, &stderr, enc, Controls{
			Paused: paused.Load,
			Frames: &frames,
		}, func([]byte) bool { return true }) // stand-in for Discord
	}()

	// Let it play, then hold it paused far longer than it played.
	time.Sleep(300 * time.Millisecond)
	atPause := frames.Load()
	paused.Store(true)

	time.Sleep(1500 * time.Millisecond)
	afterPause := frames.Load()

	cancel()
	<-done

	if atPause == 0 {
		t.Fatal("no frames were sent before pausing; the test proves nothing")
	}
	// A couple of frames may already have been encoded when the pause landed.
	if grew := afterPause - atPause; grew > 3 {
		t.Errorf("frames grew by %d during a 1.5s pause (%d -> %d); paused time is "+
			"being counted as listening", grew, atPause, afterPause)
	}
}
