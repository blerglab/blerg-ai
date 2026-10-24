package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

var errDesktopCredential = errors.New("desktop credential refused")

func legacyDesktopEnabled() bool { return os.Getenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS") == "true" }

// coreDesktopCall never follows redirects or logs request/response bodies. Only
// the authenticated issuer response may return a bearer to its installer.
func (a *API) coreDesktopCall(ctx context.Context, path string, body, out any) error {
	if a.coreURL == "" || a.coreInternalKey == "" {
		return errDesktopCredential
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.coreURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", a.coreInternalKey)
	res, err := coreHTTPClient(nil).Do(req)
	if err != nil {
		return errDesktopCredential
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return errDesktopCredential
	}
	raw, err = io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return errDesktopCredential
	}
	if json.Unmarshal(raw, out) != nil {
		return errDesktopCredential
	}
	return nil
}
func (a *API) authenticateDesktop(ctx context.Context, token string) (desktopauth.Binding, error) {
	var b desktopauth.Binding
	if !strings.HasPrefix(token, "bdt_") {
		return b, errDesktopCredential
	}
	err := a.coreDesktopCall(ctx, "/internal/desktop-tokens/validate", map[string]string{"token": token}, &b)
	if err != nil || b.DesktopID == "" || b.OwnerID == "" || b.Revision < 1 || b.TokenID == "" || !b.ExpiresAt.After(time.Now()) {
		return desktopauth.Binding{}, errDesktopCredential
	}
	return b, nil
}

func (a *API) desktopAdmin(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return "", false
	}
	if p.Kind != string(identity.Human) || p.Sid == "" || !p.Has("account.manage") || p.OnBehalfOf != "" {
		writeError(w, 403, "administrator login required")
		return "", false
	}
	if a.dbPool == nil {
		writeError(w, 503, "desktop authority unavailable")
		return "", false
	}
	return p.Sub, true
}
func (a *API) RegisterDesktopRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/desktop-ownership", a.HandleDesktopOwnership)
	mux.HandleFunc("PUT /api/desktop-ownership/{id}", a.HandleAssignDesktop)
	mux.HandleFunc("POST /api/desktop-ownership/{id}/credential", a.HandleDesktopCredential)
	mux.HandleFunc("POST /internal/desktops/bootstrap", a.HandleBootstrapDesktop)
}
func (a *API) HandleDesktopOwnership(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	owner, ok := a.desktopAdmin(w, r)
	if !ok {
		return
	}
	// Admin assignment inventory includes only unknown/own labels, with no paths, repos,
	// origins, runtime availability or credential. P1 consumes persisted ownership
	// for every normal desktop metadata list.
	rows, err := a.dbPool.Query(r.Context(), `SELECT id::text,name,COALESCE(desktop_owner_id,''),ownership_revision,status FROM daemons WHERE mode<>'runner' AND (desktop_owner_id IS NULL OR desktop_owner_id=$1) ORDER BY name,id`, owner)
	if err != nil {
		writeError(w, 503, "desktop authority unavailable")
		return
	}
	defer rows.Close()
	type assignment struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Owner    string `json:"owner_id"`
		Revision int64  `json:"ownership_revision"`
		Status   string `json:"status"`
	}
	list := []assignment{}
	for rows.Next() {
		var d assignment
		if rows.Scan(&d.ID, &d.Name, &d.Owner, &d.Revision, &d.Status) != nil {
			writeError(w, 503, "desktop authority unavailable")
			return
		}
		list = append(list, d)
	}
	if rows.Err() != nil {
		writeError(w, 503, "desktop authority unavailable")
		return
	}
	var owners []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if a.coreDesktopCall(r.Context(), "/internal/desktop-owners", map[string]any{}, &owners) != nil {
		writeError(w, 503, "account directory unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"desktops": list, "owners": owners, "actor": owner})
}
func (a *API) HandleAssignDesktop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, ok := a.desktopAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		Owner    string `json:"owner_id"`
		Revision int64  `json:"ownership_revision"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil || body.Owner == "" || body.Revision < 1 {
		writeError(w, 400, "owner and current revision required")
		return
	}
	var owners []struct {
		ID string `json:"id"`
	}
	if a.coreDesktopCall(r.Context(), "/internal/desktop-owners", map[string]any{}, &owners) != nil {
		writeError(w, 503, "account directory unavailable")
		return
	}
	found := false
	for _, o := range owners {
		if o.ID == body.Owner {
			found = true
		}
	}
	if !found {
		writeError(w, 400, "choose an enabled account")
		return
	}
	id := r.PathValue("id")
	var assigned *db.DesktopRow
	err := db.WithDesktopFence(r.Context(), a.dbPool, id, func(ctx context.Context) error {
		before, err := db.GetDesktop(ctx, a.dbPool, id)
		if err != nil || before == nil {
			return db.ErrDesktopAuthority
		}
		assigned, err = db.AssignDesktop(ctx, a.dbPool, id, body.Owner, body.Revision)
		if err != nil {
			return err
		}
		a.desktopOwnershipChanged(id, before.OwnerID, body.Owner, assigned.Revision) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
		return nil
	})
	if err != nil {
		writeError(w, 409, "desktop changed; refresh before assigning it")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, assigned)
}

func (a *API) desktopOwnershipChanged(id, oldOwner, newOwner string, revision int64) {
	if dc := a.hub.GetDaemon(id); dc != nil {
		a.hub.abandonWorkspaceDiscoveryRequests(dc)
		a.hub.abandonReposRootRequests(dc)
		a.hub.abandonWorkspaceFileRequests(dc)
		a.hub.abandonSessionOriginRequests(dc)
		a.hub.abandonCommitRequests(dc)
		a.hub.abandonExtRequests(dc)
		a.hub.abandonProgressOriginRequests(dc)
		a.hub.UnregisterIfCurrent(dc)
		if dc.desktopClose != nil {
			dc.desktopClose()
		}
	}
	// P1 subscribes to this removal event to invalidate all picker/cache state.
	a.hub.BroadcastJSONPerAccount(func(account string) any {
		if account != oldOwner && account != newOwner {
			return map[string]any{"type": "desktop_inventory_invalidated"}
		}
		return protocol.DesktopOwnershipChanged{Type: "desktop_ownership_changed", DesktopID: id, OwnershipRevision: revision}
	})
}

// The local installer already holds the internal component key. This endpoint
// is not accessible with a daemon, runtime, agent or ordinary browser bearer.
// A specific installing account may be named; otherwise core picks the first admin.
func (a *API) HandleBootstrapDesktop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !tokenEqual(r.Header.Get("X-Internal-Key"), a.coreInternalKey) || a.dbPool == nil {
		writeError(w, 401, "unauthorized")
		return
	}
	var body struct {
		Name  string `json:"name"`
		ID    string `json:"desktop_id"`
		Owner string `json:"owner_id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil || body.Name == "" || len(body.Name) > 200 || strings.ContainsAny(body.Name, "\x00\r\n") {
		writeError(w, 400, "desktop name required")
		return
	}
	body.Name = strings.ToLower(body.Name)
	if body.ID == "" {
		old, err := db.FindUnownedDesktopByName(r.Context(), a.dbPool, body.Name)
		if err != nil {
			writeError(w, 503, "desktop authority unavailable")
			return
		}
		if old != nil {
			body.ID = old.ID
		} else {
			body.ID = newUUID()
			if db.UpsertDaemon(r.Context(), a.dbPool, body.ID, body.Name, "local", "") != nil {
				writeError(w, 503, "desktop authority unavailable")
				return
			}
		}
	}
	var issued struct {
		Binding desktopauth.Binding `json:"binding"`
		Token   string              `json:"token"`
	}
	err := db.WithDesktopFence(r.Context(), a.dbPool, body.ID, func(ctx context.Context) error {
		d, err := db.GetDesktop(ctx, a.dbPool, body.ID)
		if err != nil || d == nil {
			return db.ErrDesktopAuthority
		}
		revision, owner := d.Revision, body.Owner
		if d.OwnerID != "" {
			if owner != "" && owner != d.OwnerID {
				return db.ErrDesktopAuthority
			}
			owner = d.OwnerID
		} else {
			revision++
		}
		if err := a.coreDesktopCall(ctx, "/internal/desktop-tokens/issue", map[string]any{"desktop_id": d.ID, "owner_id": owner, "ownership_revision": revision}, &issued); err != nil {
			return err
		}
		if issued.Binding.DesktopID != d.ID || issued.Binding.Revision != revision || issued.Binding.OwnerID == "" || (d.OwnerID != "" && issued.Binding.OwnerID != d.OwnerID) || !strings.HasPrefix(issued.Token, "bdt_") || len(issued.Token) != 68 {
			return errDesktopCredential
		}
		if d.OwnerID == "" {
			assigned, err := db.AssignDesktop(ctx, a.dbPool, d.ID, issued.Binding.OwnerID, d.Revision)
			if err != nil {
				return err
			}
			a.desktopOwnershipChanged(d.ID, "", assigned.OwnerID, assigned.Revision) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
		}
		return nil
	})
	if err != nil {
		writeError(w, 409, "create the installing account first, then retry desktop enrollment")
		return
	}
	writeJSON(w, 200, issued)
}

