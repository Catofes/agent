package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"classroom-agent/internal/store"
	"classroom-agent/internal/tools"
)

type fakeClient struct {
	mu       sync.Mutex
	answers  []Completion
	requests []CompletionRequest
}

type timeoutClient struct {
	mu    sync.Mutex
	calls int
}

type extractorFunc func(context.Context, string, string) ([]string, error)

type completionClientFunc func(context.Context, CompletionRequest) (Completion, error)

func (f completionClientFunc) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	return f(ctx, req)
}

func (f extractorFunc) Extract(ctx context.Context, user, assistant string) ([]string, error) {
	return f(ctx, user, assistant)
}

func (f *timeoutClient) Complete(ctx context.Context, _ CompletionRequest) (Completion, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	<-ctx.Done()
	return Completion{}, ctx.Err()
}

func (f *timeoutClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeClient) Complete(_ context.Context, r CompletionRequest) (Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	out := f.answers[0]
	f.answers = f.answers[1:]
	if r.OnReasoningDelta != nil {
		for _, delta := range out.ReasoningDeltas {
			if err := r.OnReasoningDelta(delta); err != nil {
				return Completion{}, err
			}
		}
	}
	if r.OnDelta != nil && len(out.ToolCalls) == 0 {
		for _, delta := range out.Deltas {
			if err := r.OnDelta(delta); err != nil {
				return Completion{}, err
			}
		}
	}
	return out, nil
}

func TestEngineToolLoop(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(dir, "students.csv")
	_ = os.WriteFile(csvPath, []byte("id,name\n2101,张三\n"), 0o600)
	if _, err = st.ImportStudentsCSV(ctx, run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateConversation(ctx, store.Conversation{ID: "conv", RunID: run.ID, StudentID: "2101"}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeClient{answers: []Completion{{Reasoning: "需要精确计算", ReasoningDeltas: []string{"需要", "精确计算"}, ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: ToolFunction{Name: "calculator", Arguments: `{"expression":"23*17"}`}}}, TokensIn: 10, TokensOut: 2}, {Reasoning: "工具结果可用", ReasoningDeltas: []string{"工具结果可用"}, Content: "结果是 391", Deltas: []string{"结果是 ", "391"}, TokensIn: 15, TokensOut: 4}}}
	engine := NewEngine(st, fake, tools.NewRegistry(tools.Calculator{}), "model", "secret", time.Second, 2)
	engine.TokenBudget = 1000
	engine.MaxToolCalls = 4
	engine.MaxOutputChars = 1000
	var events []Event
	err = engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn", Input: "计算", Design: store.Design{Persona: "数学老师", SkillMD: "准确", Tools: []string{"calculator"}, MaxTurns: 3}}, func(e Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("calls=%d", len(fake.requests))
	}
	if got := fake.requests[1].Messages[len(fake.requests[1].Messages)-2].Reasoning; got != "需要精确计算" {
		t.Fatalf("tool-call reasoning was not replayed: %q", got)
	}
	if got := fake.requests[0].UserID; len(got) != 32 || got == "2101" {
		t.Fatalf("anonymous id=%q", got)
	}
	if len(events) < 5 || events[len(events)-1].Type != "turn_end" {
		t.Fatalf("events=%#v", events)
	}
	var reasoning string
	for _, event := range events {
		if event.Type == "reasoning_delta" {
			reasoning += event.Delta
		}
	}
	if reasoning != "需要精确计算工具结果可用" {
		t.Fatalf("reasoning events=%q", reasoning)
	}
	msgs, err := st.Messages(ctx, run.ID, "2101", "conv", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("messages=%#v", msgs)
	}
	if msgs[1].Reasoning != "需要精确计算" {
		t.Fatalf("tool protocol reasoning was not persisted: %#v", msgs[1])
	}
	if msgs[3].Reasoning != "工具结果可用" {
		t.Fatalf("final reasoning was not persisted: %#v", msgs[3])
	}
	if msgs[3].FinishReason != "completed" {
		t.Fatalf("completion reason was not persisted: %#v", msgs[3])
	}
	u, err := st.Usage(ctx, run.ID, "2101")
	if err != nil || u.TokensIn != 25 || u.TokensOut != 6 {
		t.Fatalf("usage=%#v err=%v", u, err)
	}
}

