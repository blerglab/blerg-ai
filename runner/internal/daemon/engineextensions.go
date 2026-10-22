package daemon

// Engine extensions (card #144): what a person can add to a running session from its
// Capabilities tab, per engine. Each engine plugs in one provider behind engineExtensions:
//
//   - Catalog: the marketplaces the engine knows and every plugin in them, with installed and
//     enabled state, read from the engine's own home (never from the session's workspace, which
//     the session can write);
//   - AddMarketplace: add a marketplace source (an allow-listed GitHub owner/repo) to the
//     daemon-owned plugin home the engine installs Blerg's plugins into;
//   - MCPSignIn: start the engine's own OAuth sign-in for one MCP server that needs it and return
//     the authorization URL it made.
//
// Installing a plugin is not here: it stays the existing owner-approved path (server
// HandleAddSessionPlugin → ApplySessionPlugins / always-on → pluginsinstall), which already has
// per-engine installers. Listing MCP servers with their auth state is the engine's MCPStatus
// (mcptracker.go), also already per engine. An engine without a provider answers "unsupported",
// and the browser falls back to that engine's add form.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// extCode is a fixed reply code (protocol.PluginCatalogResult / MCPSignInResult ErrorCode).
type extCode string

func (c extCode) Error() string { return string(c) }

const (
	extUnsupported extCode = "unsupported"
	extNotAllowed  extCode = "not_allowed"
	extAddFailed   extCode = "add_failed"
	extNotFound    extCode = "not_found"
	extConnector   extCode = "connector"
	extRefused     extCode = "refused"
	extBusy        extCode = "busy"
	extUntrusted   extCode = "untrusted"
	extUnavailable extCode = "unavailable"
)

// extCatalog is one engine's catalog for a session.
type extCatalog struct {
	Marketplaces []protocol.CatalogMarketplace
	Plugins      []protocol.CatalogPlugin
	Truncated    bool
}

// extSignIn is what an engine answered to a sign-in: the URL to open (https, from the engine),
// whether the person has anything to do, and whether the engine waits for a callback.
type extSignIn struct {
	AuthURL            string
	RequiresUserAction bool
	CallbackExpected   bool
	// Source is where the server was defined, as shown to the person ("user": the person's own
	// user settings or global config). Sign-in is refused (extUntrusted) for any other source.
	Source string
}

// engineExtensions is one engine's provider for one session.
type engineExtensions interface {
	Catalog(ctx context.Context) (extCatalog, error)
	// AddMarketplace receives a source that already passed pluginspec.ValidMarketplaceSource; the
	// provider re-checks the daemon's allow-list.
	AddMarketplace(ctx context.Context, source string) error
	MCPSignIn(ctx context.Context, server string) (extSignIn, error)
}

// extensionProviders maps an engine id to the constructor of its provider. A constructor returns
// extUnsupported for a session it cannot serve (a sandboxed one, another driver). Codex and other
// engines register here; the hello advertises exactly the engines listed.
var extensionProviders = map[string]func(h *AgentHost, sess *agentSession) (engineExtensions, error){
	"claude": newClaudeExtensions,
}

