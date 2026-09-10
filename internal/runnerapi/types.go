package runnerapi

import "time"

type Artifact struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	MIMEType  string    `json:"mime_type"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	ExpiresAt time.Time `json:"expires_at"`
}

type ExecuteRequest struct {
	RequestID        string   `json:"request_id"`
	Code             string   `json:"code"`
	Stdin            string   `json:"stdin,omitempty"`
	InputArtifactIDs []string `json:"input_artifact_ids,omitempty"`
}

type ExecuteResponse struct {
	ExecutionID string     `json:"execution_id"`
	Status      string     `json:"status"`
	ExitCode    int        `json:"exit_code"`
	Stdout      string     `json:"stdout"`
	Stderr      string     `json:"stderr"`
	DurationMS  int64      `json:"duration_ms"`
	Queued      bool       `json:"queued,omitempty"`
	QueueWaitMS int64      `json:"queue_wait_ms,omitempty"`
	Truncated   bool       `json:"truncated"`
	Artifacts   []Artifact `json:"artifacts,omitempty"`
}

type ErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
