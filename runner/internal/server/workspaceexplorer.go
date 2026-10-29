package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type workspaceExplorerWaiter struct {
	dc  *DaemonConn
	req protocol.WorkspaceExplorerRequest
	ch  chan protocol.WorkspaceExplorerResult
}
type workspaceExplorerRequests struct {
	mu       sync.Mutex
	pending  map[string]workspaceExplorerWaiter
	active   int
	users    map[string]int
	daemons  map[string]int
	statuses map[string]bool
}

// Explorer leases sit beneath viewer leases. At most two of the four global
// viewer slots and one of each daemon/person's two slots can be explorer work.
// Admission refuses immediately: tree retries cannot queue ahead of viewers.
func (h *Hub) acquireWorkspaceExplorer(dc *DaemonConn, user, sid string, status bool) (func(), bool) {
	r := &h.explorerReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active >= 2 || r.users[user] >= 1 || r.daemons[dc.ID] >= 1 || (status && r.statuses[sid]) {
		return nil, false
	}
	releaseViewer, ok := h.acquireWorkspaceFileRead(dc, user)
	if !ok {
		return nil, false
	}
	if r.users == nil {
		r.users = map[string]int{}
		r.daemons = map[string]int{}
		r.statuses = map[string]bool{}
	}
	r.active++
	r.users[user]++
	r.daemons[dc.ID]++
	if status {
		r.statuses[sid] = true
	}
	return func() {
		r.mu.Lock()
		r.active--
		r.users[user]--
		r.daemons[dc.ID]--
		if r.users[user] == 0 {
			delete(r.users, user)
		}
		if r.daemons[dc.ID] == 0 {
			delete(r.daemons, dc.ID)
		}
		if status {
			delete(r.statuses, sid)
		}
		r.mu.Unlock()
		releaseViewer()
	}, true
}
func (h *Hub) requestWorkspaceExplorer(ctx context.Context, dc *DaemonConn, req protocol.WorkspaceExplorerRequest) (protocol.WorkspaceExplorerResult, error) {
	authorized, ok := dc.authorizedWorkspaceRuntime(req.SessionID)
	if !ok || authorized != req.WorkspaceRuntimeRef {
		return protocol.WorkspaceExplorerResult{}, errWorkspaceUnavailable
	}
	req.Type = "workspace_explorer"
	req.RequestID = newUUID()
	waiter := workspaceExplorerWaiter{dc: dc, req: req, ch: make(chan protocol.WorkspaceExplorerResult, 1)}
	r := &h.explorerReqs
	r.mu.Lock()
	if len(r.pending) >= 2 {
		r.mu.Unlock()
		return protocol.WorkspaceExplorerResult{}, errWorkspaceUnavailable
	}
	if r.pending == nil {
		r.pending = map[string]workspaceExplorerWaiter{}
	}
	r.pending[req.RequestID] = waiter
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, req.RequestID)
		r.mu.Unlock()
		// Cancel is idempotent, scoped to the original connection/request/session.
		req.Type = "cancel_workspace_explorer"
		raw, _ := json.Marshal(req)
		h.trySendToDaemon(dc, raw)
	}()
	raw, _ := json.Marshal(req)
	if h.FindDaemonForSession(req.SessionID) != dc || !h.trySendToDaemon(dc, raw) {
		return protocol.WorkspaceExplorerResult{}, errWorkspaceUnavailable
	}
	select {
	case res, ok := <-waiter.ch:
		if !ok || h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(req.SessionID) != dc {
			return protocol.WorkspaceExplorerResult{}, errWorkspaceUnavailable
		}
		return res, nil
	case <-ctx.Done():
		return protocol.WorkspaceExplorerResult{}, ctx.Err()
	}
}
func (h *Hub) deliverWorkspaceExplorerResult(dc *DaemonConn, res protocol.WorkspaceExplorerResult) {
	if h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(res.SessionID) != dc || (dc.BoundSessionID != "" && dc.BoundSessionID != res.SessionID) {
		return
	}
	authorized, ok := dc.authorizedWorkspaceRuntime(res.SessionID)
	if !ok || authorized != res.WorkspaceRuntimeRef {
		return
	}
	r := &h.explorerReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.pending[res.RequestID]
	if !ok || w.dc != dc || w.req.SessionID != res.SessionID || w.req.LaunchGeneration != res.LaunchGeneration || w.req.Generation != res.Generation || w.req.Operation != res.Operation || w.req.Path != res.Path {
		return
	}
	delete(r.pending, res.RequestID)
	w.ch <- res
}
func (h *Hub) abandonWorkspaceExplorerRequests(dc *DaemonConn) {
	r := &h.explorerReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.pending {
		if w.dc == dc {
			delete(r.pending, id)
			close(w.ch)
		}
	}
}
func validExplorerResult(req protocol.WorkspaceExplorerRequest, res protocol.WorkspaceExplorerResult) bool {
	if !protocol.ValidWorkspaceStatusReason(res.StatusReason) || res.StatusReason != "" && (res.OK || req.Operation != "status") {
		return false
	}
	if !res.OK && (len(res.Entries) != 0 || res.Status != nil || res.Cursor != "") {
		return false
	}
	if res.LaunchGeneration != req.LaunchGeneration || res.Generation != req.Generation || res.Path != req.Path || res.Operation != req.Operation || len(res.Cursor) > 8192 {
		return false
	}
	limit := protocol.WorkspaceExplorerEntries
	if req.Operation == "search" {
		limit = protocol.WorkspaceSearchHits
	}
	if len(res.Entries) > limit {
		return false
	}
	seen := map[string]bool{}
	for _, entry := range res.Entries {
		if !workspacePathAllowed(entry.Path) || !utf8.ValidString(entry.Path) || path.Clean(entry.Path) != entry.Path || entry.Name != path.Base(entry.Path) || entry.Size < 0 || seen[entry.Path] {
			return false
		}
		seen[entry.Path] = true
		if req.Operation == "list" {
			parent := path.Dir(entry.Path)
			want := req.Path
			if want == "" {
				want = "."
			}
			if parent != want {
				return false
			}
		} else if req.Path != "" && !strings.HasPrefix(entry.Path, req.Path+"/") {
			return false
		}
	}
	if req.Operation == "status" && res.OK {
		s := res.Status
		if s == nil || (req.MemberID != "" && s.MemberID != req.MemberID) || s.Path != req.Path || s.Changed < 0 || s.Untracked < 0 || len(s.Paths) > protocol.WorkspaceSearchVisited || s.Changed+s.Untracked != len(s.Paths) || len(s.Branch) > 1024 {
			return false
		}
		if s.Head != "" && !protocol.ValidSessionOriginCommit(s.Head) {
			return false
		}
		seen = map[string]bool{}
		for _, name := range s.Paths {
			if !utf8.ValidString(name) || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || seen[name] {
				return false
			}
			seen[name] = true
		}
		// Optional for older peers; when present, codes describe exactly the validated paths.
		if len(s.Files) != 0 {
			if len(s.Files) != len(s.Paths) {
				return false
			}
			untracked := 0
			for i, file := range s.Files {
				if file.Code == "U" {
					untracked++
				}
				if file.Path != s.Paths[i] || len(file.Code) != 1 || strings.IndexAny(file.Code, "MADUCTR") != 0 || (file.Directory && file.Code != "U") {
					return false
				}
			}
			if untracked != s.Untracked {
				return false
			}
		}

	}
	raw, err := json.Marshal(res)
	return err == nil && len(raw) <= protocol.WorkspaceExplorerReplyBytes
}

