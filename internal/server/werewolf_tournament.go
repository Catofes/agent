package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"classroom-agent/internal/store"
)

const tournamentGamesPerStudent = 20

type werewolfTournamentPlan struct {
	Matches [][]string                `json:"matches"`
	Agents  map[string]werewolfPlayer `json:"agents"`
}

func buildWerewolfSchedule(students []store.TournamentPlayer, gamesEach int, allowSystem bool) ([][]string, error) {
	if len(students) < 6 {
		return nil, errors.New("至少需要 6 名名单中的学生才能开赛")
	}
	remaining := map[string]int{}
	ids := make([]string, 0, len(students))
	for _, student := range students {
		if strings.TrimSpace(student.ID) == "" || strings.HasPrefix(student.ID, "bot:") {
			return nil, errors.New("参赛名单包含无效学号")
		}
		remaining[student.ID] = gamesEach
		ids = append(ids, student.ID)
	}
	totalSlots := len(students) * gamesEach
	if totalSlots%6 != 0 && !allowSystem {
		return nil, fmt.Errorf("%d 名学生每人 %d 局无法组成完整六人局，请允许系统补位", len(students), gamesEach)
	}
	matches := make([][]string, 0, (totalSlots+5)/6)
	for totalSlots > 0 {
		if err := cryptoShuffle(ids); err != nil {
			return nil, err
		}
		sort.SliceStable(ids, func(i, j int) bool { return remaining[ids[i]] > remaining[ids[j]] })
		match := make([]string, 0, 6)
		for _, id := range ids {
			if remaining[id] == 0 || len(match) == 6 {
				break
			}
			match = append(match, id)
			remaining[id]--
			totalSlots--
		}
		if len(match) == 0 {
			return nil, errors.New("比赛排赛失败")
		}
		for len(match) < 6 {
			match = append(match, fmt.Sprintf("bot:%d:%d", len(matches)+1, len(match)+1))
		}
		if err := cryptoShuffle(match); err != nil {
			return nil, err
		}
		matches = append(matches, match)
	}
	return matches, nil
}

func cryptoShuffle[T any](items []T) error {
	for i := len(items) - 1; i > 0; i-- {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return err
		}
		j := int(index.Int64())
		items[i], items[j] = items[j], items[i]
	}
	return nil
}

func (s *Server) studentWerewolfTournament(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	s.werewolfTournament(w, r, p.Session.RunID, false)
}

func (s *Server) teacherWerewolfTournament(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	s.werewolfTournament(w, r, run.ID, true)
}

func (s *Server) werewolfTournament(w http.ResponseWriter, r *http.Request, runID string, teacher bool) {
	policy, err := s.Store.RunPolicy(r.Context(), runID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取课堂设置失败")
		return
	}
	if !teacher && !policy.WerewolfEnabled {
		writeJSON(w, 200, map[string]any{"enabled": false, "tournament": nil, "standings": []any{}})
		return
	}
	tournament, err := s.Store.LatestWerewolfTournament(r.Context(), runID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 200, map[string]any{"enabled": policy.WerewolfEnabled, "tournament": nil, "standings": []any{}})
		return
	}
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取比赛失败")
		return
	}
	standings, err := s.Store.WerewolfTournamentStandings(r.Context(), runID, tournament.ID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取积分榜失败")
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": policy.WerewolfEnabled, "tournament": tournament, "standings": standings})
}

func (s *Server) teacherWerewolfTournamentMatches(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	id := chi.URLParam(r, "id")
	tournament, err := s.Store.WerewolfTournament(r.Context(), run.ID, id)
	if err != nil {
		writeError(w, 404, "TOURNAMENT_NOT_FOUND", "未找到这场比赛")
		return
	}
	var plan werewolfTournamentPlan
	if err = json.Unmarshal([]byte(tournament.ScheduleJSON), &plan); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "比赛计划损坏")
		return
	}
	matches, err := s.Store.WerewolfTournamentMatches(r.Context(), run.ID, id)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取对局记录失败")
		return
	}
	for i := range matches {
		for _, studentID := range plan.Matches[matches[i].Index] {
			if player, ok := plan.Agents[studentID]; ok {
				matches[i].Players = append(matches[i].Players, store.TournamentPlayer{ID: studentID, Name: player.Name})
			} else {
				matches[i].Players = append(matches[i].Players, store.TournamentPlayer{ID: studentID, Name: "系统 Agent"})
			}
		}
	}
	writeJSON(w, 200, map[string]any{"matches": matches})
}

