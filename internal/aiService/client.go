package aiService

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

type PromptRequest struct {
	Message string `json:"user_message"`
}

type PromptResponse struct {
	Output string `json:"output"`
}

func NewClient() *Client {
	return &Client{
		baseURL: os.Getenv("AI_SERVICE_ADDR"),
		apiKey:  os.Getenv("AI_SERVICE_API_KEY"),
		http: &http.Client{
			Timeout: 0,
		},
	}
}

func (c *Client) Prompt(ctx context.Context, message string) (string, error) {

	reqBody := PromptRequest{
		Message: message,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		c.baseURL+"/prompt",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")

	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result PromptResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.Output, nil
}
