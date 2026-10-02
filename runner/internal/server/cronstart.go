package server

// The cron start path and lifecycle (spec 7.3 to 7.6, 8).
//
// CronService is what the scheduler (internal/cron) is wired to. It is
//
//   - the cron.Starter: it starts one agent session per run, as the cron's own in-process agent
//     principal {Kind agent, Sub token id, OnBehalfOf owner}, through the same StartSession every
//     other start uses;
//   - the cron.Sessions the scheduler consults for overlap and the concurrent limit;
//   - the cron.Config.OnPaused handler, and the helpers the crons API calls when a cron is deleted,
//     paused or renewed (StopCronSessions, RevokeCronToken);
//   - the max-runtime watchdog and the hourly sweeper.
//
// A start goes, in this order:
//
//  1. Replay. An idempotency key for this run that already names a session answers with it (a
//     repeat call, the reaper) without touching anything. StartSession would refuse it if the cron
//     was edited in between (the request hash changed), so this is answered here, read only.
//  2. Liveness. core's /internal/tokens/status must say the cron's token is live. Not live (or
//     unknown to core) pauses the cron with the reason "access revoked" and the run fails. If core
//     cannot be reached the run is HELD, not failed: nothing was written and the next tick asks again.
//  3. Runtime. `auto` is resolved here to the cluster when a JobManager is configured and to the
//     Docker sandbox otherwise; the request always carries an explicit cluster or docker, never an
//     empty value (which would fall back to the bare host) and never the bare host itself.
//  4. Capacity, BEFORE any session row, idempotency key or webhook exists: the cluster's session
//     cap, or on Docker a connected daemon with the sandbox image (the cron's pinned daemon, when
//     it has one, or none). No capacity is a typed cron.ErrCapacity, so a held run retried every
//     tick creates nothing.
//  5. The grant. The cron's `mcp` is resolved with the cron token as proof (resolveGrantProof, the
//     machinery the launch sheet uses), after checking that every connection still exists in core
//     and can be used: a missing one, or one that needs signing in again, is a credential failure
//     with the reason visible on the run.
//     A cron with a board_id gets one more grant, the built-in `board` connection (cronboard.go),
//     after core has been shown to issue a board token for it; a cron without one gets none. The
//     cron's `mcp` may be empty: the board alone is then the whole grant.
//  6. StartSession, with the in-process only fields: the daemon pin, the cron id (the session is
//     marked and made private the moment its account is recorded) and NoOperatorFallback.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/cron"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// Reasons the cron machinery pauses a cron with.
const (
	// CronPausedAccessRevoked is the paused_reason when core no longer considers the cron's token
	// live (revoked in Settings, "log out everywhere", an expired or disabled account).
	CronPausedAccessRevoked = "access revoked"
)

// Timing defaults for the watchdog and the sweeper.
const (
	defaultCronWatchdogEvery = 30 * time.Second
	defaultCronSweepEvery    = time.Hour
	cronSweepFirstDelay      = time.Minute
	cronCallTimeout          = 15 * time.Second
	cronMaxTitleName         = 100
	// maxRevocationAttempts is how many times a cron token's revocation is retried before it is
	// dropped with a warning (the backoff between attempts reaches an hour).
	maxRevocationAttempts = 20
)

var (
	errDaemonUnavailable = errors.New("no suitable daemon is connected")
	errAccessRevoked     = errors.New("access revoked: the cron's token is no longer live, so the cron was paused")
)

// ---- typed errors ------------------------------------------------------------------------

// cronError is an error that reads as its message and matches a cron error kind
// (cron.ErrCapacity, cron.ErrCredential) with errors.Is, so the scheduler's typed handling applies
// without the kind's own text being prefixed to every message.
type cronError struct {
	kind error
	msg  string
}

func (e *cronError) Error() string        { return e.msg }
func (e *cronError) Is(target error) bool { return target == e.kind }

func capacityErrorf(format string, args ...any) error {
	return &cronError{kind: cron.ErrCapacity, msg: fmt.Sprintf(format, args...)}
}

func credentialErrorf(format string, args ...any) error {
	return &cronError{kind: cron.ErrCredential, msg: fmt.Sprintf(format, args...)}
}

// startFailureCause names the typed causes CreateSessionJob's failures carry, for APIError.Cause.
func startFailureCause(err error) error {
	switch {
	case errors.Is(err, ErrNoPersonalCredential):
		return ErrNoPersonalCredential
	case errors.Is(err, errClusterCap):
		return errClusterCap
	}
	return nil
}

// ---- what a cron's start request must look like --------------------------------------------

// cronStartProblem is StartSession's own check of a cron start, the one place the containment
// rules of spec 8 are enforced for whoever builds the request: the Docker sandbox or a cluster
// pod, never the bare host or an unset runtime; Claude; no repository; one turn; and never the
// operator credential.
func cronStartProblem(req runnerStartRequest) string {
	if req.CronID == "" {
		return ""
	}
	switch {
	case req.Runtime != runnerRuntimeCluster && req.Runtime != runnerRuntimeDocker:
		return "a cron session runs in a cluster pod or the Docker sandbox, never on the host"
	case req.Engine != "claude":
		return "a cron session uses the claude engine"
	case !req.NoRepo || !req.AutoStop:
		return "a cron session has no repository and runs a single turn"
	case !req.NoOperatorFallback:
		return "a cron session must not fall back to the operator credential"
	}
	return ""
}

