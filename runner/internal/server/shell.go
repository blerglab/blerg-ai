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

	"github.com/blerglab/blerg-ai/contracts/shellpolicy"
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
	policy, _, err := s.authorize(ctx, a, false)
	return policy, err
}

func (s shellAuthority) AuthorizePersistent(ctx context.Context, a ownershell.Actor) (protocol.ShellPolicy, time.Time, error) {
	return s.authorize(ctx, a, true)
}

func (s shellAuthority) authorizationCall(ctx context.Context, request shellpolicy.AuthorizationRequest) (int, []byte, error) {
	body, _ := json.Marshal(request)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/internal/shell/authorize", bytes.NewReader(body))
	if err != nil {
		return 0, nil, ownershell.ErrUnknown
	}
	req.Header.Set("X-Internal-Key", s.key)
	req.Header.Set("Content-Type", "application/json")
	// Core authorization is a fixed authenticated endpoint. A redirect is an
	// unknown observation, never permission to send the key or login elsewhere.
	client := *s.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, ownershell.ErrUnknown
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2049))
	if err != nil || len(data) > 2048 {
		return 0, nil, ownershell.ErrUnknown
	}
	return resp.StatusCode, data, nil
}

func (s shellAuthority) authorize(ctx context.Context, a ownershell.Actor, requireTyped bool) (protocol.ShellPolicy, time.Time, error) {
	var policy protocol.ShellPolicy
	if a.Kind != "human" || a.Owner == "" || a.Login == "" {
		return policy, time.Time{}, ownershell.ErrDenied
	}
	if s.url == "" || s.key == "" || s.client == nil {
		return policy, time.Time{}, ownershell.ErrUnknown
	}
	request := shellpolicy.AuthorizationRequest{Owner: a.Owner, Login: a.Login, Nonce: shellRandom()}
	if request.Nonce == "" {
		return policy, time.Time{}, ownershell.ErrUnknown
	}
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, data, err := s.authorizationCall(call, request)
	if err != nil {
		return policy, time.Time{}, err
	}
	if status == http.StatusBadRequest && !requireTyped {
		// Older core versions reject the nonce field. Only the ordinary v1 channel
		// may retry its original exact-actor request; persistence never falls back.
		request.Nonce = ""
		status, data, err = s.authorizationCall(call, request)
		if err != nil {
			return policy, time.Time{}, err
		}
	}
	if status == http.StatusForbidden {
		var denial shellpolicy.AuthorizationDenial
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if dec.Decode(&denial) == nil && dec.Decode(new(any)) == io.EOF && denial.Matches(request) {
			return policy, time.Time{}, ownershell.Refuse(ownershell.ReasonAuthority)
		}
		return policy, time.Time{}, ownershell.ErrUnknown
	}
	if status != http.StatusOK {
		return policy, time.Time{}, ownershell.ErrUnknown
	}
	var approval shellpolicy.AuthorizationAllowed
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&approval) != nil || dec.Decode(new(any)) != io.EOF {
		return policy, time.Time{}, ownershell.ErrUnknown
	}
	if approval.Matches(request, time.Now().Unix()) {
		return approval.ShellPolicy, time.Unix(approval.LoginExpiresAt, 0), nil
	}
	if !requireTyped && approval.Legacy() {
		return approval.ShellPolicy, time.Time{}, nil
	}
	return policy, time.Time{}, ownershell.ErrUnknown
}

// shellResolver adds persisted owner/session checks to the P0b runtime adapter. Neither an
// admin's viewing permission nor a browser-supplied runtime label is shell authority.
type shellResolver struct {
	api     *API
	runtime ownershell.Resolver
}

