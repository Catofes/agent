package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"classroom-agent/internal/agent"
	"classroom-agent/internal/store"
)

type screenDemoStreamEvent struct {
	Type string          `json:"type"`
	Demo screenDemoState `json:"demo"`
}

func cloneScreenDemo(state screenDemoState) screenDemoState {
	clone := state
	clone.Messages = append([]screenMessage(nil), state.Messages...)
	clone.Skills = append([]store.Skill(nil), state.Skills...)
	clone.Design.Tools = append([]string(nil), state.Design.Tools...)
	return clone
}

func (s *Server) currentScreenDemo() screenDemoState {
	s.demoMu.RLock()
	defer s.demoMu.RUnlock()
	return cloneScreenDemo(s.screenDemo)
}

func (s *Server) updateScreenDemo(id string, update func(*screenDemoState)) (screenDemoState, bool) {
	s.demoMu.Lock()
	if !s.screenDemo.Active || s.screenDemo.ID != id {
		s.demoMu.Unlock()
		return screenDemoState{}, false
	}
	update(&s.screenDemo)
	s.screenDemoRevision++
	s.screenDemo.Revision = s.screenDemoRevision
	snapshot := cloneScreenDemo(s.screenDemo)
	s.demoMu.Unlock()
	s.screenDemoHub.PublishLatest(snapshot)
	return snapshot, true
}

func (s *Server) resetScreenDemo() screenDemoState {
	s.demoMu.Lock()
	if s.screenDemoCancel != nil {
		s.screenDemoCancel()
	}
	s.screenDemoCancel = nil
	s.screenDemoStarting = false
	s.screenDemoRevision++
	s.screenDemo = screenDemoState{Revision: s.screenDemoRevision, Messages: []screenMessage{}, answerIndex: -1}
	snapshot := cloneScreenDemo(s.screenDemo)
	s.demoMu.Unlock()
	s.screenDemoHub.PublishLatest(snapshot)
	return snapshot
}

func (s *Server) getScreenDemo(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.currentScreenDemo()
	writeJSON(w, http.StatusOK, map[string]any{"student_id": snapshot.StudentID, "demo": snapshot})
}

