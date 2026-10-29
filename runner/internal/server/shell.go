package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// shellAuthority uses only the authenticated control plane, never credential-fetch fallback.
type shellAuthority struct {
	url, key string
	client   *http.Client
}

func (s shellAuthority) Authorize(ctx context.Context, a ownershell.Actor) (protocol.ShellPolicy, error) {
	var p protocol.ShellPolicy
	if s.url == "" || s.key == "" || a.Kind != "human" || a.Owner == "" || a.Login == "" {
		return p, ownershell.ErrDenied
	}
	body, _ := json.Marshal(struct {
		Owner string `json:"owner_id"`
		Login string `json:"login_id"`
	}{a.Owner, a.Login})
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(call, http.MethodPost, s.url+"/internal/shell/authorize", bytes.NewReader(body))
	if err != nil {
		return p, ownershell.ErrDenied
	}
	req.Header.Set("X-Internal-Key", s.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return p, ownershell.ErrDenied
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return p, ownershell.ErrDenied
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2049))
	if err != nil || len(data) > 2048 || json.Unmarshal(data, &p) != nil {
		return protocol.ShellPolicy{}, ownershell.ErrDenied
	}
	return p, nil
}

// shellResolver adds persisted owner/session checks to the P0b runtime adapter. Neither an
// admin's viewing permission nor a browser-supplied runtime label is shell authority.
type shellResolver struct {
	api     *API
	runtime ownershell.Resolver
}

func (s shellResolver) Resolve(ctx context.Context, id string) (ownershell.Binding, error) {
	if s.runtime == nil || s.api.dbPool == nil {
		return ownershell.Binding{}, ownershell.ErrDenied
	}
	b, attachable, err := s.resolve(ctx, id)
	if err == nil || !attachable {
		return b, err
	}
	// Attaching a legacy workspace is setup, not verification: it is rate limited on the
	// desktop. Shell liveness checks run every second, so an attached, bound workspace is
	// verified like any other launch (persisted binding plus the runtime's own description).
	// Only a resolution that found the launch missing, such as after a desktop restart,
	// attaches again and retries once. A definite refusal is never retried.
	// Attachment retries must end before the reserve begins, so a busy desktop always
	// leaves time for the fresh persisted verification below.
	end, ok := shellAttachDeadline(ctx)
	if !ok {
		return b, err
	}
	attach, cancel := context.WithDeadline(ctx, end)
	err = s.api.ensureLegacyWorkspace(attach, id)
	cancel()
	if err != nil {
		if !errors.Is(err, errLegacyWorkspaceBusy) {
			return ownershell.Binding{}, ownershell.ErrDenied
		}
		// Busy proves nothing, so every persisted check that can deny runs again first,
		// under the caller's own deadline, and busy carries only freshly persisted identity.
		if hook, _ := shellBusyRecheckHook.Load().(func(context.Context)); hook != nil {
			hook(ctx)
		}
		row, _, b, attachable, err := s.persisted(ctx, id)
		if err != nil || !attachable {
			if err == nil {
				err = ownershell.ErrDenied
			}
			return b, err
		}
		return ownershell.Binding{Session: id, Owner: *row.SpawningAccountID}, ownershell.Refuse(ownershell.ReasonRuntimeBusy)
	}
	b, _, err = s.resolve(ctx, id)
	return b, err
}

// shellVerifyReserve is the part of a resolution's deadline kept for the fresh persisted
// verification after a busy attachment. Attachment never runs into it.
const shellVerifyReserve = 750 * time.Millisecond

// shellAttachDeadline is the caller's deadline minus the verification reserve. Without a
// caller deadline, attachment keeps its own bound and verification runs under the caller's
// context. ok is false when no attachment time is left.
func shellAttachDeadline(ctx context.Context) (time.Time, bool) {
	deadline, has := ctx.Deadline()
	if !has {
		return time.Now().Add(legacyAttachBusyBudget), true
	}
	end := deadline.Add(-shellVerifyReserve)
	return end, time.Now().Before(end)
}

// shellBusyRecheckHook lets tests delay the fresh verification; nil in production.
var shellBusyRecheckHook atomic.Value

