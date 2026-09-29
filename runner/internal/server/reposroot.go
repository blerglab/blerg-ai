package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Changing a daemon's repos root from the app: the browser asks with
// PUT /api/daemons/{id}/repos-root, the server forwards a set_repos_root to
// that daemon and waits for its repos_root_result. The daemon does the real
// validation — it is the one that can see its filesystem — so this side only
// refuses what is obviously not a path, and relays the daemon's reason.

// reposRootTimeout bounds the wait for a daemon's answer. A daemon older
// than set_repos_root never answers (it logs the message as unhandled), so
// this is also how such a daemon is reported. A var so tests can shorten it.
var reposRootTimeout = 10 * time.Second

// maxReposRootLen bounds a requested or reported root (PATH_MAX on Linux).
const maxReposRootLen = 4096

// maxReposRootError bounds a daemon's refusal reason before it is relayed.
const maxReposRootError = 500

var (
	errReposRootDaemonGone = errors.New("daemon disconnected")
	errReposRootTimeout    = errors.New("daemon did not answer")
	errReposRootSendFull   = errors.New("daemon send buffer full")
)

// reposRootWaiter is one request waiting for its daemon's answer. The
// answer is accepted only from the connection it was sent to: a daemon can
// never answer for another, nor a reconnected one for its predecessor.
type reposRootWaiter struct {
	dc *DaemonConn
	ch chan protocol.ReposRootResult // buffered 1; closed when dc disconnects
}

// reposRootRequests holds the requests in flight, keyed by request id.
type reposRootRequests struct {
	mu      sync.Mutex
	pending map[string]reposRootWaiter
}

// requestReposRoot sends dc a set_repos_root for root and waits for the
// answer, the timeout, ctx, or dc disconnecting — whichever comes first.
func (h *Hub) requestReposRoot(ctx context.Context, dc *DaemonConn, root string) (protocol.ReposRootResult, error) {
	id := newUUID()
	w := reposRootWaiter{dc: dc, ch: make(chan protocol.ReposRootResult, 1)}
	r := &h.reposRootReqs
	r.mu.Lock()
	if r.pending == nil {
		r.pending = make(map[string]reposRootWaiter)
	}
	r.pending[id] = w
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
	}()

	data, err := json.Marshal(protocol.SetReposRoot{Type: "set_repos_root", RequestID: id, ReposRoot: root})
	if err != nil {
		return protocol.ReposRootResult{}, err
	}
	if !h.trySendToDaemon(dc, data) {
		return protocol.ReposRootResult{}, errReposRootSendFull
	}

	timer := time.NewTimer(reposRootTimeout)
	defer timer.Stop()
	select {
	case res, ok := <-w.ch:
		if !ok {
			return protocol.ReposRootResult{}, errReposRootDaemonGone
		}
		return res, nil
	case <-timer.C:
		return protocol.ReposRootResult{}, errReposRootTimeout
	case <-ctx.Done():
		return protocol.ReposRootResult{}, ctx.Err()
	}
}

// trySendToDaemon queues data for dc without blocking. dc.send is closed
// when the daemon's read pump exits, so a send racing that close is
// recovered from and reported as not sent.
func (h *Hub) trySendToDaemon(dc *DaemonConn, data []byte) (sent bool) {
	if h.GetDaemon(dc.ID) != dc {
		return false
	}
	defer func() {
		if recover() != nil {
			sent = false
		}
	}()
	select {
	case dc.send <- data:
		return true
	default:
		return false
	}
}

// deliverReposRootResult hands a daemon's answer to the request waiting for
// it. An answer nobody is waiting for (the request timed out), or from a
// connection other than the one asked, is dropped.
func (h *Hub) deliverReposRootResult(dc *DaemonConn, res protocol.ReposRootResult) {
	r := &h.reposRootReqs
	r.mu.Lock()
	w, ok := r.pending[res.RequestID]
	if ok && w.dc == dc {
		delete(r.pending, res.RequestID)
	}
	r.mu.Unlock()
	if !ok || w.dc != dc {
		return
	}
	w.ch <- res // buffered 1, and each waiter is delivered to at most once
}

// abandonReposRootRequests fails every request waiting on dc, which has
// just disconnected, so its callers answer now rather than at the timeout.
func (h *Hub) abandonReposRootRequests(dc *DaemonConn) {
	r := &h.reposRootReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.pending {
		if w.dc == dc {
			delete(r.pending, id)
			close(w.ch)
		}
	}
}

