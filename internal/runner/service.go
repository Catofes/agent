package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"classroom-agent/internal/runnerapi"
)

const metadataFile = "metadata.json"

var safeIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var errArtifactTooLarge = errors.New("artifact exceeds size limit")

type Config struct {
	ListenAddr       string
	Token            string
	StoragePath      string
	DockerBinary     string
	PythonImage      string
	ExecutionTimeout time.Duration
	ArtifactTTL      time.Duration
	Concurrency      int
	MaxCodeBytes     int64
	MaxStdinBytes    int64
	MaxOutputBytes   int64
	MaxFileBytes     int64
	MaxFiles         int
	Memory           string
	CPUs             string
	PIDs             int
}

func DefaultConfig() Config {
	return Config{
		ListenAddr: ":8090", StoragePath: "/var/lib/classroom-runner", DockerBinary: "docker",
		PythonImage: "classroom-python:latest", ExecutionTimeout: 5 * time.Second,
		ArtifactTTL: 24 * time.Hour, Concurrency: 8, MaxCodeBytes: 64 << 10,
		MaxStdinBytes: 64 << 10, MaxOutputBytes: 128 << 10, MaxFileBytes: 10 << 20,
		MaxFiles: 10, Memory: "256m", CPUs: "0.5", PIDs: 64,
	}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("RUNNER_TOKEN is required")
	}
	if c.Concurrency < 1 || c.ExecutionTimeout <= 0 || c.ArtifactTTL <= 0 {
		return errors.New("runner concurrency and timeouts must be positive")
	}
	if c.MaxCodeBytes < 1 || c.MaxStdinBytes < 0 || c.MaxOutputBytes < 1 || c.MaxFileBytes < 1 || c.MaxFiles < 1 {
		return errors.New("runner size limits must be positive")
	}
	if !filepath.IsAbs(c.StoragePath) {
		return errors.New("RUNNER_STORAGE_PATH must be absolute and identical inside the runner container and host")
	}
	if strings.TrimSpace(c.PythonImage) == "" {
		return errors.New("RUNNER_PYTHON_IMAGE is required")
	}
	return nil
}

type Service struct {
	config    Config
	logger    *slog.Logger
	semaphore chan struct{}
	dockerMu  sync.Mutex
}

func New(config Config, logger *slog.Logger) (*Service, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	for _, dir := range []string{config.StoragePath, filepath.Join(config.StoragePath, "artifacts"), filepath.Join(config.StoragePath, "executions")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create runner storage: %w", err)
		}
	}
	s := &Service{config: config, logger: logger, semaphore: make(chan struct{}, config.Concurrency)}
	s.cleanupExpired()
	s.cleanupContainers(context.Background())
	return s, nil
}

func (s *Service) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) })
	r.Group(func(r chi.Router) {
		r.Use(s.authenticate)
		r.Post("/v1/artifacts", s.uploadArtifact)
		r.Get("/v1/artifacts/{id}", s.downloadArtifact)
		r.Delete("/v1/artifacts/{id}", s.deleteArtifact)
		r.Post("/v1/executions", s.execute)
	})
	return r
}

func (s *Service) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(s.config.Token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.config.Token)) != 1 {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "runner authentication failed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) uploadArtifact(w http.ResponseWriter, r *http.Request) {
	s.cleanupExpired()
	r.Body = http.MaxBytesReader(w, r.Body, s.config.MaxFileBytes+(1<<20))
	if err := r.ParseMultipartForm(s.config.MaxFileBytes + (1 << 20)); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_UPLOAD", "invalid or oversized upload")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "FILE_REQUIRED", "file is required")
		return
	}
	defer file.Close()
	name, ok := safeFileName(header.Filename)
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_FILENAME", "invalid filename")
		return
	}
	artifact, err := s.saveArtifact(file, name)
	if err != nil {
		if errors.Is(err, errArtifactTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "ARTIFACT_TOO_LARGE", "artifact exceeds size limit")
			return
		}
		writeError(w, http.StatusInternalServerError, "STORE_FAILED", "could not store artifact")
		return
	}
	writeJSON(w, http.StatusCreated, artifact)
}