func (a *API) HandleGetWorkspaceExplorer(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if p.Kind != "human" {
		workspaceFileProblem(w, 403, workspaceCodeForbidden, "Workspace files require a signed-in person.")
		return
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		workspaceFileProblem(w, 404, workspaceCodeNotFound, "Session not found.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if a.dbPool == nil {
		workspaceFileProblem(w, 409, workspaceCodeUnavailable, "The session workspace is unavailable.")
		return
	}
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		workspaceFileProblem(w, 409, workspaceCodeUnavailable, "The session workspace is unavailable.")
		return
	}
	if row == nil || (row.Private && !ownsSession(p.Sub, row)) {
		workspaceFileProblem(w, 404, workspaceCodeNotFound, "Session not found.")
		return
	}
	if !ownsSession(p.Sub, row) && !p.Has(insightsAdminCap) {
		workspaceFileProblem(w, 403, workspaceCodeForbidden, "Only the session owner or an administrator can open workspace files.")
		return
	}
	if !a.hub.desktopSessionVisible(ctx, p.Sub, row) {
		workspaceFileProblem(w, 403, workspaceCodeForbidden, "Only the desktop owner can browse its workspace.")
		return
	}
	switch row.Status {
	case "ended", "stopped", "error", "disconnected":
		workspaceFileProblem(w, 409, workspaceCodeNotRunning, "The session has no live workspace.")
		return
	}
	req := protocol.WorkspaceExplorerRequest{SessionID: sid, Operation: r.URL.Query().Get("op"), Path: r.URL.Query().Get("path"), Filter: r.URL.Query().Get("filter"), Cursor: r.URL.Query().Get("cursor"), MemberID: r.URL.Query().Get("member")}
	if req.Operation == "" {
		req.Operation = "list"
	}
	if (req.Operation != "list" && req.Operation != "search" && req.Operation != "status") || (req.Path != "" && !workspacePathAllowed(req.Path)) || len(req.Filter) > 256 || len(req.Cursor) > 8192 {
		workspaceFileProblem(w, 400, workspaceCodeInvalid, "Invalid workspace request.")
		return
	}
	if err := a.ensureLegacyWorkspace(ctx, sid); err != nil {
		code := workspaceCodeUnavailable
		if errors.Is(err, errLegacyWorkspaceBusy) {
			code = workspaceCodeBusy // the explorer retries a busy refusal
		}
		workspaceFileProblem(w, 409, code, err.Error())
		return
	}
	manifest, generation, err := loadExplorerBinding(ctx, a.dbPool, sid)
	if err != nil {
		workspaceFileProblem(w, 409, workspaceCodeUnavailable, "The runtime workspace is not bound.")
		return
	}
	req.LaunchGeneration = manifest.LaunchGeneration
	req.Generation = generation
	dc := a.hub.FindDaemonForSession(sid)
	if dc == nil || (dc.BoundSessionID != "" && dc.BoundSessionID != sid) || validateWorkspaceDesktop(ctx, a.dbPool, dc, sid) != nil {
		workspaceFileProblem(w, 409, workspaceCodeNotRunning, "The session has no live workspace daemon.")
		return
	}
	if manifest.Runtime != "cluster" && a.hub.ownedHostDaemon(ctx, p.Sub, dc.ID) != dc {
		workspaceFileProblem(w, 409, workspaceCodeUnavailable, "The desktop authority changed.")
		return
	}
	release, ok := a.hub.acquireWorkspaceExplorer(dc, p.Sub, sid, req.Operation == "status")
	if !ok {
		workspaceFileProblem(w, 409, workspaceCodeBusy, "The workspace explorer is busy.")
		return
	}
	defer release()
	res, err := a.hub.requestWorkspaceExplorer(ctx, dc, req)
	if err != nil {
		workspaceFileProblem(w, 409, workspaceCodeDaemonTimeout, "The workspace runtime did not reply.")
		return
	}
	if !validExplorerResult(req, res) || !validExplorerMembers(manifest, res) {
		workspaceFileProblem(w, 409, workspaceCodeInvalid, "The runtime returned an invalid workspace reply.")
		return
	}
	body := map[string]any{"result": res, "members": manifest.Members, "runtime": manifest.Runtime}
	encoded, marshalErr := json.Marshal(body)
	if marshalErr != nil || len(encoded)+1 > protocol.WorkspaceExplorerReplyBytes {
		workspaceFileProblem(w, 409, workspaceCodeInvalid, "The runtime returned an oversized workspace reply.")
		return
	}
	encoded = append(encoded, '\n')
	started := false
	send := func(fenced context.Context) error {
		current, gen, err := loadExplorerBinding(fenced, a.dbPool, sid)
		if err != nil || current.LaunchGeneration != req.LaunchGeneration || gen != req.Generation || a.hub.GetDaemon(dc.ID) != dc || a.hub.FindDaemonForSession(sid) != dc {
			return errWorkspaceUnavailable
		}
		authorized, ok := dc.authorizedWorkspaceRuntime(sid)
		if !ok || authorized != req.WorkspaceRuntimeRef {
			return errWorkspaceUnavailable
		}
		latest, err := db.GetSession(fenced, a.dbPool, sid)
		if err != nil || latest == nil || (latest.Private && !ownsSession(p.Sub, latest)) || (!ownsSession(p.Sub, latest) && !p.Has(insightsAdminCap)) {
			return errWorkspaceUnavailable
		}
		if !a.hub.desktopSessionVisible(fenced, p.Sub, latest) || (current.Runtime != "cluster" && a.hub.ownedHostDaemon(fenced, p.Sub, dc.ID) != dc) {
			return errWorkspaceUnavailable
		}
		switch latest.Status {
		case "ended", "stopped", "error", "disconnected":
			return errWorkspaceUnavailable
		}
		if dc.RuntimeTokenID != "" {
			if err := db.ValidateRuntimeBinding(fenced, a.dbPool, sid, dc.RuntimeTokenID, false); err != nil {
				return err
			}
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", "application/json")
		started, err = writeFencedResponse(fenced, w, http.StatusOK, encoded)
		return err
	}
	if dc.RuntimeTokenID != "" {
		err = db.WithRuntimeLaunchFenceRetry(ctx, a.dbPool, sid, send)
	} else {
		err = withWorkspaceDesktopFence(ctx, a.dbPool, dc, sid, send)
	}
	if err != nil && !started {
		workspaceFileProblem(w, 409, workspaceCodeChanged, "The workspace changed while reading.")
	}
}

func validExplorerMembers(manifest protocol.WorkspaceManifest, res protocol.WorkspaceExplorerResult) bool {
	for _, id := range res.UnavailableMembers {
		found := false
		for _, member := range manifest.Members {
			if member.ID == id {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, entry := range res.Entries {
		if entry.MemberID == "" {
			continue
		}
		found := false
		for _, member := range manifest.Members {
			if member.ID == entry.MemberID && member.Path == entry.Path {
				found = true
				break
			}
		}
		if !found || !entry.Repository {
			return false
		}
	}
	if res.Status != nil && res.Status.MemberID != "" {
		want := res.Path
		if want == "" {
			want = "."
		}
		for _, member := range manifest.Members {
			if member.ID == res.Status.MemberID && member.Path == want {
				return true
			}
		}
		return false
	}
	return true
}
