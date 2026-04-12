package music

import (
	"os/exec"
	"strings"
)

func GetStreamURL(id string) (string, error) {
	cmd := exec.Command(
		"yt-dlp",
		"--no-playlist",
		"-f", "bestaudio",
		"-g",
		"https://www.youtube.com/watch?v="+id,
	)

	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(out)), nil
}
