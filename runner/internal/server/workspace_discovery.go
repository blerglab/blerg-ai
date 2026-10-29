package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

var errDiscoveryUnavailable = errors.New("workspace discovery unavailable or changed")

type discoveryWaiter struct {
	dc          *DaemonConn
	owner, root string
	ch          chan protocol.WorkspaceDiscoveryResult
}
type discoveryRequests struct {
	sync.Mutex
	pending map[string]discoveryWaiter
}

// Ownership is checked before slots or metadata. Per-person and per-connection
// bounds prevent one browser from occupying every discovery worker.
func (h *Hub) requestWorkspaceDiscovery(ctx context.Context, owner string, dc *DaemonConn, req protocol.DiscoverWorkspace) (protocol.WorkspaceDiscoveryResult, error) {
	var zero protocol.WorkspaceDiscoveryResult
	if dc == nil || h.ownedHostDaemon(ctx, owner, dc.ID) != dc || dc.DiscoveryVersion != 1 {
		return zero, errDiscoveryUnavailable
	}
	root := dc.CurrentReposRoot()
	r := &h.discoveryReqs
	r.Lock()
	if len(r.pending) >= 2 {
		r.Unlock()
		return zero, errDiscoveryUnavailable
	}
	for _, w := range r.pending {
		if w.dc == dc || w.owner == owner {
			r.Unlock()
			return zero, errDiscoveryUnavailable
		}
	}
	if r.pending == nil {
		r.pending = map[string]discoveryWaiter{}
	}
	req.Type = "discover_workspace"
	req.RequestID = newUUID()
	w := discoveryWaiter{dc: dc, owner: owner, root: root, ch: make(chan protocol.WorkspaceDiscoveryResult, 1)}
	r.pending[req.RequestID] = w
	r.Unlock()
	defer func() { r.Lock(); delete(r.pending, req.RequestID); r.Unlock() }()
	raw, err := json.Marshal(req)
	if err != nil || len(raw) > 256<<10 || !h.trySendToDaemon(dc, raw) {
		return zero, errDiscoveryUnavailable
	}
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	select {
	case res, ok := <-w.ch:
		if !ok || h.ownedHostDaemon(ctx, owner, dc.ID) != dc || dc.CurrentReposRoot() != root || !validDiscoveryResult(res) {
			return zero, errDiscoveryUnavailable
		}
		return res, nil
	case <-timer.C:
	case <-ctx.Done():
	}
	cancel, _ := json.Marshal(protocol.DiscoverWorkspace{Type: "discover_workspace", RequestID: req.RequestID, Operation: "cancel"})
	h.trySendToDaemon(dc, cancel)
	return zero, errDiscoveryUnavailable
}
func (h *Hub) deliverWorkspaceDiscovery(dc *DaemonConn, res protocol.WorkspaceDiscoveryResult) {
	r := &h.discoveryReqs
	r.Lock()
	defer r.Unlock()
	w, ok := r.pending[res.RequestID]
	if !ok || w.dc != dc {
		return
	}
	delete(r.pending, res.RequestID)
	w.ch <- res
}
func (h *Hub) abandonWorkspaceDiscoveryRequests(dc *DaemonConn) {
	r := &h.discoveryReqs
	r.Lock()
	defer r.Unlock()
	for id, w := range r.pending {
		if w.dc == dc {
			delete(r.pending, id)
			close(w.ch)
		}
	}
}
func validDiscoveryPath(p string) bool {
	return p != "" && len(p) <= 4096 && path.Clean(p) == p && !path.IsAbs(p) && p != ".." && !strings.HasPrefix(p, "../") && !strings.Contains(p, "\\") && strings.IndexFunc(p, unicode.IsControl) < 0
}
func validDiscoveryResult(res protocol.WorkspaceDiscoveryResult) bool {
	if !res.OK {
		switch res.ErrorCode {
		case "unavailable", "stale", "invalid_selection", "too_many_members", "overlapping_repositories", "submodule", "discovery_limit":
			return res.Page == nil && res.Resolved == nil
		}
		return false
	}
	if res.Page != nil {
		p := res.Page
		if p.RootIdentity == "" || len(p.Entries) > 500 || !validDiscoveryPath(p.Root.Path) {
			return false
		}
		for _, e := range p.Entries {
			if !validDiscoveryPath(e.Path) || e.SourceIdentity == "" || e.Name != path.Base(e.Path) || (e.Kind != "directory" && e.Kind != "repository") {
				return false
			}
		}
	}
	if res.Resolved != nil {
		p := res.Resolved
		if !p.Layout.Valid() || p.SelectionIdentity == "" || p.RootIdentity == "" || len(p.Members) > 20 {
			return false
		}
		ids := map[string]bool{}
		common := map[string]bool{}
		for i, m := range p.Members {
			for _, prev := range p.Members[:i] {
				if m.Path == prev.Path || m.Path == "." || prev.Path == "." || strings.HasPrefix(m.Path, prev.Path+"/") || strings.HasPrefix(prev.Path, m.Path+"/") {
					return false
				}
			}
			if !validDiscoveryPath(m.Path) || m.SourceIdentity == "" || m.CommonDirectoryIdentity == "" || ids[m.SourceIdentity] || common[m.CommonDirectoryIdentity] {
				return false
			}
			ids[m.SourceIdentity] = true
			common[m.CommonDirectoryIdentity] = true
		}
	}
	raw, err := json.Marshal(res)
	return err == nil && len(raw) <= 1<<20
}
func (a *API) RegisterWorkspaceDiscoveryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/workspace/discovery", a.HandleWorkspaceDiscovery)
	mux.HandleFunc("POST /api/workspace/preview", a.HandleWorkspacePreview)
}
func (a *API) discoveryTarget(ctx context.Context, owner string, s protocol.WorkspaceSelectionRequest) *DaemonConn {
	dc := a.hub.ownedHostDaemon(ctx, owner, s.DaemonID)
	if dc == nil || dc.DiscoveryVersion != 1 || dc.ownershipRevision() != s.OwnershipRevision || dc.DesktopGeneration != s.ConnectionGeneration {
		return nil
	}
	return dc
}
func discoveryProblem(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 409, map[string]string{"code": "workspace_changed", "error": "The selected desktop or folder is unavailable or changed. Select it again."})
}

