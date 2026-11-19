package server

// Commit lookup: what a commit chip's hovercard shows, and the commit's diff a click opens, read
// on demand from the session's own workspace through the daemon that runs it (read_commit; the
// daemon side is internal/daemon/commitlookup.go).
//
// Who may read: the exact session and desktop owner for desktop workspaces, or
// the existing session owner/administrator policy for cluster workspaces; only a person. The
// workspace-file reader's rule (a commit by sha reaches any history in the workspace's repository,
// so it is filesystem access, not transcript access). Someone else's private session answers the
// uniform 404 (an administrator's too); anyone else gets 403; an agent, service or session token
// is refused. All of it is decided before any daemon is asked. A session that is not
// live (ended, stopped, failed, disconnected) has no workspace to read: 409 before any daemon is
// asked. The daemon answers from the workspace the live launch is bound to; nothing from the
// request but the validated sha reaches it.
//
// Bounds: at most commitPendingMax reads wait on all daemons, commitPendingPerDaemon on one,
// commitPendingPerSession on one session and commitPendingPerAccount for one person (over: 429); the daemon caps its own concurrency and every
// field it returns, and a reply over those caps is refused here. Nothing is cached on the server:
// the browser keeps what it read (a commit does not change), and every read is authorized afresh.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workbranch"
)

const (
	commitPendingMax        = 16
	commitPendingPerDaemon  = 4
	commitPendingPerSession = 2
	commitPendingPerAccount = 4
	// commitResultMaxFrame is the largest commit_result frame read from a daemon: a diff at its cap,
	// base64, and room for the envelope.
	commitResultMaxFrame = (protocol.CommitPatchMaxBytes+2)/3*4 + (64 << 10)
)

// The longest a read waits for its daemon (a summary, a diff).
var (
	commitLookupTimeout = 7 * time.Second
	commitDiffTimeout   = 12 * time.Second
)

var (
	errCommitUnavailable = errors.New("commit lookup unavailable")
	errCommitBusy        = errors.New("commit lookup busy")
)

// Codes are fixed by the caller; wrapped errors retain errors.Is behavior.
type commitLookupFailure struct {
	code string
	err  error
}

func (e *commitLookupFailure) Error() string          { return e.code }
func (e *commitLookupFailure) Unwrap() error          { return e.err }
func commitLookupFailed(code string, err error) error { return &commitLookupFailure{code, err} }

type commitWaiter struct {
	graph     bool
	ref       protocol.WorkspaceGitRef
	dc        *DaemonConn
	sessionID string
	account   string
	ch        chan protocol.CommitResult
}
type commitRequests struct {
	mu      sync.Mutex
	pending map[string]commitWaiter
}

func (h *Hub) requestCommit(ctx context.Context, dc *DaemonConn, account, sid, sha string, patch bool, timeout time.Duration) (protocol.CommitResult, error) {
	return h.requestCommitRead(ctx, dc, account, protocol.ReadCommit{SessionID: sid, SHA: sha, Patch: patch}, timeout)
}

// requestCommitRead sends one read_commit (a commit, its diff, or whether a child's branch is
// merged) under the same bounds and waits for its answer.
func (h *Hub) requestCommitRead(ctx context.Context, dc *DaemonConn, account string, req protocol.ReadCommit, timeout time.Duration) (protocol.CommitResult, error) {
	sid := req.SessionID
	ref, err := h.qualifyWorkspaceGit(ctx, sid, req.WorkspaceGitRef)
	if err != nil {
		return protocol.CommitResult{}, err
	}
	req.WorkspaceGitRef = ref
	id := newUUID()
	w := commitWaiter{graph: req.Graph, ref: ref, dc: dc, sessionID: sid, account: account, ch: make(chan protocol.CommitResult, 1)}
	r := &h.commitReqs
	r.mu.Lock()
	perDaemon, perSession, perAccount, graphs := 0, 0, 0, 0
	for _, p := range r.pending {
		if p.account == account {
			perAccount++
		}
		if p.graph && (p.dc.ID == dc.ID || p.account == account) {
			graphs++
		}
		if p.dc.ID == dc.ID {
			perDaemon++
		}
		if p.sessionID == sid {
			perSession++
		}
	}
	// One graph per desktop or person leaves slots for plan sync and commit diffs.
	if req.Graph && graphs >= 1 || len(r.pending) >= commitPendingMax || perDaemon >= commitPendingPerDaemon || perSession >= commitPendingPerSession || perAccount >= commitPendingPerAccount {
		r.mu.Unlock()
		return protocol.CommitResult{}, commitLookupFailed("commit_lookup_busy", errCommitBusy)
	}
	if r.pending == nil {
		r.pending = make(map[string]commitWaiter)
	}
	r.pending[id] = w
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock() }()
	req.Type, req.RequestID = "read_commit", id
	raw, _ := json.Marshal(req)
	if h.FindDaemonForSession(sid) != dc || !h.trySendToDaemon(dc, raw) {
		return protocol.CommitResult{}, commitLookupFailed("commit_dispatch_unavailable", errCommitUnavailable)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res, ok := <-w.ch:
		if !ok || h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(sid) != dc {
			return protocol.CommitResult{}, commitLookupFailed("commit_connection_changed", errCommitUnavailable)
		}
		return res, nil
	case <-timer.C:
		return protocol.CommitResult{}, commitLookupFailed("commit_reply_timeout", errCommitUnavailable)
	case <-ctx.Done():
		return protocol.CommitResult{}, ctx.Err()
	}
}