// persisted runs every check against the server's own records. A non-nil error is a final
// answer. attachable reports a legacy launch that is not yet completely attached or bound.
func (s shellResolver) persisted(ctx context.Context, id string) (*db.SessionRow, *db.SessionWorkspace, ownershell.Binding, bool, error) {
	row, err := db.GetSession(ctx, s.api.dbPool, id)
	if err != nil || row == nil || row.SpawningAccountID == nil || *row.SpawningAccountID == "" || row.RestrictTools || row.ParentSessionID != nil || row.CronID != nil || row.StartedByKind == "cron" || row.EndedAt != nil || row.ArchivedAt != nil || row.DiscardRequestedAt != nil {
		return nil, nil, ownershell.Binding{}, false, ownershell.ErrDenied
	}
	if row.Status == "starting" || row.Status == "disconnected" {
		return nil, nil, ownershell.Binding{Session: id, Owner: *row.SpawningAccountID}, false, ownershell.Refuse(ownershell.ReasonNotReady)
	}
	if !isLiveStatus(row.Status) || row.Runtime == nil {
		return nil, nil, ownershell.Binding{}, false, ownershell.ErrDenied
	}
	w, err := db.GetSessionReaderWorkspace(ctx, s.api.dbPool, id)
	if err != nil || w == nil {
		return nil, nil, ownershell.Binding{}, false, ownershell.ErrDenied
	}
	if w.Manifest == nil {
		return row, w, ownershell.Binding{}, true, nil
	}
	if w.Manifest.Validate() != nil {
		return nil, nil, ownershell.Binding{}, false, ownershell.ErrDenied
	}
	persistedRuntime := *row.Runtime
	switch persistedRuntime {
	case "daemon":
		persistedRuntime = "host"
	case "docker":
		persistedRuntime = "sandbox"
	}
	if persistedRuntime != w.Manifest.Runtime || w.Manifest.OwnerID != *row.SpawningAccountID {
		return nil, nil, ownershell.Binding{}, false, ownershell.ErrDenied
	}
	return row, w, ownershell.Binding{}, strings.HasPrefix(w.Manifest.LaunchGeneration, "legacy-"), nil
}

// resolve verifies the persisted launch against the runtime's description. attachable
// reports a legacy session whose launch was missing, so attachment may complete it.
func (s shellResolver) resolve(ctx context.Context, id string) (ownershell.Binding, bool, error) {
	row, w, b, attachable, err := s.persisted(ctx, id)
	if err != nil {
		return b, false, err
	}
	if w.Manifest == nil {
		return ownershell.Binding{}, attachable, ownershell.ErrDenied
	}
	generation, prepared, err := db.GetWorkspaceRuntime(ctx, s.api.dbPool, id)
	if err != nil || prepared == nil || prepared.Generation != generation || prepared.Validate(*w.Manifest) != nil {
		return ownershell.Binding{}, attachable, ownershell.ErrDenied
	}
	b, err = s.runtime.Resolve(ctx, id)
	if err != nil {
		// Only persisted identity is trusted on refusal. The manager uses it to
		// restrict safe diagnostics to the owner; no runtime authority survives.
		reason := ownershell.RefusalReason(err)
		if reason == ownershell.ReasonRuntimeBusy {
			// Only the server's own attachment backoff may report busy; a runtime reply cannot.
			reason = ownershell.ReasonUnavailable
		}
		// Only a launch the runtime does not hold (binding refused: not registered or retiring,
		// or no answer) may be completed by attaching. Identity, consent, cleanup, capability
		// and policy refusals are definite.
		missing := reason == ownershell.ReasonRuntimeBinding || reason == ownershell.ReasonUnavailable
		return ownershell.Binding{Session: id, Owner: *row.SpawningAccountID}, attachable && missing, ownershell.Refuse(reason)
	}
	if b.Session != id || b.Owner != *row.SpawningAccountID || b.Owner != w.Manifest.OwnerID || b.Launch != w.Manifest.LaunchGeneration || b.Runtime != w.Manifest.Runtime || b.ManifestDigest != w.Manifest.Digest || b.RuntimeGeneration != generation || b.Root != prepared.Root || b.Launch != prepared.LaunchGeneration {
		return ownershell.Binding{}, false, ownershell.ErrDenied
	}
	return b, false, nil
}

