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

func (s *Server) saveDesign(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	run, err := s.Store.Run(r.Context(), p.Session.RunID)
	if err != nil || run.Locked {
		writeError(w, 423, "CLASS_LOCKED", "老师已暂停课堂操作")
		return
	}
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
		if name != "calculator" {
			writeError(w, 400, "UNKNOWN_TOOL", "包含未开放的装备")
			return
		}
		if !seen[name] {
			seen[name] = true
			clean = append(clean, name)
		}
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

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	conversationID := strings.TrimSpace(r.URL.Query().Get("conversation_id"))
	if conversationID == "" {
		writeError(w, 400, "CONVERSATION_REQUIRED", "请选择一个对话")
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

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.withShutdown(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	if ctx.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "SERVER_SHUTTING_DOWN", "服务正在停止，请稍后重试")
		return
	}
	p := principalOf(r)
	run, err := s.Store.Run(r.Context(), p.Session.RunID)
	if err != nil || run.Locked {
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
	err = s.Agent.Run(r.Context(), agent.Request{RunID: p.Session.RunID, StudentID: p.Session.StudentID, ConversationID: in.ConversationID, TurnID: turnID, Input: in.Message, Design: d}, emit)
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

func agentError(err error) (string, string) {
	switch {
	case errors.Is(err, agent.ErrBusy):
		return "CHAT_IN_PROGRESS", "上一条回答仍在生成"
	case errors.Is(err, agent.ErrBudget):
		return "BUDGET_EXCEEDED", "本场次使用额度已用完，请联系老师"
	case errors.Is(err, agent.ErrTurnLimit):
		return "TURN_LIMIT_REACHED", "Agent 已达到最大步数，请调整任务后再试"
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
	return 502
}

func (s *Server) studentEvents(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
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
	sendSSE(w, "classroom", classroomEvent{Type: "classroom", Locked: run.Locked, RunID: run.ID})
	flusher.Flush()
	id, ch := s.studentHub.Subscribe()
	defer s.studentHub.Unsubscribe(id)
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
		sendSSE(w, "wall", map[string]any{"run": current, "students": items, "server_time": time.Now().UTC()})
		flusher.Flush()
		return true
	}
	_ = run
	if !sendWall() {
		return
	}
	id, ch := s.wallHub.Subscribe()
	defer s.wallHub.Unsubscribe(id)
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

func (s *Server) lock(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Locked bool `json:"locked"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	if err = s.Store.SetLocked(r.Context(), run.ID, in.Locked); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "切换锁定状态失败")
		return
	}
	s.studentHub.Publish(classroomEvent{Type: "classroom", Locked: in.Locked, RunID: run.ID})
	s.wallHub.Publish(struct{}{})
	s.Logger.Info("classroom lock changed", "run_id", run.ID, "locked", in.Locked)
	writeJSON(w, 200, map[string]bool{"locked": in.Locked})
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || runeLen(in.Name) > 80 {
		writeError(w, 400, "INVALID_RUN_NAME", "场次名称不能为空且不能超过 80 个字")
		return
	}
	old, _ := s.Store.ActiveRun(r.Context())
	created, err := s.Store.CreateRun(r.Context(), newID("run_"), in.Name, old.ID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "新建场次失败")
		return
	}
	s.studentHub.Publish(classroomEvent{Type: "run_ended", RunID: old.ID})
	s.wallHub.Publish(struct{}{})
	s.screenMu.Lock()
	s.screen = screenState{Empty: true}
	s.screenMu.Unlock()
	s.screenHub.Publish(screenState{Empty: true})
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
	turnID := strings.TrimSpace(in.TurnID)
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
	var selected []store.Message
	hasUser, hasFinal := false, false
	for _, m := range all {
		if m.TurnID == turnID {
			selected = append(selected, m)
			hasUser = hasUser || m.Role == "user"
			hasFinal = hasFinal || (m.Role == "assistant" && m.ToolCalls == "")
		}
	}
	if !hasUser || !hasFinal {
		writeError(w, http.StatusConflict, "NO_COMPLETE_TURN", "指定回合尚未完成或不存在")
		return
	}
	state := screenState{Name: st.Name, Persona: d.Persona, SkillMD: d.SkillMD, Tools: d.Tools, MaxTurns: d.MaxTurns, Messages: selected, Empty: false}
	s.screenMu.Lock()
	s.screen = state
	s.screenMu.Unlock()
	s.screenHub.Publish(state)
	s.Logger.Info("spotlight changed", "run_id", run.ID, "student_id", st.ID, "turn_id", turnID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) screenEvents(w http.ResponseWriter, r *http.Request) {
	prepareSSE(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	id, ch := s.screenHub.Subscribe()
	defer s.screenHub.Unsubscribe(id)
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
