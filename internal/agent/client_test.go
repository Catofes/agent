package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"classroom-agent/internal/tools"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type maxChunkReader struct {
	r   io.Reader
	max int
}

func (r maxChunkReader) Read(p []byte) (int, error) {
	if len(p) > r.max {
		p = p[:r.max]
	}
	return r.r.Read(p)
}

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

func TestQwenClientUsesCompatibleStreamingReasoningAndTools(t *testing.T) {
	var requestBody map[string]any
	client := &QwenClient{BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", APIKey: "qwen-secret", ReasoningEffort: "low", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/compatible-mode/v1/chat/completions" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &requestBody); err != nil {
			t.Fatal(err)
		}
		stream := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"先判断\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"答案\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}}
	definition := tools.Definition{Type: "function", Function: tools.FunctionSpec{Name: "calculator", Description: "test tool", Parameters: map[string]any{"type": "object"}}}
	got, err := client.Complete(context.Background(), CompletionRequest{Model: "qwen3.8-flash", UserID: "anonymous", Messages: []Message{{Role: "user", Content: "问题"}}, Tools: []tools.Definition{definition}})
	if err != nil {
		t.Fatal(err)
	}
	if requestBody["enable_thinking"] != true || requestBody["reasoning_effort"] != "low" || requestBody["user"] != "anonymous" {
		t.Fatalf("Qwen controls missing: %#v", requestBody)
	}
	if requestBody["thinking"] != nil || requestBody["user_id"] != nil {
		t.Fatalf("DeepSeek-only controls leaked: %#v", requestBody)
	}
	if toolsList, ok := requestBody["tools"].([]any); !ok || len(toolsList) != 1 {
		t.Fatalf("tools=%#v", requestBody["tools"])
	}
	if got.Reasoning != "先判断" || got.Content != "答案" || got.TokensIn != 7 || got.TokensOut != 3 {
		t.Fatalf("completion=%#v", got)
	}
}

