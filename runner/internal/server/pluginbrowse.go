package server

// The plugin browser and MCP sign-in (card #144), through the daemon that runs the session
// (daemon side: internal/daemon/engineextensions.go).
//
//	GET  /api/sessions/{id}/plugins/catalog       the marketplaces the session's engine knows and their plugins
//	POST /api/sessions/{id}/plugins/marketplaces  {source}: add a marketplace, answer the new catalog
//	POST /api/sessions/{id}/mcp/sign-in           {server}: start the engine's sign-in, answer its URL
//
// Who: the session's owner only, as a person signed in to the browser (a browser session id, not
// acting on someone's behalf): the catalog is the owner's machine's plugin inventory, and adding a
// marketplace or signing in changes the engine's state there. Someone else's private session is
// the uniform 404. All of it is decided before any daemon is asked. Installing stays
// POST /api/sessions/{id}/plugins (sessionplugins.go).
//
// An engine the daemon has no provider for answers 409 with code "unsupported": the browser
// falls back to that engine's add form. Every reply is bounded and re-validated here.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const (
	extPendingMax        = 16
	extPendingPerDaemon  = 4
	extPendingPerSession = 2
	extPendingPerAccount = 4
)

var (
	extCatalogTimeout = 20 * time.Second
	extAddTimeout     = 150 * time.Second
	extSignInTimeout  = 50 * time.Second
)

var (
	errExtUnavailable = errors.New("daemon request unavailable")
	errExtBusy        = errors.New("daemon request busy")
)

