package music

import (
	"log"
	"os/exec"
	"strings"
)

func GetStreamURL(id string) (string, error) {
	cmd := exec.Command(
		"yt-dlp",
		"--no-playlist",
		"-f", "bestaudio",
		"--print", "url",
		"https://www.youtube.com/watch?v="+id,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("yt-dlp SEARCH ERROR: %v\nOUTPUT:\n%s", err, string(out))
		return "", err
	}

	return strings.TrimSpace(string(out)), nil
}
