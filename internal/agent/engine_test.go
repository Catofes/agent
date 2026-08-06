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
	if msgs[3].FinishReason != "completed" {
		t.Fatalf("completion reason was not persisted: %#v", msgs[3])
	}
	u, err := st.Usage(ctx, run.ID, "2101")
	if err != nil || u.TokensIn != 25 || u.TokensOut != 6 {
		t.Fatalf("usage=%#v err=%v", u, err)
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
}

func TestBuildMessagesKeepsPromptLayersAndToolProtocol(t *testing.T) {
	calls := `[{"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expression\":\"2+2\"}"}}]`
	messages := buildMessages(store.Design{Persona: "耐心数学老师", SkillMD: "仅在精确计算时使用"}, []store.Message{
		{Role: "user", Content: "计算 2+2"},
		{Role: "assistant", Content: "", Reasoning: "需要精确计算", ToolCalls: calls},
		{Role: "tool", Content: "结果：4", ToolCalls: "call_1"},
	})
	if len(messages) != 4 || messages[0].Role != "system" {
		t.Fatalf("messages=%#v", messages)
	}
	for _, want := range []string{
		"平台安全规则 > Soul > 当前任务相关的 Skill > 可用 Memory > 当前对话",
		"耐心数学老师",
		"仅在精确计算时使用",
		"不适用时按 Soul 和通用能力正常回答",
		"装备了工具也不代表必须调用",
	} {
		if !strings.Contains(messages[0].Content, want) {
			t.Fatalf("system prompt misses %q: %s", want, messages[0].Content)
		}
	}
	if len(messages[2].ToolCalls) != 1 || messages[2].ToolCalls[0].Function.Name != "calculator" || messages[2].Reasoning != "需要精确计算" {
		t.Fatalf("assistant tool protocol=%#v", messages[2])
	}
	if messages[3].ToolCallID != "call_1" || messages[3].Content != "结果：4" {
		t.Fatalf("tool message=%#v", messages[3])
	}
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
	for _, want := range []string{"全新人设", "全新技能规则", "新的任务"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("request misses %q: %s", want, joined)
		}
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
