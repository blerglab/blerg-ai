package mcpgw

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PathPrefix is where the gateway serves; the path carries only the connection name.
const PathPrefix = "/mcp-gw/"

// Defaults (spec 5.4).
const (
	DefaultCallTimeout    = 60 * time.Second
	DefaultMaxConcurrent  = 4
	DefaultMaxResultBytes = 256 << 10
	DefaultDescriptionCap = 2048 // runes
	maxRequestBytes       = 1 << 20
	maxUpstreamBytes      = 8 << 20
	callLogRetention      = 90 * 24 * time.Hour
)

// ProposalSink captures a tool call in `propose` mode instead of forwarding it. While no sink
// is installed (SetProposalSink, done by the runner's server start) propose-mode tools are
// treated as off: hidden from tools/list and refused on tools/call. With one, they are listed
// exactly like allow tools and their tools/call goes to the sink. A sink returns a
// *ProposalRefusal for a call the agent should be told was not queued.
type ProposalSink interface {
	// Propose freezes the call and returns the text the agent receives (the proposal id
	// and its runner URL). It is called only for a tool whose grant mode is propose and
	// whose pinned hash matches the upstream's current definition.
	Propose(ctx context.Context, req ProposalRequest) (string, error)
}

// ProposalRequest is everything a sink needs to freeze a call.
type ProposalRequest struct {
	SessionID      string
	AccountID      string
	ConnectionID   string
	ConnectionName string
	URLSnapshot    string
	Tool           string
	ToolHash       string
	Arguments      json.RawMessage
}

// Refresher asks the daemon running a session to have its agent list its MCP tools again
// (Hub.RefreshSessionTools). Installed by the server after the hub exists; nil means no refresh.
type Refresher interface {
	RefreshSessionTools(sessionID string) bool
}

// Config configures a Gateway. DB and Core are required; zero values elsewhere take the defaults.
type Config struct {
	DB   *pgxpool.Pool
	Core CoreClient
	// Policy is the runner's network policy for upstream servers (allow-lists come from
	// BLERG_RUNNER_MCP_ALLOW_HTTP_HOSTS and BLERG_RUNNER_MCP_ALLOW_PRIVATE_HOSTS).
	Policy netguard.Policy
	Sink   ProposalSink
	// Refresher asks the daemon to have a session's agent list tools again after a re-pin or grant.
	Refresher Refresher
	// Board and BoardMCPURL enable the built-in `board` connection (board.go): core's token
	// exchange and the board's MCP endpoint. Either unset leaves the built-in unavailable.
	Board       BoardExchanger
	BoardMCPURL string

	CallTimeout    time.Duration // per tools/list or tools/call, whole upstream exchange included
	MaxConcurrent  int           // per grant
	MaxResultBytes int
	DescriptionCap int // runes
	HashCacheTTL   time.Duration
	CredMaxTTL     time.Duration

	FailedAuthLimit  int // failed authentications per (address, token) per window before that pair is blocked; an address as a whole gets ten times as many
	FailedAuthWindow time.Duration

	ChangeLimits db.ToolChangeLimits // caps on pending tool changes; zero: the defaults

	Now func() time.Time // tests
}

// Gateway is the MCP gateway handler.
type Gateway struct {
	cfg    Config
	client *http.Client
	// boardClient reaches the board's MCP endpoint and nothing else (BoardHTTPClient).
	boardClient *http.Client
	limiter     *failLimiter // failed authentications per (address, token)
	// ipLimiter counts every failure per address, with a far larger allowance than limiter: it
	// bounds token-less and malformed spraying.
	ipLimiter *failLimiter

	sinkMu sync.RWMutex
	sink   ProposalSink

	refresherMu sync.RWMutex
	refresher   Refresher

	observerMu sync.RWMutex
	observer   BoardObserver

	statesMu sync.Mutex
	states   map[string]*grantState

	// trusted are the proxies whose forwarding headers the public route believes
	// (SetTrustedProxies); lookups bounds its database lookups for throttled addresses.
	trustedMu sync.RWMutex
	trusted   []netip.Prefix
	lookups   *lookupBudget
	recent    recentGrants
	// unrevoked are the session board credentials whose revoke failed, per account, until they
	// expire (CallBoard stops minting past the unrevoked limit, maxUnrevokedBoardTokens unless
	// SetUnrevokedBoardTokenLimit set another).
	unrevoked      unrevokedBoardTokens
	unrevokedLimit atomic.Int64
}

// SetTrustedProxies sets the proxies (BLERG_RUNNER_TRUSTED_PROXIES) whose X-Forwarded-For and
// X-Real-IP the public route believes when it throttles a client. nil trusts none: every client
// behind a proxy then shares the proxy's allowance.
func (g *Gateway) SetTrustedProxies(p []netip.Prefix) {
	g.trustedMu.Lock()
	g.trusted = p
	g.trustedMu.Unlock()
}

