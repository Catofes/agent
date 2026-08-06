package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"classroom-agent/internal/tools"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Reasoning  string     `json:"reasoning_content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type CompletionRequest struct {
	Model            string
	UserID           string
	Messages         []Message
	Tools            []tools.Definition
	BeforeCall       func(context.Context) error
	OnDelta          func(string) error
	OnReasoningDelta func(string) error
}

type Completion struct {
	Content         string
	Reasoning       string
	Deltas          []string
	ReasoningDeltas []string
	ToolCalls       []ToolCall
	TokensIn        int64
	TokensOut       int64
}

type Client interface {
	Complete(context.Context, CompletionRequest) (Completion, error)
}

type DeepSeekClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func (c *DeepSeekClient) Complete(ctx context.Context, in CompletionRequest) (Completion, error) {
	body := map[string]any{"model": in.Model, "messages": in.Messages, "stream": true, "stream_options": map[string]any{"include_usage": true}, "user": in.UserID, "thinking": map[string]string{"type": "enabled"}, "reasoning_effort": "high"}
	body["user_id"] = in.UserID
	delete(body, "user")
	if len(in.Tools) > 0 {
		body["tools"] = in.Tools
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Completion{}, err
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return Completion{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Completion{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Completion{}, fmt.Errorf("provider status %d: %s", resp.StatusCode, sanitizeProviderError(limited))
	}
	return decodeStreamWithDelta(resp.Body, in.OnDelta, in.OnReasoningDelta)
}

func decodeStream(r io.Reader) (Completion, error) {
	return decodeStreamWithDelta(r, nil, nil)
}

func decodeStreamWithDelta(r io.Reader, onDelta, onReasoningDelta func(string) error) (Completion, error) {
	type streamTool struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type chunk struct {
		Choices []struct {
			Delta struct {
				Content   string       `json:"content"`
				Reasoning string       `json:"reasoning_content"`
				ToolCalls []streamTool `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	var out Completion
	calls := map[int]*ToolCall{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ch chunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return Completion{}, fmt.Errorf("decode provider stream: %w", err)
		}
		if ch.Usage != nil {
			out.TokensIn = ch.Usage.Prompt
			out.TokensOut = ch.Usage.Completion
		}
		for _, choice := range ch.Choices {
			if choice.Delta.Reasoning != "" {
				out.Reasoning += choice.Delta.Reasoning
				out.ReasoningDeltas = append(out.ReasoningDeltas, choice.Delta.Reasoning)
				if onReasoningDelta != nil {
					if err := onReasoningDelta(choice.Delta.Reasoning); err != nil {
						return Completion{}, err
					}
				}
			}
			if choice.Delta.Content != "" {
				out.Content += choice.Delta.Content
				out.Deltas = append(out.Deltas, choice.Delta.Content)
				if onDelta != nil {
					if err := onDelta(choice.Delta.Content); err != nil {
						return Completion{}, err
					}
				}
			}
			for _, part := range choice.Delta.ToolCalls {
				tc := calls[part.Index]
				if tc == nil {
					tc = &ToolCall{Type: "function"}
					calls[part.Index] = tc
				}
				tc.ID += part.ID
				if part.Type != "" {
					tc.Type = part.Type
				}
				tc.Function.Name += part.Function.Name
				tc.Function.Arguments += part.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Completion{}, err
	}
	for i := 0; i < len(calls); i++ {
		if tc := calls[i]; tc != nil {
			out.ToolCalls = append(out.ToolCalls, *tc)
		}
	}
	if out.Content == "" && len(out.ToolCalls) == 0 {
		return Completion{}, errors.New("provider returned an empty completion")
	}
	return out, nil
}

func sanitizeProviderError(v []byte) string {
	s := strings.TrimSpace(string(v))
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}