func TestEngineHostedSearchUsesProviderAndPersistsVisibleTrace(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(dir, "students.csv")
	_ = os.WriteFile(csvPath, []byte("id,name\n2101,张三\n"), 0o600)
	if _, err = st.ImportStudentsCSV(ctx, run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateConversation(ctx, store.Conversation{ID: "conv", RunID: run.ID, StudentID: "2101"}); err != nil {
		t.Fatal(err)
	}
	var captured CompletionRequest
	client := completionClientFunc(func(_ context.Context, req CompletionRequest) (Completion, error) {
		captured = req
		if req.OnHostedTool != nil {
			if err := req.OnHostedTool(HostedToolEvent{ID: "ws_1", Phase: "start", Name: "web_search", Summary: "正在搜索"}); err != nil {
				return Completion{}, err
			}
			if err := req.OnHostedTool(HostedToolEvent{ID: "ws_1", Phase: "result", Name: "web_search", Summary: "搜索完成"}); err != nil {
				return Completion{}, err
			}
		}
		return Completion{Content: "杭州晴", Deltas: []string{"杭州晴"}, TokensIn: 10, TokensOut: 2}, nil
	})
	engine := NewEngine(st, client, tools.NewRegistry(tools.Calculator{}, tools.HostedWebSearch{}), "model", "secret", time.Second, 1)
	engine.HostedWebSearch = true
	engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
	var events []Event
	err = engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn", Input: "杭州天气", Design: store.Design{Tools: []string{"web_search"}, MaxTurns: 2}}, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !captured.EnableWebSearch || len(captured.Tools) != 0 {
		t.Fatalf("hosted search request=%#v", captured)
	}
	msgs, err := st.Messages(ctx, run.ID, "2101", "conv", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 || msgs[1].Role != "assistant" || msgs[2].Role != "tool" || msgs[3].Content != "杭州晴" {
		t.Fatalf("messages=%#v", msgs)
	}
	rebuilt := buildMessages(store.Design{}, nil, nil, msgs)
	for _, message := range rebuilt {
		if message.Role == "tool" || len(message.ToolCalls) > 0 {
			t.Fatalf("hosted trace leaked into model history: %#v", rebuilt)
		}
	}
	var starts, results int
	for _, event := range events {
		if event.Type == "tool_start" {
			starts++
		}
		if event.Type == "tool_result" {
			results++
		}
	}
	if starts != 1 || results != 1 {
		t.Fatalf("events=%#v", events)
	}
}

func TestEngineDirectAnswerUsesAnonymousIdentityAndNoUnselectedTools(t *testing.T) {
	ctx := context.Background()
	st, run := newEngineTestStore(t)
	fake := &fakeClient{answers: []Completion{{Content: "直接回答", Deltas: []string{"直接", "回答"}, TokensIn: 3, TokensOut: 2}}}
	engine := NewEngine(st, fake, tools.NewRegistry(tools.Calculator{}), "model", "secret", time.Second, 1)
	engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
	var events []Event
	err := engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn", Input: "普通闲聊", Design: store.Design{Persona: "友好助手", MaxTurns: 2}}, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("requests=%d", len(fake.requests))
	}
	request := fake.requests[0]
	if len(request.Tools) != 0 {
		t.Fatalf("unselected tools were exposed: %#v", request.Tools)
	}
	if request.UserID == "" || request.UserID == "2101" || strings.Contains(request.UserID, run.ID) {
		t.Fatalf("provider identity was not anonymized: %q", request.UserID)
	}
	if got := eventTypes(events); strings.Join(got, ",") != "turn_start,text_delta,text_delta,turn_end" {
		t.Fatalf("events=%v", got)
	}
	messages, err := st.Messages(ctx, run.ID, "2101", "conv", 0, 20)
	if err != nil || len(messages) != 2 || messages[1].Content != "直接回答" || messages[1].FinishReason != "completed" {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
}

func TestEngineSupportsConsecutiveToolIterations(t *testing.T) {
	ctx := context.Background()
	st, run := newEngineTestStore(t)
	call := func(id, expression string) ToolCall {
		return ToolCall{ID: id, Type: "function", Function: ToolFunction{Name: "calculator", Arguments: `{"expression":"` + expression + `"}`}}
	}
	fake := &fakeClient{answers: []Completion{
		{ToolCalls: []ToolCall{call("c1", "2+3")}, TokensIn: 2, TokensOut: 1},
		{ToolCalls: []ToolCall{call("c2", "5*4")}, TokensIn: 3, TokensOut: 1},
		{Content: "最终是 20", Deltas: []string{"最终是 ", "20"}, TokensIn: 4, TokensOut: 2},
	}}
	engine := NewEngine(st, fake, tools.NewRegistry(tools.Calculator{}), "model", "secret", time.Second, 1)
	engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
	var events []Event
	err := engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn", Input: "分两步算", Design: store.Design{Tools: []string{"calculator"}, MaxTurns: 3}}, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 3 {
		t.Fatalf("requests=%d", len(fake.requests))
	}
	var starts, results int
	for _, event := range events {
		if event.Type == "tool_start" {
			starts++
		}
		if event.Type == "tool_result" && event.Success != nil && *event.Success {
			results++
		}
	}
	if starts != 2 || results != 2 {
		t.Fatalf("events=%#v", events)
	}
	messages, err := st.Messages(ctx, run.ID, "2101", "conv", 0, 20)
	if err != nil || len(messages) != 6 || messages[5].FinishReason != "completed" {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
}

func TestEngineRejectsForgedUnselectedToolCall(t *testing.T) {
	ctx := context.Background()
	st, run := newEngineTestStore(t)
	forged := ToolCall{ID: "c1", Type: "function", Function: ToolFunction{Name: "calculator", Arguments: `{"expression":"2+2"}`}}
	fake := &fakeClient{answers: []Completion{
		{ToolCalls: []ToolCall{forged}, TokensIn: 2, TokensOut: 1},
		{Content: "工具不可用", Deltas: []string{"工具不可用"}, TokensIn: 2, TokensOut: 1},
	}}
	engine := NewEngine(st, fake, tools.NewRegistry(tools.Calculator{}), "model", "secret", time.Second, 1)
	engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
	var toolResult Event
	err := engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn", Input: "不要工具", Design: store.Design{Tools: []string{}, MaxTurns: 2}}, func(event Event) error {
		if event.Type == "tool_result" {
			toolResult = event
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 2 || len(fake.requests[0].Tools) != 0 {
		t.Fatalf("requests=%#v", fake.requests)
	}
	if toolResult.Success == nil || *toolResult.Success || !strings.Contains(toolResult.Summary, "未启用") {
		t.Fatalf("forged tool result=%#v", toolResult)
	}
	messages, err := st.Messages(ctx, run.ID, "2101", "conv", 0, 20)
	if err != nil || len(messages) != 4 || !strings.Contains(messages[2].Content, "未启用") || strings.Contains(messages[2].Content, "结果：4") {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
}

func TestEngineRetriesTimeoutOnce(t *testing.T) {
	ctx := context.Background()
	st, run := newEngineTestStore(t)
	fake := &timeoutClient{}
	engine := NewEngine(st, fake, tools.NewRegistry(tools.Calculator{}), "model", "secret", 10*time.Millisecond, 1)
	engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
	err := engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn", Input: "超时任务", Design: store.Design{MaxTurns: 2}}, func(Event) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if calls := fake.callCount(); calls != 2 {
		t.Fatalf("calls=%d want=2", calls)
	}
	messages, dbErr := st.Messages(ctx, run.ID, "2101", "conv", 0, 20)
	if dbErr != nil || len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages=%#v err=%v", messages, dbErr)
	}
}

func TestEngineClientInterruptStopsStreamingWithoutRetry(t *testing.T) {
	ctx := context.Background()
	st, run := newEngineTestStore(t)
	fake := &fakeClient{answers: []Completion{{Content: "不会完成", Deltas: []string{"第一段", "第二段"}}}}
	engine := NewEngine(st, fake, tools.NewRegistry(tools.Calculator{}), "model", "secret", time.Second, 1)
	engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
	disconnected := errors.New("client disconnected")
	err := engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn", Input: "生成任务", Design: store.Design{MaxTurns: 2}}, func(event Event) error {
		if event.Type == "text_delta" {
			return disconnected
		}
		return nil
	})
	if !errors.Is(err, disconnected) {
		t.Fatalf("err=%v", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("streamed request was retried: %d", len(fake.requests))
	}
	messages, dbErr := st.Messages(ctx, run.ID, "2101", "conv", 0, 20)
	if dbErr != nil || len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages=%#v err=%v", messages, dbErr)
	}
}

