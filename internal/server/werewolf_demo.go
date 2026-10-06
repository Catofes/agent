package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"classroom-agent/internal/store"
)

// These two classroom accounts can prepare and present the Werewolf activity.
// Their student sessions retain access to their own Agent design; this does not
// grant access to unrelated teacher administration endpoints.
func isWerewolfManager(id string) bool {
	return strings.EqualFold(id, "A01") || strings.EqualFold(id, "A02")
}

func canManageWerewolf(session store.Session) bool {
	return session.ManagerAuthorized && isWerewolfManager(session.StudentID)
}

func (s *Server) requireWerewolfTeacher(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r)
		if !p.Session.IsTeacher && !canManageWerewolf(p.Session) {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "仅教师及 A01、A02 可管理狼人杀")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) werewolfManager(w http.ResponseWriter, r *http.Request) (principal, bool) {
	p := principalOf(r)
	if p.Session.IsTeacher || !canManageWerewolf(p.Session) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "仅 A01、A02 可管理狼人杀演示")
		return principal{}, false
	}
	return p, true
}

func (s *Server) getWerewolfControl(w http.ResponseWriter, r *http.Request) {
	p, ok := s.werewolfManager(w, r)
	if !ok {
		return
	}
	policy, err := s.Store.RunPolicy(r.Context(), p.Session.RunID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取狼人杀开关失败")
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": policy.WerewolfEnabled})
}

func (s *Server) setWerewolfControl(w http.ResponseWriter, r *http.Request) {
	p, ok := s.werewolfManager(w, r)
	if !ok {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeError(w, 400, "INVALID_CONTROL", "请选择开启或关闭")
		return
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil || run.ID != p.Session.RunID {
		writeError(w, 409, "RUN_ENDED", "当前课堂已结束")
		return
	}
	policy, err := s.Store.RunPolicy(r.Context(), run.ID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取课堂设置失败")
		return
	}
	wasEnabled := policy.WerewolfEnabled
	policy.WerewolfEnabled = *in.Enabled
	policy, err = s.Store.SetRunPolicy(r.Context(), run.ID, policy)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "更新狼人杀开关失败")
		return
	}
	if wasEnabled && !policy.WerewolfEnabled {
		s.cancelCurrentTournament(run.ID, "stopped", "教师关闭了狼人杀环节")
		s.cancelCurrentWerewolf(run.ID, "stopped", "教师关闭了狼人杀环节")
	}
	s.studentHub.Publish(classroomEvent{Type: "classroom_policy", Locked: run.Locked, RunID: run.ID,
		MemoryMode: policy.MemoryMode, SkillsEnabled: policy.SkillsEnabled, WerewolfEnabled: policy.WerewolfEnabled,
		PresetSkills: policy.PresetSkills, AllowedTools: policy.AllowedTools, Revision: policy.Revision})
	s.wallHub.Publish(struct{}{})
	s.Logger.Info("werewolf classroom visibility changed", "account", p.Session.StudentID, "enabled", policy.WerewolfEnabled)
	writeJSON(w, 200, map[string]any{"enabled": policy.WerewolfEnabled})
}

func (s *Server) getWerewolfDemo(w http.ResponseWriter, r *http.Request) {
	p, ok := s.werewolfManager(w, r)
	if !ok {
		return
	}
	game, err := s.Store.LatestWerewolfDemo(r.Context(), p.Session.RunID, p.Session.StudentID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 200, map[string]any{"game": nil})
		return
	}
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取演示对局失败")
		return
	}
	state, err := decodeWerewolfState(game)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "演示数据损坏")
		return
	}
	players := make([]map[string]any, 0, len(state.Players))
	for _, player := range state.Players {
		players = append(players, map[string]any{"seat": player.Seat, "name": player.Name, "role": player.Role,
			"alive": player.Alive, "mine": player.StudentID == p.Session.StudentID, "system": player.System})
	}
	writeJSON(w, 200, map[string]any{"game": map[string]any{"id": game.ID, "status": game.Status,
		"round": state.Round, "phase": state.Phase, "winner": game.Winner, "error": game.Error, "players": players}})
}

