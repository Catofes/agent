package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing/fstest"
	"time"

	"classroom-agent/internal/agent"
	"classroom-agent/internal/config"
	"classroom-agent/internal/server"
	"classroom-agent/internal/store"
	"classroom-agent/internal/tools"
)

type options struct {
	students    int
	real        bool
	model       string
	baseURL     string
	concurrency int
	timeout     time.Duration
}

type studentClient struct {
	id             string
	http           *http.Client
	conversationID string
}

type metrics struct {
	mu                 sync.Mutex
	errors             []string
	http5xx            int
	databaseLockErrors int
	chatDurations      []time.Duration
	maxGoroutines      int64
}

func (m *metrics) addError(stage, student string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errors = append(m.errors, fmt.Sprintf("%s student=%s: %v", stage, student, err))
	if strings.Contains(strings.ToLower(err.Error()), "database is locked") {
		m.databaseLockErrors++
	}
}

func (m *metrics) observeHTTP(status int, body []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if status >= 500 {
		m.http5xx++
	}
	if strings.Contains(strings.ToLower(string(body)), "database is locked") {
		m.databaseLockErrors++
	}
}

func (m *metrics) addDuration(d time.Duration) {
	m.mu.Lock()
	m.chatDurations = append(m.chatDurations, d)
	m.mu.Unlock()
}

type fakeLLM struct{}

type smokeTool struct{}

func (smokeTool) Definition() tools.Definition {
	return tools.Definition{Type: "function", Function: tools.FunctionSpec{Name: "smoke_echo", Description: "冒烟测试专用工具", Parameters: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}}}
}

func (smokeTool) Execute(_ context.Context, raw json.RawMessage) (tools.Result, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{}, err
	}
	return tools.Result{ModelText: in.Text, Summary: "echo: " + in.Text}, nil
}

func (fakeLLM) Complete(ctx context.Context, req agent.CompletionRequest) (agent.Completion, error) {
	select {
	case <-time.After(15 * time.Millisecond):
	case <-ctx.Done():
		return agent.Completion{}, ctx.Err()
	}
	for _, message := range req.Messages {
		if message.Role == "tool" {
			for _, delta := range []string{"计算", "结果是 391"} {
				if req.OnDelta != nil {
					if err := req.OnDelta(delta); err != nil {
						return agent.Completion{}, err
					}
				}
			}
			return agent.Completion{Content: "计算结果是 391", Deltas: []string{"计算", "结果是 391"}, TokensIn: 18, TokensOut: 6}, nil
		}
	}
	return agent.Completion{
		ToolCalls: []agent.ToolCall{{ID: "smoke_echo", Type: "function", Function: agent.ToolFunction{Name: "smoke_echo", Arguments: `{"text":"391"}`}}},
		TokensIn:  12,
		TokensOut: 3,
	}, nil
}

type countingClient struct {
	inner   agent.Client
	calls   atomic.Int64
	errors  atomic.Int64
	current atomic.Int64
	maximum atomic.Int64
}

func (c *countingClient) Complete(ctx context.Context, req agent.CompletionRequest) (agent.Completion, error) {
	c.calls.Add(1)
	current := c.current.Add(1)
	defer c.current.Add(-1)
	for {
		maximum := c.maximum.Load()
		if current <= maximum || c.maximum.CompareAndSwap(maximum, current) {
			break
		}
	}
	completion, err := c.inner.Complete(ctx, req)
	if err != nil {
		c.errors.Add(1)
	}
	return completion, err
}

func main() {
	opt := options{}
	flag.IntVar(&opt.students, "students", 50, "number of simulated students")
	flag.BoolVar(&opt.real, "real", false, "call the real DeepSeek API (requires DEEPSEEK_API_KEY)")
	flag.StringVar(&opt.model, "model", env("DEEPSEEK_MODEL", "deepseek-v4-flash"), "model used in real mode")
	flag.StringVar(&opt.baseURL, "base-url", env("DEEPSEEK_BASE_URL", "https://api.deepseek.com"), "DeepSeek API base URL")
	flag.IntVar(&opt.concurrency, "llm-concurrency", 8, "application LLM concurrency limit")
	flag.DurationVar(&opt.timeout, "timeout", 90*time.Second, "per-model-call timeout")
	flag.Parse()

	if opt.students < 1 || opt.concurrency < 1 {
		fatalf("students and llm-concurrency must be positive")
	}
	if opt.real && strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")) == "" {
		fatalf("real mode requires DEEPSEEK_API_KEY")
	}

	if err := run(opt); err != nil {
		fatalf("smoke failed: %v", err)
	}
}