// extensionEngines is what the hello advertises (PluginCatalogEngines, MCPSignInEngines).
func extensionEngines() []string {
	var out []string
	for _, id := range append([]string{"claude"}, engineOrder...) {
		if _, ok := extensionProviders[id]; ok && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// extensionsFor builds the provider for a live session.
func (h *AgentHost) extensionsFor(sessionID string) (engineExtensions, *agentSession, error) {
	sess := h.get(sessionID)
	if sess == nil {
		return nil, nil, extUnavailable
	}
	engine := sess.engine
	if engine == "" {
		engine = "claude"
	}
	mk, ok := extensionProviders[engine]
	if !ok {
		return nil, sess, extUnsupported
	}
	p, err := mk(h, sess)
	return p, sess, err
}

// extReads bounds catalog reads and sign-ins in flight on this daemon.
var (
	extReads   = make(chan struct{}, 2)
	extSignIns = make(chan struct{}, 2)
)

// extBudget bounds one catalog read (a marketplace add included) and one sign-in.
var (
	extCatalogBudget = 2 * time.Minute
	extSignInBudget  = 45 * time.Second
)

// extSlotGrace is how long past its budget a request may hold its slot.
var extSlotGrace = 5 * time.Second

// withSlotDeadline runs work holding one slot of sem (already taken by the caller) and frees the
// slot when work returns or when deadline passes, whichever is first: work stuck in a call that
// cannot be cancelled (an open that blocks) can no longer hold the daemon's slots for ever.
// done is false when the deadline won; the work's later result is dropped.
func withSlotDeadline[T any](sem chan struct{}, deadline time.Duration, work func() T) (res T, done bool) {
	var once sync.Once
	free := func() { once.Do(func() { <-sem }) }
	ch := make(chan T, 1)
	go func() {
		defer free()
		ch <- work()
	}()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case res = <-ch:
		return res, true
	case <-timer.C:
		free()
		return res, false
	}
}

func codeOf(err error) string {
	var c extCode
	if errors.As(err, &c) {
		return string(c)
	}
	return string(extUnavailable)
}

// PluginCatalog answers read_plugin_catalog.
func (h *AgentHost) PluginCatalog(ctx context.Context, req protocol.ReadPluginCatalog) protocol.PluginCatalogResult {
	fail := func(err error) protocol.PluginCatalogResult {
		return protocol.PluginCatalogResult{ErrorCode: codeOf(err)}
	}
	p, _, err := h.extensionsFor(req.SessionID)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, extCatalogBudget)
	defer cancel()
	if req.AddMarketplace != "" {
		if !pluginspec.ValidMarketplaceSource(req.AddMarketplace) {
			return fail(extNotAllowed)
		}
		if err := p.AddMarketplace(ctx, req.AddMarketplace); err != nil {
			return fail(err)
		}
	}
	c, err := p.Catalog(ctx)
	if err != nil {
		return fail(err)
	}
	markets, plugins, cut := protocol.SanitizePluginCatalog(c.Marketplaces, c.Plugins)
	plugins, over := protocol.BudgetPluginCatalog(plugins, protocol.CatalogEncodedBudget)
	return protocol.PluginCatalogResult{OK: true, Marketplaces: markets, Plugins: plugins, Truncated: c.Truncated || cut || over}
}

// MCPSignIn answers mcp_sign_in. A started sign-in makes the session's MCP state read again for a
// while, so the row changes when the engine connects the server.
func (h *AgentHost) MCPSignIn(ctx context.Context, req protocol.MCPSignIn) protocol.MCPSignInResult {
	fail := func(err error) protocol.MCPSignInResult { return protocol.MCPSignInResult{ErrorCode: codeOf(err)} }
	if !protocol.ValidMCPServerName(req.Server) {
		return fail(extNotFound)
	}
	p, sess, err := h.extensionsFor(req.SessionID)
	if err != nil {
		return fail(err)
	}
	// One sign-in per server per session: a second click while one is starting is "busy"; one
	// started recently is reused (the engine is still listening for its callback), so repeated
	// clicks do not stack listeners on the person's machine.
	key := req.SessionID + "\x00" + req.Server
	release, cached, ok := signIns.begin(key, time.Now())
	if !ok {
		return fail(extBusy)
	}
	if cached != nil {
		return *cached
	}
	var res protocol.MCPSignInResult
	defer func() { release(res) }()
	ctx, cancel := context.WithTimeout(ctx, extSignInBudget)
	defer cancel()
	out, err := p.MCPSignIn(ctx, req.Server)
	if err != nil {
		res = fail(err)
		return res
	}
	if (out.AuthURL != "" && !protocol.ValidAuthURL(out.AuthURL)) || (out.RequiresUserAction && out.AuthURL == "") {
		res = fail(extRefused)
		return res
	}
	if t := sess.mcpState.Load(); t != nil {
		t.watch(mcpWatchMax)
	}
	res = protocol.MCPSignInResult{OK: true, AuthURL: out.AuthURL, RequiresUserAction: out.RequiresUserAction, CallbackExpected: out.CallbackExpected, Source: out.Source}
	return res
}

