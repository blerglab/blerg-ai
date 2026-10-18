package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sessionPluginArguments struct {
	pluginspec.Entry
	Engine       string `json:"engine"`
	Reason       string `json:"reason"`
	SessionApply bool   `json:"session_apply"`
}

func decodeSessionPlugin(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(dst) != nil || dec.Decode(new(any)) != io.EOF {
		writeError(w, 400, "Provide a valid plugin request.")
		return false
	}
	return true
}
func sessionPluginEngine(row *db.SessionRow) string {
	engine := ""
	if row.Engine != nil {
		engine = *row.Engine
	}
	return engines.Normalize(engine)
}
func (a *API) sessionPluginOwner(w http.ResponseWriter, r *http.Request) (*db.SessionRow, identity.Principal, bool) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return nil, p, false
	}
	if p.Kind != "human" || p.Sid == "" {
		writeError(w, 403, msgProposalHuman)
		return nil, p, false
	}
	if a.dbPool == nil {
		writeError(w, 503, noDatabaseMsg)
		return nil, p, false
	}
	row, err := db.GetSession(r.Context(), a.dbPool, r.PathValue("id"))
	if err != nil || row == nil || !ownsSession(p.Sub, row) {
		writeError(w, 404, "session not found")
		return nil, p, false
	}
	return row, p, true
}
func (a *API) sessionPluginEligibility(ctx context.Context, row *db.SessionRow) (bool, string) {
	if row == nil || terminalSessionStatus(row.Status) || row.Kind != "agent" {
		return false, "This session cannot reload plugins; always-on changes apply to new sessions."
	}
	if !pluginspec.SupportsEngine(sessionPluginEngine(row)) {
		return false, "This engine loads plugin changes in new sessions."
	}
	if row.Runtime != nil && *row.Runtime != "" && *row.Runtime != "daemon" {
		return false, "This runtime loads plugin changes in new sessions."
	}
	var restricted bool
	if err := a.dbPool.QueryRow(ctx, `SELECT restrict_tools FROM sessions WHERE id=$1`, row.ID).Scan(&restricted); err != nil {
		return false, "Session restrictions could not be verified."
	}
	if restricted {
		return false, "Restricted sessions run without plugins; always-on changes apply to new unrestricted sessions."
	}
	dc := a.hub.FindDaemonForSession(row.ID)
	if dc == nil || dc.ID != row.DaemonID || !dc.CanSessionPluginsFor(sessionPluginEngine(row)) {
		return false, "The live daemon does not support plugin reload; always-on changes apply to new sessions."
	}
	return true, ""
}
func (a *API) sessionPluginAllowed(e pluginspec.Entry) bool {
	return pluginspec.ValidateClaude(e) == nil && a.pluginAllow.Allows(e.Marketplace)
}
func (a *API) pluginPolicy(c sessionPluginCatalog) sessionPluginCatalog {
	if !c.Any {
		out := make([]string, 0, len(c.Allowed))
		for _, m := range c.Allowed {
			if a.pluginAllow.Allows(m) {
				out = append(out, m)
			}
		}
		c.Allowed = out
		return c
	}
	sources, anySource := a.pluginAllow.List()
	c.Allowed = sources
	c.Any = anySource
	return c
}
func catalogPermits(c sessionPluginCatalog, e pluginspec.Entry) bool {
	if c.Any {
		return true
	}
	for _, m := range c.Allowed {
		if strings.EqualFold(m, e.Marketplace) {
			return true
		}
	}
	return false
}

