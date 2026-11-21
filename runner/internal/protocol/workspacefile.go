package protocol

// ReadWorkspaceFile asks the current session daemon to read one authorized workspace file.
// It never launches or resumes an engine.
type ReadWorkspaceFile struct {
	WorkspaceRuntimeRef
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
}

// WorkspaceFileResult is a bounded, owner-only request reply, never a transcript event.
type WorkspaceFileResult struct {
	WorkspaceRuntimeRef
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
	// RequestedPath binds an absolute reference to the normalized relative Path.
	RequestedPath string `json:"requested_path,omitempty"`
	// Chunks is how many workspace_file_chunk frames came before this one and
	// carry ContentB64 instead of it. Zero means the content is inline.
	Chunks int `json:"chunks,omitempty"`
}

// WorkspaceFileChunkBytes bounds the base64 text in one chunk, so a file reply
// holds the daemon's socket for one small frame at a time.
const WorkspaceFileChunkBytes = 128 << 10

// WorkspaceFileChunk carries part of a reply's ContentB64, in order. It is sent
// only to a server that set DaemonWelcome.WorkspaceFileChunks; the reply's
// workspace_file_result comes last, with Chunks set and no content.
type WorkspaceFileChunk struct {
	WorkspaceRuntimeRef
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	Seq       int    `json:"seq"`
	Data      string `json:"data"`
}
