package server

// Starting a session with MCP connections (spec 6.1, 5.2, 8).
//
// ONE function, resolveGrant, turns what a person selected into a ResolvedGrant, and ONE
// function, attachGrant, puts a ResolvedGrant on a session that has been recorded. Every
// spawn path calls them, so none can drop a grant silently:
//
//	POST /api/sessions   cluster with a repo    HandlePostSessions
//	POST /api/sessions   cluster, no repo       startClusterNoRepo
//	POST /api/sessions   daemon (Docker)        HandlePostSessions
//	StartSession         cluster                startRunnerSession
//	StartSession         daemon (Docker)        startBoardSessionOnDaemon
//
// mcpstart_test.go enumerates all five and fails when one neither attaches the grant nor
// refuses. The rules:
//
//   - Only the public human route resolves an `mcp` field: Kind "human" with a live login
//     session id, which becomes the grant's proof. The v1 start, the runner's MCP
//     start_session tool and any agent principal are refused, and RunnerStartRequest.Grant is
//     json:"-" so a request body can never carry one. The scheduler builds its own in process.
//   - A grant session must be a Claude agent-kind session in a cluster pod or the Docker
//     sandbox, never on the bare host, on a daemon that reports mcp_gateway, with the gateway
//     configured (BLERG_RUNNER_MCP_GW_ADDR) and its address for sessions set
//     (BLERG_RUNNER_MCP_GW_URL).
//   - Restricted sessions follow who is watching, not the grant
//     (docs/design/interactive-mcp-sessions.md). Every cron session (grant or not) and every
//     board-started grant session (StartSession with Grant) is started with RestrictTools
//     (protocol.SpawnSession, or restrictToolsEnvVar in a pod's Job), set only server-side,
//     never from a request, and recorded in sessions.restrict_tools for a resume. The daemon or
//     pod then runs Claude with the built-in tool allow-list (file tools only),
//     --strict-mcp-config (no ambient MCP server), and an empty --setting-sources plus
//     --disable-slash-commands, so none of the developer's settings, hooks, plugins or skills
//     load (daemon.ccHardeningFlags has the evidence). A launch-sheet session with connections
//     (POST /api/sessions, human principal) is NOT restricted: it keeps its tools, settings and
//     plugins, and only the grant bounds what the MCP servers may be asked; it still runs with
//     --strict-mcp-config and the gateway config directory denied to the file tools. A daemon
//     whose hello lacks restrict_tools would ignore the field and run a restricted session
//     open, so it is refused for a grant or a cron start (checkRestrictTarget) and the
//     scheduler's pre-flight treats it as no capacity.
//   - Every connection must be the caller's own (core's list) and every selected tool's hash
//     must equal the hash in a live upstream tools/list at this moment.
//   - The raw gateway tokens travel only inside the per-session Kubernetes Secret (cluster) or
//     protocol.SpawnSession.MCPGateway (daemon). The session is marked private AFTER its
//     spawning account is recorded.
//   - Grants are deleted when the session ends (revokeSessionGrants, called wherever session
//     tokens are revoked) and re-issued on a cluster resume.
//
// `propose` mode is accepted and stored. The gateway lists a propose tool like any other and
// freezes its calls into a proposal a person approves (proposals.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// mcpGatewayEnvVar is the ONE environment variable a cluster pod receives its gateway grant
// through (the JSON of a protocol.MCPGatewayConfig, sourced from the per-session Secret with a
// secretKeyRef). It is daemon.MCPGatewayEnvVar; this package cannot import the daemon (the
// daemon's tests import this package), so mcpstart_parity_test.go pins the two together.
const mcpGatewayEnvVar = "BLERG_RUNNER_MCP_CONFIG"

// restrictToolsEnvVar is the plain (non-secret) environment variable that tells a cluster pod to
// run Claude Code restricted: the tool allow-list, no ambient MCP server, no user settings,
// plugins, hooks or skills. It is daemon.RestrictToolsEnvVar (pinned by mcpstart_parity_test.go).
const restrictToolsEnvVar = "BLERG_RUNNER_RESTRICT_TOOLS"

// maxGatewayServers and gatewayServerName mirror what the receiver (daemon.ValidateMCPGateway)
// accepts, so a config the server sends is one the pod or daemon will take.
const maxGatewayServers = 20

var gatewayServerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// validateGatewayConfig checks a grant config before it is delivered: the same rules as
// daemon.ValidateMCPGateway. The error never contains a token.
func validateGatewayConfig(cfg *protocol.MCPGatewayConfig) error {
	if cfg == nil {
		return errors.New("no MCP gateway configuration")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("the MCP gateway base URL must be a plain http(s) URL")
	}
	if len(cfg.Servers) == 0 || len(cfg.Servers) > maxGatewayServers {
		return fmt.Errorf("an MCP gateway grant names 1 to %d connections", maxGatewayServers)
	}
	seen := map[string]bool{}
	for _, s := range cfg.Servers {
		if !gatewayServerName.MatchString(s.Name) || seen[s.Name] {
			return errors.New("an MCP gateway connection name is not valid")
		}
		seen[s.Name] = true
		if s.Token == "" || len(s.Token) > 512 || strings.ContainsFunc(s.Token, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return fmt.Errorf("the MCP gateway token for %q is not valid", s.Name)
		}
	}
	return nil
}

// Limits on what one start may select.
const (
	maxMCPSelections   = 20  // an account has at most 20 connections
	maxMCPToolsPerConn = 500 // the gateway lists at most 500 tools
	maxMCPToolNameLen  = 200
)

// MCPToolSelection is one tool's mode and the hash the client saw for it.
type MCPToolSelection struct {
	Mode string `json:"mode"` // "allow" | "propose"
	Hash string `json:"hash"`
}

// MCPSelection is one entry of a start request's `mcp` list. Tools nil (omitted) takes the
// connection's default_tools; a present-but-empty object selects no tools and is refused.
type MCPSelection struct {
	Connection string                      `json:"connection"`
	Tools      map[string]MCPToolSelection `json:"tools,omitempty"`
}

// ResolvedGrant is a validated set of MCP connections for one session: whose they are, the
// proof the gateway will present to core, and each connection with its pinned tools. It is
// only ever built in process (resolveGrant, or the scheduler) and is never decoded from a body.
type ResolvedGrant struct {
	AccountID   string
	Proof       mcpgw.Proof
	Connections []mcpgw.GrantSpec
}

// valid reports whether g is a usable grant: an account, exactly one proof for that account,
// and at least one connection.
func (g *ResolvedGrant) valid() bool {
	return g != nil && g.AccountID != "" && g.Proof.AccountID == g.AccountID && g.Proof.Valid() && len(g.Connections) > 0
}

// MCPConnection is one of an account's connections as core lists it (no secret).
type MCPConnection struct {
	ID           string
	Name         string
	URL          string
	AuthKind     string
	Status       string
	DefaultTools map[string]mcpgw.ToolGrant
}

// ErrMCPProofInvalid means core does not consider the proof live (the login ended, the
// token was revoked): its list answers one uniform 404 for that.
var ErrMCPProofInvalid = errors.New("the sign-in behind this request is no longer valid")

// MCPConnectionLister lists an account's connections at core.
type MCPConnectionLister interface {
	ListConnections(ctx context.Context, proof mcpgw.Proof) ([]MCPConnection, error)
}

// HTTPMCPCoreClient lists connections through core's POST /internal/mcp/connections/list
// (spec 4.5), guarded by the internal key.
type HTTPMCPCoreClient struct {
	BaseURL     string
	InternalKey string
	HTTP        *http.Client // nil: a client with a 10 second timeout
}

// httpClient is the configured client, or one that never follows a redirect: every request
// carries the internal key, which must not be re-sent to wherever a redirect points.
func (c *HTTPMCPCoreClient) httpClient() *http.Client {
	cl := &http.Client{Timeout: 10 * time.Second}
	if c.HTTP != nil {
		cp := *c.HTTP
		cl = &cp
	}
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return cl
}

// ErrMCPCoreOutdated means core answered 404 from its router, not from a handler: it has no MCP
// connection routes (an older blerg-core). Core's own uniform "dead proof" 404 says "not found".
var ErrMCPCoreOutdated = errors.New("blerg-core does not offer MCP connections: update it")

// coreNotFound tells a dead proof (core's handler answered its uniform 404) from a missing route
// (the router answered), the two of which used to be reported as "sign in again".
func coreNotFound(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if strings.TrimSpace(string(raw)) == "not found" {
		return ErrMCPProofInvalid
	}
	return ErrMCPCoreOutdated
}