// deliverCommitResult hands a daemon's reply to the request waiting for it: only from the daemon
// that was asked, for the session it was asked about, once.
func (h *Hub) deliverCommitResult(dc *DaemonConn, res protocol.CommitResult) {
	if h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(res.SessionID) != dc || (dc.BoundSessionID != "" && dc.BoundSessionID != res.SessionID) {
		return
	}
	r := &h.commitReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.pending[res.RequestID]
	if !ok || w.dc != dc || w.sessionID != res.SessionID || w.ref != res.WorkspaceGitRef {
		return
	}
	delete(r.pending, res.RequestID)
	w.ch <- res
}

func (h *Hub) abandonCommitRequests(dc *DaemonConn) {
	r := &h.commitReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.pending {
		if w.dc == dc {
			delete(r.pending, id)
			close(w.ch)
		}
	}
}

func (a *API) RegisterCommitRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{id}/commits/{sha}/file", func(w http.ResponseWriter, r *http.Request) { a.handleCommit(w, r, true) })
	mux.HandleFunc("GET /api/sessions/{id}/git/graph", func(w http.ResponseWriter, r *http.Request) { a.handleCommit(w, r, false) })
	mux.HandleFunc("GET /api/sessions/{id}/git", func(w http.ResponseWriter, r *http.Request) {
		a.handleCommit(w, r, false)
	})
	mux.HandleFunc("GET /api/sessions/{id}/git/diff", func(w http.ResponseWriter, r *http.Request) {
		a.handleCommit(w, r, true)
	})
	mux.HandleFunc("GET /api/sessions/{id}/commits/{sha}", func(w http.ResponseWriter, r *http.Request) { a.handleCommit(w, r, false) })
	mux.HandleFunc("GET /api/sessions/{id}/commits/{sha}/diff", func(w http.ResponseWriter, r *http.Request) { a.handleCommit(w, r, true) })
}

func commitProblem(w http.ResponseWriter, status int, message string) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, map[string]string{"error": message, "error_id": newUUID()})
}

