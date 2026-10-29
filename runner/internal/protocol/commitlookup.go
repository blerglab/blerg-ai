package protocol

// ReadCommit asks the session's daemon to read one commit from the session's own workspace (a
// sandboxed session: from the repository its clone was made from, where its commits are brought
// back). SHA is the only value from the request that reaches git, and only after
// ValidCommitLookupSHA accepts it. Patch asks for the commit's diff instead of its summary. It never
// starts an engine.
type ReadCommit struct {
	WorkspaceGitRef
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	SHA       string `json:"sha"`
	Patch     bool   `json:"patch,omitempty"`
	// MergedChild, instead of a sha, asks whether the branch wip/<MergedChild> (a child session's
	// work, brought back into this session's repository) is merged into the branch the workspace
	// has checked out (HEAD). It must be a session id (ValidChildBranchID), so the only ref that
	// reaches git is refs/heads/wip/<uuid>. The answer is CommitResult.Merged and TipDate.
	MergedChild string `json:"merged_child,omitempty"`
	// The server supplies the persisted child's exact member launch and base.
	ChildLaunchGeneration string `json:"child_launch_generation,omitempty"`
	ChildBase             string `json:"child_base,omitempty"`
}

// ValidChildBranchID accepts a lower-case UUID and nothing else.
func ValidChildBranchID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}

// CommitFile is one file a commit changed, with its added and deleted line counts (both 0 and
// Binary true for a binary file). OldPath is set for a rename.
type CommitFile struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Adds    int    `json:"adds"`
	Dels    int    `json:"dels"`
	Binary  bool   `json:"binary,omitempty"`
}

// CommitInfo is a commit's summary, every field bounded by the daemon (CommitLookupLimits).
type CommitInfo struct {
	WorkspaceGitRef
	SHA            string       `json:"sha"`
	Short          string       `json:"short"`
	Subject        string       `json:"subject"`
	Body           string       `json:"body"`
	BodyTruncated  bool         `json:"body_truncated,omitempty"`
	Author         string       `json:"author"`
	AuthorDate     string       `json:"author_date"`
	Parents        []string     `json:"parents"`
	Files          []CommitFile `json:"files"`
	FilesTruncated bool         `json:"files_truncated,omitempty"`
	Adds           int          `json:"adds"`
	Dels           int          `json:"dels"`
}

// CommitResult answers ReadCommit. It carries no workspace path and no git diagnostics. ErrorCode:
// "invalid" (not a sha), "not_found" (no such commit in the workspace), "too_large" (the diff is
// over CommitPatchMaxBytes), "busy", or "unavailable" (no live workspace, no repository, git
// failed). PatchB64 is the diff's bytes, base64 (a diff need not be UTF-8).
type CommitResult struct {
	WorkspaceGitRef
	Type      string      `json:"type"`
	RequestID string      `json:"request_id"`
	SessionID string      `json:"session_id"`
	OK        bool        `json:"ok"`
	Commit    *CommitInfo `json:"commit,omitempty"`
	PatchB64  string      `json:"patch_b64,omitempty"`
	ErrorCode string      `json:"error_code,omitempty"`
	// Merged answers a MergedChild read: the child's branch tip is an ancestor of HEAD. TipDate is
	// the tip's committer date (RFC 3339), so the server can tell work merged from a branch that
	// never moved. ErrorCode "not_found": no such branch.
	Merged  bool   `json:"merged,omitempty"`
	TipDate string `json:"tip_date,omitempty"`
}

// The bounds of a commit read, shared by the daemon (which enforces them) and the server (which
// refuses a reply over them).
const (
	CommitPatchMaxBytes   = 2 << 20
	CommitSubjectMaxBytes = 512
	CommitBodyMaxBytes    = 4 << 10
	CommitAuthorMaxBytes  = 256
	CommitPathMaxBytes    = 1024
	CommitFilesMax        = 300
	CommitParentsMax      = 16
)

// ValidCommitLookupSHA accepts 7 to 64 lowercase hex digits and nothing else, so the value can
// never read as an option, a range, a path or a revision expression.
func ValidCommitLookupSHA(value string) bool {
	if len(value) < 7 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
