package music

import (
	"discordAudio/internal/logger"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func GetStreamURL(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("empty track id")
	}

	endpoint := searchServiceAddr() + "/stream?" + url.Values{
		"id":       {id},
		"provider": {providerYouTube},
	}.Encode()

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Send(fmt.Sprintf("search service STREAM ERROR: %v", err))
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		logger.Send(fmt.Sprintf("search service STREAM ERROR: %s: %s", resp.Status, strings.TrimSpace(string(body))))
		return "", fmt.Errorf("search service returned %s", resp.Status)
	}

	var result streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("json parse error: %w", err)
	}

	if strings.TrimSpace(result.StreamURL) == "" {
		logger.Send(fmt.Sprintf("search service STREAM ERROR: empty stream_url for id %q", id))
		return "", fmt.Errorf("search service did not return a playable URL")
	}

	return result.StreamURL, nil
}
