package music

import (
	"discordAudio/internal/logger"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os/exec"
	"strings"
)

func Search(query string) ([]Track, error) {

	query = strings.TrimSpace(query)
	if query == "" {
		return []Track{}, nil
	}

	variants := []searchVariant{
		{
			name: "ytsearch pseudo-URL",
			args: []string{"ytsearch10:" + query, "--flat-playlist", "-J", "--no-warnings"},
		},
		{
			name: "youtube results URL",
			args: []string{
				//"--flat-playlist",
				"--playlist-end", "10",
				"-J",
				"https://www.youtube.com/results?search_query=" + url.QueryEscape(query),
				"--no-warnings",
			},
		},
	}

	var lastErr error
	for _, variant := range variants {
		tracks, err := runSearchVariant(variant)
		if err == nil {
			return tracks, nil
		}
		lastErr = err
		log.Printf("yt-dlp SEARCH variant failed (%s): %v", variant.name, err)
	}

	if lastErr == nil {
		lastErr = logger.Send(fmt.Sprintf("yt-dlp search failed: %v", lastErr))
	}
	return nil, lastErr
}

func runSearchVariant(variant searchVariant) ([]Track, error) {
	cmd := exec.Command(ytDLPBinary(), variant.args...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w; output: %s", err, strings.TrimSpace(string(out)))
	}

	logger.Send(fmt.Sprintf("yt-dlp RAW (%s): %s", variant.name, string(out)))

	var result struct {
		Entries []Track `json:"entries"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("json parse error: %w", err)
	}

	tracks := make([]Track, 0, len(result.Entries))
	for _, entry := range result.Entries {
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Title) == "" {
			continue
		}
		entry.Title = SafeName(entry.Title)
		tracks = append(tracks, entry)
	}

	if len(tracks) == 0 {
		return nil, fmt.Errorf("empty result set")
	}

	return tracks, nil
}
