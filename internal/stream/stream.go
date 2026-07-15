package stream

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gopus"
)

// Controls lets the caller adjust playback live: gain (0..1 volume multiplier,
// used for ducking), paused (hold playback without losing position) and overlay
// (a second PCM source mixed on top, e.g. the assistant talking over the music).
// Any callback may be nil.
type Controls struct {
	Gain   func() float64 // volume multiplier applied per frame; nil => 1.0
	Paused func() bool    // when true, stop sending frames until it clears; nil => never
	// Overlay returns up to n samples of 48kHz-stereo PCM to mix into the next
	// frame, or nil when there is nothing to overlay. Mixed after Gain, so the
	// overlay stays at full volume while the music underneath is ducked.
	Overlay func(n int) []int16
}

// TranscodePCM decodes an audio stream (raw s16le at the given rate/channels)
// into 48kHz-stereo PCM held in memory — used to prepare a TTS clip for mixing.
func TranscodePCM(ctx context.Context, audio io.Reader, sampleRate, channels int) ([]int16, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-f", "s16le",
		"-ar", strconv.Itoa(sampleRate),
		"-ac", strconv.Itoa(channels),
		"-i", "pipe:0",
		"-f", "s16le",
		"-ar", "48000",
		"-ac", "2",
		"pipe:1",
	)
	cmd.Stdin = audio

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg transcode: %w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}

	pcm := make([]int16, len(out)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(out[i*2:]))
	}
	return pcm, nil
}

// mixSample adds two samples with saturation, so the ducked music plus the
// assistant's voice can't wrap around into distortion.
func mixSample(a, b int16) int16 {
	v := int32(a) + int32(b)
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
}

// StartStreaming plays an audio URL into the voice connection until it ends or
// ctx is cancelled (cancellation is how the player implements skip/stop).
func StartStreaming(ctx context.Context, vc *discordgo.VoiceConnection, url string, ctrl Controls) error {
	cmd := exec.Command("ffmpeg",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "5",
		"-i", url,
		"-f", "s16le",
		"-ar", "48000",
		"-ac", "2",
		"pipe:1",
	)
	return startFFmpegStreaming(ctx, vc, cmd, ctrl)
}

// StartStreamingPCMReader plays raw PCM (e.g. a TTS stream) into the voice
// connection, transcoding from the given sample rate / channel count.
func StartStreamingPCMReader(ctx context.Context, vc *discordgo.VoiceConnection, audio io.Reader, sampleRate int, channels int, ctrl Controls) error {
	cmd := exec.Command("ffmpeg",
		"-f", "s16le",
		"-ar", strconv.Itoa(sampleRate),
		"-ac", strconv.Itoa(channels),
		"-i", "pipe:0",
		"-f", "s16le",
		"-ar", "48000",
		"-ac", "2",
		"pipe:1",
	)
	cmd.Stdin = audio

	return startFFmpegStreaming(ctx, vc, cmd, ctrl)
}

func startFFmpegStreaming(ctx context.Context, vc *discordgo.VoiceConnection, cmd *exec.Cmd, ctrl Controls) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	enc, err := gopus.NewEncoder(48000, 2, gopus.Audio)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = waitFFmpeg(cmd, &stderr)
		return err
	}

	if err := vc.Speaking(true); err != nil {
		_ = cmd.Process.Kill()
		_ = waitFFmpeg(cmd, &stderr)
		return err
	}
	defer vc.Speaking(false)

	// Buffer for 20ms PCM frames (960 samples * 2 channels).
	pcmBuf := make([]int16, 960*2)

	for {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = waitFFmpeg(cmd, &stderr)
			return ctx.Err()
		default:
			// Pause: stop pulling from ffmpeg so it blocks and playback holds its
			// position; poll until resumed or cancelled.
			if ctrl.Paused != nil && ctrl.Paused() {
				select {
				case <-ctx.Done():
					_ = cmd.Process.Kill()
					_ = waitFFmpeg(cmd, &stderr)
					return ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
				continue
			}

			if err := binary.Read(stdout, binary.LittleEndian, pcmBuf); err != nil {
				if err != io.EOF && err != io.ErrUnexpectedEOF {
					log.Println("PCM read error:", err)
				}
				return waitFFmpeg(cmd, &stderr)
			}
			// Ducking: scale the PCM frame before encoding.
			if ctrl.Gain != nil {
				if g := ctrl.Gain(); g < 0.999 {
					for i := range pcmBuf {
						pcmBuf[i] = int16(float64(pcmBuf[i]) * g)
					}
				}
			}
			// Overlay: mix the assistant's voice on top of the ducked music.
			if ctrl.Overlay != nil {
				if ov := ctrl.Overlay(len(pcmBuf)); len(ov) > 0 {
					for i := 0; i < len(ov) && i < len(pcmBuf); i++ {
						pcmBuf[i] = mixSample(pcmBuf[i], ov[i])
					}
				}
			}
			opusFrame, err := enc.Encode(pcmBuf, len(pcmBuf)/2, len(pcmBuf)/2)
			if err != nil {
				continue
			}
			// Select on ctx as well so a skip/stop interrupts even when the
			// Discord send buffer is full.
			select {
			case vc.OpusSend <- opusFrame:
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				_ = waitFFmpeg(cmd, &stderr)
				return ctx.Err()
			}
		}
	}
}

func waitFFmpeg(cmd *exec.Cmd, stderr *bytes.Buffer) error {
	if err := cmd.Wait(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return fmt.Errorf("ffmpeg exited: %w; stderr: %s", err, message)
		}
		return fmt.Errorf("ffmpeg exited: %w", err)
	}
	return nil
}
