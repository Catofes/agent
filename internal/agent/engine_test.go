package agent

import (
	"context"
	"os"
	"path/filepath"
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
	fake := &fakeClient{answers: []Completion{{ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: ToolFunction{Name: "calculator", Arguments: `{"expression":"23*17"}`}}}, TokensIn: 10, TokensOut: 2}, {Content: "结果是 391", Deltas: []string{"结果是 ", "391"}, TokensIn: 15, TokensOut: 4}}}
	engine := NewEngine(st, fake, tools.NewRegistry(tools.Calculator{}), "model", "secret", time.Second, 2)
	engine.TokenBudget = 1000
	engine.MaxToolCalls = 4
	engine.MaxOutputChars = 1000
	var events []Event
	err = engine.Run(ctx, Request{RunID: run.ID, StudentID: "2101", TurnID: "turn", Input: "计算", Design: store.Design{Persona: "数学老师", SkillMD: "准确", Tools: []string{"calculator"}, MaxTurns: 3}}, func(e Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("calls=%d", len(fake.requests))
	}
	if got := fake.requests[0].UserID; len(got) != 32 || got == "2101" {
		t.Fatalf("anonymous id=%q", got)
	}
	if len(events) < 5 || events[len(events)-1].Type != "turn_end" {
		t.Fatalf("events=%#v", events)
	}
	msgs, err := st.Messages(ctx, run.ID, "2101", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("messages=%#v", msgs)
	}
	u, err := st.Usage(ctx, run.ID, "2101")
	if err != nil || u.TokensIn != 25 || u.TokensOut != 6 {
		t.Fatalf("usage=%#v err=%v", u, err)
	}
}
