package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

type blockingClient struct {
	started  chan struct{}
	canceled chan struct{}
	start    sync.Once
	stop     sync.Once
}

type delayedClient struct {
	first   chan struct{}
	release chan struct{}
}

type concurrencyClient struct {
	mu      sync.Mutex
	current int
	max     int
}

func (c *concurrencyClient) Complete(ctx context.Context, req agent.CompletionRequest) (agent.Completion, error) {
	c.mu.Lock()
	c.current++
	if c.current > c.max {
		c.max = c.current
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.current--
		c.mu.Unlock()
	}()
	select {
	case <-time.After(15 * time.Millisecond):
	case <-ctx.Done():
		return agent.Completion{}, ctx.Err()
	}
	if req.OnDelta != nil {
		if err := req.OnDelta("并发回答"); err != nil {
			return agent.Completion{}, err
		}
	}
	return agent.Completion{Content: "并发回答", Deltas: []string{"并发回答"}, TokensIn: 2, TokensOut: 2}, nil
}

func (c *concurrencyClient) maximum() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.max
}

func newDelayedClient() *delayedClient {
	return &delayedClient{first: make(chan struct{}), release: make(chan struct{})}
}

func (c *delayedClient) Complete(ctx context.Context, req agent.CompletionRequest) (agent.Completion, error) {
	if err := req.OnDelta("先到"); err != nil {
		return agent.Completion{}, err
	}
	close(c.first)
	select {
	case <-c.release:
	case <-ctx.Done():
		return agent.Completion{}, ctx.Err()
	}
	if err := req.OnDelta("后到"); err != nil {
		return agent.Completion{}, err
	}
	return agent.Completion{Content: "先到后到", Deltas: []string{"先到", "后到"}, TokensIn: 2, TokensOut: 2}, nil
}

func newBlockingClient() *blockingClient {
	return &blockingClient{started: make(chan struct{}), canceled: make(chan struct{})}
}

func (c *blockingClient) Complete(ctx context.Context, _ agent.CompletionRequest) (agent.Completion, error) {
	c.start.Do(func() { close(c.started) })
	<-ctx.Done()
	c.stop.Do(func() { close(c.canceled) })
	return agent.Completion{}, ctx.Err()
}

func testServer(t *testing.T) (http.Handler, *store.Store) {
	app, st := testServerWithClient(t, directClient{})
	return app.Routes(), st
}

func testServerWithClient(t *testing.T, client agent.Client) (*Server, *store.Store) {
	return testServerWithRoster(t, client, 2)
}

func testServerWithRoster(t *testing.T, client agent.Client, studentCount int) (*Server, *store.Store) {
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
	var roster strings.Builder
	roster.WriteString("id,name\n")
	for i := 1; i <= studentCount; i++ {
		name := fmt.Sprintf("学生%d", i)
		if i == 1 {
			name = "张三"
		} else if i == 2 {
			name = "李四"
		}
		fmt.Fprintf(&roster, "%d,%s\n", 2100+i, name)
	}
	_ = os.WriteFile(csvPath, []byte(roster.String()), 0o600)
	if _, err = st.ImportStudentsCSV(context.Background(), run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AdminPassword: "teacher-secret", AnonymousHMACKey: "hmac-secret", SessionTTL: time.Hour, LLMTimeout: time.Second, LLMConcurrency: 4, StudentTokenBudget: 1000, DefaultMaxTurns: 5, MinMaxTurns: 1, MaxMaxTurns: 8, MaxToolCalls: 4, MaxPersonaChars: 100, MaxSkillChars: 1000, MaxInputChars: 100, MaxOutputChars: 1000}
	engine := agent.NewEngine(st, client, tools.NewRegistry(tools.Calculator{}), "fake", "hmac-secret", time.Second, 4)
	engine.TokenBudget, engine.MaxToolCalls, engine.MaxOutputChars = 1000, 4, 1000
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("student")}, "teacher.html": &fstest.MapFile{Data: []byte("teacher")}, "screen.html": &fstest.MapFile{Data: []byte("screen")}}
	templates := fstest.MapFS{"quiz.md": &fstest.MapFile{Data: []byte("# template")}}
	app := New(cfg, st, engine, web, templates, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { st.Close() })
	return app, st
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
	status, conversation, _ := requestJSON(t, c1, "POST", "/api/conversations", map[string]string{})
	if status != http.StatusCreated {
		t.Fatalf("create conversation status=%d", status)
	}
	conversationID, _ := conversation["id"].(string)
	status, _, raw = requestJSON(t, c1, "POST", "/api/chat", map[string]string{"conversation_id": conversationID, "message": "你好"})
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

