package protocol

// AttachmentCleanupRequest asks about a bounded set of old private folders.
// It is accepted only from a host daemon, never from a session pod.
type AttachmentCleanupRequest struct {
	Type       string   `json:"type"`
	RequestID  string   `json:"request_id"`
	SessionIDs []string `json:"session_ids"`
}

// AttachmentCleanupResult lists only ended/discarded or nonexistent sessions.
// Errors and sessions owned by a different daemon are omitted (retain storage).
type AttachmentCleanupResult struct {
	Type      string   `json:"type"`
	RequestID string   `json:"request_id"`
	Removable []string `json:"removable"`
}
