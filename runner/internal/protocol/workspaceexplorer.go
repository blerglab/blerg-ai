package protocol

// Explorer budgets apply once per operation, including nested directories.
const (
	WorkspaceExplorerEntries    = 500
	WorkspaceExplorerReplyBytes = 1 << 20
	WorkspaceSearchDepth        = 32
	WorkspaceSearchVisited      = 10000
	WorkspaceSearchHits         = 200
)

// WorkspaceExplorerRequest is a launch-bound request, never an engine input.
// Generation identifies the prepared root binding, independently of
// immutable launch membership. Empty Path denotes that public root.
type WorkspaceExplorerRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	WorkspaceRuntimeRef
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Filter    string `json:"filter,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	MemberID  string `json:"member_id,omitempty"`
}

type WorkspaceEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"size,omitempty"`
	// Discovered repositories confer no launch membership or project authority.
	Repository bool   `json:"repository,omitempty"`
	MemberID   string `json:"member_id,omitempty"`
}

// WorkspacePathStatus is additive display metadata over the existing bounded Paths.
// Directory applies only to Git's collapsed untracked directories.
type WorkspacePathStatus struct {
	Path      string `json:"path"`
	Code      string `json:"code"`
	Directory bool   `json:"directory,omitempty"`
}

type WorkspaceStatus struct {
	MemberID  string                `json:"member_id,omitempty"`
	Path      string                `json:"path"`
	Branch    string                `json:"branch"`
	Changed   int                   `json:"changed"`
	Untracked int                   `json:"untracked"`
	Paths     []string              `json:"paths"`
	Partial   bool                  `json:"partial"`
	Files     []WorkspacePathStatus `json:"files,omitempty"`
}

type WorkspaceExplorerResult struct {
	UnavailableMembers []string `json:"unavailable_members,omitempty"`
	Type               string   `json:"type"`
	RequestID          string   `json:"request_id"`
	SessionID          string   `json:"session_id"`
	WorkspaceRuntimeRef
	Operation string           `json:"operation"`
	Path      string           `json:"path"`
	OK        bool             `json:"ok"`
	Entries   []WorkspaceEntry `json:"entries"`
	Status    *WorkspaceStatus `json:"status,omitempty"`
	Cursor    string           `json:"cursor,omitempty"`
	Truncated bool             `json:"truncated"`
	ErrorCode string           `json:"error_code,omitempty"`
}
