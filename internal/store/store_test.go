package store

import (
	"context"
	"database/sql"
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
	conversation, err := st.CreateConversation(ctx, Conversation{ID: "conv_one", RunID: run.ID, StudentID: "2101"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.AddMessage(ctx, Message{RunID: run.ID, StudentID: "2101", ConversationID: conversation.ID, TurnID: "t1", Role: "user", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err = st.AddUsage(ctx, run.ID, "2101", 10, 5, 0, .1); err != nil {
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
	if _, err = st.Conversation(ctx, run2.ID, "2101", conversation.ID); err == nil {
		t.Fatal("conversation leaked into new run")
	}
	msgs, err := st.StudentMessages(ctx, run2.ID, "2101", 0, 10)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("messages leaked: %v %v", msgs, err)
	}
	active, err := st.ActiveRun(ctx)
	if err != nil || active.ID != run2.ID {
		t.Fatalf("active=%#v err=%v", active, err)
	}
}

func TestConversationIsolation(t *testing.T) {
	ctx := context.Background()
	st, csvPath := testStore(t)
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportStudentsCSV(ctx, run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Conversation{{ID: "first", RunID: run.ID, StudentID: "2101"}, {ID: "second", RunID: run.ID, StudentID: "2101"}, {ID: "other", RunID: run.ID, StudentID: "2102"}} {
		if _, err = st.CreateConversation(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = st.AddMessage(ctx, Message{RunID: run.ID, StudentID: "2101", ConversationID: "first", TurnID: "t1", Role: "user", Content: "第一段历史"}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.AddMessage(ctx, Message{RunID: run.ID, StudentID: "2101", ConversationID: "second", TurnID: "t2", Role: "user", Content: "第二段历史"}); err != nil {
		t.Fatal(err)
	}
	first, err := st.Messages(ctx, run.ID, "2101", "first", 0, 10)
	if err != nil || len(first) != 1 || first[0].Content != "第一段历史" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	if _, err = st.Conversation(ctx, run.ID, "2102", "first"); err == nil {
		t.Fatal("another student accessed the conversation")
	}
	if _, err = st.AddMessage(ctx, Message{RunID: run.ID, StudentID: "2102", ConversationID: "first", TurnID: "bad", Role: "user", Content: "越权"}); err == nil {
		t.Fatal("cross-student message write succeeded")
	}
	list, err := st.Conversations(ctx, run.ID, "2101", 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("list=%#v err=%v", list, err)
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

func TestVersionOneMessagesMigrateIntoLegacyConversation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")
	csvPath := filepath.Join(dir, "students.csv")
	if err := os.WriteFile(csvPath, []byte("id,name\n2101,张三\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportStudentsCSV(ctx, run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateConversation(ctx, Conversation{ID: "will_be_removed", RunID: run.ID, StudentID: "2101"}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.AddMessage(ctx, Message{RunID: run.ID, StudentID: "2101", ConversationID: "will_be_removed", TurnID: "turn", Role: "user", Content: "旧消息"}); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP INDEX messages_conversation`,
		`ALTER TABLE messages DROP COLUMN conversation_id`,
		`DROP TABLE conversations`,
		`PRAGMA user_version = 1`,
	} {
		if _, err = raw.Exec(q); err != nil {
			raw.Close()
			t.Fatal(err)
		}
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	conversations, err := migrated.Conversations(ctx, run.ID, "2101", 10)
	if err != nil || len(conversations) != 1 || conversations[0].Title != "历史对话" {
		t.Fatalf("conversations=%#v err=%v", conversations, err)
	}
	messages, err := migrated.Messages(ctx, run.ID, "2101", conversations[0].ID, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].Content != "旧消息" {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
}
