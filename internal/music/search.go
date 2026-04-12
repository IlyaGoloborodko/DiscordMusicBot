package music

import (
	"encoding/json"
	"log"
	"os/exec"
)

func Search(query string) ([]Track, error) {

	cmd := exec.Command(
		"yt-dlp",
		"ytsearch10:"+query,
		"--flat-playlist",
		"-J",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("yt-dlp SEARCH ERROR: %v\nOUTPUT:\n%s", err, string(out))
		return nil, err
	}

	var result struct {
		Entries []Track `json:"entries"`
	}

	err = json.Unmarshal(out, &result)
	if err != nil {
		return nil, err
	}

	for i := range result.Entries {
		result.Entries[i].Title = SafeName(result.Entries[i].Title)
	}

	return result.Entries, nil
}
