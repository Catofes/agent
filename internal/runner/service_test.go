package runner

import (
	"bytes"
	"context"
	"encoding/json"
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
