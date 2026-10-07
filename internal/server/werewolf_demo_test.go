package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWerewolfManagersCanControlClassAndRunPrivateDemo(t *testing.T) {
	model := &scriptedWerewolfClient{}
	app, st := testServerWithRoster(t, directClient{}, 6)
	app.WerewolfClient = model
	t.Cleanup(app.Shutdown)
	run, err := st.ActiveRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	roster := filepath.Join(t.TempDir(), "students.csv")
	rows := "id,name\nA01,演示教师一\nA02,演示教师二\n"
	for i := 1; i <= 6; i++ {
		rows += fmt.Sprintf("%d,学生%d\n", 2100+i, i)
	}
	if err = os.WriteFile(roster, []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportStudentsCSV(context.Background(), run.ID, roster); err != nil {
		t.Fatal(err)
	}
	routes := app.Routes()
	a01, a02, student := newClient(routes), newClient(routes), newClient(routes)
	for client, id := range map[*testClient]string{a01: "A01", a02: "A02"} {
		if code, _, body := requestJSON(t, client, http.MethodPost, "/api/login", map[string]string{"id": id, "teacher_password": "teacher-secret"}); code != 200 {
			t.Fatalf("manager login %s=%d %s", id, code, body)
		}
	}
	if code, _, _ := requestJSON(t, student, http.MethodPost, "/api/login", map[string]string{"id": "2101"}); code != 200 {
		t.Fatalf("student login=%d", code)
	}
	if code, _, _ := requestJSON(t, student, http.MethodPut, "/api/werewolf/control", map[string]bool{"enabled": true}); code != 403 {
		t.Fatalf("student control=%d", code)
	}
	if code, _, _ := requestJSON(t, student, http.MethodGet, "/api/teacher/werewolf/current", nil); code != 403 {
		t.Fatalf("student teacher board=%d", code)
	}
	if code, _, _ := requestJSON(t, a01, http.MethodGet, "/api/teacher/policy", nil); code != 403 {
		t.Fatalf("unrelated teacher policy=%d", code)
	}
	if code, body, _ := requestJSON(t, a01, http.MethodGet, "/api/werewolf/prompts", nil); code != 200 || body["prompts"] == nil {
		t.Fatalf("manager prompts while closed=%d %#v", code, body)
	}
	if code, _, raw := requestJSON(t, a01, http.MethodPut, "/api/werewolf/prompts", map[string]string{"general": "演示 Soul：逐条核查证据。", "seer_check": "查验 Skill：优先核查发言最强者。"}); code != 200 {
		t.Fatalf("save demo design=%d %s", code, raw)
	}
	if code, body, _ := requestJSON(t, a01, http.MethodPut, "/api/werewolf/control", map[string]bool{"enabled": true}); code != 200 || body["enabled"] != true {
		t.Fatalf("enable=%d %#v", code, body)
	}
	if code, body, _ := requestJSON(t, student, http.MethodGet, "/api/werewolf/current", nil); code != 200 || body["enabled"] != true {
		t.Fatalf("student open=%d %#v", code, body)
	}
	if code, body, _ := requestJSON(t, a02, http.MethodPut, "/api/werewolf/control", map[string]bool{"enabled": false}); code != 200 || body["enabled"] != false {
		t.Fatalf("disable=%d %#v", code, body)
	}
	if code, _, _ := requestJSON(t, student, http.MethodGet, "/api/werewolf/prompts", nil); code != 403 {
		t.Fatalf("closed student editor=%d", code)
	}
	if code, _, _ := requestJSON(t, student, http.MethodGet, "/api/werewolf/demo", nil); code != 403 {
		t.Fatalf("student demo=%d", code)
	}
	code, started, raw := requestJSON(t, a01, http.MethodPost, "/api/werewolf/demo", map[string]string{"role": "seer"})
	if code != 200 {
		t.Fatalf("start demo=%d %s", code, raw)
	}
	game := started["game"].(map[string]any)
	id := game["id"].(string)
	players := game["players"].([]any)
	if len(players) != 6 || players[0].(map[string]any)["role"] != "seer" || players[0].(map[string]any)["mine"] != true {
		t.Fatalf("demo roles=%#v", players)
	}
	for _, rawPlayer := range players[1:] {
		if rawPlayer.(map[string]any)["system"] != true {
			t.Fatalf("non-system opponent=%#v", rawPlayer)
		}
	}
	if code, body, _ := requestJSON(t, a02, http.MethodGet, "/api/werewolf/demo", nil); code != 200 || body["game"] != nil {
		t.Fatalf("other manager demo=%d %#v", code, body)
	}
	if code, _, _ := requestJSON(t, a02, http.MethodGet, "/api/teacher/werewolf/games/"+id+"/events", nil); code != 404 {
		t.Fatalf("other manager accessed demo trace=%d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	var events []any
	for time.Now().Before(deadline) {
		code, body, _ := requestJSON(t, a01, http.MethodGet, "/api/werewolf/demo/events", nil)
		if code != 200 {
			t.Fatalf("demo events=%d", code)
		}
		events = body["events"].([]any)
		for _, rawEvent := range events {
			event := rawEvent.(map[string]any)
			if event["action_skill_name"] == "预言家 · 夜间查验 Skill" {
				if event["werewolf_soul"] != "演示 Soul：逐条核查证据。" || event["action_skill"] != "查验 Skill：优先核查发言最强者。" || !strings.Contains(event["prompt"].(string), "狼人杀 Soul") {
					t.Fatalf("demo prompt components=%#v", event)
				}
				goto checked
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("seer action missing from %d demo events", len(events))
checked:
	if code, body, _ := requestJSON(t, student, http.MethodGet, "/api/werewolf/current", nil); code != 200 || body["game"] != nil {
		t.Fatalf("demo leaked as classroom game=%d %#v", code, body)
	}
	if code, _, _ := requestJSON(t, a01, http.MethodGet, "/api/teacher/werewolf/games/"+id+"/events", nil); code != 404 {
		t.Fatalf("manager accessed demo via classroom trace=%d", code)
	}
}
