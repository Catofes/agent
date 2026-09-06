package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"classroom-agent/internal/runnerapi"
	"classroom-agent/internal/store"
)

type pythonRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn pythonRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestPythonExecuteMapsOwnedFilesAndStoresOutputs(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, err := st.EnsureActiveRun(ctx, "run", "课堂")
	if err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(t.TempDir(), "students.csv")
	if err = os.WriteFile(csvPath, []byte("id,name\n2101,张三\n2102,李四\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportStudentsCSV(ctx, run.ID, csvPath); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateConversation(ctx, store.Conversation{ID: "conv", RunID: run.ID, StudentID: "2101"}); err != nil {
		t.Fatal(err)
	}
	input, err := st.CreateArtifact(ctx, store.Artifact{ID: "local_input", RunnerArtifactID: "remote_input", RunID: run.ID, StudentID: "2101", ConversationID: "conv", Name: "data.csv", MIMEType: "text/csv", Size: 4, SHA256: "abc", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	runnerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/executions" {
			http.NotFound(w, r)
			return
		}
		var request runnerapi.ExecuteRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.InputArtifactIDs) != 1 || request.InputArtifactIDs[0] != "remote_input" {
			t.Fatalf("runner ids=%v", request.InputArtifactIDs)
		}
		_ = json.NewEncoder(w).Encode(runnerapi.ExecuteResponse{
			ExecutionID: "exec_1", Status: "completed", Stdout: "ok\n", DurationMS: 12,
			Artifacts: []runnerapi.Artifact{{ID: "remote_output", Name: "chart.png", MIMEType: "image/png", Size: 12, SHA256: "def", ExpiresAt: time.Now().Add(time.Hour)}},
		})
	})
	runnerHTTP := &http.Client{Transport: pythonRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		runnerHandler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}

	tool := &PythonExecute{Runner: runnerapi.NewClient("http://runner.test", "token", runnerHTTP), Store: st, Timeout: time.Second, MaxCodeChars: 1000}
	execCtx := WithExecutionScope(ctx, ExecutionScope{RunID: run.ID, StudentID: "2101", ConversationID: "conv", TurnID: "turn_1"})
	result, err := tool.Execute(execCtx, json.RawMessage(`{"code":"print('ok')","input_artifact_ids":["`+input.ID+`"]}`))
	if err != nil || len(result.Artifacts) != 1 || result.Artifacts[0].Name != "chart.png" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	stored, err := st.Artifact(ctx, run.ID, "2101", result.Artifacts[0].ID)
	if err != nil || stored.RunnerArtifactID != "remote_output" || stored.TurnID != "turn_1" {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	if _, _, err = tool.Run(WithExecutionScope(ctx, ExecutionScope{RunID: run.ID, StudentID: "2102", ConversationID: "conv"}), "print(1)", "", []string{input.ID}); err == nil {
		t.Fatal("other student used the input artifact")
	}
}