func (g *Gateway) trustedProxies() []netip.Prefix {
	g.trustedMu.RLock()
	defer g.trustedMu.RUnlock()
	return g.trusted
}

// New builds a Gateway.
func New(cfg Config) *Gateway {
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultCallTimeout
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = DefaultMaxConcurrent
	}
	if cfg.MaxResultBytes <= 0 {
		cfg.MaxResultBytes = DefaultMaxResultBytes
	}
	if cfg.DescriptionCap <= 0 {
		cfg.DescriptionCap = DefaultDescriptionCap
	}
	if cfg.HashCacheTTL <= 0 {
		cfg.HashCacheTTL = 10 * time.Second
	}
	if cfg.CredMaxTTL <= 0 {
		cfg.CredMaxTTL = 5 * time.Minute
	}
	if cfg.FailedAuthLimit <= 0 {
		cfg.FailedAuthLimit = 30
	}
	if cfg.FailedAuthWindow <= 0 {
		cfg.FailedAuthWindow = time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ChangeLimits.Session <= 0 {
		cfg.ChangeLimits.Session = db.DefaultMaxPendingChangesPerSession
	}
	if cfg.ChangeLimits.Account <= 0 {
		cfg.ChangeLimits.Account = db.DefaultMaxPendingChangesPerAccount
	}
	g := &Gateway{cfg: cfg, sink: cfg.Sink, refresher: cfg.Refresher, states: map[string]*grantState{}}
	g.limiter = newFailLimiter(cfg.FailedAuthLimit, cfg.FailedAuthWindow, cfg.Now)
	g.ipLimiter = newFailLimiter(cfg.FailedAuthLimit*10, cfg.FailedAuthWindow, cfg.Now)
	g.lookups = newLookupBudget(publicLookupsPerSecond, publicLookupBurst, cfg.Now)
	// The overall client timeout is a backstop; the per-call context is what normally ends a call.
	g.client = cfg.Policy.Client(cfg.CallTimeout+30*time.Second, maxUpstreamBytes)
	g.boardClient = BoardHTTPClient(cfg.BoardMCPURL, cfg.CallTimeout+30*time.Second, maxUpstreamBytes)
	return g
}

// BoardObserver hears each successful tool call on a session's built-in board connection (the
// runner keeps the session's plan from the moves it sees: server/plansync.go). It must return at
// once; it gets the board, the tool and the sanitized result.
type BoardObserver interface {
	BoardToolCalled(sessionID, boardID, tool string, result json.RawMessage)
}

// SetBoardObserver installs the observer (nil removes it).
func (g *Gateway) SetBoardObserver(o BoardObserver) {
	g.observerMu.Lock()
	g.observer = o
	g.observerMu.Unlock()
}

func (g *Gateway) boardObserver() BoardObserver {
	g.observerMu.RLock()
	defer g.observerMu.RUnlock()
	return g.observer
}

// SetProposalSink installs the sink for propose-mode tools (nil removes it).
func (g *Gateway) SetProposalSink(s ProposalSink) {
	g.sinkMu.Lock()
	g.sink = s
	g.sinkMu.Unlock()
}

// ProposalSink returns the installed sink (nil when none), so a caller can wrap it.
func (g *Gateway) ProposalSink() ProposalSink { return g.proposalSink() }

func (g *Gateway) proposalSink() ProposalSink {
	g.sinkMu.RLock()
	defer g.sinkMu.RUnlock()
	return g.sink
}

func (g *Gateway) now() time.Time { return g.cfg.Now() }

// CallTimeout is the per-call upstream timeout the gateway applies.
func (g *Gateway) CallTimeout() time.Duration { return g.cfg.CallTimeout }

// Handler is the gateway's HTTP handler: POST /mcp-gw/{name}. It belongs on its own
// listener, never on the runner's main mux.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(PathPrefix+"{name}", func(w http.ResponseWriter, r *http.Request) { g.serve(w, r, nil) })
	return mux
}

