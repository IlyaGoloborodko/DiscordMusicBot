package music

import (
	"discordAudio/internal/logger"
	"fmt"
	"os/exec"
	"strings"
)

func GetStreamURL(id string) (string, error) {
	cmd := exec.Command(
		ytDLPBinary(),
		"--no-playlist",
		"-f", "bestaudio",
		"--print", "url",
		"--no-warnings",
		"--quiet",
		"https://www.youtube.com/watch?v="+id,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			logger.Send(fmt.Sprintf("yt-dlp STREAM ERROR: %v\nSTDERR:\n%s", err, string(exitErr.Stderr)))
		} else {
			logger.Send(fmt.Sprintf("yt-dlp STREAM ERROR: %v", err))
		}
		return "", err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		url := strings.TrimSpace(line)
		if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
			return url, nil
		}
	}

	logger.Send(fmt.Sprintf("yt-dlp STREAM ERROR: no valid URL in output: %q", string(out)))
	return "", fmt.Errorf("yt-dlp did not return a playable URL")
}
