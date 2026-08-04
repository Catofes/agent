package agent

import (
	"strings"
	"testing"
)

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
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\ndata: [DONE]\n\n"
	got, err := decodeStream(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "你好" || len(got.Deltas) != 2 {
		t.Fatalf("got %#v", got)
	}
}
