package protocol

// RepositoryRequest is the owner's pending decision, read from the membership journal.
type RepositoryRequest struct {
	Refusal    string `json:"refusal,omitempty"`
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	MemberName string `json:"member_name"`
	Digest     string `json:"proposed_digest"`
}

// WorkspaceMembershipVersion is negotiated at each runtime hop. An old peer has
// no affirmative support and every membership change is refused for it.
const WorkspaceMembershipVersion = 1

// Waiting and refusal codes a runtime reports. Waiting leaves the authorized
// change pending; a refusal closes it. Person-facing text is the server's.
const (
	MembershipWaitTurn   = "turn"
	MembershipWaitShell  = "shell"
	MembershipWaitKeeper = "keeper"
	// MembershipWaitKeeperIncompatible: the keeper runs a protocol this daemon cannot speak.
	MembershipWaitKeeperIncompatible = "keeper_incompatible"
	MembershipWaitRuntime            = "runtime"
	MembershipWaitFence              = "fence"
	MembershipWaitBusy               = "busy"
	MembershipWaitInstall            = "install" // committed; the runtime has not reopened yet

	MembershipRefuseSource    = "source"    // source replaced, moved or no longer a repository
	MembershipRefuseCollision = "collision" // the member path is already taken in the workspace
	MembershipRefuseQuota     = "quota"     // aggregate budget or quota exhausted
	MembershipRefuseWork      = "work"      // removal found unfinished or undelivered work
	MembershipRefuseLocal     = "local"     // removal would delete files Git ignores
	MembershipRefuseAnchor    = "anchor"    // a chat works in this repository
	MembershipRefusePrepare   = "prepare"   // preparation failed and was rolled back
)

// WorkspaceMembershipApply asks the runtime to apply one authorized change at a
// safe turn boundary. Repeating it is safe: every step reconciles its journal.
type WorkspaceMembershipApply struct {
	Type        string            `json:"type"`
	SessionID   string            `json:"session_id"`
	OperationID string            `json:"operation_id"`
	Kind        string            `json:"kind"`
	MemberID    string            `json:"member_id"`
	Proposed    WorkspaceManifest `json:"proposed"`
	// OldDigest is the revision this change was authorized against.
	OldDigest string `json:"old_digest"`
	// Source selects an addition beneath the configured root. The runtime resolves
	// and pins it again; it never trusts a path it cannot re-prove.
	Source SelectionRef `json:"source,omitzero"`
	// DiscardLocal is the digest of the ignored files the person agreed to lose.
	// Files that no longer match it refuse the removal again.
	DiscardLocal string `json:"discard_local,omitempty"`
}

// MaxWorkspaceLocalNames bounds the names a refusal carries to the person's row.
const MaxWorkspaceLocalNames = 20

// WorkspaceLocalFiles describes what a removal could not prove delivered: files
// Git ignores in a checkout that would be deleted. Count covers every entry; Names
// shows the first few, and Digest binds them with their sizes and times.
type WorkspaceLocalFiles struct {
	Names  []string `json:"names,omitempty"`
	Count  int      `json:"count,omitempty"`
	Digest string   `json:"digest,omitempty"`
}

// WorkspaceMembershipReport travels on the authenticated workspace control call.
// Its fields never decide authority; the server compares them to its journal.
type WorkspaceMembershipReport struct {
	OperationID string `json:"operation_id"`
	Receipt     string `json:"receipt,omitempty"`
	Waiting     string `json:"waiting,omitempty"`
	Refusal     string `json:"refusal,omitempty"`
	// Local accompanies MembershipRefuseLocal.
	Local *WorkspaceLocalFiles `json:"local,omitempty"`
}

// MemberPath is the public projection of an addition, or "" for a named folder.
// DiscoverySource is otherwise unchanged by an addition resolve.
type DiscoveryAddition struct {
	Source DiscoverySource `json:"source"`
	// PhysicalPath is set when the workspace is a physical folder: the repository
	// keeps its real relative path there, and "." adopts the folder itself.
	PhysicalPath string `json:"physical_path,omitempty"`
}
