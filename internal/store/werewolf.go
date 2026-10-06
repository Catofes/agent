package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type WerewolfGame struct {
	ID           string    `json:"id"`
	RunID        string    `json:"-"`
	DemoOwner    string    `json:"-"`
	TournamentID string    `json:"-"`
	Status       string    `json:"status"`
	StateJSON    string    `json:"-"`
	Winner       string    `json:"winner,omitempty"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (s *Store) migrateWerewolfDemo(ctx context.Context) error {
	hasColumn, err := s.tableHasColumn(ctx, "werewolf_games", "demo_owner")
	if err != nil {
		return err
	}
	hasManagerColumn, err := s.tableHasColumn(ctx, "sessions", "manager_authorized")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !hasColumn {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE werewolf_games ADD COLUMN demo_owner TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !hasManagerColumn {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN manager_authorized INTEGER NOT NULL DEFAULT 0 CHECK(manager_authorized IN (0,1))`); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS werewolf_demo_recent ON werewolf_games(run_id,demo_owner,created_at DESC)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 20`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateWerewolfParallel(ctx context.Context) error {
	hasColumn, err := s.tableHasColumn(ctx, "werewolf_games", "tournament_id")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	queries := []string{}
	if !hasColumn {
		queries = append(queries, `ALTER TABLE werewolf_games ADD COLUMN tournament_id TEXT NOT NULL DEFAULT ''`)
	}
	queries = append(queries,
		`UPDATE werewolf_games SET tournament_id=(SELECT tournament_id FROM werewolf_tournament_matches WHERE game_id=werewolf_games.id LIMIT 1) WHERE EXISTS(SELECT 1 FROM werewolf_tournament_matches WHERE game_id=werewolf_games.id)`,
		`UPDATE werewolf_games SET tournament_id='legacy-tournament' WHERE tournament_id='' AND json_valid(state_json) AND json_extract(state_json,'$.tournament')=1`,
		`DROP INDEX IF EXISTS werewolf_one_running_per_run`,
		`CREATE UNIQUE INDEX IF NOT EXISTS werewolf_one_running_per_run ON werewolf_games(run_id) WHERE status='running' AND tournament_id=''`,
		`CREATE INDEX IF NOT EXISTS werewolf_tournament_games ON werewolf_games(tournament_id,status)`,
		`PRAGMA user_version = 21`,
	)
	for _, query := range queries {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type WerewolfEvent struct {
	ID         int64     `json:"id"`
	GameID     string    `json:"-"`
	Phase      string    `json:"phase"`
	Type       string    `json:"type"`
	ActorID    string    `json:"actor_id,omitempty"`
	TargetID   string    `json:"-"`
	Visibility string    `json:"-"`
	Text       string    `json:"text"`
	Prompt     string    `json:"prompt,omitempty"`
	Response   string    `json:"response,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Store) migrateWerewolf(ctx context.Context) error {
	hasPolicyColumn, err := s.tableHasColumn(ctx, "run_policies", "werewolf_enabled")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	queries := []string{}
	if !hasPolicyColumn {
		queries = append(queries, `ALTER TABLE run_policies ADD COLUMN werewolf_enabled INTEGER NOT NULL DEFAULT 0 CHECK(werewolf_enabled IN (0,1))`)
	}
	queries = append(queries,
		`CREATE TABLE IF NOT EXISTS werewolf_prompts(
 run_id TEXT NOT NULL, student_id TEXT NOT NULL, prompts_json TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(run_id,student_id), FOREIGN KEY(run_id,student_id) REFERENCES students(run_id,id)
)`,
		`CREATE TABLE IF NOT EXISTS werewolf_games(
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), status TEXT NOT NULL
 CHECK(status IN ('running','complete','stopped','failed','interrupted')),
 state_json TEXT NOT NULL, winner TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS werewolf_one_running_per_run ON werewolf_games(run_id) WHERE status='running'`,
		`CREATE INDEX IF NOT EXISTS werewolf_games_recent ON werewolf_games(run_id,created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS werewolf_events(
 id INTEGER PRIMARY KEY AUTOINCREMENT, game_id TEXT NOT NULL REFERENCES werewolf_games(id),
 phase TEXT NOT NULL, type TEXT NOT NULL, actor_id TEXT NOT NULL DEFAULT '',
 target_id TEXT NOT NULL DEFAULT '', visibility TEXT NOT NULL
 CHECK(visibility IN ('public','wolves','private','teacher')),
 text TEXT NOT NULL DEFAULT '', prompt TEXT NOT NULL DEFAULT '', response TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS werewolf_events_game ON werewolf_events(game_id,id)`,
		`PRAGMA user_version = 18`,
	)
	for _, query := range queries {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) WerewolfPrompts(ctx context.Context, runID, studentID string) (string, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT prompts_json FROM werewolf_prompts WHERE run_id=? AND student_id=?`, runID, studentID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return raw, err == nil, err
}

func (s *Store) SaveWerewolfPrompts(ctx context.Context, runID, studentID, raw string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO werewolf_prompts(run_id,student_id,prompts_json,updated_at)
 VALUES(?,?,?,?) ON CONFLICT(run_id,student_id) DO UPDATE SET prompts_json=excluded.prompts_json,updated_at=excluded.updated_at`,
		runID, studentID, raw, formatTime(time.Now().UTC()))
	return err
}