func eventTypes(events []Event) []string {
	out := make([]string, len(events))
	for i, event := range events {
		out[i] = event.Type
	}
	return out
}

func TestEnginePersistsLimitReasons(t *testing.T) {
	toolCall := ToolCall{ID: "c1", Type: "function", Function: ToolFunction{Name: "calculator", Arguments: `{"expression":"2+3"}`}}
	tests := []struct {
		name         string
		answers      []Completion
		maxTurns     int
		maxTools     int
		budget       int64
		wantErr      error
		wantReason   string
		wantMessages int
		wantUnknown  int64
	}{
		{name: "tool limit", answers: []Completion{{ToolCalls: []ToolCall{toolCall, {ID: "c2", Type: "function", Function: ToolFunction{Name: "calculator", Arguments: `{"expression":"4+5"}`}}}}}, maxTurns: 3, maxTools: 1, budget: 1000, wantErr: ErrToolLimit, wantReason: "tool_limit_reached", wantMessages: 2, wantUnknown: 1},
		{name: "turn limit", answers: []Completion{{ToolCalls: []ToolCall{toolCall}}}, maxTurns: 1, maxTools: 3, budget: 1000, wantErr: ErrTurnLimit, wantReason: "turn_limit_reached", wantMessages: 4, wantUnknown: 1},
		{name: "budget during tools", answers: []Completion{{ToolCalls: []ToolCall{toolCall}, TokensIn: 4, TokensOut: 3}}, maxTurns: 3, maxTools: 3, budget: 5, wantErr: ErrBudget, wantReason: "budget_exceeded", wantMessages: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			st, run := newEngineTestStore(t)
			fake := &fakeClient{answers: tt.answers}
			engine := NewEngine(st, fake, tools.NewRegistry(tools.Calculator{}), "model", "secret", time.Second, 1)
			engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = tt.budget, tt.maxTools, 1000
			err := engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn", Input: "任务", Design: store.Design{Tools: []string{"calculator"}, MaxTurns: tt.maxTurns}}, func(Event) error { return nil })
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err=%v want=%v", err, tt.wantErr)
			}
			messages, err := st.Messages(ctx, run.ID, "2101", "conv", 0, 20)
			if err != nil || len(messages) != tt.wantMessages {
				t.Fatalf("messages=%#v err=%v", messages, err)
			}
			last := messages[len(messages)-1]
			if last.FinishReason != tt.wantReason || strings.TrimSpace(last.Content) == "" {
				t.Fatalf("termination=%#v", last)
			}
			usage, err := st.Usage(ctx, run.ID, "2101")
			if err != nil || usage.UnknownCalls != tt.wantUnknown {
				t.Fatalf("usage=%#v err=%v", usage, err)
			}
		})
	}
}