func (a *API) handleCommit(w http.ResponseWriter, r *http.Request, patch bool) {
	graphRead := r.Pattern == "GET /api/sessions/{id}/git/graph"
	branchRead := graphRead || r.Pattern == "GET /api/sessions/{id}/git" || r.Pattern == "GET /api/sessions/{id}/git/diff"
	var p identity.Principal
	var ok bool
	// The board brokers only the current signed-in person's proof. Its stored
	// automation credential never grants access to workspace Git.
	if branchRead && r.Header.Get(desktopViewerHeader) != "" {
		if a.boardDesktopViewer(r) == "" {
			commitProblem(w, 403, "Sign in to read workspace Git.")
			return
		}
		p, _ = identity.Verify(r.Header.Get(desktopViewerHeader), "blerg-board", a.coreAuth.KeySet(), a.coreAuth, coreAuthSensitiveCaps)
		ok = p.Kind == "human"
	} else {
		p, ok = a.authBrowser(w, r)
	}
	if !ok {
		return
	}
	if p.Kind != "human" {
		commitProblem(w, http.StatusForbidden, "Commits can only be read by a signed-in person.")
		return
	}
	if a.dbPool == nil || a.hub == nil {
		commitProblem(w, http.StatusConflict, "The session's workspace is unavailable.")
		return
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		commitProblem(w, http.StatusNotFound, "Session not found.")
		return
	}
	timeout := commitLookupTimeout
	if patch {
		timeout = commitDiffTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout+time.Second)
	defer cancel()
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		commitProblem(w, http.StatusConflict, "The session's workspace is unavailable.")
		return
	}
	// The workspace-file reader's rule: someone else's private session does not exist for this
	// person, an administrator included; otherwise the owner or an administrator only.
	if row == nil || (row.Private && !ownsSession(p.Sub, row)) {
		commitProblem(w, http.StatusNotFound, "Session not found.")
		return
	}
	if !ownsSession(p.Sub, row) && (graphRead || !p.Has(insightsAdminCap)) {
		commitProblem(w, http.StatusForbidden, "Only the session owner or an administrator can read its commits.")
		return
	}
	sha := r.PathValue("sha")
	if !branchRead && !protocol.ValidCommitLookupSHA(sha) {
		commitProblem(w, http.StatusNotFound, "Commit not found.")
		return
	}
	if terminalSessionStatus(row.Status) || row.Status == "disconnected" {
		commitProblem(w, http.StatusConflict, "The session's workspace is no longer open.")
		return
	}
	// The daemon that runs the session now: a host daemon, or a cluster session's own pod (bound to it).
	dc := a.hub.FindDaemonForSession(sid)
	if dc == nil || (dc.BoundSessionID != "" && dc.BoundSessionID != sid) {
		commitProblem(w, http.StatusConflict, "The session's daemon is not connected.")
		return
	}
	if derefOrEmpty(row.Runtime) != "cluster" && (!ownsSession(p.Sub, row) || a.hub.ownedHostDaemon(ctx, p.Sub, dc.ID) != dc) {
		commitProblem(w, 403, "Only the desktop owner can read its commits.")
		return
	}
	if branchRead && r.URL.Query().Get("member_id") == "" {
		manifest, generation, err := loadExplorerBinding(ctx, a.dbPool, sid)
		if err == nil && len(manifest.Members) > 1 {
			if patch {
				commitProblem(w, 422, "Choose a workspace repository before reading its diff.")
				return
			}
			members := []map[string]string{}
			for _, m := range manifest.Members {
				members = append(members, map[string]string{"id": m.ID, "name": m.Name})
			}
			writeJSON(w, 200, map[string]any{"members": members, "generation": generation, "launch_generation": manifest.LaunchGeneration})
			return
		}
	}
	req := protocol.ReadCommit{SessionID: sid, SHA: sha, Patch: patch, WorkspaceGitRef: workspaceGitQuery(r)}
	// A branch view names a repository, not a runtime: the binding is the server's to
	// supply, and the daemon's fence still refuses it if the workspace moved on.
	if branchRead && req.Generation == 0 && req.LaunchGeneration == "" {
		if manifest, generation, err := loadExplorerBinding(ctx, a.dbPool, sid); err == nil {
			req.LaunchGeneration, req.Generation = manifest.LaunchGeneration, generation
			if req.MemberID == "" && len(manifest.Members) == 1 {
				req.MemberID = manifest.Members[0].ID
			}
		}
	}
	if r.Pattern == "GET /api/sessions/{id}/commits/{sha}/file" {
		req.FilePath = r.URL.Query().Get("path")
		req.Original = r.URL.Query().Get("side") == "original"
		if !protocol.ValidCommitFilePath(req.FilePath) || r.URL.Query().Get("side") != "original" && r.URL.Query().Get("side") != "changed" {
			commitProblem(w, 422, "Invalid commit file selection.")
			return
		}
	}

	if graphRead {
		req.Graph = true
		req.Depth = 50
		if raw := r.URL.Query().Get("depth"); raw != "" {
			depth, err := strconv.Atoi(raw)
			if err != nil || depth < 1 || depth > 200 {
				commitProblem(w, 422, "Invalid history depth.")
				return
			}
			req.Depth = depth
		}
	} else if branchRead {
		req.SHA = ""
		req.Branch = workbranch.Effective(sid, row.WorkBranch)
		// A caller may select another branch in this authorized member, never a path.
		if branch := r.URL.Query().Get("branch"); branch != "" {
			req.Branch = branch
		}
		req.Base = r.URL.Query().Get("base")
		if !workbranch.Valid(req.Branch) || req.Base != "" && !workbranch.Valid(req.Base) {
			commitProblem(w, 422, "Invalid Git branch.")
			return
		}
	}
	res, err := a.hub.requestCommitRead(ctx, dc, p.Sub, req, timeout)
	if errors.Is(err, errCommitBusy) {
		commitProblem(w, http.StatusTooManyRequests, "Too many commit reads at once; try again.")
		return
	}
	if err != nil {
		commitProblem(w, http.StatusConflict, "The daemon did not read local Git. Keep the workspace connected and update the daemon if it is older than the server.")
		return
	}
	// The daemon that answered must still run this session.
	if a.hub.GetDaemon(dc.ID) != dc || a.hub.FindDaemonForSession(sid) != dc {
		commitProblem(w, http.StatusConflict, "The session's workspace changed while reading the commit.")
		return
	}
	if dc.RuntimeTokenID != "" {
		if err := db.ValidateRuntimeBinding(ctx, a.dbPool, sid, dc.RuntimeTokenID, false); err != nil {
			commitProblem(w, http.StatusConflict, "The session's workspace changed while reading the commit.")
			return
		}
	}
	if !res.OK {
		switch res.ErrorCode {
		case "not_found", "invalid":
			commitProblem(w, http.StatusNotFound, "Commit not found.")
		case "objects_unavailable":
			commitProblem(w, http.StatusFailedDependency, "Local Git objects needed for this diff are missing or unavailable. Blobless cluster clones may not contain them.")
		case "too_large":
			commitProblem(w, http.StatusRequestEntityTooLarge, "The commit's diff is too large to open here.")
		case "busy":
			commitProblem(w, http.StatusTooManyRequests, "Too many commit reads at once; try again.")
		default:
			commitProblem(w, http.StatusConflict, "The session's workspace is unavailable.")
		}
		return
	}
	send := func(fenced context.Context) error {
		if !a.hub.currentWorkspaceGit(fenced, sid, res.WorkspaceGitRef) {
			return errCommitUnavailable
		}
		if dc.RuntimeTokenID != "" {
			if err := db.ValidateRuntimeBinding(fenced, a.dbPool, sid, dc.RuntimeTokenID, false); err != nil {
				return err
			}
		}
		if derefOrEmpty(row.Runtime) != "cluster" && a.hub.ownedHostDaemon(fenced, p.Sub, dc.ID) != dc {
			return db.ErrDesktopAuthority
		}
		// A slow diff reader cannot extend the short final ownership fence.
		deadline := time.Now().Add(time.Second)
		if until, ok := fenced.Deadline(); ok && until.Before(deadline) {
			deadline = until
		}
		_ = http.NewResponseController(w).SetWriteDeadline(deadline)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Blerg-Generation", strconv.FormatInt(res.Generation, 10))
		w.Header().Set("X-Blerg-Member-ID", res.MemberID)
		w.Header().Set("X-Blerg-Launch-Generation", res.LaunchGeneration)
		if patch {
			body, ok := validCommitPatch(res)
			if !ok || req.FilePath != "" && !res.File {
				commitProblem(w, http.StatusConflict, "The daemon returned an invalid commit.")
				return nil
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "sandbox")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return nil
		}
		if graphRead {
			if !validBranchGraph(res.Graph, req.Depth) {
				commitProblem(w, 409, "The daemon could not read branch history.")
				return nil
			}
			res.Graph.WorkspaceGitRef = res.WorkspaceGitRef
			writeJSON(w, 200, res.Graph)
			return nil
		}
		if branchRead {
			if !validBranchInfo(res.Branch, req.Branch) {
				commitProblem(w, 409, "The daemon could not return local branch history. Update the daemon if it is older than the server, then try again.")
				return nil
			}
			writeJSON(w, 200, res.Branch)
			return nil
		}
		if !validCommitInfo(res.Commit, sha) {
			commitProblem(w, http.StatusConflict, "The daemon returned an invalid commit.")
			return nil
		}
		res.Commit.WorkspaceGitRef = res.WorkspaceGitRef
		writeJSON(w, http.StatusOK, res.Commit)
		return nil
	}
	var deliveryErr error
	switch {
	case derefOrEmpty(row.Runtime) != "cluster":
		deliveryErr = db.WithDesktopFence(ctx, a.dbPool, dc.ID, send)
	case dc.RuntimeTokenID != "":
		deliveryErr = db.WithRuntimeLaunchFence(ctx, a.dbPool, sid, send)
	default:
		deliveryErr = send(ctx)
	}
	if deliveryErr != nil {
		commitProblem(w, 409, "The session's workspace changed while reading the commit.")
	}

}