// Keep the final authority fence short even when the client stops reading.
// Refuse writers that cannot install a deadline; no source bytes have been sent.
func writeDiscoverySnapshot(ctx context.Context, w http.ResponseWriter, body any) (bool, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return false, err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	return writeFencedResponse(ctx, w, http.StatusOK, append(raw, '\n'))
}

// Install the deadline before even committing headers (including a 304), and
// flush before unlocking authority. Once started, callers must not retry.
func writeFencedResponse(ctx context.Context, w http.ResponseWriter, status int, raw []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// ResponseController has no capability probe. Check the same unwrap chain
	// before committing any source bytes, including writers with deadlines but
	// no flush implementation.
	if !responseSupportsFlush(w) {
		return false, http.ErrNotSupported
	}
	deadline := time.Now().Add(time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(deadline); err != nil {
		return false, err
	}
	w.WriteHeader(status)
	var err error
	if len(raw) > 0 {
		var n int
		n, err = w.Write(raw)
		if err == nil && n != len(raw) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = controller.Flush()
	}
	return true, err
}

func responseSupportsFlush(w http.ResponseWriter) bool {
	for {
		switch next := w.(type) {
		case interface{ FlushError() error }, http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = next.Unwrap()
		default:
			return false
		}
	}
}

func (a *API) HandleWorkspaceDiscovery(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	dc := a.hub.ownedHostDaemon(r.Context(), p.Sub, q.Get("daemon_id"))
	if dc == nil || dc.DiscoveryVersion != 1 {
		discoveryProblem(w)
		return
	}
	written := false
	send := func(ctx context.Context) error {
		res, err := a.hub.requestWorkspaceDiscovery(ctx, p.Sub, dc, protocol.DiscoverWorkspace{Operation: "list", Path: q.Get("path"), Cursor: q.Get("cursor"), RootIdentity: q.Get("root_identity")})
		if err != nil {
			return err
		}
		return db.WithDesktopFence(ctx, a.dbPool, dc.ID, func(fenced context.Context) error {
			if a.hub.ownedHostDaemon(fenced, p.Sub, dc.ID) != dc {
				return errDiscoveryUnavailable
			}
			var err error
			written, err = writeDiscoverySnapshot(fenced, w, map[string]any{"ok": res.OK, "code": res.ErrorCode, "page": res.Page, "daemon_id": dc.ID, "ownership_revision": dc.ownershipRevision(), "connection_generation": dc.DesktopGeneration})
			return err
		})
	}
	if err := send(r.Context()); err != nil && !written {
		discoveryProblem(w)
	}
}
func (a *API) HandleWorkspacePreview(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	var body struct {
		Runtime   string                             `json:"runtime"`
		Selection protocol.WorkspaceSelectionRequest `json:"selection"`
		Expand    bool                               `json:"expand"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil {
		discoveryProblem(w)
		return
	}
	if body.Runtime == "cluster" {
		m, err := a.authorizedHostedSelection(r.Context(), p, body.Selection)
		if err != nil {
			discoveryProblem(w)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, 200, map[string]any{"ok": true, "selection": body.Selection, "members": m.Members})
		return
	}
	if (body.Runtime != "host" && body.Runtime != "sandbox") || len(body.Selection.Hosted) > 0 {
		discoveryProblem(w)
		return
	}
	dc := a.discoveryTarget(r.Context(), p.Sub, body.Selection)
	if dc == nil {
		discoveryProblem(w)
		return
	}
	written := false
	send := func(ctx context.Context) error {
		op := "resolve"
		if body.Expand {
			op = "expand"
		}
		s := body.Selection
		res, err := a.hub.requestWorkspaceDiscovery(ctx, p.Sub, dc, protocol.DiscoverWorkspace{Operation: op, Layout: s.Layout, RootIdentity: s.RootIdentity, Members: s.Members, Folder: s.Folder, MembershipSnapshot: s.MembershipSnapshot, Runtime: body.Runtime})
		if err != nil {
			return err
		}
		if !res.OK || res.Resolved == nil {
			w.Header().Set("Cache-Control", "private, no-store")
			writeJSON(w, 409, map[string]any{"ok": false, "code": res.ErrorCode})
			return nil
		}
		v := res.Resolved
		if v.Layout != s.Layout {
			if v.Layout == "" && s.Layout != "" {
				w.Header().Set("Cache-Control", "private, no-store")
				writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "code": "desktop_update_required", "error": "Update Blerg on this desktop to use several repositories (reinstall its daemon)."})
				return nil
			}
			return errDiscoveryUnavailable
		}
		// Never null: a host folder with no Git repository is a valid zero-member
		// selection, and the picker reads both lists as arrays.
		s.Members = []protocol.SelectionRef{}
		members := v.Members
		if members == nil {
			members = []protocol.DiscoverySource{}
		}
		s.Layout = v.Layout
		s.Folder = v.Folder
		s.MembershipSnapshot = v.MembershipSnapshot
		s.RootIdentity = v.RootIdentity
		for _, m := range v.Members {
			s.Members = append(s.Members, protocol.SelectionRef{Path: m.Path, SourceIdentity: m.SourceIdentity})
		}
		return db.WithDesktopFence(ctx, a.dbPool, dc.ID, func(fenced context.Context) error {
			if a.discoveryTarget(fenced, p.Sub, s) != dc {
				return errDiscoveryUnavailable
			}
			var err error
			paths, fallback := workspaceselection.MemberPaths(*v)
			written, err = writeDiscoverySnapshot(fenced, w, map[string]any{"ok": true, "selection": s, "members": members, "structure_fallback": fallback, "member_paths": paths})
			return err
		})
	}
	if err := send(r.Context()); err != nil && !written {
		discoveryProblem(w)
	}
}
