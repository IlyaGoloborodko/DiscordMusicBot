package stream

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gopus"
)

// StartStreaming plays an audio URL into the voice connection until it ends or
// ctx is cancelled (cancellation is how the player implements skip/stop).
func StartStreaming(ctx context.Context, vc *discordgo.VoiceConnection, url string) error {
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
	return startFFmpegStreaming(ctx, vc, cmd)
}

// StartStreamingPCMReader plays raw PCM (e.g. a TTS stream) into the voice
// connection, transcoding from the given sample rate / channel count.
func StartStreamingPCMReader(ctx context.Context, vc *discordgo.VoiceConnection, audio io.Reader, sampleRate int, channels int) error {
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

	return startFFmpegStreaming(ctx, vc, cmd)
}

func startFFmpegStreaming(ctx context.Context, vc *discordgo.VoiceConnection, cmd *exec.Cmd) error {
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
			if err := binary.Read(stdout, binary.LittleEndian, pcmBuf); err != nil {
				if err != io.EOF && err != io.ErrUnexpectedEOF {
					log.Println("PCM read error:", err)
				}
				return waitFFmpeg(cmd, &stderr)
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
