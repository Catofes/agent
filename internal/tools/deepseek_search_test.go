package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestDeepSeekAnthropicSearchRequestAndFormatting(t *testing.T) {
	var got map[string]any
	search := &DeepSeekSearch{BaseURL: "https://api.deepseek.com", APIKey: "secret", Model: "deepseek-flash", HTTP: &http.Client{Transport: searchRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/anthropic/v1/messages" || r.Header.Get("x-api-key") != "secret" || r.Header.Get("anthropic-version") == "" {
			t.Fatalf("unexpected request url=%s headers=%v", r.URL, r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		body := `{"content":[{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"上海天气"}},{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[{"type":"web_search_result","url":"https://weather.example/shanghai","title":"上海天气","page_age":"今天"}]},{"type":"text","text":"上海今天晴。"}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	result, err := search.ExecuteChannel(context.Background(), json.RawMessage(`{"query":"上海天气","freshness":"day"}`), DeepSeekSearchAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	tools, ok := got["tools"].([]any)
	if !ok || len(tools) != 1 || tools[0].(map[string]any)["type"] != "web_search_20250305" {
		t.Fatalf("unexpected tools: %#v", got["tools"])
	}
	for _, want := range []string{"Anthropic Messages", "上海今天晴", "https://weather.example/shanghai"} {
		if !strings.Contains(result.ModelText, want) {
			t.Fatalf("result missing %q: %s", want, result.ModelText)
		}
	}
}

func TestDeepSeekResponsesRequiresActualSearchCall(t *testing.T) {
	search := &DeepSeekSearch{BaseURL: "https://api.deepseek.com", APIKey: "secret", Model: "deepseek-flash", HTTP: &http.Client{Transport: searchRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"凭记忆回答"}]}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	_, err := search.ExecuteChannel(context.Background(), json.RawMessage(`{"query":"最新消息"}`), DeepSeekSearchResponses)
	if err == nil || !strings.Contains(err.Error(), "未执行 web_search") {
		t.Fatalf("got %v", err)
	}
}

type routedSearchStub struct{ result Result }

func (s routedSearchStub) Definition() Definition { return webSearchDefinition() }
func (s routedSearchStub) Execute(context.Context, json.RawMessage) (Result, error) {
	return s.result, nil
}

func TestRoutedWebSearchUsesSavedProvider(t *testing.T) {
	router := NewRoutedWebSearch(routedSearchStub{result: Result{ModelText: "zhipu"}}, nil)
	ctx := WithSearchRoute(context.Background(), SearchRoute{Provider: "zhipu", DeepSeekChannel: "anthropic"})
	result, err := router.Execute(ctx, json.RawMessage(`{"query":"测试"}`))
	if err != nil || result.ModelText != "zhipu" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestLiveConfiguredSearchProviders(t *testing.T) {
	if os.Getenv("LIVE_SEARCH_TEST") != "1" {
		t.Skip("set LIVE_SEARCH_TEST=1 to exercise configured external search APIs")
	}
	ctx := context.Background()
	if key := strings.TrimSpace(os.Getenv("ZHIPU_SEARCH_API_KEY")); key != "" {
		result, err := NewZhipuSearch(key, os.Getenv("ZHIPU_SEARCH_ENGINE")).Execute(ctx, json.RawMessage(`{"query":"今天上海天气","freshness":"day"}`))
		if err != nil || !strings.Contains(result.ModelText, "URL:") {
			t.Fatalf("live Zhipu search result=%q err=%v", result.Summary, err)
		}
	}
	key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY is not configured")
	}
	model := strings.TrimSpace(os.Getenv("DEEPSEEK_MODEL"))
	if model == "" {
		model = "deepseek-flash"
	}
	baseURL := strings.TrimSpace(os.Getenv("DEEPSEEK_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	search := &DeepSeekSearch{BaseURL: baseURL, APIKey: key, Model: model, HTTP: &http.Client{}}
	result, err := search.ExecuteChannel(ctx, json.RawMessage(`{"query":"今天上海天气","freshness":"day"}`), DeepSeekSearchAnthropic)
	if err != nil || !strings.Contains(result.ModelText, "Anthropic Messages") {
		t.Fatalf("live DeepSeek search result=%q err=%v", result.Summary, err)
	}
}