func (s *Service) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	artifact, filePath, err := s.loadArtifact(chi.URLParam(r, "id"))
	if errors.Is(err, os.ErrNotExist) || (err == nil && time.Now().UTC().After(artifact.ExpiresAt)) {
		writeError(w, http.StatusNotFound, "ARTIFACT_NOT_FOUND", "artifact not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_FAILED", "could not read artifact")
		return
	}
	w.Header().Set("Content-Type", artifact.MIMEType)
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", contentDisposition(artifact.Name))
	http.ServeFile(w, r, filePath)
}

func (s *Service) deleteArtifact(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !safeIDPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "ARTIFACT_NOT_FOUND", "artifact not found")
		return
	}
	err := os.RemoveAll(filepath.Join(s.config.StoragePath, "artifacts", id))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_FAILED", "could not delete artifact")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) execute(w http.ResponseWriter, r *http.Request) {
	s.cleanupExpired()
	r.Body = http.MaxBytesReader(w, r.Body, s.config.MaxCodeBytes+s.config.MaxStdinBytes+(64<<10))
	var request runnerapi.ExecuteRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid execution request")
		return
	}
	if !safeIDPattern.MatchString(request.RequestID) || strings.TrimSpace(request.Code) == "" || int64(len(request.Code)) > s.config.MaxCodeBytes || int64(len(request.Stdin)) > s.config.MaxStdinBytes || !utf8.ValidString(request.Code) || !utf8.ValidString(request.Stdin) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "request id, code, or stdin is invalid")
		return
	}
	select {
	case s.semaphore <- struct{}{}:
		defer func() { <-s.semaphore }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "RUNNER_BUSY", "runner is busy")
		return
	}
	response, err := s.runPython(r.Context(), request)
	if err != nil {
		s.logger.Warn("python execution failed", "request_id", request.RequestID, "error", err)
		writeError(w, http.StatusBadGateway, "EXECUTION_FAILED", "python execution failed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Service) runPython(parent context.Context, request runnerapi.ExecuteRequest) (response runnerapi.ExecuteResponse, returnErr error) {
	started := time.Now()
	executionID, err := randomID("exec_")
	if err != nil {
		return response, err
	}
	response.ExecutionID = executionID
	root := filepath.Join(s.config.StoragePath, "executions", executionID)
	sourcePath, inputPath, outputPath := filepath.Join(root, "source"), filepath.Join(root, "input"), filepath.Join(root, "output")
	if err = os.Mkdir(root, 0o750); err != nil {
		return response, err
	}
	defer cleanupExecutionRoot(root)
	if err = os.Mkdir(sourcePath, 0o750); err != nil {
		return response, err
	}
	if err = os.Mkdir(inputPath, 0o750); err != nil {
		return response, err
	}
	if err = os.Mkdir(outputPath, 0o777); err != nil {
		return response, err
	}
	if err = os.Chmod(outputPath, 0o777); err != nil {
		return response, err
	}
	if err = s.materializeInputs(request.InputArtifactIDs, inputPath); err != nil {
		return response, err
	}
	if err = os.WriteFile(filepath.Join(sourcePath, "main.py"), []byte(request.Code), 0o444); err != nil {
		return response, err
	}
	if err = os.Chmod(sourcePath, 0o555); err != nil {
		return response, err
	}
	if err = os.Chmod(inputPath, 0o555); err != nil {
		return response, err
	}
	containerName := "classroom-python-" + strings.TrimPrefix(executionID, "exec_")
	args := []string{
		"create", "--pull=never", "--name", containerName,
		"--label", "classroom.runner=true", "--network=none", "--read-only",
		"--cap-drop=ALL", "--security-opt", "no-new-privileges=true",
		"--pids-limit", strconv.Itoa(s.config.PIDs), "--memory", s.config.Memory,
		"--memory-swap", s.config.Memory, "--cpus", s.config.CPUs,
		"--user", "65534:65534", "--workdir", "/workspace", "--interactive",
		"--env", "HOME=/tmp", "--env", "TMPDIR=/tmp",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=16m",
		"--mount", "type=bind,src=" + sourcePath + ",dst=/workspace/source,readonly",
		"--mount", "type=bind,src=" + inputPath + ",dst=/workspace/input,readonly",
		"--mount", "type=bind,src=" + outputPath + ",dst=/workspace/output",
		s.config.PythonImage, "python", "-I", "-B", "/workspace/source/main.py",
	}
	create := exec.CommandContext(parent, s.config.DockerBinary, args...)
	if output, createErr := create.CombinedOutput(); createErr != nil {
		return response, fmt.Errorf("docker create: %w: %s", createErr, strings.TrimSpace(string(output)))
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanupCtx, s.config.DockerBinary, "rm", "-f", containerName).Run()
	}()

	ctx, cancel := context.WithTimeout(parent, s.config.ExecutionTimeout)
	defer cancel()
	stdout, stderr := &limitBuffer{limit: s.config.MaxOutputBytes}, &limitBuffer{limit: s.config.MaxOutputBytes}
	command := exec.CommandContext(ctx, s.config.DockerBinary, "start", "--attach", "--interactive", containerName)
	command.Stdin = strings.NewReader(request.Stdin)
	command.Stdout, command.Stderr = stdout, stderr
	runErr := command.Run()
	response.Stdout, response.Stderr = stdout.String(), stderr.String()
	response.Truncated = stdout.truncated || stderr.truncated
	response.Status = "completed"
	if ctx.Err() != nil {
		response.Status = "timeout"
		response.ExitCode = 124
		killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = exec.CommandContext(killCtx, s.config.DockerBinary, "kill", containerName).Run()
		killCancel()
	} else {
		exitCode, inspectErr := s.containerExitCode(parent, containerName)
		if inspectErr != nil {
			if runErr != nil {
				return response, fmt.Errorf("docker start: %w", runErr)
			}
			return response, inspectErr
		}
		response.ExitCode = exitCode
		if exitCode != 0 {
			response.Status = "failed"
		}
	}
	artifacts, artifactErr := s.collectOutputs(outputPath)
	if artifactErr != nil {
		return response, artifactErr
	}
	response.Artifacts = artifacts
	response.DurationMS = time.Since(started).Milliseconds()
	return response, nil
}

