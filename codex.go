package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const codexBaseURL = "https://chatgpt.com/backend-api/codex"

type CodexClient struct {
	tokens *TokenSource
}

type CodexModel struct {
	Slug           string `json:"slug"`
	SupportedInAPI bool   `json:"supported_in_api"`
	Visibility     string `json:"visibility"`
}

func (c *CodexClient) Models(ctx context.Context) ([]CodexModel, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}

	url := codexBaseURL + "/models?client_version=1.0.0"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	c.setAuthHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Codex models request failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Models []CodexModel `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Models, nil
}

func (c *CodexClient) StreamResponses(ctx context.Context, payload map[string]any) (*http.Response, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexBaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setAuthHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	return http.DefaultClient.Do(req)
}

func (c *CodexClient) setAuthHeaders(req *http.Request, token CodexToken) {
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	if token.AccountID != "" {
		req.Header.Set("ChatGPT-Account-ID", token.AccountID)
	}
}

type StreamEvent struct {
	Type string
	Data map[string]any
}

func ReadStreamEvents(r io.Reader, fn func(StreamEvent) error) error {
	reader := bufio.NewReader(r)
	var eventName string
	var dataLines []string

	dispatch := func(data string) error {
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			return nil
		}
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(data))
		if err := dec.Decode(&m); err != nil {
			return err
		}
		typeName := eventName
		if typeName == "" {
			typeName = stringValue(m, "type")
		}
		return fn(StreamEvent{Type: typeName, Data: m})
	}

	flush := func() error {
		if len(dataLines) == 0 {
			eventName = ""
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		defer func() { eventName = "" }()
		return dispatch(data)
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			if err == io.EOF {
				return flush()
			}
			return err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
		} else if value, ok := strings.CutPrefix(line, "event:"); ok {
			eventName = strings.TrimSpace(value)
		} else if data, ok := strings.CutPrefix(line, "data:"); ok {
			data = strings.TrimPrefix(data, " ")
			dataLines = append(dataLines, data)
		}

		if err == io.EOF {
			return flush()
		}
	}
}
