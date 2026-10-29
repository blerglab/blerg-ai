package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// Core's Settings → Account → Your desktops (card #193) reaches these routes with the internal
// component key and the account of the person core authenticated (a human browser session of
// their own). The person-facing guards are the same as the runner's own browser routes.

func (a *API) registerInternalDesktopRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /internal/desktops", a.HandleInternalListDesktops)
	mux.HandleFunc("GET /internal/desktops/{id}/enrollment-connection", a.internalDesktop(func(w http.ResponseWriter, r *http.Request, actor string, _ bool) {
		a.desktopEnrollmentConnectionFor(w, r, actor)
	}))
	mux.HandleFunc("POST /internal/desktops/{id}/enroll", a.internalDesktop(func(w http.ResponseWriter, r *http.Request, actor string, _ bool) {
		a.enrollDesktopFor(w, r, actor)
	}))
	mux.HandleFunc("POST /internal/desktops/{id}/rename", a.internalDesktop(a.renameDesktopFor))
	mux.HandleFunc("POST /internal/desktops/{id}/revoke", a.internalDesktop(a.revokeDesktopFor))
	mux.HandleFunc("PUT /internal/desktops/{id}/owner", a.internalDesktop(func(w http.ResponseWriter, r *http.Request, _ string, admin bool) {
		if !admin {
			writeError(w, 403, "Only an administrator can assign a desktop.")
			return
		}
		a.assignDesktopFor(w, r)
	}))
	mux.HandleFunc("POST /api/desktops/enroll", a.HandleEnrollDesktopWithCode)
}

// internalDesktopActor authenticates core and returns the person it acts for.
func (a *API) internalDesktopActor(w http.ResponseWriter, r *http.Request) (string, bool, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if a.coreInternalKey == "" || !tokenEqual(r.Header.Get("X-Internal-Key"), a.coreInternalKey) {
		writeError(w, 401, "unauthorized")
		return "", false, false
	}
	actor := r.Header.Get("X-Blerg-Account")
	if !validSessionID(actor) {
		writeError(w, 400, "account required")
		return "", false, false
	}
	if a.dbPool == nil {
		writeError(w, 503, "Desktops are unavailable.")
		return "", false, false
	}
	return actor, r.Header.Get("X-Blerg-Admin") == "1", true
}

func (a *API) internalDesktop(fn func(http.ResponseWriter, *http.Request, string, bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, admin, ok := a.internalDesktopActor(w, r)
		if !ok {
			return
		}
		if !validSessionID(r.PathValue("id")) {
			writeError(w, 404, "Desktop not found.")
			return
		}
		fn(w, r, actor, admin)
	}
}

type desktopListView struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	HostName      string     `json:"host_name"`
	OwnerID       string     `json:"owner_id"`
	Revision      int64      `json:"ownership_revision"`
	State         string     `json:"state"` // enrolled | offline | needs_enrolling
	Connected     bool       `json:"connected"`
	LastSeenAt    *time.Time `json:"last_seen_at"`
	StatusReason  string     `json:"status_reason"`
	Compatibility bool       `json:"compatibility"`
	Mine          bool       `json:"mine"`
	CanEnroll     bool       `json:"can_enroll"`
	CanRename     bool       `json:"can_rename"`
	CanRevoke     bool       `json:"can_revoke"`
}

func (a *API) HandleInternalListDesktops(w http.ResponseWriter, r *http.Request) {
	actor, admin, ok := a.internalDesktopActor(w, r)
	if !ok {
		return
	}
	rows, err := db.ListDesktopInventory(r.Context(), a.dbPool, actor, admin)
	if err != nil {
		writeError(w, 503, "Desktops are unavailable.")
		return
	}
	list := []desktopListView{}
	for _, d := range rows {
		dc := a.hub.GetDaemon(d.ID)
		v := desktopListView{ID: d.ID, Name: d.Name, HostName: d.HostName, OwnerID: d.OwnerID, Revision: d.Revision, Connected: dc != nil, LastSeenAt: d.LastSeenAt, StatusReason: d.StatusReason, Compatibility: d.Compatibility, Mine: d.OwnerID == actor}
		switch {
		case d.OwnerID == "":
			v.State = "needs_enrolling"
			v.CanEnroll = dc != nil && dc.DesktopEnrollment && dc.LegacyDesktop
		case dc != nil:
			v.State = "enrolled"
		default:
			v.State = "offline"
		}
		if dc != nil {
			now := time.Now().UTC()
			v.LastSeenAt = &now
		}
		v.CanRename = v.Mine
		v.CanRevoke = d.OwnerID != "" && (v.Mine || admin)
		list = append(list, v)
	}
	writeJSON(w, 200, map[string]any{"desktops": list, "can_manage_others": admin})
}

// cleanDesktopName trims a person's desktop name; empty clears it back to the host name.
func cleanDesktopName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if len([]rune(name)) > 64 || strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0 {
		return "", false
	}
	return name, true
}

func (a *API) renameDesktopFor(w http.ResponseWriter, r *http.Request, actor string, _ bool) {
	var body struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		writeError(w, 400, "Name required.")
		return
	}
	name, ok := cleanDesktopName(body.Name)
	if !ok {
		writeError(w, 400, "Use up to 64 characters, without line breaks or control characters.")
		return
	}
	done, err := db.RenameDesktop(r.Context(), a.dbPool, r.PathValue("id"), actor, name)
	if err != nil {
		writeError(w, 503, "Desktops are unavailable.")
		return
	}
	if !done {
		writeError(w, 404, "You can rename only your own desktops.")
		return
	}
	a.hub.BroadcastJSONPerAccount(func(string) any { return map[string]any{"type": "desktop_inventory_invalidated"} }) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
	writeJSON(w, 200, map[string]string{"status": "renamed", "name": name})
}

