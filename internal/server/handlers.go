package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"classroom-agent/internal/agent"
	"classroom-agent/internal/store"
)

func (s *Server) getDesign(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	d, err := s.Store.Design(r.Context(), p.Session.RunID, p.Session.StudentID)
	if errors.Is(err, sql.ErrNoRows) {
		d = store.Design{Persona: "", SkillMD: "", Tools: []string{}, MaxTurns: s.Config.DefaultMaxTurns}
		writeJSON(w, 200, d)
		return
	}
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取设计失败")
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	policy, err := s.Store.RunPolicy(r.Context(), p.Session.RunID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取课堂能力失败")
		return
	}
	writeJSON(w, 200, map[string]any{
		"memory_mode":     policy.MemoryMode,
		"allowed_tools":   policy.AllowedTools,
		"available_tools": s.Agent.Tools.Names(),
		"revision":        policy.Revision,
	})
}

func (s *Server) saveDesign(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var in struct {
		Persona  string   `json:"persona"`
		SkillMD  string   `json:"skill_md"`
		Tools    []string `json:"tools"`
		MaxTurns int      `json:"max_turns"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if runeLen(in.Persona) > s.Config.MaxPersonaChars || runeLen(in.SkillMD) > s.Config.MaxSkillChars {
		writeError(w, 400, "TEXT_TOO_LONG", "人设或技能手册超过长度限制")
		return
	}
	if in.MaxTurns < s.Config.MinMaxTurns || in.MaxTurns > s.Config.MaxMaxTurns {
		writeError(w, 400, "INVALID_MAX_TURNS", fmt.Sprintf("最大步数应在 %d 到 %d 之间", s.Config.MinMaxTurns, s.Config.MaxMaxTurns))
		return
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(in.Tools))
	for _, name := range in.Tools {
		if _, ok := s.Agent.Tools.Get(name); !ok {
			writeError(w, 400, "UNKNOWN_TOOL", "包含未开放的装备")
			return
		}
		if !seen[name] {
			seen[name] = true
			clean = append(clean, name)
		}
	}
	s.controlMu.RLock()
	defer s.controlMu.RUnlock()
	run, err := s.Store.Run(r.Context(), p.Session.RunID)
	if err != nil || run.Status != "active" || run.Locked {
		writeError(w, 423, "CLASS_LOCKED", "老师已暂停课堂操作")
		return
	}
	d, err := s.Store.SaveDesign(r.Context(), store.Design{RunID: p.Session.RunID, StudentID: p.Session.StudentID, Persona: strings.TrimSpace(in.Persona), SkillMD: strings.TrimSpace(in.SkillMD), Tools: clean, MaxTurns: in.MaxTurns})
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "保存设计失败")
		return
	}
	s.wallHub.Publish(struct{}{})
	writeJSON(w, 200, d)
}

type templateDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	entries, err := fs.ReadDir(s.Templates, ".")
	if err != nil {
		writeError(w, 500, "TEMPLATE_ERROR", "读取模板失败")
		return
	}
	names := map[string]string{"quiz-master.md": "出题官", "weekly-editor.md": "周报小编", "debate-coach.md": "辩论教练"}
	var out []templateDTO
	for _, e := range entries {
		if e.IsDir() || !safeFileBase(e.Name()) {
			continue
		}
		b, err := fs.ReadFile(s.Templates, e.Name())
		if err != nil {
			continue
		}
		display := names[e.Name()]
		if display == "" {
			display = strings.TrimSuffix(e.Name(), ".md")
		}
		out = append(out, templateDTO{ID: strings.TrimSuffix(e.Name(), ".md"), Name: display, Content: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, map[string]any{"templates": out})
}

func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	items, err := s.Store.Conversations(r.Context(), p.Session.RunID, p.Session.StudentID, 100)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取对话列表失败")
		return
	}
	writeJSON(w, 200, map[string]any{"conversations": items})
}

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	run, err := s.Store.Run(r.Context(), p.Session.RunID)
	if err != nil || run.Locked {
		writeError(w, 423, "CLASS_LOCKED", "老师已暂停课堂操作")
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if runeLen(in.Title) > 80 {
		writeError(w, 400, "TITLE_TOO_LONG", "对话标题不能超过 80 个字")
		return
	}
	c, err := s.Store.CreateConversation(r.Context(), store.Conversation{ID: newID("conv_"), RunID: p.Session.RunID, StudentID: p.Session.StudentID, Title: in.Title})
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "新建对话失败")
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" || runeLen(id) > maxConversationChars {
		writeError(w, 404, "CONVERSATION_NOT_FOUND", "未找到该对话")
		return
	}
	run, err := s.Store.Run(r.Context(), p.Session.RunID)
	if err != nil || run.Status != "active" || run.Locked {
		writeError(w, 423, "CLASS_LOCKED", "老师已暂停课堂操作")
		return
	}
	err = s.Store.DeleteConversation(r.Context(), p.Session.RunID, p.Session.StudentID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "CONVERSATION_NOT_FOUND", "未找到该对话")
		return
	}
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "删除对话失败")
		return
	}
	s.wallHub.Publish(struct{}{})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	conversationID := strings.TrimSpace(r.URL.Query().Get("conversation_id"))
	if conversationID == "" {
		writeError(w, 400, "CONVERSATION_REQUIRED", "请选择一个对话")
		return
	}
	if runeLen(conversationID) > maxConversationChars {
		writeError(w, 400, "INVALID_CONVERSATION_ID", "对话标识不正确")
		return
	}
	if _, err := s.Store.Conversation(r.Context(), p.Session.RunID, p.Session.StudentID, conversationID); err != nil {
		writeError(w, 404, "CONVERSATION_NOT_FOUND", "未找到该对话")
		return
	}
	items, err := s.Store.Messages(r.Context(), p.Session.RunID, p.Session.StudentID, conversationID, parseCursor(r.URL.Query().Get("cursor")), 200)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取对话失败")
		return
	}
	next := int64(0)
	if len(items) > 0 {
		next = items[len(items)-1].ID
	}
	writeJSON(w, 200, map[string]any{"messages": items, "next_cursor": next})
}

func (s *Server) getMemory(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	policy, err := s.Store.RunPolicy(r.Context(), p.Session.RunID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取课堂 Memory 策略失败")
		return
	}
	state, err := s.Store.MemoryState(r.Context(), p.Session.RunID, p.Session.StudentID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取 Memory 失败")
		return
	}
	tokens := 0
	for _, item := range state.Items {
		tokens += memoryTokenEstimate(item.Content)
	}
	writeJSON(w, 200, map[string]any{
		"enabled": state.Enabled,
		"items":   state.Items,
		"usage":   map[string]int{"items": len(state.Items), "estimated_tokens": tokens},
		"limits":  map[string]int{"items": s.Config.MaxMemoryItems, "chars_per_item": s.Config.MaxMemoryChars, "estimated_tokens": s.Config.MaxMemoryTokens},
		"scope":   "current_run",
		"mode":    policy.MemoryMode,
	})
}

func (s *Server) setMemorySettings(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	policy, err := s.Store.RunPolicy(r.Context(), p.Session.RunID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取课堂 Memory 策略失败")
		return
	}
	if policy.MemoryMode == store.MemoryModeDisabled {
		writeError(w, http.StatusForbidden, "MEMORY_DISABLED_BY_TEACHER", "老师未在本场次开放 Memory")
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.Store.SetMemoryEnabled(r.Context(), p.Session.RunID, p.Session.StudentID, in.Enabled); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "更新 Memory 开关失败")
		return
	}
	writeJSON(w, 200, map[string]bool{"enabled": in.Enabled})
}

func (s *Server) updateMemory(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	policy, err := s.Store.RunPolicy(r.Context(), p.Session.RunID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取课堂 Memory 策略失败")
		return
	}
	if policy.MemoryMode == store.MemoryModeDisabled {
		writeError(w, http.StatusForbidden, "MEMORY_DISABLED_BY_TEACHER", "老师未在本场次开放 Memory")
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" || runeLen(id) > maxMemoryIDChars {
		writeError(w, 404, "MEMORY_NOT_FOUND", "未找到该条 Memory")
		return
	}
	var in struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Content = strings.TrimSpace(in.Content)
	if in.Content == "" || runeLen(in.Content) > s.Config.MaxMemoryChars {
		writeError(w, 400, "INVALID_MEMORY", fmt.Sprintf("Memory 不能为空且不能超过 %d 个字", s.Config.MaxMemoryChars))
		return
	}
	if in.Status != "candidate" && in.Status != "confirmed" {
		writeError(w, 400, "INVALID_MEMORY_STATUS", "Memory 状态不正确")
		return
	}
	items, err := s.Store.Memories(r.Context(), p.Session.RunID, p.Session.StudentID, "")
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取 Memory 失败")
		return
	}
	tokens := memoryTokenEstimate(in.Content)
	found := false
	for _, item := range items {
		if item.ID == id {
			found = true
			continue
		}
		tokens += memoryTokenEstimate(item.Content)
	}
	if !found {
		writeError(w, 404, "MEMORY_NOT_FOUND", "未找到该条 Memory")
		return
	}
	if tokens > s.Config.MaxMemoryTokens {
		writeError(w, 400, "MEMORY_LIMIT_EXCEEDED", "Memory 总 token 估算已超过上限，请先缩短或删除其他条目")
		return
	}
	updated, err := s.Store.UpdateMemory(r.Context(), p.Session.RunID, p.Session.StudentID, id, in.Content, in.Status)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "MEMORY_NOT_FOUND", "未找到该条 Memory")
		return
	}
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "更新 Memory 失败")
		return
	}
	writeJSON(w, 200, updated)
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" || runeLen(id) > maxMemoryIDChars {
		writeError(w, 404, "MEMORY_NOT_FOUND", "未找到该条 Memory")
		return
	}
	err := s.Store.DeleteMemory(r.Context(), p.Session.RunID, p.Session.StudentID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "MEMORY_NOT_FOUND", "未找到该条 Memory")
		return
	}
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "删除 Memory 失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) clearMemory(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if err := s.Store.ClearMemories(r.Context(), p.Session.RunID, p.Session.StudentID); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "清空 Memory 失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func memoryTokenEstimate(value string) int {
	return (len([]byte(value)) + 2) / 3
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.withShutdown(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	if ctx.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "SERVER_SHUTTING_DOWN", "服务正在停止，请稍后重试")
		return
	}
	p := principalOf(r)
	s.controlMu.RLock()
	run, err := s.Store.Run(r.Context(), p.Session.RunID)
	locked := err != nil || run.Status != "active" || run.Locked
	s.controlMu.RUnlock()
	if locked {
		writeError(w, 423, "CLASS_LOCKED", "老师已暂停课堂操作")
		return
	}
	var in struct {
		ConversationID string `json:"conversation_id"`
		Message        string `json:"message"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Message = strings.TrimSpace(in.Message)
	in.ConversationID = strings.TrimSpace(in.ConversationID)
	if in.ConversationID == "" {
		writeError(w, 400, "CONVERSATION_REQUIRED", "请选择一个对话")
		return
	}
	if runeLen(in.ConversationID) > maxConversationChars {
		writeError(w, 400, "INVALID_CONVERSATION_ID", "对话标识不正确")
		return
	}
	if _, err = s.Store.Conversation(r.Context(), p.Session.RunID, p.Session.StudentID, in.ConversationID); err != nil {
		writeError(w, 404, "CONVERSATION_NOT_FOUND", "未找到该对话")
		return
	}
	if in.Message == "" {
		writeError(w, 400, "EMPTY_MESSAGE", "请输入任务")
		return
	}
	if runeLen(in.Message) > s.Config.MaxInputChars {
		writeError(w, 400, "TEXT_TOO_LONG", "消息超过长度限制")
		return
	}
	d, err := s.Store.Design(r.Context(), p.Session.RunID, p.Session.StudentID)
	if errors.Is(err, sql.ErrNoRows) {
		d = store.Design{RunID: p.Session.RunID, StudentID: p.Session.StudentID, Tools: []string{}, MaxTurns: s.Config.DefaultMaxTurns}
	} else if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取设计失败")
		return
	}
	policy, err := s.Store.RunPolicy(r.Context(), p.Session.RunID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取课堂能力失败")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "STREAM_UNSUPPORTED", "服务器不支持流式响应")
		return
	}
	enc := json.NewEncoder(w)
	emitted := false
	emit := func(ev agent.Event) error {
		emitted = true
		if err := enc.Encode(ev); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	turnID := newID("turn_")
	beforeModelCall := func(ctx context.Context) error {
		s.controlMu.RLock()
		defer s.controlMu.RUnlock()
		current, checkErr := s.Store.Run(ctx, p.Session.RunID)
		if checkErr != nil || current.Status != "active" || current.Locked {
			return agent.ErrClassLocked
		}
		return nil
	}
	toolsForCall := func(ctx context.Context) ([]string, error) {
		s.controlMu.RLock()
		defer s.controlMu.RUnlock()
		current, checkErr := s.Store.Run(ctx, p.Session.RunID)
		if checkErr != nil || current.Status != "active" || current.Locked {
			return nil, agent.ErrClassLocked
		}
		currentPolicy, checkErr := s.Store.RunPolicy(ctx, p.Session.RunID)
		if checkErr != nil {
			return nil, checkErr
		}
		return intersectStrings(d.Tools, currentPolicy.AllowedTools), nil
	}
	onMemoryUpdate := func(update agent.MemoryUpdate) {
		if update.Err != nil {
			s.Logger.Warn("memory extraction update", "run_id", p.Session.RunID, "student_id", p.Session.StudentID, "status", update.Status, "error", update.Err)
		}
		items := make([]memoryEventItem, 0, len(update.Items))
		for _, item := range update.Items {
			items = append(items, memoryEventItem{ID: item.ID, Content: item.Content, Status: item.Status})
		}
		target := p.Session.RunID + "\x00" + p.Session.StudentID
		s.studentMemoryHub.Publish(target, classroomEvent{Type: "memory_status", RunID: p.Session.RunID, MemoryStatus: update.Status, MemoryItems: items})
	}
	err = s.Agent.Run(r.Context(), agent.Request{RunID: p.Session.RunID, StudentID: p.Session.StudentID, ConversationID: in.ConversationID, TurnID: turnID, Input: in.Message, Design: d, MemoryMode: policy.MemoryMode, PolicyRevision: policy.Revision, BeforeModelCall: beforeModelCall, ToolsForCall: toolsForCall, OnMemoryUpdate: onMemoryUpdate}, emit)
	if err != nil {
		code, msg := agentError(err)
		if !emitted {
			writeError(w, statusForAgent(err), code, msg)
		} else {
			_ = emit(agent.Event{Type: "error", TurnID: turnID, Code: code, Message: msg})
		}
	}
	s.wallHub.Publish(struct{}{})
}

func intersectStrings(selected, allowed []string) []string {
	allow := make(map[string]bool, len(allowed))
	for _, value := range allowed {
		allow[value] = true
	}
	out := make([]string, 0, len(selected))
	for _, value := range selected {
		if allow[value] {
			out = append(out, value)
		}
	}
	return out
}

func agentError(err error) (string, string) {
	switch {
	case errors.Is(err, agent.ErrBusy):
		return "CHAT_IN_PROGRESS", "上一条回答仍在生成"
	case errors.Is(err, agent.ErrBudget):
		return "BUDGET_EXCEEDED", "本场次使用额度已用完，请联系老师"
	case errors.Is(err, agent.ErrTurnLimit):
		return "TURN_LIMIT_REACHED", "Agent 已达到最大步数，请调整任务后再试"
	case errors.Is(err, agent.ErrToolLimit):
		return "TOOL_LIMIT_REACHED", "Agent 已达到本轮工具调用上限，请缩小任务后再试"
	case errors.Is(err, agent.ErrClassLocked):
		return "CLASS_LOCKED", "老师已暂停课堂操作"
	case errors.Is(err, context.DeadlineExceeded):
		return "UPSTREAM_TIMEOUT", "模型响应超时，请稍后重试"
	case errors.Is(err, context.Canceled):
		return "REQUEST_CANCELED", "生成已停止"
	default:
		return "UPSTREAM_ERROR", "模型暂时不可用，请稍后重试"
	}
}
func statusForAgent(err error) int {
	if errors.Is(err, agent.ErrBusy) {
		return 409
	}
	if errors.Is(err, agent.ErrBudget) {
		return 429
	}
	if errors.Is(err, agent.ErrClassLocked) {
		return http.StatusLocked
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	return 502
}

func (s *Server) studentEvents(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id, ch := s.studentHub.Subscribe()
	defer s.studentHub.Unsubscribe(id)
	memoryID, memoryCh := s.studentMemoryHub.Subscribe(p.Session.RunID + "\x00" + p.Session.StudentID)
	defer s.studentMemoryHub.Unsubscribe(memoryID)
	run, err := s.Store.Run(r.Context(), p.Session.RunID)
	if err != nil {
		writeError(w, 401, "RUN_ENDED", "课堂场次已结束")
		return
	}
	prepareSSE(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	policy, err := s.Store.RunPolicy(r.Context(), run.ID)
	if err != nil {
		return
	}
	sendSSE(w, "classroom", classroomEvent{Type: "classroom", Locked: run.Locked, RunID: run.ID, MemoryMode: policy.MemoryMode, AllowedTools: policy.AllowedTools, Revision: policy.Revision})
	flusher.Flush()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.shutdown.Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			if ev.Target != "" && ev.Target != p.Session.StudentID {
				continue
			}
			if ev.Type == "session_invalidated" {
				sendSSE(w, "session_invalidated", map[string]string{"message": "该学号已在另一台设备登录"})
				flusher.Flush()
				return
			}
			sendSSE(w, "classroom", ev)
			flusher.Flush()
		case ev, open := <-memoryCh:
			if !open {
				return
			}
			sendSSE(w, "memory", ev)
			flusher.Flush()
		case <-tick.C:
			sess, e := s.Store.Session(r.Context(), p.Session.TokenHash)
			if e != nil || sess.RevokedAt != nil {
				sendSSE(w, "session_invalidated", map[string]string{"message": "登录已失效"})
				flusher.Flush()
				return
			}
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) wallEvents(w http.ResponseWriter, r *http.Request) {
	id, ch := s.wallHub.Subscribe()
	defer s.wallHub.Unsubscribe(id)
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	prepareSSE(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	sendWall := func() bool {
		current, e := s.Store.ActiveRun(r.Context())
		if e != nil {
			return false
		}
		items, e := s.Store.Wall(r.Context(), current.ID)
		if e != nil {
			return false
		}
		policy, e := s.Store.RunPolicy(r.Context(), current.ID)
		if e != nil {
			return false
		}
		sendSSE(w, "wall", map[string]any{"run": current, "policy": policy, "available_tools": s.Agent.Tools.Names(), "students": items, "screen_connections": s.screenHub.Count(), "server_time": time.Now().UTC()})
		flusher.Flush()
		return true
	}
	_ = run
	if !sendWall() {
		return
	}
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.shutdown.Done():
			return
		case _, open := <-ch:
			if !open || !sendWall() {
				return
			}
		case <-tick.C:
			if !sendWall() {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) teacherStudent(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" || runeLen(id) > maxStudentIDChars {
		writeError(w, 404, "STUDENT_NOT_FOUND", "未找到该学生")
		return
	}
	st, err := s.Store.Student(r.Context(), run.ID, id)
	if err != nil {
		writeError(w, 404, "STUDENT_NOT_FOUND", "未找到该学生")
		return
	}
	d, err := s.Store.Design(r.Context(), run.ID, id)
	if errors.Is(err, sql.ErrNoRows) {
		d = store.Design{Tools: []string{}, MaxTurns: s.Config.DefaultMaxTurns}
	} else if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取设计失败")
		return
	}
	msgs, err := s.Store.StudentMessages(r.Context(), run.ID, id, parseCursor(r.URL.Query().Get("cursor")), 500)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取对话失败")
		return
	}
	usage, _ := s.Store.Usage(r.Context(), run.ID, id)
	conversations, _ := s.Store.Conversations(r.Context(), run.ID, id, 100)
	writeJSON(w, 200, map[string]any{"student": st, "design": d, "conversations": conversations, "messages": msgs, "usage": usage})
}

func (s *Server) getTeacherPolicy(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	policy, err := s.Store.RunPolicy(r.Context(), run.ID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取课堂能力失败")
		return
	}
	writeJSON(w, 200, map[string]any{"policy": policy, "available_tools": s.Agent.Tools.Names()})
}

func (s *Server) setTeacherPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MemoryMode   string   `json:"memory_mode"`
		AllowedTools []string `json:"allowed_tools"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	clean, ok := s.validatePolicyInput(w, in.MemoryMode, in.AllowedTools)
	if !ok {
		return
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	policy, err := s.Store.SetRunPolicy(r.Context(), run.ID, store.RunPolicy{MemoryMode: in.MemoryMode, AllowedTools: clean})
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "更新课堂能力失败")
		return
	}
	s.studentHub.Publish(classroomEvent{Type: "classroom_policy", Locked: run.Locked, RunID: run.ID, MemoryMode: policy.MemoryMode, AllowedTools: policy.AllowedTools, Revision: policy.Revision})
	s.wallHub.Publish(struct{}{})
	s.Logger.Info("classroom policy changed", "run_id", run.ID, "memory_mode", policy.MemoryMode, "allowed_tools", policy.AllowedTools, "revision", policy.Revision)
	writeJSON(w, 200, policy)
}

func (s *Server) validatePolicyInput(w http.ResponseWriter, memoryMode string, allowedTools []string) ([]string, bool) {
	if memoryMode != store.MemoryModeDisabled && memoryMode != store.MemoryModeReviewRequired && memoryMode != store.MemoryModeAdaptive {
		writeError(w, 400, "INVALID_MEMORY_MODE", "Memory 模式不正确")
		return nil, false
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(allowedTools))
	for _, name := range allowedTools {
		if _, exists := s.Agent.Tools.Get(name); !exists {
			writeError(w, 400, "UNKNOWN_TOOL", "包含服务端未注册的 Tool")
			return nil, false
		}
		if !seen[name] {
			seen[name] = true
			clean = append(clean, name)
		}
	}
	return clean, true
}

func (s *Server) lock(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Locked bool `json:"locked"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	if err = s.Store.SetLocked(r.Context(), run.ID, in.Locked); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "切换锁定状态失败")
		return
	}
	policy, _ := s.Store.RunPolicy(r.Context(), run.ID)
	s.studentHub.Publish(classroomEvent{Type: "classroom", Locked: in.Locked, RunID: run.ID, MemoryMode: policy.MemoryMode, AllowedTools: policy.AllowedTools, Revision: policy.Revision})
	s.wallHub.Publish(struct{}{})
	s.Logger.Info("classroom lock changed", "run_id", run.ID, "locked", in.Locked)
	writeJSON(w, 200, map[string]bool{"locked": in.Locked})
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name         string   `json:"name"`
		MemoryMode   string   `json:"memory_mode"`
		AllowedTools []string `json:"allowed_tools"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || runeLen(in.Name) > 80 {
		writeError(w, 400, "INVALID_RUN_NAME", "场次名称不能为空且不能超过 80 个字")
		return
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	old, _ := s.Store.ActiveRun(r.Context())
	if in.MemoryMode == "" {
		oldPolicy, _ := s.Store.RunPolicy(r.Context(), old.ID)
		in.MemoryMode = oldPolicy.MemoryMode
		if in.AllowedTools == nil {
			in.AllowedTools = oldPolicy.AllowedTools
		}
	}
	clean, ok := s.validatePolicyInput(w, in.MemoryMode, in.AllowedTools)
	if !ok {
		return
	}
	created, err := s.Store.CreateRunWithPolicy(r.Context(), newID("run_"), in.Name, old.ID, store.RunPolicy{MemoryMode: in.MemoryMode, AllowedTools: clean})
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "新建场次失败")
		return
	}
	s.studentHub.Publish(classroomEvent{Type: "run_ended", RunID: old.ID})
	s.wallHub.Publish(struct{}{})
	s.screenMu.Lock()
	s.screenRevision++
	s.screen = screenState{Revision: s.screenRevision, Empty: true}
	s.screenAck = nil
	emptyScreen := s.screen
	s.screenMu.Unlock()
	s.screenHub.Publish(emptyScreen)
	s.Logger.Info("run created", "run_id", created.ID, "name", created.Name)
	writeJSON(w, 201, created)
}

func (s *Server) spotlight(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID     string `json:"id"`
		TurnID string `json:"turn_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.ID = strings.TrimSpace(in.ID)
	in.TurnID = strings.TrimSpace(in.TurnID)
	if in.ID == "" || runeLen(in.ID) > maxStudentIDChars || runeLen(in.TurnID) > maxTurnIDChars {
		writeError(w, 400, "INVALID_SPOTLIGHT", "投屏目标参数不正确")
		return
	}
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	st, err := s.Store.Student(r.Context(), run.ID, in.ID)
	if err != nil {
		writeError(w, 404, "STUDENT_NOT_FOUND", "未找到该学生")
		return
	}
	d, err := s.Store.Design(r.Context(), run.ID, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		d = store.Design{Tools: []string{}, MaxTurns: s.Config.DefaultMaxTurns}
	} else if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取设计失败")
		return
	}
	all, err := s.Store.StudentMessages(r.Context(), run.ID, in.ID, 0, 500)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取对话失败")
		return
	}
	turnID := in.TurnID
	if turnID == "" {
		for i := len(all) - 1; i >= 0; i-- {
			if all[i].Role == "assistant" && all[i].ToolCalls == "" {
				turnID = all[i].TurnID
				break
			}
		}
	}
	if turnID == "" {
		writeError(w, http.StatusConflict, "NO_COMPLETE_TURN", "该学生还没有可投屏的完整回答")
		return
	}
	var selectedFinalID int64
	hasUser, hasFinal := false, false
	for _, m := range all {
		if m.TurnID == turnID {
			hasUser = hasUser || m.Role == "user"
			if m.Role == "assistant" && m.ToolCalls == "" {
				hasFinal = true
				selectedFinalID = m.ID
			}
		}
	}
	if !hasUser || !hasFinal {
		writeError(w, http.StatusConflict, "NO_COMPLETE_TURN", "指定回合尚未完成或不存在")
		return
	}
	history := make([]store.Message, 0, len(all))
	for _, message := range all {
		if message.ID <= selectedFinalID {
			history = append(history, message)
		}
	}
	conversations, err := s.Store.Conversations(r.Context(), run.ID, in.ID, 200)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取对话列表失败")
		return
	}
	titles := make(map[string]string, len(conversations))
	for _, conversation := range conversations {
		titles[conversation.ID] = conversation.Title
	}
	state := screenState{SpotlightID: newID("screen_"), Name: st.Name, Persona: d.Persona, SkillMD: d.SkillMD, Tools: d.Tools, MaxTurns: d.MaxTurns, Messages: publicScreenMessages(history, titles, turnID), Empty: false}
	ack := make(chan struct{})
	s.screenMu.Lock()
	s.screenRevision++
	state.Revision = s.screenRevision
	s.screen = state
	s.screenAck = ack
	s.screenMu.Unlock()
	connections := s.screenHub.Count()
	s.screenHub.Publish(state)
	s.Logger.Info("spotlight published", "run_id", run.ID, "student_id", st.ID, "turn_id", turnID, "spotlight_id", state.SpotlightID, "screen_connections", connections)
	if connections == 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "delivered": false, "spotlight_id": state.SpotlightID, "screen_connections": 0})
		return
	}
	timer := time.NewTimer(s.spotlightAckTimeout)
	defer timer.Stop()
	select {
	case <-ack:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delivered": true, "spotlight_id": state.SpotlightID, "screen_connections": connections})
	case <-timer.C:
		s.Logger.Warn("spotlight acknowledgement timed out", "run_id", run.ID, "student_id", st.ID, "turn_id", turnID, "spotlight_id", state.SpotlightID, "screen_connections", connections)
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "delivered": false, "spotlight_id": state.SpotlightID, "screen_connections": connections})
	case <-r.Context().Done():
		return
	}
}

