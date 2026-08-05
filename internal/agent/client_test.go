package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDecodeStream(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calcu","arguments":"{\"expression\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"lator","arguments":"\"2+3\"}"}}]}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4}}`,
		`data: [DONE]`, ""}, "\n")
	got, err := decodeStream(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Function.Name != "calculator" || got.ToolCalls[0].Function.Arguments != `{"expression":"2+3"}` {
		t.Fatalf("bad calls: %#v", got.ToolCalls)
	}
	if got.TokensIn != 12 || got.TokensOut != 4 {
		t.Fatalf("bad usage: %#v", got)
	}
}

func TestDecodeTextStream(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"先判断\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\ndata: [DONE]\n\n"
	got, err := decodeStream(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "你好" || got.Reasoning != "先判断" || len(got.Deltas) != 2 || len(got.ReasoningDeltas) != 1 {
		t.Fatalf("got %#v", got)
	}
}

func TestDeepSeekClientEnablesThinkingAndStreamsSeparateDeltas(t *testing.T) {
	var requestBody map[string]any
	client := &DeepSeekClient{BaseURL: "https://api.deepseek.com", APIKey: "secret", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &requestBody); err != nil {
			t.Fatal(err)
		}
		stream := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"草稿\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"答案\"}}]}\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}}
	var reasoning, text string
	got, err := client.Complete(context.Background(), CompletionRequest{Model: "deepseek-v4-flash", UserID: "anonymous", Messages: []Message{{Role: "user", Content: "问题"}}, OnReasoningDelta: func(delta string) error { reasoning += delta; return nil }, OnDelta: func(delta string) error { text += delta; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	thinking, _ := requestBody["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || requestBody["reasoning_effort"] != "high" {
		t.Fatalf("thinking was not enabled: %#v", requestBody)
	}
	if _, exists := requestBody["tool_choice"]; exists {
		t.Fatalf("thinking request contains incompatible tool_choice: %#v", requestBody)
	}
	if requestBody["user_id"] != "anonymous" || requestBody["user"] != nil {
		t.Fatalf("anonymous user id missing: %#v", requestBody)
	}
	if reasoning != "草稿" || text != "答案" || got.Reasoning != reasoning || got.Content != text {
		t.Fatalf("reasoning=%q text=%q completion=%#v", reasoning, text, got)
	}
}