func (s *Store) WerewolfPromptReadiness(ctx context.Context, runID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT student_id FROM werewolf_prompts WHERE run_id=?`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ready := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ready[id] = true
	}
	return ready, rows.Err()
}

func (s *Store) CreateWerewolfGame(ctx context.Context, id, runID, stateJSON string) (WerewolfGame, error) {
	now := formatTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `INSERT INTO werewolf_games(id,run_id,status,state_json,created_at,updated_at)
 VALUES(?,?,'running',?,?,?)`, id, runID, stateJSON, now, now)
	if err != nil {
		return WerewolfGame{}, err
	}
	return s.WerewolfGame(ctx, runID, id)
}

func (s *Store) CreateWerewolfTournamentGame(ctx context.Context, id, runID, tournamentID, stateJSON string) (WerewolfGame, error) {
	now := formatTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `INSERT INTO werewolf_games(id,run_id,tournament_id,status,state_json,created_at,updated_at)
 VALUES(?,?,?,'running',?,?,?)`, id, runID, tournamentID, stateJSON, now, now)
	if err != nil {
		return WerewolfGame{}, err
	}
	return s.WerewolfGame(ctx, runID, id)
}

func (s *Store) CreateWerewolfDemo(ctx context.Context, id, runID, owner, stateJSON string) (WerewolfGame, error) {
	now := formatTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `INSERT INTO werewolf_games(id,run_id,demo_owner,status,state_json,created_at,updated_at)
 VALUES(?,?,?,'running',?,?,?)`, id, runID, owner, stateJSON, now, now)
	if err != nil {
		return WerewolfGame{}, err
	}
	return s.WerewolfGame(ctx, runID, id)
}

func (s *Store) WerewolfGame(ctx context.Context, runID, id string) (WerewolfGame, error) {
	var game WerewolfGame
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id,run_id,demo_owner,tournament_id,status,state_json,winner,error,created_at,updated_at
 FROM werewolf_games WHERE run_id=? AND id=?`, runID, id).Scan(
		&game.ID, &game.RunID, &game.DemoOwner, &game.TournamentID, &game.Status, &game.StateJSON, &game.Winner, &game.Error, &created, &updated)
	if err != nil {
		return WerewolfGame{}, err
	}
	game.CreatedAt, _ = parseTime(created)
	game.UpdatedAt, _ = parseTime(updated)
	return game, nil
}