type coreListRequest struct {
	AccountID string `json:"account_id"`
	TokenID   string `json:"token_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type coreListResponse struct {
	Connections []struct {
		ID           string          `json:"id"`
		Name         string          `json:"name"`
		URL          string          `json:"url"`
		AuthKind     string          `json:"auth_kind"`
		Status       string          `json:"status"`
		DefaultTools json.RawMessage `json:"default_tools"`
	} `json:"connections"`
}

// ListConnections implements MCPConnectionLister.
func (c *HTTPMCPCoreClient) ListConnections(ctx context.Context, proof mcpgw.Proof) ([]MCPConnection, error) {
	if !proof.Valid() || proof.AccountID == "" {
		return nil, errors.New("no liveness proof for the connection list")
	}
	if c.BaseURL == "" || c.InternalKey == "" {
		return nil, errors.New("core is not configured")
	}
	raw, err := json.Marshal(coreListRequest{AccountID: proof.AccountID, TokenID: proof.TokenID, SessionID: proof.SessionID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, //nolint:gosec // BaseURL is the operator-configured blerg-core address (BLERG_CORE_URL), never caller input
		strings.TrimRight(c.BaseURL, "/")+"/internal/mcp/connections/list", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", c.InternalKey)
	client := c.httpClient()
	resp, err := client.Do(req) //nolint:gosec // same request, to the operator-configured core
	if err != nil {
		return nil, fmt.Errorf("core unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, coreNotFound(resp)
	default:
		return nil, fmt.Errorf("core returned %d", resp.StatusCode)
	}
	var out coreListResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode core response: %w", err)
	}
	conns := make([]MCPConnection, 0, len(out.Connections))
	for _, c := range out.Connections {
		mc := MCPConnection{ID: c.ID, Name: c.Name, URL: c.URL, AuthKind: c.AuthKind, Status: c.Status}
		if t := bytes.TrimSpace(c.DefaultTools); len(t) > 0 && string(t) != "null" {
			if err := json.Unmarshal(t, &mc.DefaultTools); err != nil {
				return nil, fmt.Errorf("decode default_tools of %q: %w", c.Name, err)
			}
		}
		conns = append(conns, mc)
	}
	return conns, nil
}

// mcpDefaultsInvalid is core's 400 for a default_tools update: its message names the offending
// field and holds no secret, so it is safe to show the person.
type mcpDefaultsInvalid struct{ Msg string }

func (e *mcpDefaultsInvalid) Error() string { return e.Msg }

// mcpDefaultsSetter saves a connection's default tool selection at core. The core client
// implements it; a lister that does not makes the route answer 503.
type mcpDefaultsSetter interface {
	SetDefaultTools(ctx context.Context, proof mcpgw.Proof, connectionID string, tools map[string]mcpgw.ToolGrant) error
}

type coreDefaultsRequest struct {
	AccountID    string                     `json:"account_id"`
	SessionID    string                     `json:"session_id"`
	ConnectionID string                     `json:"connection_id"`
	DefaultTools map[string]mcpgw.ToolGrant `json:"default_tools"`
}

// SetDefaultTools implements mcpDefaultsSetter through core's POST
// /internal/mcp/connections/defaults. Only a login session proof is accepted (core refuses a
// token id), so an agent or cron token can never rewrite a person's defaults. A 404 is a proof
// that is no longer live; a 400 carries core's validation message.
func (c *HTTPMCPCoreClient) SetDefaultTools(ctx context.Context, proof mcpgw.Proof, connectionID string, tools map[string]mcpgw.ToolGrant) error {
	if proof.AccountID == "" || proof.SessionID == "" || proof.TokenID != "" {
		return errors.New("saving default tools needs a login session proof")
	}
	if c.BaseURL == "" || c.InternalKey == "" {
		return errors.New("core is not configured")
	}
	if tools == nil {
		tools = map[string]mcpgw.ToolGrant{}
	}
	raw, err := json.Marshal(coreDefaultsRequest{AccountID: proof.AccountID, SessionID: proof.SessionID, ConnectionID: connectionID, DefaultTools: tools})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, //nolint:gosec // BaseURL is the operator-configured blerg-core address (BLERG_CORE_URL), never caller input
		strings.TrimRight(c.BaseURL, "/")+"/internal/mcp/connections/defaults", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", c.InternalKey)
	client := c.httpClient()
	resp, err := client.Do(req) //nolint:gosec // same request, to the operator-configured core
	if err != nil {
		return fmt.Errorf("core unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return coreNotFound(resp)
	case http.StatusBadRequest:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &mcpDefaultsInvalid{Msg: strings.TrimSpace(string(msg))}
	default:
		return fmt.Errorf("core returned %d", resp.StatusCode)
	}
}

// liveToolHasher is the gateway's start-time tool check (mcpgw.Gateway.LiveToolHashes).
type liveToolHasher interface {
	LiveToolHashes(ctx context.Context, proof mcpgw.Proof, connectionID, urlSnapshot string) (map[string]string, error)
}

// mcpStartConfig is everything a session start needs to attach a grant.
type mcpStartConfig struct {
	Hasher     liveToolHasher      // nil: the gateway is not running
	GatewayURL string              // the address sessions dial (BLERG_RUNNER_MCP_GW_URL)
	Core       MCPConnectionLister // nil: core is not configured
	Board      boardAccess         // nil: the built-in board connection is not available (cronboard.go)
}

// mcpStartHolder is the Hub's (concurrency-safe) reference to the start configuration, so the
// paths that hold only a Hub (a cluster resume) see the same configuration as the API.
type mcpStartHolder struct {
	mu  sync.RWMutex
	cfg *mcpStartConfig
}

func (h *mcpStartHolder) set(c *mcpStartConfig) {
	h.mu.Lock()
	h.cfg = c
	h.mu.Unlock()
	var b boardAccess
	if c != nil {
		b = c.Board
	}
	setBoardAccess(b) // session ends revoke board tokens through it (revokeSessionGrants)
}

func (h *mcpStartHolder) get() *mcpStartConfig {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg
}

// SetMCPGateway wires session starts to the gateway: the running gateway (nil when it is
// disabled), the address sessions dial (BLERG_RUNNER_MCP_GW_URL), and core's connection list.
// Without all three a start that selects MCP connections is refused with a specific message.
func (a *API) SetMCPGateway(handle *MCPGatewayHandle, gatewayURL, coreURL, coreInternalKey string) {
	cfg := &mcpStartConfig{GatewayURL: strings.TrimRight(strings.TrimSpace(gatewayURL), "/")}
	if handle != nil && handle.Gateway != nil {
		cfg.Hasher = handle.Gateway
		if handle.Gateway.BoardConfigured() {
			cfg.Board = handle.Gateway
		}
	}
	if coreURL != "" && coreInternalKey != "" {
		cfg.Core = &HTTPMCPCoreClient{BaseURL: coreURL, InternalKey: coreInternalKey}
	}
	if cfg.GatewayURL != "" {
		// The daemon and the pod validate the same shape; catch it once, here.
		probe := &protocol.MCPGatewayConfig{BaseURL: cfg.GatewayURL, Servers: []protocol.MCPGatewayServer{{Name: "x", Token: "x"}}}
		if err := validateGatewayConfig(probe); err != nil {
			log.Printf("%s is not usable: %v", envMCPGatewayURL, err)
			cfg.GatewayURL = ""
		}
	}
	a.hub.mcpStart.set(cfg)
}

// envMCPGatewayURL names the variable holding the address sessions dial to reach the gateway.
const envMCPGatewayURL = "BLERG_RUNNER_MCP_GW_URL"

// MCPGatewayURLFromEnv reads BLERG_RUNNER_MCP_GW_URL (for the server's main).
func MCPGatewayURLFromEnv() string { return os.Getenv(envMCPGatewayURL) }

// setMCPStart replaces the start configuration (tests use it to fake the seams).
func (a *API) setMCPStart(cfg *mcpStartConfig) { a.hub.mcpStart.set(cfg) }

// CanMCPGateway reports whether the daemon said, in its hello, that it honours
// SpawnSession.MCPGateway. An older daemon would ignore the field and run the session with
// no MCP at all, so it is never sent a grant.
func (dc *DaemonConn) CanMCPGateway() bool {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.mcpGateway
}

// SetMCPGateway records the hello's mcp_gateway capability.
func (dc *DaemonConn) SetMCPGateway(v bool) {
	dc.liveMu.Lock()
	dc.mcpGateway = v
	dc.liveMu.Unlock()
}

// CanRestrictTools reports whether the daemon said, in its hello, that it honours
// SpawnSession.RestrictTools. An older daemon would ignore the field and run an unattended
// session with every built-in tool and the developer's whole Claude configuration, so it is
// never sent one, and a cron treats it as no capacity.
func (dc *DaemonConn) CanRestrictTools() bool {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.restrictTools
}

// SetRestrictTools records the hello's restrict_tools capability.
func (dc *DaemonConn) SetRestrictTools(v bool) {
	dc.liveMu.Lock()
	dc.restrictTools = v
	dc.liveMu.Unlock()
}

// grantRequester is who is asking for a grant: the verified principal of a public route.
type grantRequester struct {
	Kind    string
	Account string
	Sid     string
}

func requesterOf(p identity.Principal) grantRequester {
	return grantRequester{Kind: p.Kind, Account: p.Sub, Sid: p.Sid}
}

// grantTarget is where the session would run.
type grantTarget struct {
	Runtime string      // runnerRuntimeCluster, runnerRuntimeDocker or daemonRuntimeName (the bare host)
	Engine  string      // "" means claude
	Kind    string      // "agent" is the only kind that may carry a grant
	Daemon  *DaemonConn // the daemon a Docker session goes to; nil for the cluster
}

// Messages of the refusals a start with MCP connections can get.
const (
	msgMCPHumanOnly      = "MCP connections can only be attached by a signed-in person, not by an agent token or the runner key"
	msgMCPSignIn         = "your sign-in is no longer valid for MCP connections: sign in again and retry"
	msgMCPEngine         = "MCP connections work only with the claude engine"
	msgMCPKind           = "MCP connections need an agent session (kind \"agent\")"
	msgMCPRuntime        = "MCP connections run only in a cluster pod or the Docker sandbox, never directly on the host"
	msgMCPDaemonOld      = "this daemon does not report mcp_gateway: update it to start a session with MCP connections"
	msgRestrictRuntime   = "an unattended session runs only in a cluster pod or the Docker sandbox, never directly on the host"
	msgRestrictDaemonOld = "this daemon does not report restrict_tools: update it to run an unattended or MCP-connected session"
	msgMCPNoGateway      = "the MCP gateway is not configured on this server (set BLERG_RUNNER_MCP_GW_ADDR)"
	msgMCPNoGatewayURL   = "the MCP gateway has no address for sessions to dial (set " + envMCPGatewayURL + ")"
	msgMCPNoCore         = "MCP connections need blerg-core (BLERG_CORE_URL and the internal key)"
	msgMCPNoDB           = "MCP connections need the runner database"
	msgMCPNotFound       = "connection not found"
	msgMCPBodyForbidden  = "mcp is not accepted on this route: MCP connections can only be attached from the launch sheet by a signed-in person"
)

// checkGrantRequester refuses everyone but a signed-in person: Kind "human" with an account
// and a live login session id (the proof the gateway will present to core). An agent
// principal, the runner key and a token from before core stamped `sid` cannot attach.
func checkGrantRequester(who grantRequester) *APIError {
	if who.Kind != "human" {
		return apiErrorf(http.StatusForbidden, "%s", msgMCPHumanOnly)
	}
	if who.Account == "" || who.Sid == "" {
		return apiErrorf(http.StatusUnauthorized, "%s", msgMCPSignIn)
	}
	return nil
}

// checkGrantTarget refuses a start whose placement cannot carry a grant. It is the one
// place the engine, kind, runtime, daemon capability and gateway configuration are judged,
// for a selection from a person and for a grant the scheduler built alike.
func (a *API) checkGrantTarget(t grantTarget) *APIError {
	if a.dbPool == nil {
		return apiErrorf(http.StatusServiceUnavailable, "%s", msgMCPNoDB)
	}
	if t.Engine != "" && t.Engine != "claude" {
		return apiErrorf(http.StatusUnprocessableEntity, "%s", msgMCPEngine)
	}
	if t.Kind != "agent" {
		return apiErrorf(http.StatusUnprocessableEntity, "%s", msgMCPKind)
	}
	switch t.Runtime {
	case runnerRuntimeCluster:
	case runnerRuntimeDocker:
		if t.Daemon == nil || !t.Daemon.CanMCPGateway() {
			return apiErrorf(http.StatusUnprocessableEntity, "%s", msgMCPDaemonOld)
		}
		if !t.Daemon.CanRestrictTools() {
			return apiErrorf(http.StatusUnprocessableEntity, "%s", msgRestrictDaemonOld)
		}
	default:
		return apiErrorf(http.StatusUnprocessableEntity, "%s", msgMCPRuntime)
	}
	cfg := a.hub.mcpStart.get()
	switch {
	case cfg == nil || cfg.Hasher == nil:
		return apiErrorf(http.StatusServiceUnavailable, "%s", msgMCPNoGateway)
	case cfg.GatewayURL == "":
		return apiErrorf(http.StatusServiceUnavailable, "%s", msgMCPNoGatewayURL)
	}
	return nil
}

// checkRestrictTarget refuses a start that must be restricted (a cron session) but has no
// placement that can restrict it: never the bare host, and on Docker only a daemon that says it
// honours RestrictTools. A cluster pod is the same build as the server and always can.
func checkRestrictTarget(runtime string, dc *DaemonConn) *APIError {
	switch runtime {
	case runnerRuntimeCluster:
		return nil
	case runnerRuntimeDocker:
		if dc == nil || !dc.CanRestrictTools() {
			return apiErrorf(http.StatusUnprocessableEntity, "%s", msgRestrictDaemonOld)
		}
		return nil
	}
	return apiErrorf(http.StatusUnprocessableEntity, "%s", msgRestrictRuntime)
}

// checkGrant is checkGrantTarget for a grant that already exists (StartSession's, built in
// process): the grant must be well formed and the target must be able to carry it. Each
// spawn path calls it BEFORE it records anything.
func (a *API) checkGrant(g *ResolvedGrant, t grantTarget) *APIError {
	if !g.valid() {
		return apiErrorf(http.StatusInternalServerError, "invalid MCP grant")
	}
	return a.checkGrantTarget(t)
}

// resolveGrant turns a person's `mcp` selection into a ResolvedGrant, or refuses. Absent or
// empty selection returns nil, nil. Nothing is recorded or created here.
func (a *API) resolveGrant(ctx context.Context, who grantRequester, sel []MCPSelection, t grantTarget) (*ResolvedGrant, *APIError) {
	if len(sel) == 0 {
		return nil, nil
	}
	if apiErr := checkGrantRequester(who); apiErr != nil {
		return nil, apiErr
	}
	return a.resolveGrantProof(ctx, mcpgw.Proof{AccountID: who.Account, SessionID: who.Sid}, sel, t)
}

// resolveGrantProof is resolveGrant once the requester has been judged and the proof chosen: a
// signed-in person's login session (resolveGrant) or a cron's token (the scheduler, cronstart.go).
// The selection must be non-empty.
func (a *API) resolveGrantProof(ctx context.Context, proof mcpgw.Proof, sel []MCPSelection, t grantTarget) (*ResolvedGrant, *APIError) {
	if apiErr := a.checkGrantTarget(t); apiErr != nil {
		return nil, apiErr
	}
	cfg := a.hub.mcpStart.get()
	if cfg.Core == nil {
		return nil, apiErrorf(http.StatusServiceUnavailable, "%s", msgMCPNoCore)
	}
	if len(sel) > maxMCPSelections {
		return nil, apiErrorf(http.StatusBadRequest, "at most %d MCP connections per session", maxMCPSelections)
	}
	conns, err := cfg.Core.ListConnections(ctx, proof)
	switch {
	case errors.Is(err, ErrMCPProofInvalid):
		return nil, apiErrorCause(http.StatusUnauthorized, ErrMCPProofInvalid, "%s", msgMCPSignIn)
	case errors.Is(err, ErrMCPCoreOutdated):
		return nil, apiErrorf(http.StatusServiceUnavailable, "%s", ErrMCPCoreOutdated.Error())
	case err != nil:
		log.Printf("mcp start: list connections: %v", err)
		return nil, apiErrorf(http.StatusServiceUnavailable, "could not read your MCP connections from blerg-core")
	}
	byID := make(map[string]MCPConnection, len(conns))
	for _, c := range conns {
		byID[c.ID] = c
	}
	grant := &ResolvedGrant{AccountID: proof.AccountID, Proof: proof}
	seen := map[string]bool{}
	for _, s := range sel {
		c, ok := byID[s.Connection]
		if s.Connection == "" || !ok {
			// Someone else's connection and one that does not exist look the same.
			return nil, apiErrorf(http.StatusNotFound, "%s", msgMCPNotFound)
		}
		if seen[c.ID] {
			return nil, apiErrorf(http.StatusBadRequest, "connection %q is selected twice", c.Name)
		}
		seen[c.ID] = true
		if c.Status != "ok" {
			return nil, apiErrorf(http.StatusUnprocessableEntity, "connection %q cannot be used now (status %q): fix it in settings first", c.Name, c.Status)
		}
		if !mcpgw.ValidConnectionName(c.Name) {
			return nil, apiErrorf(http.StatusUnprocessableEntity, "connection %q has a name the gateway cannot serve", c.Name)
		}
		tools, apiErr := selectedTools(c, s)
		if apiErr != nil {
			return nil, apiErr
		}
		live, err := cfg.Hasher.LiveToolHashes(ctx, proof, c.ID, c.URL)
		switch {
		case errors.Is(err, mcpgw.ErrConnectionGone):
			return nil, apiErrorf(http.StatusNotFound, "%s", msgMCPNotFound)
		case err != nil:
			return nil, apiErrorf(http.StatusBadGateway, "could not list the tools of connection %q: %v", c.Name, err)
		}
		for name, tg := range tools {
			if h := live[name]; h == "" || h != tg.Hash {
				return nil, apiErrorf(http.StatusConflict,
					"tool %q of connection %q changed, or is not offered any more: open the tool picker and confirm it again", name, c.Name)
			}
		}
		grant.Connections = append(grant.Connections, mcpgw.GrantSpec{ConnectionID: c.ID, Name: c.Name, URL: c.URL, Tools: tools})
	}
	return grant, nil
}

// mcpProposeAllowed gates the `propose` tool mode. Proposals (spec 9) exist: the gateway freezes a
// propose call and a person approves it on the proposals routes, so the mode is accepted. The
// variable remains as the switch a build without proposals would turn off; with it false a
// selection or a saved default naming propose is refused with a 422.
var mcpProposeAllowed = true

const msgMCPProposeUnavailable = `mode "propose" is not available: use "allow" or leave the tool off`

// selectedTools is the modes and hashes one selection asks for: its own tools, or the
// connection's default_tools when it names none. Every entry is allow or propose with a hash.
func selectedTools(c MCPConnection, s MCPSelection) (map[string]mcpgw.ToolGrant, *APIError) {
	tools := map[string]mcpgw.ToolGrant{}
	if s.Tools == nil {
		for name, tg := range c.DefaultTools {
			if tg.Mode == mcpgw.ModePropose && !mcpProposeAllowed {
				return nil, apiErrorf(http.StatusUnprocessableEntity, "connection %q, tool %q: its saved default is %s",
					c.Name, name, msgMCPProposeUnavailable)
			}
			if tg.Mode == mcpgw.ModeAllow || tg.Mode == mcpgw.ModePropose {
				tools[name] = tg
			}
		}
		if len(tools) == 0 {
			return nil, apiErrorf(http.StatusUnprocessableEntity,
				"connection %q has no default tools: choose the tools this session may use", c.Name)
		}
	} else {
		for name, ts := range s.Tools {
			if name == "" || len(name) > maxMCPToolNameLen {
				return nil, apiErrorf(http.StatusBadRequest, "connection %q: a tool name must be 1-%d characters", c.Name, maxMCPToolNameLen)
			}
			if ts.Mode != mcpgw.ModeAllow && ts.Mode != mcpgw.ModePropose {
				return nil, apiErrorf(http.StatusBadRequest, "connection %q, tool %q: mode must be %q or %q", c.Name, name, mcpgw.ModeAllow, mcpgw.ModePropose)
			}
			if ts.Mode == mcpgw.ModePropose && !mcpProposeAllowed {
				return nil, apiErrorf(http.StatusUnprocessableEntity, "connection %q, tool %q: %s", c.Name, name, msgMCPProposeUnavailable)
			}
			if ts.Hash == "" {
				return nil, apiErrorf(http.StatusBadRequest, "connection %q, tool %q: the hash you saw for the tool is required", c.Name, name)
			}
			tools[name] = mcpgw.ToolGrant{Mode: ts.Mode, Hash: ts.Hash}
		}
		if len(tools) == 0 {
			return nil, apiErrorf(http.StatusBadRequest, "connection %q selects no tools", c.Name)
		}
	}
	if len(tools) > maxMCPToolsPerConn {
		return nil, apiErrorf(http.StatusBadRequest, "connection %q selects more than %d tools", c.Name, maxMCPToolsPerConn)
	}
	return tools, nil
}

// mcpKeyInBody reports whether a JSON object body has a top-level "mcp" key, in any letter
// case (encoding/json matches field names case-insensitively, so "MCP" would count too).
func mcpKeyInBody(raw []byte) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	for k := range m {
		if strings.EqualFold(k, "mcp") {
			return true
		}
	}
	return false
}

// RejectMCPArgument refuses a v1 start body (or an MCP tool call's arguments) that names an
// `mcp` key: only the launch sheet's route, for a signed-in person, may attach connections.
// It returns nil when there is none.
func RejectMCPArgument(raw json.RawMessage) *APIError {
	if mcpKeyInBody(raw) {
		return apiErrorf(http.StatusBadRequest, "%s", msgMCPBodyForbidden)
	}
	return nil
}

// attachGrant puts a grant on a session whose row exists: it checks the spawning account is
// recorded as the grant's account, marks the session private (after that), creates the grants
// and returns the gateway config to deliver (the raw tokens live only in it). On error nothing
// is left behind but the row, which the caller removes (abortGrantSession).
func (a *API) attachGrant(ctx context.Context, sessionID string, g *ResolvedGrant) (*protocol.MCPGatewayConfig, *APIError) {
	if !g.valid() {
		return nil, apiErrorf(http.StatusInternalServerError, "invalid MCP grant")
	}
	cfg := a.hub.mcpStart.get()
	if a.dbPool == nil || cfg == nil || cfg.Hasher == nil || cfg.GatewayURL == "" {
		return nil, apiErrorf(http.StatusServiceUnavailable, "%s", msgMCPNoGateway)
	}
	row, err := db.GetSession(ctx, a.dbPool, sessionID)
	if err != nil || row == nil || row.SpawningAccountID == nil || *row.SpawningAccountID != g.AccountID {
		// The privacy rule keys on the spawning account: without it recorded the session
		// would be private to nobody, so no grant is made.
		log.Printf("mcp start %s: spawning account not recorded (%v)", sessionID, err)
		return nil, apiErrorf(http.StatusInternalServerError, "session owner could not be recorded")
	}
	if err := a.MarkPrivate(ctx, sessionID); err != nil {
		log.Printf("mcp start %s: mark private: %v", sessionID, err)
		return nil, apiErrorf(http.StatusInternalServerError, "session could not be made private")
	}
	tokens, err := mcpgw.CreateGrants(ctx, a.dbPool, sessionID, g.AccountID, g.Proof, g.Connections)
	if err != nil {
		log.Printf("mcp start %s: create grants: %v", sessionID, err)
		return nil, apiErrorf(http.StatusInternalServerError, "MCP connections could not be granted")
	}
	out := gatewayConfig(cfg.GatewayURL, g.Connections, tokens)
	if err := validateGatewayConfig(out); err != nil {
		_ = mcpgw.DeleteGrantsForSession(ctx, a.dbPool, sessionID)
		log.Printf("mcp start %s: gateway config: %v", sessionID, err)
		return nil, apiErrorf(http.StatusInternalServerError, "MCP connections could not be granted")
	}
	return out, nil
}

func gatewayConfig(baseURL string, conns []mcpgw.GrantSpec, tokens map[string]string) *protocol.MCPGatewayConfig {
	out := &protocol.MCPGatewayConfig{BaseURL: baseURL}
	for _, c := range conns {
		out.Servers = append(out.Servers, protocol.MCPGatewayServer{Name: c.Name, Token: tokens[c.ConnectionID]})
	}
	return out
}

// abortGrantSession undoes a start that failed after its row (and maybe grants) were made:
// the grants go, and so do the pre-created row and the session's messaging token.
func (a *API) abortGrantSession(ctx context.Context, sessionID string) {
	abortSpawnSessionToken(ctx, a.dbPool, sessionID) // also deletes the grants
}

// revokeSessionGrants deletes a session's MCP grants so its gateway tokens stop working. It
// is called wherever the session's other credentials are revoked at a final end. Best effort,
// logged; a no-op without a database.
func revokeSessionGrants(ctx context.Context, pool *pgxpool.Pool, sessionID, why string) {
	if pool == nil {
		return
	}
	if err := mcpgw.DeleteGrantsForSession(ctx, pool, sessionID); err != nil {
		log.Printf("revoke MCP grants %s (%s): %v", sessionID, why, err)
	}
	// Then the board tokens the gateway exchanged for it (none unless it had a board grant).
	revokeBoardTokens(ctx, sessionID)
}

// grantResumeProblem is why a resume of a session with MCP grants must be refused, or nil.
// The re-issued tokens would carry the requester's login as their proof, so the requester
// must BE the account that started the session and must have a login session to offer; an
// agent token or the runner key never resumes a session that holds a person's connections.
// A session with no grants is not affected.
func grantResumeProblem(ctx context.Context, pool *pgxpool.Pool, sessionID, launcher, requesterAccountID, requesterSessionID string) (grants []db.MCPGrant, problem *APIError) {
	if pool == nil {
		return nil, nil
	}
	grants, err := db.ListMCPGrantsForSession(ctx, pool, sessionID)
	if err != nil {
		log.Printf("resume %s: list MCP grants: %v", sessionID, err)
		return nil, apiErrorf(http.StatusServiceUnavailable, "could not check the session's MCP connections")
	}
	if len(grants) == 0 {
		return nil, nil
	}
	if launcher == "" || requesterAccountID != launcher {
		return nil, apiErrorf(http.StatusForbidden, "only the account that started this session can resume it: it holds MCP connections")
	}
	if requesterSessionID == "" {
		return nil, apiErrorf(http.StatusUnauthorized, "%s", msgMCPSignIn)
	}
	return grants, nil
}

// reissueGrants gives a resumed session new gateway tokens: the same connections, tools and
// call budget, new random tokens, and the requester's login as the proof, replacing the old
// grants in one transaction. It returns the config to deliver to the new pod.
func reissueGrants(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, account, requesterSessionID string, old []db.MCPGrant) (*protocol.MCPGatewayConfig, error) {
	cfg := h.mcpStart.get()
	if cfg == nil || cfg.Hasher == nil || cfg.GatewayURL == "" {
		return nil, errors.New(msgMCPNoGateway)
	}
	specs := make([]mcpgw.GrantSpec, 0, len(old))
	for _, g := range old {
		specs = append(specs, mcpgw.GrantSpec{ConnectionID: g.ConnectionID, Name: g.Name, URL: g.URLSnapshot, Tools: g.Tools,
			CallBudget: g.CallBudget, Builtin: g.Builtin, BuiltinRef: g.BuiltinRef})
	}
	tokens, err := mcpgw.ReissueGrants(ctx, pool, sessionID, account, mcpgw.Proof{AccountID: account, SessionID: requesterSessionID}, specs)
	if err != nil {
		return nil, err
	}
	out := gatewayConfig(cfg.GatewayURL, specs, tokens)
	if err := validateGatewayConfig(out); err != nil {
		return nil, err
	}
	return out, nil
}