// PublicHandler is the gateway as mounted on the runner's public web port, for sessions on
// workstation daemons that cannot reach its own listener. It differs from Handler in three ways:
//
//   - accept is asked about each authenticated grant before anything else of the request is read;
//     the runner accepts only grants issued for this route, of a live daemon session, within their
//     idle limit.
//   - Every refusal before a grant is accepted (no token, a malformed, unknown or stale one, a grant
//     accept refuses, a path that does not name the grant's connection) is the same JSON-RPC 401,
//     so the route never tells whether a token exists.
//     When accept cannot answer (its database failed) the request gets a retryable 503 instead:
//     the token stays in the recent-grant cache and no failure is counted against it.
//   - A client is the real one behind a trusted proxy (SetTrustedProxies). A malformed token from
//     an address over its failure allowance is turned away; a well-formed one is still looked up,
//     within a global budget, so a valid token is served.
func (g *Gateway) PublicHandler(accept func(context.Context, *db.MCPGrant) (bool, error)) http.Handler {
	mux := http.NewServeMux()
	m := &mount{accept: accept, public: true}
	mux.HandleFunc(PathPrefix+"{name}", func(w http.ResponseWriter, r *http.Request) { g.serve(w, r, m) })
	return mux
}

// mount is how the gateway is mounted: nil is its own listener (Handler).
type mount struct {
	accept func(context.Context, *db.MCPGrant) (bool, error)
	public bool
}

// publicLookupsPerSecond and publicLookupBurst bound the database lookups the public route makes
// for well-formed tokens from addresses over their failure allowance (lookupBudget).
const (
	publicLookupsPerSecond = 100
	publicLookupBurst      = 200
)

// codeUnauthorized is the JSON-RPC error code of the public route's uniform refusal.
const codeUnauthorized = -32001

// Run prunes the call log now and then hourly, and drops idle per-grant state, until ctx ends.
func (g *Gateway) Run(ctx context.Context) {
	g.Prune(ctx)
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Prune(ctx)
		}
	}
}

// Prune deletes call-log rows older than 90 days and forgets idle per-grant state.
func (g *Gateway) Prune(ctx context.Context) {
	g.sweepStates(time.Hour)
	// A grant normally goes when its session ends; this catches the ones a crashed runner left.
	if n, err := db.DeleteOrphanMCPGrants(ctx, g.cfg.DB); err != nil {
		log.Printf("mcpgw: sweep orphan grants: %v", err)
	} else if n > 0 {
		log.Printf("mcpgw: swept %d grants of ended sessions", n)
	}
	g.reapBoardStates(ctx) // board tokens of grants deleted behind the gateway's back
	if n, err := db.PruneMCPCallLog(ctx, g.cfg.DB, callLogRetention); err != nil {
		log.Printf("mcpgw: prune call log: %v", err)
	} else if n > 0 {
		log.Printf("mcpgw: pruned %d call log rows", n)
	}
}

func (g *Gateway) sweepStates(idle time.Duration) {
	cutoff := g.now().Add(-idle).UnixNano()
	g.statesMu.Lock()
	defer g.statesMu.Unlock()
	for k, st := range g.states {
		if st.inflight.Load() == 0 && st.lastUsed.Load() < cutoff {
			delete(g.states, k)
		}
	}
}

// stateFor returns the per-grant state, creating it on first use. It returns nil when no grant with
// the token hash is stored any more: a request that got past authentication just before the
// session ended must not recreate the state (and with it mint a token nothing revokes) after the
// end's revocation deleted it. The state is keyed by the grant's token hash, so a resumed session's
// new grant gets its own state, never the old one's.
func (g *Gateway) stateFor(ctx context.Context, gr *db.MCPGrant) *grantState {
	key := hex.EncodeToString(gr.TokenHash)
	g.statesMu.Lock()
	st, ok := g.states[key]
	if ok {
		st.touch()
	}
	g.statesMu.Unlock()
	if ok {
		return st
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	exists := g.grantExists(lctx, gr.TokenHash)
	cancel()
	if !exists {
		return nil
	}
	g.statesMu.Lock()
	defer g.statesMu.Unlock()
	if st, ok = g.states[key]; !ok {
		st = &grantState{g: g, sem: make(chan struct{}, g.cfg.MaxConcurrent),
			builtin: gr.Builtin != "", sessionID: gr.SessionID, account: gr.AccountID, tokenHash: gr.TokenHash}
		g.states[key] = st
	}
	st.touch()
	return st
}

// ---- HTTP plumbing ------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, msg string) {
	if len(bytes.TrimSpace(id)) == 0 {
		id = json.RawMessage("null")
	}
	writeJSON(w, status, map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{Code: code, Message: msg}})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// bearer returns the token of an "Authorization: Bearer" header ("" when absent or absurd).
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 8 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	tok := strings.TrimSpace(h[7:])
	if len(tok) > 256 {
		return ""
	}
	return tok
}

