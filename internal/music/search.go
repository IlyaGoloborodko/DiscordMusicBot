package music

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func Search(query string) ([]Track, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []Track{}, nil
	}

	endpoint := searchServiceAddr() + "/search?" + url.Values{
		"q":        {query},
		"provider": {providerYouTube},
		"limit":    {searchLimit},
	}.Encode()

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("search service returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var result searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("json parse error: %w", err)
	}

	tracks := make([]Track, 0, len(result.Results))
	for _, entry := range result.Results {
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Title) == "" {
			continue
		}
		if strings.TrimSpace(entry.URL) == "" {
			entry.URL = "https://www.youtube.com/watch?v=" + entry.ID
		}
		entry.Title = SafeName(entry.Title)
		tracks = append(tracks, entry)
	}

	return tracks, nil
}
