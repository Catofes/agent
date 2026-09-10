package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type searchRoundTripFunc func(*http.Request) (*http.Response, error)

func (f searchRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestZhipuSearchRequestAndFormatting(t *testing.T) {
	var got map[string]any
	search := NewZhipuSearch("secret", "search_pro")
	search.client = &http.Client{Transport: searchRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request %s auth=%q", r.Method, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"search_result":[{"title":"杭州天气","content":"晴，最高 30 度","link":"https://weather.example/hangzhou","media":"示例天气","publish_date":"2026-08-29"}]}`))}, nil
	})}
	result, err := search.Execute(context.Background(), json.RawMessage(`{"query":"杭州天气","freshness":"day"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got["search_engine"] != "search_pro" || got["search_recency_filter"] != "oneDay" || got["count"] != float64(5) {
		t.Fatalf("unexpected request body: %#v", got)
	}
	for _, want := range []string{"智谱搜索", "杭州天气", "https://weather.example/hangzhou", "示例天气", "2026-08-29"} {
		if !strings.Contains(result.ModelText, want) {
			t.Fatalf("result missing %q: %s", want, result.ModelText)
		}
	}
}

func TestZhipuSearchRejectsLongQuery(t *testing.T) {
	search := NewZhipuSearch("secret", "search_std")
	_, err := search.Execute(context.Background(), json.RawMessage(`{"query":"`+strings.Repeat("长", zhipuSearchMaxQueryRunes+1)+`"}`))
	if err == nil || !strings.Contains(err.Error(), "超过 70 字") {
		t.Fatalf("got %v", err)
	}
}

func TestZhipuSearchRelaxesFreshnessAfterNoResults(t *testing.T) {
	var recencies []string
	search := NewZhipuSearch("secret", "search_std")
	search.client = &http.Client{Transport: searchRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		recency, _ := got["search_recency_filter"].(string)
		recencies = append(recencies, recency)
		body := `{"search_result":[]}`
		if recency == "noLimit" {
			body = `{"search_result":[{"title":"库里中国行","content":"较早的相关报道","link":"https://example.com/curry"}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	result, err := search.Execute(context.Background(), json.RawMessage(`{"query":"库里 中国行 最新消息","freshness":"week"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(recencies, ",") != "oneWeek,noLimit" {
		t.Fatalf("recencies=%v", recencies)
	}
	if !strings.Contains(result.ModelText, "已自动放宽为不限时间") || !strings.Contains(result.ModelText, "https://example.com/curry") {
		t.Fatalf("unexpected result: %s", result.ModelText)
	}
}

func TestZhipuQuarkRequestOmitsUnsupportedCount(t *testing.T) {
	var got map[string]any
	search := NewZhipuSearch("secret", "search_pro_quark")
	search.client = &http.Client{Transport: searchRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"search_result":[{"title":"结果","content":"摘要","link":"https://example.com"}]}`))}, nil
	})}
	if _, err := search.Execute(context.Background(), json.RawMessage(`{"query":"测试"}`)); err != nil {
		t.Fatal(err)
	}
	if _, exists := got["count"]; exists {
		t.Fatalf("Quark request contains unsupported count: %#v", got)
	}
}

func TestFormatZhipuResultsDropsUnsafeURLs(t *testing.T) {
	var response zhipuSearchResponse
	response.SearchResult = append(response.SearchResult,
		struct {
			Title       string `json:"title"`
			Content     string `json:"content"`
			Link        string `json:"link"`
			Media       string `json:"media"`
			PublishDate string `json:"publish_date"`
		}{Title: "内网", Content: "secret", Link: "http://127.0.0.1/admin"},
		struct {
			Title       string `json:"title"`
			Content     string `json:"content"`
			Link        string `json:"link"`
			Media       string `json:"media"`
			PublishDate string `json:"publish_date"`
		}{Title: "公开", Content: "public", Link: "https://example.com/page"},
	)
	result, err := formatZhipuResults("测试", response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.ModelText, "127.0.0.1") || !strings.Contains(result.ModelText, "example.com") {
		t.Fatalf("unsafe URL filtering failed: %s", result.ModelText)
	}
}