func newEngineTestStore(t *testing.T) (*store.Store, store.Run) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(t.TempDir(), "students.csv")
	if err := os.WriteFile(csvPath, []byte("id,name\n2101,张三\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportStudentsCSV(ctx, run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateConversation(ctx, store.Conversation{ID: "conv", RunID: run.ID, StudentID: "2101"}); err != nil {
		t.Fatal(err)
	}
	return st, run
}

func TestTechnicalToolDetailIsValidAndBounded(t *testing.T) {
	call := ToolCall{ID: strings.Repeat("i", 3000), Type: "function", Function: ToolFunction{Name: strings.Repeat("工具", 1000), Arguments: strings.Repeat("中", 5000)}}
	detail := technicalCallDetail(call)
	if !json.Valid(detail) {
		t.Fatalf("detail is invalid JSON: %q", detail)
	}
	if len(detail) > 2048 {
		t.Fatalf("detail length=%d", len(detail))
	}
	summary := summarizeArguments(strings.Repeat("中", 200))
	if !strings.HasSuffix(summary, "…") || len([]rune(summary)) != 161 {
		t.Fatalf("summary was not rune-safe: %q", summary)
	}
	pythonSummary := summarizeToolCall(ToolCall{Function: ToolFunction{Name: "python_execute", Arguments: `{"code":"print(1)","input_artifact_ids":["artifact_1"]}`}})
	if pythonSummary != "运行 8 字 Python 代码，使用 1 个文件" {
		t.Fatalf("python summary=%q", pythonSummary)
	}
}

func TestBuildMessagesKeepsPromptLayersAndToolProtocol(t *testing.T) {
	calls := `[{"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expression\":\"2+2\"}"}}]`
	messages := buildMessages(store.Design{Persona: "耐心数学老师"}, []store.Skill{{ID: "math", Name: "精确计算", Summary: "核验数值结果", WhenToUse: "需要精确计算时使用", TriggerMode: store.SkillTriggerAuto, Content: "使用计算器核验结果", Enabled: true}}, nil, []store.Message{
		{Role: "user", Content: "计算 2+2"},
		{Role: "assistant", Content: "", Reasoning: "需要精确计算", ToolCalls: calls},
		{Role: "tool", Content: "结果：4", ToolCalls: "call_1"},
	})
	if len(messages) != 4 || messages[0].Role != "system" {
		t.Fatalf("messages=%#v", messages)
	}
	for _, want := range []string{
		"平台安全规则 > Soul > 已加载的当前任务相关 Skill > 可用 Memory > 当前对话",
		"耐心数学老师",
		"需要精确计算时使用",
		"不匹配时直接按 Soul 和通用能力回答",
		"装备了工具也不代表必须调用",
	} {
		if !strings.Contains(messages[0].Content, want) {
			t.Fatalf("system prompt misses %q: %s", want, messages[0].Content)
		}
	}
	if strings.Contains(messages[0].Content, "使用计算器核验结果") {
		t.Fatalf("skill body leaked into catalog prompt: %s", messages[0].Content)
	}
	if len(messages[2].ToolCalls) != 1 || messages[2].ToolCalls[0].Function.Name != "calculator" || messages[2].Reasoning != "需要精确计算" {
		t.Fatalf("assistant tool protocol=%#v", messages[2])
	}
	if messages[3].ToolCallID != "call_1" || messages[3].Content != "结果：4" {
		t.Fatalf("tool message=%#v", messages[3])
	}
}