func validCommitPatch(res protocol.CommitResult) ([]byte, bool) {
	if res.Commit != nil || len(res.PatchB64) > base64.StdEncoding.EncodedLen(protocol.CommitPatchMaxBytes) {
		return nil, false
	}
	body, err := base64.StdEncoding.Strict().DecodeString(res.PatchB64)
	if err != nil || len(body) > protocol.CommitPatchMaxBytes {
		return nil, false
	}
	return body, true
}

// validCommitInfo refuses a reply that is not the commit asked for or is over the daemon's caps.
func validCommitInfo(c *protocol.CommitInfo, sha string) bool {
	if c == nil || !protocol.ValidSessionOriginCommit(c.SHA) || !strings.HasPrefix(c.SHA, sha) || c.Short != c.SHA[:7] {
		return false
	}
	if len(c.Subject) > protocol.CommitSubjectMaxBytes || len(c.Body) > protocol.CommitBodyMaxBytes || len(c.Author) > protocol.CommitAuthorMaxBytes || len(c.AuthorDate) > 64 {
		return false
	}
	if len(c.Parents) > protocol.CommitParentsMax || len(c.Files) > protocol.CommitFilesMax || c.Adds < 0 || c.Dels < 0 {
		return false
	}
	for _, p := range c.Parents {
		if !protocol.ValidSessionOriginCommit(p) {
			return false
		}
	}
	for _, f := range c.Files {
		if f.Path == "" || len(f.Path) > protocol.CommitPathMaxBytes || len(f.OldPath) > protocol.CommitPathMaxBytes || f.Adds < 0 || f.Dels < 0 {
			return false
		}
	}
	if c.Parents == nil {
		c.Parents = []string{}
	}
	if c.Files == nil {
		c.Files = []protocol.CommitFile{}
	}
	return true
}

