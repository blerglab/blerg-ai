package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// WorkspaceFinalization is the private completion transaction shared by P2 and
// its authenticated server adapter. Field encoding preserves P2 journal digests.
// Connection IDs are excluded so a fresh authenticated link can reconcile a
// lost receipt after the session ended. This is never a browser/agent operation.
type WorkspaceFinalization struct {
	Binding WorkspaceRuntimeBinding
	Message SessionEnded
	Results []WorkspaceMemberResult
}
type WorkspaceFinalReceipt struct{ Digest string }

func (f WorkspaceFinalization) Digest() string {
	raw, _ := json.Marshal(f)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// LocalWorkspaceControl is accepted only on the enrolled desktop connection.
// The server derives session owner and transport authority from persisted state.
type LocalWorkspaceControl struct {
	ParentSessionID string `json:"parent_session_id,omitempty"`

	Type      string                  `json:"type"`
	RequestID string                  `json:"request_id"`
	SessionID string                  `json:"session_id"`
	Operation string                  `json:"operation"`
	Token     string                  `json:"token,omitempty"`
	Launch    SupervisorLaunch        `json:"launch,omitzero"`
	Binding   WorkspaceRuntimeBinding `json:"binding,omitzero"`
	Manifest  *WorkspaceManifest      `json:"manifest,omitempty"`
	Results   []WorkspaceMemberResult `json:"results,omitempty"`
	Final     *WorkspaceFinalization  `json:"final,omitempty"`
}
type LocalWorkspaceControlResult struct {
	ParentSessionID string `json:"parent_session_id,omitempty"`

	Type      string                `json:"type"`
	RequestID string                `json:"request_id"`
	SessionID string                `json:"session_id"`
	OK        bool                  `json:"ok"`
	Launch    SupervisorLaunch      `json:"launch,omitzero"`
	Manifest  *WorkspaceManifest    `json:"manifest,omitempty"`
	Receipt   WorkspaceFinalReceipt `json:"receipt,omitzero"`
}
