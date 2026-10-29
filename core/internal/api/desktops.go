package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// Settings → Account → Your desktops (card #193). A desktop credential is account identity,
// so the page lives in core. The desktop records themselves (name, connection, owner and
// revision) live in the runner, which core reaches on RunnerURL with InternalKey and the
// signed-in person's account; the runner keeps every #186 guard (own or unassigned desktop,
// the exact connection the person confirmed, its audit). Enrollment codes are core's own.

func (d Deps) registerDesktopRoutes(mux *http.ServeMux) {
	person := func(h http.HandlerFunc) http.HandlerFunc { return requireHumanPrincipal(h, d, "card.read") }
	mux.HandleFunc("GET /api/desktops", person(d.handleListDesktops))
	mux.HandleFunc("PATCH /api/desktops/{id}", person(d.handleRenameDesktop))
	mux.HandleFunc("DELETE /api/desktops/{id}", person(d.handleRevokeDesktop))
	mux.HandleFunc("PUT /api/desktops/{id}/owner", person(d.handleAssignDesktop))
	mux.HandleFunc("GET /api/desktops/{id}/enrollment-connection", person(d.handleDesktopEnrollmentConnection))
	mux.HandleFunc("POST /api/desktops/{id}/enroll", person(d.handleEnrollDesktop))
	mux.HandleFunc("GET /api/desktop-enrollment-codes", person(d.handleListDesktopEnrollmentCodes))
	mux.HandleFunc("POST /api/desktop-enrollment-codes", person(d.handleIssueDesktopEnrollmentCode))
	mux.HandleFunc("DELETE /api/desktop-enrollment-codes/{id}", person(d.handleRevokeDesktopEnrollmentCode))
	mux.HandleFunc("POST /internal/desktop-enrollment-codes/redeem", d.handleRedeemDesktopEnrollmentCode)
}

// desktopPerson is the signed-in person behind a desktop action: a browser session of their
// own, never an agent token or a delegated one.
func (d Deps) desktopPerson(w http.ResponseWriter, r *http.Request) (cid.Principal, *identity.Service, bool) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := principalFromCtx(r.Context())
	if !ok || p.Sid == "" || p.OnBehalfOf != "" {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "Sign in as yourself to manage your desktops."})
		return p, nil, false
	}
	svc, ok := d.identitySvc()
	if !ok {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "Desktops are unavailable."})
		return p, nil, false
	}
	return p, svc, true
}

const desktopsUnavailable = "Your desktops are unavailable: core cannot reach the runner (BLERG_CORE_RUNNER_URL)."

// runnerDesktopCall calls one of the runner's internal desktop routes for the person p. It
// never follows redirects or logs bodies. The status and JSON body come back as the runner sent them.
func (d Deps) runnerDesktopCall(ctx context.Context, p cid.Principal, method, path string, body []byte) (int, []byte, error) {
	if d.RunnerURL == "" || d.InternalKey == "" {
		return 0, nil, errors.New("runner not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var rd io.Reader = http.NoBody
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.RunnerURL+path, rd) //nolint:gosec // G704: RunnerURL is operator configuration; paths are fixed with a UUID-shaped id
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", d.InternalKey)
	req.Header.Set("X-Blerg-Account", p.Sub)
	if p.Has("account.manage") {
		req.Header.Set("X-Blerg-Admin", "1")
	}
	client := d.RunnerClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := noRedirect.Do(req) //nolint:gosec // G704: see above; redirects are never followed
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 256<<10))
	if err != nil {
		return 0, nil, err
	}
	return res.StatusCode, raw, nil
}

// proxyDesktop relays one call and reports whether the runner answered 200.
func (d Deps) proxyDesktop(w http.ResponseWriter, r *http.Request, p cid.Principal, method, path string, body []byte) bool {
	status, raw, err := d.runnerDesktopCall(r.Context(), p, method, path, body)
	if err != nil || status == http.StatusUnauthorized || status == http.StatusNotFound && !json.Valid(raw) {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": desktopsUnavailable})
		return false
	}
	if !json.Valid(raw) {
		writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": "The runner gave an unexpected answer."})
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw) //nolint:gosec // G705: validated JSON from the runner, served as application/json
	return status == http.StatusOK
}

func desktopPathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !uuidShape.MatchString(id) {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "Desktop not found."})
		return "", false
	}
	return strings.ToLower(id), true
}

func readDesktopBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || (len(raw) > 0 && !json.Valid(raw)) {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "Invalid request."})
		return nil, false
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	return raw, true
}

func (d Deps) handleListDesktops(w http.ResponseWriter, r *http.Request) {
	p, svc, ok := d.desktopPerson(w, r)
	if !ok {
		return
	}
	status, raw, err := d.runnerDesktopCall(r.Context(), p, http.MethodGet, "/internal/desktops", nil)
	var view map[string]any
	if err != nil || status != http.StatusOK || json.Unmarshal(raw, &view) != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": desktopsUnavailable})
		return
	}
	view["owners"] = []identity.DesktopOwner{}
	if p.Has("account.manage") {
		owners, err := svc.DesktopOwners(r.Context())
		if err != nil {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "The account directory is unavailable."})
			return
		}
		view["owners"] = owners
	}
	view["actor"] = p.Sub
	writeJSON(w, view)
}

func (d Deps) handleRenameDesktop(w http.ResponseWriter, r *http.Request) {
	p, svc, ok := d.desktopPerson(w, r)
	if !ok {
		return
	}
	id, ok := desktopPathID(w, r)
	if !ok {
		return
	}
	body, ok := readDesktopBody(w, r)
	if !ok {
		return
	}
	if d.proxyDesktop(w, r, p, http.MethodPost, "/internal/desktops/"+id+"/rename", body) {
		_ = svc.RecordDesktopAudit(r.Context(), p.Sub, "desktop_renamed", id)
	}
}

// handleRevokeDesktop removes a desktop and revokes its credentials in steps that a retry
// completes, whichever step failed: the runner authorizes the person ("check"), core revokes
// every credential it issued for the desktop (idempotent), then the runner removes it at the
// checked revision ("remove"). A desktop the runner already removed for this person (an answer
// lost on the way back) answers "removed" to "check", so the retry still revokes the credentials.
func (d Deps) handleRevokeDesktop(w http.ResponseWriter, r *http.Request) {
	p, svc, ok := d.desktopPerson(w, r)
	if !ok {
		return
	}
	id, ok := desktopPathID(w, r)
	if !ok {
		return
	}
	type phaseAnswer struct {
		Removed  bool  `json:"removed"`
		Revision int64 `json:"ownership_revision"`
	}
	call := func(ctx context.Context, body map[string]any) (phaseAnswer, bool) {
		raw, _ := json.Marshal(body)
		status, out, err := d.runnerDesktopCall(ctx, p, http.MethodPost, "/internal/desktops/"+id+"/revoke", raw)
		var a phaseAnswer
		if err != nil || status == http.StatusUnauthorized || !json.Valid(out) || (status == http.StatusOK && json.Unmarshal(out, &a) != nil) {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": desktopsUnavailable})
			return a, false
		}
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(out)
			return a, false
		}
		return a, true
	}
	checked, ok := call(r.Context(), map[string]any{"phase": "check"})
	if !ok {
		return
	}
	if err := svc.RevokeDesktopTokens(r.Context(), p.Sub, id); err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not revoke the desktop's credentials, so it was not removed. Select Remove again."})
		return
	}
	if !checked.Removed {
		if _, ok := call(r.Context(), map[string]any{"phase": "remove", "ownership_revision": checked.Revision}); !ok {
			return
		}
	}
	writeJSON(w, map[string]string{"status": "revoked"})
}