func run(opt options) error {
	started := time.Now()
	workDir, err := os.MkdirTemp("", "classroom-agent-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	rosterPath := filepath.Join(workDir, "students.csv")
	if err := writeRoster(rosterPath, opt.students); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(workDir, "smoke.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	run, err := st.EnsureActiveRun(context.Background(), "smoke-run", "50 人冒烟测试")
	if err != nil {
		return err
	}
	if _, err = st.ImportStudentsCSV(context.Background(), run.ID, rosterPath); err != nil {
		return err
	}
	if _, err = st.SetRunPolicy(context.Background(), run.ID, store.RunPolicy{MemoryMode: store.MemoryModeReviewRequired, SkillsEnabled: true, AllowedTools: []string{"smoke_echo"}}); err != nil {
		return err
	}

	var llm agent.Client = fakeLLM{}
	mode := "fake"
	if opt.real {
		mode = "real"
		llm = &agent.DeepSeekClient{
			BaseURL: opt.baseURL,
			APIKey:  os.Getenv("DEEPSEEK_API_KEY"),
			HTTP: &http.Client{Transport: &http.Transport{
				MaxIdleConns:        opt.students * 2,
				MaxIdleConnsPerHost: opt.students,
				IdleConnTimeout:     90 * time.Second,
			}},
		}
	}
	counted := &countingClient{inner: llm}
	cfg := smokeConfig(opt)
	engine := agent.NewEngine(st, counted, tools.NewRegistry(smokeTool{}), opt.model, cfg.AnonymousHMACKey, opt.timeout, opt.concurrency)
	engine.TokenBudget = cfg.StudentTokenBudget
	engine.MaxToolCalls = cfg.MaxToolCalls
	engine.MaxOutputChars = cfg.MaxOutputChars
	engine.MaxReasoningChars = cfg.MaxReasoningChars
	web := fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte("student")},
		"teacher.html":     &fstest.MapFile{Data: []byte("teacher")},
		"screen.html":      &fstest.MapFile{Data: []byte("screen")},
		"ndjson-stream.js": &fstest.MapFile{Data: []byte("parser")},
	}
	templates := fstest.MapFS{"smoke.md": &fstest.MapFile{Data: []byte("# smoke")}}
	app := server.New(cfg, st, engine, web, templates, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service := httptest.NewServer(app.Routes())
	defer func() {
		app.Shutdown()
		service.Close()
	}()

	result := &metrics{}
	stopMonitor := monitorGoroutines(result)
	defer stopMonitor()
	var beforeMem runtime.MemStats
	runtime.ReadMemStats(&beforeMem)

	students := make([]studentClient, opt.students)
	for i := range students {
		jar, _ := cookiejar.New(nil)
		students[i] = studentClient{
			id:   fmt.Sprintf("%04d", 2101+i),
			http: &http.Client{Jar: jar, Timeout: opt.timeout * 3},
		}
	}
	teacherJar, _ := cookiejar.New(nil)
	teacher := &http.Client{Jar: teacherJar, Timeout: opt.timeout * 3}

	if err := requestExpect(teacher, http.MethodPost, service.URL+"/api/teacher/login", map[string]string{"password": cfg.AdminPassword}, http.StatusOK, result); err != nil {
		return fmt.Errorf("teacher login: %w", err)
	}
	for i := range students {
		student := &students[i]
		if err := prepareStudent(service.URL, student, result); err != nil {
			result.addError("prepare", student.id, err)
		}
	}
	if len(result.errors) > 0 {
		return summarizeErrors(result.errors)
	}

	startGate := make(chan struct{})
	var wg sync.WaitGroup
	for i := range students {
		student := &students[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			chatStarted := time.Now()
			err := chat(service.URL, student, result)
			result.addDuration(time.Since(chatStarted))
			if err != nil {
				result.addError("chat", student.id, err)
			}
		}()
	}
	close(startGate)
	wg.Wait()

	callsBeforeLock := counted.calls.Load()
	if err := requestExpect(teacher, http.MethodPost, service.URL+"/api/teacher/lock", map[string]bool{"locked": true}, http.StatusOK, result); err != nil {
		return fmt.Errorf("lock classroom: %w", err)
	}
	for i := range students {
		student := &students[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := map[string]string{"conversation_id": student.conversationID, "message": "锁定后不应调用模型"}
			if err := requestExpect(student.http, http.MethodPost, service.URL+"/api/chat", body, http.StatusLocked, result); err != nil {
				result.addError("locked-chat", student.id, err)
			}
		}()
	}
	wg.Wait()
	if counted.calls.Load() != callsBeforeLock {
		result.addError("locked-chat", "all", fmt.Errorf("model calls increased from %d to %d", callsBeforeLock, counted.calls.Load()))
	}

	for i := range students {
		student := &students[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, header, err := readStudentSnapshot(student.http, service.URL+"/api/events")
			if err != nil {
				result.addError("sse-reconnect", student.id, err)
				return
			}
			if snapshot.Locked != true || snapshot.RunID != "smoke-run" {
				result.addError("sse-reconnect", student.id, fmt.Errorf("unexpected snapshot: %+v", snapshot))
			}
			if !strings.Contains(header.Get("Cache-Control"), "no-transform") || header.Get("X-Accel-Buffering") != "no" {
				result.addError("sse-headers", student.id, fmt.Errorf("Cache-Control=%q X-Accel-Buffering=%q", header.Get("Cache-Control"), header.Get("X-Accel-Buffering")))
			}
		}()
	}
	wg.Wait()
	if err := requestExpect(teacher, http.MethodPost, service.URL+"/api/teacher/lock", map[string]bool{"locked": false}, http.StatusOK, result); err != nil {
		return fmt.Errorf("unlock classroom: %w", err)
	}

	stopMonitor()
	var afterMem runtime.MemStats
	runtime.ReadMemStats(&afterMem)
	printReport(opt, mode, started, result, counted, beforeMem, afterMem)
	if len(result.errors) > 0 || result.http5xx > 0 || result.databaseLockErrors > 0 {
		return summarizeErrors(result.errors)
	}
	return nil
}

func smokeConfig(opt options) config.Config {
	return config.Config{
		AdminPassword:        "smoke-teacher-password",
		AnonymousHMACKey:     "smoke-anonymous-hmac-key",
		SessionTTL:           time.Hour,
		LLMTimeout:           opt.timeout,
		LLMConcurrency:       opt.concurrency,
		StudentTokenBudget:   100_000,
		DefaultMaxTurns:      3,
		MinMaxTurns:          1,
		MaxMaxTurns:          8,
		MaxToolCalls:         4,
		MaxPersonaChars:      4_000,
		MaxSkillChars:        12_000,
		MaxInputChars:        4_000,
		MaxOutputChars:       16_000,
		MaxReasoningChars:    12_000,
		MaxMemoryItems:       100,
		MaxMemoryChars:       400,
		MaxMemoryTokens:      1_200,
		MemoryExtractTimeout: 20 * time.Second,
		DeepSeekModel:        opt.model,
		DeepSeekBaseURL:      opt.baseURL,
		CookieSecure:         false,
		InitialRunName:       "50 人冒烟测试",
	}
}

func writeRoster(path string, count int) error {
	var roster strings.Builder
	roster.WriteString("id,name\n")
	for i := 0; i < count; i++ {
		fmt.Fprintf(&roster, "%04d,模拟学生%d\n", 2101+i, i+1)
	}
	return os.WriteFile(path, []byte(roster.String()), 0o600)
}

func prepareStudent(baseURL string, student *studentClient, result *metrics) error {
	if err := requestExpect(student.http, http.MethodPost, baseURL+"/api/login", map[string]string{"id": student.id}, http.StatusOK, result); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	design := map[string]any{
		"persona":   "你是一位准确、简洁的数学助手",
		"skill_md":  "# 冒烟测试\n调用已选工具并核对返回结果。",
		"tools":     []string{"smoke_echo"},
		"max_turns": 3,
	}
	if err := requestExpect(student.http, http.MethodPut, baseURL+"/api/design", design, http.StatusOK, result); err != nil {
		return fmt.Errorf("save design: %w", err)
	}
	status, raw, err := requestJSON(student.http, http.MethodPost, baseURL+"/api/conversations", map[string]string{})
	result.observeHTTP(status, raw)
	if err != nil {
		return fmt.Errorf("create conversation: %w", err)
	}
	if status != http.StatusCreated {
		return fmt.Errorf("create conversation status=%d body=%s", status, raw)
	}
	var conversation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &conversation); err != nil || conversation.ID == "" {
		return fmt.Errorf("decode conversation: %w body=%s", err, raw)
	}
	student.conversationID = conversation.ID
	snapshot, _, err := readStudentSnapshot(student.http, baseURL+"/api/events")
	if err != nil {
		return fmt.Errorf("initial SSE: %w", err)
	}
	if snapshot.Locked || snapshot.RunID != "smoke-run" {
		return fmt.Errorf("initial SSE snapshot=%+v", snapshot)
	}
	return nil
}

