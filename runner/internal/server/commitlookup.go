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

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
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

type commitWaiter struct {
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
	w := commitWaiter{ref: ref, dc: dc, sessionID: sid, account: account, ch: make(chan protocol.CommitResult, 1)}
	r := &h.commitReqs
	r.mu.Lock()
	perDaemon, perSession, perAccount := 0, 0, 0
	for _, p := range r.pending {
		if p.account == account {
			perAccount++
		}
		if p.dc.ID == dc.ID {
			perDaemon++
		}
		if p.sessionID == sid {
			perSession++
		}
	}
	if len(r.pending) >= commitPendingMax || perDaemon >= commitPendingPerDaemon || perSession >= commitPendingPerSession || perAccount >= commitPendingPerAccount {
		r.mu.Unlock()
		return protocol.CommitResult{}, errCommitBusy
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
		return protocol.CommitResult{}, errCommitUnavailable
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res, ok := <-w.ch:
		if !ok || h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(sid) != dc {
			return protocol.CommitResult{}, errCommitUnavailable
		}
		return res, nil
	case <-timer.C:
		return protocol.CommitResult{}, errCommitUnavailable
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
	mux.HandleFunc("GET /api/sessions/{id}/commits/{sha}", func(w http.ResponseWriter, r *http.Request) { a.handleCommit(w, r, false) })
	mux.HandleFunc("GET /api/sessions/{id}/commits/{sha}/diff", func(w http.ResponseWriter, r *http.Request) { a.handleCommit(w, r, true) })
}

func commitProblem(w http.ResponseWriter, status int, message string) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, map[string]string{"error": message, "error_id": newUUID()})
}

func (a *API) handleCommit(w http.ResponseWriter, r *http.Request, patch bool) {
	p, ok := a.authBrowser(w, r)
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
	if !ownsSession(p.Sub, row) && !p.Has(insightsAdminCap) {
		commitProblem(w, http.StatusForbidden, "Only the session owner or an administrator can read its commits.")
		return
	}
	sha := r.PathValue("sha")
	if !protocol.ValidCommitLookupSHA(sha) {
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
	res, err := a.hub.requestCommitRead(ctx, dc, p.Sub, protocol.ReadCommit{SessionID: sid, SHA: sha, Patch: patch, WorkspaceGitRef: workspaceGitQuery(r)}, timeout)
	if errors.Is(err, errCommitBusy) {
		commitProblem(w, http.StatusTooManyRequests, "Too many commit reads at once; try again.")
		return
	}
	if err != nil {
		commitProblem(w, http.StatusConflict, "The session's daemon did not read the commit.")
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
			if !ok {
				commitProblem(w, http.StatusConflict, "The daemon returned an invalid commit.")
				return nil
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "sandbox")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
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