func (s shellResolver) ConsumePersistentConsent(ctx context.Context, a ownershell.Actor, b ownershell.Binding) (string, error) {
	provider, ok := s.runtime.(ownershell.PersistentConsentProvider)
	if !ok {
		return "", ownershell.ErrDenied
	}
	return provider.ConsumePersistentConsent(ctx, a, b)
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
		return ownershell.Binding{Session: id, Owner: *row.SpawningAccountID, RuntimeTargetID: derefOrEmpty(row.RuntimeTargetID)}, ownershell.Refuse(ownershell.ReasonRuntimeBusy)
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
	if err != nil {
		return nil, nil, ownershell.Binding{}, false, ownershell.ErrUnknown
	}
	retainedAbsence := row != nil && row.EndReason == nil && (row.EndedAt != nil || row.Status == "error") && s.api.retainedDaemonAbsence(ctx, id)
	if row == nil || row.SpawningAccountID == nil || *row.SpawningAccountID == "" || row.RestrictTools || row.ParentSessionID != nil || row.CronID != nil || row.StartedByKind == "cron" || row.EndedAt != nil && !retainedAbsence || row.ArchivedAt != nil || row.DiscardRequestedAt != nil {
		return nil, nil, ownershell.Binding{}, false, ownershell.ErrDenied
	}
	if row.Status == "starting" || row.Status == "disconnected" {
		return nil, nil, ownershell.Binding{Session: id, Owner: *row.SpawningAccountID, RuntimeTargetID: derefOrEmpty(row.RuntimeTargetID)}, false, ownershell.Refuse(ownershell.ReasonNotReady)
	}
	if (!isLiveStatus(row.Status) && !retainedAbsence) || row.Runtime == nil {
		return nil, nil, ownershell.Binding{}, false, ownershell.ErrDenied
	}
	w, err := db.GetSessionReaderWorkspace(ctx, s.api.dbPool, id)
	if err != nil {
		return nil, nil, ownershell.Binding{}, false, ownershell.ErrUnknown
	}
	if w == nil {
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
	if ownershell.Unknown(err) {
		return ownershell.Binding{Session: id, Owner: *row.SpawningAccountID, RuntimeTargetID: derefOrEmpty(row.RuntimeTargetID)}, false, ownershell.ErrUnknown
	}
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
		return ownershell.Binding{Session: id, Owner: *row.SpawningAccountID, RuntimeTargetID: derefOrEmpty(row.RuntimeTargetID)}, attachable && missing, ownershell.Refuse(reason)
	}
	b.RuntimeTargetID = derefOrEmpty(row.RuntimeTargetID)
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
	m.SetNameCleanup(a.forgetShellName)
	a.shells = m
	a.hub.mu.Lock()
	a.hub.shellCustodian = m.Incarnation()
	a.hub.mu.Unlock()
	_, persistentReady := supervisor.(ownershell.PersistentSupervisor)
	a.hub.shellCustodyReady.Store(persistentReady)
	go a.watchShellLimits(ctx)
	go monitorShellControl(ctx, authority, m)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		defer m.Shutdown(context.WithoutCancel(ctx))
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
	mux.HandleFunc("GET /api/sessions/{id}/shell-consents", a.HandlePersistentConsentList)
	mux.HandleFunc("GET /api/sessions/{id}/shells", a.HandleListShells)
	mux.HandleFunc("GET /api/shells/{handle}/socket", a.HandleShellSocket)
	mux.HandleFunc("DELETE /api/shells/{handle}", a.HandleCloseShell)
	mux.HandleFunc("PATCH /api/shells/{handle}", a.HandleRenameShell)
	mux.HandleFunc("GET /api/shells/{handle}/details", a.HandleShellDetails)
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
	return a.checkShellSessionOwner(ctx, actor, id) == nil
}
func (a *API) checkShellSessionOwner(ctx context.Context, actor ownershell.Actor, id string) error {
	if a.dbPool == nil || actor.Owner == "" || !db.ValidUUID(id) {
		return ownershell.ErrDenied
	}
	row, err := db.GetSession(ctx, a.dbPool, id)
	if err != nil {
		return ownershell.ErrUnknown
	}
	if row == nil || row.SpawningAccountID == nil || *row.SpawningAccountID != actor.Owner {
		return ownershell.ErrDenied
	}
	return nil
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
	// A human may choose a name and privileged host mode; command, env and cwd stay trusted.
	body, err := io.ReadAll(io.LimitReader(r.Body, 2049))
	var options struct {
		Name       *string `json:"name"`
		Privileged *bool   `json:"privileged,omitempty"`
	}
	if err != nil || len(body) > 2048 {
		writeError(w, 400, "invalid shell options")
		return
	}
	if len(body) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if dec.Decode(&options) != nil || options.Name == nil && options.Privileged == nil || dec.Decode(new(any)) != io.EOF {
			writeError(w, 400, "invalid shell options")
			return
		}
		if options.Name != nil {
			name, err := ownershell.NormalizeName(*options.Name)
			if err != nil {
				writeError(w, 400, "invalid shell name")
				return
			}
			options.Name = &name
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	// Visibility and admin capabilities are not shell ownership. Resolve the
	// exact persisted owner before querying any private runtime or readiness.
	if a.dbPool != nil {
		if err := a.checkShellSessionOwner(ctx, actor, r.PathValue("id")); err != nil {
			shellError(w, err)
			return
		}
	}
	if a.shellRuntime != nil {
		binding, err := (shellResolver{a, a.shellRuntime}).Resolve(ctx, r.PathValue("id"))
		if err == nil && binding.Owner == actor.Owner && binding.Runtime == "host" && (!binding.HostConsentValid && binding.ShellLink.Version != 2 || binding.ShellLink.Version == 2 && !a.shellRuntime.readyPersistentConsent(actor, binding)) {
			writeError(w, http.StatusPreconditionRequired, "host_consent_required")
			return
		}
	}
	info, err := a.shells.OpenPrivileged(ctx, actor, r.PathValue("id"), options.Privileged != nil && *options.Privileged)
	if err != nil {
		shellError(w, err)
		return
	}
	if options.Name != nil {
		named, nameErr := a.shells.Rename(ctx, actor, info.Handle, *options.Name, a.persistShellName(actor))
		if nameErr != nil {
			cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			_ = a.shells.Close(cleanup, actor, info.Handle)
			cancelCleanup()
			shellError(w, nameErr)
			return
		}
		info = named
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
	case ownershell.Unknown(err):
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "shell temporarily unavailable", "code": string(ownershell.ReasonUnavailable)})
	default:
		body := map[string]string{"error": "shell unavailable", "code": string(ownershell.RefusalReason(err))}
		if cause := ownershell.StartCause(err); cause != "" {
			body["cause"] = cause
		}
		writeJSON(w, http.StatusForbidden, body)
	}
}