// signInReuse is how long a started sign-in's URL is handed out again instead of starting another
// flow: under the engines' own callback wait (Codex: 300 s).
const signInReuse = 4 * time.Minute

type signInEntry struct {
	running bool
	at      time.Time
	res     protocol.MCPSignInResult
}

// signInTable tracks sign-ins per session and server.
type signInTable struct {
	mu sync.Mutex
	m  map[string]*signInEntry
}

var signIns = &signInTable{m: map[string]*signInEntry{}}

// begin claims key. ok false: one is starting now. cached: a recent started sign-in to reuse.
// Otherwise the caller runs the sign-in and calls release with its result.
func (t *signInTable) begin(key string, now time.Time) (release func(protocol.MCPSignInResult), cached *protocol.MCPSignInResult, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.m {
		if !e.running && now.Sub(e.at) >= signInReuse {
			delete(t.m, k)
		}
	}
	if e := t.m[key]; e != nil {
		if e.running {
			return nil, nil, false
		}
		if e.res.OK && e.res.AuthURL != "" {
			res := e.res
			return nil, &res, true
		}
	}
	if len(t.m) >= 256 {
		return nil, nil, false
	}
	t.m[key] = &signInEntry{running: true, at: now}
	return func(res protocol.MCPSignInResult) {
		t.mu.Lock()
		defer t.mu.Unlock()
		if res.OK && res.AuthURL != "" {
			t.m[key] = &signInEntry{at: time.Now(), res: res}
		} else {
			delete(t.m, key)
		}
	}, nil, true
}

// ─── wire ────────────────────────────────────────────────────────────────────

func (m *Manager) handleReadPluginCatalog(raw []byte) {
	var req protocol.ReadPluginCatalog
	if len(raw) > 2048 || json.Unmarshal(raw, &req) != nil || m.agents == nil {
		return
	}
	if !validSessionOriginID(req.RequestID) || !validSessionOriginID(req.SessionID) {
		return
	}
	reply := func(res protocol.PluginCatalogResult) {
		res.Type, res.RequestID, res.SessionID = "plugin_catalog_result", req.RequestID, req.SessionID
		outcome := res.ErrorCode
		if res.OK {
			outcome = "ok"
		}
		log.Printf("plugin_catalog session=%q add=%t outcome=%s plugins=%d", req.SessionID, req.AddMarketplace != "", outcome, len(res.Plugins))
		_ = m.client.Send(res)
	}
	select {
	case extReads <- struct{}{}:
	default:
		go reply(protocol.PluginCatalogResult{ErrorCode: string(extBusy)})
		return
	}
	go func() {
		res, done := withSlotDeadline(extReads, extCatalogBudget+extSlotGrace, func() protocol.PluginCatalogResult {
			return m.agents.PluginCatalog(context.Background(), req)
		})
		if !done {
			res = protocol.PluginCatalogResult{ErrorCode: string(extUnavailable)}
		}
		reply(res)
	}()
}

func (m *Manager) handleMCPSignIn(raw []byte) {
	var req protocol.MCPSignIn
	if len(raw) > 2048 || json.Unmarshal(raw, &req) != nil || m.agents == nil {
		return
	}
	if !validSessionOriginID(req.RequestID) || !validSessionOriginID(req.SessionID) {
		return
	}
	reply := func(res protocol.MCPSignInResult) {
		res.Type, res.RequestID, res.SessionID = "mcp_sign_in_result", req.RequestID, req.SessionID
		outcome := res.ErrorCode
		if res.OK {
			outcome = "ok"
		}
		// Never the URL: it carries the flow's state.
		log.Printf("mcp_sign_in session=%q outcome=%s", req.SessionID, outcome)
		_ = m.client.Send(res)
	}
	select {
	case extSignIns <- struct{}{}:
	default:
		go reply(protocol.MCPSignInResult{ErrorCode: string(extBusy)})
		return
	}
	go func() {
		res, done := withSlotDeadline(extSignIns, extSignInBudget+extSlotGrace, func() protocol.MCPSignInResult {
			return m.agents.MCPSignIn(context.Background(), req)
		})
		if !done {
			res = protocol.MCPSignInResult{ErrorCode: string(extUnavailable)}
		}
		reply(res)
	}()
}
