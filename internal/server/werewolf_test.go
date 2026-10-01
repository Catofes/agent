package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"classroom-agent/internal/agent"
)

type scriptedWerewolfClient struct {
	mu      sync.Mutex
	prompts []string
}

type waitingWerewolfClient struct{}

func (waitingWerewolfClient) Complete(ctx context.Context, _ agent.CompletionRequest) (agent.Completion, error) {
	<-ctx.Done()
	return agent.Completion{}, ctx.Err()
}

func (c *scriptedWerewolfClient) Complete(_ context.Context, request agent.CompletionRequest) (agent.Completion, error) {
	c.mu.Lock()
	c.prompts = append(c.prompts, request.Messages[0].Content)
	c.mu.Unlock()
	user := request.Messages[len(request.Messages)-1].Content
	answer := "我会依据公开信息判断，请大家核对发言。"
	if strings.Contains(user, "狼人今晚的目标是") {
		answer = "不用"
	} else if index := strings.LastIndex(user, "可选座位："); index >= 0 {
		seats := werewolfNumbers.FindString(user[index:])
		if seats != "" {
			answer = seats
		}
	}
	return agent.Completion{Content: answer, TokensIn: 3, TokensOut: 2}, nil
}

func TestWerewolfRulesAndChoiceValidation(t *testing.T) {
	state := werewolfState{Players: []werewolfPlayer{
		{Seat: 1, Role: "wolf", Alive: true}, {Seat: 2, Role: "wolf", Alive: true},
		{Seat: 3, Role: "seer", Alive: true}, {Seat: 4, Role: "witch", Alive: true},
		{Seat: 5, Role: "villager", Alive: true}, {Seat: 6, Role: "villager", Alive: true},
	}}
	if got := werewolfWinner(state); got != "" {
		t.Fatalf("initial winner=%q", got)
	}
	if got := parseWerewolfChoice("3号", []int{3, 4}); got != 3 {
		t.Fatalf("valid choice=%d", got)
	}
	if got := parseWerewolfChoice("3号或4号", []int{3, 4}); got != 0 {
		t.Fatalf("ambiguous choice=%d", got)
	}
	if got := parseWerewolfChoice("1", []int{3, 4}); got != 0 {
		t.Fatalf("forbidden choice=%d", got)
	}
	if got := parseWerewolfChoice("学号2103", []int{3, 4}); got != 0 {
		t.Fatalf("student id was mistaken for a seat=%d", got)
	}
	state.Players[0].Alive = false
	state.Players[1].Alive = false
	if got := werewolfWinner(state); got != "villagers" {
		t.Fatalf("all wolves gone winner=%q", got)
	}
	state.Players[0].Alive = true
	state.Players[3].Alive = false
	state.Players[4].Alive = false
	state.Players[5].Alive = false
	if got := werewolfWinner(state); got != "wolves" {
		t.Fatalf("wolf parity winner=%q", got)
	}
}

