package agent

import (
	"context"
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
	u, err := st.Usage(ctx, run.ID, "2101")
	if err != nil || u.TokensIn != 25 || u.TokensOut != 6 {
		t.Fatalf("usage=%#v err=%v", u, err)
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