type shellAuditLog struct{}

func (shellAuditLog) Record(a ownershell.Audit) {
	b, err := json.Marshal(a)
	if err == nil {
		log.Printf("owner shell audit: %s", b)
	}
}

// StartShellChannel is called once at startup, after core credentials are configured. A nil
// runtime adapter fails closed until P0b and the concrete supervisors are integrated.
func (a *API) StartShellChannel(ctx context.Context, resolver ownershell.Resolver, supervisor ownershell.Supervisor) error {
	authority := shellAuthority{a.coreURL, a.coreInternalKey, &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 3 * time.Second}}}
	m, err := ownershell.New(authority, shellResolver{a, resolver}, supervisor, shellAuditLog{}, nil)
	if err != nil {
		return err
	}
	a.shells = m
	go a.watchShellLimits(ctx)
	go monitorShellControl(ctx, authority, m)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		defer m.Unavailable(context.WithoutCancel(ctx))
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				call, cancel := context.WithTimeout(ctx, 3*time.Second)
				m.Tick(call)
				cancel()
			}
		}
	}()
	return nil
}

// monitorShellControl fails closed immediately on stream interruption and on a stalled heartbeat.
// Every successful reconnect starts with a fresh core snapshot, never cached enablement.
func monitorShellControl(ctx context.Context, s shellAuthority, m *ownershell.Manager) {
	for ctx.Err() == nil {
		watchShellControl(ctx, s, m)
		m.Unavailable(context.WithoutCancel(ctx))
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func watchShellControl(ctx context.Context, s shellAuthority, m *ownershell.Manager) {
	if s.url == "" || s.key == "" {
		return
	}
	call, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(call, http.MethodPost, s.url+"/internal/shell/control", nil)
	if err != nil {
		return
	}
	req.Header.Set("X-Internal-Key", s.key)
	resp, err := s.client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return
	}
	// Reader sends at most one bounded snapshot ahead. Cancellation always closes the response.
	lines := make(chan []byte, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(lines)
		scan := bufio.NewScanner(resp.Body)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			line := append([]byte(nil), scan.Bytes()...)
			select {
			case lines <- line:
			case <-call.Done():
				return
			}
		}
	}()
	defer func() { cancel(); _ = resp.Body.Close(); <-done }()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	seen := map[string]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case line, ok := <-lines:
			if !ok {
				return
			}
			var state struct {
				Policy      protocol.ShellPolicy `json:"policy"`
				Revocations []struct {
					Kind  string    `json:"kind"`
					Value string    `json:"value"`
					At    time.Time `json:"revoked_at"`
				} `json:"revocations"`
			}
			if json.Unmarshal(line, &state) != nil {
				return
			}
			m.SetPolicy(ctx, state.Policy)
			nextSeen := make(map[string]time.Time, len(state.Revocations))
			for _, r := range state.Revocations {
				k := r.Kind + ":" + r.Value
				if old, ok := seen[k]; !ok || r.At.After(old) {
					switch r.Kind {
					case "sid":
						m.Revoke(ctx, "", r.Value, "")
					case "sub", "lineage":
						m.Revoke(ctx, r.Value, "", "")
					}
				}
				nextSeen[k] = r.At
			}
			seen = nextSeen
			tick, cancelTick := context.WithTimeout(ctx, 3*time.Second)
			m.Tick(tick)
			cancelTick()
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(3 * time.Second)
		}
	}
}
func (a *API) RegisterShellRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{id}/shell-capability", a.HandleShellCapability)
	mux.HandleFunc("POST /api/sessions/{id}/shells", a.HandleOpenShell)
	mux.HandleFunc("POST /api/sessions/{id}/shell-consent/{action}", a.HandleShellConsent)
	mux.HandleFunc("GET /api/sessions/{id}/shells", a.HandleListShells)
	mux.HandleFunc("GET /api/shells/{handle}/socket", a.HandleShellSocket)
	mux.HandleFunc("DELETE /api/shells/{handle}", a.HandleCloseShell)
	mux.HandleFunc("GET /api/shells/settings", a.HandleGetShellSettings)
	mux.HandleFunc("PUT /api/shells/settings", a.HandlePutShellSettings)
}
func shellOrigin(r *http.Request) bool {
	return r.Header.Get("Origin") != "" && checkBrowserWSOrigin(r)
}
func (a *API) shellActor(w http.ResponseWriter, r *http.Request) (ownershell.Actor, bool) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return ownershell.Actor{}, false
	}
	if p.Kind != "human" || p.Sid == "" {
		writeError(w, http.StatusForbidden, "human sign-in required")
		return ownershell.Actor{}, false
	}
	return ownershell.Actor{Kind: p.Kind, Owner: p.Sub, Login: p.Sid}, true
}