func TestWerewolfTeacherControlAndPrivateTrace(t *testing.T) {
	model := &scriptedWerewolfClient{}
	app, st := testServerWithRoster(t, directClient{}, 7)
	app.WerewolfClient = model
	routes := app.Routes()
	teacher := newClient(routes)
	student := newClient(routes)
	spectator := newClient(routes)
	if code, _, _ := requestJSON(t, teacher, http.MethodPost, "/api/teacher/login", map[string]string{"password": "teacher-secret"}); code != 200 {
		t.Fatalf("teacher login=%d", code)
	}
	for client, id := range map[*testClient]string{student: "2101", spectator: "2107"} {
		if code, _, _ := requestJSON(t, client, http.MethodPost, "/api/login", map[string]string{"id": id}); code != 200 {
			t.Fatalf("student %s login=%d", id, code)
		}
	}
	if code, _, _ := requestJSON(t, student, http.MethodGet, "/api/werewolf/prompts", nil); code != 403 {
		t.Fatalf("disabled prompt endpoint=%d", code)
	}
	selected := []string{"2101", "2102", "2103", "2104", "2105", "2106"}
	if code, _, _ := requestJSON(t, teacher, http.MethodPost, "/api/teacher/werewolf/games", map[string]any{"student_ids": selected}); code != 403 {
		t.Fatalf("disabled game start=%d", code)
	}
	run, _ := st.ActiveRun(context.Background())
	policy, _ := st.RunPolicy(context.Background(), run.ID)
	policy.WerewolfEnabled = true
	if _, err := st.SetRunPolicy(context.Background(), run.ID, policy); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := requestJSON(t, student, http.MethodPut, "/api/werewolf/prompts", map[string]string{"general": "自定义总策略：记录每次投票。"}); code != 200 {
		t.Fatalf("save strategy=%d", code)
	}
	for _, id := range []string{"2102", "2103", "2104", "2105", "2106"} {
		client := newClient(routes)
		if code, _, _ := requestJSON(t, client, http.MethodPost, "/api/login", map[string]string{"id": id}); code != 200 {
			t.Fatalf("student %s login=%d", id, code)
		}
		if code, _, _ := requestJSON(t, client, http.MethodPut, "/api/werewolf/prompts", map[string]string{"general": "自定义总策略：记录每次投票。"}); code != 200 {
			t.Fatalf("student %s prompt save=%d", id, code)
		}
	}
	if code, _, _ := requestJSON(t, teacher, http.MethodPost, "/api/teacher/werewolf/games", map[string]any{"student_ids": []string{"2101", "2101", "2103", "2104", "2105", "2106"}}); code != 400 {
		t.Fatalf("duplicate roster=%d", code)
	}
	code, start, raw := requestJSON(t, teacher, http.MethodPost, "/api/teacher/werewolf/games", map[string]any{"student_ids": selected})
	if code != 200 {
		t.Fatalf("start=%d body=%s", code, raw)
	}
	game := start["game"].(map[string]any)
	id := game["id"].(string)
	deadline := time.Now().Add(5 * time.Second)
	for {
		stored, err := st.WerewolfGame(context.Background(), run.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status != "running" {
			if stored.Status != "complete" {
				t.Fatalf("game status=%s error=%s", stored.Status, stored.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("game did not finish")
		}
		time.Sleep(15 * time.Millisecond)
	}
	model.mu.Lock()
	foundStudentStrategy := false
	for _, prompt := range model.prompts {
		foundStudentStrategy = foundStudentStrategy || strings.Contains(prompt, "自定义总策略：记录每次投票。")
	}
	model.mu.Unlock()
	if !foundStudentStrategy {
		t.Fatal("student-authored strategy was not used by their Agent")
	}
	path := fmt.Sprintf("/api/teacher/werewolf/games/%s/events?after=0", id)
	code, all, _ := requestJSON(t, teacher, http.MethodGet, path, nil)
	if code != 200 || len(all["events"].([]any)) == 0 {
		t.Fatalf("teacher trace=%d %#v", code, all)
	}
	foundPrivatePrompt := false
	for _, rawEvent := range all["events"].([]any) {
		event := rawEvent.(map[string]any)
		foundPrivatePrompt = foundPrivatePrompt || event["channel"] == "private" && event["prompt"] != ""
	}
	if !foundPrivatePrompt {
		t.Fatal("teacher trace did not include private prompt flow")
	}
	code, public, _ := requestJSON(t, spectator, http.MethodGet, strings.Replace(path, "/api/teacher/", "/api/", 1), nil)
	if code != 200 {
		t.Fatalf("spectator trace=%d", code)
	}
	for _, rawEvent := range public["events"].([]any) {
		event := rawEvent.(map[string]any)
		if event["channel"] != "public" || event["prompt"] != nil {
			t.Fatalf("spectator received hidden trace: %#v", event)
		}
	}
	policy.WerewolfEnabled = false
	if _, err := st.SetRunPolicy(context.Background(), run.ID, policy); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := requestJSON(t, spectator, http.MethodGet, strings.Replace(path, "/api/teacher/", "/api/", 1), nil); code != 403 {
		t.Fatalf("disabled spectator trace=%d", code)
	}
	app.Shutdown()
}

func TestWerewolfRunningRolesStayPrivateAndTeacherCanStop(t *testing.T) {
	app, st := testServerWithRoster(t, directClient{}, 7)
	app.WerewolfClient = waitingWerewolfClient{}
	routes := app.Routes()
	teacher, spectator, player := newClient(routes), newClient(routes), newClient(routes)
	if code, _, _ := requestJSON(t, teacher, http.MethodPost, "/api/teacher/login", map[string]string{"password": "teacher-secret"}); code != 200 {
		t.Fatalf("teacher login=%d", code)
	}
	for client, id := range map[*testClient]string{spectator: "2107", player: "2101"} {
		if code, _, _ := requestJSON(t, client, http.MethodPost, "/api/login", map[string]string{"id": id}); code != 200 {
			t.Fatalf("student login=%d", code)
		}
	}
	run, _ := st.ActiveRun(context.Background())
	policy, _ := st.RunPolicy(context.Background(), run.ID)
	policy.WerewolfEnabled = true
	if _, err := st.SetRunPolicy(context.Background(), run.ID, policy); err != nil {
		t.Fatal(err)
	}
	selected := []string{"2101", "2102", "2103", "2104", "2105", "2106"}
	if code, _, raw := requestJSON(t, teacher, http.MethodPost, "/api/teacher/werewolf/games", map[string]any{"student_ids": selected}); code != 200 {
		t.Fatalf("start=%d body=%s", code, raw)
	}
	check := func(client *testClient, path string, wantRoles int) string {
		t.Helper()
		code, body, raw := requestJSON(t, client, http.MethodGet, path, nil)
		if code != 200 {
			t.Fatalf("current=%d body=%s", code, raw)
		}
		game := body["game"].(map[string]any)
		seen := 0
		for _, entry := range game["players"].([]any) {
			if entry.(map[string]any)["role"] != nil {
				seen++
			}
		}
		if seen != wantRoles {
			t.Fatalf("exposed %d roles, wanted %d", seen, wantRoles)
		}
		return game["id"].(string)
	}
	id := check(spectator, "/api/werewolf/current", 0)
	check(player, "/api/werewolf/current", 1)
	check(teacher, "/api/teacher/werewolf/current", 6)
	if code, _, raw := requestJSON(t, teacher, http.MethodPost, "/api/teacher/werewolf/games/"+id+"/stop", nil); code != 200 {
		t.Fatalf("stop=%d body=%s", code, raw)
	}
	game, err := st.WerewolfGame(context.Background(), run.ID, id)
	if err != nil || game.Status != "stopped" {
		t.Fatalf("stored game=%#v err=%v", game, err)
	}
	app.Shutdown()
}

func TestWerewolfRosterExcludesTemporaryAccounts(t *testing.T) {
	app, st := testServerWithRoster(t, directClient{}, 6)
	defer app.Shutdown()
	ctx := context.Background()
	run, _ := st.ActiveRun(ctx)
	if _, err := st.CreateTestStudent(ctx, run.ID, "test_temporary"); err != nil {
		t.Fatal(err)
	}
	teacher := newClient(app.Routes())
	if code, _, _ := requestJSON(t, teacher, http.MethodPost, "/api/teacher/login", map[string]string{"password": "teacher-secret"}); code != 200 {
		t.Fatalf("login=%d", code)
	}
	code, body, raw := requestJSON(t, teacher, http.MethodGet, "/api/teacher/werewolf/roster", nil)
	if code != 200 || len(body["students"].([]any)) != 6 {
		t.Fatalf("roster=%d %s", code, raw)
	}
	for _, item := range body["students"].([]any) {
		if item.(map[string]any)["id"] == "test_temporary" {
			t.Fatal("temporary account included in tournament headcount")
		}
	}
	policy, _ := st.RunPolicy(ctx, run.ID)
	if _, err := app.newWerewolfState(ctx, run.ID, policy, []string{"2101", "2102", "2103", "2104", "2105", "test_temporary"}, false); err == nil {
		t.Fatal("temporary account accepted in a single game")
	}
}
