package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	csvPath := filepath.Join(dir, "students.csv")
	if err = os.WriteFile(csvPath, []byte("id,name\n2101,张三\n2102,李四\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return st, csvPath
}

func TestStoreRunIsolationAndSessionReplacement(t *testing.T) {
	ctx := context.Background()
	st, csvPath := testStore(t)
	run, err := st.EnsureActiveRun(ctx, "run_one", "第一场")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := st.ImportStudentsCSV(ctx, run.ID, csvPath); err != nil || n != 2 {
		t.Fatalf("import=%d err=%v", n, err)
	}
	if _, err = st.SaveDesign(ctx, Design{RunID: run.ID, StudentID: "2101", Persona: "老师", SkillMD: "步骤", Tools: []string{"calculator"}, MaxTurns: 5}); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(time.Hour)
	if err = st.CreateSession(ctx, Session{TokenHash: "old", RunID: run.ID, StudentID: "2101", ExpiresAt: exp}); err != nil {
		t.Fatal(err)
	}
	if err = st.CreateSession(ctx, Session{TokenHash: "new", RunID: run.ID, StudentID: "2101", ExpiresAt: exp}); err != nil {
		t.Fatal(err)
	}
	old, err := st.Session(ctx, "old")
	if err != nil || old.RevokedAt == nil {
		t.Fatalf("old session not revoked: %#v %v", old, err)
	}
	if _, err = st.AddMessage(ctx, Message{RunID: run.ID, StudentID: "2101", TurnID: "t1", Role: "user", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err = st.AddUsage(ctx, run.ID, "2101", 10, 5, .1); err != nil {
		t.Fatal(err)
	}
	run2, err := st.CreateRun(ctx, "run_two", "第二场", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Student(ctx, run2.ID, "2101"); err != nil {
		t.Fatal("roster was not copied", err)
	}
	if _, err = st.Design(ctx, run2.ID, "2101"); err == nil {
		t.Fatal("design leaked into new run")
	}
	msgs, err := st.Messages(ctx, run2.ID, "2101", 0, 10)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("messages leaked: %v %v", msgs, err)
	}
	active, err := st.ActiveRun(ctx)
	if err != nil || active.ID != run2.ID {
		t.Fatalf("active=%#v err=%v", active, err)
	}
}

func TestImportCSVIsAtomic(t *testing.T) {
	ctx := context.Background()
	st, _ := testStore(t)
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad.csv")
	_ = os.WriteFile(bad, []byte("id,name\n1,甲\n1,乙\n"), 0o600)
	if _, err = st.ImportStudentsCSV(ctx, run.ID, bad); err == nil {
		t.Fatal("expected duplicate error")
	}
	if _, err = st.Student(ctx, run.ID, "1"); err == nil {
		t.Fatal("partial import occurred")
	}
}