func (dc *DaemonConn) SetExtensionEngines(catalog, signIn []string) {
	clean := func(in []string) []string {
		var out []string
		for _, e := range in {
			if pluginspec.SupportsEngine(e) && !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
		return out
	}
	dc.liveMu.Lock()
	defer dc.liveMu.Unlock()
	dc.pluginCatalogEngines = clean(catalog)
	dc.mcpSignInEngines = clean(signIn)
}

func (dc *DaemonConn) canPluginCatalog(engine string) bool {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return slices.Contains(dc.pluginCatalogEngines, engine)
}

func (dc *DaemonConn) canMCPSignIn(engine string) bool {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return slices.Contains(dc.mcpSignInEngines, engine)
}

type extWaiter struct {
	dc        *DaemonConn
	sessionID string
	account   string
	reply     string // the result type expected
	ch        chan []byte
}

type extRequests struct {
	mu      sync.Mutex
	pending map[string]extWaiter
}

// requestExt sends msg (built with the request id) to dc and waits for its reply frame.
func (h *Hub) requestExt(ctx context.Context, dc *DaemonConn, account, sid, reply string, build func(id string) any, timeout time.Duration) ([]byte, error) {
	id := newUUID()
	w := extWaiter{dc: dc, sessionID: sid, account: account, reply: reply, ch: make(chan []byte, 1)}
	r := &h.extReqs
	r.mu.Lock()
	perDaemon, perSession, perAccount := 0, 0, 0
	for _, p := range r.pending {
		if p.account == account {
			perAccount++
		}
		if p.dc.ID == dc.ID {
			perDaemon++
		}
		if p.sessionID == sid {
			perSession++
		}
	}
	if len(r.pending) >= extPendingMax || perDaemon >= extPendingPerDaemon || perSession >= extPendingPerSession || perAccount >= extPendingPerAccount {
		r.mu.Unlock()
		return nil, errExtBusy
	}
	if r.pending == nil {
		r.pending = make(map[string]extWaiter)
	}
	r.pending[id] = w
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock() }()
	raw, _ := json.Marshal(build(id))
	if h.FindDaemonForSession(sid) != dc || !h.trySendToDaemon(dc, raw) {
		return nil, errExtUnavailable
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res, ok := <-w.ch:
		if !ok || h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(sid) != dc {
			return nil, errExtUnavailable
		}
		return res, nil
	case <-timer.C:
		return nil, errExtUnavailable
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// deliverExtResult hands a reply to its request: only from the daemon asked, for the session
// asked about, of the type expected, once.
func (h *Hub) deliverExtResult(dc *DaemonConn, typ string, raw []byte) {
	var head struct {
		RequestID string `json:"request_id"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return
	}
	if h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(head.SessionID) != dc || dc.BoundSessionID != "" {
		return
	}
	r := &h.extReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.pending[head.RequestID]
	if !ok || w.dc != dc || w.sessionID != head.SessionID || w.reply != typ {
		return
	}
	delete(r.pending, head.RequestID)
	w.ch <- raw
}

func (h *Hub) abandonExtRequests(dc *DaemonConn) {
	r := &h.extReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.pending {
		if w.dc == dc {
			delete(r.pending, id)
			close(w.ch)
		}
	}
}

func (a *API) RegisterPluginBrowseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{id}/plugins/catalog", a.handlePluginCatalog)
	mux.HandleFunc("POST /api/sessions/{id}/plugins/marketplaces", a.handleAddMarketplace)
	mux.HandleFunc("POST /api/sessions/{id}/mcp/sign-in", a.handleMCPSignIn)
}

func extProblem(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}

type extTarget struct {
	row    *db.SessionRow
	p      identity.Principal
	dc     *DaemonConn
	engine string
}

// extSession decides who may ask, and whether this session can be asked, before any daemon is.
func (a *API) extSession(w http.ResponseWriter, r *http.Request, canEngine func(*DaemonConn, string) bool) (extTarget, bool) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return extTarget{}, false
	}
	if p.Kind != "human" || p.Sid == "" || p.OnBehalfOf != "" {
		extProblem(w, http.StatusForbidden, "forbidden", "Only a signed-in person can do this.")
		return extTarget{}, false
	}
	if a.dbPool == nil || a.hub == nil {
		extProblem(w, http.StatusServiceUnavailable, "unavailable", noDatabaseMsg)
		return extTarget{}, false
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		extProblem(w, http.StatusNotFound, "not_found", "Session not found.")
		return extTarget{}, false
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil {
		extProblem(w, http.StatusConflict, "unavailable", "The session could not be read.")
		return extTarget{}, false
	}
	if row == nil || (row.Private && !ownsSession(p.Sub, row)) {
		extProblem(w, http.StatusNotFound, "not_found", "Session not found.")
		return extTarget{}, false
	}
	if !ownsSession(p.Sub, row) {
		extProblem(w, http.StatusForbidden, "forbidden", "Only the session's owner can do this.")
		return extTarget{}, false
	}
	if row.Kind != "agent" || row.ArchivedAt != nil || row.DiscardRequestedAt != nil || terminalSessionStatus(row.Status) || row.Status == "disconnected" {
		extProblem(w, http.StatusConflict, "unavailable", "The session is not running.")
		return extTarget{}, false
	}
	engine := sessionPluginEngine(row)
	if row.Runtime != nil && *row.Runtime != "" && *row.Runtime != "daemon" {
		extProblem(w, http.StatusConflict, "sandboxed", "This is not available for sandboxed sessions yet.")
		return extTarget{}, false
	}
	dc := a.hub.FindDaemonForSession(sid)
	if dc == nil || dc.BoundSessionID != "" || dc.ID != row.DaemonID {
		extProblem(w, http.StatusConflict, "unavailable", "The session's daemon is not connected.")
		return extTarget{}, false
	}
	if !canEngine(dc, engine) {
		extProblem(w, http.StatusConflict, "unsupported", "This engine has no catalog here.")
		return extTarget{}, false
	}
	return extTarget{row: row, p: p, dc: dc, engine: engine}, true
}

func extFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, errExtBusy) {
		extProblem(w, http.StatusTooManyRequests, "busy", "Too many requests at once; try again.")
		return
	}
	extProblem(w, http.StatusConflict, "unavailable", "The session's daemon did not answer.")
}

func (a *API) catalogFromDaemon(w http.ResponseWriter, r *http.Request, t extTarget, add string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout+time.Second)
	defer cancel()
	raw, err := a.hub.requestExt(ctx, t.dc, t.p.Sub, t.row.ID, "plugin_catalog_result", func(id string) any {
		return protocol.ReadPluginCatalog{Type: "read_plugin_catalog", RequestID: id, SessionID: t.row.ID, AddMarketplace: add}
	}, timeout)
	if err != nil {
		extFailure(w, err)
		return
	}
	var res protocol.PluginCatalogResult
	if json.Unmarshal(raw, &res) != nil {
		extProblem(w, http.StatusConflict, "unavailable", "The daemon returned an invalid catalog.")
		return
	}
	if !res.OK {
		switch res.ErrorCode {
		case "unsupported":
			extProblem(w, http.StatusConflict, "unsupported", "This engine has no catalog here.")
		case "not_allowed":
			extProblem(w, http.StatusForbidden, "not_allowed", "This marketplace is not allowed by this install.")
		case "add_failed":
			extProblem(w, http.StatusBadGateway, "add_failed", "The marketplace could not be added. Check the source and try again.")
		case "busy":
			extProblem(w, http.StatusTooManyRequests, "busy", "Too many requests at once; try again.")
		default:
			extProblem(w, http.StatusConflict, "unavailable", "The catalog could not be read.")
		}
		return
	}
	markets, plugins, cut := protocol.SanitizePluginCatalog(res.Marketplaces, res.Plugins)
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"engine": t.engine, "marketplaces": markets, "plugins": plugins, "truncated": res.Truncated || cut})
}

func (a *API) handlePluginCatalog(w http.ResponseWriter, r *http.Request) {
	t, ok := a.extSession(w, r, (*DaemonConn).canPluginCatalog)
	if !ok {
		return
	}
	a.catalogFromDaemon(w, r, t, "", extCatalogTimeout)
}

func (a *API) handleAddMarketplace(w http.ResponseWriter, r *http.Request) {
	t, ok := a.extSession(w, r, (*DaemonConn).canPluginCatalog)
	if !ok {
		return
	}
	var req struct {
		Source string `json:"source"`
	}
	if !decodeSessionPlugin(w, r, &req) {
		return
	}
	source, valid := protocol.MarketplaceSourceFromInput(req.Source)
	if !valid {
		extProblem(w, http.StatusBadRequest, "invalid", "Enter a GitHub owner/repo or an https://github.com/owner/repo URL.")
		return
	}
	if !a.pluginAllow.Allows(source) {
		extProblem(w, http.StatusForbidden, "not_allowed", "This marketplace is not allowed by this install.")
		return
	}
	core, err := a.pluginCore()
	if err != nil {
		extProblem(w, http.StatusServiceUnavailable, "unavailable", errSessionPluginCore.Error())
		return
	}
	cctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	catalog, err := core.Catalog(cctx, t.p.Sub, proofFromPrincipal(t.p), t.engine)
	cancel()
	if err != nil {
		extProblem(w, http.StatusServiceUnavailable, "unavailable", errSessionPluginCore.Error())
		return
	}
	if !catalogPermits(a.pluginPolicy(catalog), pluginspec.Entry{Marketplace: source}) {
		extProblem(w, http.StatusForbidden, "not_allowed", "This marketplace is not allowed by Core.")
		return
	}
	a.catalogFromDaemon(w, r, t, source, extAddTimeout)
}

func (a *API) handleMCPSignIn(w http.ResponseWriter, r *http.Request) {
	t, ok := a.extSession(w, r, (*DaemonConn).canMCPSignIn)
	if !ok {
		return
	}
	var req struct {
		Server string `json:"server"`
	}
	if !decodeSessionPlugin(w, r, &req) {
		return
	}
	if !protocol.ValidMCPServerName(req.Server) {
		extProblem(w, http.StatusBadRequest, "invalid", "Choose an MCP server of this session.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), extSignInTimeout+time.Second)
	defer cancel()
	raw, err := a.hub.requestExt(ctx, t.dc, t.p.Sub, t.row.ID, "mcp_sign_in_result", func(id string) any {
		return protocol.MCPSignIn{Type: "mcp_sign_in", RequestID: id, SessionID: t.row.ID, Server: req.Server}
	}, extSignInTimeout)
	if err != nil {
		extFailure(w, err)
		return
	}
	var res protocol.MCPSignInResult
	if json.Unmarshal(raw, &res) != nil {
		extProblem(w, http.StatusConflict, "unavailable", "The daemon returned an invalid answer.")
		return
	}
	if !res.OK {
		switch res.ErrorCode {
		case "not_found":
			extProblem(w, http.StatusNotFound, "not_found", "This server does not need a sign-in now.")
		case "connector":
			extProblem(w, http.StatusConflict, "connector", "Connect claude.ai connectors on claude.ai.")
		case "untrusted":
			extProblem(w, http.StatusForbidden, "untrusted", "Sign in to this server from the terminal (it was not defined in your own settings).")
		case "refused":
			extProblem(w, http.StatusBadGateway, "refused", "The engine could not start a sign-in for this server.")
		case "unsupported":
			extProblem(w, http.StatusConflict, "unsupported", "Sign-in is not available for this session.")
		case "busy":
			extProblem(w, http.StatusTooManyRequests, "busy", "Too many requests at once; try again.")
		default:
			extProblem(w, http.StatusConflict, "unavailable", "The sign-in could not be started.")
		}
		return
	}
	// Only an https URL the engine made; a URL is required when the person has something to do.
	if (res.AuthURL != "" && !protocol.ValidAuthURL(res.AuthURL)) || (res.RequiresUserAction && res.AuthURL == "") {
		extProblem(w, http.StatusBadGateway, "refused", "The engine could not start a sign-in for this server.")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	// The daemon must say the server is the person's own (user scope, global config): an older
	// daemon, which does not check, or any other source gets no link.
	if res.RequiresUserAction && res.Source != "user" {
		extProblem(w, http.StatusForbidden, "untrusted", "Sign in to this server from the terminal (it was not defined in your own settings).")
		return
	}
	source := res.Source
	if source != "user" {
		source = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"auth_url": res.AuthURL, "requires_user_action": res.RequiresUserAction, "callback_expected": res.CallbackExpected, "source": source})
}
