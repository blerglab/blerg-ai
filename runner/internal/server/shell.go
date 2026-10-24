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
	row, err := db.GetSession(ctx, s.api.dbPool, id)
	if err != nil || row == nil || row.SpawningAccountID == nil || *row.SpawningAccountID == "" || row.ParentSessionID != nil || row.CronID != nil || row.StartedByKind == "cron" || row.EndedAt != nil || row.ArchivedAt != nil || row.DiscardRequestedAt != nil {
		return ownershell.Binding{}, ownershell.ErrDenied
	}
	if row.Status != "running" && row.Status != "idle" && row.Status != "waiting" {
		return ownershell.Binding{}, ownershell.ErrDenied
	}
	w, err := db.GetSessionWorkspace(ctx, s.api.dbPool, id)
	if err != nil || w == nil || w.Manifest == nil || w.Manifest.Validate() != nil {
		return ownershell.Binding{}, ownershell.ErrDenied
	}
	if row.Runtime == nil {
		return ownershell.Binding{}, ownershell.ErrDenied
	}
	persistedRuntime := *row.Runtime
	switch persistedRuntime {
	case "daemon":
		persistedRuntime = "host"
	case "docker":
		persistedRuntime = "sandbox"
	}
	if persistedRuntime != w.Manifest.Runtime {
		return ownershell.Binding{}, ownershell.ErrDenied
	}
	generation, prepared, err := db.GetWorkspaceRuntime(ctx, s.api.dbPool, id)
	if err != nil || prepared == nil || prepared.Generation != generation || prepared.Validate(*w.Manifest) != nil {
		return ownershell.Binding{}, ownershell.ErrDenied
	}
	b, err := s.runtime.Resolve(ctx, id)
	if err != nil || b.Session != id || b.Owner != *row.SpawningAccountID || b.Owner != w.Manifest.OwnerID || b.Launch != w.Manifest.LaunchGeneration || b.Runtime != w.Manifest.Runtime || b.ManifestDigest != w.Manifest.Digest || b.RuntimeGeneration != generation || b.Root != prepared.Root || b.Launch != prepared.LaunchGeneration {
		return ownershell.Binding{}, ownershell.ErrDenied
	}
	return b, nil
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
	default:
		writeError(w, http.StatusForbidden, "shell unavailable")
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
	at, err := a.shells.Attach(r.Context(), actor, handle, r.URL.Query().Get("takeover") == "true")
	if err != nil {
		shellError(w, err)
		return
	}
	defer a.shells.Detach(actor, handle, at.ID)
	up := websocket.Upgrader{Subprotocols: []string{"bearer"}, CheckOrigin: shellOrigin, ReadBufferSize: ownershell.MaxFrame, WriteBufferSize: ownershell.MaxFrame}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(24 << 10)
	stop := make(chan struct{})
	defer close(stop)
	go func(parent context.Context) {
		defer func() { _ = conn.Close() }()
		for {
			select {
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
		if err != nil {
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
	infos, err := a.shells.List(ctx, actor, r.PathValue("id"))
	if err != nil {
		shellError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, infos)
}
