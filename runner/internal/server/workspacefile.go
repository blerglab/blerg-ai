package server

import (
	"context"
	"encoding/base64"
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
	"github.com/blerglab/blerg-ai/runner/internal/fileview"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const workspaceFileTimeout = 10 * time.Second
const workspaceFileMaxBytes = 25 << 20
const workspaceFileMaxFrame = (workspaceFileMaxBytes+2)/3*4 + (64 << 10)

var errWorkspaceUnavailable = errors.New("workspace file daemon unavailable")

type workspaceFileWaiter struct {
	dc        *DaemonConn
	sessionID string
	ch        chan protocol.WorkspaceFileResult
}
type workspaceFileRequests struct {
	mu             sync.Mutex
	pending        map[string]workspaceFileWaiter
	active         int
	activeByDaemon map[string]int
}

// A lease remains held through validation and the bounded HTTP response write, not just RPC.
func (h *Hub) acquireWorkspaceFileRead(dc *DaemonConn) (func(), bool) {
	r := &h.workspaceFileReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active >= 2 || r.activeByDaemon[dc.ID] >= 2 {
		return nil, false
	}
	if r.activeByDaemon == nil {
		r.activeByDaemon = make(map[string]int)
	}
	r.active++
	r.activeByDaemon[dc.ID]++
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.active--
		r.activeByDaemon[dc.ID]--
		if r.activeByDaemon[dc.ID] == 0 {
			delete(r.activeByDaemon, dc.ID)
		}
	}, true
}

func (h *Hub) requestWorkspaceFile(ctx context.Context, dc *DaemonConn, sid, name string) (protocol.WorkspaceFileResult, error) {
	id := newUUID()
	w := workspaceFileWaiter{dc: dc, sessionID: sid, ch: make(chan protocol.WorkspaceFileResult, 1)}
	r := &h.workspaceFileReqs
	r.mu.Lock()
	count := 0
	for _, p := range r.pending {
		if p.dc.ID == dc.ID {
			count++
		}
	}
	if len(r.pending) >= 4 || count >= 2 {
		r.mu.Unlock()
		return protocol.WorkspaceFileResult{}, errWorkspaceUnavailable
	}
	if r.pending == nil {
		r.pending = make(map[string]workspaceFileWaiter)
	}
	r.pending[id] = w
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock() }()
	raw, _ := json.Marshal(protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: id, SessionID: sid, Path: name})
	if h.FindDaemonForSession(sid) != dc || !h.trySendToDaemon(dc, raw) {
		return protocol.WorkspaceFileResult{}, errWorkspaceUnavailable
	}
	timer := time.NewTimer(workspaceFileTimeout)
	defer timer.Stop()
	select {
	case res, ok := <-w.ch:
		if !ok || h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(sid) != dc {
			return protocol.WorkspaceFileResult{}, errWorkspaceUnavailable
		}
		return res, nil
	case <-timer.C:
		return protocol.WorkspaceFileResult{}, errWorkspaceUnavailable
	case <-ctx.Done():
		return protocol.WorkspaceFileResult{}, ctx.Err()
	}
}

func (h *Hub) deliverWorkspaceFileResult(dc *DaemonConn, res protocol.WorkspaceFileResult) {
	if h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(res.SessionID) != dc || (dc.BoundSessionID != "" && dc.BoundSessionID != res.SessionID) {
		return
	}
	r := &h.workspaceFileReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.pending[res.RequestID]
	if !ok || w.dc != dc || w.sessionID != res.SessionID {
		return
	}
	delete(r.pending, res.RequestID)
	w.ch <- res
}
func (h *Hub) abandonWorkspaceFileRequests(dc *DaemonConn) {
	r := &h.workspaceFileReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.pending {
		if w.dc == dc {
			delete(r.pending, id)
			close(w.ch)
		}
	}
}

func workspacePathAllowed(name string) bool {
	if name == "" || len(name) > 4096 || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "~") || strings.Contains(name, "\\") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return false
		}
	}
	return path.Clean(name) != "."
}

func (a *API) RegisterWorkspaceFileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{id}/workspace-file", a.HandleGetWorkspaceFile)
}

func workspaceFileProblem(w http.ResponseWriter, status int, message string) {
	// The daemon wait can exhaust the success deadline. Give this fixed-size refusal a
	// separate, bounded write window so old peers receive JSON rather than a dropped socket.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, map[string]string{"error": message, "error_id": newUUID()})
}

