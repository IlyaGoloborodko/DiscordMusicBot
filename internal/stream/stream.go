package stream

import (
	"bytes"
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

func StartStreaming(vc *discordgo.VoiceConnection, url string) error {
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
	return startFFmpegStreaming(vc, cmd)
}

func StartStreamingReader(vc *discordgo.VoiceConnection, audio io.Reader) error {
	cmd := exec.Command("ffmpeg",
		"-i", "pipe:0",
		"-f", "s16le",
		"-ar", "48000",
		"-ac", "2",
		"pipe:1",
	)
	cmd.Stdin = audio

	return startFFmpegStreaming(vc, cmd)
}

func StartStreamingPCMReader(vc *discordgo.VoiceConnection, audio io.Reader, sampleRate int, channels int) error {
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

	return startFFmpegStreaming(vc, cmd)
}

func startFFmpegStreaming(vc *discordgo.VoiceConnection, cmd *exec.Cmd) error {
	stopChan := StopChan()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

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

	// Buffer for 20ms PCM frames
	pcmBuf := make([]int16, 960*2)

	for {
		select {
		case <-stopChan:
			// получили сигнал остановки — завершаем цикл
			_ = cmd.Process.Kill()
			_ = waitFFmpeg(cmd, &stderr)
			return nil
		default:
			// читаем PCM и отправляем в Discord
			if err := binary.Read(stdout, binary.LittleEndian, pcmBuf); err != nil {
				if err != io.EOF {
					log.Println("PCM read error:", err)
				}
				if err := waitFFmpeg(cmd, &stderr); err != nil {
					return err
				}
				return nil
			}
			opusFrame, err := enc.Encode(pcmBuf, len(pcmBuf)/2, len(pcmBuf)/2)
			if err != nil {
				continue
			}
			vc.OpusSend <- opusFrame
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
