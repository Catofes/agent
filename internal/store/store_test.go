package store

import (
	"context"
	"database/sql"
	"errors"
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
	if err = st.DeleteConversation(ctx, run.ID, "2102", "first"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-student delete err=%v", err)
	}
	if err = st.DeleteConversation(ctx, run.ID, "2101", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Conversation(ctx, run.ID, "2101", "first"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted conversation still exists: %v", err)
	}
	messages, err := st.Messages(ctx, run.ID, "2101", "first", 0, 10)
	if err != nil || len(messages) != 0 {
		t.Fatalf("deleted conversation retained messages: %#v err=%v", messages, err)
	}
}

func TestDemoConversationsStayOutOfStudentHistory(t *testing.T) {
	ctx := context.Background()
	st, csvPath := testStore(t)
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportStudentsCSV(ctx, run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	student, err := st.CreateConversation(ctx, Conversation{ID: "student", RunID: run.ID, StudentID: "2101"})
	if err != nil || student.Kind != ConversationKindStudent {
		t.Fatalf("student=%#v err=%v", student, err)
	}
	demo, err := st.CreateConversation(ctx, Conversation{ID: "demo", RunID: run.ID, StudentID: "2101", Kind: ConversationKindDemo, Title: "大屏演示"})
	if err != nil || demo.Kind != ConversationKindDemo {
		t.Fatalf("demo=%#v err=%v", demo, err)
	}
	if _, err = st.AddMessage(ctx, Message{RunID: run.ID, StudentID: "2101", ConversationID: demo.ID, TurnID: "turn", Role: "user", Content: "演示问题"}); err != nil {
		t.Fatal(err)
	}
	wall, err := st.Wall(ctx, run.ID)
	if err != nil || len(wall) == 0 || wall[0].ChatTurns != 0 {
		t.Fatalf("demo leaked into wall chat count: %#v err=%v", wall, err)
	}
	studentMessages, err := st.StudentMessages(ctx, run.ID, "2101", 0, 10)
	if err != nil || len(studentMessages) != 0 {
		t.Fatalf("demo leaked into student message history: %#v err=%v", studentMessages, err)
	}
	listed, err := st.Conversations(ctx, run.ID, "2101", 10)
	if err != nil || len(listed) != 1 || listed[0].ID != student.ID {
		t.Fatalf("student conversations=%#v err=%v", listed, err)
	}
	if _, err = st.Conversation(ctx, run.ID, "2101", demo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("student lookup exposed demo: %v", err)
	}
	if _, err = st.DemoConversation(ctx, run.ID, "2101", demo.ID); err != nil {
		t.Fatalf("demo lookup failed: %v", err)
	}
	if err = st.DeleteDemoConversations(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DemoConversation(ctx, run.ID, "2101", demo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("demo cleanup err=%v", err)
	}
}

func TestRunPolicyDefaultsAndRevision(t *testing.T) {
	ctx := context.Background()
	st, _ := testStore(t)
	run, err := st.EnsureActiveRun(ctx, "policy_run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := st.RunPolicy(ctx, run.ID)
	if err != nil || policy.MemoryMode != MemoryModeReviewRequired || !policy.SkillsEnabled || len(policy.PresetSkills) != 0 || len(policy.AllowedTools) != 0 || policy.Revision != 1 {
		t.Fatalf("default policy=%#v err=%v", policy, err)
	}
	if err = st.InitializeRunProviders(ctx, run.ID, "deepseek", "zhipu"); err != nil {
		t.Fatal(err)
	}
	policy, err = st.SetRunPolicy(ctx, run.ID, RunPolicy{MemoryMode: MemoryModeAdaptive, SkillsEnabled: false, PresetSkills: []string{"python-beginner", "python-beginner"}, AllowedTools: []string{"web_fetch", "web_fetch"}, ModelProvider: "qwen", SearchProvider: "disabled"})
	if err != nil || policy.MemoryMode != MemoryModeAdaptive || policy.SkillsEnabled || len(policy.PresetSkills) != 1 || policy.PresetSkills[0] != "python-beginner" || len(policy.AllowedTools) != 1 || policy.AllowedTools[0] != "web_fetch" || policy.ModelProvider != "qwen" || policy.SearchProvider != "disabled" || policy.Revision != 2 {
		t.Fatalf("updated policy=%#v err=%v", policy, err)
	}
}

func TestMemoryIsolationDeletionAndRunScope(t *testing.T) {
	ctx := context.Background()
	st, csvPath := testStore(t)
	run, err := st.EnsureActiveRun(ctx, "memory_run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportStudentsCSV(ctx, run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	if err = st.SetMemoryEnabled(ctx, run.ID, "2101", true); err != nil {
		t.Fatal(err)
	}
	created, err := st.AddMemory(ctx, Memory{ID: "mem_one", RunID: run.ID, StudentID: "2101", Content: "我喜欢篮球", Status: "candidate"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateMemory(ctx, run.ID, "2102", created.ID, "越权修改", "confirmed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-student update err=%v", err)
	}
	other, err := st.Memories(ctx, run.ID, "2102", "")
	if err != nil || len(other) != 0 {
		t.Fatalf("memory leaked to other student: %#v err=%v", other, err)
	}
	updated, err := st.UpdateMemory(ctx, run.ID, "2101", created.ID, "我喜欢打篮球", "confirmed")
	if err != nil || updated.Status != "confirmed" {
		t.Fatalf("update=%#v err=%v", updated, err)
	}
	run2, err := st.CreateRun(ctx, "memory_run_two", "下一堂课", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.MemoryState(ctx, run2.ID, "2101")
	if err != nil || state.Enabled || len(state.Items) != 0 {
		t.Fatalf("memory crossed run: %#v err=%v", state, err)
	}
	if err = st.DeleteMemory(ctx, run.ID, "2101", created.ID); err != nil {
		t.Fatal(err)
	}
	items, err := st.Memories(ctx, run.ID, "2101", "")
	if err != nil || len(items) != 0 {
		t.Fatalf("deleted memory remains: %#v err=%v", items, err)
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

func TestVersionEightDesignSkillMigratesToStructuredSkill(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "skills-v8.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(t.TempDir(), "students.csv")
	if err = os.WriteFile(csvPath, []byte("id,name\n2101,张三\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportStudentsCSV(ctx, run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveDesign(ctx, Design{RunID: run.ID, StudentID: "2101", SkillMD: "先拆解，再核验", Tools: []string{}, MaxTurns: 3}); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`DROP TABLE skills`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`PRAGMA user_version = 8`); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	items, err := migrated.Skills(ctx, run.ID, "2101")
	if err != nil || len(items) != 1 || items[0].Name != "我的 Skill" || items[0].Summary != "我的 Skill" || items[0].WhenToUse == "" || items[0].TriggerMode != SkillTriggerAuto || items[0].Content != "先拆解，再核验" || !items[0].Enabled {
		t.Fatalf("skills=%#v err=%v", items, err)
	}
}

func TestVersionElevenPolicyEnablesSkillsByDefault(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "policy-v11.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`ALTER TABLE run_policies DROP COLUMN skills_enabled`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`PRAGMA user_version = 11`); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	policy, err := migrated.RunPolicy(ctx, run.ID)
	if err != nil || !policy.SkillsEnabled || len(policy.PresetSkills) != 0 || len(policy.AllowedTools) != 0 {
		t.Fatalf("policy=%#v err=%v", policy, err)
	}
}

func TestVersionTwelveAddsPresetPolicyAndRemovesCalculator(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "policy-v12.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(t.TempDir(), "students.csv")
	if err = os.WriteFile(csvPath, []byte("id,name\n2101,张三\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportStudentsCSV(ctx, run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveDesign(ctx, Design{RunID: run.ID, StudentID: "2101", Tools: []string{"calculator", "python_execute"}, MaxTurns: 3}); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`ALTER TABLE run_policies DROP COLUMN preset_skills`,
		`UPDATE run_policies SET allowed_tools='["calculator","web_fetch"]'`,
		`UPDATE designs SET tools='["calculator","python_execute"]'`,
		`PRAGMA user_version = 12`,
	} {
		if _, err = raw.Exec(query); err != nil {
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
	policy, err := migrated.RunPolicy(ctx, run.ID)
	if err != nil || len(policy.PresetSkills) != 0 || len(policy.AllowedTools) != 1 || policy.AllowedTools[0] != "web_fetch" {
		t.Fatalf("policy=%#v err=%v", policy, err)
	}
	design, err := migrated.Design(ctx, run.ID, "2101")
	if err != nil || len(design.Tools) != 1 || design.Tools[0] != "python_execute" {
		t.Fatalf("design=%#v err=%v", design, err)
	}
	var version int
	if err = migrated.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != 15 {
		t.Fatalf("version=%d err=%v", version, err)
	}
}