// markCronSession records which cron started the session and makes it private. It is called by
// every spawn path right after the spawning account is recorded, before anything else can name the
// session, and does nothing for a start that is not a cron's.
func (a *API) markCronSession(ctx context.Context, req runnerStartRequest, sessionID string) *APIError {
	if req.CronID == "" {
		return nil
	}
	if a.dbPool == nil {
		return apiErrorf(http.StatusServiceUnavailable, "crons need the runner database")
	}
	if err := db.SetSessionCronID(ctx, a.dbPool, sessionID, req.CronID); err != nil {
		log.Printf("cron start %s: set cron id: %v", sessionID, err)
		return apiErrorf(http.StatusInternalServerError, "session could not be marked as a cron run")
	}
	if err := a.MarkPrivate(ctx, sessionID); err != nil {
		log.Printf("cron start %s: mark private: %v", sessionID, err)
		return apiErrorf(http.StatusInternalServerError, "session could not be made private")
	}
	return nil
}

// resolveCronRuntime turns a cron's runtime into the explicit one its session starts with.
func resolveCronRuntime(runtime string, clusterConfigured bool) (string, error) {
	switch runtime {
	case "", "auto":
		if clusterConfigured {
			return runnerRuntimeCluster, nil
		}
		return runnerRuntimeDocker, nil
	case runnerRuntimeCluster:
		if !clusterConfigured {
			return "", errors.New("the cluster runtime is not configured on this install")
		}
		return runnerRuntimeCluster, nil
	case runnerRuntimeDocker:
		return runnerRuntimeDocker, nil
	}
	return "", fmt.Errorf("a cron cannot use the %q runtime", runtime)
}

// cronPrincipal is the in-process principal a cron acts as. It is never built from a request.
func cronPrincipal(c *db.Cron) runnerPrincipal {
	return runnerPrincipal{
		Kind: agentPrincipalKind, Sub: c.TokenID, OnBehalfOf: c.OwnerAccountID,
		Aud: coreAuthAudience, Caps: []string{coreAuthRunnerCap},
	}
}

// cronSessionTitle is the cron's name and the slot it runs for, in the cron's own time zone.
func cronSessionTitle(c *db.Cron, run *db.CronRun) string {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		loc = time.UTC
	}
	name := c.Name
	if r := []rune(name); len(r) > cronMaxTitleName {
		name = string(r[:cronMaxTitleName])
	}
	return name + " " + run.ScheduledFor.In(loc).Format("2006-01-02 15:04 MST")
}

// parseCronMCP reads a cron's stored `mcp` (the same objects a launch request carries).
func parseCronMCP(raw json.RawMessage) ([]MCPSelection, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var sel []MCPSelection
	if err := json.Unmarshal(raw, &sel); err != nil {
		return nil, fmt.Errorf("the cron's mcp setting is not valid: %w", err)
	}
	return sel, nil
}

// ---- core: token status and revoke ---------------------------------------------------------

// CronCore is the part of blerg-core's internal API the cron machinery uses (spec 4.5, 7.4).
type CronCore interface {
	// TokenLive reports whether tokenID is a live token of accountID. A token core does not know,
	// or that is another account's, is not live (nil error: core says 200 {"live": false}). An
	// error, any other status included, means core could not say.
	TokenLive(ctx context.Context, accountID, tokenID string) (bool, error)
	// RevokeToken revokes the cron token with the internal key alone (no session proof): core only
	// lets that revoke a cron-kind token owned by accountID. Revoking a token that is gone is not
	// an error.
	RevokeToken(ctx context.Context, accountID, tokenID string) error
}

// HTTPCronCore is CronCore over POST /internal/tokens/status and /internal/tokens/revoke.
type HTTPCronCore struct {
	BaseURL     string
	InternalKey string
	HTTP        *http.Client // nil: a client with a 10 second timeout
}

type cronTokenRequest struct {
	AccountID string `json:"account_id"`
	TokenID   string `json:"token_id"`
}

