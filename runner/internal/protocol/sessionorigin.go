package protocol

// ReadSessionOrigin reads only HEAD from an existing workspace; it never starts an engine.
type ReadSessionOrigin struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
}

// SessionOriginResult carries no workspace path, credential, command or git diagnostics.
// An empty successful commit means no verifiable HEAD (non-repository or unborn branch).
type SessionOriginResult struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	OK        bool   `json:"ok"`
	Commit    string `json:"commit"`
}

func ValidSessionOriginCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
