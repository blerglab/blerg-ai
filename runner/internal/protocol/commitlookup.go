package protocol

import (
	"strings"
	"time"
)

// ReadCommit asks the session's daemon to read one commit from the session's own workspace (a
// sandboxed session: from the repository its clone was made from, where its commits are brought
// back). SHA is accepted only after ValidCommitLookupSHA. Patch asks for the diff;
// FilePath selects a validated literal blob path instead, with Original selecting
// the first parent. It never starts an engine.
type ReadCommit struct {
	BoardActivitySince string `json:"board_activity_since,omitempty"`
	BoardHistory       bool   `json:"board_history,omitempty"`
	DatasetMerges      bool   `json:"dataset_merges,omitempty"`
	Release            string `json:"release,omitempty"`
	ReleaseBase        string `json:"release_base,omitempty"`
	FilePath           string `json:"file_path,omitempty"`
	Original           bool   `json:"original,omitempty"`
	Graph              bool   `json:"graph,omitempty"`
	Depth              int    `json:"depth,omitempty"`
	WorkspaceGitRef
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	SHA       string `json:"sha"`
	Branch    string `json:"branch,omitempty"`
	Base      string `json:"base,omitempty"`
	Patch     bool   `json:"patch,omitempty"`
	// MergedChild, instead of a sha, asks whether a child session's recorded branch
	// is merged into the workspace branch. It must pass ValidChildBranchID.
	// ChildBranch is supplied by the server from the persisted session; older
	// peers omit it and use the legacy branch. The answer is Merged and TipDate.
	MergedChild string `json:"merged_child,omitempty"`
	ChildBranch string `json:"child_branch,omitempty"`
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
// failed). PatchB64 carries diff or file bytes as base64; File marks a blob response.
type MergeHistory struct {
	WorkspaceGitRef
	ObservedAt time.Time     `json:"observed_at,omitempty"`
	Tip        string        `json:"tip"`
	Commits    []GraphCommit `json:"commits"`
}

type CommitResult struct {
	CodeHistory *CodeHistory  `json:"code_history,omitempty"`
	Merges      *MergeHistory `json:"merges,omitempty"`
	File        bool          `json:"file,omitempty"`
	Graph       *BranchGraph  `json:"graph,omitempty"`
	WorkspaceGitRef
	Type      string      `json:"type"`
	RequestID string      `json:"request_id"`
	SessionID string      `json:"session_id"`
	OK        bool        `json:"ok"`
	Commit    *CommitInfo `json:"commit,omitempty"`
	Branch    *BranchInfo `json:"branch,omitempty"`
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

// BranchInfo is a bounded local branch comparison. No provider API is involved.
type BranchInfo struct {
	Name    string         `json:"branch"`
	Base    string         `json:"base"`
	Commits []BranchCommit `json:"commits"`
}
type BranchCommit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

// BranchGraph is a bounded snapshot of local branch history, with no checkout state.
type BranchGraph struct {
	WorkspaceGitRef
	Commits       []GraphCommit `json:"commits"`
	Refs          []GraphRef    `json:"refs"`
	More          bool          `json:"more"`
	DefaultBranch string        `json:"default_branch,omitempty"`
	RefsLimited   bool          `json:"refs_limited,omitempty"`
	Tags          []GraphTag    `json:"tags"`
	TagsLimited   bool          `json:"tags_limited,omitempty"`
}
type GraphTag struct {
	GraphRef
	Contains []string `json:"contains"`
	Previous string   `json:"previous,omitempty"`
	Complete bool     `json:"complete"`
}
type GraphCommit struct {
	Date    string   `json:"date,omitempty"`
	SHA     string   `json:"sha"`
	Parents []string `json:"parents"`
	Subject string   `json:"subject"`
}
type GraphRef struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

// ValidCommitFilePath accepts a literal repository-relative blob path, never a revision.
func ValidCommitFilePath(path string) bool {
	if path == "" || len(path) > CommitPathMaxBytes || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// CodeHistory is a complete, bounded reachable-main snapshot for board reconciliation.
// Commit prose remains data; explicit merge titles and Git revert trailers drive moves. Merge-body card
// numbers only suppress unsupported historical-completion warnings.
type CodeHistory struct {
	Activity         []CodeActivity `json:"activity,omitempty"`
	ActivityComplete bool           `json:"activity_complete,omitempty"`
	Tip              string         `json:"tip"`
	Commits          []CodeCommit   `json:"commits"`
}
type CodeCommit struct {
	BodyCards []int    `json:"body_cards,omitempty"`
	SHA       string   `json:"sha"`
	Date      string   `json:"date"`
	Subject   string   `json:"subject"`
	Parents   []string `json:"parents"`
	Reverts   string   `json:"reverts,omitempty"`
	Deployed  bool     `json:"deployed,omitempty"`
}

// CodeActivity is an activity hint, never main-merge or deployment proof.
type CodeActivity struct {
	Subject string `json:"subject"`
	Date    string `json:"date"`
}