// reposRootProblem is the server's own check on a requested root: only
// what makes it obviously not a path. "" when fine.
func reposRootProblem(root string) string {
	switch {
	case root == "":
		return "a folder is required"
	case len(root) > maxReposRootLen:
		return "the path is too long"
	case strings.IndexFunc(root, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0:
		return "the path contains control characters"
	case !strings.HasPrefix(root, "/"):
		return "the path must be absolute (start with /)"
	}
	return ""
}

// applyReposRoot records a daemon's new repos root: in memory for the
// launch UI and spawns, and in its DB row. root comes from the daemon (its
// answer or a heartbeat); one that is not a plausible absolute path is
// ignored rather than stored.
func applyReposRoot(ctx context.Context, dbPool *pgxpool.Pool, dc *DaemonConn, root string) {
	if reposRootProblem(root) != "" || root == dc.CurrentReposRoot() {
		return
	}
	dc.SetReposRoot(root)
	if dbPool != nil {
		if err := db.UpsertDaemon(ctx, dbPool, dc.ID, dc.Name, dc.Mode, root); err != nil {
			log.Printf("daemon %s: record repos root: %v", dc.ID, err)
		}
	}
}

// handleReposRootResult is the daemon read pump's repos_root_result case.
// A successful change is applied here, not by the waiting request, so it
// sticks even when that request already gave up.
func handleReposRootResult(ctx context.Context, h *Hub, dbPool *pgxpool.Pool, dc *DaemonConn, res protocol.ReposRootResult) {
	if res.OK {
		applyReposRoot(ctx, dbPool, dc, res.ReposRoot)
		repos := res.CheckedOutRepos
		if repos == nil {
			repos = []string{}
		}
		dc.SetCheckedOutRepos(repos)
		// The old root's remotes describe folders that are no longer
		// listed; the new ones arrive in the heartbeat the daemon sends
		// right after this answer.
		dc.SetRepoRemotes(nil)
	}
	h.deliverReposRootResult(dc, res)
}

type putReposRootRequest struct {
	ReposRoot string `json:"repos_root"`
}

type putReposRootResponse struct {
	ReposRoot string `json:"repos_root"`
}

// HandlePutDaemonReposRoot is PUT /api/daemons/{id}/repos-root: change where
// that daemon keeps repos for new sessions. Gated like every other
// daemon-directed browser action (stopping a session, spawning one): a
// signed-in browser. 200 {repos_root} once the daemon has applied and saved
// it; 422 {error} with the daemon's reason when it refused (nothing
// changed); 404 when no such daemon is connected; 504 when it did not answer.
func (a *API) HandlePutDaemonReposRoot(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authBrowser(w, r); !ok {
		return
	}
	daemonID := r.PathValue("id")
	var req putReposRootRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if problem := reposRootProblem(req.ReposRoot); problem != "" {
		writeError(w, http.StatusUnprocessableEntity, problem)
		return
	}
	dc := a.hub.GetDaemon(daemonID)
	// A cluster session pod connects as its own per-session "daemon"; it is
	// not a workstation anyone configures (GET /api/daemons hides it too).
	if dc == nil || dc.Mode == "runner" {
		writeError(w, http.StatusNotFound, "daemon not connected")
		return
	}

	res, err := a.hub.requestReposRoot(r.Context(), dc, req.ReposRoot)
	switch {
	case errors.Is(err, errReposRootSendFull):
		writeError(w, http.StatusServiceUnavailable, "the daemon is busy — try again in a moment")
		return
	case errors.Is(err, errReposRootDaemonGone):
		writeError(w, http.StatusBadGateway, "the daemon disconnected before answering")
		return
	case errors.Is(err, errReposRootTimeout):
		writeError(w, http.StatusGatewayTimeout,
			"the daemon did not answer — it may be an older version that can't change this from the app; update it, or set BLERG_RUNNER_REPOS_ROOT and restart it")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "request failed")
		return
	}
	if !res.OK {
		reason := res.Error
		if reason == "" {
			reason = "the daemon refused the change"
		}
		if len(reason) > maxReposRootError {
			reason = reason[:maxReposRootError]
		}
		writeError(w, http.StatusUnprocessableEntity, reason)
		return
	}
	log.Printf("daemon %s (%s): repos root changed to %s", dc.ID, dc.Name, res.ReposRoot)
	writeJSON(w, http.StatusOK, putReposRootResponse{ReposRoot: res.ReposRoot})
}
