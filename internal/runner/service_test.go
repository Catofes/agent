package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"classroom-agent/internal/runnerapi"
)

func TestArtifactUploadDownloadAndAuthentication(t *testing.T) {
	config := DefaultConfig()
	config.Token = "runner-secret"
	config.StoragePath = filepath.Join(t.TempDir(), "runner")
	config.DockerBinary = "/bin/false"
	service, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := service.Routes()

	request := httptest.NewRequest(http.MethodGet, "/v1/artifacts/nope", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%v", recorder.Code)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "result.csv")
	_, _ = io.WriteString(part, "x,y\n3,4\n")
	_ = writer.Close()
	request = httptest.NewRequest(http.MethodPost, "/v1/artifacts", &body)
	request.Header.Set("Authorization", "Bearer runner-secret")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("upload status=%d body=%s", response.StatusCode, data)
	}
	var artifact runnerapi.Artifact
	if err = json.NewDecoder(response.Body).Decode(&artifact); err != nil || artifact.Name != "result.csv" || artifact.SHA256 == "" {
		t.Fatalf("artifact=%#v err=%v", artifact, err)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+artifact.ID, nil)
	request.Header.Set("Authorization", "Bearer runner-secret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response = recorder.Result()
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(data) != "x,y\n3,4\n" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download status=%d body=%q headers=%v", response.StatusCode, data, response.Header)
	}
}

func TestSafeFileNameAndLimitBuffer(t *testing.T) {
	for _, name := range []string{"../secret", `folder\\secret`, ".", ""} {
		if _, ok := safeFileName(name); ok {
			t.Fatalf("accepted unsafe filename %q", name)
		}
	}
	if name, ok := safeFileName("图表.png"); !ok || name != "图表.png" {
		t.Fatalf("safe filename=%q ok=%v", name, ok)
	}
	buffer := &limitBuffer{limit: 4}
	if n, err := buffer.Write([]byte("abcdef")); err != nil || n != 6 || buffer.String() != "abcd" || !buffer.truncated {
		t.Fatalf("buffer=%q n=%d truncated=%v err=%v", buffer.String(), n, buffer.truncated, err)
	}
}

func TestExecutionQueueIsBoundedAndReleasesCancelledWaiters(t *testing.T) {
	config := DefaultConfig()
	config.Token = "secret"
	config.StoragePath = filepath.Join(t.TempDir(), "storage")
	config.DockerBinary = "/bin/false"
	config.Concurrency = 1
	config.QueueCapacity = 2
	service, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}

	if queued, waited, err := service.acquireExecutionSlot(context.Background()); err != nil || queued || waited != 0 {
		t.Fatalf("first slot queued=%v waited=%v err=%v", queued, waited, err)
	}
	type result struct {
		queued bool
		waited time.Duration
		err    error
	}
	waiter := func(ctx context.Context) <-chan result {
		resultCh := make(chan result, 1)
		go func() {
			queued, waited, err := service.acquireExecutionSlot(ctx)
			resultCh <- result{queued: queued, waited: waited, err: err}
		}()
		return resultCh
	}
	firstWaiter := waiter(context.Background())
	cancelledCtx, cancel := context.WithCancel(context.Background())
	secondWaiter := waiter(cancelledCtx)
	deadline := time.Now().Add(time.Second)
	for len(service.waiting) != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(service.waiting) != 2 {
		t.Fatalf("waiting=%d want 2", len(service.waiting))
	}
	if _, _, err := service.acquireExecutionSlot(context.Background()); !errors.Is(err, errRunnerQueueFull) {
		t.Fatalf("full queue error=%v", err)
	}

	cancel()
	cancelled := <-secondWaiter
	if !cancelled.queued || !errors.Is(cancelled.err, context.Canceled) {
		t.Fatalf("cancelled waiter=%#v", cancelled)
	}
	if len(service.waiting) != 1 {
		t.Fatalf("waiting after cancel=%d want 1", len(service.waiting))
	}

	<-service.semaphore
	acquired := <-firstWaiter
	if !acquired.queued || acquired.err != nil {
		t.Fatalf("queued waiter=%#v", acquired)
	}
	<-service.semaphore
}

func TestHealthReportsExecutionCapacity(t *testing.T) {
	config := DefaultConfig()
	config.Token = "secret"
	config.StoragePath = filepath.Join(t.TempDir(), "storage")
	config.DockerBinary = "/bin/false"
	config.Concurrency = 3
	config.QueueCapacity = 7
	service, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	service.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var status map[string]any
	if err = json.NewDecoder(recorder.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || status["concurrency"] != float64(3) || status["queue_capacity"] != float64(7) || status["running"] != float64(0) || status["queued"] != float64(0) {
		t.Fatalf("status=%d body=%v", recorder.Code, status)
	}
}

func TestFortySimultaneousExecutionsFitDefaultCapacity(t *testing.T) {
	config := DefaultConfig()
	config.Token = "secret"
	config.StoragePath = filepath.Join(t.TempDir(), "storage")
	config.DockerBinary = "/bin/false"
	service, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}

	const requests = 40
	start := make(chan struct{})
	release := make(chan struct{})
	results := make(chan error, requests)
	queuedResults := make(chan bool, requests)
	for range requests {
		go func() {
			<-start
			queued, _, err := service.acquireExecutionSlot(context.Background())
			if err == nil {
				<-release
				<-service.semaphore
			}
			queuedResults <- queued
			results <- err
		}()
	}
	close(start)
	deadline := time.Now().Add(time.Second)
	for (len(service.semaphore) != config.Concurrency || len(service.waiting) != requests-config.Concurrency) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if running, queued := len(service.semaphore), len(service.waiting); running != 16 || queued != 24 {
		t.Fatalf("running=%d queued=%d want 16/24", running, queued)
	}
	close(release)
	queued := 0
	for range requests {
		if err := <-results; err != nil {
			t.Fatalf("execution rejected: %v", err)
		}
		if <-queuedResults {
			queued++
		}
	}
	if queued != 24 {
		t.Fatalf("queued executions=%d want 24", queued)
	}
}

func TestRunPythonUsesFixedDockerIsolationAndStdin(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "docker.log")
	dockerPath := filepath.Join(directory, "docker")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$1" in
  ps|create|rm|kill) exit 0 ;;
  start) IFS= read -r value; printf 'stdin=%%s\n' "$value"; exit 0 ;;
  inspect) printf '0\n'; exit 0 ;;
esac
exit 1
`, logPath)
	if err := os.WriteFile(dockerPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	config.Token = "secret"
	config.StoragePath = filepath.Join(directory, "storage")
	config.DockerBinary = dockerPath
	config.ExecutionTimeout = time.Second
	service, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := service.runPython(context.Background(), runnerapi.ExecuteRequest{RequestID: "request_1", Code: "print(input())", Stdin: "student value\n"})
	if err != nil || response.Status != "completed" || response.ExitCode != 0 || response.Stdout != "stdin=student value\n" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	for _, required := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "no-new-privileges=true", "--user 65534:65534", "python -I -B /workspace/source/main.py"} {
		if !strings.Contains(logText, required) {
			t.Fatalf("docker invocation missing %q:\n%s", required, logText)
		}
	}
	if strings.Contains(logText, "print(input())") {
		t.Fatalf("source leaked into docker command: %s", logText)
	}
}