// HandleGetWorkspaceFile is a human owner/admin capability, stricter than transcript visibility.
func (a *API) HandleGetWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if p.Kind != "human" {
		workspaceFileProblem(w, 403, "Workspace files can only be opened by a signed-in person.")
		return
	}
	if a.dbPool == nil {
		workspaceFileProblem(w, 409, "The session workspace is unavailable.")
		return
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		workspaceFileProblem(w, 404, "Session not found.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), workspaceFileTimeout)
	defer cancel()
	if until, ok := ctx.Deadline(); ok {
		_ = http.NewResponseController(w).SetWriteDeadline(until)
	}
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		workspaceFileProblem(w, 409, "The session workspace is unavailable.")
		return
	}
	if row == nil {
		workspaceFileProblem(w, 404, "Session not found.")
		return
	}
	// A private session is visible to its owner alone (privacy.go): not to an administrator, and to
	// anyone else it does not exist, so the answer matches an unknown session's.
	if row.Private && !ownsSession(p.Sub, row) {
		workspaceFileProblem(w, 404, "Session not found.")
		return
	}
	if !ownsSession(p.Sub, row) && !p.Has(insightsAdminCap) {
		workspaceFileProblem(w, 403, "Only the session owner or an administrator can open workspace files.")
		return
	}
	name := r.URL.Query().Get("path")
	if !workspacePathAllowed(name) {
		workspaceFileProblem(w, 403, "Blerg only opens relative paths inside the session workspace.")
		return
	}
	switch row.Status {
	case "ended", "stopped", "error", "disconnected":
		workspaceFileProblem(w, 409, "The session has no live workspace engine.")
		return
	}
	dc := a.hub.FindDaemonForSession(sid)
	if dc == nil || (dc.BoundSessionID != "" && dc.BoundSessionID != sid) {
		workspaceFileProblem(w, 409, "The session has no live workspace daemon.")
		return
	}
	release, leased := a.hub.acquireWorkspaceFileRead(dc)
	if !leased {
		workspaceFileProblem(w, 409, "The workspace file reader is busy.")
		return
	}
	defer release()
	res, err := a.hub.requestWorkspaceFile(ctx, dc, sid, name)
	if err != nil {
		workspaceFileProblem(w, 409, "The session daemon did not provide a workspace file.")
		return
	}
	// Validate authority again after waiting: a reconnect or launch replacement cannot supply data.
	if a.hub.GetDaemon(dc.ID) != dc || a.hub.FindDaemonForSession(sid) != dc {
		workspaceFileProblem(w, 409, "The session workspace changed while opening the file.")
		return
	}
	if dc.RuntimeTokenID != "" {
		if err := db.ValidateRuntimeBinding(ctx, a.dbPool, sid, dc.RuntimeTokenID, false); err != nil {
			workspaceFileProblem(w, 409, "The session workspace changed while opening the file.")
			return
		}
	}
	if !res.OK {
		status, message := 409, "The session workspace is unavailable."
		switch res.ErrorCode {
		case "forbidden":
			status = 403
			message = "Blerg could not safely open that workspace file."
			if strings.HasPrefix(res.Error, "Blerg doesn't open files that usually hold secrets") {
				message = "Blerg doesn't open files that usually hold secrets (restricted filename)."
			}
		case "not_found":
			status = 404
			message = "The workspace file was not found."
		case "too_large":
			status = 413
			message = "The workspace file is larger than its configured size limit."
		}
		workspaceFileProblem(w, status, message)
		return
	}
	if !workspacePathAllowed(res.Path) || res.Path != path.Clean(name) || res.Size < 0 || res.Size > workspaceFileMaxBytes || len(res.ContentB64) > base64.StdEncoding.EncodedLen(workspaceFileMaxBytes) || res.Truncated {
		workspaceFileProblem(w, 409, "The daemon returned an invalid workspace file.")
		return
	}
	// Strict decoding checks padding bits; rejecting CR/LF also enforces the canonical
	// encoding without allocating another full-size decoded or re-encoded copy.
	if strings.ContainsAny(res.ContentB64, "\r\n") {
		workspaceFileProblem(w, 409, "The daemon returned an invalid workspace file.")
		return
	}
	decoded := io.LimitReader(base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(res.ContentB64)), res.Size+1)
	head, err := io.ReadAll(io.LimitReader(decoded, 512))
	if err != nil {
		workspaceFileProblem(w, 409, "The daemon returned an invalid workspace file.")
		return
	}
	rest, err := io.Copy(io.Discard, decoded)
	if err != nil || int64(len(head))+rest != res.Size {
		workspaceFileProblem(w, 409, "The daemon returned an invalid workspace file.")
		return
	}
	if _, err := time.Parse(time.RFC3339Nano, res.Mtime); err != nil {
		workspaceFileProblem(w, 409, "The daemon returned an invalid workspace file.")
		return
	}
	ct, view := fileview.Classify(res.Path, head, res.Size)
	body := map[string]any{"path": res.Path, "size": res.Size, "view": view, "content_type": ct, "truncated": false, "content_b64": res.ContentB64, "mtime": res.Mtime}
	responseStarted := false
	send := func(fenced context.Context) error {
		if err := fenced.Err(); err != nil {
			return err
		}
		if a.hub.GetDaemon(dc.ID) != dc || a.hub.FindDaemonForSession(sid) != dc {
			return errWorkspaceUnavailable
		}
		if dc.RuntimeTokenID != "" {
			if err := db.ValidateRuntimeBinding(fenced, a.dbPool, sid, dc.RuntimeTokenID, false); err != nil {
				return err
			}
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		deadline := time.Now().Add(workspaceFileTimeout)
		if until, ok := fenced.Deadline(); ok && until.Before(deadline) {
			deadline = until
		}
		_ = http.NewResponseController(w).SetWriteDeadline(deadline)
		responseStarted = true
		writeJSON(w, http.StatusOK, body)
		return nil
	}
	if dc.RuntimeTokenID != "" {
		err = db.WithRuntimeLaunchFenceRetry(ctx, a.dbPool, sid, send)
	} else {
		err = send(ctx)
	}
	// A fence commit can fail after the callback already started the HTTP response.
	// Never append a second JSON document (or retry a possibly partial file response).
	if err != nil && !responseStarted {
		workspaceFileProblem(w, 409, "The session workspace changed while opening the file.")
	}
}
