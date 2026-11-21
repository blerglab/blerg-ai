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
	// Membership carries a repository-change report (operations membership_*).
	Membership *WorkspaceMembershipReport `json:"membership,omitempty"`
}
type LocalWorkspaceControlResult struct {
	Binding         WorkspaceRuntimeBinding `json:"binding,omitzero"`
	ShellEligible   bool                    `json:"shell_eligible,omitempty"`
	ParentSessionID string                  `json:"parent_session_id,omitempty"`

	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	OK        bool   `json:"ok"`
	// Cause is a safe typed diagnostic for every failed reply, including transient failures.
	Cause WorkspaceControlCause `json:"cause,omitempty"`
	// Refused, on a failed reply, is the server's definitive reason that this launch has no
	// authority any more (the session ended or a newer launch replaced it). Empty means the failure
	// may be transient: a database error, a connection not yet recovered, or an older server.
	Refused  string                `json:"refused,omitempty"`
	Launch   SupervisorLaunch      `json:"launch,omitzero"`
	Manifest *WorkspaceManifest    `json:"manifest,omitempty"`
	Receipt  WorkspaceFinalReceipt `json:"receipt,omitzero"`
	// MembershipPhase is the server's journal phase for a membership_status call.
	MembershipPhase string `json:"membership_phase,omitempty"`
}

// WorkspaceControlCause is a safe diagnostic label, independent of Refused:
// connection recovery and storage failures must remain retryable.
type WorkspaceControlCause string

const (
	WorkspaceControlSessionUnknown      WorkspaceControlCause = "session_unknown"
	WorkspaceControlOwnerChanged        WorkspaceControlCause = "session_owner_changed"
	WorkspaceControlSessionDiscarded    WorkspaceControlCause = "session_discarded"
	WorkspaceControlLaunchMissing       WorkspaceControlCause = "launch_missing"
	WorkspaceControlLaunchReplaced      WorkspaceControlCause = "launch_replaced"
	WorkspaceControlRuntimeReplaced     WorkspaceControlCause = "runtime_replaced"
	WorkspaceControlSessionCompleted    WorkspaceControlCause = "session_completed"
	WorkspaceControlSessionStopping     WorkspaceControlCause = "session_stopping"
	WorkspaceControlLaunchConflict      WorkspaceControlCause = "launch_conflict"
	WorkspaceControlManifestMismatch    WorkspaceControlCause = "manifest_mismatch"
	WorkspaceControlChildAuthority      WorkspaceControlCause = "child_authority_mismatch"
	WorkspaceControlManifest            WorkspaceControlCause = "manifest_unavailable"
	WorkspaceControlUnavailable         WorkspaceControlCause = "authority_unavailable"
	WorkspaceControlConnectionRecovery  WorkspaceControlCause = "connection_recovery_required"
	WorkspaceControlRuntimeUnauthorized WorkspaceControlCause = "runtime_not_authorized"
	WorkspaceControlRefused             WorkspaceControlCause = "launch_refused"
	WorkspaceControlStorage             WorkspaceControlCause = "storage_unavailable"
	WorkspaceControlDeadline            WorkspaceControlCause = "deadline"
	WorkspaceControlInvalid             WorkspaceControlCause = "invalid_request"
	WorkspaceControlDesktop             WorkspaceControlCause = "desktop_authority_changed"
)
