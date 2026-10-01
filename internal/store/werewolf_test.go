package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWerewolfFinalScoreCompletesTournamentAtomically(t *testing.T) {
	ctx := context.Background()
	st, csv := testStore(t)
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportStudentsCSV(ctx, run.ID, csv); err != nil {
		t.Fatal(err)
	}
	tournament := WerewolfTournament{ID: "tournament", RunID: run.ID, ScheduleJSON: "{}", TotalMatches: 1, TargetGames: 1}
	if err = st.CreateWerewolfTournament(ctx, tournament, []TournamentPlayer{{ID: "2101", Name: "张三"}}); err != nil {
		t.Fatal(err)
	}
	game, err := st.CreateWerewolfGame(ctx, "game", run.ID, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.LinkWerewolfTournamentGame(ctx, tournament.ID, 0, game.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateWerewolfGame(ctx, run.ID, game.ID, "complete", "{}", "wolves", ""); err != nil {
		t.Fatal(err)
	}
	results := []TournamentResult{{StudentID: "2101", Role: "wolf", Outcome: "win", Points: 6}}
	if err = st.CompleteWerewolfTournamentMatch(ctx, run.ID, tournament.ID, 0, game.ID, results); err != nil {
		t.Fatal(err)
	}
	// A restart directly after scoring must not leave an unresumable interrupted tournament.
	if err = st.InterruptWerewolfTournaments(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := st.WerewolfTournament(ctx, run.ID, tournament.ID)
	if err != nil || current.Status != "complete" || current.NextMatch != 1 {
		t.Fatalf("tournament=%#v err=%v", current, err)
	}
	if err = st.CompleteWerewolfTournamentMatch(ctx, run.ID, tournament.ID, 0, game.ID, results); err == nil {
		t.Fatal("same match scored twice")
	}
	standings, err := st.WerewolfTournamentStandings(ctx, run.ID, tournament.ID)
	if err != nil || len(standings) != 1 || standings[0].Points != 6 || standings[0].Played != 1 {
		t.Fatalf("standings=%#v err=%v", standings, err)
	}
}

func TestWerewolfMigrationFromVersion17PreservesClassroom(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { st.Close() }()
	run, err := st.EnsureActiveRun(ctx, "existing", "已有课堂")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetRunPolicy(ctx, run.ID, RunPolicy{MemoryMode: MemoryModeAdaptive, SkillsEnabled: true, ModelProvider: "qwen"}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"werewolf_tournament_results", "werewolf_tournament_matches", "werewolf_tournament_players", "werewolf_tournaments", "werewolf_events", "werewolf_games", "werewolf_prompts"} {
		if _, err = st.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = st.db.ExecContext(ctx, "ALTER TABLE run_policies DROP COLUMN werewolf_enabled; PRAGMA user_version=17"); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err = st.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 19 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	policy, err := st.RunPolicy(ctx, run.ID)
	if err != nil || policy.WerewolfEnabled || policy.ModelProvider != "qwen" || policy.MemoryMode != MemoryModeAdaptive || !policy.SkillsEnabled {
		t.Fatalf("policy=%#v err=%v", policy, err)
	}
	if _, err = st.CreateWerewolfGame(ctx, "first", run.ID, "{}"); err != nil {
		t.Fatal(err)
	}
	if err = st.CreateWerewolfTournament(ctx, WerewolfTournament{ID: "first", RunID: run.ID, ScheduleJSON: "{}", TotalMatches: 1, TargetGames: 1}, nil); err != nil {
		t.Fatal(err)
	}
}