func TestQwenResponsesUsesHostedSearchAndExposesSources(t *testing.T) {
	var requestBody map[string]any
	client := &QwenClient{BaseURL: "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", APIKey: "qwen-secret", ReasoningEffort: "medium", UseResponses: true, HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/compatible-mode/v1/responses" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &requestBody); err != nil {
			t.Fatal(err)
		}
		stream := strings.Join([]string{
			`data: {"type":"response.web_search_call.in_progress","item_id":"ws_1"}`,
			`data: {"type":"response.web_search_call.searching","item_id":"ws_1"}`,
			`data: {"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"杭州天气","sources":[{"type":"url","url":"https://weather.example/hangzhou"}]}}}`,
			`data: {"type":"response.reasoning_text.delta","delta":"先查"}`,
			`data: {"type":"response.output_text.delta","delta":"杭州晴"}`,
			`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"先查"}]},{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"杭州天气","sources":[{"type":"url","url":"https://weather.example/hangzhou"}]}},{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"杭州晴"}]}],"usage":{"input_tokens":120,"output_tokens":8}}}`,
			"",
		}, "\n")
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}}
	definition := tools.Definition{Type: "function", Function: tools.FunctionSpec{Name: "calculator", Description: "test tool", Parameters: map[string]any{"type": "object"}}}
	var hosted []HostedToolEvent
	got, err := client.Complete(context.Background(), CompletionRequest{Model: "qwen3.8-flash", UserID: "anonymous", Messages: []Message{{Role: "user", Content: "杭州天气"}}, Tools: []tools.Definition{definition}, EnableWebSearch: true, OnHostedTool: func(event HostedToolEvent) error {
		hosted = append(hosted, event)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "杭州晴" || got.Reasoning != "先查" || got.TokensIn != 120 || got.TokensOut != 8 {
		t.Fatalf("completion=%#v", got)
	}
	if len(hosted) != 2 || hosted[0].Name != "web_search" || hosted[1].Phase != "result" || !strings.Contains(string(hosted[1].Detail), "weather.example/hangzhou") {
		t.Fatalf("hosted=%#v", hosted)
	}
	if reasoning, _ := requestBody["reasoning"].(map[string]any); reasoning["effort"] != "medium" {
		t.Fatalf("reasoning=%#v", requestBody["reasoning"])
	}
	toolList, _ := requestBody["tools"].([]any)
	if len(toolList) != 2 {
		t.Fatalf("tools=%#v", requestBody["tools"])
	}
	function, _ := toolList[0].(map[string]any)
	builtin, _ := toolList[1].(map[string]any)
	if function["type"] != "function" || function["name"] != "calculator" || builtin["type"] != "web_search" {
		t.Fatalf("tools=%#v", toolList)
	}
}

func TestBailianDeepSeekUsesChatWithoutHostedSearch(t *testing.T) {
	var requestBody map[string]any
	client := &BailianDeepSeekClient{BaseURL: "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", APIKey: "bailian-secret", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/compatible-mode/v1/chat/completions" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &requestBody); err != nil {
			t.Fatal(err)
		}
		stream := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"先想\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"答案\"}}]}\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}}
	got, err := client.Complete(context.Background(), CompletionRequest{Model: "deepseek-v4-flash-0731", UserID: "anonymous", Messages: []Message{{Role: "user", Content: "问题"}}})
	if err != nil {
		t.Fatal(err)
	}
	if requestBody["enable_thinking"] != true || requestBody["user"] != "anonymous" || requestBody["thinking"] != nil {
		t.Fatalf("Bailian controls=%#v", requestBody)
	}
	if got.Reasoning != "先想" || got.Content != "答案" {
		t.Fatalf("completion=%#v", got)
	}
}

func TestBailianDeepSeekResponsesUsesBailianHostedSearch(t *testing.T) {
	var requestBody map[string]any
	client := &BailianDeepSeekClient{BaseURL: "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", APIKey: "bailian-secret", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/compatible-mode/v1/responses" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &requestBody); err != nil {
			t.Fatal(err)
		}
		stream := strings.Join([]string{
			`data: {"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"杭州天气","sources":[{"type":"url","url":"https://weather.example/hangzhou"}]}}}`,
			`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"杭州天气","sources":[{"type":"url","url":"https://weather.example/hangzhou"}]}},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"杭州晴"}]}],"usage":{"input_tokens":12,"output_tokens":2}}}`,
			"",
		}, "\n")
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}}
	definition := tools.Definition{Type: "function", Function: tools.FunctionSpec{Name: "calculator", Description: "test tool", Parameters: map[string]any{"type": "object"}}}
	var hosted []HostedToolEvent
	got, err := client.Complete(context.Background(), CompletionRequest{Model: "deepseek-v4-flash-0731", UserID: "anonymous", Messages: []Message{{Role: "user", Content: "杭州天气"}}, Tools: []tools.Definition{definition}, EnableWebSearch: true, OnHostedTool: func(event HostedToolEvent) error {
		hosted = append(hosted, event)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "杭州晴" || requestBody["enable_thinking"] != nil || requestBody["reasoning"] != nil {
		t.Fatalf("completion/body=%#v %#v", got, requestBody)
	}
	toolList, _ := requestBody["tools"].([]any)
	if len(toolList) != 3 || toolList[1].(map[string]any)["type"] != "web_search" || toolList[2].(map[string]any)["type"] != "web_extractor" {
		t.Fatalf("tools=%#v", requestBody["tools"])
	}
	if len(hosted) != 2 || !strings.Contains(hosted[1].Summary, "百炼 DeepSeek") {
		t.Fatalf("hosted=%#v", hosted)
	}
}

func TestDecodeStreamAcrossJSONAndUTF8ChunkBoundaries(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"先想"}}]}`,
		`data: {"choices":[{"delta":{"content":"中"}}]}`,
		`data: {"choices":[{"delta":{"content":"文"}}]}`,
		`data: [DONE]`, "",
	}, "\n")
	for _, max := range []int{1, 2, 7, len(stream)} {
		got, err := decodeStream(maxChunkReader{r: strings.NewReader(stream), max: max})
		if err != nil {
			t.Fatalf("chunk size %d: %v", max, err)
		}
		if got.Reasoning != "先想" || got.Content != "中文" || len(got.Deltas) != 2 {
			t.Fatalf("chunk size %d: %#v", max, got)
		}
	}
}

func TestDecodeResponseStreamWithHostedSearch(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.web_search_call.in_progress","item_id":"ws_1"}`,
		`data: {"type":"response.web_search_call.searching","item_id":"ws_1"}`,
		`data: {"type":"response.web_search_call.completed","item_id":"ws_1"}`,
		`data: {"type":"response.reasoning_text.delta","delta":"先查"}`,
		`data: {"type":"response.output_text.delta","delta":"杭州晴"}`,
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"先查"}]},{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"杭州天气"}},{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"杭州晴"}]}],"usage":{"input_tokens":120,"output_tokens":8}}}`,
		"",
	}, "\n")
	var text, reasoning string
	var hosted []HostedToolEvent
	got, err := decodeResponseStream(strings.NewReader(stream), func(delta string) error { text += delta; return nil }, func(delta string) error { reasoning += delta; return nil }, func(event HostedToolEvent) error { hosted = append(hosted, event); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "杭州晴" || got.Reasoning != "先查" || text != got.Content || reasoning != got.Reasoning {
		t.Fatalf("completion=%#v deltas=%q/%q", got, reasoning, text)
	}
	if len(hosted) != 2 || hosted[0].Phase != "start" || hosted[1].Phase != "result" {
		t.Fatalf("hosted events=%#v", hosted)
	}
	if got.TokensIn != 120 || got.TokensOut != 8 || len(got.RawResponseItems) != 3 {
		t.Fatalf("usage/raw output=%#v", got)
	}
}

func TestDeepSeekResponsesRequestUsesBuiltinAndFlatFunctionTools(t *testing.T) {
	var requestBody map[string]any
	client := &DeepSeekClient{BaseURL: "https://api.deepseek.com", APIKey: "secret", UseResponses: true, HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &requestBody); err != nil {
			t.Fatal(err)
		}
		stream := `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"答案"}]}],"usage":{"input_tokens":3,"output_tokens":1}}}` + "\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}}
	definition := tools.Definition{Type: "function", Function: tools.FunctionSpec{Name: "calculator", Description: "test tool", Parameters: map[string]any{"type": "object"}}}
	got, err := client.Complete(context.Background(), CompletionRequest{Model: "deepseek-v4-flash", UserID: "anonymous", Messages: []Message{{Role: "system", Content: "规则"}, {Role: "user", Content: "问题"}}, Tools: []tools.Definition{definition}, EnableWebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "答案" || requestBody["user"] != "anonymous" {
		t.Fatalf("completion/body=%#v %#v", got, requestBody)
	}
	toolList, _ := requestBody["tools"].([]any)
	if len(toolList) != 2 {
		t.Fatalf("tools=%#v", requestBody["tools"])
	}
	function, _ := toolList[0].(map[string]any)
	if function["type"] != "function" || function["name"] != "calculator" || function["function"] != nil {
		t.Fatalf("function tool was not flattened: %#v", function)
	}
	builtin, _ := toolList[1].(map[string]any)
	if builtin["type"] != "web_search" {
		t.Fatalf("builtin=%#v", builtin)
	}
}

func TestDeepSeekHostedModeKeepsChatProtocolWhenSearchIsNotEnabled(t *testing.T) {
	var path string
	client := &DeepSeekClient{BaseURL: "https://api.deepseek.com", APIKey: "secret", UseResponses: true, HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		stream := "data: {\"choices\":[{\"delta\":{\"content\":\"答案\"}}]}\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}}
	if _, err := client.Complete(context.Background(), CompletionRequest{Model: "deepseek-v4-flash", Messages: []Message{{Role: "user", Content: "问题"}}}); err != nil {
		t.Fatal(err)
	}
	if path != "/chat/completions" {
		t.Fatalf("path=%s", path)
	}
}

func TestResponseInputReplaysRawProviderItemsBeforeToolOutput(t *testing.T) {
	raw := json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"calculator","arguments":"{\"expression\":\"2+2\"}"}`)
	input := responseInput([]Message{{Role: "assistant", RawResponseItems: []json.RawMessage{raw}}, {Role: "tool", ToolCallID: "call_1", Content: "4"}})
	if len(input) != 2 {
		t.Fatalf("input=%#v", input)
	}
	first := input[0].(map[string]any)
	second := input[1].(map[string]any)
	if first["type"] != "function_call" || second["type"] != "function_call_output" || second["call_id"] != "call_1" {
		t.Fatalf("input=%#v", input)
	}
}

func TestResponseInputReconstructedReasoningIncludesRequiredSummary(t *testing.T) {
	input := responseInput([]Message{{
		Role:      "assistant",
		Reasoning: "需要读取记忆",
		ToolCalls: []ToolCall{{ID: "call_memory", Type: "function", Function: ToolFunction{Name: "recall_memory", Arguments: `{"query":"居住城市"}`}}},
	}})
	if len(input) != 2 {
		t.Fatalf("input=%#v", input)
	}
	reasoning := input[0].(map[string]any)
	summary, ok := reasoning["summary"].([]any)
	if reasoning["type"] != "reasoning" || !ok || len(summary) != 0 {
		t.Fatalf("reasoning=%#v", reasoning)
	}
	encoded, err := json.Marshal(reasoning)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"summary":[]`) {
		t.Fatalf("encoded reasoning=%s", encoded)
	}
	call := input[1].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "call_memory" {
		t.Fatalf("call=%#v", call)
	}
}
