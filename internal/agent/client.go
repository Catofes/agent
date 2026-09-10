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
	Role             string            `json:"role"`
	Content          string            `json:"content"`
	Reasoning        string            `json:"reasoning_content,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`
	RawResponseItems []json.RawMessage `json:"-"`
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
	EnableWebSearch  bool
	BeforeCall       func(context.Context) error
	OnDelta          func(string) error
	OnReasoningDelta func(string) error
	OnHostedTool     func(HostedToolEvent) error
}

type HostedToolEvent struct {
	ID      string
	Phase   string
	Name    string
	Summary string
	Detail  json.RawMessage
}

type Completion struct {
	Content          string
	Reasoning        string
	Deltas           []string
	ReasoningDeltas  []string
	ToolCalls        []ToolCall
	TokensIn         int64
	TokensOut        int64
	RawResponseItems []json.RawMessage
}

type Client interface {
	Complete(context.Context, CompletionRequest) (Completion, error)
}

type DeepSeekClient struct {
	BaseURL      string
	APIKey       string
	UseResponses bool
	HTTP         *http.Client
}

// QwenClient speaks Alibaba Cloud Model Studio's OpenAI-compatible Chat
// Completions protocol. Its request parameters intentionally stay separate
// from DeepSeekClient: the two providers expose similar streams but use
// different controls for reasoning.
type QwenClient struct {
	BaseURL         string
	APIKey          string
	ReasoningEffort string
	HTTP            *http.Client
}

func (c *QwenClient) Complete(ctx context.Context, in CompletionRequest) (Completion, error) {
	effort := strings.ToLower(strings.TrimSpace(c.ReasoningEffort))
	if effort == "" {
		effort = "low"
	}
	body := map[string]any{
		"model":          in.Model,
		"messages":       in.Messages,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"user":           in.UserID,
	}
	if effort == "none" {
		body["enable_thinking"] = false
	} else {
		body["enable_thinking"] = true
		body["reasoning_effort"] = effort
	}
	if len(in.Tools) > 0 {
		body["tools"] = in.Tools
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Completion{}, err
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
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

func (c *DeepSeekClient) Complete(ctx context.Context, in CompletionRequest) (Completion, error) {
	if c.UseResponses && in.EnableWebSearch {
		return c.completeResponses(ctx, in)
	}
	return c.completeChat(ctx, in)
}

func (c *DeepSeekClient) completeChat(ctx context.Context, in CompletionRequest) (Completion, error) {
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

func (c *DeepSeekClient) completeResponses(ctx context.Context, in CompletionRequest) (Completion, error) {
	body := map[string]any{
		"model":     in.Model,
		"input":     responseInput(in.Messages),
		"stream":    true,
		"user":      in.UserID,
		"reasoning": map[string]string{"effort": "high"},
	}
	responseTools := make([]map[string]any, 0, len(in.Tools)+1)
	for _, definition := range in.Tools {
		responseTools = append(responseTools, map[string]any{
			"type":        "function",
			"name":        definition.Function.Name,
			"description": definition.Function.Description,
			"parameters":  definition.Function.Parameters,
		})
	}
	if in.EnableWebSearch {
		responseTools = append(responseTools, map[string]any{"type": "web_search"})
	}
	if len(responseTools) > 0 {
		body["tools"] = responseTools
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Completion{}, err
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/responses"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
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
	return decodeResponseStream(resp.Body, in.OnDelta, in.OnReasoningDelta, in.OnHostedTool)
}

func responseInput(messages []Message) []any {
	out := make([]any, 0, len(messages)*2)
	for _, message := range messages {
		if len(message.RawResponseItems) > 0 {
			for _, raw := range message.RawResponseItems {
				var item any
				if json.Unmarshal(raw, &item) == nil {
					out = append(out, item)
				}
			}
			continue
		}
		switch message.Role {
		case "tool":
			out = append(out, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Content})
		case "assistant":
			if message.Reasoning != "" {
				out = append(out, map[string]any{"type": "reasoning", "content": []map[string]string{{"type": "reasoning_text", "text": message.Reasoning}}})
			}
			if message.Content != "" {
				out = append(out, map[string]any{"type": "message", "role": "assistant", "content": message.Content})
			}
			for _, call := range message.ToolCalls {
				out = append(out, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			}
		default:
			out = append(out, map[string]any{"type": "message", "role": message.Role, "content": message.Content})
		}
	}
	return out
}

func decodeResponseStream(r io.Reader, onDelta, onReasoningDelta func(string) error, onHostedTool func(HostedToolEvent) error) (Completion, error) {
	type responseEnvelope struct {
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	type event struct {
		Type     string           `json:"type"`
		Delta    string           `json:"delta"`
		ItemID   string           `json:"item_id"`
		Item     json.RawMessage  `json:"item"`
		Response responseEnvelope `json:"response"`
	}
	type item struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Action    json.RawMessage `json:"action"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	var terminal *responseEnvelope
	started := map[string]bool{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return Completion{}, fmt.Errorf("decode provider response event: %w", err)
		}
		switch ev.Type {
		case "response.output_text.delta":
			if onDelta != nil && ev.Delta != "" {
				if err := onDelta(ev.Delta); err != nil {
					return Completion{}, err
				}
			}
		case "response.reasoning_text.delta":
			if onReasoningDelta != nil && ev.Delta != "" {
				if err := onReasoningDelta(ev.Delta); err != nil {
					return Completion{}, err
				}
			}
		case "response.web_search_call.in_progress", "response.web_search_call.searching":
			if onHostedTool != nil && !started[ev.ItemID] {
				started[ev.ItemID] = true
				if err := onHostedTool(HostedToolEvent{ID: ev.ItemID, Phase: "start", Name: "web_search", Summary: "正在由 DeepSeek 搜索网上信息"}); err != nil {
					return Completion{}, err
				}
			}
		case "response.web_search_call.completed":
			if onHostedTool != nil {
				if err := onHostedTool(HostedToolEvent{ID: ev.ItemID, Phase: "result", Name: "web_search", Summary: "DeepSeek 已完成联网搜索"}); err != nil {
					return Completion{}, err
				}
			}
		case "response.completed", "response.incomplete", "response.failed":
			copy := ev.Response
			terminal = &copy
		}
	}
	if err := scanner.Err(); err != nil {
		return Completion{}, err
	}
	if terminal == nil {
		return Completion{}, errors.New("provider response stream ended without a terminal event")
	}
	if terminal.Status != "completed" {
		message := terminal.Status
		if terminal.Error != nil && terminal.Error.Message != "" {
			message = terminal.Error.Message
		}
		return Completion{}, fmt.Errorf("provider response %s: %s", terminal.Status, message)
	}
	out := Completion{TokensIn: terminal.Usage.Input, TokensOut: terminal.Usage.Output, RawResponseItems: terminal.Output}
	for _, raw := range terminal.Output {
		var value item
		if err := json.Unmarshal(raw, &value); err != nil {
			return Completion{}, fmt.Errorf("decode provider response item: %w", err)
		}
		switch value.Type {
		case "message":
			for _, content := range value.Content {
				if content.Type == "output_text" {
					out.Content += content.Text
				}
			}
		case "reasoning":
			for _, content := range value.Content {
				if content.Type == "reasoning_text" {
					out.Reasoning += content.Text
				}
			}
		case "function_call":
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: value.CallID, Type: "function", Function: ToolFunction{Name: value.Name, Arguments: value.Arguments}})
		}
	}
	if out.Content == "" && len(out.ToolCalls) == 0 {
		return Completion{}, errors.New("provider returned an empty completion")
	}
	return out, nil
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
