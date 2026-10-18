package protocol

// ResolveProgressOrigin asks the owning daemon to bind CLI progress to a live
// native shell call or thread. Callers never supply a persisted display identity.
type ResolveProgressOrigin struct {
	Type           string `json:"type"`
	RequestID      string `json:"request_id"`
	SessionID      string `json:"session_id"`
	CallID         string `json:"call_id,omitempty"`
	NativeThreadID string `json:"native_thread_id,omitempty"`
}

type ProgressOriginResult struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	SessionID    string `json:"session_id"`
	OK           bool   `json:"ok"`
	ParentCallID string `json:"parent_call_id,omitempty"`
	ChildID      string `json:"child_id,omitempty"`
}