func TestEngineReadsOnlyConfirmedRelevantMemoryAndPersistsReceipt(t *testing.T) {
	ctx := context.Background()
	st, run := newEngineTestStore(t)
	if err := st.SetMemoryEnabled(ctx, run.ID, "2101", true); err != nil {
		t.Fatal(err)
	}
	for _, memory := range []store.Memory{
		{ID: "relevant", RunID: run.ID, StudentID: "2101", Content: "我喜欢篮球", Status: "confirmed"},
		{ID: "candidate", RunID: run.ID, StudentID: "2101", Content: "我参加篮球校队", Status: "candidate"},
		{ID: "unrelated", RunID: run.ID, StudentID: "2101", Content: "我正在学习钢琴", Status: "confirmed"},
	} {
		if _, err := st.AddMemory(ctx, memory); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeClient{answers: []Completion{{Content: "训练建议", Deltas: []string{"训练建议"}, TokensIn: 2, TokensOut: 1}}}
	engine := NewEngine(st, fake, tools.NewRegistry(), "model", "secret", time.Second, 1)
	engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
	var events []Event
	err := engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "memory_turn", Input: "给我篮球训练建议", Design: store.Design{Persona: "教练", SkillMD: "运动建议", MaxTurns: 1}}, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	system := fake.requests[0].Messages[0].Content
	if !strings.Contains(system, "我喜欢篮球") || strings.Contains(system, "篮球校队") || strings.Contains(system, "学习钢琴") {
		t.Fatalf("wrong memory selection in prompt: %s", system)
	}
	if len(events) == 0 || len(events[0].Memories) != 1 || events[0].Memories[0].ID != "relevant" || len(events[0].Skills) != 0 {
		t.Fatalf("context receipt=%#v", events)
	}
	messages, err := st.Messages(ctx, run.ID, "2101", "conv", 0, 20)
	if err != nil || len(messages) != 2 || !strings.Contains(messages[0].ContextReceipt, "relevant") {
		t.Fatalf("persisted receipt messages=%#v err=%v", messages, err)
	}
}

func TestEngineLoadsMatchedSkillBodyOnDemandAndUpdatesReceipt(t *testing.T) {
	ctx := context.Background()
	st, run := newEngineTestStore(t)
	skill := store.Skill{ID: "math", Name: "数学验算", Summary: "独立核验数值结果", WhenToUse: "需要验算数值结果时使用", TriggerMode: store.SkillTriggerAuto, Content: "先列式，再用计算器交叉核验。", Enabled: true}
	call := ToolCall{ID: "load_1", Type: "function", Function: ToolFunction{Name: "load_skill", Arguments: `{"skill_id":"math"}`}}
	fake := &fakeClient{answers: []Completion{
		{ToolCalls: []ToolCall{call}, TokensIn: 2, TokensOut: 1},
		{Content: "验算完成", Deltas: []string{"验算完成"}, TokensIn: 3, TokensOut: 1},
	}}
	engine := NewEngine(st, fake, tools.NewRegistry(), "model", "secret", time.Second, 1)
	engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
	var events []Event
	if err := engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "skill_turn", Input: "帮我验算", Design: store.Design{Persona: "老师", MaxTurns: 2}, Skills: []store.Skill{skill}}, func(event Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	firstSystem := fake.requests[0].Messages[0].Content
	if !strings.Contains(firstSystem, skill.Name) || !strings.Contains(firstSystem, skill.Summary) || !strings.Contains(firstSystem, skill.WhenToUse) || strings.Contains(firstSystem, skill.Content) {
		t.Fatalf("first prompt should contain only catalog metadata: %s", firstSystem)
	}
	secondMessages := fake.requests[1].Messages
	if !strings.Contains(secondMessages[len(secondMessages)-1].Content, skill.Content) {
		t.Fatalf("loaded skill body was not returned as tool content: %#v", secondMessages)
	}
	foundLoaded := false
	for _, event := range events {
		if event.Type == "skill_loaded" && event.SkillID == skill.ID && len(event.Skills) == 1 {
			foundLoaded = true
		}
	}
	if !foundLoaded {
		t.Fatalf("skill_loaded event missing: %#v", events)
	}
	messages, err := st.Messages(ctx, run.ID, "2101", "conv", 0, 20)
	if err != nil || !strings.Contains(messages[0].ContextReceipt, skill.Name) {
		t.Fatalf("updated receipt messages=%#v err=%v", messages, err)
	}
}

