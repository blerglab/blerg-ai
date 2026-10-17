package protocol

// ReadWorkspaceFile asks the current session daemon to read one relative workspace file.
// It never launches or resumes an engine.
type ReadWorkspaceFile struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
}

// WorkspaceFileResult is a bounded, owner-only request reply, never a transcript event.
type WorkspaceFileResult struct {
	Type        string `json:"type"`
	RequestID   string `json:"request_id"`
	SessionID   string `json:"session_id"`
	OK          bool   `json:"ok"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	View        string `json:"view"`
	ContentType string `json:"content_type"`
	Truncated   bool   `json:"truncated"`
	ContentB64  string `json:"content_b64"`
	Mtime       string `json:"mtime"`
	ErrorCode   string `json:"error_code,omitempty"`
	Error       string `json:"error,omitempty"`
}