// plausibleToken reports whether tok has the form of a gateway token ("gw_" plus 64 hex
// characters). Anything else can never match a grant, so it is refused without a database lookup.
func plausibleToken(tok string) bool {
	if len(tok) != 67 || !strings.HasPrefix(tok, "gw_") {
		return false
	}
	for _, c := range tok[3:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// authenticate resolves the request's token to its grant before anything else is read.
//
// The token is checked FIRST and only failures are rate limited, so a valid token is never turned
// away, whatever else shares its source address (every sandbox container behind one NAT address
// shares it). A failure is counted against the (address, token) pair, so one container repeating a
// stale token blocks only itself, and against the address as a whole with a much larger allowance
// (spraying random or missing tokens).
//
// On the public route (m.public) the client is the real one behind a trusted proxy, a well-formed
// token from an address over its allowance is still looked up within a global budget, and every
// refusal is JSON-RPC shaped.
func (g *Gateway) authenticate(w http.ResponseWriter, r *http.Request, m *mount) (*db.MCPGrant, string) {
	ip := clientIP(r)
	public := m != nil && m.public
	if public {
		ip = publicClientIP(r, g.trustedProxies())
	}
	tooMany := func() {
		w.Header().Set("Retry-After", "60")
		if public {
			writeRPCError(w, http.StatusTooManyRequests, nil, codeUnauthorized, "too many failed attempts")
			return
		}
		http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
	}
	unauthorized := func(keys ...string) {
		g.ipLimiter.fail(ip)
		for _, k := range keys {
			g.limiter.fail(k)
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		if public {
			writeRPCError(w, http.StatusUnauthorized, nil, codeUnauthorized, "unauthorized")
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
	tok := bearer(r)
	if !plausibleToken(tok) {
		// Nothing to look up: a missing or malformed token only ever counts against the address.
		if g.ipLimiter.blocked(ip) {
			tooMany()
			return nil, ""
		}
		unauthorized()
		return nil, ""
	}
	sum := sha256.Sum256([]byte(tok))
	key := ip + "|" + hex.EncodeToString(sum[:6])
	if g.limiter.blocked(key) {
		// This very token already failed too often from this address: it is stale, and a
		// stale token does not become valid, so skip the lookup.
		tooMany()
		return nil, ""
	}
	if public && g.ipLimiter.blocked(ip) && !g.recent.contains(sum, g.now()) && !g.lookups.take() {
		// The public route: an address over its allowance still has a well-formed token looked
		// up, so a valid one is served, but only within a global budget that bounds the lookups
		// a flood can cause.
		tooMany()
		return nil, ""
	}
	gr, err := db.MCPGrantByTokenHash(r.Context(), g.cfg.DB, sum[:])
	if errors.Is(err, db.ErrMCPGrantNotFound) {
		g.recent.forget(sum)
		// A well-formed token that matches nothing counts against the address as well as the pair,
		// and past the address allowance is turned away with 429, so spraying distinct plausible
		// tokens is bounded too. A valid token never reaches here, so it is never turned away.
		if g.ipLimiter.blocked(ip) {
			tooMany()
			return nil, ""
		}
		unauthorized(key)
		return nil, ""
	}
	if err != nil {
		log.Printf("mcpgw: grant lookup: %v", err)
		if public {
			writeRPCError(w, http.StatusServiceUnavailable, nil, codeGateway, "unavailable")
			return nil, ""
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return nil, ""
	}
	if subtle.ConstantTimeCompare(gr.TokenHash, sum[:]) != 1 {
		g.recent.forget(sum)
		unauthorized(key)
		return nil, ""
	}
	return gr, key
}

func (g *Gateway) serve(w http.ResponseWriter, r *http.Request, m *mount) {
	gr, key := g.authenticate(w, r, m)
	if gr == nil {
		return
	}
	if m != nil && m.public {
		accepted := true
		if m.accept != nil {
			ok, err := m.accept(r.Context(), gr)
			if err != nil {
				// The runner could not tell: not a refusal, so the token keeps its place in the
				// recent-grant cache and nothing counts against it. Accept runs before the path check,
				// so a valid token on another connection's path also gets this 503 while the database
				// is down, not the 401: only a holder of a valid token can see that, and no caller can
				// cause the database error.
				log.Printf("mcpgw: public route accept: %v", err)
				w.Header().Set("Retry-After", "5")
				writeRPCError(w, http.StatusServiceUnavailable, nil, codeGateway, "unavailable")
				return
			}
			accepted = ok
		}
		// Refused exactly like an unknown token, and counted the same way.
		if !accepted || r.PathValue("name") != gr.Name {
			g.recent.forget(sha256.Sum256([]byte(bearer(r))))
			ip := publicClientIP(r, g.trustedProxies())
			if g.ipLimiter.blocked(ip) {
				// From a throttled address every unsuccessful token gets the same 429, a token
				// that exists but is refused here included.
				w.Header().Set("Retry-After", "60")
				writeRPCError(w, http.StatusTooManyRequests, nil, codeUnauthorized, "too many failed attempts")
				return
			}
			g.ipLimiter.fail(ip)
			g.limiter.fail(key)
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeRPCError(w, http.StatusUnauthorized, nil, codeUnauthorized, "unauthorized")
			return
		}
	}
	if m != nil && m.public {
		g.recent.remember(sha256.Sum256([]byte(bearer(r))), g.now())
	}
	// One token per (session, connection): the path names the connection and must agree.
	if r.PathValue("name") != gr.Name {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		writeRPCError(w, http.StatusRequestEntityTooLarge, nil, codeInvalidRequest, "request too large")
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		writeRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest, "batch requests are not supported")
		return
	}
	var req rpcMessage
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, codeParseError, "parse error")
		return
	}
	if req.Method == "" {
		// A response or something else: the gateway never sends requests, so none is expected.
		writeRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest, "invalid request")
		return
	}
	if req.hasID() && !validID(req.ID) {
		writeRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest, "invalid request id")
		return
	}
	// A client may omit the header (Claude Code does on initialize); when it names a version
	// it must be one the gateway speaks.
	if v := r.Header.Get("Mcp-Protocol-Version"); v != "" && req.Method != "initialize" {
		if _, ok := negotiateVersion(v); !ok {
			writeRPCError(w, http.StatusBadRequest, req.ID, codeInvalidRequest, "unsupported protocol version")
			return
		}
	}
	g.dispatch(w, r, gr, req)
}

func (g *Gateway) dispatch(w http.ResponseWriter, r *http.Request, gr *db.MCPGrant, req rpcMessage) {
	switch req.Method {
	case "notifications/initialized", "notifications/cancelled":
		if req.hasID() {
			writeRPCError(w, http.StatusBadRequest, req.ID, codeInvalidRequest, "notifications carry no id")
			return
		}
		w.WriteHeader(http.StatusAccepted)
	case "initialize", "ping", "tools/list", "tools/call":
		if !req.hasID() {
			writeRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest, "request without an id")
			return
		}
		switch req.Method {
		case "initialize":
			g.handleInitialize(w, req)
		case "ping":
			writeRPCResult(w, req.ID, map[string]any{})
		case "tools/list":
			g.handleToolsList(w, r, gr, req)
		default:
			g.handleToolsCall(w, r, gr, req)
		}
	default:
		// Everything else is refused, including the server/discover probe newer Claude Code
		// versions send first: a JSON-RPC error at HTTP 200 is what makes them fall back.
		if !req.hasID() {
			writeRPCError(w, http.StatusBadRequest, nil, codeMethodNotFound, "method not found")
			return
		}
		writeRPCError(w, http.StatusOK, req.ID, codeMethodNotFound, "method not found")
	}
}