func TestLoadedSkillCanRecallScopedConfirmedMemoryAndUpdateReceipt(t *testing.T) {
	ctx := context.Background()
	st, run := newEngineTestStore(t)
	if err := st.SetMemoryEnabled(ctx, run.ID, "2101", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMemory(ctx, store.Memory{ID: "city", RunID: run.ID, StudentID: "2101", Content: "学生住在北京", Status: "confirmed"}); err != nil {
		t.Fatal(err)
	}
	skill := store.Skill{ID: "weather", Name: "查询天气", Summary: "查询天气并给出建议", WhenToUse: "学生询问天气时", TriggerMode: store.SkillTriggerAuto, Content: "如果上下文没有城市，先调用 recall_memory 查询学生的居住城市。", Enabled: true}
	loadCall := ToolCall{ID: "load_1", Type: "function", Function: ToolFunction{Name: "load_skill", Arguments: `{"skill_id":"weather"}`}}
	recallCall := ToolCall{ID: "memory_1", Type: "function", Function: ToolFunction{Name: "recall_memory", Arguments: `{"query":"学生所在城市"}`}}
	fake := &fakeClient{answers: []Completion{
		{ToolCalls: []ToolCall{loadCall}, TokensIn: 2, TokensOut: 1},
		{ToolCalls: []ToolCall{recallCall}, TokensIn: 2, TokensOut: 1},
		{Content: "我来查询北京天气", Deltas: []string{"我来查询北京天气"}, TokensIn: 3, TokensOut: 2},
	}}
	engine := NewEngine(st, fake, tools.NewRegistry(), "model", "secret", time.Second, 1)
	engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
	var events []Event
	if err := engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "skill_memory_turn", Input: "明天天气如何", Design: store.Design{Persona: "助手", MaxTurns: 3}, Skills: []store.Skill{skill}, MemoryMode: store.MemoryModeReviewRequired}, func(event Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fake.requests[0].Messages[0].Content, "学生住在北京") {
		t.Fatalf("location should be recalled on demand, not preloaded: %s", fake.requests[0].Messages[0].Content)
	}
	foundDefinition := false
	for _, definition := range fake.requests[1].Tools {
		if definition.Function.Name == "recall_memory" {
			foundDefinition = true
		}
	}
	if !foundDefinition {
		t.Fatalf("recall_memory was not exposed: %#v", fake.requests[1].Tools)
	}
	thirdMessages := fake.requests[2].Messages
	if !strings.Contains(thirdMessages[len(thirdMessages)-1].Content, "学生住在北京") {
		t.Fatalf("recalled Memory missing from tool result: %#v", thirdMessages)
	}
	foundRecall := false
	for _, event := range events {
		if event.Type == "memory_recalled" && len(event.Memories) == 1 && event.Memories[0].ID == "city" && len(event.Skills) == 1 {
			foundRecall = true
		}
	}
	if !foundRecall {
		t.Fatalf("memory_recalled event missing: %#v", events)
	}
	messages, err := st.Messages(ctx, run.ID, "2101", "conv", 0, 20)
	if err != nil || !strings.Contains(messages[0].ContextReceipt, "学生住在北京") || !strings.Contains(messages[0].ContextReceipt, skill.Name) {
		t.Fatalf("updated context receipt messages=%#v err=%v", messages, err)
	}
}

