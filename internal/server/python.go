package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"classroom-agent/internal/store"
	"classroom-agent/internal/tools"
)

const maxPythonInputArtifacts = 5

func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	conversationID := strings.TrimSpace(r.URL.Query().Get("conversation_id"))
	if conversationID != "" {
		if _, err := s.Store.Conversation(r.Context(), p.Session.RunID, p.Session.StudentID, conversationID); err != nil {
			writeError(w, http.StatusNotFound, "CONVERSATION_NOT_FOUND", "未找到该对话")
			return
		}
	}
	items, err := s.Store.Artifacts(r.Context(), p.Session.RunID, p.Session.StudentID, conversationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取文件失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": items})
}

func (s *Server) uploadArtifact(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if !s.pythonAllowed(r.Context(), p, w) {
		return
	}
	if err := r.ParseMultipartForm(s.Config.MaxArtifactBytes + (1 << 20)); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_UPLOAD", "文件格式不正确或超过大小限制")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	conversationID := strings.TrimSpace(r.FormValue("conversation_id"))
	if conversationID == "" {
		writeError(w, http.StatusBadRequest, "CONVERSATION_REQUIRED", "请选择一个对话")
		return
	}
	if _, err := s.Store.Conversation(r.Context(), p.Session.RunID, p.Session.StudentID, conversationID); err != nil {
		writeError(w, http.StatusNotFound, "CONVERSATION_NOT_FOUND", "未找到该对话")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "FILE_REQUIRED", "请选择文件")
		return
	}
	defer file.Close()
	if header.Size > s.Config.MaxArtifactBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "ARTIFACT_TOO_LARGE", "文件超过大小限制")
		return
	}
	uploadCtx, cancel := context.WithTimeout(r.Context(), s.Config.RunnerTimeout)
	defer cancel()
	remote, err := s.Runner.Upload(uploadCtx, header.Filename, io.LimitReader(file, s.Config.MaxArtifactBytes+1))
	if err != nil {
		writeError(w, http.StatusBadGateway, "RUNNER_UNAVAILABLE", "Python Runner 暂不可用")
		return
	}
	artifact, err := s.Store.CreateArtifact(r.Context(), store.Artifact{
		ID: newID("artifact_"), RunnerArtifactID: remote.ID, RunID: p.Session.RunID,
		StudentID: p.Session.StudentID, ConversationID: conversationID, Name: remote.Name,
		MIMEType: remote.MIMEType, Size: remote.Size, SHA256: remote.SHA256, ExpiresAt: remote.ExpiresAt,
	})
	if err != nil {
		_ = s.Runner.Delete(context.Background(), remote.ID)
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "保存文件索引失败")
		return
	}
	writeJSON(w, http.StatusCreated, artifact)
}

func (s *Server) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	artifact, err := s.Store.Artifact(r.Context(), p.Session.RunID, p.Session.StudentID, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && time.Now().UTC().After(artifact.ExpiresAt)) {
		writeError(w, http.StatusNotFound, "ARTIFACT_NOT_FOUND", "文件已清理或不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取文件失败")
		return
	}
	if s.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "RUNNER_UNAVAILABLE", "Python Runner 暂不可用")
		return
	}
	response, err := s.Runner.Download(r.Context(), artifact.RunnerArtifactID)
	if err != nil {
		if strings.Contains(err.Error(), "ARTIFACT_NOT_FOUND") {
			_ = s.Store.DeleteArtifact(r.Context(), p.Session.RunID, p.Session.StudentID, artifact.ID)
			writeError(w, http.StatusNotFound, "ARTIFACT_NOT_FOUND", "文件已清理或不存在")
			return
		}
		writeError(w, http.StatusBadGateway, "RUNNER_UNAVAILABLE", "Python Runner 暂不可用")
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", artifact.MIMEType)
	w.Header().Set("Content-Length", fmt.Sprint(artifact.Size))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	disposition := "attachment"
	if strings.HasPrefix(artifact.MIMEType, "image/png") || strings.HasPrefix(artifact.MIMEType, "image/jpeg") {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": artifact.Name}))
	_, _ = io.Copy(w, io.LimitReader(response.Body, s.Config.MaxArtifactBytes))
}

func (s *Server) runPython(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if !s.pythonAllowed(r.Context(), p, w) {
		return
	}
	var input struct {
		ConversationID   string   `json:"conversation_id"`
		Code             string   `json:"code"`
		Stdin            string   `json:"stdin"`
		InputArtifactIDs []string `json:"input_artifact_ids"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ConversationID = strings.TrimSpace(input.ConversationID)
	if _, err := s.Store.Conversation(r.Context(), p.Session.RunID, p.Session.StudentID, input.ConversationID); err != nil {
		writeError(w, http.StatusNotFound, "CONVERSATION_NOT_FOUND", "未找到该对话")
		return
	}
	if len(input.InputArtifactIDs) > maxPythonInputArtifacts {
		writeError(w, http.StatusBadRequest, "TOO_MANY_FILES", "输入文件最多 5 个")
		return
	}
	tool, ok := s.Agent.Tools.Get("python_execute")
	python, ok := tool.(*tools.PythonExecute)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "RUNNER_UNAVAILABLE", "Python Runner 暂不可用")
		return
	}
	scope := tools.ExecutionScope{RunID: p.Session.RunID, StudentID: p.Session.StudentID, ConversationID: input.ConversationID, TurnID: newID("manual_")}
	response, artifacts, err := python.Run(tools.WithExecutionScope(r.Context(), scope), input.Code, input.Stdin, input.InputArtifactIDs)
	if err != nil {
		if errors.Is(err, tools.ErrInvalidInput) {
			writeError(w, http.StatusBadRequest, "INVALID_PYTHON", err.Error())
		} else {
			writeError(w, http.StatusBadGateway, "RUNNER_UNAVAILABLE", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"execution_id": response.ExecutionID, "status": response.Status, "exit_code": response.ExitCode,
		"stdout": response.Stdout, "stderr": response.Stderr, "duration_ms": response.DurationMS,
		"truncated": response.Truncated, "artifacts": artifacts,
	})
}

func (s *Server) pythonAllowed(ctx context.Context, p principal, w http.ResponseWriter) bool {
	if s.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "RUNNER_UNAVAILABLE", "Python Runner 暂不可用")
		return false
	}
	if !s.studentCanMutate(ctx, p, w) {
		return false
	}
	policy, err := s.Store.RunPolicy(ctx, p.Session.RunID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取课堂能力失败")
		return false
	}
	for _, name := range policy.AllowedTools {
		if name == "python_execute" {
			return true
		}
	}
	writeError(w, http.StatusForbidden, "TOOL_DISABLED", "老师暂未开放 Python")
	return false
}