func (g *Gateway) handleInitialize(w http.ResponseWriter, req rpcMessage) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &p)
	version, _ := negotiateVersion(p.ProtocolVersion)
	writeRPCResult(w, req.ID, map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": "blerg-gateway", "version": "1"},
	})
}

// ---- tools/list and tools/call --------------------------------------------------------

// SetRefresher installs the daemon refresh (nil removes it).
func (g *Gateway) SetRefresher(r Refresher) {
	g.refresherMu.Lock()
	g.refresher = r
	g.refresherMu.Unlock()
}

func (g *Gateway) refresherFn() Refresher {
	g.refresherMu.RLock()
	defer g.refresherMu.RUnlock()
	return g.refresher
}

// outcome is what one gateway call produced, for the reply and the call log.
type outcome struct {
	result  json.RawMessage
	rpcErr  *rpcError
	tool    string
	mode    string
	outcome string
}

// toolChangedMessage is the refusal when a tool's live definition no longer matches the hash the
// session pinned: the tool is off until a person selects it again, in a new session.
func toolChangedMessage(tool string) string {
	return "tool " + tool + " changed on the server since this session selected it, so it is off here; ask the person to start a new session and select it again"
}

// toolUnavailableMessage is the refusal while a changed definition waits for the person (strict):
// neutral on purpose, so the agent does not lobby for the change.
func toolUnavailableMessage(tool string) string {
	return "tool " + tool + " is unavailable in this session because its definition changed; continue without it and do not ask the person to enable it"
}

// toolRelistMessage is the refusal of a call to a changed tool under auto_accept: the re-pin
// happens on tools/list, so the agent must list first and call with the new definition.
func toolRelistMessage(tool string) string {
	return "tool " + tool + "'s definition changed; list tools again before calling it"
}

// toolReenabledMessage is the refusal of a call to an entry re-pinned or granted since the agent
// last listed (listed:false): it is calling with a definition it has not seen.
func toolReenabledMessage(tool string) string {
	return "tool " + tool + " was re-enabled with a new definition; list tools again before calling it"
}

