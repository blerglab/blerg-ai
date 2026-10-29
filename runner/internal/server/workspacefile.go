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

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/fileview"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const workspaceFileTimeout = 10 * time.Second
const workspaceFileMaxBytes = 25 << 20
const workspaceFileMaxFrame = (workspaceFileMaxBytes+2)/3*4 + (64 << 10)

var errWorkspaceUnavailable = errors.New("workspace file daemon unavailable")

type workspaceFileWaiter struct {
	dc                *DaemonConn
	sessionID         string
	ch                chan protocol.WorkspaceFileResult
	launchGeneration  string
	runtimeGeneration int64
}
type workspaceFileRequests struct {
	mu             sync.Mutex
	pending        map[string]workspaceFileWaiter
	active         int
	activeByDaemon map[string]int
	activeByUser   map[string]int
}

// Workspace-file reads at once: for the whole server, per daemon and per person. The per-person
// limit keeps one person's document (a README full of images) from holding every slot: someone
// else's viewer still gets one. A refused read is answered 409 busy, and the client retries.
const (
	workspaceReadsGlobal    = 4
	workspaceReadsPerDaemon = 2
	workspaceReadsPerUser   = 2
)

// A lease remains held through validation and the bounded HTTP response write, not just RPC.
func (h *Hub) acquireWorkspaceFileRead(dc *DaemonConn, user string) (func(), bool) {
	r := &h.workspaceFileReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active >= workspaceReadsGlobal || r.activeByDaemon[dc.ID] >= workspaceReadsPerDaemon || r.activeByUser[user] >= workspaceReadsPerUser {
		return nil, false
	}
	if r.activeByDaemon == nil {
		r.activeByDaemon = make(map[string]int)
	}
	if r.activeByUser == nil {
		r.activeByUser = make(map[string]int)
	}
	r.active++
	r.activeByDaemon[dc.ID]++
	r.activeByUser[user]++
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.active--
		r.activeByDaemon[dc.ID]--
		if r.activeByDaemon[dc.ID] == 0 {
			delete(r.activeByDaemon, dc.ID)
		}
		r.activeByUser[user]--
		if r.activeByUser[user] == 0 {
			delete(r.activeByUser, user)
		}
	}, true
}

func (h *Hub) requestWorkspaceFile(ctx context.Context, dc *DaemonConn, sid, name string, binding ...protocol.WorkspaceExplorerRequest) (protocol.WorkspaceFileResult, error) {
	id := newUUID()
	w := workspaceFileWaiter{dc: dc, sessionID: sid, ch: make(chan protocol.WorkspaceFileResult, 1)}
	if len(binding) > 0 {
		w.launchGeneration = binding[0].LaunchGeneration
		w.runtimeGeneration = binding[0].Generation
	}
	if w.launchGeneration != "" {
		authorized, ok := dc.authorizedWorkspaceRuntime(sid)
		if !ok || authorized != binding[0].WorkspaceRuntimeRef {
			return protocol.WorkspaceFileResult{}, errWorkspaceUnavailable
		}
	}
	r := &h.workspaceFileReqs
	r.mu.Lock()
	count := 0
	for _, p := range r.pending {
		if p.dc.ID == dc.ID {
			count++
		}
	}
	if len(r.pending) >= workspaceReadsGlobal || count >= workspaceReadsPerDaemon {
		r.mu.Unlock()
		return protocol.WorkspaceFileResult{}, errWorkspaceUnavailable
	}
	if r.pending == nil {
		r.pending = make(map[string]workspaceFileWaiter)
	}
	r.pending[id] = w
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock() }()
	req := protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: id, SessionID: sid, Path: name, WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: w.launchGeneration, Generation: w.runtimeGeneration}}
	raw, _ := json.Marshal(req)
	if w.launchGeneration != "" {
		defer func() {
			req.Type = "cancel_workspace_file"
			cancelRaw, _ := json.Marshal(req)
			h.trySendToDaemon(dc, cancelRaw)
		}()
	}
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
	if res.LaunchGeneration != "" {
		authorized, ok := dc.authorizedWorkspaceRuntime(res.SessionID)
		if !ok || authorized != res.WorkspaceRuntimeRef {
			return
		}
	}
	r := &h.workspaceFileReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.pending[res.RequestID]
	if !ok || w.dc != dc || w.sessionID != res.SessionID || w.launchGeneration != res.LaunchGeneration || w.runtimeGeneration != res.Generation {
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
	if name == "" || len(name) > 4096 || strings.HasPrefix(name, "//") || strings.HasPrefix(name, "~") || strings.Contains(name, "\\") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return false
		}
	}
	return path.Clean(name) != "." && path.Clean(name) != "/"
}