// revokeDesktopFor removes a desktop from its owner (the owner, or an administrator for anyone's).
// Core drives it in two idempotent phases so a failure partway through recovers on retry:
//
//   - "check": authorizes the caller and returns the current revision; core then revokes every
//     credential it issued for the desktop (repeatable).
//   - "remove": under the desktop fence, at that revision, unassigns it, closes its connection and
//     audits "revoked".
//
// Once removed, both phases answer {"removed": true} to the person who removed it (or any
// administrator), from the durable "revoked" audit record, so a retry after a lost answer still
// finishes core's revocation. Nobody else learns anything about an unowned desktop.
func (a *API) revokeDesktopFor(w http.ResponseWriter, r *http.Request, actor string, admin bool) {
	id := r.PathValue("id")
	var body struct {
		Phase    string `json:"phase"`
		Revision int64  `json:"ownership_revision"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil || (body.Phase != "check" && body.Phase != "remove") {
		writeError(w, 400, "phase required")
		return
	}
	refused := func() {
		writeError(w, 404, "That desktop is not yours to remove, or it changed. Refresh and try again.")
	}
	var out map[string]any
	err := db.WithDesktopFence(r.Context(), a.dbPool, id, func(ctx context.Context) error {
		d, err := db.GetDesktop(ctx, a.dbPool, id)
		if err != nil || d == nil {
			return db.ErrDesktopAuthority
		}
		if d.OwnerID == "" {
			if db.DesktopRemovedBy(ctx, a.dbPool, id, actor, admin) {
				out = map[string]any{"status": "revoked", "removed": true}
				return nil
			}
			return db.ErrDesktopAuthority
		}
		if d.OwnerID != actor && !admin {
			return db.ErrDesktopAuthority
		}
		if body.Phase == "check" {
			out = map[string]any{"status": "authorized", "ownership_revision": d.Revision}
			return nil
		}
		next, err := db.RevokeDesktop(ctx, a.dbPool, id, actor, body.Revision)
		if err != nil {
			return err
		}
		a.desktopOwnershipChanged(id, d.OwnerID, "", next) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
		out = map[string]any{"status": "revoked", "removed": true}
		return nil
	})
	if err != nil {
		refused()
		return
	}
	writeJSON(w, 200, out)
}

// enrollCodeLimiter bounds redemption attempts per client address and overall. A code has 160
// random bits, so this guards the server, not the code.
type enrollCodeLimiter struct {
	mu      sync.Mutex
	window  time.Time
	perAddr map[string]int
	total   int
}

const (
	enrollCodeAttemptsPerAddr = 10
	enrollCodeAttemptsTotal   = 120
)

func (l *enrollCodeLimiter) allow(addr string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.window) >= time.Minute {
		l.window, l.perAddr, l.total = now, map[string]int{}, 0
	}
	if l.perAddr[addr] >= enrollCodeAttemptsPerAddr || l.total >= enrollCodeAttemptsTotal {
		return false
	}
	l.perAddr[addr]++
	l.total++
	return true
}

var enrollCodes = &enrollCodeLimiter{}

// enrollTrustedProxies: behind an ingress the peer is the proxy; BLERG_RUNNER_TRUSTED_PROXIES
// (the MCP gateway's setting) names the proxies whose forwarded client address is believed.
var enrollTrustedProxies = sync.OnceValue(func() []netip.Prefix {
	trusted, err := mcpgw.ParseTrustedProxies(os.Getenv("BLERG_RUNNER_TRUSTED_PROXIES"))
	if err != nil {
		return nil
	}
	return trusted
})

func validEnrollmentCode(code string) bool {
	if len(code) != 44 || !strings.HasPrefix(code, "bec_") {
		return false
	}
	for _, c := range code[4:] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// HandleEnrollDesktopWithCode is `install.sh install --enroll <code>` on a new desktop: core
// consumes the person's one-time code and names them; this desktop gets a new identity owned by
// them and its credential goes back to the installer only. The code is never logged.
func (a *API) HandleEnrollDesktopWithCode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !enrollCodes.allow(mcpgw.PublicClientIP(r, enrollTrustedProxies()), time.Now()) {
		writeError(w, 429, "Too many enrollment attempts. Wait a minute and try again.")
		return
	}
	if a.dbPool == nil || a.coreURL == "" || a.coreInternalKey == "" {
		writeError(w, 503, "Desktop enrollment is unavailable on this server.")
		return
	}
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil || !validDesktopName(body.Name) {
		writeError(w, 400, "A desktop name and an enrollment code are required.")
		return
	}
	refused := "This enrollment code is not valid: it is wrong, expired, revoked or already used. Add a desktop again in Settings → Account → Your desktops."
	if !validEnrollmentCode(body.Code) {
		writeError(w, 403, refused)
		return
	}
	var redeemed struct {
		Owner string `json:"owner_id"`
	}
	if err := a.coreDesktopCall(r.Context(), "/internal/desktop-enrollment-codes/redeem", map[string]string{"code": body.Code}, &redeemed); err != nil || !validSessionID(redeemed.Owner) {
		writeError(w, 403, refused)
		return
	}
	issued, err := a.bootstrapDesktop(r.Context(), body.Name, "", redeemed.Owner, true)
	if err != nil || issued.Binding.OwnerID != redeemed.Owner {
		status := 409
		if errors.Is(err, errDesktopUnavailable) {
			status = 503
		}
		writeError(w, status, "The code was used, but the desktop could not be enrolled. Add a desktop again in Settings → Account → Your desktops.")
		return
	}
	writeJSON(w, 200, issued)
}