func refuse(mode, tool, out string, code int, msg string) outcome {
	return outcome{rpcErr: &rpcError{Code: code, Message: msg}, tool: tool, mode: mode, outcome: out}
}

// upstreamFailure maps an error from the upstream exchange to a reply and an outcome label.
func upstreamFailure(ctx context.Context, mode, tool string, err error) outcome {
	switch {
	case errors.Is(err, ErrConnectionGone):
		return refuse(mode, tool, "gone", codeGateway,
			"connection no longer available: it was removed, or the sign-in that started this session has ended")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return refuse(mode, tool, "timeout", codeGateway, "the upstream call timed out")
	case errors.Is(err, netguard.ErrBodyTooLarge):
		return refuse(mode, tool, "error", codeGateway, "the upstream server's reply was too large")
	case errors.Is(err, errBudgetExhausted):
		return refuse(mode, tool, "budget", codeGateway, "the call budget for this session and connection is used up")
	default:
		// The class only: a *url.Error prints the request URL, and an upstream URL may carry
		// something that must not reach a log.
		log.Printf("mcpgw: upstream error (%s)", errorClass(err))
		return refuse(mode, tool, "error", codeGateway, upstreamMessage(err))
	}
}

// errConnectionURLRefused is what a connection URL the network policy rejects turns into. The
// policy's own reason text (which can name an address or a range) is deliberately dropped.
var errConnectionURLRefused = errors.New("the connection's address is not permitted")

// errorClass names a class of upstream error with a fixed string, never the error's own text.
func errorClass(err error) string {
	var ue *url.Error
	var ne net.Error
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, errConnectionURLRefused), errors.Is(err, netguard.ErrBlockedAddress), errors.Is(err, netguard.ErrURLNotPermitted):
		return "blocked by network policy"
	case errors.Is(err, ErrConnectionGone):
		return "connection gone"
	case errors.Is(err, errUpstreamAuth):
		return "upstream rejected the credential"
	case errors.Is(err, errUpstreamSession):
		return "upstream session lost"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &ue), errors.As(err, &ne):
		return "network error"
	default:
		return "error"
	}
}

// upstreamMessage is the text an agent sees for an upstream failure: our own wording for
// known cases, a generic line otherwise (never a raw network error, which can name hosts).
func upstreamMessage(err error) string {
	if errors.Is(err, errConnectionURLRefused) || errors.Is(err, netguard.ErrBlockedAddress) || errors.Is(err, netguard.ErrURLNotPermitted) {
		return errConnectionURLRefused.Error() // fixed text: never the policy's reason
	}
	for _, known := range []error{errUpstreamAuth, errUpstreamSession, errNoResponse, errBoardAccess, errBoardNotConfigured} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	msg := err.Error()
	for _, prefix := range []string{"the upstream server", "connection ", "the connection's"} {
		if strings.HasPrefix(msg, prefix) {
			return capRunes(msg, 300)
		}
	}
	return "the upstream call failed"
}

func upstreamRPCError(mode, tool string, e *rpcError) outcome {
	return outcome{
		rpcErr:  &rpcError{Code: codeGateway, Message: "upstream error: " + capRunes(e.Message, 300)},
		tool:    tool,
		mode:    mode,
		outcome: "upstream_error",
	}
}

// guarded runs fn under the grant's concurrency limit and call budget, with the per-call
// timeout, then logs and writes the reply. A call over the limit fails immediately.
func (g *Gateway) guarded(w http.ResponseWriter, r *http.Request, gr *db.MCPGrant, req rpcMessage, mode, tool string,
	fn func(ctx context.Context, st *grantState) outcome) {
	start := g.now()
	st := g.stateFor(r.Context(), gr)
	var oc outcome
	switch {
	case st == nil:
		oc = refuse(mode, tool, "gone", codeGateway,
			"connection no longer available: it was removed, or the sign-in that started this session has ended")
	case !st.tryAcquire():
		oc = refuse(mode, tool, "busy", codeGateway, "too many concurrent calls on this connection; retry shortly")
	default:
		defer st.release()
		ctx, cancel := context.WithTimeout(r.Context(), g.cfg.CallTimeout)
		defer cancel()
		ok, err := db.ConsumeMCPCall(ctx, g.cfg.DB, gr.SessionID, gr.ConnectionID)
		switch {
		case err != nil:
			log.Printf("mcpgw: budget: %v", err)
			oc = refuse(mode, tool, "error", codeGateway, "the call budget could not be checked")
		case !ok:
			oc = refuse(mode, tool, "budget", codeGateway, "the call budget for this session and connection is used up")
		default:
			oc = fn(ctx, st)
		}
	}
	if oc.outcome != "busy" && oc.outcome != "budget" {
		// Refusals for load or budget are not logged: a runaway agent would write one row per
		// refused call for as long as it kept trying.
		g.logCall(r.Context(), gr, oc, g.now().Sub(start))
	}
	if oc.rpcErr != nil {
		writeRPCError(w, http.StatusOK, req.ID, oc.rpcErr.Code, oc.rpcErr.Message)
		return
	}
	writeRPCResult(w, req.ID, oc.result)
}