func (s *Server) teacherWerewolfTournamentMatch(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	id := chi.URLParam(r, "id")
	tournament, err := s.Store.WerewolfTournament(r.Context(), run.ID, id)
	if err != nil {
		writeError(w, 404, "TOURNAMENT_NOT_FOUND", "未找到这场比赛")
		return
	}
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil || index < 0 || index >= tournament.TotalMatches {
		writeError(w, 400, "INVALID_MATCH", "场次编号无效")
		return
	}
	gameID, err := s.Store.WerewolfTournamentMatchGame(r.Context(), id, index)
	if err != nil {
		writeError(w, 404, "MATCH_NOT_FOUND", "本场尚未开局")
		return
	}
	game, err := s.Store.WerewolfGame(r.Context(), run.ID, gameID)
	if err != nil || game.TournamentID != id {
		writeError(w, 404, "MATCH_NOT_FOUND", "未找到本场记录")
		return
	}
	state, err := decodeWerewolfState(game)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "对局数据损坏")
		return
	}
	events, err := s.Store.WerewolfEvents(r.Context(), run.ID, gameID, 0)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取交流记录失败")
		return
	}
	items := make([]map[string]any, 0, len(events))
	for _, event := range events {
		items = append(items, werewolfEventItem(event, state, true))
	}
	players := make([]map[string]any, 0, len(state.Players))
	for _, player := range state.Players {
		players = append(players, map[string]any{"seat": player.Seat, "name": player.Name, "role": player.Role, "alive": player.Alive, "system": player.System})
	}
	writeJSON(w, 200, map[string]any{"game": map[string]any{"id": game.ID, "status": game.Status, "round": state.Round,
		"phase": state.Phase, "winner": game.Winner, "error": game.Error, "players": players}, "events": items})
}