func (s *Store) LatestWerewolfGame(ctx context.Context, runID string) (WerewolfGame, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM werewolf_games WHERE run_id=? AND demo_owner='' AND tournament_id='' ORDER BY created_at DESC,id DESC LIMIT 1`, runID).Scan(&id)
	if err != nil {
		return WerewolfGame{}, err
	}
	return s.WerewolfGame(ctx, runID, id)
}

func (s *Store) RunningWerewolfInteractiveGame(ctx context.Context, runID string) (WerewolfGame, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM werewolf_games WHERE run_id=? AND tournament_id='' AND status='running' LIMIT 1`, runID).Scan(&id)
	if err != nil {
		return WerewolfGame{}, err
	}
	return s.WerewolfGame(ctx, runID, id)
}

func (s *Store) LatestWerewolfDemo(ctx context.Context, runID, owner string) (WerewolfGame, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM werewolf_games WHERE run_id=? AND demo_owner=? ORDER BY created_at DESC,id DESC LIMIT 1`, runID, owner).Scan(&id)
	if err != nil {
		return WerewolfGame{}, err
	}
	return s.WerewolfGame(ctx, runID, id)
}

func (s *Store) UpdateWerewolfGame(ctx context.Context, runID, id, status, stateJSON, winner, message string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE werewolf_games SET status=?,state_json=?,winner=?,error=?,updated_at=?
 WHERE run_id=? AND id=? AND status='running'`, status, stateJSON, winner, message, formatTime(time.Now().UTC()), runID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AppendWerewolfEvent(ctx context.Context, runID, id, stateJSON string, event WerewolfEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE werewolf_games SET state_json=?,updated_at=? WHERE run_id=? AND id=? AND status='running'`,
		stateJSON, formatTime(time.Now().UTC()), runID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO werewolf_events(game_id,phase,type,actor_id,target_id,visibility,text,prompt,response,created_at)
 VALUES(?,?,?,?,?,?,?,?,?,?)`, id, event.Phase, event.Type, event.ActorID, event.TargetID, event.Visibility,
		event.Text, event.Prompt, event.Response, formatTime(time.Now().UTC()))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) WerewolfEvents(ctx context.Context, runID, id string, after int64) ([]WerewolfEvent, error) {
	if _, err := s.WerewolfGame(ctx, runID, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,game_id,phase,type,actor_id,target_id,visibility,text,prompt,response,created_at
 FROM werewolf_events WHERE game_id=? AND id>? ORDER BY id LIMIT 500`, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []WerewolfEvent{}
	for rows.Next() {
		var event WerewolfEvent
		var created string
		if err = rows.Scan(&event.ID, &event.GameID, &event.Phase, &event.Type, &event.ActorID,
			&event.TargetID, &event.Visibility, &event.Text, &event.Prompt, &event.Response, &created); err != nil {
			return nil, err
		}
		event.CreatedAt, _ = parseTime(created)
		items = append(items, event)
	}
	return items, rows.Err()
}

func (s *Store) InterruptWerewolfGames(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE werewolf_games SET status='interrupted',error='服务重启，对局已中断',updated_at=?
 WHERE status='running'`, formatTime(time.Now().UTC()))
	return err
}

func (s *Store) StopWerewolfGame(ctx context.Context, runID, id string) error {
	game, err := s.WerewolfGame(ctx, runID, id)
	if err != nil {
		return err
	}
	if game.Status != "running" {
		return fmt.Errorf("game is %s", game.Status)
	}
	return s.UpdateWerewolfGame(ctx, runID, id, "stopped", game.StateJSON, "", "教师已结束对局")
}

func (s *Store) StopWerewolfTournamentGames(ctx context.Context, runID, tournamentID, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE werewolf_games SET status='stopped',error=?,updated_at=? WHERE run_id=? AND tournament_id=? AND status='running'`,
		message, formatTime(time.Now().UTC()), runID, tournamentID)
	return err
}
