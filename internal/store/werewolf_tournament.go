package store

import (
	"context"
	"errors"
	"time"
)

type WerewolfTournament struct {
	ID           string    `json:"id"`
	RunID        string    `json:"-"`
	Status       string    `json:"status"`
	ScheduleJSON string    `json:"-"`
	NextMatch    int       `json:"completed_matches"`
	TotalMatches int       `json:"total_matches"`
	TargetGames  int       `json:"target_games"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type TournamentPlayer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TournamentResult struct {
	StudentID string
	Role      string
	Outcome   string
	Points    int
}

type TournamentStanding struct {
	StudentID string `json:"student_id"`
	Name      string `json:"name"`
	Played    int    `json:"played"`
	Wins      int    `json:"wins"`
	Losses    int    `json:"losses"`
	Draws     int    `json:"draws"`
	Points    int    `json:"points"`
}

func (s *Store) migrateWerewolfTournament(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS werewolf_tournaments(
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), status TEXT NOT NULL
 CHECK(status IN ('running','complete','stopped','failed','interrupted')),
 schedule_json TEXT NOT NULL, next_match INTEGER NOT NULL DEFAULT 0, total_matches INTEGER NOT NULL,
 target_games INTEGER NOT NULL, error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS werewolf_one_running_tournament ON werewolf_tournaments(run_id) WHERE status='running'`,
		`CREATE INDEX IF NOT EXISTS werewolf_tournaments_recent ON werewolf_tournaments(run_id,created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS werewolf_tournament_players(
 tournament_id TEXT NOT NULL REFERENCES werewolf_tournaments(id), student_id TEXT NOT NULL,
 name TEXT NOT NULL, PRIMARY KEY(tournament_id,student_id)
)`,
		`CREATE TABLE IF NOT EXISTS werewolf_tournament_matches(
 tournament_id TEXT NOT NULL REFERENCES werewolf_tournaments(id), match_index INTEGER NOT NULL,
 game_id TEXT NOT NULL REFERENCES werewolf_games(id), PRIMARY KEY(tournament_id,match_index)
)`,
		`CREATE TABLE IF NOT EXISTS werewolf_tournament_results(
 tournament_id TEXT NOT NULL REFERENCES werewolf_tournaments(id), match_index INTEGER NOT NULL,
 student_id TEXT NOT NULL, role TEXT NOT NULL, outcome TEXT NOT NULL
 CHECK(outcome IN ('win','loss','draw')), points INTEGER NOT NULL,
 PRIMARY KEY(tournament_id,match_index,student_id),
 FOREIGN KEY(tournament_id,student_id) REFERENCES werewolf_tournament_players(tournament_id,student_id)
)`,
		`PRAGMA user_version = 19`,
	} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RosterStudents(ctx context.Context, runID string) ([]TournamentPlayer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name FROM students WHERE run_id=? AND active=1 AND account_kind='roster' ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	students := []TournamentPlayer{}
	for rows.Next() {
		var student TournamentPlayer
		if err = rows.Scan(&student.ID, &student.Name); err != nil {
			return nil, err
		}
		students = append(students, student)
	}
	return students, rows.Err()
}

func (s *Store) CreateWerewolfTournament(ctx context.Context, tournament WerewolfTournament, players []TournamentPlayer) error {
	now := formatTime(time.Now().UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO werewolf_tournaments(id,run_id,status,schedule_json,total_matches,target_games,created_at,updated_at)
 VALUES(?,?,'running',?,?,?,?,?)`, tournament.ID, tournament.RunID, tournament.ScheduleJSON,
		tournament.TotalMatches, tournament.TargetGames, now, now); err != nil {
		return err
	}
	for _, player := range players {
		if _, err = tx.ExecContext(ctx, `INSERT INTO werewolf_tournament_players(tournament_id,student_id,name) VALUES(?,?,?)`,
			tournament.ID, player.ID, player.Name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) WerewolfTournament(ctx context.Context, runID, id string) (WerewolfTournament, error) {
	var tournament WerewolfTournament
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id,run_id,status,schedule_json,next_match,total_matches,target_games,error,created_at,updated_at
 FROM werewolf_tournaments WHERE run_id=? AND id=?`, runID, id).Scan(&tournament.ID, &tournament.RunID,
		&tournament.Status, &tournament.ScheduleJSON, &tournament.NextMatch, &tournament.TotalMatches,
		&tournament.TargetGames, &tournament.Error, &created, &updated)
	if err != nil {
		return WerewolfTournament{}, err
	}
	tournament.CreatedAt, _ = parseTime(created)
	tournament.UpdatedAt, _ = parseTime(updated)
	return tournament, nil
}