func (a *API) HandleSessionPlugins(w http.ResponseWriter, r *http.Request) {
	row, p, ok := a.sessionPluginOwner(w, r)
	if !ok {
		return
	}
	core, err := a.pluginCore()
	if err != nil {
		writeError(w, 503, errSessionPluginCore.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	catalog, err := core.Catalog(ctx, p.Sub, proofFromPrincipal(p), sessionPluginEngine(row))
	if err != nil {
		writeError(w, 503, errSessionPluginCore.Error())
		return
	}
	catalog = a.pluginPolicy(catalog)
	requests, err := db.ListSessionPluginProposals(ctx, a.dbPool, p.Sub, row.ID)
	if err != nil {
		writeError(w, 500, "Plugin requests could not be read.")
		return
	}
	views := make([]proposalView, 0, len(requests))
	for i := range requests {
		views = append(views, viewOfProposal(&requests[i]))
	}
	apply, reason := a.sessionPluginEligibility(ctx, row)
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, map[string]any{"engine": sessionPluginEngine(row), "supported": pluginspec.SupportsEngine(sessionPluginEngine(row)), "session_apply": apply, "reason": reason, "allowed_marketplaces": catalog.Allowed, "any_marketplace": catalog.Any, "plugins": catalog.Plugins, "requests": views})
}
func (a *API) insertSessionPlugin(ctx context.Context, row *db.SessionRow, args sessionPluginArguments) (*db.Proposal, error) {
	raw, _ := json.Marshal(args)
	return db.InsertProposalLimited(ctx, a.dbPool, db.Proposal{AccountID: *row.SpawningAccountID, SessionID: row.ID, ConnectionID: db.SessionPluginConnectionID, ConnectionName: "session-plugin", Tool: "request_plugin", ToolHash: "session-plugin-v1", URLSnapshot: "session-plugins:" + row.DaemonID, Arguments: raw, AgentSummary: "Request plugin " + args.Plugin + " for this session."}, db.ProposalLimits{Account: 50, Session: 10, Cron: 20})
}
func (a *API) HandleSessionPluginRequest(w http.ResponseWriter, r *http.Request) {
	sid, raw, ok := a.agentSessionWrite(w, r, "plugin requests", 16<<10)
	if !ok {
		return
	}
	var req struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || len(req.Reason) > 2000 || !utf8.ValidString(req.Reason) || strings.TrimSpace(req.Reason) == "" || strings.IndexFunc(req.Reason, func(c rune) bool { return unicode.IsControl(c) && c != '\n' && c != '\t' }) >= 0 {
		writeError(w, 400, "Provide a plugin name and a reason of at most 2000 bytes.")
		return
	}
	name, market, has := strings.Cut(req.Name, "@")
	if !has {
		sources, anySource := a.pluginAllow.List()
		if anySource || len(sources) != 1 {
			writeError(w, 400, "Use plugin@owner/repo to choose an allowed marketplace.")
			return
		}
		market = sources[0]
	}
	e := pluginspec.Entry{Marketplace: market, Plugin: name}
	if !a.sessionPluginAllowed(e) {
		writeError(w, 403, "This plugin or marketplace is not allowed.")
		return
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil || row == nil || row.SpawningAccountID == nil || *row.SpawningAccountID == "" || row.Kind != "agent" || !pluginspec.SupportsEngine(sessionPluginEngine(row)) {
		writeError(w, 409, "This session cannot request plugins.")
		return
	}
	core, err := a.pluginCore()
	if err != nil {
		writeError(w, 503, errSessionPluginCore.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	catalog, err := core.Policy(ctx, sessionPluginEngine(row))
	if err != nil {
		writeError(w, 503, errSessionPluginCore.Error())
		return
	}
	if !catalogPermits(catalog, e) {
		writeError(w, 403, "This marketplace is not allowed by Core.")
		return
	}
	apply, _ := a.sessionPluginEligibility(r.Context(), row)
	p, err := a.insertSessionPlugin(r.Context(), row, sessionPluginArguments{Entry: e, Engine: sessionPluginEngine(row), Reason: req.Reason, SessionApply: apply})
	if err != nil {
		writeError(w, 409, "The plugin request could not be queued; check pending limits.")
		return
	}
	writeJSON(w, 202, viewOfProposal(p))
}
func (a *API) HandleAddSessionPlugin(w http.ResponseWriter, r *http.Request) {
	row, p, ok := a.sessionPluginOwner(w, r)
	if !ok {
		return
	}
	var req struct {
		Marketplace string `json:"marketplace"`
		Plugin      string `json:"plugin"`
		Scope       string `json:"scope"`
	}
	if !decodeSessionPlugin(w, r, &req) {
		return
	}
	e := pluginspec.Entry{Marketplace: req.Marketplace, Plugin: req.Plugin}
	if !a.sessionPluginAllowed(e) || (req.Scope != "session" && req.Scope != "always") {
		writeError(w, 403, "Choose an allowed plugin and scope.")
		return
	}
	apply, reason := a.sessionPluginEligibility(r.Context(), row)
	if req.Scope == "session" && !apply {
		writeError(w, 409, reason)
		return
	}
	core, err := a.pluginCore()
	if err != nil {
		writeError(w, 503, errSessionPluginCore.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	catalog, err := core.Catalog(ctx, p.Sub, proofFromPrincipal(p), sessionPluginEngine(row))
	if err != nil {
		writeError(w, 503, errSessionPluginCore.Error())
		return
	}
	if !catalogPermits(catalog, e) {
		writeError(w, 403, "This marketplace is not allowed by Core.")
		return
	}
	prop, err := a.insertSessionPlugin(r.Context(), row, sessionPluginArguments{Entry: e, Engine: sessionPluginEngine(row), Reason: "Added by the session owner.", SessionApply: apply})
	if err != nil {
		writeError(w, 409, "The plugin change could not be queued.")
		return
	}
	raw, _ := json.Marshal(map[string]string{"scope": req.Scope, "expected_arguments_raw": string(prop.Arguments)})
	inner := r.Clone(r.Context())
	inner.Body = io.NopCloser(bytes.NewReader(raw))
	(&proposalsHandler{api: a}).approveSessionPlugin(w, inner, requesterOf(p), prop, http.StatusAccepted)
}

func (h *proposalsHandler) approveSessionPlugin(w http.ResponseWriter, r *http.Request, who grantRequester, p *db.Proposal, status int) {
	var req struct {
		Scope    string `json:"scope"`
		Expected string `json:"expected_arguments_raw"`
	}
	if !decodeSessionPlugin(w, r, &req) {
		return
	}
	if req.Expected != string(p.Arguments) || (req.Scope != "session" && req.Scope != "always") {
		writeError(w, 409, "Review the exact plugin request and choose a scope before approving.")
		return
	}
	var args sessionPluginArguments
	if json.Unmarshal(p.Arguments, &args) != nil || !h.api.sessionPluginAllowed(args.Entry) {
		writeError(w, 403, "This plugin or marketplace is no longer allowed.")
		return
	}
	row, err := db.GetSession(r.Context(), h.api.dbPool, p.SessionID)
	if err != nil || row == nil || !ownsSession(who.Account, row) || sessionPluginEngine(row) != args.Engine {
		writeError(w, 409, "The session owner or engine changed; request the plugin again.")
		return
	}
	apply, reason := h.api.sessionPluginEligibility(r.Context(), row)
	if req.Scope == "session" && !apply {
		writeError(w, 409, reason)
		return
	}
	if apply && p.URLSnapshot != "session-plugins:"+row.DaemonID {
		writeError(w, 409, "The session moved; request the plugin again.")
		return
	}
	core, err := h.api.pluginCore()
	if err != nil {
		writeError(w, 503, errSessionPluginCore.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	proof := coreProof{SessionID: who.Sid}
	catalog, err := core.Catalog(ctx, who.Account, proof, args.Engine)
	if err != nil {
		writeError(w, 503, errSessionPluginCore.Error())
		return
	}
	if !catalogPermits(catalog, args.Entry) {
		writeError(w, 403, "This marketplace is no longer allowed.")
		return
	}
	if _, err = db.BeginSessionPluginExecution(ctx, h.api.dbPool, who.Account, p.ID, req.Expected); err != nil {
		transitionError(w, err, "The plugin request changed or was already decided.")
		return
	}
	resultState, message := "queued", "Approved; waiting for the engine to become idle."
	state := db.ProposalDone
	if req.Scope == "always" {
		if err = core.Add(ctx, who.Account, proof, args.Engine, args.Entry); err != nil {
			state = db.ProposalFailed
			resultState = "failed"
			message = "The always-on setting could not be confirmed. Check Settings before retrying."
		}
	}
	if state == db.ProposalDone && !apply {
		resultState = "new_sessions"
		message = "Saved as always on; this runtime loads the plugin in new sessions."
	}
	result, _ := json.Marshal(map[string]string{"state": resultState, "message": message, "scope": req.Scope})
	recCtx, stop := context.WithTimeout(context.WithoutCancel(r.Context()), proposalWriteBudget)
	defer stop()
	done, err := db.FinishProposal(recCtx, h.api.dbPool, p.ID, state, result)
	if err != nil {
		writeError(w, 500, "The plugin approval outcome could not be recorded.")
		return
	}
	if state == db.ProposalDone && apply {
		h.api.hub.dispatchSessionPlugin(recCtx, h.api.dbPool, done)
	}
	writeJSON(w, status, viewOfProposal(done))
}

// Only approved durable work is replayed; daemon request IDs make dispatch idempotent.
func (h *Hub) dispatchSessionPlugin(ctx context.Context, pool *pgxpool.Pool, p *db.Proposal) {
	if !db.IsSessionPluginProposal(p) || p.State != db.ProposalDone {
		return
	}
	var args sessionPluginArguments
	var result struct {
		State string `json:"state"`
	}
	if json.Unmarshal(p.Arguments, &args) != nil || json.Unmarshal(p.Result, &result) != nil || result.State != "queued" {
		return
	}
	row, err := db.GetSession(ctx, pool, p.SessionID)
	if err != nil || row == nil || !ownsSession(p.AccountID, row) || terminalSessionStatus(row.Status) {
		return
	}
	dc := h.FindDaemonForSession(p.SessionID)
	if dc == nil || dc.ID != row.DaemonID || p.URLSnapshot != "session-plugins:"+dc.ID || !dc.CanSessionPluginsFor(args.Engine) {
		return
	}
	raw, _ := json.Marshal(protocol.ApplySessionPlugins{Type: "apply_session_plugins", RequestID: p.ID, SessionID: p.SessionID, Plugins: []pluginspec.Entry{args.Entry}})
	h.trySendToDaemon(dc, raw)
}