// Absolute-to-relative conversion belongs to the launch-bound daemon. Require it
// to echo the cleaned request and return a safe relative path for document links.
func workspaceReplyPathAllowed(name string, res protocol.WorkspaceFileResult) bool {
	if !workspacePathAllowed(res.Path) || path.IsAbs(res.Path) || res.Path != path.Clean(res.Path) {
		return false
	}
	if path.IsAbs(name) {
		return res.RequestedPath == path.Clean(name) && strings.HasSuffix(path.Clean(name), "/"+res.Path)
	}
	return res.Path == path.Clean(name)
}

func (a *API) RegisterWorkspaceFileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{id}/workspace-file", a.HandleGetWorkspaceFile)
	mux.HandleFunc("GET /api/sessions/{id}/workspace", a.HandleGetWorkspaceExplorer)
}

// Machine-readable reasons for a refused workspace-file read, beside the sentence for people.
// Clients branch on these, never on the text (packages/chat/src/model/imageQueue.ts retries
// workspaceCodeBusy without limit while a document is open, and workspaceCodeDaemonTimeout and
// workspaceCodeChanged a few times; anything else is final).
const (
	workspaceCodeBusy             = "busy"           // every read slot is taken; try again shortly
	workspaceCodeDaemonTimeout    = "daemon_timeout" // the daemon's reply did not arrive in time
	workspaceCodeChanged          = "changed"        // the workspace changed (a reconnect) while opening
	workspaceCodeNotRunning       = "not_running"    // the session has no live engine or daemon
	workspaceCodeUnavailable      = "unavailable"
	workspaceCodeForbidden        = "forbidden"
	workspaceCodePersonRequired   = "person_required"
	workspaceCodeSessionOwnership = "session_ownership"
	workspaceCodeDesktopOwnership = "desktop_ownership"
	workspaceCodeNotFound         = "not_found"
	workspaceCodeTooLarge         = "too_large"
	workspaceCodeInvalid          = "invalid"
)

func workspaceFileProblem(w http.ResponseWriter, status int, code, message string) {
	// The daemon wait can exhaust the success deadline. Give this fixed-size refusal a
	// separate, bounded write window so old peers receive JSON rather than a dropped socket.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, map[string]string{"error": message, "code": code, "error_id": newUUID()})
}

// Refusals remain safe to show, even after a read loses its authority. A private
// session stays indistinguishable from an unknown one for everyone but its owner.
func (a *API) workspaceFileAccessDenied(ctx context.Context, w http.ResponseWriter, p identity.Principal, row *db.SessionRow) bool {
	if row == nil || (row.Private && !ownsSession(p.Sub, row)) {
		workspaceFileProblem(w, 404, workspaceCodeNotFound, "Session not found.")
		return true
	}
	if !ownsSession(p.Sub, row) && (!p.Has(insightsAdminCap) || derefOrEmpty(row.Runtime) != runnerRuntimeCluster) {
		workspaceFileProblem(w, 403, workspaceCodeSessionOwnership, "You don't have access to these workspace files. Ask the session owner to open or publish the file for you.")
		return true
	}
	if !a.hub.desktopSessionVisible(ctx, p.Sub, row) {
		workspaceFileProblem(w, 403, workspaceCodeDesktopOwnership, "This desktop is unavailable to you. Open Settings → Account → Your desktops and enroll it to you, or ask an administrator to assign it first.")
		return true
	}
	return false
}