func (s *Server) startScreenDemo(w http.ResponseWriter, r *http.Request) {
	var in struct {
		StudentID string `json:"student_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.StudentID = strings.TrimSpace(in.StudentID)
	if in.StudentID == "" || runeLen(in.StudentID) > maxStudentIDChars {
		writeError(w, http.StatusBadRequest, "INVALID_STUDENT", "演示学生参数不正确")
		return
	}
	s.demoMu.Lock()
	if s.screenDemo.Running || s.screenDemoStarting {
		s.demoMu.Unlock()
		writeError(w, http.StatusConflict, "DEMO_IN_PROGRESS", "请先等待当前演示回答结束或停止测试")
		return
	}
	s.screenDemoStarting = true
	s.screenDemoRevision++
	startRevision := s.screenDemoRevision
	s.demoMu.Unlock()
	started := false
	defer func() {
		if started {
			return
		}
		s.demoMu.Lock()
		if s.screenDemoRevision == startRevision {
			s.screenDemoStarting = false
		}
		s.demoMu.Unlock()
	}()
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil || run.Locked {
		writeError(w, http.StatusLocked, "CLASS_LOCKED", "课堂当前不可开始演示")
		return
	}
	student, err := s.Store.Student(r.Context(), run.ID, in.StudentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "STUDENT_NOT_FOUND", "未找到该学生")
		return
	}
	design, err := s.Store.Design(r.Context(), run.ID, in.StudentID)
	if errors.Is(err, sql.ErrNoRows) {
		design = store.Design{RunID: run.ID, StudentID: in.StudentID, Tools: []string{}, MaxTurns: s.Config.DefaultMaxTurns}
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取学生配置失败")
		return
	}
	policy, err := s.Store.RunPolicy(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取课堂能力失败")
		return
	}
	var skills []store.Skill
	if policy.SkillsEnabled {
		skills, err = s.Store.Skills(r.Context(), run.ID, in.StudentID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取 Skill 列表失败")
			return
		}
	} else {
		design.SkillMD = ""
	}
	all, err := s.Store.StudentMessages(r.Context(), run.ID, in.StudentID, 0, 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取学生对话失败")
		return
	}
	conversations, err := s.Store.Conversations(r.Context(), run.ID, in.StudentID, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取对话列表失败")
		return
	}
	titles := make(map[string]string, len(conversations))
	for _, conversation := range conversations {
		titles[conversation.ID] = conversation.Title
	}
	s.screenMu.Lock()
	s.screenRevision++
	s.screen = screenState{
		Revision: s.screenRevision, StudentID: student.ID, Name: student.Name,
		Persona: design.Persona, SkillMD: formatSkillsForDisplay(skills), Tools: design.Tools,
		MaxTurns: design.MaxTurns, Messages: publicScreenMessages(all, titles, ""), Empty: false,
	}
	screenSnapshot := s.screen
	s.screenMu.Unlock()
	s.screenHub.Publish(screenSnapshot)
	if err = s.Store.DeleteDemoConversations(r.Context(), run.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "清理旧演示失败")
		return
	}
	conversation, err := s.Store.CreateConversation(r.Context(), store.Conversation{
		ID: newID("demo_"), RunID: run.ID, StudentID: in.StudentID,
		Kind: store.ConversationKindDemo, Title: "大屏演示",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建演示对话失败")
		return
	}
	s.demoMu.Lock()
	if !s.screenDemoStarting || s.screenDemoRevision != startRevision {
		s.demoMu.Unlock()
		writeError(w, http.StatusConflict, "DEMO_START_CANCELLED", "演示创建已取消")
		return
	}
	s.screenDemoRevision++
	s.screenDemoStarting = false
	s.screenDemo = screenDemoState{
		ID: newID("screen_demo_"), Revision: s.screenDemoRevision, Active: true,
		Name: student.Name, Status: "等待教师输入", Messages: []screenMessage{},
		RunID: run.ID, StudentID: in.StudentID, ConversationID: conversation.ID,
		Design: design, Skills: skills, answerIndex: -1,
	}
	snapshot := cloneScreenDemo(s.screenDemo)
	s.demoMu.Unlock()
	started = true
	s.screenDemoHub.PublishLatest(snapshot)
	s.Logger.Info("screen demo started", "run_id", run.ID, "student_id", in.StudentID, "demo_id", snapshot.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"student_id": snapshot.StudentID, "screen_connections": s.screenHub.Count(), "demo": snapshot})
}

func (s *Server) stopScreenDemo(w http.ResponseWriter, _ *http.Request) {
	state := s.resetScreenDemo()
	writeJSON(w, http.StatusOK, map[string]any{"demo": state})
}

func (s *Server) chatScreenDemo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.withShutdown(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	var in struct {
		DemoID  string `json:"demo_id"`
		Message string `json:"message"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.DemoID = strings.TrimSpace(in.DemoID)
	in.Message = strings.TrimSpace(in.Message)
	if in.DemoID == "" || runeLen(in.DemoID) > maxConversationChars {
		writeError(w, http.StatusBadRequest, "INVALID_DEMO", "演示会话参数不正确")
		return
	}
	if in.Message == "" {
		writeError(w, http.StatusBadRequest, "EMPTY_MESSAGE", "请输入测试问题")
		return
	}
	if runeLen(in.Message) > s.Config.MaxInputChars {
		writeError(w, http.StatusBadRequest, "TEXT_TOO_LONG", fmt.Sprintf("消息超过长度限制（最多 %d 字）", s.Config.MaxInputChars))
		return
	}
	s.demoMu.Lock()
	if s.screenDemoStarting || !s.screenDemo.Active || s.screenDemo.ID != in.DemoID {
		s.demoMu.Unlock()
		writeError(w, http.StatusNotFound, "DEMO_NOT_FOUND", "演示会话不存在或已结束")
		return
	}
	if s.screenDemo.Running {
		s.demoMu.Unlock()
		writeError(w, http.StatusConflict, "DEMO_IN_PROGRESS", "上一条演示回答仍在生成")
		return
	}
	demo := cloneScreenDemo(s.screenDemo)
	s.screenDemo.Running = true
	s.screenDemo.Status = "正在思考…"
	s.screenDemo.answerIndex = -1
	for index := range s.screenDemo.Messages {
		s.screenDemo.Messages[index].Current = false
	}
	s.screenDemo.Messages = append(s.screenDemo.Messages, screenMessage{Role: "user", Content: in.Message, Current: true})
	s.screenDemoRevision++
	s.screenDemo.Revision = s.screenDemoRevision
	s.screenDemoCancel = cancel
	initial := cloneScreenDemo(s.screenDemo)
	s.demoMu.Unlock()
	s.screenDemoHub.PublishLatest(initial)
	defer func() {
		s.demoMu.Lock()
		needsUpdate := s.screenDemo.Active && s.screenDemo.ID == in.DemoID && s.screenDemo.Running
		s.demoMu.Unlock()
		if needsUpdate {
			s.finishScreenDemoError(in.DemoID, "生成已停止")
		}
		s.demoMu.Lock()
		if s.screenDemo.ID == in.DemoID {
			s.screenDemoCancel = nil
		}
		s.demoMu.Unlock()
	}()

	run, err := s.Store.Run(ctx, demo.RunID)
	if err != nil || run.Status != "active" || run.Locked {
		s.finishScreenDemoError(in.DemoID, "课堂已锁定或结束")
		writeError(w, http.StatusLocked, "CLASS_LOCKED", "课堂已锁定或结束")
		return
	}
	if _, err = s.Store.DemoConversation(ctx, demo.RunID, demo.StudentID, demo.ConversationID); err != nil {
		s.finishScreenDemoError(in.DemoID, "演示会话已失效")
		writeError(w, http.StatusNotFound, "DEMO_NOT_FOUND", "演示会话已失效")
		return
	}
	policy, err := s.Store.RunPolicy(ctx, demo.RunID)
	if err != nil {
		s.finishScreenDemoError(in.DemoID, "读取课堂能力失败")
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取课堂能力失败")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.finishScreenDemoError(in.DemoID, "服务器不支持流式响应")
		writeError(w, http.StatusInternalServerError, "STREAM_UNSUPPORTED", "服务器不支持流式响应")
		return
	}
	enc := json.NewEncoder(w)
	emitState := func(state screenDemoState) error {
		if err := enc.Encode(screenDemoStreamEvent{Type: "demo_state", Demo: state}); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if err = emitState(initial); err != nil {
		s.finishScreenDemoError(in.DemoID, "教师端连接已断开")
		return
	}
	beforeModelCall := func(callCtx context.Context) error {
		current, checkErr := s.Store.Run(callCtx, demo.RunID)
		if checkErr != nil || current.Status != "active" || current.Locked {
			return agent.ErrClassLocked
		}
		s.demoMu.RLock()
		active := s.screenDemo.Active && s.screenDemo.ID == in.DemoID
		s.demoMu.RUnlock()
		if !active {
			return context.Canceled
		}
		return nil
	}
	toolsForCall := func(callCtx context.Context) ([]string, error) {
		if err := beforeModelCall(callCtx); err != nil {
			return nil, err
		}
		currentPolicy, checkErr := s.Store.RunPolicy(callCtx, demo.RunID)
		if checkErr != nil {
			return nil, checkErr
		}
		return intersectStrings(demo.Design.Tools, currentPolicy.AllowedTools), nil
	}
	skillsAllowedForCall := func(callCtx context.Context) (bool, error) {
		if err := beforeModelCall(callCtx); err != nil {
			return false, err
		}
		currentPolicy, checkErr := s.Store.RunPolicy(callCtx, demo.RunID)
		if checkErr != nil {
			return false, checkErr
		}
		return currentPolicy.SkillsEnabled, nil
	}
	emit := func(event agent.Event) error {
		// The public demo deliberately exposes only the fact that reasoning is in
		// progress. The initial state already carries that status, and Skill or
		// Memory receipts are private context rather than display content.
		if event.Type == "reasoning_delta" || event.Type == "skill_loaded" || event.Type == "memory_recalled" {
			return nil
		}
		state, changed := s.applyScreenDemoEvent(in.DemoID, event)
		if !changed {
			return context.Canceled
		}
		return emitState(state)
	}
	turnID := newID("turn_")
	err = s.Agent.Run(ctx, agent.Request{
		RunID: demo.RunID, StudentID: demo.StudentID, ConversationID: demo.ConversationID,
		TurnID: turnID, Input: in.Message, Design: demo.Design, Skills: demo.Skills,
		MemoryMode: store.MemoryModeDisabled, DisableMemory: true,
		PolicyRevision: policy.Revision, ModelProvider: policy.ModelProvider, SearchProvider: policy.SearchProvider, DeepSeekSearchChannel: policy.DeepSeekSearchChannel,
		BeforeModelCall: beforeModelCall, ToolsForCall: toolsForCall, SkillsAllowedForCall: skillsAllowedForCall,
	}, emit)
	if err != nil && !errors.Is(err, context.Canceled) {
		code, message := agentError(err)
		state := s.finishScreenDemoError(in.DemoID, message)
		_ = enc.Encode(screenDemoStreamEvent{Type: "demo_error", Demo: state})
		flusher.Flush()
		s.Logger.Warn("screen demo chat failed", "run_id", demo.RunID, "student_id", demo.StudentID, "demo_id", in.DemoID, "code", code, "error", err)
	}
	s.wallHub.Publish(struct{}{})
}

func (s *Server) applyScreenDemoEvent(id string, event agent.Event) (screenDemoState, bool) {
	return s.updateScreenDemo(id, func(state *screenDemoState) {
		switch event.Type {
		case "turn_start":
			state.Status = "正在思考…"
		case "reasoning_delta":
			state.Status = "正在思考…"
		case "text_delta":
			state.Status = "正在回答…"
			if state.answerIndex < 0 || state.answerIndex >= len(state.Messages) {
				state.Messages = append(state.Messages, screenMessage{Role: "assistant", Current: true})
				state.answerIndex = len(state.Messages) - 1
			}
			state.Messages[state.answerIndex].Content += event.Delta
		case "tool_start":
			state.answerIndex = -1
			state.Status = "正在调用工具…"
			state.Messages = append(state.Messages, screenMessage{Role: "assistant", Content: fmt.Sprintf("第 %d 步：%s｜%s", event.Step, event.Tool, event.Summary), Current: true, Process: true})
		case "tool_result":
			state.Status = "工具执行完成，继续思考…"
			state.Messages = append(state.Messages, screenMessage{Role: "tool", Content: event.Summary, Current: true, Process: true})
		case "turn_end":
			state.Running = false
			state.Status = "回答完成"
			state.answerIndex = -1
		case "error":
			state.Running = false
			state.Status = event.Message
			state.answerIndex = -1
		}
	})
}

func (s *Server) finishScreenDemoError(id, message string) screenDemoState {
	state, ok := s.updateScreenDemo(id, func(state *screenDemoState) {
		state.Running = false
		state.Status = message
		state.answerIndex = -1
	})
	if !ok {
		return s.currentScreenDemo()
	}
	return state
}
