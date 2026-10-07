package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"classroom-agent/internal/agent"
	"classroom-agent/internal/store"
)

type drainingWerewolfClient struct {
	release chan struct{}
	started chan struct{}
}

func (c drainingWerewolfClient) Complete(ctx context.Context, _ agent.CompletionRequest) (agent.Completion, error) {
	select {
	case c.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	<-c.release
	return agent.Completion{}, ctx.Err()
}

func TestWerewolfTournamentScheduleGivesEveryoneTwentyGames(t *testing.T) {
	for _, count := range []int{6, 7, 25, 50} {
		students := make([]store.TournamentPlayer, count)
		for i := range students {
			students[i].ID = fmt.Sprintf("student-%d", i)
		}
		matches, err := buildWerewolfSchedule(students, 20, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != (count*20+5)/6 {
			t.Fatalf("%d students: %d matches", count, len(matches))
		}
		played := map[string]int{}
		bots := 0
		for _, match := range matches {
			if len(match) != 6 {
				t.Fatalf("%d students: match has %d players", count, len(match))
			}
			seen := map[string]bool{}
			for _, id := range match {
				if seen[id] {
					t.Fatalf("student %s appears twice in a match", id)
				}
				seen[id] = true
				if len(id) >= 4 && id[:4] == "bot:" {
					bots++
				} else {
					played[id]++
				}
			}
		}
		if bots != len(matches)*6-count*20 {
			t.Fatalf("%d students: %d filler seats", count, bots)
		}
		for _, student := range students {
			if played[student.ID] != 20 {
				t.Fatalf("%d students: %s played %d", count, student.ID, played[student.ID])
			}
		}
	}
	students := make([]store.TournamentPlayer, 7)
	for i := range students {
		students[i].ID = fmt.Sprint(i)
	}
	if _, err := buildWerewolfSchedule(students, 20, false); err == nil {
		t.Fatal("seven students without filler should fail")
	}
}

func TestWerewolfTournamentScoreByRoleAndOutcome(t *testing.T) {
	state := werewolfState{Players: []werewolfPlayer{
		{StudentID: "a", Role: "wolf"}, {StudentID: "b", Role: "wolf", System: true},
		{StudentID: "c", Role: "villager"}, {StudentID: "d", Role: "villager"},
		{StudentID: "e", Role: "seer"}, {StudentID: "f", Role: "witch"},
	}}
	for _, test := range []struct {
		winner string
		points []int
	}{
		{"wolves", []int{6, -3, -3, -3, -3}},
		{"villagers", []int{-6, 3, 3, 3, 3}},
		{"draw", []int{0, 0, 0, 0, 0}},
	} {
		results := scoreTournamentMatch(state, test.winner)
		if len(results) != 5 {
			t.Fatalf("%s: scored %d players", test.winner, len(results))
		}
		for i, result := range results {
			if result.Points != test.points[i] || result.StudentID == "b" {
				t.Fatalf("%s: result %d=%#v", test.winner, i, result)
			}
		}
	}
}

func TestWerewolfTournamentCompletesAndShowsEveryStudentScore(t *testing.T) {
	app, st := testServerWithRoster(t, directClient{}, 7)
	app.WerewolfClient = &scriptedWerewolfClient{}
	app.Agent.TokenBudget = 1 // The teacher-run competition uses its own API budget.
	routes := app.Routes()
	teacher, student := newClient(routes), newClient(routes)
	if code, _, _ := requestJSON(t, teacher, http.MethodPost, "/api/teacher/login", map[string]string{"password": "teacher-secret"}); code != 200 {
		t.Fatalf("teacher login=%d", code)
	}
	if code, _, _ := requestJSON(t, student, http.MethodPost, "/api/login", map[string]string{"id": "2101"}); code != 200 {
		t.Fatalf("student login=%d", code)
	}
	run, err := st.ActiveRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	policy, err := st.RunPolicy(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy.WerewolfEnabled = true
	if _, err = st.SetRunPolicy(context.Background(), run.ID, policy); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := requestJSON(t, teacher, http.MethodPost, "/api/teacher/werewolf/tournament", map[string]bool{"allow_system": false}); code != 400 {
		t.Fatalf("tournament without filler=%d", code)
	}
	code, started, raw := requestJSON(t, teacher, http.MethodPost, "/api/teacher/werewolf/tournament", map[string]bool{"allow_system": true})
	if code != 200 {
		t.Fatalf("start=%d body=%s", code, raw)
	}
	tournament := started["tournament"].(map[string]any)
	if tournament["total_matches"].(float64) != 24 {
		t.Fatalf("planned matches=%v", tournament["total_matches"])
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		current, err := st.LatestWerewolfTournament(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "running" {
			if current.Status != "complete" || current.NextMatch != 24 {
				t.Fatalf("tournament status=%s progress=%d error=%s", current.Status, current.NextMatch, current.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tournament timed out at match %d", current.NextMatch)
		}
		time.Sleep(20 * time.Millisecond)
	}
	code, body, raw := requestJSON(t, student, http.MethodGet, "/api/werewolf/tournament", nil)
	if code != 200 {
		t.Fatalf("student leaderboard=%d body=%s", code, raw)
	}
	standings := body["standings"].([]any)
	if len(standings) != 7 {
		t.Fatalf("standings has %d students", len(standings))
	}
	for _, entry := range standings {
		row := entry.(map[string]any)
		if row["played"].(float64) != 20 {
			t.Fatalf("student %s played %v", row["student_id"], row["played"])
		}
	}
	usage, err := st.Usage(context.Background(), run.ID, "2101")
	if err != nil || usage.TokensIn+usage.TokensOut != 0 {
		t.Fatalf("tournament changed student workbench quota: %#v, %v", usage, err)
	}
	app.Shutdown()
}

func TestWerewolfTournamentPauseAndResumeKeepsProgress(t *testing.T) {
	t.Setenv("WEREWOLF_TOURNAMENT_WORKERS", "1")
	app, st := testServerWithRoster(t, directClient{}, 6)
	release := make(chan struct{})
	startedModel := make(chan struct{}, 1)
	var released sync.Once
	releaseWorker := func() { released.Do(func() { close(release) }) }
	t.Cleanup(releaseWorker)
	app.WerewolfClient = drainingWerewolfClient{release: release, started: startedModel}
	routes := app.Routes()
	teacher := newClient(routes)
	if code, _, _ := requestJSON(t, teacher, http.MethodPost, "/api/teacher/login", map[string]string{"password": "teacher-secret"}); code != 200 {
		t.Fatalf("teacher login=%d", code)
	}
	run, _ := st.ActiveRun(context.Background())
	policy, _ := st.RunPolicy(context.Background(), run.ID)
	policy.WerewolfEnabled = true
	if _, err := st.SetRunPolicy(context.Background(), run.ID, policy); err != nil {
		t.Fatal(err)
	}
	code, started, raw := requestJSON(t, teacher, http.MethodPost, "/api/teacher/werewolf/tournament", map[string]bool{"allow_system": false})
	if code != 200 {
		t.Fatalf("start=%d body=%s", code, raw)
	}
	id := started["tournament"].(map[string]any)["id"].(string)
	select {
	case <-startedModel:
	case <-time.After(3 * time.Second):
		t.Fatal("tournament model did not start")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		matches, _ := st.WerewolfTournamentMatches(context.Background(), run.ID, id)
		if len(matches) > 0 && matches[0].Status == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first tournament match did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, _, raw := requestJSON(t, teacher, http.MethodPost, "/api/teacher/werewolf/tournament/"+id+"/stop", nil); code != 200 {
		t.Fatalf("pause=%d body=%s", code, raw)
	}
	paused, _ := st.WerewolfTournament(context.Background(), run.ID, id)
	if paused.Status != "stopped" || paused.NextMatch != 0 {
		t.Fatalf("paused tournament=%#v", paused)
	}
	if code, _, raw := requestJSON(t, teacher, http.MethodPost, "/api/teacher/werewolf/tournament/"+id+"/resume", nil); code != 409 {
		t.Fatalf("resumed before old worker exited=%d body=%s", code, raw)
	}
	releaseWorker()
	deadline = time.Now().Add(3 * time.Second)
	for {
		app.tournamentMu.Lock()
		finished := app.tournamentCancel == nil
		app.tournamentMu.Unlock()
		if finished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("paused worker did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	app.WerewolfClient = &scriptedWerewolfClient{}
	if code, _, raw := requestJSON(t, teacher, http.MethodPost, "/api/teacher/werewolf/tournament/"+id+"/resume", nil); code != 200 {
		t.Fatalf("resume=%d body=%s", code, raw)
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		current, err := st.WerewolfTournament(context.Background(), run.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "running" {
			if current.Status != "complete" || current.NextMatch != 20 {
				t.Fatalf("resumed tournament=%#v", current)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resumed tournament timed out after %d matches", current.NextMatch)
		}
		time.Sleep(20 * time.Millisecond)
	}
	app.Shutdown()
}

func TestWerewolfTournamentFortyStudentsCanReplaySavedMatch(t *testing.T) {
	app, st := testServerWithRoster(t, directClient{}, 40)
	t.Cleanup(app.Shutdown)
	app.WerewolfClient = &scriptedWerewolfClient{}
	teacher := newClient(app.Routes())
	if code, _, _ := requestJSON(t, teacher, http.MethodPost, "/api/teacher/login", map[string]string{"password": "teacher-secret"}); code != 200 {
		t.Fatalf("login=%d", code)
	}
	run, _ := st.ActiveRun(context.Background())
	policy, _ := st.RunPolicy(context.Background(), run.ID)
	policy.WerewolfEnabled = true
	if _, err := st.SetRunPolicy(context.Background(), run.ID, policy); err != nil {
		t.Fatal(err)
	}
	code, started, raw := requestJSON(t, teacher, http.MethodPost, "/api/teacher/werewolf/tournament", map[string]bool{"allow_system": true})
	if code != 200 {
		t.Fatalf("start=%d body=%s", code, raw)
	}
	id := started["tournament"].(map[string]any)["id"].(string)
	// Race instrumentation makes this SQLite-heavy integration test much slower
	// on shared CI runners; this verifies completion, not a response-time SLA.
	deadline := time.Now().Add(90 * time.Second)
	for {
		current, err := st.WerewolfTournament(context.Background(), run.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "running" {
			if current.Status != "complete" || current.NextMatch != 134 {
				t.Fatalf("tournament=%#v", current)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %d matches", current.NextMatch)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := st.LatestWerewolfGame(context.Background(), run.ID); err == nil {
		t.Fatal("tournament match appeared as live classroom game")
	}
	standings, err := st.WerewolfTournamentStandings(context.Background(), run.ID, id)
	if err != nil || len(standings) != 40 {
		t.Fatalf("standings count=%d err=%v", len(standings), err)
	}
	for _, row := range standings {
		if row.Played != 20 || row.Wins+row.Losses+row.Draws != 20 {
			t.Fatalf("incomplete student standing=%#v", row)
		}
	}
	code, body, raw := requestJSON(t, teacher, http.MethodGet, "/api/teacher/werewolf/tournament/"+id+"/matches", nil)
	if code != 200 || len(body["matches"].([]any)) != 134 {
		t.Fatalf("matches=%d body=%s", code, raw)
	}
	code, body, raw = requestJSON(t, teacher, http.MethodGet, "/api/teacher/werewolf/tournament/"+id+"/matches/0", nil)
	if code != 200 || len(body["events"].([]any)) == 0 {
		t.Fatalf("replay=%d body=%s", code, raw)
	}
	app.Shutdown()
}