func (s *Server) startWerewolfDemo(w http.ResponseWriter, r *http.Request) {
	p, ok := s.werewolfManager(w, r)
	if !ok {
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Role != "wolf" && in.Role != "villager" && in.Role != "seer" && in.Role != "witch" {
		writeError(w, 400, "INVALID_ROLE", "请选择演示身份")
		return
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil || run.ID != p.Session.RunID {
		writeError(w, 409, "RUN_ENDED", "当前课堂已结束")
		return
	}
	if s.WerewolfClient == nil || strings.Contains(s.Config.DeepSeekAPIKey, "placeholder") {
		writeError(w, 409, "MODEL_UNAVAILABLE", "请先配置真实的 DeepSeek API Key")
		return
	}
	if tournament, e := s.Store.LatestWerewolfTournament(r.Context(), run.ID); e == nil && tournament.Status == "running" {
		writeError(w, 409, "TOURNAMENT_RUNNING", "班级比赛进行中，请结束后再演示")
		return
	}
	policy, err := s.Store.RunPolicy(r.Context(), run.ID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取课堂设置失败")
		return
	}
	ids := []string{p.Session.StudentID, "bot:demo:2", "bot:demo:3", "bot:demo:4", "bot:demo:5", "bot:demo:6"}
	state, err := s.newWerewolfState(r.Context(), run.ID, policy, ids, true)
	if err != nil {
		writeError(w, 400, "INVALID_DEMO", err.Error())
		return
	}
	for i := range state.Players {
		if state.Players[i].Role == in.Role {
			state.Players[0].Role, state.Players[i].Role = state.Players[i].Role, state.Players[0].Role
			break
		}
	}
	for i := 1; i < len(state.Players); i++ {
		state.Players[i].Name = "系统 Agent " + strconv.Itoa(i)
	}
	raw, _ := json.Marshal(state)
	game, err := s.Store.CreateWerewolfDemo(r.Context(), newID("demo_"), run.ID, p.Session.StudentID, string(raw))
	if err != nil {
		writeError(w, 409, "GAME_IN_PROGRESS", "已有对局在进行，请稍后再开始演示")
		return
	}
	ctx, cancel := context.WithCancel(s.shutdown)
	s.werewolfMu.Lock()
	s.werewolfCancel = cancel
	s.werewolfGameID = game.ID
	s.werewolfMu.Unlock()
	go s.runWerewolfGame(ctx, game, state)
	s.getWerewolfDemo(w, r)
}

func (s *Server) stopWerewolfDemo(w http.ResponseWriter, r *http.Request) {
	p, ok := s.werewolfManager(w, r)
	if !ok {
		return
	}
	game, err := s.Store.LatestWerewolfDemo(r.Context(), p.Session.RunID, p.Session.StudentID)
	if err != nil || game.Status != "running" {
		writeError(w, 404, "GAME_NOT_RUNNING", "演示对局未在进行")
		return
	}
	s.werewolfMu.Lock()
	if s.werewolfGameID == game.ID && s.werewolfCancel != nil {
		s.werewolfCancel()
	}
	s.werewolfMu.Unlock()
	_ = s.Store.UpdateWerewolfGame(context.Background(), game.RunID, game.ID, "stopped", game.StateJSON, "", "演示者已停止对局")
	s.getWerewolfDemo(w, r)
}

func (s *Server) getWerewolfDemoEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := s.werewolfManager(w, r)
	if !ok {
		return
	}
	game, err := s.Store.LatestWerewolfDemo(r.Context(), p.Session.RunID, p.Session.StudentID)
	if err != nil {
		writeError(w, 404, "GAME_NOT_FOUND", "尚无演示对局")
		return
	}
	state, err := decodeWerewolfState(game)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "演示数据损坏")
		return
	}
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil || after < 0 {
		after = 0
	}
	events, err := s.Store.WerewolfEvents(r.Context(), game.RunID, game.ID, after)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取演示流程失败")
		return
	}
	items := make([]map[string]any, 0, len(events))
	next := after
	for _, event := range events {
		next = event.ID
		items = append(items, werewolfEventItem(event, state, true))
	}
	writeJSON(w, 200, map[string]any{"events": items, "next": next})
}