func (c *HTTPCronCore) post(ctx context.Context, path string, body cronTokenRequest) (*http.Response, error) {
	if c.BaseURL == "" || c.InternalKey == "" {
		return nil, errors.New("core is not configured")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", c.InternalKey)
	resp, err := coreHTTPClient(c.HTTP).Do(req)
	if err != nil {
		return nil, fmt.Errorf("core unreachable: %w", err)
	}
	return resp, nil
}

// coreHTTPClient is the client for a call to blerg-core: the configured one (or a 10 second default)
// that never follows a redirect, because every such request carries the internal key, which must not
// be re-sent to wherever a redirect points. A redirect is handed back as the response, and is an
// error to every caller.
func coreHTTPClient(c *http.Client) *http.Client {
	cl := &http.Client{Timeout: 10 * time.Second}
	if c != nil {
		cp := *c
		cl = &cp
	}
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return cl
}

// isCoreNotFound reports whether a 404 is core's own uniform answer (a handler's "not found"), not a
// router's or proxy's 404 for a route core lacks. Only the former means the object is gone; the
// latter is an error, never an acknowledgement.
func isCoreNotFound(resp *http.Response) bool {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return strings.TrimSpace(string(raw)) == "not found"
}

// TokenLive implements CronCore.
func (c *HTTPCronCore) TokenLive(ctx context.Context, accountID, tokenID string) (bool, error) {
	resp, err := c.post(ctx, "/internal/tokens/status", cronTokenRequest{AccountID: accountID, TokenID: tokenID})
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Live bool `json:"live"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
			return false, fmt.Errorf("decode core response: %w", err)
		}
		return out.Live, nil
	default:
		// Core answers 200 {"live": false} for a token it does not know or that is not the
		// account's. Anything else, a 404 included (a proxy, a rolled-back core without the
		// route), means core could not say: an error, which holds the run, never a verdict.
		return false, fmt.Errorf("core returned %d", resp.StatusCode)
	}
}

// RevokeToken implements CronCore.
func (c *HTTPCronCore) RevokeToken(ctx context.Context, accountID, tokenID string) error {
	resp, err := c.post(ctx, "/internal/tokens/revoke", cronTokenRequest{AccountID: accountID, TokenID: tokenID})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		// Core's own "not found": already gone, or never ours to revoke. A router's or proxy's 404
		// (a core without the route) acknowledges nothing: the token may still be live.
		if !isCoreNotFound(resp) {
			return errors.New("core has no token revoke route: is it up to date?")
		}
		return nil
	default:
		return fmt.Errorf("core returned %d", resp.StatusCode)
	}
}

// ---- the service --------------------------------------------------------------------------

// CronServiceConfig configures a CronService. Only Core is required.
type CronServiceConfig struct {
	Core CronCore
	// Connections lists an account's connections at core for the fire-time and sweeper checks.
	// Default: the lister the MCP start path was given (SetMCPGateway).
	Connections MCPConnectionLister
	// Cards posts the failure card of a run that could not start (cronfailurecard.go). Nil: none.
	Cards CronBoardCards
	// Leader, when set, gates the watchdog and the sweeper: they run only while it returns true
	// (main passes the scheduler's HoldsLock, so only the replica that schedules does either).
	Leader func() bool
	Now    func() time.Time
	Logf   func(format string, args ...any)

	WatchdogEvery time.Duration // default 30 seconds
	SweepEvery    time.Duration // default 1 hour
}

// CronService is the cron start path and lifecycle. Build it with NewCronService.
type CronService struct {
	api  *API
	cfg  CronServiceConfig
	core CronCore

	cards    CronBoardCards
	cardWG   sync.WaitGroup
	cardGate cardLimiter // per-cron and global bounds on failure card posts (cronfailurecard.go)

	mu      sync.Mutex
	flagged map[string][]string // cron id -> reasons its MCP connections cannot be used (sweeper)
}

// Compile time checks: the scheduler's two dependencies.
var (
	_ cron.Starter    = (*CronService)(nil)
	_ cron.Sessions   = (*CronService)(nil)
	_ cron.TxSessions = (*CronService)(nil)
)

// NewCronService builds the service and makes it reachable as api.Cron().
func NewCronService(api *API, cfg CronServiceConfig) *CronService {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.WatchdogEvery <= 0 {
		cfg.WatchdogEvery = defaultCronWatchdogEvery
	}
	if cfg.SweepEvery <= 0 {
		cfg.SweepEvery = defaultCronSweepEvery
	}
	s := &CronService{api: api, cfg: cfg, core: cfg.Core, cards: cfg.Cards, flagged: map[string][]string{}}
	api.cron = s
	return s
}

// Cron returns the cron service NewCronService registered, or nil when crons are not enabled
// (no database or no core).
func (a *API) Cron() *CronService { return a.cron }

func (s *CronService) connections() MCPConnectionLister {
	if s.cfg.Connections != nil {
		return s.cfg.Connections
	}
	if cfg := s.api.hub.mcpStart.get(); cfg != nil && cfg.Core != nil {
		return cfg.Core
	}
	return nil
}

// ---- cron.Sessions ------------------------------------------------------------------------

// SessionActive implements cron.Sessions: the session exists and has not ended. A read that
// fails counts as active, so a database blip can never start an overlapping run.
func (s *CronService) SessionActive(ctx context.Context, sessionID string) bool {
	if s.api.dbPool == nil {
		return false
	}
	row, err := db.GetSession(ctx, s.api.dbPool, sessionID)
	if err != nil {
		s.cfg.Logf("cron: session %s lookup: %v", sessionID, err)
		return true
	}
	return row != nil && !terminalSessionStatus(row.Status)
}

// SessionActiveOn implements cron.TxSessions: SessionActive read through q, the transaction a
// run-now's overlap check runs on. A read that fails counts as active, as above.
func (s *CronService) SessionActiveOn(ctx context.Context, q db.Querier, sessionID string) bool {
	active, err := db.SessionIsActive(ctx, q, sessionID)
	if err != nil {
		s.cfg.Logf("cron: session %s lookup: %v", sessionID, err)
		return true
	}
	return active
}

// ConcurrentActive implements cron.Sessions: the account's cron-started sessions still running.
// When the count cannot be read it reports a very large number, so the run is held.
func (s *CronService) ConcurrentActive(ctx context.Context, account string) int {
	if s.api.dbPool == nil {
		return 0
	}
	n, err := db.CountActiveCronSessions(ctx, s.api.dbPool, account)
	if err != nil {
		s.cfg.Logf("cron: count active sessions of %s: %v", account, err)
		return 1 << 30
	}
	return n
}

// ---- cron.Starter -------------------------------------------------------------------------

// Start implements cron.Starter (the order is described at the top of this file). A run that fails
// to start on a cron with a board also leaves the failure card there (cronfailurecard.go), in the
// background and best effort.
func (s *CronService) Start(ctx context.Context, c *db.Cron, run *db.CronRun) (string, error) {
	sid, err := s.start(ctx, c, run)
	if err != nil {
		s.postFailureCard(ctx, c, run, err)
	}
	return sid, err
}

func (s *CronService) start(ctx context.Context, c *db.Cron, run *db.CronRun) (string, error) {
	a := s.api
	if a.dbPool == nil {
		return "", errors.New("crons need the runner database")
	}
	// The idempotency scope belongs to the cron, not its token (spec 7.3): renewing the token
	// between two attempts at one run must not open a second key space.
	scope, key := cron.StableIdempotencyScope(c), run.IdempotencyKey()

	// 1. Replay: read only, before anything else.
	row, err := db.GetIdempotencyKey(ctx, a.dbPool, scope, key)
	if err != nil {
		return "", fmt.Errorf("idempotency lookup: %w", err)
	}
	if row != nil {
		sess, err := db.GetSession(ctx, a.dbPool, row.SessionID)
		if err != nil {
			return "", fmt.Errorf("session lookup: %w", err)
		}
		if sess != nil {
			return row.SessionID, nil
		}
		return "", capacityErrorf("an earlier start of this run has not finished")
	}

	// The cron as it is NOW: the row the scheduler handed over may be seconds old, and a renewal
	// or a pause since then changes which token is the cron's, or whether it may run at all.
	fresh, err := db.GetCron(ctx, a.dbPool, c.ID)
	switch {
	case errors.Is(err, db.ErrCronNotFound):
		return "", fmt.Errorf("%w: the cron was deleted", cron.ErrNotRunnable)
	case err != nil:
		return "", fmt.Errorf("cron lookup: %w", err)
	case fresh.PausedReason != nil, !fresh.Enabled && !run.Manual:
		return "", fmt.Errorf("%w: it is paused or disabled", cron.ErrNotRunnable)
	}
	c = fresh
	principal := cronPrincipal(c)

	// 2. Liveness of the cron's identity.
	if err := s.checkLive(ctx, c); err != nil {
		return "", err
	}

	// 3 and 4. Explicit runtime, then capacity, before any row or key exists.
	rt, err := resolveCronRuntime(c.Runtime, a.hub.JobManager() != nil)
	if err != nil {
		return "", err
	}
	sel, err := parseCronMCP(c.MCP)
	if err != nil {
		return "", err
	}
	hasBoard := c.BoardID != nil && *c.BoardID != ""
	dc, err := s.preflight(rt, c.DaemonID, len(sel) > 0 || hasBoard) //nolint:contextcheck // the cluster count is bounded by the JobManager client timeout, like every cluster call
	if err != nil {
		return "", err
	}

	// 5. The grant.
	var grant *ResolvedGrant
	target := grantTarget{Runtime: rt, Engine: "claude", Kind: "agent", Daemon: dc}
	if len(sel) > 0 {
		if grant, err = s.buildGrant(ctx, c, sel, target); err != nil {
			return "", err
		}
	}
	if hasBoard {
		if grant, err = s.addBoardGrant(ctx, c, grant, target); err != nil {
			return "", err
		}
	}

	// 6. Start.
	req := RunnerStartRequest{
		Title: cronSessionTitle(c, run), Prompt: c.Prompt, Model: derefOrEmpty(c.Model), Effort: derefOrEmpty(c.Effort),
		Engine: "claude", Runtime: rt, AutoStop: true, NoRepo: true,
		Grant: grant, CronID: c.ID, NoOperatorFallback: true, IdemScope: scope,
	}
	if dc != nil {
		req.DaemonID = dc.ID
	}
	resp, apiErr := a.StartSession(ctx, principal, req, key)
	if apiErr != nil {
		return "", &sessionStartError{s.startError(ctx, c, apiErr)}
	}
	return resp.SessionID, nil
}

// checkLive confirms with core that the cron's token is live, pausing the cron when it is not.
func (s *CronService) checkLive(ctx context.Context, c *db.Cron) error {
	live, err := s.core.TokenLive(ctx, c.OwnerAccountID, c.TokenID)
	if err != nil {
		return capacityErrorf("cannot confirm the cron's access with blerg-core, will retry: %v", err)
	}
	if !live {
		return s.pauseRevoked(ctx, c)
	}
	return nil
}

// pauseRevoked pauses the cron with the reason "access revoked" and cleans up after it. It is a
// compare-and-set on the token that was found dead: a cron that was renewed since (and so already
// holds a live one) is left alone.
//
// It returns the error the start ends with: errAccessRevoked, or, when the token was swapped
// under this start (nothing was paused), a capacity error so the run is held and retried with
// the new token rather than failed.
func (s *CronService) pauseRevoked(ctx context.Context, c *db.Cron) error {
	paused, err := db.PauseCronForToken(ctx, s.api.dbPool, c.ID, c.TokenID, CronPausedAccessRevoked)
	if err != nil {
		s.cfg.Logf("cron %s: pause (access revoked): %v", c.ID, err)
		return capacityErrorf("cannot record the revoked access, will retry: %v", err)
	}
	if paused {
		s.OnPaused(ctx, c, CronPausedAccessRevoked)
		return errAccessRevoked
	}
	if cur, err := db.GetCron(ctx, s.api.dbPool, c.ID); err == nil && cur.PausedReason == nil && cur.TokenID != c.TokenID {
		return capacityErrorf("the cron's access token was renewed while its run was starting, will retry")
	}
	return errAccessRevoked
}

// preflight is the capacity check. It writes nothing. For the Docker runtime it returns the daemon
// the session must run on (the request pins it, so the check and the start agree).
func (s *CronService) preflight(rt string, pin *string, needGateway bool) (*DaemonConn, error) {
	if rt == runnerRuntimeCluster {
		jm := s.api.hub.JobManager()
		n, err := jm.ActiveSessionJobs()
		if err != nil {
			return nil, capacityErrorf("the cluster's running sessions cannot be counted, will retry: %v", err)
		}
		if limit := jm.effectiveMaxSessions(); n >= limit {
			return nil, capacityErrorf("cluster session cap reached (%d)", limit)
		}
		return nil, nil
	}
	return s.pickDaemon(pin, needGateway)
}

// pickDaemon chooses the connected daemon a Docker sandbox cron runs on: the pinned one, which
// must be connected and able, or the lowest id among those that can.
func (s *CronService) pickDaemon(pin *string, needGateway bool) (*DaemonConn, error) {
	problem := func(dc *DaemonConn) string {
		switch {
		case !dc.SandboxAvailable():
			return fmt.Sprintf("daemon %q has no sandbox image", dc.Name)
		// Every cron session is restricted, grant or no grant: a daemon that would ignore the
		// field runs it unrestricted, so it is no capacity (the run is held, not run open).
		case !dc.CanRestrictTools():
			return fmt.Sprintf("daemon %q does not report restrict_tools: update it", dc.Name)
		case needGateway && !dc.CanMCPGateway():
			return fmt.Sprintf("daemon %q does not report mcp_gateway: update it", dc.Name)
		}
		return ""
	}
	if pin != nil {
		dc := s.api.hub.GetDaemon(*pin)
		if dc == nil {
			return nil, capacityErrorf("the pinned daemon is not connected")
		}
		if p := problem(dc); p != "" {
			return nil, capacityErrorf("the pinned daemon cannot run this cron: %s", p)
		}
		return dc, nil
	}
	all := s.api.hub.GetAllDaemons()
	if len(all) == 0 {
		return nil, capacityErrorf("no daemon is connected")
	}
	slices.SortFunc(all, func(x, y *DaemonConn) int { return strings.Compare(x.ID, y.ID) })
	var why string
	for _, dc := range all {
		p := problem(dc)
		if p == "" {
			return dc, nil
		}
		why = p
	}
	return nil, capacityErrorf("no connected daemon can run the Docker sandbox (%s)", why)
}

// connProblem is one connection of a cron that cannot be used right now.
type connProblem struct {
	ID     string
	Reason string
	Gone   bool // core no longer lists it (as opposed to a connection that needs attention)
}

// connectionProblems checks every connection a cron names against core, with the cron token as
// proof: each must still exist and be usable. ErrMCPProofInvalid means core does not consider the
// token live.
func (s *CronService) connectionProblems(ctx context.Context, c *db.Cron, sel []MCPSelection) ([]connProblem, error) {
	lister := s.connections()
	if lister == nil {
		return nil, errors.New(msgMCPNoCore)
	}
	conns, err := lister.ListConnections(ctx, mcpgw.Proof{AccountID: c.OwnerAccountID, TokenID: c.TokenID})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]MCPConnection, len(conns))
	for _, cn := range conns {
		byID[cn.ID] = cn
	}
	var out []connProblem
	for _, sl := range sel {
		cn, ok := byID[sl.Connection]
		switch {
		case !ok:
			out = append(out, connProblem{sl.Connection, fmt.Sprintf("connection %s no longer exists: it was deleted, or is not the cron owner's", sl.Connection), true})
		case cn.Status != "ok":
			out = append(out, connProblem{sl.Connection, fmt.Sprintf("connection %q needs attention (status %q): sign in again or fix it in settings", cn.Name, cn.Status), false})
		}
	}
	return out, nil
}

// buildGrant is step 5: the fire-time connection check, then the shared resolver with the cron
// token as proof.
func (s *CronService) buildGrant(ctx context.Context, c *db.Cron, sel []MCPSelection, target grantTarget) (*ResolvedGrant, error) {
	problems, err := s.connectionProblems(ctx, c, sel)
	switch {
	case errors.Is(err, ErrMCPProofInvalid):
		return nil, s.pauseRevoked(ctx, c)
	case err != nil:
		return nil, capacityErrorf("cannot read the cron's MCP connections from blerg-core, will retry: %v", err)
	}
	if len(problems) > 0 {
		reasons := make([]string, len(problems))
		for i, p := range problems {
			reasons[i] = p.Reason
		}
		return nil, credentialErrorf("%s", strings.Join(reasons, "; "))
	}
	g, apiErr := s.api.resolveGrantProof(ctx, mcpgw.Proof{AccountID: c.OwnerAccountID, TokenID: c.TokenID}, sel, target)
	if apiErr != nil {
		return nil, s.startError(ctx, c, apiErr)
	}
	return g, nil
}

// startError maps a failed StartSession (or grant resolution) to the typed error the scheduler
// acts on.
func (s *CronService) startError(ctx context.Context, c *db.Cron, apiErr *APIError) error {
	switch {
	case errors.Is(apiErr, ErrNoPersonalCredential):
		return credentialErrorf("%s", apiErr.Message)
	case errors.Is(apiErr, errClusterCap), errors.Is(apiErr, errDaemonUnavailable):
		// Lost a race for the last slot or daemon after the pre-flight said yes.
		return capacityErrorf("%s", apiErr.Message)
	case errors.Is(apiErr, ErrMCPProofInvalid):
		return s.pauseRevoked(ctx, c)
	}
	return errors.New(apiErr.Message)
}

// ---- lifecycle: pause, delete, revoke ------------------------------------------------------

// OnPaused is cron.Config.OnPaused: a paused cron loses its token and its running sessions
// (spec 7.6). The token is revoked with the internal key alone, which core allows only for a
// cron-kind token of that account.
func (s *CronService) OnPaused(ctx context.Context, c *db.Cron, reason string) {
	if err := s.revokeOwed(ctx, c.OwnerAccountID, c.TokenID); err != nil {
		s.cfg.Logf("cron %s paused (%s): revoke token (kept and retried): %v", c.ID, reason, err)
	}
	if n, err := s.StopCronSessions(ctx, c.ID); err != nil {
		s.cfg.Logf("cron %s paused (%s): stop sessions: %v", c.ID, reason, err)
	} else if n > 0 {
		s.cfg.Logf("cron %s paused (%s): stopped %d running session(s)", c.ID, reason, n)
	}
}

// RevokeCronToken revokes the cron's token at core. Idempotent. The crons API calls it when a cron
// is deleted or paused; renewing mints a new token and revokes the old one the same way.
func (s *CronService) RevokeCronToken(ctx context.Context, c *db.Cron) error {
	if c == nil || c.TokenID == "" {
		return nil
	}
	return s.core.RevokeToken(ctx, c.OwnerAccountID, c.TokenID)
}

// revokeOwed revokes a retired cron token at core and makes sure a core that cannot be reached
// right now does not leave it live (spec 7.6): the revocation is recorded first, tried, and only
// forgotten once core acknowledges. RetryRevocations retries what is left. An empty id is a no-op.
func (s *CronService) revokeOwed(ctx context.Context, account, tokenID string) error {
	if tokenID == "" || s.api.dbPool == nil {
		return nil
	}
	// The record must survive the caller's context ending (a cancelled request, a shutdown).
	bg := context.WithoutCancel(ctx)
	if err := db.EnqueueTokenRevocation(bg, s.api.dbPool, account, tokenID); err != nil {
		// Could not even record it: still try, and say so.
		s.cfg.Logf("cron token %s: could not record the owed revocation: %v", tokenID, err)
	}
	rctx, cancel := context.WithTimeout(bg, cronCallTimeout)
	defer cancel()
	if err := s.core.RevokeToken(rctx, account, tokenID); err != nil {
		return err
	}
	if err := db.DeleteTokenRevocation(bg, s.api.dbPool, tokenID); err != nil {
		s.cfg.Logf("cron token %s: revoked, but the record could not be cleared: %v", tokenID, err)
	}
	return nil
}

// RetryRevocations retries the token revocations core has not acknowledged yet and reports how many
// it cleared. The leader runs it on every watchdog pass.
func (s *CronService) RetryRevocations(ctx context.Context) int {
	if s.api.dbPool == nil {
		return 0
	}
	// Only the rows whose retry time has come: one that core keeps refusing is pushed back (and
	// finally dropped), so it cannot hold the head of the queue while the rest wait.
	owed, err := db.ListDueTokenRevocations(ctx, s.api.dbPool, s.cfg.Now(), 50)
	if err != nil {
		s.cfg.Logf("cron token revocations: %v", err)
		return 0
	}
	cleared := 0
	for _, r := range owed {
		rctx, cancel := context.WithTimeout(ctx, cronCallTimeout)
		err := s.core.RevokeToken(rctx, r.AccountID, r.TokenID)
		cancel()
		if err != nil {
			s.cfg.Logf("cron token %s: revoke retry: %v", r.TokenID, err)
			if n, dropped, ferr := db.RecordTokenRevocationFailure(ctx, s.api.dbPool, r.TokenID, s.cfg.Now(), maxRevocationAttempts); ferr != nil {
				s.cfg.Logf("cron token %s: record revoke failure: %v", r.TokenID, ferr)
			} else if dropped {
				s.cfg.Logf("cron token %s: WARNING: core refused the revocation %d times; giving up (the token expires on its own)", r.TokenID, n)
			}
			continue
		}
		if err := db.DeleteTokenRevocation(ctx, s.api.dbPool, r.TokenID); err != nil {
			s.cfg.Logf("cron token %s: clear revocation record: %v", r.TokenID, err)
			continue
		}
		cleared++
	}
	return cleared
}

// cronStopEnd attributes a stop to the cron's own agent principal, acting for its owner.
func cronStopEnd(account string) db.SessionEnd {
	return db.SessionEnd{Reason: db.EndReasonStoppedByAgent, ByKind: db.EndedByAgent, ByAccount: account}
}

// StopCronSessions stops every running session the cron started (kills the daemon session or
// deletes the Job, ends the row, revokes its board token and deletes its MCP grants, fires the
// completion webhook) and reports how many it stopped. The crons API calls it on delete and pause.
func (s *CronService) StopCronSessions(ctx context.Context, cronID string) (int, error) {
	if s.api.dbPool == nil {
		return 0, nil
	}
	active, err := db.ListActiveCronSessions(ctx, s.api.dbPool, cronID)
	if err != nil {
		return 0, err
	}
	for _, sess := range active {
		s.api.stopSession(ctx, sess.SessionID, cronStopEnd(sess.Account))
	}
	return len(active), nil
}

// ---- watchdog -----------------------------------------------------------------------------

// WatchdogOnce stops every cron session that has run longer than its cron's max_runtime_seconds,
// and every one whose cron no longer exists. It uses the ordinary stop path, so the pod or daemon
// session, the gateway grants and the session's tokens all go. It reports how many it stopped.
func (s *CronService) WatchdogOnce(ctx context.Context) int {
	if s.api.dbPool == nil {
		return 0
	}
	active, err := db.ListActiveCronSessions(ctx, s.api.dbPool, "")
	if err != nil {
		s.cfg.Logf("cron watchdog: %v", err)
		return 0
	}
	now := s.cfg.Now()
	stopped := 0
	for _, sess := range active {
		switch {
		case sess.MaxRuntimeSeconds == nil:
			s.cfg.Logf("cron watchdog: stopping session %s: its cron %s no longer exists", sess.SessionID, sess.CronID)
		case now.Sub(sess.StartedAt) > time.Duration(*sess.MaxRuntimeSeconds)*time.Second:
			s.cfg.Logf("cron watchdog: stopping session %s of cron %s: over its %d second limit", sess.SessionID, sess.CronID, *sess.MaxRuntimeSeconds)
		default:
			continue
		}
		s.api.stopSession(ctx, sess.SessionID, cronStopEnd(sess.Account))
		stopped++
	}
	return stopped
}

// ---- sweeper ------------------------------------------------------------------------------

// SweepReport is what one hourly sweep did and found.
type SweepReport struct {
	OrphanGrants      int64               // grants of ended or missing sessions, removed
	StaleGrants       int                 // grants whose connection is gone from core or whose proof is dead, removed
	FlaggedCrons      map[string][]string // cron id -> reasons its connections cannot be used
	CoreUnreachable   bool                // core could not be asked; the core based checks were skipped
	CronsChecked      int
	GrantGroupsFailed int
}

// SweepOnce is the hourly sweeper (spec 7.6). Deleting a connection reaches the runner through the
// gateway's own credential fetches and this sweep, because core never calls the runner:
//
//   - grants whose session has ended or does not exist are deleted (their tokens die);
//   - grants whose connection core no longer lists for the grant's proof, or whose proof core no
//     longer considers live, are deleted, so the gateway stops serving them at once;
//   - crons that name a connection core no longer has (or that needs signing in) are FLAGGED, never
//     edited or deleted: their `mcp` stays as the owner wrote it, and the next fire fails visibly
//     with the reason (Start checks again at fire time, which is what actually fails the run). The
//     flags are readable through MissingConnections so the UI can warn before the run;
//   - pending proposals of a connection core no longer lists (found through a grant's proof or a
//     cron's token; a dead proof is not a deletion) are marked failed with a reason. A proposal of
//     a human session whose grant is already gone is caught when someone tries to approve it.
func (s *CronService) SweepOnce(ctx context.Context) SweepReport {
	rep := SweepReport{FlaggedCrons: map[string][]string{}}
	pool := s.api.dbPool
	if pool == nil {
		return rep
	}
	n, err := db.DeleteOrphanMCPGrants(ctx, pool)
	if err != nil {
		s.cfg.Logf("cron sweeper: orphan grants: %v", err)
	}
	rep.OrphanGrants = n

	lister := s.connections()
	if lister == nil {
		rep.CoreUnreachable = true
		return rep
	}
	s.sweepGrants(ctx, lister, &rep)
	s.sweepCrons(ctx, &rep)
	s.mu.Lock()
	s.flagged = rep.FlaggedCrons
	s.mu.Unlock()
	return rep
}

func (s *CronService) sweepGrants(ctx context.Context, lister MCPConnectionLister, rep *SweepReport) {
	refs, err := db.ListMCPGrantRefs(ctx, s.api.dbPool)
	if err != nil {
		s.cfg.Logf("cron sweeper: list grants: %v", err)
		return
	}
	type group struct {
		proof mcpgw.Proof
		refs  []db.MCPGrantRef
	}
	groups := map[mcpgw.Proof]*group{}
	for _, r := range refs {
		p := mcpgw.Proof{AccountID: r.AccountID}
		if r.ProofKind == "token_id" {
			p.TokenID = r.ProofValue
		} else {
			p.SessionID = r.ProofValue
		}
		if groups[p] == nil {
			groups[p] = &group{proof: p}
		}
		groups[p].refs = append(groups[p].refs, r)
	}
	for _, g := range groups {
		conns, err := lister.ListConnections(ctx, g.proof)
		gone := map[string]bool{}
		var deleted []string // really absent from a list core answered (a dead proof is not a deletion)
		switch {
		case errors.Is(err, ErrMCPProofInvalid):
			for _, r := range g.refs { // the login or token behind these grants is dead
				gone[r.ConnectionID] = true
			}
		case err != nil:
			rep.GrantGroupsFailed++
			rep.CoreUnreachable = true
			continue
		default:
			have := map[string]bool{}
			for _, cn := range conns {
				have[cn.ID] = true
			}
			for _, r := range g.refs {
				if !have[r.ConnectionID] {
					gone[r.ConnectionID] = true
					deleted = append(deleted, r.ConnectionID)
				}
			}
		}
		s.failProposalsOf(ctx, g.proof.AccountID, deleted)
		for _, r := range g.refs {
			if !gone[r.ConnectionID] {
				continue
			}
			if ok, err := db.DeleteMCPGrant(ctx, s.api.dbPool, r.SessionID, r.ConnectionID); err != nil {
				s.cfg.Logf("cron sweeper: delete grant %s/%s: %v", r.SessionID, r.ConnectionID, err)
			} else if ok {
				rep.StaleGrants++
			}
		}
	}
}

func (s *CronService) sweepCrons(ctx context.Context, rep *SweepReport) {
	crons, err := db.ListCronsWithMCP(ctx, s.api.dbPool)
	if err != nil {
		s.cfg.Logf("cron sweeper: list crons: %v", err)
		return
	}
	now := s.cfg.Now()
	for i := range crons {
		c := &crons[i]
		if !c.Enabled || c.PausedReason != nil || !c.TokenExpiresAt.After(now) {
			continue // not going to fire; nothing to flag
		}
		sel, err := parseCronMCP(c.MCP)
		if err != nil || len(sel) == 0 {
			continue
		}
		rep.CronsChecked++
		problems, err := s.connectionProblems(ctx, c, sel)
		switch {
		case errors.Is(err, ErrMCPProofInvalid):
			continue // a dead token is found and paused at the next fire
		case err != nil:
			rep.CoreUnreachable = true
			continue
		}
		var gone []string
		for _, p := range problems {
			rep.FlaggedCrons[c.ID] = append(rep.FlaggedCrons[c.ID], p.Reason)
			s.cfg.Logf("cron sweeper: cron %s: %s", c.ID, p.Reason)
			if p.Gone {
				gone = append(gone, p.ID)
			}
		}
		s.failProposalsOf(ctx, c.OwnerAccountID, gone)
	}
}

// failProposalsOf closes the pending proposals of connections core no longer has: they can never
// run, and their session (and its grant) may be long gone, so this is the one place that notices.
func (s *CronService) failProposalsOf(ctx context.Context, account string, connectionIDs []string) {
	if len(connectionIDs) == 0 {
		return
	}
	n, err := db.FailPendingProposals(ctx, s.api.dbPool, account, connectionIDs, proposalGoneReason)
	if err != nil {
		s.cfg.Logf("cron sweeper: fail proposals of deleted connections: %v", err)
		return
	}
	if n > 0 {
		s.cfg.Logf("cron sweeper: %d pending proposal(s) failed: their connection was deleted", n)
	}
}

// MissingConnections is what the last sweep found wrong with the cron's connections: one reason per
// connection that is gone or needs attention. Empty when none, or before the first sweep.
func (s *CronService) MissingConnections(cronID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.flagged[cronID])
}

// ---- run loops ----------------------------------------------------------------------------

// Run drives the watchdog (every WatchdogEvery) and the sweeper (a minute after start, then every
// SweepEvery) until ctx is done. It does nothing while Leader, when set, says this replica does
// not schedule.
func (s *CronService) Run(ctx context.Context) {
	wd := time.NewTicker(s.cfg.WatchdogEvery)
	defer wd.Stop()
	sweep := time.NewTimer(cronSweepFirstDelay)
	defer sweep.Stop()
	leader := func() bool { return s.cfg.Leader == nil || s.cfg.Leader() }
	for {
		select {
		case <-ctx.Done():
			return
		case <-wd.C:
			if leader() {
				wctx, cancel := context.WithTimeout(ctx, cronCallTimeout*4)
				s.WatchdogOnce(wctx)
				s.RetryRevocations(wctx)
				cancel()
			}
		case <-sweep.C:
			if leader() {
				sctx, cancel := context.WithTimeout(ctx, cronCallTimeout*8)
				rep := s.SweepOnce(sctx)
				cancel()
				if rep.OrphanGrants+int64(rep.StaleGrants) > 0 || len(rep.FlaggedCrons) > 0 {
					s.cfg.Logf("cron sweeper: removed %d orphan and %d stale grants; %d cron(s) name unusable connections",
						rep.OrphanGrants, rep.StaleGrants, len(rep.FlaggedCrons))
				}
			}
			sweep.Reset(s.cfg.SweepEvery)
		}
	}
}

// StartCrons builds the cron service and the scheduler and runs them until ctx is done. Crons are
// enabled only with the runner database AND blerg-core (their identity, status and connections all
// live there); otherwise it logs why they are off and returns false. It introduces no configuration:
// the limits are the documented defaults of internal/cron.
func StartCrons(ctx context.Context, api *API, coreURL, coreInternalKey string) bool {
	if api == nil || api.dbPool == nil {
		log.Println("crons disabled: the runner database is not configured")
		return false
	}
	if coreURL == "" || coreInternalKey == "" {
		log.Println("crons disabled: they need blerg-core (BLERG_CORE_URL and BLERG_RUNNER_CORE_INTERNAL_KEY)")
		return false
	}
	var sched *cron.Scheduler
	svcCfg := CronServiceConfig{
		Core:   &HTTPCronCore{BaseURL: coreURL, InternalKey: coreInternalKey},
		Leader: func() bool { return sched != nil && sched.HoldsLock() },
	}
	if base := BoardURLFromEnv(os.Getenv); base != "" {
		svcCfg.Cards = &HTTPBoardCards{BaseURL: base, Exchanger: &mcpgw.HTTPBoardExchanger{BaseURL: coreURL, InternalKey: coreInternalKey}}
	}
	svc := NewCronService(api, svcCfg)
	sched, err := cron.New(cron.Config{Pool: api.dbPool, Starter: svc, Sessions: svc, OnPaused: svc.OnPaused})
	if err != nil {
		log.Printf("crons disabled: %v", err)
		api.cron = nil
		return false
	}
	go sched.Run(ctx)
	go svc.Run(ctx)
	log.Println("crons enabled")
	return true
}