// Browser ownership must be checked before resolution: legacy resolution can
// attach a workspace and query the desktop even when no shell is available.
func (a *API) ownsShellSession(ctx context.Context, actor ownershell.Actor, id string) bool {
	if a.dbPool == nil || actor.Owner == "" {
		return false
	}
	row, err := db.GetSession(ctx, a.dbPool, id)
	return err == nil && row != nil && row.SpawningAccountID != nil && *row.SpawningAccountID == actor.Owner
}

func (a *API) HandleOpenShell(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !shellOrigin(r) {
		writeError(w, http.StatusForbidden, "forbidden origin")
		return
	}
	actor, ok := a.shellActor(w, r)
	if !ok {
		return
	}
	if a.shells == nil {
		writeError(w, http.StatusServiceUnavailable, "shell unavailable")
		return
	}
	// The shell has fixed trusted argv/cwd; browser command, env and path input is forbidden.
	body, err := io.ReadAll(io.LimitReader(r.Body, 3))
	if err != nil || len(body) != 0 {
		writeError(w, http.StatusBadRequest, "body must be empty")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	// Visibility and admin capabilities are not shell ownership. Resolve the
	// exact persisted owner before querying any private runtime or readiness.
	if a.dbPool != nil {
		if !a.ownsShellSession(ctx, actor, r.PathValue("id")) {
			shellError(w, ownershell.ErrDenied)
			return
		}
	}
	if a.shellRuntime != nil {
		binding, err := (shellResolver{a, a.shellRuntime}).Resolve(ctx, r.PathValue("id"))
		if err == nil && binding.Owner == actor.Owner && binding.Runtime == "host" && !binding.HostConsentValid {
			writeError(w, http.StatusPreconditionRequired, "host_consent_required")
			return
		}
	}
	info, err := a.shells.Open(ctx, actor, r.PathValue("id"))
	if err != nil {
		shellError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, info)
}
func shellError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ownershell.ErrBusy):
		writeError(w, http.StatusConflict, "shell busy")
	case errors.Is(err, ownershell.ErrLimit):
		writeError(w, http.StatusTooManyRequests, "shell limit exceeded")
	case ownershell.Busy(err):
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "desktop busy", "code": string(ownershell.ReasonRuntimeBusy)})
	default:
		body := map[string]string{"error": "shell unavailable", "code": string(ownershell.RefusalReason(err))}
		if cause := ownershell.StartCause(err); cause != "" {
			body["cause"] = cause
		}
		writeJSON(w, http.StatusForbidden, body)
	}
}
func (a *API) HandleCloseShell(w http.ResponseWriter, r *http.Request) {
	if !shellOrigin(r) {
		writeError(w, http.StatusForbidden, "forbidden origin")
		return
	}
	actor, ok := a.shellActor(w, r)
	if !ok {
		return
	}
	if a.shells == nil {
		writeError(w, http.StatusServiceUnavailable, "shell unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := a.shells.Close(ctx, actor, r.PathValue("handle")); err != nil {
		shellError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *API) HandleShellSocket(w http.ResponseWriter, r *http.Request) {
	if !shellOrigin(r) {
		writeError(w, http.StatusForbidden, "forbidden origin")
		return
	}
	p, ok := a.verifyBrowserToken(wsBearerToken(r))
	if !ok || p.Kind != "human" || p.Sid == "" {
		writeError(w, http.StatusUnauthorized, "human sign-in required")
		return
	}
	if a.shells == nil {
		writeError(w, http.StatusServiceUnavailable, "shell unavailable")
		return
	}
	actor := ownershell.Actor{Kind: p.Kind, Owner: p.Sub, Login: p.Sid}
	handle := r.PathValue("handle")
	// Takeover is an explicit affirmative action; origin and exact login checks apply to it too.
	up := websocket.Upgrader{Subprotocols: []string{"bearer"}, CheckOrigin: shellOrigin, ReadBufferSize: ownershell.MaxFrame, WriteBufferSize: ownershell.MaxFrame}
	// writeEnded tells the owner's tab which inactivity limit ended the shell, if one did.
	// A browser cannot read a refused upgrade's body, so this is the socket's only frame.
	// It then starts a close handshake, so the reason is not lost to a reset from closing
	// over the browser's unread frames; the caller lets the reader drain until the reply.
	writeEnded := func(conn *websocket.Conn) bool {
		reason, after, ok := a.shells.Ended(actor, handle)
		if !ok || conn.SetWriteDeadline(time.Now().Add(3*time.Second)) != nil {
			return false
		}
		if conn.WriteJSON(map[string]any{"type": "ended", "reason": string(reason), "after_seconds": int64(after / time.Second)}) != nil {
			return false
		}
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shell ended"), time.Now().Add(time.Second))
		return true
	}
	// A client that understands replay asks for it; an older page gets live output only.
	attach := a.shells.Attach
	if r.URL.Query().Get("replay") == "1" {
		attach = a.shells.AttachWithReplay
	}
	at, err := attach(r.Context(), actor, handle, r.URL.Query().Get("takeover") == "true")
	if err != nil {
		if _, _, ended := a.shells.Ended(actor, handle); ended {
			if conn, upErr := up.Upgrade(w, r, nil); upErr == nil {
				conn.SetReadLimit(shellSocketReadLimit)
				if writeEnded(conn) {
					drainUntilClose(conn)
				}
				_ = conn.Close()
			}
			return
		}
		shellError(w, err)
		return
	}
	defer a.shells.Detach(actor, handle, at.ID)
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(shellSocketReadLimit)
	stop := make(chan struct{})
	defer close(stop)
	writerDone := make(chan struct{})
	var endedSent atomic.Bool
	go func(parent context.Context) {
		defer close(writerDone)
		// Shutdown closes output/phase too. Drain the diagnostic first so a
		// successful cleanup cannot race away the owner's final explanation.
		writeFailure := func(cause string) {
			if cause = ownershell.SafeStartCause(cause); cause != "" {
				if conn.SetWriteDeadline(time.Now().Add(3*time.Second)) == nil {
					_ = conn.WriteJSON(map[string]string{"type": "error", "cause": cause})
				}
			}
		}
		// Every exit, including a queued frame refused after retirement, says which
		// inactivity limit ended the shell before the socket closes. The writer is the
		// socket's only writer, so the reason cannot interleave with another frame. After
		// it, the reader drains to the browser's close reply (bounded) and the handler closes.
		defer func() {
			if writeEnded(conn) {
				endedSent.Store(true)
				_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
				return
			}
			_ = conn.Close()
		}()
		defer func() {
			select {
			case cause := <-at.Errors:
				writeFailure(cause)
			default:
			}
		}()
		if !a.writeShellReplay(parent, conn, actor, handle, at) {
			return
		}
		for {
			select {
			case cause := <-at.Errors:
				writeFailure(cause)
				return
			case phase, ok := <-at.Phase:
				if !ok {
					return
				}
				call, cancel := context.WithTimeout(parent, 3*time.Second)
				err := a.shells.Forward(call, actor, handle, at.ID, nil, func([]byte) error {
					if conn.SetWriteDeadline(time.Now().Add(3*time.Second)) != nil {
						return ownershell.ErrDenied
					}
					return conn.WriteJSON(map[string]string{"type": "phase", "phase": phase})
				})
				cancel()
				if err != nil {
					return
				}

			case <-stop:
				return
			case data, ok := <-at.Output:
				if !ok {
					return
				}
				ctx, cancel := context.WithTimeout(parent, 3*time.Second)
				err := a.shells.Forward(ctx, actor, handle, at.ID, data, func(b []byte) error {
					if e := conn.SetWriteDeadline(time.Now().Add(3 * time.Second)); e != nil {
						return e
					}
					return conn.WriteMessage(websocket.BinaryMessage, b)
				})
				cancel()
				if err != nil {
					return
				}
			}
		}
	}(r.Context())
	for {
		if endedSent.Load() {
			drainUntilClose(conn)
			return
		}
		if conn.SetReadDeadline(time.Now().Add(ownershell.HeartbeatLease)) != nil {
			return
		}
		var frame protocol.OwnerShellClientFrame
		if conn.ReadJSON(&frame) != nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		switch frame.Type {
		case "input":
			err = a.shells.InputFrame(ctx, actor, handle, at.ID, frame.Seq, frame.Data, frame.Human)
		case "resize":
			err = a.shells.Resize(ctx, actor, handle, at.ID, frame.Seq, frame.Cols, frame.Rows)
		case "activity":
			err = a.shells.Activity(ctx, actor, handle, at.ID)
		case "heartbeat":
			err = a.shells.Heartbeat(ctx, actor, handle, at.ID)
		default:
			err = ownershell.ErrDenied
		}
		cancel()
		if ownershell.Busy(err) && (frame.Type == "heartbeat" || frame.Type == "activity") {
			// A busy desktop proves nothing; the heartbeat lease and grant expiry still bound this.
			continue
		}
		if err != nil {
			_, _, ended := a.shells.Ended(actor, handle)
			if ended || ownershell.StartCause(err) != "" {
				select {
				case <-writerDone:
				case <-time.After(3 * time.Second):
				}
			}
			if endedSent.Load() {
				drainUntilClose(conn)
			}
			return
		}
	}
}

// shellSocketReadLimit caps every message read on an owner-shell socket.
const shellSocketReadLimit = 24 << 10

// drainUntilClose discards what the browser sent after the ended reason until its
// close reply, for at most three seconds, so closing never resets the connection over unread
// data that would discard the reason before the browser reads it.
func drainUntilClose(conn *websocket.Conn) {
	// The cap applies here too (a message over it ends the drain), and dropped messages
	// are discarded as they stream in rather than assembled in memory.
	conn.SetReadLimit(shellSocketReadLimit)
	if conn.SetReadDeadline(time.Now().Add(3*time.Second)) != nil {
		return
	}
	for {
		_, r, err := conn.NextReader()
		if err != nil {
			return
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			return
		}
	}
}

func (a *API) HandleListShells(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("Origin") != "" && !shellOrigin(r) {
		writeError(w, http.StatusForbidden, "forbidden origin")
		return
	}
	actor, ok := a.shellActor(w, r)
	if !ok {
		return
	}
	if a.shells == nil {
		writeError(w, http.StatusServiceUnavailable, "shell unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if !a.ownsShellSession(ctx, actor, r.PathValue("id")) {
		shellError(w, ownershell.ErrDenied)
		return
	}
	infos, err := a.shells.List(ctx, actor, r.PathValue("id"))
	if err != nil {
		shellError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, infos)
}

// writeShellReplay sends a requested scrollback replay before any live output:
// one {"type":"replay","bytes":N,"seq":S} notice, then exactly N bytes in
// binary frames of at most MaxFrame. Live output that follows starts at stream
// offset S. Every frame is revalidated by Forward like live output.
func (a *API) writeShellReplay(parent context.Context, conn *websocket.Conn, actor ownershell.Actor, handle string, at ownershell.Attachment) bool {
	if at.Replay == nil {
		return true
	}
	write := func(data []byte, send func() error) bool {
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		defer cancel()
		return a.shells.Forward(ctx, actor, handle, at.ID, data, func([]byte) error {
			if err := conn.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
				return err
			}
			return send()
		}) == nil
	}
	if !write(nil, func() error {
		return conn.WriteJSON(map[string]any{"type": "replay", "bytes": len(at.Replay), "seq": at.ReplayEnd})
	}) {
		return false
	}
	for rest := at.Replay; len(rest) > 0; {
		chunk := rest[:min(len(rest), ownershell.MaxFrame)]
		rest = rest[len(chunk):]
		if !write(chunk, func() error { return conn.WriteMessage(websocket.BinaryMessage, chunk) }) {
			return false
		}
	}
	return true
}