func (s *Server) startWerewolfTournament(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AllowSystem bool `json:"allow_system"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if !s.tournamentWorkerIdle(w) {
		return
	}
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	policy, err := s.Store.RunPolicy(r.Context(), run.ID)
	if err != nil || !policy.WerewolfEnabled {
		writeError(w, 403, "WEREWOLF_DISABLED", "请先开启狼人杀环节")
		return
	}
	if s.WerewolfClient == nil || strings.Contains(s.Config.DeepSeekAPIKey, "placeholder") {
		writeError(w, 409, "MODEL_UNAVAILABLE", "请先配置真实的 DeepSeek API Key")
		return
	}
	if _, gameErr := s.Store.RunningWerewolfInteractiveGame(r.Context(), run.ID); gameErr == nil {
		writeError(w, 409, "GAME_IN_PROGRESS", "请先结束当前演示或单局对战")
		return
	} else if !errors.Is(gameErr, sql.ErrNoRows) {
		writeError(w, 500, "DATABASE_ERROR", "读取当前对局失败")
		return
	}
	students, err := s.Store.RosterStudents(r.Context(), run.ID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "读取参赛名单失败")
		return
	}
	matches, err := buildWerewolfSchedule(students, tournamentGamesPerStudent, in.AllowSystem)
	if err != nil {
		writeError(w, 400, "INVALID_TOURNAMENT", err.Error())
		return
	}
	plan := werewolfTournamentPlan{Matches: matches, Agents: map[string]werewolfPlayer{}}
	for _, student := range students {
		player, snapshotErr := s.werewolfPlayerSnapshot(r.Context(), run.ID, student.ID)
		if snapshotErr != nil {
			writeError(w, 500, "DATABASE_ERROR", "读取学生 Agent 设计失败")
			return
		}
		plan.Agents[student.ID] = player
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "保存比赛计划失败")
		return
	}
	tournament := store.WerewolfTournament{ID: newID("tournament_"), RunID: run.ID,
		ScheduleJSON: string(raw), TotalMatches: len(matches), TargetGames: tournamentGamesPerStudent}
	if err = s.Store.CreateWerewolfTournament(r.Context(), tournament, students); err != nil {
		writeError(w, 409, "TOURNAMENT_RUNNING", "已有比赛正在进行")
		return
	}
	s.startTournamentWorker(tournament)
	s.werewolfTournament(w, r, run.ID, true)
}

func (s *Server) resumeWerewolfTournament(w http.ResponseWriter, r *http.Request) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if !s.tournamentWorkerIdle(w) {
		return
	}
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	policy, err := s.Store.RunPolicy(r.Context(), run.ID)
	if err != nil || !policy.WerewolfEnabled {
		writeError(w, 403, "WEREWOLF_DISABLED", "请先开启狼人杀环节")
		return
	}
	if s.WerewolfClient == nil || strings.Contains(s.Config.DeepSeekAPIKey, "placeholder") {
		writeError(w, 409, "MODEL_UNAVAILABLE", "请先配置真实的 DeepSeek API Key")
		return
	}
	id := chi.URLParam(r, "id")
	tournament, err := s.Store.WerewolfTournament(r.Context(), run.ID, id)
	if err != nil || tournament.NextMatch >= tournament.TotalMatches || tournament.Status == "complete" || tournament.Status == "running" {
		writeError(w, 409, "CANNOT_RESUME", "这场比赛无法继续")
		return
	}
	if _, gameErr := s.Store.RunningWerewolfInteractiveGame(r.Context(), run.ID); gameErr == nil {
		writeError(w, 409, "GAME_IN_PROGRESS", "请先结束当前演示或单局对战")
		return
	} else if !errors.Is(gameErr, sql.ErrNoRows) {
		writeError(w, 500, "DATABASE_ERROR", "读取当前对局失败")
		return
	}
	if err = s.Store.SetWerewolfTournamentStatus(r.Context(), run.ID, id, tournament.Status, "running", ""); err != nil {
		writeError(w, 409, "TOURNAMENT_RUNNING", "无法继续比赛")
		return
	}
	tournament.Status = "running"
	s.startTournamentWorker(tournament)
	s.werewolfTournament(w, r, run.ID, true)
}

func (s *Server) stopWerewolfTournament(w http.ResponseWriter, r *http.Request) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	id := chi.URLParam(r, "id")
	tournament, err := s.Store.WerewolfTournament(r.Context(), run.ID, id)
	if err != nil || tournament.Status != "running" {
		writeError(w, 404, "TOURNAMENT_NOT_RUNNING", "比赛未在进行")
		return
	}
	if err := s.cancelCurrentTournament(run.ID, "stopped", "教师暂停了比赛"); err != nil {
		writeError(w, 503, "DATABASE_BUSY", "比赛暂停未能保存，请重试")
		return
	}
	s.werewolfTournament(w, r, run.ID, true)
}

func (s *Server) clearWerewolfTournaments(w http.ResponseWriter, r *http.Request) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有活动场次")
		return
	}
	if err = s.Store.ClearWerewolfTournaments(r.Context(), run.ID); errors.Is(err, store.ErrWerewolfTournamentRunning) {
		writeError(w, 409, "TOURNAMENT_RUNNING", "请先暂停比赛，再清空记录")
		return
	} else if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "清空比赛记录失败")
		return
	}
	s.werewolfTournament(w, r, run.ID, true)
}

func (s *Server) startTournamentWorker(tournament store.WerewolfTournament) {
	ctx, cancel := context.WithCancel(s.shutdown)
	s.tournamentMu.Lock()
	s.tournamentWorkerSerial++
	serial := s.tournamentWorkerSerial
	s.tournamentCancel = cancel
	s.tournamentID = tournament.ID
	s.tournamentMu.Unlock()
	go s.runWerewolfTournament(ctx, tournament, serial)
}

func (s *Server) tournamentWorkerIdle(w http.ResponseWriter) bool {
	s.tournamentMu.Lock()
	defer s.tournamentMu.Unlock()
	if s.tournamentCancel != nil {
		writeError(w, 409, "TOURNAMENT_BUSY", "比赛任务尚未退出，请稍后重试")
		return false
	}
	return true
}

func (s *Server) cancelCurrentTournament(runID, status, message string) error {
	tournament, err := s.Store.LatestWerewolfTournament(context.Background(), runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if tournament.Status != "running" {
		return nil
	}
	s.tournamentMu.Lock()
	if s.tournamentID == tournament.ID && s.tournamentCancel != nil {
		s.tournamentCancel()
	}
	s.tournamentMu.Unlock()
	if err := retryTournamentWrite(func() error {
		return s.Store.SetWerewolfTournamentStatus(context.Background(), runID, tournament.ID, "running", status, message)
	}); err != nil {
		return err
	}
	return retryTournamentWrite(func() error {
		return s.Store.StopWerewolfTournamentGames(context.Background(), runID, tournament.ID, message)
	})
}

func retryTournamentWrite(write func() error) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		err = write()
		if err == nil || !strings.Contains(err.Error(), "SQLITE_BUSY") && !strings.Contains(err.Error(), "database is locked") {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	return err
}

func (s *Server) clearTournamentWorker(id string, serial uint64) {
	s.tournamentMu.Lock()
	defer s.tournamentMu.Unlock()
	if s.tournamentID == id && s.tournamentWorkerSerial == serial {
		s.tournamentCancel = nil
		s.tournamentID = ""
	}
}

func (s *Server) runWerewolfTournament(ctx context.Context, tournament store.WerewolfTournament, serial uint64) {
	defer s.clearTournamentWorker(tournament.ID, serial)
	var plan werewolfTournamentPlan
	if err := json.Unmarshal([]byte(tournament.ScheduleJSON), &plan); err != nil {
		s.failTournament(tournament, err)
		return
	}
	completed, err := s.Store.WerewolfTournamentScoredMatches(ctx, tournament.ID)
	if err != nil {
		s.failTournament(tournament, err)
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int, len(plan.Matches))
	for index := range plan.Matches {
		if !completed[index] {
			jobs <- index
		}
	}
	close(jobs)
	workerCount := tournamentWorkerCount(len(jobs))
	var workers sync.WaitGroup
	firstError := make(chan error, 1)
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if workerCtx.Err() != nil {
					return
				}
				if err := s.runWerewolfTournamentMatch(workerCtx, tournament, plan, index); err != nil {
					if workerCtx.Err() == nil {
						select {
						case firstError <- fmt.Errorf("第 %d 局：%w", index+1, err):
						default:
						}
					}
					cancel()
					return
				}
			}
		}()
	}
	workers.Wait()
	select {
	case err := <-firstError:
		s.failTournament(tournament, err)
		_ = s.Store.StopWerewolfTournamentGames(context.Background(), tournament.RunID, tournament.ID, "比赛中断；可继续未完成场次")
		return
	default:
	}
	if ctx.Err() != nil {
		return
	}
	current, err := s.Store.WerewolfTournament(context.Background(), tournament.RunID, tournament.ID)
	if err == nil && current.Status == "running" && current.NextMatch == current.TotalMatches {
		_ = s.Store.SetWerewolfTournamentStatus(context.Background(), tournament.RunID, tournament.ID, "running", "complete", "")
	}
}

func tournamentWorkerCount(pending int) int {
	count := 32
	if configured, err := strconv.Atoi(os.Getenv("WEREWOLF_TOURNAMENT_WORKERS")); err == nil && configured > 0 {
		count = configured
	}
	if count > 64 {
		count = 64
	}
	if count > pending {
		count = pending
	}
	return count
}

func (s *Server) runWerewolfTournamentMatch(ctx context.Context, tournament store.WerewolfTournament, plan werewolfTournamentPlan, index int) error {
	if err := s.checkTournamentActive(ctx, tournament); err != nil {
		return err
	}
	gameID, err := s.Store.WerewolfTournamentMatchGame(ctx, tournament.ID, index)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var game store.WerewolfGame
	if gameID != "" {
		game, err = s.Store.WerewolfGame(ctx, tournament.RunID, gameID)
		if err != nil {
			return err
		}
	}
	if game.Status != "complete" {
		state, stateErr := stateForTournamentMatch(plan, plan.Matches[index])
		if stateErr != nil {
			return stateErr
		}
		raw, _ := json.Marshal(state)
		game, err = s.Store.CreateWerewolfTournamentGame(ctx, newID("wolf_"), tournament.RunID, tournament.ID, string(raw))
		if err != nil {
			return err
		}
		if err = s.Store.LinkWerewolfTournamentGame(ctx, tournament.ID, index, game.ID); err != nil {
			_ = s.Store.UpdateWerewolfGame(context.Background(), tournament.RunID, game.ID, "stopped", game.StateJSON, "", "比赛记录写入失败")
			return err
		}
		s.runWerewolfGame(ctx, game, state)
		game, err = s.Store.WerewolfGame(context.Background(), tournament.RunID, game.ID)
		if err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if game.Status != "complete" {
		return fmt.Errorf("状态为 %s：%s", game.Status, game.Error)
	}
	state, err := decodeWerewolfState(game)
	if err != nil {
		return err
	}
	return s.Store.CompleteWerewolfTournamentMatch(ctx, tournament.RunID, tournament.ID, index, game.ID, scoreTournamentMatch(state, game.Winner))
}

func stateForTournamentMatch(plan werewolfTournamentPlan, ids []string) (werewolfState, error) {
	players := make([]werewolfPlayer, 0, len(ids))
	for i, id := range ids {
		if strings.HasPrefix(id, "bot:") {
			players = append(players, werewolfPlayer{StudentID: id, Name: fmt.Sprintf("系统补位 %d", i+1),
				System: true, Alive: true, Seat: i + 1, Prompts: defaultWerewolfPrompts()})
			continue
		}
		player, ok := plan.Agents[id]
		if !ok {
			return werewolfState{}, errors.New("比赛计划缺少学生 Agent")
		}
		player.Seat, player.Alive = i+1, true
		players = append(players, player)
	}
	state, err := makeWerewolfState(players)
	state.Tournament = true
	return state, err
}

func scoreTournamentMatch(state werewolfState, winner string) []store.TournamentResult {
	results := []store.TournamentResult{}
	for _, player := range state.Players {
		if player.System {
			continue
		}
		result := store.TournamentResult{StudentID: player.StudentID, Role: player.Role, Outcome: "draw"}
		if winner == "wolves" {
			if player.Role == "wolf" {
				result.Outcome, result.Points = "win", 6
			} else {
				result.Outcome, result.Points = "loss", -3
			}
		} else if winner == "villagers" {
			if player.Role == "wolf" {
				result.Outcome, result.Points = "loss", -6
			} else {
				result.Outcome, result.Points = "win", 3
			}
		}
		results = append(results, result)
	}
	return results
}

func (s *Server) checkTournamentActive(ctx context.Context, tournament store.WerewolfTournament) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := s.Store.WerewolfTournament(ctx, tournament.RunID, tournament.ID)
	if err != nil || current.Status != "running" {
		return store.ErrNotFound
	}
	run, err := s.Store.ActiveRun(ctx)
	if err != nil || run.ID != tournament.RunID {
		return store.ErrNotFound
	}
	policy, err := s.Store.RunPolicy(ctx, run.ID)
	if err != nil || !policy.WerewolfEnabled {
		return store.ErrNotFound
	}
	return nil
}

func (s *Server) failTournament(tournament store.WerewolfTournament, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, store.ErrNotFound) {
		return
	}
	s.Logger.Error("werewolf tournament failed", "tournament_id", tournament.ID, "error", err)
	_ = s.Store.SetWerewolfTournamentStatus(context.Background(), tournament.RunID, tournament.ID,
		"running", "failed", "比赛中断，请教师检查模型额度与服务配置后继续")
}