// Return true when a refusal was written. An exhausted RPC context cannot
// establish a new ownership decision; preserve the caller's timeout response.
func (a *API) recheckWorkspaceFileAccess(ctx context.Context, w http.ResponseWriter, p identity.Principal, sid string) bool {
	if ctx.Err() != nil {
		return false
	}
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		workspaceFileProblem(w, 409, workspaceCodeUnavailable, "The session workspace is unavailable.")
		return true
	}
	return a.workspaceFileAccessDenied(ctx, w, p, row)
}

// HandleGetWorkspaceFile is a human owner/admin capability, stricter than transcript visibility.
func (a *API) HandleGetWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if p.Kind != "human" {
		workspaceFileProblem(w, 403, workspaceCodePersonRequired, "Sign in as a person to open workspace files.")
		return
	}
	if a.dbPool == nil {
		workspaceFileProblem(w, 409, workspaceCodeUnavailable, "The session workspace is unavailable.")
		return
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		workspaceFileProblem(w, 404, workspaceCodeNotFound, "Session not found.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), workspaceFileTimeout)
	defer cancel()
	if until, ok := ctx.Deadline(); ok {
		_ = http.NewResponseController(w).SetWriteDeadline(until)
	}
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		workspaceFileProblem(w, 409, workspaceCodeUnavailable, "The session workspace is unavailable.")
		return
	}
	if a.workspaceFileAccessDenied(ctx, w, p, row) {
		return
	}
	name := r.URL.Query().Get("path")
	if !workspacePathAllowed(name) {
		workspaceFileProblem(w, 403, workspaceCodeForbidden, "Blerg only opens paths inside the session workspace.")
		return
	}
	switch row.Status {
	case "ended", "stopped", "error", "disconnected":
		workspaceFileProblem(w, 409, workspaceCodeNotRunning, "The session has no live workspace engine.")
		return
	}
	dc := a.hub.FindDaemonForSession(sid)
	if dc == nil || (dc.BoundSessionID != "" && dc.BoundSessionID != sid) || validateWorkspaceDesktop(ctx, a.dbPool, dc, sid) != nil {
		workspaceFileProblem(w, 409, workspaceCodeNotRunning, "The session has no live workspace daemon.")
		return
	}
	release, leased := a.hub.acquireWorkspaceFileRead(dc, p.Sub)
	if !leased {
		workspaceFileProblem(w, 409, workspaceCodeBusy, "The workspace file reader is busy.")
		return
	}
	defer release()
	viewerBinding := protocol.WorkspaceExplorerRequest{}
	persisted, bindingErr := db.GetSessionReaderWorkspace(ctx, a.dbPool, sid)
	if bindingErr != nil {
		workspaceFileProblem(w, 409, workspaceCodeUnavailable, "The workspace binding is unavailable.")
		return
	}
	if persisted != nil && persisted.Kind == "folder" && persisted.Manifest == nil {
		workspaceFileProblem(w, 409, workspaceCodeUnavailable, "The workspace manifest is unavailable.")
		return
	}
	if persisted != nil && persisted.Manifest != nil {
		manifest, generation, err := loadExplorerBinding(ctx, a.dbPool, sid)
		if err != nil {
			workspaceFileProblem(w, 409, workspaceCodeUnavailable, "The runtime workspace is not bound.")
			return
		}
		viewerBinding.LaunchGeneration = manifest.LaunchGeneration
		viewerBinding.Generation = generation
	}
	res, err := a.hub.requestWorkspaceFile(ctx, dc, sid, name, viewerBinding)
	if a.recheckWorkspaceFileAccess(ctx, w, p, sid) {
		return
	}
	if err != nil {
		workspaceFileProblem(w, 409, workspaceCodeDaemonTimeout, "The session daemon did not provide a workspace file.")
		return
	}
	// Validate authority again after waiting: a reconnect or launch replacement cannot supply data.
	if a.hub.GetDaemon(dc.ID) != dc || a.hub.FindDaemonForSession(sid) != dc {
		workspaceFileProblem(w, 409, workspaceCodeChanged, "The session workspace changed while opening the file.")
		return
	}
	if dc.RuntimeTokenID != "" {
		if err := db.ValidateRuntimeBinding(ctx, a.dbPool, sid, dc.RuntimeTokenID, false); err != nil {
			workspaceFileProblem(w, 409, workspaceCodeChanged, "The session workspace changed while opening the file.")
			return
		}
	}
	if !res.OK {
		status, code, message := 409, workspaceCodeUnavailable, "The session workspace is unavailable."
		switch res.ErrorCode {
		case "busy":
			code, message = workspaceCodeBusy, "The desktop's file reader is busy."
		case "forbidden":
			status, code = 403, workspaceCodeForbidden
			message = "Blerg could not safely open that workspace file."
			if strings.HasPrefix(res.Error, "Blerg doesn't open files that usually hold secrets") {
				message = "Blerg doesn't open files that usually hold secrets (restricted filename)."
			}
		case "not_found":
			status, code = 404, workspaceCodeNotFound
			message = "The workspace file was not found."
		case "too_large":
			status, code = 413, workspaceCodeTooLarge
			message = "The workspace file is larger than its configured size limit."
		}
		workspaceFileProblem(w, status, code, message)
		return
	}
	if !workspaceReplyPathAllowed(name, res) || res.Size < 0 || res.Size > workspaceFileMaxBytes || len(res.ContentB64) > base64.StdEncoding.EncodedLen(workspaceFileMaxBytes) || res.Truncated {
		workspaceFileProblem(w, 409, workspaceCodeInvalid, "The daemon returned an invalid workspace file.")
		return
	}
	// Strict decoding checks padding bits; rejecting CR/LF also enforces the canonical
	// encoding without allocating another full-size decoded or re-encoded copy.
	if strings.ContainsAny(res.ContentB64, "\r\n") {
		workspaceFileProblem(w, 409, workspaceCodeInvalid, "The daemon returned an invalid workspace file.")
		return
	}
	decoded := io.LimitReader(base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(res.ContentB64)), res.Size+1)
	head, err := io.ReadAll(io.LimitReader(decoded, 512))
	if err != nil {
		workspaceFileProblem(w, 409, workspaceCodeInvalid, "The daemon returned an invalid workspace file.")
		return
	}
	rest, err := io.Copy(io.Discard, decoded)
	if err != nil || int64(len(head))+rest != res.Size {
		workspaceFileProblem(w, 409, workspaceCodeInvalid, "The daemon returned an invalid workspace file.")
		return
	}
	if _, err := time.Parse(time.RFC3339Nano, res.Mtime); err != nil {
		workspaceFileProblem(w, 409, workspaceCodeInvalid, "The daemon returned an invalid workspace file.")
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
		latest, err := db.GetSession(fenced, a.dbPool, sid)
		if err != nil || latest == nil || (latest.Private && !ownsSession(p.Sub, latest)) || (!ownsSession(p.Sub, latest) && !p.Has(insightsAdminCap)) {
			return errWorkspaceUnavailable
		}
		switch latest.Status {
		case "ended", "stopped", "error", "disconnected":
			return errWorkspaceUnavailable
		}
		if viewerBinding.LaunchGeneration != "" {
			manifest, generation, err := loadExplorerBinding(fenced, a.dbPool, sid)
			authorized, ok := dc.authorizedWorkspaceRuntime(sid)
			if err != nil || !ok || authorized != viewerBinding.WorkspaceRuntimeRef || manifest.LaunchGeneration != viewerBinding.LaunchGeneration || generation != viewerBinding.Generation {
				return errWorkspaceUnavailable
			}
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
		err = withWorkspaceDesktopFence(ctx, a.dbPool, dc, sid, func(fenced context.Context) error {
			if !a.hub.desktopSessionVisible(fenced, p.Sub, row) {
				return errWorkspaceUnavailable
			}
			return send(fenced)
		})
	}
	// A fence commit can fail after the callback already started the HTTP response.
	// Never append a second JSON document (or retry a possibly partial file response).
	if err != nil && !responseStarted {
		if a.recheckWorkspaceFileAccess(ctx, w, p, sid) {
			return
		}
		workspaceFileProblem(w, 409, workspaceCodeChanged, "The session workspace changed while opening the file.")
	}
}
