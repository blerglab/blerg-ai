package protocol

// WorkspaceStorageRequest is sent only on the owner's desktop control link.
// Confirm binds the person's affirmative action to the exact preview digest.
type WorkspaceStorageRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	OwnerID   string `json:"owner_id"`
	Operation string `json:"operation"`
	Confirm   string `json:"confirm,omitempty"`
}
type WorkspaceStoragePreview struct {
	FileCount   int      `json:"file_count"`
	CommitCount int      `json:"commit_count"`
	Truncated   bool     `json:"truncated"`
	Digest      string   `json:"digest"`
	Files       []string `json:"files"`
	Commits     []string `json:"commits"`
	Released    bool     `json:"released"`
}
type WorkspaceStorageResult struct {
	Type      string                   `json:"type"`
	RequestID string                   `json:"request_id"`
	SessionID string                   `json:"session_id"`
	Preview   *WorkspaceStoragePreview `json:"preview,omitempty"`
	Error     string                   `json:"error,omitempty"`
}