func cleanupExecutionRoot(root string) {
	_ = os.Chmod(filepath.Join(root, "source"), 0o750)
	_ = os.Chmod(filepath.Join(root, "input"), 0o750)
	_ = os.Chmod(filepath.Join(root, "output"), 0o750)
	_ = os.RemoveAll(root)
}

func (s *Service) containerExitCode(ctx context.Context, name string) (int, error) {
	output, err := exec.CommandContext(ctx, s.config.DockerBinary, "inspect", "--format", "{{.State.ExitCode}}", name).Output()
	if err != nil {
		return 0, fmt.Errorf("docker inspect: %w", err)
	}
	exitCode, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0, fmt.Errorf("invalid docker exit code")
	}
	return exitCode, nil
}

func (s *Service) materializeInputs(ids []string, destination string) error {
	if len(ids) > s.config.MaxFiles {
		return errors.New("too many input artifacts")
	}
	used := map[string]bool{}
	for _, id := range ids {
		artifact, source, err := s.loadArtifact(id)
		if err != nil || time.Now().UTC().After(artifact.ExpiresAt) {
			return fmt.Errorf("input artifact not found")
		}
		name := uniqueName(artifact.Name, used)
		if err = copyFile(source, filepath.Join(destination, name), 0o444, s.config.MaxFileBytes); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) collectOutputs(directory string) ([]runnerapi.Artifact, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	artifacts := make([]runnerapi.Artifact, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || len(artifacts) >= s.config.MaxFiles {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > s.config.MaxFileBytes {
			continue
		}
		name, ok := safeFileName(entry.Name())
		if !ok {
			continue
		}
		file, err := os.Open(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		artifact, saveErr := s.saveArtifact(file, name)
		file.Close()
		if saveErr != nil {
			return nil, saveErr
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func (s *Service) saveArtifact(source io.Reader, name string) (runnerapi.Artifact, error) {
	id, err := randomID("runner_art_")
	if err != nil {
		return runnerapi.Artifact{}, err
	}
	directory := filepath.Join(s.config.StoragePath, "artifacts", id)
	if err = os.Mkdir(directory, 0o750); err != nil {
		return runnerapi.Artifact{}, err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(directory)
		}
	}()
	filePath := filepath.Join(directory, "data")
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o440)
	if err != nil {
		return runnerapi.Artifact{}, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(source, s.config.MaxFileBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return runnerapi.Artifact{}, copyErr
	}
	if closeErr != nil {
		return runnerapi.Artifact{}, closeErr
	}
	if written > s.config.MaxFileBytes {
		return runnerapi.Artifact{}, errArtifactTooLarge
	}
	artifact := runnerapi.Artifact{ID: id, Name: name, MIMEType: mimeType(name), Size: written, SHA256: hex.EncodeToString(hash.Sum(nil)), ExpiresAt: time.Now().UTC().Add(s.config.ArtifactTTL)}
	metadata, _ := json.Marshal(artifact)
	if err = os.WriteFile(filepath.Join(directory, metadataFile), metadata, 0o440); err != nil {
		return runnerapi.Artifact{}, err
	}
	failed = false
	return artifact, nil
}

func (s *Service) loadArtifact(id string) (runnerapi.Artifact, string, error) {
	if !safeIDPattern.MatchString(id) {
		return runnerapi.Artifact{}, "", os.ErrNotExist
	}
	directory := filepath.Join(s.config.StoragePath, "artifacts", id)
	metadata, err := os.ReadFile(filepath.Join(directory, metadataFile))
	if err != nil {
		return runnerapi.Artifact{}, "", err
	}
	var artifact runnerapi.Artifact
	if err = json.Unmarshal(metadata, &artifact); err != nil || artifact.ID != id {
		return runnerapi.Artifact{}, "", errors.New("invalid artifact metadata")
	}
	return artifact, filepath.Join(directory, "data"), nil
}

func (s *Service) cleanupExpired() {
	entries, _ := os.ReadDir(filepath.Join(s.config.StoragePath, "artifacts"))
	now := time.Now().UTC()
	for _, entry := range entries {
		artifact, _, err := s.loadArtifact(entry.Name())
		if err != nil || now.After(artifact.ExpiresAt) {
			_ = os.RemoveAll(filepath.Join(s.config.StoragePath, "artifacts", entry.Name()))
		}
	}
}

func (s *Service) cleanupContainers(ctx context.Context) {
	s.dockerMu.Lock()
	defer s.dockerMu.Unlock()
	list := exec.CommandContext(ctx, s.config.DockerBinary, "ps", "-aq", "--filter", "label=classroom.runner=true")
	output, err := list.Output()
	if err != nil {
		return
	}
	for _, id := range strings.Fields(string(output)) {
		_ = exec.CommandContext(ctx, s.config.DockerBinary, "rm", "-f", id).Run()
	}
}

type limitBuffer struct {
	data      []byte
	limit     int64
	truncated bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - int64(len(b.data))
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return original, nil
}

func (b *limitBuffer) String() string { return strings.ToValidUTF8(string(b.data), "�") }

func randomID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

func safeFileName(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "/\\") {
		return "", false
	}
	if value == "" || value == "." || value == ".." || len(value) > 180 || strings.ContainsAny(value, "\x00/\\") {
		return "", false
	}
	return value, true
}

func uniqueName(name string, used map[string]bool) string {
	if !used[name] {
		used[name] = true
		return name
	}
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(name, extension)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d%s", base, i, extension)
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

func copyFile(source, destination string, mode os.FileMode, limit int64) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(out, io.LimitReader(in, limit+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > limit {
		return errArtifactTooLarge
	}
	return nil
}

func mimeType(name string) string {
	if value := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); value != "" {
		return value
	}
	return "application/octet-stream"
}

func contentDisposition(name string) string {
	return `attachment; filename="download"; filename*=UTF-8''` + strings.ReplaceAll(urlEncode(name), "+", "%20")
}

func urlEncode(value string) string {
	var b strings.Builder
	for _, r := range []byte(value) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-._~", rune(r)) {
			b.WriteByte(r)
		} else {
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	return b.String()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