func TestConversationAPIScopeAndPagination(t *testing.T) {
	handler, _ := testServer(t)
	first := newClient(handler)
	status, _, _ := requestJSON(t, first, "POST", "/api/login", map[string]string{"id": "2101"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, created, _ := requestJSON(t, first, "POST", "/api/conversations", map[string]string{"title": "隔离测试"})
	if status != http.StatusCreated {
		t.Fatal(status)
	}
	conversationID := created["id"].(string)
	status, _, _ = requestJSON(t, first, "POST", "/api/chat", map[string]string{"conversation_id": conversationID, "message": "只属于张三"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, listed, _ := requestJSON(t, first, "GET", "/api/conversations", nil)
	if status != http.StatusOK || len(listed["conversations"].([]any)) != 1 {
		t.Fatalf("list status=%d body=%v", status, listed)
	}
	status, messages, _ := requestJSON(t, first, "GET", "/api/messages?conversation_id="+conversationID+"&cursor=0", nil)
	if status != http.StatusOK || len(messages["messages"].([]any)) != 2 {
		t.Fatalf("messages status=%d body=%v", status, messages)
	}
	firstMessage := messages["messages"].([]any)[0].(map[string]any)
	cursor := int64(firstMessage["id"].(float64))
	status, page, _ := requestJSON(t, first, "GET", fmt.Sprintf("/api/messages?conversation_id=%s&cursor=%d", conversationID, cursor), nil)
	if status != http.StatusOK || len(page["messages"].([]any)) != 1 {
		t.Fatalf("paged messages status=%d body=%v", status, page)
	}
	status, another, _ := requestJSON(t, first, "POST", "/api/conversations", map[string]string{})
	if status != http.StatusCreated {
		t.Fatal(status)
	}
	status, empty, _ := requestJSON(t, first, "GET", "/api/messages?conversation_id="+another["id"].(string), nil)
	if status != http.StatusOK || len(empty["messages"].([]any)) != 0 {
		t.Fatalf("new conversation inherited history: status=%d body=%v", status, empty)
	}

	second := newClient(handler)
	status, _, _ = requestJSON(t, second, "POST", "/api/login", map[string]string{"id": "2102"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, _, _ = requestJSON(t, second, "GET", "/api/messages?conversation_id="+conversationID, nil)
	if status != http.StatusNotFound {
		t.Fatalf("cross-student read status=%d", status)
	}
	status, _, _ = requestJSON(t, second, "POST", "/api/chat", map[string]string{"conversation_id": conversationID, "message": "越权"})
	if status != http.StatusNotFound {
		t.Fatalf("cross-student chat status=%d", status)
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

func TestJSONRequestsAreStrictAndBounded(t *testing.T) {
	handler, _ := testServer(t)
	for name, raw := range map[string]string{
		"unknown field":  `{"id":"2101","extra":"x"}`,
		"two objects":    `{"id":"2101"}{"id":"2102"}`,
		"oversized body": `{"id":"` + strings.Repeat("1", 70<<10) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}

	student := newClient(handler)
	status, _, _ := requestJSON(t, student, http.MethodPost, "/api/login", map[string]string{"id": "2101"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, body, _ := requestJSON(t, student, http.MethodPost, "/api/chat", map[string]string{"conversation_id": strings.Repeat("c", maxConversationChars+1), "message": "任务"})
	if status != http.StatusBadRequest || body["error"].(map[string]any)["code"] != "INVALID_CONVERSATION_ID" {
		t.Fatalf("long conversation status=%d body=%v", status, body)
	}

	teacher := newClient(handler)
	status, _, _ = requestJSON(t, teacher, http.MethodPost, "/api/teacher/login", map[string]string{"password": "teacher-secret"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, _, _ = requestJSON(t, teacher, http.MethodPost, "/api/teacher/spotlight", map[string]string{"id": "2101", "turn_id": strings.Repeat("t", maxTurnIDChars+1)})
	if status != http.StatusBadRequest {
		t.Fatalf("long spotlight status=%d", status)
	}
}

func TestSpotlightBeforeAndAfterScreenConnect(t *testing.T) {
	handler, _ := testServer(t)
	teacher := newClient(handler)
	status, _, _ := requestJSON(t, teacher, "POST", "/api/teacher/login", map[string]string{"password": "teacher-secret"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, body, _ := requestJSON(t, teacher, "POST", "/api/teacher/spotlight", map[string]string{"id": "2102", "turn_id": ""})
	if status != http.StatusConflict || body["error"].(map[string]any)["code"] != "NO_COMPLETE_TURN" {
		t.Fatalf("empty spotlight status=%d body=%v", status, body)
	}

	student := newClient(handler)
	status, _, _ = requestJSON(t, student, "POST", "/api/login", map[string]string{"id": "2101"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, conversation, _ := requestJSON(t, student, "POST", "/api/conversations", map[string]string{})
	if status != http.StatusCreated {
		t.Fatal(status)
	}
	status, _, firstRaw := requestJSON(t, student, "POST", "/api/chat", map[string]string{"conversation_id": conversation["id"].(string), "message": "第一轮作品"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	firstTurn := turnIDFromNDJSON(t, firstRaw)
	status, _, secondRaw := requestJSON(t, student, "POST", "/api/chat", map[string]string{"conversation_id": conversation["id"].(string), "message": "第二轮作品"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	secondTurn := turnIDFromNDJSON(t, secondRaw)
	if firstTurn == secondTurn {
		t.Fatal("chat turns are not unique")
	}

	stream, cancel, done := startScreenStream(handler)
	if initial := waitSSEJSON(t, stream, 1); initial["empty"] != true {
		t.Fatalf("initial=%v", initial)
	}
	status, _, _ = requestJSON(t, teacher, "POST", "/api/teacher/spotlight", map[string]string{"id": "2101", "turn_id": firstTurn})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	if pushed := waitSSEJSON(t, stream, 2); pushed["name"] != "张三" || pushed["empty"] != false || !screenContains(pushed, "第一轮作品") || screenContains(pushed, "第二轮作品") {
		t.Fatalf("pushed=%v", pushed)
	} else {
		assertPublicScreenFields(t, pushed)
	}
	status, _, _ = requestJSON(t, teacher, "POST", "/api/teacher/spotlight", map[string]string{"id": "2101", "turn_id": ""})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	latest := waitSSEJSON(t, stream, 3)
	if !screenContains(latest, "第二轮作品") || screenContains(latest, "第一轮作品") {
		t.Fatalf("default spotlight did not choose latest complete turn: %v", latest)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnected screen stream did not close")
	}

	stream2, cancel2, done2 := startScreenStream(handler)
	defer func() { cancel2(); <-done2 }()
	if snapshot := waitSSEJSON(t, stream2, 1); snapshot["name"] != "张三" || !screenContains(snapshot, "第二轮作品") {
		t.Fatalf("snapshot=%v", snapshot)
	}

	student2 := newClient(handler)
	status, _, _ = requestJSON(t, student2, "POST", "/api/login", map[string]string{"id": "2102"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, conversation2, _ := requestJSON(t, student2, "POST", "/api/conversations", map[string]string{})
	if status != http.StatusCreated {
		t.Fatal(status)
	}
	status, _, _ = requestJSON(t, student2, "POST", "/api/chat", map[string]string{"conversation_id": conversation2["id"].(string), "message": "李四的作品"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, _, _ = requestJSON(t, teacher, "POST", "/api/teacher/spotlight", map[string]string{"id": "2102", "turn_id": ""})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	if switched := waitSSEJSON(t, stream2, 2); switched["name"] != "李四" || !screenContains(switched, "李四的作品") {
		t.Fatalf("screen did not switch students: %v", switched)
	}
}

func turnIDFromNDJSON(t *testing.T, raw string) string {
	t.Helper()
	for _, line := range strings.Split(raw, "\n") {
		var event agent.Event
		if json.Unmarshal([]byte(line), &event) == nil && event.Type == "turn_start" && event.TurnID != "" {
			return event.TurnID
		}
	}
	t.Fatalf("turn_start missing from %q", raw)
	return ""
}

func screenContains(screen map[string]any, want string) bool {
	raw, _ := json.Marshal(screen["messages"])
	return strings.Contains(string(raw), want)
}

func assertPublicScreenFields(t *testing.T, screen map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(screen)
	for _, forbidden := range []string{"student_id", "conversation_id", "turn_id", "tool_calls", "reasoning_content", "tokens_in", "tokens_out", "estimated_cost", "created_at"} {
		if strings.Contains(string(raw), `"`+forbidden+`"`) {
			t.Fatalf("public screen leaked %s: %s", forbidden, raw)
		}
	}
}

func TestShutdownClosesStreamsAndCancelsLLM(t *testing.T) {
	client := newBlockingClient()
	app, _ := testServerWithClient(t, client)
	handler := app.Routes()
	student := newClient(handler)
	status, _, _ := requestJSON(t, student, http.MethodPost, "/api/login", map[string]string{"id": "2101"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, conversation, _ := requestJSON(t, student, http.MethodPost, "/api/conversations", map[string]string{})
	if status != http.StatusCreated {
		t.Fatal(status)
	}
	teacher := newClient(handler)
	status, _, _ = requestJSON(t, teacher, http.MethodPost, "/api/teacher/login", map[string]string{"password": "teacher-secret"})
	if status != http.StatusOK {
		t.Fatal(status)
	}

	studentEvents, cancelStudent, studentDone := startHandlerStream(handler, http.MethodGet, "/api/events", student.cookie, nil)
	defer cancelStudent()
	wallEvents, cancelWall, wallDone := startHandlerStream(handler, http.MethodGet, "/api/teacher/wall", teacher.cookie, nil)
	defer cancelWall()
	screenEvents, cancelScreen, screenDone := startHandlerStream(handler, http.MethodGet, "/api/screen/events", nil, nil)
	defer cancelScreen()
	for name, stream := range map[string]*streamResponse{"student": studentEvents, "wall": wallEvents, "screen": screenEvents} {
		waitFlush(t, name, stream)
	}

	chatBody, _ := json.Marshal(map[string]string{"conversation_id": conversation["id"].(string), "message": "等待模型"})
	_, cancelChat, chatDone := startHandlerStream(handler, http.MethodPost, "/api/chat", student.cookie, chatBody)
	defer cancelChat()
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("LLM request did not start")
	}

	started := time.Now()
	app.Shutdown()
	app.Shutdown()
	deadline := time.After(2 * time.Second)
	for name, done := range map[string]<-chan struct{}{"student": studentDone, "wall": wallDone, "screen": screenDone, "chat": chatDone, "llm": client.canceled} {
		select {
		case <-done:
		case <-deadline:
			t.Fatalf("%s did not stop within 2 seconds", name)
		}
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("shutdown took %v", elapsed)
	}
	if got := app.studentHub.Count() + app.wallHub.Count() + app.screenHub.Count(); got != 0 {
		t.Fatalf("shutdown left %d event subscribers", got)
	}
}

func TestFiftyStudentsChatWithinGlobalConcurrencyLimit(t *testing.T) {
	client := &concurrencyClient{}
	app, st := testServerWithRoster(t, client, 50)
	handler := app.Routes()
	students := make([]*testClient, 50)
	conversationIDs := make([]string, 50)
	for i := range students {
		students[i] = newClient(handler)
		id := strconv.Itoa(2101 + i)
		status, _, _ := requestJSON(t, students[i], http.MethodPost, "/api/login", map[string]string{"id": id})
		if status != http.StatusOK {
			t.Fatalf("login %s status=%d", id, status)
		}
		status, conversation, _ := requestJSON(t, students[i], http.MethodPost, "/api/conversations", map[string]string{})
		if status != http.StatusCreated {
			t.Fatalf("conversation %s status=%d", id, status)
		}
		conversationIDs[i] = conversation["id"].(string)
	}
	type result struct {
		index  int
		status int
		raw    string
	}
	results := make(chan result, len(students))
	var wg sync.WaitGroup
	for i := range students {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, _, raw := requestJSON(t, students[i], http.MethodPost, "/api/chat", map[string]string{"conversation_id": conversationIDs[i], "message": "并发任务"})
			results <- result{index: i, status: status, raw: raw}
		}(i)
	}
	wg.Wait()
	close(results)
	for got := range results {
		if got.status != http.StatusOK || !strings.Contains(got.raw, `"type":"turn_end"`) {
			t.Fatalf("student %d status=%d body=%s", got.index+1, got.status, got.raw)
		}
	}
	if max := client.maximum(); max < 2 || max > 4 {
		t.Fatalf("maximum LLM concurrency=%d want 2..4", max)
	}
	wall, err := st.Wall(context.Background(), "run")
	if err != nil || len(wall) != 50 {
		t.Fatalf("wall students=%d err=%v", len(wall), err)
	}
	for _, student := range wall {
		if student.ChatTurns != 1 {
			t.Fatalf("student %s turns=%d", student.ID, student.ChatTurns)
		}
	}
}

func TestSameStudentDoubleChatCreatesOneCompleteTurn(t *testing.T) {
	client := newDelayedClient()
	app, st := testServerWithClient(t, client)
	handler := app.Routes()
	student := newClient(handler)
	status, _, _ := requestJSON(t, student, http.MethodPost, "/api/login", map[string]string{"id": "2101"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, conversation, _ := requestJSON(t, student, http.MethodPost, "/api/conversations", map[string]string{})
	if status != http.StatusCreated {
		t.Fatal(status)
	}
	conversationID := conversation["id"].(string)
	body, _ := json.Marshal(map[string]string{"conversation_id": conversationID, "message": "第一条"})
	_, cancel, done := startHandlerStream(handler, http.MethodPost, "/api/chat", student.cookie, body)
	defer cancel()
	select {
	case <-client.first:
	case <-time.After(2 * time.Second):
		t.Fatal("first chat did not start")
	}
	status, errorBody, _ := requestJSON(t, student, http.MethodPost, "/api/chat", map[string]string{"conversation_id": conversationID, "message": "重复发送"})
	if status != http.StatusConflict || errorBody["error"].(map[string]any)["code"] != "CHAT_IN_PROGRESS" {
		t.Fatalf("duplicate status=%d body=%v", status, errorBody)
	}
	close(client.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("first chat did not complete")
	}
	messages, err := st.Messages(context.Background(), "run", "2101", conversationID, 0, 20)
	if err != nil || len(messages) != 2 || messages[0].Content != "第一条" || messages[1].FinishReason != "completed" {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
}

func TestChatFlushesFirstDeltaBeforeCompletion(t *testing.T) {
	client := newDelayedClient()
	app, _ := testServerWithClient(t, client)
	handler := app.Routes()
	student := newClient(handler)
	status, _, _ := requestJSON(t, student, http.MethodPost, "/api/login", map[string]string{"id": "2101"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, conversation, _ := requestJSON(t, student, http.MethodPost, "/api/conversations", map[string]string{})
	if status != http.StatusCreated {
		t.Fatal(status)
	}
	body, _ := json.Marshal(map[string]string{"conversation_id": conversation["id"].(string), "message": "流式测试"})
	stream, cancel, done := startHandlerStream(handler, http.MethodPost, "/api/chat", student.cookie, body)
	defer cancel()
	select {
	case <-client.first:
	case <-time.After(2 * time.Second):
		t.Fatal("first upstream delta did not arrive")
	}
	partial := string(stream.bytes())
	if !strings.Contains(partial, `"type":"text_delta"`) || !strings.Contains(partial, `"delta":"先到"`) {
		t.Fatalf("first delta was not flushed: %s", partial)
	}
	if strings.Contains(partial, "后到") || strings.Contains(partial, `"type":"turn_end"`) {
		t.Fatalf("response completed before delayed upstream was released: %s", partial)
	}
	close(client.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("chat did not finish after upstream release")
	}
	complete := string(stream.bytes())
	if !strings.Contains(complete, `"delta":"后到"`) || !strings.Contains(complete, `"type":"turn_end"`) {
		t.Fatalf("final stream is incomplete: %s", complete)
	}
}

type streamResponse struct {
	mu      sync.Mutex
	header  http.Header
	body    bytes.Buffer
	flushed chan struct{}
}

func newStreamResponse() *streamResponse {
	return &streamResponse{header: make(http.Header), flushed: make(chan struct{}, 8)}
}

func (w *streamResponse) Header() http.Header { return w.header }
func (w *streamResponse) WriteHeader(int)     {}
func (w *streamResponse) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}
func (w *streamResponse) Flush() {
	select {
	case w.flushed <- struct{}{}:
	default:
	}
}
func (w *streamResponse) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.body.Bytes()...)
}

func startScreenStream(handler http.Handler) (*streamResponse, context.CancelFunc, <-chan struct{}) {
	return startHandlerStream(handler, http.MethodGet, "/api/screen/events", nil, nil)
}

func startHandlerStream(handler http.Handler, method, target string, cookie *http.Cookie, body []byte) (*streamResponse, context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader).WithContext(ctx)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := newStreamResponse()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(w, req)
		close(done)
	}()
	return w, cancel, done
}

func waitFlush(t *testing.T, name string, response *streamResponse) {
	t.Helper()
	select {
	case <-response.flushed:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s stream did not flush", name)
	}
}

func waitSSEJSON(t *testing.T, response *streamResponse, want int) map[string]any {
	t.Helper()
	for {
		select {
		case <-response.flushed:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for SSE flush")
		}
		var events []map[string]any
		scanner := bufio.NewScanner(bytes.NewReader(response.bytes()))
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var out map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &out); err != nil {
				t.Fatalf("decode SSE: %v", err)
			}
			events = append(events, out)
		}
		if len(events) >= want {
			return events[want-1]
		}
	}
}