func helloTokenForDesktop(dc *DaemonConn) string { return dc.desktopToken }

func desktopSelectedSessions(ids []string, allowed map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if allowed[id] {
			out = append(out, id)
		}
	}
	return out
}
func desktopBackgroundInventory(inv *protocol.BackgroundTaskInventory, allowed map[string]bool) *protocol.BackgroundTaskInventory {
	if inv == nil {
		return nil
	}
	out := &protocol.BackgroundTaskInventory{Lifetime: inv.Lifetime, Sessions: map[string][]string{}}
	for id, tasks := range inv.Sessions {
		if allowed[id] {
			out.Sessions[id] = tasks
		}
	}
	return out
}

// Only the exact persisted owner can issue a credential for an existing desktop.
// Core's internal issuer is called under the same ownership fence as assignment.
func (a *API) HandleDesktopCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if p.Kind != string(identity.Human) || p.Sid == "" || p.OnBehalfOf != "" || a.dbPool == nil {
		writeError(w, 403, "person login required")
		return
	}
	var out struct {
		Binding desktopauth.Binding `json:"binding"`
		Token   string              `json:"token"`
	}
	err := db.WithDesktopFence(r.Context(), a.dbPool, r.PathValue("id"), func(ctx context.Context) error {
		d, err := db.GetDesktop(ctx, a.dbPool, r.PathValue("id"))
		if err != nil || d == nil || d.OwnerID != p.Sub {
			return db.ErrDesktopAuthority
		}
		if err := a.coreDesktopCall(ctx, "/internal/desktop-tokens/issue", map[string]any{"desktop_id": d.ID, "owner_id": d.OwnerID, "ownership_revision": d.Revision}, &out); err != nil {
			return err
		}
		if out.Binding.DesktopID != d.ID || out.Binding.OwnerID != d.OwnerID || out.Binding.Revision != d.Revision {
			return errDesktopCredential
		}
		return nil
	})
	if err != nil {
		writeError(w, 404, "desktop unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