func (d Deps) handleAssignDesktop(w http.ResponseWriter, r *http.Request) {
	p, svc, ok := d.desktopPerson(w, r)
	if !ok {
		return
	}
	if !p.Has("account.manage") {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "Only an administrator can assign a desktop."})
		return
	}
	id, ok := desktopPathID(w, r)
	if !ok {
		return
	}
	body, ok := readDesktopBody(w, r)
	if !ok {
		return
	}
	if d.proxyDesktop(w, r, p, http.MethodPut, "/internal/desktops/"+id+"/owner", body) {
		_ = svc.RecordDesktopAudit(r.Context(), p.Sub, "desktop_assigned", id)
	}
}

func (d Deps) handleDesktopEnrollmentConnection(w http.ResponseWriter, r *http.Request) {
	p, _, ok := d.desktopPerson(w, r)
	if !ok {
		return
	}
	id, ok := desktopPathID(w, r)
	if !ok {
		return
	}
	d.proxyDesktop(w, r, p, http.MethodGet, "/internal/desktops/"+id+"/enrollment-connection", nil)
}

func (d Deps) handleEnrollDesktop(w http.ResponseWriter, r *http.Request) {
	p, svc, ok := d.desktopPerson(w, r)
	if !ok {
		return
	}
	id, ok := desktopPathID(w, r)
	if !ok {
		return
	}
	body, ok := readDesktopBody(w, r)
	if !ok {
		return
	}
	if d.proxyDesktop(w, r, p, http.MethodPost, "/internal/desktops/"+id+"/enroll", body) {
		_ = svc.RecordDesktopAudit(r.Context(), p.Sub, "desktop_enroll_requested", id)
	}
}

func (d Deps) handleListDesktopEnrollmentCodes(w http.ResponseWriter, r *http.Request) {
	p, svc, ok := d.desktopPerson(w, r)
	if !ok {
		return
	}
	codes, err := svc.LiveDesktopEnrollmentCodes(r.Context(), p.Sub)
	if err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "Enrollment codes are unavailable."})
		return
	}
	writeJSON(w, map[string]any{"codes": codes})
}

// handleIssueDesktopEnrollmentCode returns the code once. It is never stored or logged.
func (d Deps) handleIssueDesktopEnrollmentCode(w http.ResponseWriter, r *http.Request) {
	p, svc, ok := d.desktopPerson(w, r)
	if !ok {
		return
	}
	c, raw, err := svc.IssueDesktopEnrollmentCode(r.Context(), p.Sub)
	if errors.Is(err, identity.ErrDesktopEnrollmentRate) {
		writeJSONStatus(w, http.StatusTooManyRequests, map[string]string{"error": "Too many enrollment codes this hour. Wait a while, or use one you already have."})
		return
	}
	if err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not create an enrollment code."})
		return
	}
	writeJSON(w, map[string]any{"id": c.ID, "code": raw, "created_at": c.CreatedAt, "expires_at": c.ExpiresAt})
}

func (d Deps) handleRevokeDesktopEnrollmentCode(w http.ResponseWriter, r *http.Request) {
	p, svc, ok := d.desktopPerson(w, r)
	if !ok {
		return
	}
	done, err := svc.RevokeDesktopEnrollmentCode(r.Context(), p.Sub, r.PathValue("id"))
	if err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not revoke the code."})
		return
	}
	if !done {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "That code is already used, revoked or gone."})
		return
	}
	writeJSON(w, map[string]string{"status": "revoked"})
}

// handleRedeemDesktopEnrollmentCode is the runner's half of `install.sh install --enroll`: it
// consumes the code once and names the person the new desktop belongs to. Internal key only.
func (d Deps) handleRedeemDesktopEnrollmentCode(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.desktopService(w, r)
	if !ok {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	owner, err := svc.RedeemDesktopEnrollmentCode(r.Context(), body.Code)
	if err != nil {
		http.Error(w, "enrollment code refused", http.StatusForbidden)
		return
	}
	writeJSON(w, map[string]string{"owner_id": owner})
}