// HandleShellDetails returns bounded end metadata even after the terminal disappears.
// It never exposes the handle, binding, terminal history, or arbitrary runtime errors.
func (a *API) HandleShellDetails(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("Origin") != "" && !shellOrigin(r) {
		writeError(w, 403, "forbidden origin")
		return
	}
	actor, ok := a.shellActor(w, r)
	if !ok {
		return
	}
	if a.shells == nil {
		writeError(w, 503, "shell unavailable")
		return
	}
	reason, _, ok := a.shells.EndReason(actor, r.PathValue("handle"))
	if !ok {
		writeError(w, 404, "end details unavailable")
		return
	}
	writeJSON(w, 200, map[string]string{"reason": string(reason)})
}

func (a *API) forgetShellName(ctx context.Context, actor ownershell.Actor, session, shell string) {
	if a.dbPool == nil {
		return
	}
	if err := db.DeleteShellName(ctx, a.dbPool, session, actor.Owner, actor.Login, shell); err != nil {
		log.Printf("owner shell name cleanup failed: shell_id=%s", shell)
	}
}

func (a *API) persistShellName(actor ownershell.Actor) func(context.Context, ownershell.Info) error {
	return func(ctx context.Context, info ownershell.Info) error {
		if a.dbPool == nil {
			return nil
		}
		return db.SaveShellName(ctx, a.dbPool, info.Session, actor.Owner, actor.Login, info.Shell, info.Name)
	}
}

// HandleRenameShell accepts presentation metadata only, with live shell ownership checks.
func (a *API) HandleRenameShell(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !shellOrigin(r) {
		writeError(w, 403, "forbidden origin")
		return
	}
	actor, ok := a.shellActor(w, r)
	if !ok {
		return
	}
	if a.shells == nil {
		writeError(w, 503, "shell unavailable")
		return
	}
	var body struct {
		Name *string `json:"name"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 2048))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil || body.Name == nil || dec.Decode(new(any)) != io.EOF {
		writeError(w, 400, "name required")
		return
	}
	name, err := ownershell.NormalizeName(*body.Name)
	if err != nil {
		writeError(w, 400, "Use at most 120 characters without control characters.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	info, err := a.shells.Rename(ctx, actor, r.PathValue("handle"), name, a.persistShellName(actor))
	if err != nil {
		shellError(w, err)
		return
	}
	writeJSON(w, 200, info)
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
	// Only coarse platform labels reach diagnostics; the full User-Agent is discarded.
	ua := r.UserAgent()
	device := "unknown"
	switch {
	case strings.Contains(ua, "Android"):
		device = "Android"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		device = "iOS"
	case strings.Contains(ua, "Windows"):
		device = "Windows"
	case strings.Contains(ua, "Mac"):
		device = "macOS"
	case strings.Contains(ua, "Linux"):
		device = "Linux"
	}
	device = ownershell.DeviceLabel(device, r.URL.Query().Get("device"))
	at, err := a.shells.AttachDevice(r.Context(), actor, handle, r.URL.Query().Get("takeover") == "true", r.URL.Query().Get("replay") == "1", device)
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
	log.Printf("owner shell attachment: shell_id=%s event=attach takeover=%t", at.Shell, r.URL.Query().Get("takeover") == "true")
	defer log.Printf("owner shell attachment: shell_id=%s event=detach", at.Shell)
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
			select {
			case device := <-at.TakenOver:
				if conn.SetWriteDeadline(time.Now().Add(time.Second)) == nil {
					_ = conn.WriteJSON(map[string]string{"type": "control", "reason": "taken_over", "device": device})
					_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "taken over"), time.Now().Add(time.Second))
				}
				return
			default:
			}
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
	if err := a.checkShellSessionOwner(ctx, actor, r.PathValue("id")); err != nil {
		shellError(w, err)
		return
	}
	infos, err := a.shells.List(ctx, actor, r.PathValue("id"))
	if err != nil {
		shellError(w, err)
		return
	}
	if a.dbPool != nil {
		names, err := db.ShellNames(ctx, a.dbPool, r.PathValue("id"), actor.Owner, actor.Login)
		if err != nil {
			shellError(w, ownershell.ErrUnknown)
			return
		}
		for i := range infos {
			infos[i].Name = names[infos[i].Shell]
		}
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
		return conn.WriteJSON(map[string]any{"type": "replay", "bytes": len(at.Replay), "seq": at.ReplayEnd, "gap": at.ReplayGap})
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

func (s shellResolver) ReleasePersistentConsent(ctx context.Context, a ownershell.Actor, b ownershell.Binding, id string) {
	if provider, ok := s.runtime.(ownershell.PersistentConsentReleaser); ok {
		provider.ReleasePersistentConsent(ctx, a, b, id)
	}
}