func (s *Server) ackScreen(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SpotlightID string `json:"spotlight_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.SpotlightID = strings.TrimSpace(in.SpotlightID)
	if in.SpotlightID == "" || runeLen(in.SpotlightID) > maxConversationChars {
		writeError(w, http.StatusBadRequest, "INVALID_SCREEN_ACK", "投屏确认参数不正确")
		return
	}
	s.screenMu.Lock()
	if s.screen.SpotlightID != in.SpotlightID || s.screenAck == nil {
		s.screenMu.Unlock()
		writeError(w, http.StatusConflict, "STALE_SCREEN_ACK", "投屏内容已更新")
		return
	}
	select {
	case <-s.screenAck:
	default:
		close(s.screenAck)
	}
	s.screenMu.Unlock()
	s.Logger.Info("spotlight acknowledged", "spotlight_id", in.SpotlightID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func publicScreenMessages(messages []store.Message, titles map[string]string, currentTurnID string) []screenMessage {
	out := make([]screenMessage, 0, len(messages))
	for _, message := range messages {
		content := message.Content
		if message.Role == "assistant" && message.ToolCalls != "" {
			var calls []agent.ToolCall
			_ = json.Unmarshal([]byte(message.ToolCalls), &calls)
			names := make([]string, 0, len(calls))
			for _, call := range calls {
				if call.Function.Name != "" {
					names = append(names, call.Function.Name)
				}
			}
			action := "发起工具调用"
			if len(names) > 0 {
				action = "调用工具：" + strings.Join(names, "、")
			}
			if strings.TrimSpace(content) == "" {
				content = action
			} else {
				content += "\n" + action
			}
		}
		title := titles[message.ConversationID]
		if title == "" {
			title = "历史对话"
		}
		out = append(out, screenMessage{Role: message.Role, Content: content, Conversation: title, Current: message.TurnID == currentTurnID})
	}
	return out
}

func (s *Server) screenEvents(w http.ResponseWriter, r *http.Request) {
	prepareSSE(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	id, ch := s.screenHub.Subscribe()
	s.Logger.Info("screen connected", "connection_id", id, "screen_connections", s.screenHub.Count())
	s.wallHub.Publish(struct{}{})
	defer func() {
		s.screenHub.Unsubscribe(id)
		s.Logger.Info("screen disconnected", "connection_id", id, "screen_connections", s.screenHub.Count())
		s.wallHub.Publish(struct{}{})
	}()
	s.screenMu.RLock()
	current := s.screen
	s.screenMu.RUnlock()
	sendSSE(w, "screen", current)
	flusher.Flush()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.shutdown.Done():
			return
		case state, open := <-ch:
			if !open {
				return
			}
			sendSSE(w, "screen", state)
			flusher.Flush()
		case <-tick.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func prepareSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}
func sendSSE(w http.ResponseWriter, event string, v any) {
	b, _ := json.Marshal(v)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}
