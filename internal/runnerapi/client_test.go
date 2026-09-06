package runnerapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func inMemoryClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
}

func TestClientExecutionAndArtifactRoundTrip(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/executions":
			var request ExecuteRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Code != "print(1)" {
				t.Fatalf("request=%#v err=%v", request, err)
			}
			_ = json.NewEncoder(w).Encode(ExecuteResponse{ExecutionID: "exec_1", Status: "completed", Stdout: "1\n"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/artifacts":
			file, header, err := r.FormFile("file")
			if err != nil || header.Filename != "数据.csv" {
				t.Fatalf("upload header=%#v err=%v", header, err)
			}
			data, _ := io.ReadAll(file)
			if string(data) != "a,b\n1,2\n" {
				t.Fatalf("upload=%q", data)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Artifact{ID: "art_1", Name: header.Filename, Size: int64(len(data)), ExpiresAt: time.Now().Add(time.Hour)})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/artifacts/art_1":
			w.Header().Set("Content-Type", "text/csv")
			_, _ = io.WriteString(w, "a,b\n1,2\n")
		default:
			http.NotFound(w, r)
		}
	})

	client := NewClient("http://runner.test", "secret", inMemoryClient(handler))
	result, err := client.Execute(context.Background(), ExecuteRequest{RequestID: "req_1", Code: "print(1)"})
	if err != nil || result.Stdout != "1\n" {
		t.Fatalf("execute=%#v err=%v", result, err)
	}
	artifact, err := client.Upload(context.Background(), "数据.csv", strings.NewReader("a,b\n1,2\n"))
	if err != nil || artifact.ID != "art_1" {
		t.Fatalf("upload=%#v err=%v", artifact, err)
	}
	response, err := client.Download(context.Background(), artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if string(data) != "a,b\n1,2\n" {
		t.Fatalf("download=%q", data)
	}
}

func TestClientRejectsOversizedResponse(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	})
	client := NewClient("http://runner.test", "secret", inMemoryClient(handler))
	client.MaxResponseBytes = 16
	if _, err := client.Execute(context.Background(), ExecuteRequest{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected bounded response error, got %v", err)
	}
}
