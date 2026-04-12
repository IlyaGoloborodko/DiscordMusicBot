package music

import (
	"encoding/json"
	"os/exec"
)

func Search(query string) ([]Track, error) {
	cmd := exec.Command(
		"yt-dlp",
		"ytsearch10:"+query,
		"--flat-playlist",
		"-J",
	)

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var result struct {
		Entries []Track `json:"entries"`
	}

	err = json.Unmarshal(out, &result)
	if err != nil {
		return nil, err
	}

	return result.Entries, nil
}