func (s *Store) LatestWerewolfTournament(ctx context.Context, runID string) (WerewolfTournament, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM werewolf_tournaments WHERE run_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, runID).Scan(&id)
	if err != nil {
		return WerewolfTournament{}, err
	}
	return s.WerewolfTournament(ctx, runID, id)
}

func (s *Store) SetWerewolfTournamentStatus(ctx context.Context, runID, id, from, to, message string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE werewolf_tournaments SET status=?,error=?,updated_at=? WHERE run_id=? AND id=? AND status=?`,
		to, message, formatTime(time.Now().UTC()), runID, id, from)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) InterruptWerewolfTournaments(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE werewolf_tournaments SET status='interrupted',error='服务重启，比赛已中断；教师可继续',updated_at=? WHERE status='running'`,
		formatTime(time.Now().UTC()))
	return err
}

func (s *Store) LinkWerewolfTournamentGame(ctx context.Context, tournamentID string, index int, gameID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO werewolf_tournament_matches(tournament_id,match_index,game_id) VALUES(?,?,?)
 ON CONFLICT(tournament_id,match_index) DO UPDATE SET game_id=excluded.game_id`, tournamentID, index, gameID)
	return err
}

func (s *Store) WerewolfTournamentMatchGame(ctx context.Context, tournamentID string, index int) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT game_id FROM werewolf_tournament_matches WHERE tournament_id=? AND match_index=?`, tournamentID, index).Scan(&id)
	return id, err
}

func (s *Store) CompleteWerewolfTournamentMatch(ctx context.Context, runID, id string, index int, gameID string, results []TournamentResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM werewolf_games WHERE id=? AND run_id=?`, gameID, runID).Scan(&status)
	if err != nil {
		return err
	}
	if status != "complete" {
		return errors.New("match game is not complete")
	}
	res, err := tx.ExecContext(ctx, `UPDATE werewolf_tournaments SET next_match=next_match+1,updated_at=?
 WHERE id=? AND run_id=? AND status='running' AND next_match=? AND EXISTS(
 SELECT 1 FROM werewolf_tournament_matches WHERE tournament_id=? AND match_index=? AND game_id=?)`,
		formatTime(time.Now().UTC()), id, runID, index, id, index, gameID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	for _, result := range results {
		if _, err = tx.ExecContext(ctx, `INSERT INTO werewolf_tournament_results(tournament_id,match_index,student_id,role,outcome,points)
 VALUES(?,?,?,?,?,?)`, id, index, result.StudentID, result.Role, result.Outcome, result.Points); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) WerewolfTournamentStandings(ctx context.Context, runID, id string) ([]TournamentStanding, error) {
	if _, err := s.WerewolfTournament(ctx, runID, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.student_id,p.name,COUNT(r.student_id),
 COALESCE(SUM(CASE WHEN r.outcome='win' THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN r.outcome='loss' THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN r.outcome='draw' THEN 1 ELSE 0 END),0),COALESCE(SUM(r.points),0)
 FROM werewolf_tournament_players p LEFT JOIN werewolf_tournament_results r
 ON r.tournament_id=p.tournament_id AND r.student_id=p.student_id
 WHERE p.tournament_id=? GROUP BY p.student_id,p.name
 ORDER BY COALESCE(SUM(r.points),0) DESC,
 COALESCE(SUM(CASE WHEN r.outcome='win' THEN 1 ELSE 0 END),0) DESC,p.student_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	standings := []TournamentStanding{}
	for rows.Next() {
		var item TournamentStanding
		if err = rows.Scan(&item.StudentID, &item.Name, &item.Played, &item.Wins, &item.Losses, &item.Draws, &item.Points); err != nil {
			return nil, err
		}
		standings = append(standings, item)
	}
	return standings, rows.Err()
}