func TestRecallMemoryUsesConceptMatchAndRequiresMemoryToRemainEnabled(t *testing.T) {
	ctx := context.Background()
	st, run := newEngineTestStore(t)
	if err := st.SetMemoryEnabled(ctx, run.ID, "2101", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMemory(ctx, store.Memory{ID: "city", RunID: run.ID, StudentID: "2101", Content: "学生住在北京", Status: "confirmed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMemory(ctx, store.Memory{ID: "candidate", RunID: run.ID, StudentID: "2101", Content: "学生住在上海", Status: "candidate"}); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(st, &fakeClient{}, tools.NewRegistry(), "model", "secret", time.Second, 1)
	engine.MaxMemoryTokens = 1200
	items, result, err := engine.recallMemory(ctx, Request{RunID: run.ID, StudentID: "2101"}, `{"query":"学生所在城市"}`)
	if err != nil || len(items) != 1 || items[0].ID != "city" || !strings.Contains(result.ModelText, "北京") || strings.Contains(result.ModelText, "上海") {
		t.Fatalf("items=%#v result=%#v err=%v", items, result, err)
	}
	if err := st.SetMemoryEnabled(ctx, run.ID, "2101", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.recallMemory(ctx, Request{RunID: run.ID, StudentID: "2101"}, `{"query":"学生所在城市"}`); err == nil {
		t.Fatal("recall succeeded after Memory was disabled")
	}
}

func TestExplicitSkillIsOnlyExposedAfterAtMention(t *testing.T) {
	skills := []store.Skill{
		{ID: "auto", Name: "自动验算", Summary: "核验答案", WhenToUse: "计算任务", TriggerMode: store.SkillTriggerAuto, Content: "自动正文", Enabled: true},
		{ID: "manual", Name: "苏格拉底提问", Summary: "只提出引导问题", WhenToUse: "学生主动选择时", TriggerMode: store.SkillTriggerExplicit, Content: "明确调用正文", Enabled: true},
	}
	withoutMention := availableSkills(Request{Input: "帮我理解这道题", Skills: skills})
	if len(withoutMention) != 1 || withoutMention[0].ID != "auto" {
		t.Fatalf("explicit skill leaked without mention: %#v", withoutMention)
	}
	withMention := availableSkills(Request{Input: "请用 @苏格拉底提问 帮我理解", Skills: skills})
	if len(withMention) != 2 || withMention[1].ID != "manual" {
		t.Fatalf("explicit skill missing after mention: %#v", withMention)
	}
}

func TestCoreIdentityMemoryIsRecalledAcrossDifferentWording(t *testing.T) {
	ctx := context.Background()
	st, run := newEngineTestStore(t)
	if err := st.SetMemoryEnabled(ctx, run.ID, "2101", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMemory(ctx, store.Memory{ID: "name", RunID: run.ID, StudentID: "2101", Content: "学生希望将AI助手称为豆豆", Status: "confirmed"}); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(st, &fakeClient{}, tools.NewRegistry(), "model", "secret", time.Second, 1)
	items, err := engine.relevantMemories(ctx, run.ID, "2101", "你叫啥", store.MemoryModeReviewRequired)
	if err != nil || len(items) != 1 || items[0].ID != "name" {
		t.Fatalf("core memory=%#v err=%v", items, err)
	}
}

func TestMemoryExtractionCreatesCandidateAndFailureDoesNotFailChat(t *testing.T) {
	t.Run("candidate", func(t *testing.T) {
		ctx := context.Background()
		st, run := newEngineTestStore(t)
		if err := st.SetMemoryEnabled(ctx, run.ID, "2101", true); err != nil {
			t.Fatal(err)
		}
		fake := &fakeClient{answers: []Completion{{Content: "知道了", Deltas: []string{"知道了"}, TokensIn: 2, TokensOut: 1}}}
		engine := NewEngine(st, fake, tools.NewRegistry(), "model", "secret", time.Second, 1)
		engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
		done := make(chan struct{})
		updates := make(chan MemoryUpdate, 4)
		engine.MemoryExtractor = extractorFunc(func(_ context.Context, user, answer string) ([]string, error) {
			defer close(done)
			if user != "我喜欢蓝色" || answer != "知道了" {
				t.Errorf("extract input=%q answer=%q", user, answer)
			}
			return []string{"我喜欢蓝色"}, nil
		})
		if err := engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn", Input: "我喜欢蓝色", Design: store.Design{MaxTurns: 1}, OnMemoryUpdate: func(update MemoryUpdate) { updates <- update }}, func(Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("extractor was not called")
		}
		deadline := time.Now().Add(time.Second)
		for {
			items, err := st.Memories(ctx, run.ID, "2101", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(items) == 1 {
				if items[0].Status != "candidate" {
					t.Fatalf("memory=%#v", items[0])
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("candidate was not stored")
			}
			time.Sleep(time.Millisecond)
		}
		for _, want := range []string{"extracting", "changed"} {
			select {
			case update := <-updates:
				if update.Status != want {
					t.Fatalf("memory update status=%q want=%q", update.Status, want)
				}
				if want == "changed" && (len(update.Items) != 1 || update.Items[0].Content != "我喜欢蓝色") {
					t.Fatalf("changed update=%#v", update)
				}
			case <-time.After(time.Second):
				t.Fatalf("missing memory update %q", want)
			}
		}
	})

	t.Run("adaptive mode confirms naturally extracted memory", func(t *testing.T) {
		ctx := context.Background()
		st, run := newEngineTestStore(t)
		if err := st.SetMemoryEnabled(ctx, run.ID, "2101", true); err != nil {
			t.Fatal(err)
		}
		policy, err := st.SetRunPolicy(ctx, run.ID, store.RunPolicy{MemoryMode: store.MemoryModeAdaptive, AllowedTools: []string{"calculator"}})
		if err != nil {
			t.Fatal(err)
		}
		fake := &fakeClient{answers: []Completion{{Content: "明白", Deltas: []string{"明白"}, TokensIn: 2, TokensOut: 1}}}
		engine := NewEngine(st, fake, tools.NewRegistry(), "model", "secret", time.Second, 1)
		engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
		done := make(chan struct{})
		engine.MemoryExtractor = extractorFunc(func(context.Context, string, string) ([]string, error) {
			defer close(done)
			return []string{"我长期在准备机器人比赛"}, nil
		})
		req := Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn", Input: "我长期在准备机器人比赛", Design: store.Design{MaxTurns: 1}, MemoryMode: policy.MemoryMode, PolicyRevision: policy.Revision}
		if err = engine.Run(ctx, req, func(Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("extractor was not called")
		}
		deadline := time.Now().Add(time.Second)
		for {
			items, listErr := st.Memories(ctx, run.ID, "2101", "")
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(items) == 1 {
				if items[0].Status != "confirmed" {
					t.Fatalf("adaptive memory=%#v", items[0])
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("adaptive memory was not stored")
			}
			time.Sleep(time.Millisecond)
		}
	})

	t.Run("failure", func(t *testing.T) {
		ctx := context.Background()
		st, run := newEngineTestStore(t)
		if err := st.SetMemoryEnabled(ctx, run.ID, "2101", true); err != nil {
			t.Fatal(err)
		}
		fake := &fakeClient{answers: []Completion{{Content: "主回答成功", Deltas: []string{"主回答成功"}, TokensIn: 2, TokensOut: 1}}}
		engine := NewEngine(st, fake, tools.NewRegistry(), "model", "secret", time.Second, 1)
		engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
		failed := make(chan struct{})
		updates := make(chan MemoryUpdate, 4)
		engine.MemoryExtractor = extractorFunc(func(context.Context, string, string) ([]string, error) {
			close(failed)
			return nil, errors.New("extract failed")
		})
		if err := engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn", Input: "普通消息", Design: store.Design{MaxTurns: 1}, OnMemoryUpdate: func(update MemoryUpdate) { updates <- update }}, func(Event) error { return nil }); err != nil {
			t.Fatalf("main chat was blocked by extraction: %v", err)
		}
		select {
		case <-failed:
		case <-time.After(time.Second):
			t.Fatal("extractor failure path was not exercised")
		}
		for _, want := range []string{"extracting", "failed"} {
			select {
			case update := <-updates:
				if update.Status != want {
					t.Fatalf("memory update status=%q want=%q", update.Status, want)
				}
				if want == "failed" && update.Err == nil {
					t.Fatal("failed update omitted internal error")
				}
			case <-time.After(time.Second):
				t.Fatalf("missing memory update %q", want)
			}
		}
	})

	t.Run("clear invalidates in-flight extraction", func(t *testing.T) {
		ctx := context.Background()
		st, run := newEngineTestStore(t)
		if err := st.SetMemoryEnabled(ctx, run.ID, "2101", true); err != nil {
			t.Fatal(err)
		}
		fake := &fakeClient{answers: []Completion{{Content: "收到", Deltas: []string{"收到"}, TokensIn: 2, TokensOut: 1}}}
		engine := NewEngine(st, fake, tools.NewRegistry(), "model", "secret", time.Second, 1)
		engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
		started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
		updates := make(chan MemoryUpdate, 4)
		engine.MemoryExtractor = extractorFunc(func(context.Context, string, string) ([]string, error) {
			close(started)
			<-release
			defer close(finished)
			return []string{"不应复活的候选"}, nil
		})
		if err := engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn", Input: "请记住", Design: store.Design{MaxTurns: 1}, OnMemoryUpdate: func(update MemoryUpdate) { updates <- update }}, func(Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("extractor did not start")
		}
		if err := st.ClearMemories(ctx, run.ID, "2101"); err != nil {
			t.Fatal(err)
		}
		close(release)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("extractor did not finish")
		}
		time.Sleep(10 * time.Millisecond)
		items, err := st.Memories(ctx, run.ID, "2101", "")
		if err != nil || len(items) != 0 {
			t.Fatalf("deleted memory was resurrected: %#v err=%v", items, err)
		}
		for _, want := range []string{"extracting", "discarded"} {
			select {
			case update := <-updates:
				if update.Status != want {
					t.Fatalf("memory update status=%q want=%q", update.Status, want)
				}
			case <-time.After(time.Second):
				t.Fatalf("missing memory update %q", want)
			}
		}
	})
}

func TestEngineUsesLatestDesignAndOnlySelectedConversation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(dir, "students.csv")
	_ = os.WriteFile(csvPath, []byte("id,name\n2101,张三\n"), 0o600)
	if _, err = st.ImportStudentsCSV(ctx, run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old", "fresh"} {
		if _, err = st.CreateConversation(ctx, store.Conversation{ID: id, RunID: run.ID, StudentID: "2101"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = st.AddMessage(ctx, store.Message{RunID: run.ID, StudentID: "2101", ConversationID: "old", TurnID: "old_turn", Role: "user", Content: "旧对话里的秘密暗号"}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeClient{answers: []Completion{{Reasoning: "思考草稿", ReasoningDeltas: []string{"思考", "草稿"}, Content: "新回答", Deltas: []string{"新回答"}, TokensIn: 5, TokensOut: 2}}}
	engine := NewEngine(st, fake, tools.NewRegistry(tools.Calculator{}), "model", "secret", time.Second, 1)
	engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
	engine.MaxReasoningChars = 3
	design := store.Design{Persona: "全新人设", SkillMD: "全新技能规则", Tools: []string{}, MaxTurns: 2}
	var events []Event
	if err = engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", ConversationID: "fresh", TurnID: "new_turn", Input: "新的任务", Design: design}, func(event Event) error { events = append(events, event); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("requests=%d", len(fake.requests))
	}
	var joined string
	for _, m := range fake.requests[0].Messages {
		joined += "\n" + m.Content
	}
	for _, want := range []string{"全新人设", "我的 Skill", "新的任务"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("request misses %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "全新技能规则") {
		t.Fatalf("legacy skill body leaked before load_skill: %s", joined)
	}
	if strings.Contains(joined, "旧对话里的秘密暗号") {
		t.Fatalf("old conversation leaked into request: %s", joined)
	}
	var reasoning string
	for _, event := range events {
		if event.Type == "reasoning_delta" {
			reasoning += event.Delta
		}
	}
	if reasoning != "思考草" {
		t.Fatalf("reasoning limit was not enforced: %q", reasoning)
	}
}