func validBranchInfo(b *protocol.BranchInfo, branch string) bool {
	if b == nil || b.Name != branch || len(b.Base) > 1024 || len(b.Commits) > 30 {
		return false
	}
	for _, c := range b.Commits {
		if !protocol.ValidSessionOriginCommit(c.SHA) || len(c.Message) > protocol.CommitSubjectMaxBytes || len(c.Author) > protocol.CommitAuthorMaxBytes || len(c.Date) > 64 {
			return false
		}
	}
	return true
}

func validBranchGraph(g *protocol.BranchGraph, depth int) bool {
	if g == nil || len(g.Refs) > 64 || len(g.Commits) > depth || len(g.Tags) > 16 {
		return false
	}
	for _, r := range g.Refs {
		if r.Name == "" || len(r.Name) > 1024 || !protocol.ValidSessionOriginCommit(r.SHA) {
			return false
		}
	}
	names := make(map[string]bool)
	for _, tag := range g.Tags {
		names[tag.Name] = true
	}
	commits := make(map[string]bool)
	for _, c := range g.Commits {
		commits[c.SHA] = true
	}
	for _, tag := range g.Tags {
		if tag.Name == "" || len(tag.Name) > 1024 || !protocol.ValidSessionOriginCommit(tag.SHA) || len(tag.Contains) > depth || tag.Previous != "" && !names[tag.Previous] {
			return false
		}
		seen := make(map[string]bool)
		for _, sha := range tag.Contains {
			if !commits[sha] || seen[sha] {
				return false
			}
			seen[sha] = true
		}
	}

	for _, c := range g.Commits {
		if !protocol.ValidSessionOriginCommit(c.SHA) || len(c.Subject) > protocol.CommitSubjectMaxBytes || len(c.Date) > 40 || len(c.Parents) > protocol.CommitParentsMax {
			return false
		}
		for _, p := range c.Parents {
			if !protocol.ValidSessionOriginCommit(p) {
				return false
			}
		}
	}
	return true
}