func (g *Gateway) logCall(parent context.Context, gr *db.MCPGrant, oc outcome, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	if err := db.InsertMCPCallLog(ctx, g.cfg.DB, db.MCPCallLogEntry{
		SessionID: gr.SessionID, ConnectionID: gr.ConnectionID, Tool: oc.tool, Mode: oc.mode,
		Outcome: oc.outcome, DurationMS: int(d.Milliseconds()),
	}); err != nil {
		log.Printf("mcpgw: call log: %v", err)
	}
}

// effectiveMode is the grant's mode for a tool with propose folded into off while no sink
// is installed. Anything not allow or propose is off.
func (g *Gateway) effectiveMode(t db.MCPGrantTool, listed bool) string {
	if !listed {
		return "off"
	}
	switch t.Mode {
	case ModeAllow:
		return ModeAllow
	case ModePropose:
		if g.proposalSink() != nil {
			return ModePropose
		}
	}
	return "off"
}

// toolPinOK reports whether a tool the upstream lists may be offered: a user's connection must still
// match the hash the person confirmed; a built-in has no pin but offers only its fixed tool set.
func toolPinOK(gr *db.MCPGrant, entry db.MCPGrantTool, name, upstreamHash string) bool {
	if gr.Builtin != "" {
		return builtinToolOffered(name)
	}
	return entry.Hash == upstreamHash
}

type listedTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func (g *Gateway) handleToolsList(w http.ResponseWriter, r *http.Request, gr *db.MCPGrant, req rpcMessage) {
	g.guarded(w, r, gr, req, "list", "", func(ctx context.Context, st *grantState) outcome {
		tools, complete, rerr, err := st.listUpstream(ctx, gr, maxPages, true)
		if err != nil {
			return upstreamFailure(ctx, "list", "", err)
		}
		if rerr != nil {
			return upstreamRPCError("list", "", rerr)
		}
		serve, listed := g.reconcileListing(ctx, gr, st, tools, complete)
		out := []listedTool{}
		for _, t := range serve {
			schema := t.InputSchema
			if len(bytes.TrimSpace(schema)) == 0 || string(bytes.TrimSpace(schema)) == "null" {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			out = append(out, listedTool{Name: t.Name, Description: capRunes(t.Description, g.cfg.DescriptionCap), InputSchema: schema})
		}
		g.markListed(ctx, gr, listed)
		res, _ := json.Marshal(map[string]any{"tools": out})
		return outcome{result: res, mode: "list", outcome: "ok"}
	})
}

func (g *Gateway) handleToolsCall(w http.ResponseWriter, r *http.Request, gr *db.MCPGrant, req rpcMessage) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		writeRPCError(w, http.StatusOK, req.ID, codeInvalidParams, "invalid params")
		return
	}
	if args := bytes.TrimSpace(p.Arguments); len(args) > 0 && args[0] != '{' && string(args) != "null" {
		writeRPCError(w, http.StatusOK, req.ID, codeInvalidParams, "arguments must be an object")
		return
	}
	entry, listed := gr.Tools[p.Name]
	mode := g.effectiveMode(entry, listed)
	switch {
	case gr.Builtin != "" && !builtinToolOffered(p.Name):
		mode = "off" // a built-in offers its fixed set, whatever a grant row says
	case gr.Builtin != "" && boardColumnToolSet[p.Name]:
		mode = ModeAllow // column management: offered by the level, not by a grant row (launches before 0.5.0 have none)
	}
	g.guarded(w, r, gr, req, mode, p.Name, func(ctx context.Context, st *grantState) outcome {
		unknown := refuse(mode, p.Name, "refused", codeInvalidParams, "unknown tool")
		if mode == "off" {
			return unknown
		}
		if gr.Builtin != "" {
			// The session's board level (0.4.4), read on every call: lowering or revoking it holds
			// at the agent's next call.
			level, person := g.boardGrantAccess(ctx, gr)
			if !BoardLevelAllows(level, p.Name) {
				return refuse(mode, p.Name, "refused", codeInvalidParams, BoardLevelRefusal(gr.BuiltinRef, level, p.Name))
			}
			if boardColumnToolSet[p.Name] {
				// Column management acts for a person: the current permission's sign-in, or the
				// launch's when a person launched it. A cron or an unraised token launch has none.
				if person == nil {
					return unknown
				}
				if gr.ProofKind != "session_id" || gr.ProofValue != person.SessionID {
					// The launch credential is not the person's: one call with their sign-in.
					res, err := g.CallBoard(ctx, BoardCall{Proof: *person, BoardID: gr.BuiltinRef, SessionRef: gr.SessionID, Tool: p.Name, Arguments: p.Arguments})
					if err != nil {
						return outcome{result: errorResult(UpstreamMessage(err)), tool: p.Name, mode: mode, outcome: "error"}
					}
					return outcome{result: res, tool: p.Name, mode: mode, outcome: "ok"}
				}
			}
		}
		if gr.Builtin == "" {
			if !entry.IsListed() {
				// Re-pinned or granted since the agent last listed: it would call with a
				// definition it has not seen.
				return refuse(mode, p.Name, "changed", codeInvalidParams, toolReenabledMessage(p.Name))
			}
			// The pinned hash must still match the upstream's definition, or the tool is off. A
			// built-in's tools are the board's own and are not pinned.
			live, hash, ok, rerr, err := st.currentTool(ctx, gr, p.Name, entry.Hash)
			if err != nil {
				return upstreamFailure(ctx, mode, p.Name, err)
			}
			if rerr != nil {
				return upstreamRPCError(mode, p.Name, rerr)
			}
			if !ok {
				return unknown
			}
			if hash != entry.Hash {
				// The server changed this tool since the session selected it. A call never
				// re-pins (§4.1): under auto_accept the next tools/list does, so the agent is
				// asked to list, unless the list would refuse too (throttled, or unreviewable);
				// under strict the change is recorded for the person.
				autoOK := st.policyFor(ctx, gr) == PinPolicyAutoAccept && !st.throttled(p.Name, g.now()) &&
					!g.hashBlocked(ctx, gr, upstreamTool{Name: p.Name, Hash: hash}) &&
					g.reviewableChange(st, live, hash)
				if autoOK {
					g.requestRefresh(gr, st)
					return refuse(mode, p.Name, "changed", codeInvalidParams, toolRelistMessage(p.Name))
				}
				// With no fresh listing (the cached hash answered inside the refresh gap) there is
				// no definition to store; the list that records the change stores it, and
				// RecordToolChange is idempotent.
				out := g.recordAndStore(ctx, gr, st, db.ToolChangeChanged, p.Name, entry.Hash, hash, entry.Mode, live)
				if out.Blocked {
					// Declined or expired in this session: the only remedy left is a new session.
					return refuse(mode, p.Name, "changed", codeInvalidParams, toolChangedMessage(p.Name))
				}
				return refuse(mode, p.Name, "changed", codeInvalidParams, toolUnavailableMessage(p.Name))
			}
			g.seedDrifting(ctx, gr, st)
			g.settled(ctx, gr, st, p.Name) // restored: an open row is superseded even if the agent only calls
		}
		if mode == ModePropose {
			text, err := g.proposalSink().Propose(ctx, ProposalRequest{
				SessionID: gr.SessionID, AccountID: gr.AccountID, ConnectionID: gr.ConnectionID,
				ConnectionName: gr.Name, URLSnapshot: gr.URLSnapshot, Tool: p.Name, ToolHash: entry.Hash,
				Arguments: p.Arguments,
			})
			var refusal *ProposalRefusal
			if errors.As(err, &refusal) {
				// A refusal the agent should hear (over the pending cap, arguments too large). It
				// already spent one call of the budget.
				return outcome{result: errorResult(refusal.Msg), tool: p.Name, mode: mode, outcome: "refused"}
			}
			if err != nil {
				log.Printf("mcpgw: proposal sink: %v", err)
				return outcome{result: errorResult("the action could not be queued for approval"), tool: p.Name, mode: mode, outcome: "error"}
			}
			return outcome{result: textResult(text), tool: p.Name, mode: mode, outcome: "ok"}
		}
		params := map[string]any{"name": p.Name}
		if len(bytes.TrimSpace(p.Arguments)) > 0 && string(bytes.TrimSpace(p.Arguments)) != "null" {
			params["arguments"] = p.Arguments
		}
		res, rerr, err := st.request(ctx, gr, "tools/call", params)
		if err != nil {
			return upstreamFailure(ctx, mode, p.Name, err)
		}
		if rerr != nil {
			return upstreamRPCError(mode, p.Name, rerr)
		}
		result := sanitizeResult(res, g.cfg.MaxResultBytes)
		if o := g.boardObserver(); o != nil && gr.Builtin == BuiltinBoard {
			o.BoardToolCalled(gr.SessionID, gr.BuiltinRef, p.Name, result)
		}
		return outcome{result: result, tool: p.Name, mode: mode, outcome: "ok"}
	})
}
