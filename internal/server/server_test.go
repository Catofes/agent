package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"classroom-agent/internal/agent"
	"classroom-agent/internal/config"
	"classroom-agent/internal/store"
	"classroom-agent/internal/tools"
)

type directClient struct{}

func (directClient) Complete(_ context.Context, req agent.CompletionRequest) (agent.Completion, error) {
	out := agent.Completion{Content: "测试回答", Deltas: []string{"测试", "回答"}, TokensIn: 3, TokensOut: 2}
	if req.OnDelta != nil {
		for _, delta := range out.Deltas {
			if err := req.OnDelta(delta); err != nil {
				return agent.Completion{}, err
			}
		}
	}
	return out, nil
}

func testServer(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.EnsureActiveRun(context.Background(), "run", "测试课堂")
	if err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(dir, "students.csv")
	_ = os.WriteFile(csvPath, []byte("id,name\n2101,张三\n2102,李四\n"), 0o600)
	if _, err = st.ImportStudentsCSV(context.Background(), run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AdminPassword: "teacher-secret", AnonymousHMACKey: "hmac-secret", SessionTTL: time.Hour, LLMTimeout: time.Second, LLMConcurrency: 4, StudentTokenBudget: 1000, DefaultMaxTurns: 5, MinMaxTurns: 1, MaxMaxTurns: 8, MaxToolCalls: 4, MaxPersonaChars: 100, MaxSkillChars: 1000, MaxInputChars: 100, MaxOutputChars: 1000}
	engine := agent.NewEngine(st, directClient{}, tools.NewRegistry(tools.Calculator{}), "fake", "hmac-secret", time.Second, 4)
	engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("student")}, "teacher.html": &fstest.MapFile{Data: []byte("teacher")}, "screen.html": &fstest.MapFile{Data: []byte("screen")}}
	templates := fstest.MapFS{"quiz.md": &fstest.MapFile{Data: []byte("# template")}}
	app := New(cfg, st, engine, web, templates, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { st.Close() })
	return app.Routes(), st
}

type testClient struct {
	handler http.Handler
	cookie  *http.Cookie
}

func newClient(handler http.Handler) *testClient { return &testClient{handler: handler} }

func requestJSON(t *testing.T, c *testClient, method, target string, body any) (int, map[string]any, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "classroom_session" && cookie.MaxAge >= 0 {
			c.cookie = cookie
		}
	}
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, string(raw)
}

func TestStudentLoginDesignChatAndReplacement(t *testing.T) {
	handler, _ := testServer(t)
	c1 := newClient(handler)
	status, _, _ := requestJSON(t, c1, "POST", "/api/login", map[string]string{"id": "2101"})
	if status != 200 {
		t.Fatalf("login status=%d", status)
	}
	status, me, _ := requestJSON(t, c1, "GET", "/api/me", nil)
	if status != 200 || me["role"] != "student" {
		t.Fatalf("me status=%d body=%v", status, me)
	}
	design := map[string]any{"persona": "<img onerror=alert(1)>", "skill_md": "准确计算", "tools": []string{"calculator"}, "max_turns": 3}
	status, _, raw := requestJSON(t, c1, "PUT", "/api/design", design)
	if status != 200 || !strings.Contains(raw, `\u003cimg onerror`) {
		t.Fatalf("save status=%d body=%s", status, raw)
	}
	status, _, raw = requestJSON(t, c1, "POST", "/api/chat", map[string]string{"message": "你好"})
	if status != 200 || !strings.Contains(raw, "text_delta") || !strings.Contains(raw, "turn_end") {
		t.Fatalf("chat status=%d body=%s", status, raw)
	}
	c2 := newClient(handler)
	status, _, _ = requestJSON(t, c2, "POST", "/api/login", map[string]string{"id": "2101"})
	if status != 200 {
		t.Fatal("second login failed")
	}
	status, out, _ := requestJSON(t, c1, "GET", "/api/me", nil)
	if status != 401 {
		t.Fatalf("old session status=%d body=%v", status, out)
	}
	errBody := out["error"].(map[string]any)
	if errBody["code"] != "SESSION_REPLACED" {
		t.Fatalf("code=%v", errBody["code"])
	}
}

func TestTeacherLockAndRunSwitch(t *testing.T) {
	handler, _ := testServer(t)
	student := newClient(handler)
	status, _, _ := requestJSON(t, student, "POST", "/api/login", map[string]string{"id": "2102"})
	if status != 200 {
		t.Fatal(status)
	}
	teacher := newClient(handler)
	status, _, _ = requestJSON(t, teacher, "POST", "/api/teacher/login", map[string]string{"password": "teacher-secret"})
	if status != 200 {
		t.Fatal(status)
	}
	status, _, _ = requestJSON(t, teacher, "POST", "/api/teacher/lock", map[string]bool{"locked": true})
	if status != 200 {
		t.Fatal(status)
	}
	status, _, _ = requestJSON(t, student, "PUT", "/api/design", map[string]any{"persona": "x", "skill_md": "x", "tools": []string{}, "max_turns": 2})
	if status != 423 {
		t.Fatalf("locked save status=%d", status)
	}
	status, _, _ = requestJSON(t, teacher, "POST", "/api/teacher/run", map[string]string{"name": "新场次"})
	if status != 201 {
		t.Fatal(status)
	}
	status, _, _ = requestJSON(t, student, "GET", "/api/me", nil)
	if status != 401 {
		t.Fatalf("old run session status=%d", status)
	}
}

func TestPagesAreServed(t *testing.T) {
	handler, _ := testServer(t)
	for _, p := range []string{"/", "/teacher", "/screen", "/healthz"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 {
			t.Fatalf("%s status=%d", p, rec.Code)
		}
	}
}