func chat(baseURL string, student *studentClient, result *metrics) error {
	status, raw, err := requestJSON(student.http, http.MethodPost, baseURL+"/api/chat", map[string]string{
		"conversation_id": student.conversationID,
		"message":         "请用计算器计算 23*17，并给出结果。",
	})
	result.observeHTTP(status, raw)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("status=%d body=%s", status, raw)
	}
	var sawTool, sawEnd bool
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Code string `json:"code"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			return fmt.Errorf("decode NDJSON: %w", err)
		}
		switch event.Type {
		case "tool_result":
			sawTool = true
		case "turn_end":
			sawEnd = true
		case "error":
			return fmt.Errorf("stream error code=%s body=%s", event.Code, raw)
		}
	}
	if !sawEnd {
		return errors.New("turn_end missing")
	}
	if !sawTool && !strings.Contains(string(raw), "391") {
		return errors.New("tool action/result missing")
	}
	return nil
}

type classroomSnapshot struct {
	Locked bool   `json:"locked"`
	RunID  string `json:"run_id"`
}

func readStudentSnapshot(client *http.Client, endpoint string) (classroomSnapshot, http.Header, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return classroomSnapshot{}, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return classroomSnapshot{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return classroomSnapshot{}, resp.Header.Clone(), fmt.Errorf("status=%d body=%s", resp.StatusCode, raw)
	}
	scanner := bufio.NewScanner(resp.Body)
	var data []byte
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 && len(data) > 0 {
			break
		}
		if bytes.HasPrefix(line, []byte("data: ")) {
			data = append(data, line[len("data: "):]...)
		}
	}
	if err := scanner.Err(); err != nil {
		return classroomSnapshot{}, resp.Header.Clone(), err
	}
	var snapshot classroomSnapshot
	if len(data) == 0 {
		return snapshot, resp.Header.Clone(), errors.New("SSE snapshot missing")
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, resp.Header.Clone(), err
	}
	return snapshot, resp.Header.Clone(), nil
}

func requestExpect(client *http.Client, method, endpoint string, body any, want int, result *metrics) error {
	status, raw, err := requestJSON(client, method, endpoint, body)
	result.observeHTTP(status, raw)
	if err != nil {
		return err
	}
	if status != want {
		return fmt.Errorf("status=%d want=%d body=%s", status, want, raw)
	}
	return nil
}

func requestJSON(client *http.Client, method, endpoint string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

func monitorGoroutines(result *metrics) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				current := int64(runtime.NumGoroutine())
				for {
					maximum := atomic.LoadInt64(&result.maxGoroutines)
					if current <= maximum || atomic.CompareAndSwapInt64(&result.maxGoroutines, maximum, current) {
						break
					}
				}
			case <-stop:
				return
			}
		}
	}()
	return func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
}

func printReport(opt options, mode string, started time.Time, result *metrics, counted *countingClient, before, after runtime.MemStats) {
	result.mu.Lock()
	durations := append([]time.Duration(nil), result.chatDurations...)
	errorCount := len(result.errors)
	http5xx := result.http5xx
	dbLocks := result.databaseLockErrors
	result.mu.Unlock()
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	heapDelta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	fmt.Println("classroom-agent smoke report")
	fmt.Printf("mode=%s students=%d elapsed=%s\n", mode, opt.students, time.Since(started).Round(time.Millisecond))
	fmt.Printf("chat_p50=%s chat_p95=%s\n", percentile(durations, 0.50), percentile(durations, 0.95))
	fmt.Printf("errors=%d http_5xx=%d database_lock_errors=%d\n", errorCount, http5xx, dbLocks)
	fmt.Printf("llm_calls=%d llm_errors=%d max_llm_concurrency=%d configured_llm_concurrency=%d\n", counted.calls.Load(), counted.errors.Load(), counted.maximum.Load(), opt.concurrency)
	fmt.Printf("max_goroutines=%d heap_delta_bytes=%d\n", atomic.LoadInt64(&result.maxGoroutines), heapDelta)
}

func percentile(values []time.Duration, fraction float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(math.Ceil(float64(len(values))*fraction)) - 1
	if index < 0 {
		index = 0
	}
	return values[index].Round(time.Millisecond)
}

func summarizeErrors(values []string) error {
	if len(values) == 0 {
		return errors.New("smoke metrics exceeded failure threshold")
	}
	limit := len(values)
	if limit > 5 {
		limit = 5
	}
	return fmt.Errorf("%d errors: %s", len(values), strings.Join(values[:limit], "; "))
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
