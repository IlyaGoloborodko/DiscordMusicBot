package aiService

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func NewClient() *Client {
	return &Client{
		baseURL: os.Getenv("AI_SERVICE_ADDR"),
		apiKey:  os.Getenv("AI_SERVICE_API_KEY"),
		http:    &http.Client{},
	}
}

func (c *Client) Prompt(ctx context.Context, message string) (*AgentOutput, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("AI_SERVICE_ADDR is not set")
	}

	reqBody := PromptRequest{
		Message: message,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		c.baseURL+"/prompt",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("AI service returned %s: %s", resp.Status, string(body))
	}
	var result AgentOutput

	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

type TtsRequest struct {
	Text string `json:"text"`
}

func (c *Client) Tts(ctx context.Context, text string) (io.ReadCloser, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("AI_SERVICE_ADDR is not set")
	}

	reqBody := TtsRequest{
		Text: text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		c.baseURL+"/tts",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("AI service returned %s: %s", resp.Status, string(body))
	}

	return resp.Body, nil
}
