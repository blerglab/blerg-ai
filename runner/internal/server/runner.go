package server

// Runner contract endpoints (blerg-board spec Part 2c): an external broker
// (blerg-board) starts and drives cluster agent sessions over REST.
// Authenticated by a dedicated service credential (BLERG_RUNNER_KEY) —
// deliberately NOT the master daemon token; this is a narrower grant for a
// single caller.
//
//   POST /api/runner/start                       {repo,title,prompt,model,env}
//   GET  /api/runner/sessions/{id}               → {lifecycle,runtime,resumable}
//   POST /api/runner/sessions/{id}/message       {text}
//   POST /api/runner/sessions/{id}/interrupt
//   POST /api/runner/sessions/{id}/stop
//   GET  /api/runner/sessions/{id}/events?after_seq=&limit= → {events,has_more}
//   GET  /api/runner/me                          → the calling principal
//
// The public documentation of all of this is agents.go: GET /agents (manifest)
// and GET /openapi.json, both unauthenticated.
//
// `status` is deliberately not one string: lifecycle is the session state
// machine, runtime describes the hosting layer (blerg-runner's `disconnected`
// already conflates the two — see the blerg-board design's fact-check), resumable
// says whether a dead session can be revived by the next message.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/scratch"
)

// coreAuthAudience is the audience blerg-core mints runner-facing tokens
// for; identity.Verify rejects any token minted for a different component.
const coreAuthAudience = "blerg-runner"

// coreAuthRunnerCap is the capability a core-issued token must carry to
// drive the runner contract. The runner API can start cluster agent
// sessions and inject arbitrary env (including secrets), so a bare
// aud:"blerg-runner" token is not enough on its own — it must also assert
// this capability. "session.start" is the same capability blerg-core's
// project roles (owner/maintainer) use to gate session starts elsewhere, so
// reuse it here rather than invent a runner-only name.
const coreAuthRunnerCap = "session.start"

// coreAuthSensitiveCaps reports whether a capability is sensitive enough that
// a stale (unrefreshed) blerg-core revocation list should fail closed on it.
// The runner contract can start sessions and pass through secrets via env,
// so both the capability it requires and secrets.read must fail closed when
// core is unreachable past the staleness ceiling — otherwise a revoked
// runner-capable token would keep being accepted indefinitely while core is
// down (see identity.Verify's StaleBeyondCeiling branch).
//
// board.admin (delete boards, approve/delete agent rules) and card.write
// (every board/column/ticket mutation) are writes with the same problem: a
// revoked owner's token must not keep deleting boards or enabling rules while
// core is down. card.read is deliberately left out of this list, but that
// only degrades a stale token to read-only in the narrow case where
// card.read is the ONLY capability it carries — identity.Verify's staleness
// check runs over the token's FULL capability list, so any token that also
// carries a sensitive capability (in practice, any realistic member/admin
// token: it carries session.start) is refused outright, writes AND reads,
// once the revocation snapshot is past its staleness ceiling. Only a
// genuinely read-only-cap token keeps reading during that window.
func coreAuthSensitiveCaps(capability string) bool {
	switch capability {
	case coreAuthRunnerCap, "secrets.read", "board.admin", "card.write":
		return true
	default:
		return false
	}
}

// SetRunnerKey enables the runner endpoints. Empty ⇒ they all 404.
func (a *API) SetRunnerKey(key string) { a.runnerKey = key }

// runnerKeyPrincipalKind is the principal kind of the static operator key. It is
// deliberately not one of identity's token kinds: the key is a component
// credential with no owning account, so there is nobody to attribute its work
// to and no expiry to report.
const runnerKeyPrincipalKind = "runner_key"

// runnerPrincipal is WHO a runner-contract caller is, as far as the runner can
// prove it. It is the body of GET /api/runner/me verbatim, so the omitempty
// tags are load-bearing: the static key must report `{"kind":"runner_key"}` and
// not a set of empty claims it does not have.
//
// Caps is the authorization source of truth. OnBehalfOf identifies the account an agent
// token acts for: requireSessionAccess uses it to scope such a principal DOWN to the
// sessions it started, and nothing may ever use it to grant access it would not otherwise
// have (the claim is core-signed, never caller-supplied).
type runnerPrincipal struct {
	Kind       string   `json:"kind"`
	Sub        string   `json:"sub,omitempty"`
	OnBehalfOf string   `json:"on_behalf_of,omitempty"`
	Aud        string   `json:"aud,omitempty"`
	Caps       []string `json:"caps,omitempty"`
	ExpiresAt  int64    `json:"expires_at,omitempty"`
	// Sid is the browser session a human token came from (its `sid` claim), the proof core
	// wants for the launcher's credentials. Empty for agent tokens and the runner key.
	Sid string `json:"sid,omitempty"`
}

// RunnerPrincipal is runnerPrincipal under a name other packages can write
// down. The runner's MCP server (internal/mcp) calls the same contract
// operations the REST handlers do and has to name the principal it threads
// through them; the fields it needs are exported already.
type RunnerPrincipal = runnerPrincipal

// RunnerStartRequest is runnerStartRequest, exported for the same reason.
type RunnerStartRequest = runnerStartRequest

// APIError is a contract failure as the caller will see it: the HTTP status
// the REST path would answer with, and the message it would put in the body.
// Every non-HTTP caller (the MCP tools) reports the SAME message, so the two
// surfaces can never drift into explaining a failure differently.
type APIError struct {
	Status  int
	Message string
	// RetryAfter, when positive, is sent as the Retry-After header (seconds): the failure is
	// expected to clear by itself.
	RetryAfter int
	// Cause is what a caller in this process can match on (errors.Is / errors.As) to tell one
	// failure from another with the same status; it never reaches a response body. Nil for
	// almost every error.
	Cause error
}

func (e *APIError) Error() string { return e.Message }

// Unwrap exposes Cause to errors.Is and errors.As.
func (e *APIError) Unwrap() error { return e.Cause }

func apiErrorf(status int, format string, args ...any) *APIError {
	return &APIError{Status: status, Message: fmt.Sprintf(format, args...)}
}

// apiErrorCause is apiErrorf with a Cause attached.
func apiErrorCause(status int, cause error, format string, args ...any) *APIError {
	return &APIError{Status: status, Message: fmt.Sprintf(format, args...), Cause: cause}
}

// writeAPIError renders an APIError exactly as the handlers always have.
func writeAPIError(w http.ResponseWriter, e *APIError) {
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfter))
	}
	writeError(w, e.Status, e.Message)
}

// AuthorizeRunner is authRunner for callers outside this package (the MCP
// server). It writes the failure response itself, like authRunner.
func (a *API) AuthorizeRunner(w http.ResponseWriter, r *http.Request) (RunnerPrincipal, bool) {
	return a.authRunner(w, r)
}

// authRunner authenticates a runner-contract request and reports the principal
// behind it. On failure it writes the response itself (404 when the contract is
// not configured at all, 401 otherwise) and returns false, so callers need only
// `if !ok { return }`.
func (a *API) authRunner(w http.ResponseWriter, r *http.Request) (runnerPrincipal, bool) {
	if a.runnerKey == "" {
		writeError(w, http.StatusNotFound, "runner endpoints not configured")
		return runnerPrincipal{}, false
	}
	raw := ""
	if h := r.Header.Get("Authorization"); h != "" {
		raw = strings.TrimPrefix(h, "Bearer ")
	}
	if subtle.ConstantTimeCompare([]byte(raw), []byte(a.runnerKey)) == 1 {
		return runnerPrincipal{Kind: runnerKeyPrincipalKind}, true
	}
	// Not the static key — if blerg-core is configured as an identity
	// authority, accept a core-issued token scoped to this component, but
	// only if it also carries coreAuthRunnerCap: aud:"blerg-runner" alone
	// just means "core meant this for the runner component", not "this
	// caller may start sessions and read the env/secrets runner hands to
	// them". Every other core token (wrong capability set) is rejected here.
	if a.coreAuth != nil {
		if p, err := identity.Verify(raw, coreAuthAudience, a.coreAuth.KeySet(), a.coreAuth, coreAuthSensitiveCaps); err == nil {
			if p.Has(coreAuthRunnerCap) {
				return runnerPrincipal{
					Kind:       p.Kind,
					Sub:        p.Sub,
					OnBehalfOf: p.OnBehalfOf,
					Aud:        p.Aud,
					Caps:       p.Caps,
					ExpiresAt:  p.ExpiresAt,
					Sid:        p.Sid,
				}, true
			}
		}
	}
	writeError(w, http.StatusUnauthorized, "unauthorized")
	return runnerPrincipal{}, false
}

type runnerStartRequest struct {
	Repo   string `json:"repo"`
	GitURL string `json:"git_url"` // optional override for cross-org repos
	// Provider is the git provider Repo lives on (a gitprovider ID). Optional:
	// absent means GitHub, as it always did for this contract (see
	// repo_provider.go). omitempty keeps an older request's idempotency hash.
	Provider string `json:"provider,omitempty"`
	Title    string `json:"title"`
	Prompt   string `json:"prompt"`
	Model    string `json:"model"`
	// Effort is the Claude reasoning effort ("" = the model's default). Tagged
	// omitempty so a request without it hashes exactly as it did before the
	// field existed (startRequestHash) — a retry across the upgrade must not
	// read as a different request.
	Effort string            `json:"effort,omitempty"`
	Env    map[string]string `json:"env"`
	// CallbackURL is POSTed the result when the session reaches a terminal
	// lifecycle; CallbackSecret (optional) is the HMAC key that signs that
	// delivery. The secret is stored so it can sign, and is never returned by
	// any endpoint or written to a log.
	CallbackURL    string `json:"callback_url"`
	CallbackSecret string `json:"callback_secret"`
	// Engine is the coding engine ("" = claude); Runtime picks the hosting
	// layer explicitly ("cluster" | "docker" | "daemon" | "" = cluster where
	// one is configured, else the daemon's sandbox when it has the image, else
	// the daemon host — see resolveDaemonRuntime).
	Engine  string `json:"engine"`
	Runtime string `json:"runtime"`
	// AutoStop makes this a one-shot session: the runner stops it itself once
	// the first turn after the initial prompt is done, so a fire-and-forget
	// caller gets a terminal result and its callback without having to stop
	// the session. Default false — an interactive session is unchanged.
	AutoStop bool `json:"auto_stop"`
	// Interaction says whether a person is reading the session's chat as it works:
	// "interactive" or "unattended" (interaction.go). Absent means unattended on this
	// contract, which is what a tool's job is; a cron's session is unattended whatever is
	// asked. Anything else is a 400. omitempty keeps an older request's idempotency hash.
	Interaction string `json:"interaction,omitempty"`
	// NoRepo starts a session tied to no repository (repo, git_url and
	// provider must then be absent). Explicit, never inferred from a blank
	// repo: without it a blank repo is still "repo is required". On the
	// cluster the pod works in an empty directory; on a daemon the session
	// gets a new scratch folder under the repos root (a generated
	// ".scratch-…" name). omitempty keeps an older request's idempotency hash.
	NoRepo bool `json:"no_repo,omitempty"`
	// Grant is the MCP connections this session may use, resolved and checked (mcpstart.go).
	// It is set only in process (the scheduler); json:"-" means a request body can never
	// carry one and that the idempotency hash of a request without it is unchanged. Every
	// spawn path either attaches it or refuses.
	Grant *ResolvedGrant `json:"-"`
	// The three fields below are set only in process, by the cron scheduler (cronstart.go), and
	// are json:"-" for the same reason as Grant: a request body can never carry them, and the
	// idempotency hash of a request without them is unchanged.
	//
	// DaemonID pins a NoRepo start to one connected daemon (the cron's daemon_id). CronID marks the
	// session as started by that cron (sessions.cron_id) and makes it private. NoOperatorFallback
	// forbids the shared operator credential on the cluster (SessionJobSpec.NoOperatorFallback).
	DaemonID           string `json:"-"`
	CronID             string `json:"-"`
	NoOperatorFallback bool   `json:"-"`
	// IdemScope overrides the idempotency scope of this start (a cron's is stable across token
	// renewals: "cron:<cron id>"). In process only, like the fields above.
	IdemScope string `json:"-"`
}

// Runtime values POST /api/runner/start accepts. "" means "whatever this
// install has configured", which is the pre-contract behaviour.
//
// runnerRuntimeDocker is the same local sandbox the launch sheet's "Local
// sandbox" card picks: the session still runs on a connected daemon, but the
// engine runs inside the hardened container rather than on the host.
const (
	runnerRuntimeCluster = "cluster"
	runnerRuntimeDocker  = "docker"
	runnerRuntimeDaemon  = "daemon"
)

// runnerRuntimeMessage is the 422 a start with an unknown runtime answers.
const runnerRuntimeMessage = `runtime must be "cluster", "docker" or "daemon"`

// idempotencyTTL is how long a runner_idempotency record is honoured. It is a
// retry window for a broker that lost a response, not a permanent ledger of
// every start ever made.
const idempotencyTTL = 24 * time.Hour

// maxIdempotencyKeyLen bounds the caller-supplied key: it is a primary-key
// column, and an unbounded header would let a caller write arbitrarily large
// rows into it.
const maxIdempotencyKeyLen = 128

// idempotencyKeyMessage is what BOTH surfaces say about a key that was supplied
// and cannot be used. It deliberately does not mention a header: an MCP caller
// passes the key as a tool argument and has no header to correct. The HTTP
// handler keeps its own header-shaped message for the faults only a header can
// have (sent empty, sent twice).
var idempotencyKeyMessage = fmt.Sprintf("idempotency key must be 1-%d characters", maxIdempotencyKeyLen)

// CheckIdempotencyKey validates a key the caller actually supplied. A key that
// is present but empty is a caller bug rather than "no key": it would silently
// drop the retry protection the caller believes it has. Absent keys never reach
// here — only the caller knows whether one was sent, which over MCP is the
// difference between a missing argument and an empty string.
func CheckIdempotencyKey(key string) *APIError {
	if key == "" || len(key) > maxIdempotencyKeyLen {
		return apiErrorf(http.StatusBadRequest, "%s", idempotencyKeyMessage)
	}
	return nil
}

// idempotencyConflictMessage is the documented 409 body for a key reused with
// a different request (openapi.json's IdempotencyConflict).
const idempotencyConflictMessage = "idempotency key reused with a different request"

// startScope is the idempotency scope of a caller: keys are per-credential, so
// two brokers using the same obvious key ("retry-1") neither collide nor learn
// each other's session ids. The static operator key has no subject of its own.
func (p runnerPrincipal) startScope() string {
	if p.Kind == runnerKeyPrincipalKind || p.Sub == "" {
		return runnerKeyPrincipalKind
	}
	return p.Kind + ":" + p.Sub
}

// spawningAccountID is the account a session started by this principal runs
// as: an agent token acts for its owner (on_behalf_of), a human token for
// itself. The static runner key has no owning account at all — those sessions
// fall back to the shared operator credentials, as they always have.
func (p runnerPrincipal) spawningAccountID() string {
	if p.Kind == runnerKeyPrincipalKind {
		return ""
	}
	if p.OnBehalfOf != "" {
		return p.OnBehalfOf
	}
	return p.Sub
}

// agentPrincipalKind is the principal kind of a blerg-core-minted agent token
// — a credential a human created for a tool to act as them.
const agentPrincipalKind = "agent"

// sessionAdminCap is the capability that lifts the per-owner session scope. An
// account trusted to administer boards is already trusted to see the sessions
// those boards drive, so an admin's agent token keeps install-wide visibility.
const sessionAdminCap = "board.admin"

// seesAllSessions reports whether this principal may operate on every session
// on the install, rather than only the ones it started.
//
// Only an agent token is scoped: it is the one credential a *user* mints and
// hands to a tool, so one user's tool must not be able to read, steer or stop
// another user's session. The static runner key, a human session token and a
// service credential are all install-level operators (and a board.admin agent
// token is an operator too), so they keep the install-wide view they have
// always had.
func (p runnerPrincipal) seesAllSessions() bool {
	if p.Kind != agentPrincipalKind {
		return true
	}
	for _, c := range p.Caps {
		if c == sessionAdminCap {
			return true
		}
	}
	return false
}

// requireSessionAccess loads a session and enforces the rule above. It is the
// ONE place per-session authorization happens: every per-session operation
// (status, message, interrupt, stop, events, stream, result) goes through it,
// REST and MCP alike, so the two surfaces cannot come to disagree about who may
// touch what.
//
// The decision itself is canSee (privacy.go). A session the principal may not see is reported EXACTLY as an unknown one —
// same status, same message — so the contract is not an oracle for which
// session ids exist. A session with no spawning account (one the static runner
// key started) belongs to nobody, so a scoped principal cannot see it either.
func (a *API) requireSessionAccess(ctx context.Context, p runnerPrincipal, sessionID string) (*db.SessionRow, *APIError) {
	row, err := db.GetSession(ctx, a.dbPool, sessionID)
	if err != nil || row == nil {
		return nil, apiErrorf(http.StatusNotFound, "session not found")
	}
	if !canSee(p, row) { // privacy.go: also the private-session rule, which no principal bypasses
		return nil, apiErrorf(http.StatusNotFound, "session not found")
	}
	return row, nil
}

// tokenID is the agent token behind this call, sent to blerg-core with every
// credential fetch so core can verify the token is still live. Only agent
// tokens have one: a human's session token is not an agent token, and the
// runner key is not a token at all.
func (p runnerPrincipal) tokenID() string {
	if p.Kind != "agent" {
		return ""
	}
	return p.Sub
}

// proof is what this principal offers core as the live thing behind a credential fetch: the
// agent token, or for a human token the browser session it came from. The runner key and an
// unstamped (pre-sid) human token have none.
func (p runnerPrincipal) proof() coreProof {
	if p.Kind == runnerKeyPrincipalKind {
		return coreProof{}
	}
	if id := p.tokenID(); id != "" {
		return coreProof{TokenID: id}
	}
	return coreProof{SessionID: p.Sid}
}

// callbackSchemeMessage is the 422 a rejected callback_url gets. It names the
// condition the loopback exception now carries, so an operator who hits it on
// a cluster install knows which switch is missing rather than assuming the
// contract changed.
const callbackSchemeMessage = "callback_url must use https (http is allowed only for localhost, " +
	"and only where " + webhookAllowPrivateEnv + " is on)"

// validateCallbackURL enforces the webhook rule (spec §3): https anywhere, or
// plain http against the loopback host an operator is testing with — and that
// exception only on an install that has opted into private callback targets
// (BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE, which the desktop compose stack sets).
// The runner POSTs a session's result — including anything the agent said — to
// this URL, so cleartext to an arbitrary host is not something a caller gets
// to opt into.
//
// Gating the exception on the same switch as the delivery-time address guard
// is what keeps the two halves honest: on a cluster install "localhost" is the
// runner's own pod, so accepting such a URL at start would promise a delivery
// the SSRF guard then refuses — a callback that silently never arrives. It is
// rejected at start instead, with a 422 that says why.
func validateCallbackURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("callback_url is not a valid URL")
	}
	if u.Host == "" {
		return fmt.Errorf("callback_url must be an absolute URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if webhookLocalhostException(u) && webhookAllowPrivate() {
			return nil
		}
		return fmt.Errorf("%s", callbackSchemeMessage)
	default:
		return fmt.Errorf("%s", callbackSchemeMessage)
	}
}

// HandleRunnerStart spawns an agent session with caller-supplied env
// (this is how blerg-board injects BLERG_BOARD_URL/BLERG_BOARD_TOKEN/BLERG_BOARD_BOARD).
//
// Cluster sessions run as the CALLER, not as the operator: an agent token's
// owner (on_behalf_of) becomes the session's spawning account, and the token's
// own id travels with every credential fetch so blerg-core can check it is
// still live. The static runner key keeps the old behaviour — no account, no
// token, shared operator credentials.
func (a *API) HandleRunnerStart(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authRunner(w, r)
	if !ok {
		return
	}
	// Read the key before the body: a malformed key is a caller bug worth
	// reporting even if the body would also have been rejected.
	idemKey := r.Header.Get("Idempotency-Key")
	// A header that is present but empty is a caller bug, not "no key": it
	// would silently drop the retry protection the caller thinks it has.
	if sent, ok := r.Header["Idempotency-Key"]; ok &&
		(idemKey == "" || len(idemKey) > maxIdempotencyKeyLen || len(sent) > 1) {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("Idempotency-Key must be a single header of 1-%d characters", maxIdempotencyKeyLen))
		return
	}
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// MCP connections can only be attached from the launch sheet's route by a signed-in
	// person (mcpstart.go): this one refuses a body that names them rather than dropping the
	// unknown key and starting a session without the connections the caller expected.
	if apiErr := RejectMCPArgument(raw); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	var req runnerStartRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	resp, apiErr := a.StartSession(r.Context(), principal, req, idemKey)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	if resp.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"session_id": resp.SessionID})
}

// StartResponse is what a successful start reports: the session id, and
// whether this was a replay of an earlier start under the same idempotency
// key rather than a new session.
type StartResponse struct {
	SessionID string
	Replayed  bool
}

// StartSession is POST /api/runner/start with no HTTP in it: validation,
// idempotency and dispatch to the chosen runtime. The handler above and the
// MCP start_session tool are both thin wrappers over it, so the two surfaces
// cannot diverge on what a start does or what it refuses.
//
// idemKey is the caller's idempotency key ("" = none); the HTTP handler takes
// it from the Idempotency-Key header, MCP from the tool's idempotency_key
// argument.
func (a *API) StartSession(ctx context.Context, principal RunnerPrincipal, req RunnerStartRequest, idemKey string) (StartResponse, *APIError) {
	if len(idemKey) > maxIdempotencyKeyLen {
		return StartResponse{}, apiErrorf(http.StatusBadRequest, "%s", idempotencyKeyMessage)
	}
	// A grant is built in process (the scheduler) for the account the principal acts for; a
	// request decoded from a body cannot carry one. Refuse one that is malformed or for
	// somebody else rather than start a session on the wrong account's connections.
	if req.Grant != nil && (!req.Grant.valid() || principal.spawningAccountID() == "" ||
		req.Grant.AccountID != principal.spawningAccountID()) {
		return StartResponse{}, apiErrorf(http.StatusForbidden, "the MCP grant does not belong to the caller")
	}
	if msg := cronStartProblem(req); msg != "" { // cronstart.go: what a cron's start must look like
		return StartResponse{}, apiErrorf(http.StatusUnprocessableEntity, "%s", msg)
	}
	// Checked here, before the idempotency claim; applied only after the request is hashed, so
	// a request that does not state a mode hashes as it did before the field existed.
	interaction, apiErr := resolveInteraction(req.Interaction, false, req.CronID != "")
	if apiErr != nil {
		return StartResponse{}, apiErr
	}
	if req.NoRepo {
		if msg := noRepoProblem(req.Repo, req.GitURL, req.Provider, false, false); msg != "" {
			return StartResponse{}, apiErrorf(http.StatusUnprocessableEntity, "%s", msg)
		}
	} else if req.Repo == "" {
		return StartResponse{}, apiErrorf(http.StatusUnprocessableEntity, "repo is required")
	}
	if err := validateCallbackURL(req.CallbackURL); err != nil {
		return StartResponse{}, apiErrorf(http.StatusUnprocessableEntity, "%s", err.Error())
	}
	switch req.Runtime {
	case "", runnerRuntimeCluster, runnerRuntimeDocker, runnerRuntimeDaemon:
	default:
		return StartResponse{}, apiErrorf(http.StatusUnprocessableEntity, "%s", runnerRuntimeMessage)
	}
	if msg := a.modelEffortProblem(ctx, req.Engine, req.Model, req.Effort, ""); msg != "" {
		return StartResponse{}, apiErrorf(http.StatusUnprocessableEntity, "%s", msg)
	}
	if req.Runtime == runnerRuntimeCluster && a.hub.JobManager() == nil {
		return StartResponse{}, apiErrorf(http.StatusUnprocessableEntity, "cluster runtime is not configured")
	}
	// Refuse env that would shadow blerg-owned variables.
	for k := range req.Env {
		if strings.HasPrefix(k, "BLERG_RUNNER_") || k == "ANTHROPIC_API_KEY" {
			return StartResponse{}, apiErrorf(http.StatusUnprocessableEntity, "env key %s is reserved", k)
		}
	}
	// The repo name reaches a container workdir (cluster) or a daemon's repos
	// root (desktop); both hosts join it onto a path, so reject traversal here
	// before either does. The daemon re-checks containment itself. A no_repo
	// start names none (checked empty above).
	if !req.NoRepo {
		if err := validateRepoName(req.Repo); err != nil {
			return StartResponse{}, apiErrorf(http.StatusUnprocessableEntity,
				"repo must be a folder name or org/name (one slash at most, no '..', no leading dot)")
		}
		if msg := repoProviderProblem(req.Provider, req.Repo, false); msg != "" {
			return StartResponse{}, apiErrorf(http.StatusUnprocessableEntity, "%s", msg)
		}
		if !gitURLMatchesProvider(req.GitURL, req.Provider) {
			return StartResponse{}, apiErrorf(http.StatusUnprocessableEntity, "git_url is not on %s's host", req.Provider)
		}
	}

	// The session id is minted here, before anything is started, because the
	// idempotency record has to name it: a retry that arrives while the first
	// start is still in flight must be answered with the SAME id.
	sessionID := newUUID()
	scope := principal.startScope()
	if req.IdemScope != "" {
		scope = req.IdemScope
	}
	if idemKey != "" && a.dbPool != nil {
		hash, err := startRequestHash(req)
		if err != nil {
			return StartResponse{}, apiErrorf(http.StatusBadRequest, "invalid request body")
		}
		existing, claimed, err := db.ClaimIdempotencyKey(ctx, a.dbPool, scope, idemKey, hash, sessionID, idempotencyTTL)
		if err != nil {
			log.Printf("runner start: idempotency claim: %v", err)
			return StartResponse{}, apiErrorf(http.StatusInternalServerError, "idempotency store unavailable")
		}
		switch {
		case !claimed && existing == nil:
			// The record was pruned out from under the claim; a retry is the
			// honest answer rather than starting a second session.
			return StartResponse{}, apiErrorf(http.StatusServiceUnavailable, "idempotency record raced — retry")
		case !claimed && existing.RequestHash != hash:
			return StartResponse{}, apiErrorf(http.StatusConflict, "%s", idempotencyConflictMessage)
		case !claimed:
			return StartResponse{SessionID: existing.SessionID, Replayed: true}, nil
		}
		// Claimed. Sweep expired records while we are already writing here —
		// there is no scheduler in this process.
		if err := db.PruneIdempotency(ctx, a.dbPool, idempotencyTTL); err != nil {
			log.Printf("runner start: idempotency prune: %v", err)
		}
	}

	req.Interaction = interaction
	if apiErr := a.startRunnerSession(ctx, req, principal, sessionID); apiErr != nil {
		if idemKey != "" && a.dbPool != nil {
			// Nothing runs under this key, so it must not answer a retry with
			// a session that never existed.
			if err := db.ReleaseIdempotencyKey(context.WithoutCancel(ctx), a.dbPool, scope, idemKey, sessionID); err != nil {
				log.Printf("runner start: idempotency release: %v", err)
			}
		}
		return StartResponse{}, apiErr
	}
	return StartResponse{SessionID: sessionID}, nil
}

// startRequestHash is the canonical hash of a start request: the decoded
// struct re-marshalled, so it does not depend on key order, whitespace or
// unknown fields in what the caller sent. It is what decides whether a reused
// Idempotency-Key is a retry or a different request.
func startRequestHash(req runnerStartRequest) (string, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// startRunnerSession dispatches a validated start to the chosen runtime,
// reporting the failure the caller should be told about (nil = started).
func (a *API) startRunnerSession(ctx context.Context, req runnerStartRequest, principal runnerPrincipal, sessionID string) *APIError {
	jm := a.hub.JobManager()
	// Explicit runtime:"daemon" or "docker" runs on a workstation even where a
	// cluster is configured; the cluster is otherwise the default when it
	// exists.
	if jm == nil || req.Runtime == runnerRuntimeDaemon || req.Runtime == runnerRuntimeDocker {
		return a.startBoardSessionOnDaemon(ctx, req, principal, sessionID)
	}
	spawningAccount := principal.spawningAccountID()
	tokenID := principal.tokenID()
	// A grant is attached or the start is refused, before anything is recorded (mcpstart.go).
	if req.Grant != nil {
		if apiErr := a.checkGrant(req.Grant, grantTarget{Runtime: runnerRuntimeCluster, Engine: req.Engine, Kind: "agent"}); apiErr != nil {
			return apiErr
		}
	}
	// Fail closed before anything is recorded: with personal credentials wired, core only
	// releases them against a named live token/session, and this caller names neither.
	if spawningAccount != "" && jm.personalCredentialsWired() && !principal.proof().valid() {
		return apiErrorf(http.StatusUnauthorized, "%s", errNoLivenessProof.Error())
	}
	// A no-repo session has nothing to clone: no URL recorded, none (and so
	// no git token) given to the pod, and "" as its repo — the cluster has no
	// folder to name.
	gitURL, workdir := "", clusterNoRepoWorkdir
	if !req.NoRepo {
		gitURL = firstNonEmpty(req.GitURL, jm.CloneURLFor(req.Provider, req.Repo))
		workdir = "/workspace/" + req.Repo
	}
	if a.dbPool != nil {
		if err := db.UpsertDaemon(ctx, a.dbPool, clusterDaemonID, "cluster", "runner", ""); err != nil {
			log.Printf("runner: cluster daemon upsert: %v", err)
		}
		// runtime='cluster' is written by the insert itself: the reconciler
		// selects on that column, so a session whose runtime landed in a
		// separate write that failed would never be closed out.
		//
		// A grant or a cron makes the session private, and that (with its owner and the
		// cron id) goes in the same insert: no other account can list it before the
		// marking below runs.
		origin := sessionOriginFor(spawningAccount, req.Grant != nil || req.CronID != "", req.CronID)
		a.notePrivateInsert(sessionID, origin)
		if err := db.InsertClusterSessionAs(ctx, a.dbPool, sessionID, clusterDaemonID, "starting",
			workdir, req.Repo, req.Title, req.Model, origin); err != nil {
			return apiErrorf(http.StatusInternalServerError, "session create failed")
		}
		if err := db.SetSessionKind(ctx, a.dbPool, sessionID, "agent"); err != nil {
			log.Printf("runner SetSessionKind %s: %v", sessionID, err)
		}
		recordLaunchEffort(ctx, a.dbPool, sessionID, req.Effort)
		if err := db.SetSessionEngine(ctx, a.dbPool, sessionID, req.Engine); err != nil {
			log.Printf("runner SetSessionEngine %s: %v", sessionID, err)
		}
		if gitURL != "" {
			if err := db.SetSessionGitURL(ctx, a.dbPool, sessionID, gitURL); err != nil {
				log.Printf("runner SetSessionGitURL %s: %v", sessionID, err)
			}
		}
		// Attribution comes from the verified credential, never the body: a
		// caller must not be able to claim it is spawning for someone else.
		if spawningAccount != "" {
			if err := db.SetSessionSpawningAccount(ctx, a.dbPool, sessionID, spawningAccount); err != nil {
				log.Printf("runner SetSessionSpawningAccount %s: %v", sessionID, err)
			}
		}
		if tokenID != "" {
			if err := db.SetSessionTokenID(ctx, a.dbPool, sessionID, tokenID); err != nil {
				log.Printf("runner SetSessionTokenID %s: %v", sessionID, err)
			}
		}
		a.recordStartedBy(ctx, sessionID, principal, req.CronID)
		if req.CallbackURL != "" || req.CallbackSecret != "" {
			if err := db.SetSessionCallback(ctx, a.dbPool, sessionID, req.CallbackURL, req.CallbackSecret); err != nil {
				log.Printf("runner SetSessionCallback %s: %v", sessionID, err)
			}
		}
		if req.AutoStop {
			if err := db.SetSessionAutoStop(ctx, a.dbPool, sessionID, true); err != nil {
				log.Printf("runner SetSessionAutoStop %s: %v", sessionID, err)
			}
		}
		recordInteraction(ctx, a.dbPool, sessionID, req.Interaction)
	}
	// A cron's session is marked (cron_id) and made private, after its account is recorded.
	if apiErr := a.markCronSession(ctx, req, sessionID); apiErr != nil {
		a.abortGrantSession(ctx, sessionID)
		return apiErr
	}
	// The spawning account is recorded above; the session is made private after it, and the
	// tokens go only into the per-session Secret.
	var gateway *protocol.MCPGatewayConfig
	if req.Grant != nil {
		var apiErr *APIError
		if gateway, apiErr = a.attachGrant(ctx, sessionID, req.Grant); apiErr != nil {
			a.abortGrantSession(ctx, sessionID)
			return apiErr
		}
	}
	// Unattended: every cron session and every board-started grant session runs restricted
	// (mcpstart.go). Recorded so a resume rebuilds the same Job.
	restricted := req.Grant != nil || req.CronID != ""
	if restricted && a.dbPool != nil {
		if err := db.SetSessionRestrictTools(ctx, a.dbPool, sessionID); err != nil {
			log.Printf("SetSessionRestrictTools %s: %v", sessionID, err)
		}
	}
	spec := SessionJobSpec{
		MCPGateway:    gateway,
		RestrictTools: restricted,
		Interaction:   req.Interaction,
		SessionID:     sessionID, Repo: req.Repo, Title: req.Title,
		Model: req.Model, Effort: req.Effort, Engine: req.Engine, InitialPrompt: req.Prompt,
		ExtraEnv:          withClusterSessionToken(ctx, a.dbPool, sessionID, req.Env),
		GitURL:            gitURL,
		NoRepo:            req.NoRepo,
		SpawningAccountID: spawningAccount,
		TokenID:           tokenID,
		AuthSessionID:     principal.proof().SessionID,
		// A cron never runs on the shared operator credential (spec 7.4).
		NoOperatorFallback: req.NoOperatorFallback,
	}
	jm.ResolvePlugins(ctx, &spec)
	announceClusterStart(ctx, a.hub, a.dbPool, sessionID, req.Repo, false, pluginStage(spec.Plugins, spec.PluginsNote))
	if err := jm.CreateSessionJob(spec); err != nil { //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
		// The row was pre-created for the FK on agent_events, so a failed Job
		// leaves a session behind. Left at "starting" it never converges:
		// status and result report a session that is forever about to begin,
		// the completion webhook never fires, and a replayed Idempotency-Key
		// keeps handing the caller that same dead id. Mark it terminal, with
		// the same text the caller is about to be given (CreateSessionJob
		// already sanitises anything credential-bearing out of it).
		a.markClusterStartFailed(ctx, sessionID, err.Error())
		if a.dbPool != nil {
			// A session that died before it began is still a completed one as
			// far as the caller's callback is concerned — telling them only
			// through the 503 would leave an async broker waiting forever.
			a.notifyCompletion(sessionID) //nolint:contextcheck // webhook delivery outlives its caller by design: retries run for minutes (see notifyCompletion)
		}
		// The typed causes let the cron start path tell a credential failure and a cap reached
		// in a race from an ordinary one; the body the caller sees is unchanged.
		return apiErrorCause(http.StatusServiceUnavailable, startFailureCause(err), "%s", err.Error())
	}
	clusterJobCreated(ctx, a.hub, a.dbPool, sessionID, a.startWatchEvery())
	return nil
}

// startBoardSessionOnDaemon is the desktop driver for POST /api/runner/start
// (R9): with no cluster runtime configured, the board's agent runs on a
// connected daemon instead of failing. Without it, "Board session", card spawn
// and "Run board" — the flow the README leads with — 503 on every desktop
// install.
//
// By default these sessions run in the local sandbox (the same container the
// launch sheet's "Local sandbox" card uses) when the daemon has its image, and
// agent-kind on the daemon HOST, unsandboxed as the developer, when it does
// not or when the caller asks for runtime:"daemon" — see resolveDaemonRuntime.
//
// This path has no dangerously_skip_permissions field, so no start through it
// ever asks to bypass prompts; note separately that an agent-kind session runs
// non-interactively and its engine bypasses them anyway, on either runtime.
// The posture row records the runtime the session actually got.
//
// req.GitURL is ignored here: unlike a cluster Job, which clones into a fresh
// workspace, the daemon works from its own repos root.
func (a *API) startBoardSessionOnDaemon(ctx context.Context, req runnerStartRequest, principal runnerPrincipal, sessionID string) *APIError {
	var dc *DaemonConn
	if req.NoRepo {
		// No folder to look for: any connected daemon will do. The session
		// gets a new scratch folder there, created (never cloned) by the
		// daemon exactly like a new folder.
		if req.DaemonID != "" {
			// An in-process pin (a cron's daemon_id): that daemon or none, never another one.
			if dc = a.hub.GetDaemon(req.DaemonID); dc == nil {
				return apiErrorCause(http.StatusServiceUnavailable, errDaemonUnavailable, "the pinned daemon is not connected")
			}
		} else {
			dc = a.hub.DaemonForScratch(req.Runtime)
		}
		if dc == nil {
			return apiErrorCause(http.StatusServiceUnavailable, errDaemonUnavailable, "no daemon connected — connect one, or use the cluster runtime")
		}
		req.Repo = scratch.NewName()
	} else {
		dc = a.hub.DaemonForRepo(req.Repo)
	}
	if dc == nil {
		return apiErrorf(http.StatusServiceUnavailable,
			"no connected daemon has repo %q — check it out under the daemon's repos root, or connect a daemon", req.Repo)
	}
	runtime := resolveDaemonRuntime(req.Runtime, dc.SandboxAvailable())
	// A grant is attached or the start is refused, before anything is recorded: never on the
	// bare host, and only on a daemon that reports mcp_gateway (mcpstart.go).
	if req.Grant != nil {
		if apiErr := a.checkGrant(req.Grant, grantTarget{Runtime: runtime, Engine: req.Engine, Kind: "agent", Daemon: dc}); apiErr != nil {
			return apiErr
		}
	} else if req.CronID != "" {
		// A cron session with no connections is still restricted: the daemon must be able to.
		if apiErr := checkRestrictTarget(runtime, dc); apiErr != nil {
			return apiErr
		}
	}
	// A daemon session runs on the developer's own workstation with that
	// developer's credentials, so there is no per-session credential to fetch
	// and nothing for a token id to unlock. The attribution is still recorded
	// — who asked for this session is worth knowing wherever it ran, and the
	// record must not depend on which runtime happened to take it.
	sessionToken := a.spawnSessionToken(ctx, sessionID, dc, req.Repo, req.Title, req.Model,
		sessionOriginFor(principal.spawningAccountID(), req.Grant != nil || req.CronID != "", req.CronID))
	// The v1 contract has no dangerously_skip_permissions field, so no start
	// through it ever asks to bypass the engine's prompts — on either runtime.
	skipPerms := false
	if a.dbPool != nil {
		// From the verified credential, never the body. The static runner key
		// has no account behind it, so those rows stay NULL as before.
		if account := principal.spawningAccountID(); account != "" {
			if err := db.SetSessionSpawningAccount(ctx, a.dbPool, sessionID, account); err != nil {
				log.Printf("runner start: SetSessionSpawningAccount %s: %v", sessionID, err)
			}
		}
		if tokenID := principal.tokenID(); tokenID != "" {
			if err := db.SetSessionTokenID(ctx, a.dbPool, sessionID, tokenID); err != nil {
				log.Printf("runner start: SetSessionTokenID %s: %v", sessionID, err)
			}
			a.recordStartedBy(ctx, sessionID, principal, req.CronID)
		}
		if err := db.SetSessionKind(ctx, a.dbPool, sessionID, "agent"); err != nil {
			log.Printf("runner start: SetSessionKind %s: %v", sessionID, err)
		}
		recordLaunchEffort(ctx, a.dbPool, sessionID, req.Effort)
		if err := db.SetSessionPosture(ctx, a.dbPool, sessionID, runtime, skipPerms); err != nil {
			log.Printf("runner start: SetSessionPosture %s: %v", sessionID, err)
		}
		if err := db.SetSessionEngine(ctx, a.dbPool, sessionID, req.Engine); err != nil {
			log.Printf("runner start: SetSessionEngine %s: %v", sessionID, err)
		}
		if req.CallbackURL != "" || req.CallbackSecret != "" {
			if err := db.SetSessionCallback(ctx, a.dbPool, sessionID, req.CallbackURL, req.CallbackSecret); err != nil {
				log.Printf("runner start: SetSessionCallback %s: %v", sessionID, err)
			}
		}
		if req.AutoStop {
			if err := db.SetSessionAutoStop(ctx, a.dbPool, sessionID, true); err != nil {
				log.Printf("runner start: SetSessionAutoStop %s: %v", sessionID, err)
			}
		}
		recordInteraction(ctx, a.dbPool, sessionID, req.Interaction)
	}
	// A cron's session is marked (cron_id) and made private, after its account is recorded.
	if apiErr := a.markCronSession(ctx, req, sessionID); apiErr != nil {
		abortSpawnSessionToken(ctx, a.dbPool, sessionID)
		return apiErr
	}
	// blerg-board's contract for the env it injects (see handleSpawnBoardSession):
	// these two key names are what identify the board this session answers to.
	boardID := req.Env["BLERG_BOARD_BOARD"]
	boardToken := req.Env["BLERG_BOARD_TOKEN"]
	// The spawning account is recorded above; the session is made private after it, and the
	// raw tokens travel only in this spawn message.
	var gateway *protocol.MCPGatewayConfig
	if req.Grant != nil {
		var apiErr *APIError
		if gateway, apiErr = a.attachGrant(ctx, sessionID, req.Grant); apiErr != nil {
			a.abortGrantSession(ctx, sessionID)
			return apiErr
		}
	}
	restricted := req.Grant != nil || req.CronID != ""
	// Always-on plugins for a board-started Claude session on a daemon, as for a launch-sheet one.
	plugins, pluginNote := a.daemonPlugins(ctx, dc, principal.spawningAccountID(), principal.proof(), "agent", req.Engine, restricted)
	raw, err := json.Marshal(protocol.SpawnSession{ //nolint:gosec // the spawn message must carry the session token (and any MCP gateway grant) to the daemon over the authenticated websocket; never logged
		MCPGateway: gateway,
		Plugins:    plugins,
		// Every cron session and every grant session runs restricted (mcpstart.go).
		RestrictTools: req.Grant != nil || req.CronID != "",
		// Unattended unless the caller said a person is reading (interaction.go).
		Interaction: req.Interaction,
		Type:        "spawn_session", SessionID: sessionID, Repo: req.Repo, Title: req.Title,
		// DaemonForRepo picked a daemon that already has the folder, so no
		// clone is needed; the provider lets it refuse a checkout whose
		// origin is a different provider's repository.
		Provider: req.Provider,
		// A scratch folder is created like a new folder (NewRepo keeps that
		// true on a daemon older than NoRepo).
		NoRepo: req.NoRepo, NewRepo: req.NoRepo,
		Cols: 80, Rows: 24, InitialPrompt: req.Prompt, Model: req.Model, Effort: req.Effort, Kind: "agent",
		Engine:                     req.Engine,
		DangerouslySkipPermissions: skipPerms,
		// The one flag that decides host vs container on the daemon — the same
		// field POST /api/sessions sets for an agent+docker spawn.
		Sandbox:      runtime == runnerRuntimeDocker,
		SessionToken: sessionToken,
		// The board's own env (BLERG_BOARD_URL/TOKEN/BOARD) is what the card
		// agent reads — the same contract the cluster Job honours — and the
		// board linkage ALSO travels in the typed BoardID/BoardToken fields so
		// it is not lost on paths that drop ExtraEnv.
		BoardID:    boardID,
		BoardToken: boardToken,
		ExtraEnv:   req.Env,
	})
	if err != nil {
		abortSpawnSessionToken(ctx, a.dbPool, sessionID)
		return apiErrorf(http.StatusInternalServerError, "marshal error")
	}
	select {
	case dc.send <- raw:
	default:
		abortSpawnSessionToken(ctx, a.dbPool, sessionID)
		return apiErrorCause(http.StatusServiceUnavailable, errDaemonUnavailable, "daemon send buffer full — retry")
	}
	announceDaemonAgentStart(ctx, a.hub, a.dbPool, sessionID, dc.Name, runtime == runnerRuntimeDocker, pluginStage(plugins, pluginNote))
	// Record ownership now, not when the daemon's session_started arrives:
	// the board polls status and may message/stop immediately, and those all
	// resolve the session through the hub.
	a.hub.SetSessionOwner(sessionID, dc.ID)
	return nil
}

// resolveDaemonRuntime decides which of the two daemon-hosted runtimes a start
// that reached the daemon path actually runs on.
//
// An explicit choice is honoured verbatim, including runtime:"docker" against a
// daemon with no sandbox image — the daemon answers that with its own
// session_error, which is a truer failure than silently demoting the caller
// onto the bare host.
//
// With no choice at all the answer is the sandbox when the chosen daemon
// reports it can run one (its blerg-runner-sandbox image is present), and the
// daemon host otherwise — the same default the launch sheet applies. The
// container joins the blerg-sandbox network, which reaches the board and the
// runner, and carries the board's env and the messaging CLI, so a
// board-started session works there; it has no git credentials, so its agent
// can commit but not push. A caller that needs the host says
// `runtime: "daemon"` (the board: BLERG_BOARD_RUNNER_RUNTIME=daemon).
func resolveDaemonRuntime(requested string, sandboxAvailable bool) string {
	switch requested {
	case runnerRuntimeDocker:
		return runnerRuntimeDocker
	case runnerRuntimeDaemon:
		return daemonRuntimeName
	}
	if sandboxAvailable {
		return runnerRuntimeDocker
	}
	return daemonRuntimeName
}

// HandleRunnerStatus reports the three-axis status.
func (a *API) HandleRunnerStatus(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authRunner(w, r)
	if !ok {
		return
	}
	body, apiErr := a.SessionStatus(r.Context(), principal, r.PathValue("id"))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// SessionStatus is the body of GET /api/runner/sessions/{id}: the three-axis
// status, shared with the MCP get_session tool.
func (a *API) SessionStatus(ctx context.Context, principal RunnerPrincipal, sessionID string) (map[string]any, *APIError) {
	row, apiErr := a.requireSessionAccess(ctx, principal, sessionID)
	if apiErr != nil {
		return nil, apiErr
	}
	runtime := a.runnerRuntimeAxis(sessionID) //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
	// The documented five-value enum, not the raw column: runnerLifecycle
	// (result.go) is shared with the result endpoint so the two can never
	// disagree about what state a session is in. It also carries the rule that
	// a finished Job whose session never got past "starting" is a startup
	// failure (bad repo, image pull, crash before the daemon connected)
	// rather than a session still booting.
	lifecycle := runnerLifecycle(row.Status, runtime == "job_finished")
	// Why it failed, not just that it did (Task 6): the daemon's one-shot
	// error broadcast is long gone by the time the board polls, so a
	// lifecycle of "error" with no reason is the "spawn black hole" the
	// trial hit. Cleared by any non-error transition.
	errorReason := ""
	if row.ErrorReason != nil {
		errorReason = *row.ErrorReason
	}
	// Why it ended and what kind of actor ended it — never which account:
	// the contract answers brokers, not people.
	endReason, endedBy := endAttribution(row, "")
	return map[string]any{
		"lifecycle": lifecycle,
		"runtime":   runtime,
		"resumable": lifecycle == "disconnected" && a.hub.JobManager() != nil,
		// Which mode this session is in, so a caller polling a one-shot
		// session knows the runner will end it rather than waiting for a stop
		// that is never coming.
		"auto_stop": row.AutoStop,
		// Whether a person is reading the session's chat as it works ("interactive") or
		// nobody is ("unattended"): what the engine was told at start.
		"interaction":  effectiveInteraction(row),
		"error_reason": errorReason,
		"end_reason":   endReason,
		"ended_by":     endedBy,
	}, nil
}

// HandleRunnerMessage delivers a conversational turn; a disconnected cluster
// session is resumed by it (same path as the browser's agent_user_message).
func (a *API) HandleRunnerMessage(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authRunner(w, r)
	if !ok {
		return
	}
	var req struct {
		Text   string `json:"text"`
		Source string `json:"source"` // "human" (composer) | "blerg-board" (automation); default "runner"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "text required")
		return
	}
	body, apiErr := a.SendMessage(r.Context(), principal, r.PathValue("id"), req.Text, req.Source)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusAccepted, body)
}

// SendMessage delivers one conversational turn to a session, resuming a
// disconnected cluster session if that is what it takes. Shared with the MCP
// send_message tool. source "" defaults to "runner".
func (a *API) SendMessage(ctx context.Context, principal RunnerPrincipal, sessionID, text, source string) (map[string]any, *APIError) {
	if text == "" {
		return nil, apiErrorf(http.StatusBadRequest, "text required")
	}
	if source == "" {
		source = "runner"
	}
	row, apiErr := a.requireSessionAccess(ctx, principal, sessionID)
	if apiErr != nil {
		return nil, apiErr
	}
	if dc := a.hub.FindDaemonForSession(sessionID); dc != nil {
		raw, err := json.Marshal(protocol.AgentUserMessage{
			Type: "agent_user_message", SessionID: sessionID, Text: text, Source: source,
		})
		if err != nil {
			return nil, apiErrorf(http.StatusInternalServerError, "marshal error")
		}
		select {
		case dc.send <- raw:
			return map[string]any{"ok": true}, nil
		default:
			// Unlike the WS path, the runner caller gets a real error instead
			// of a silent drop.
			return nil, apiErrorf(http.StatusServiceUnavailable, "daemon send buffer full — retry")
		}
	}
	// No daemon is connected for this session. Only a disconnected cluster session can be
	// resumed by a message; for anything else the message has nowhere to go, and answering
	// "ok" (as this used to) told the caller it had been delivered when it had been dropped.
	awaited, err := db.SessionAwaitsDaemon(ctx, a.dbPool, sessionID)
	if err != nil {
		return nil, apiErrorf(http.StatusInternalServerError, "session lookup failed")
	}
	if apiErr := undeliverable(row, awaited); apiErr != nil {
		return nil, apiErr
	}
	// The resume carries the OWNER of this call, so an agent token resuming its
	// own session re-mints that account's personal credentials rather than
	// falling back to the shared operator Secret. The static runner key has no
	// account behind it and so resumes on the operator Secret, as before;
	// resumeClusterSession does the equality check against the launcher.
	//
	// A session holding MCP connections is resumed only by the account that started it, from
	// a signed-in browser session (its new gateway tokens carry that login as their proof):
	// say so here, where the caller can be told, not only in the resume itself.
	if _, problem := grantResumeProblem(ctx, a.dbPool, sessionID, derefOrEmpty(row.SpawningAccountID),
		principal.spawningAccountID(), principal.proof().SessionID); problem != nil {
		return nil, problem
	}
	resumeClusterSession(ctx, a.hub, a.dbPool, sessionID, text, principal.spawningAccountID(), principal.proof().SessionID)
	return map[string]any{"ok": true, "resumed": true}, nil
}

// undeliverable is why a message cannot reach a session no daemon is connected for, or nil when
// it is a disconnected cluster session (which a message resumes).
//
//   - A session that is live, or was marked lost when its daemon's connection dropped
//     (awaitsDaemon: a desktop daemon restarting, a laptop asleep), is waiting for its host:
//     503 with Retry-After, because it clears when the daemon is back.
//   - Any other session has ended — stopped, finished, or failed for a reason of its own (a
//     spawn the daemon refused) — and takes no message: 409. "Retry" would never succeed.
func undeliverable(row *db.SessionRow, awaitsDaemon bool) *APIError {
	if row.Status == "disconnected" {
		return nil
	}
	if terminalSessionStatus(row.Status) && (row.Status != "error" || !awaitsDaemon) {
		return apiErrorf(http.StatusConflict, "session has ended")
	}
	e := apiErrorf(http.StatusServiceUnavailable, "the session's host is not connected — retry shortly")
	e.RetryAfter = 5
	return e
}

// HandleRunnerStop ends a session for good: delete the Job (pod and all)
// and mark the session ended. Interrupt cancels a turn; Stop frees the
// cluster session slot — finished cards must call this or slots leak.
func (a *API) HandleRunnerStop(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authRunner(w, r)
	if !ok {
		return
	}
	body, apiErr := a.Stop(r.Context(), principal, r.PathValue("id"))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// Stop ends a session for good — kill the daemon session or delete the Job,
// mark the row ended, fire the completion callback, revoke the session's
// tokens. Shared with the MCP stop_session tool. Beyond "you may not stop this
// session", it reports no failure: a stop is best-effort against whatever is
// still alive.
func (a *API) Stop(ctx context.Context, principal RunnerPrincipal, sessionID string) (map[string]string, *APIError) {
	// A session nobody has heard of cannot be stopped, and a session that is
	// not this principal's is answered as if it did not exist.
	if _, apiErr := a.requireSessionAccess(ctx, principal, sessionID); apiErr != nil {
		return nil, apiErr
	}
	a.stopSession(ctx, sessionID, stopEndByRunner(principal))
	return map[string]string{"status": "stopped"}, nil
}

// stopSession is the body of a stop with no authorization in it: kill whatever
// is still running, mark the row ended, fire the completion callback, revoke
// the session's tokens. Shared with the one-shot auto-stop (autostop.go),
// which has no principal to check — the caller's authority was settled when
// they started the session with auto_stop.
//
// end is why, and by whom: recorded with the "ended" status (first write wins,
// so an auto-stop's claim, which already recorded auto_stopped, keeps it).
func (a *API) stopSession(ctx context.Context, sessionID string, end db.SessionEnd) {
	// Desktop (R9): a board-driven session lives in a daemon process, not a
	// Job. Marking the row "ended" without telling the daemon would leave the
	// agent running — and still holding the repo — so kill it first.
	if dc := a.hub.FindDaemonForSession(sessionID); dc != nil {
		if raw, err := json.Marshal(protocol.KillSession{Type: "kill_session", SessionID: sessionID}); err == nil {
			select {
			case dc.send <- raw:
			default:
				log.Printf("runner stop: daemon %s send buffer full, kill_session dropped for %s", dc.ID, sessionID)
			}
		}
	}
	if jm := a.hub.JobManager(); jm != nil {
		if err := jm.DeleteSessionJob(sessionID); err != nil { //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
			log.Printf("runner stop: delete job %s: %v", sessionID, err)
		}
	}
	now := time.Now()
	if a.dbPool != nil {
		if err := db.EndSessionStatus(ctx, a.dbPool, sessionID, "ended", &now, end); err != nil {
			log.Printf("runner stop: mark %s ended: %v", sessionID, err)
		} else {
			// Tell open browsers, reason included, rather than leaving the
			// session looking alive until the next reload.
			a.broadcastReconciledStatus(ctx, sessionID, "ended")
		}
	}
	// A stop is a completion: the broker that asked for it, or any other
	// holder of the callback, learns the outcome the same way as a natural end.
	a.notifyCompletion(sessionID) //nolint:contextcheck // webhook delivery outlives its caller by design: retries run for minutes (see notifyCompletion)
	// The session is over: revoke its messaging token rather than leaving a
	// live credential behind for a process that no longer exists.
	if a.dbPool != nil {
		if err := db.RevokeBoardTokensForSession(ctx, a.dbPool, sessionID); err != nil {
			log.Printf("runner stop: revoke session tokens %s: %v", sessionID, err)
		}
		revokeSessionGrants(ctx, a.dbPool, sessionID, "runner stop")
	}
}

// HandleRunnerInterrupt cancels the in-flight turn.
func (a *API) HandleRunnerInterrupt(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authRunner(w, r)
	if !ok {
		return
	}
	body, apiErr := a.Interrupt(r.Context(), principal, r.PathValue("id"))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusAccepted, body)
}

// Interrupt cancels a session's in-flight turn. Shared with the MCP
// interrupt_session tool. A session with nothing live to interrupt is a
// conflict, not a success — but a session this principal may not see is a 404
// first, so the conflict never becomes an existence oracle.
func (a *API) Interrupt(ctx context.Context, principal RunnerPrincipal, sessionID string) (map[string]bool, *APIError) {
	if _, apiErr := a.requireSessionAccess(ctx, principal, sessionID); apiErr != nil {
		return nil, apiErr
	}
	dc := a.hub.FindDaemonForSession(sessionID)
	if dc == nil {
		return nil, apiErrorf(http.StatusConflict, "no live runtime for this session")
	}
	raw, err := json.Marshal(protocol.InterruptSession{Type: "interrupt_session", SessionID: sessionID})
	if err != nil {
		return nil, apiErrorf(http.StatusInternalServerError, "marshal error")
	}
	select {
	case dc.send <- raw:
		return map[string]bool{"ok": true}, nil
	default:
		return nil, apiErrorf(http.StatusServiceUnavailable, "daemon send buffer full — retry")
	}
}

// HandleRunnerEvents serves the transcript window with a real envelope —
// the plain agent-events REST route returns a bare array (has_more is
// WS-only there), which the runner contract cannot use.
func (a *API) HandleRunnerEvents(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authRunner(w, r)
	if !ok {
		return
	}
	afterSeq, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var body map[string]any
	var apiErr *APIError
	// before_seq pages backwards (the older part of a transcript the live stream opened from its
	// end); it is the chat's loadOlder and has no MCP counterpart, so it is a sibling of Events
	// rather than a mode of it.
	if raw := r.URL.Query().Get("before_seq"); raw != "" {
		beforeSeq, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || beforeSeq < 0 {
			writeError(w, http.StatusBadRequest, "before_seq must be a non-negative integer")
			return
		}
		body, apiErr = a.EventsBefore(r.Context(), principal, r.PathValue("id"), beforeSeq, limit)
	} else {
		body, apiErr = a.Events(r.Context(), principal, r.PathValue("id"), afterSeq, limit)
	}
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// EventsBefore is the transcript window just before beforeSeq (0 = the end of the transcript),
// oldest first, at most limit events (<=0 or >400 means 200): `{events, has_more:false,
// has_older, first_seq, server_time}` — the same page the websocket's before_seq replay sends,
// for a client paging backwards from the live stream's opening tail.
func (a *API) EventsBefore(ctx context.Context, principal RunnerPrincipal, sessionID string, beforeSeq int64, limit int) (map[string]any, *APIError) {
	if _, apiErr := a.requireSessionAccess(ctx, principal, sessionID); apiErr != nil {
		return nil, apiErr
	}
	if limit <= 0 || limit > 400 {
		limit = 200
	}
	rows, more, err := db.ListAgentEventsBefore(ctx, a.dbPool, sessionID, beforeSeq, limit)
	if err != nil {
		return nil, apiErrorf(http.StatusInternalServerError, "event query failed")
	}
	events := make([]map[string]any, 0, len(rows))
	for _, ev := range rows {
		events = append(events, map[string]any{
			"seq":     ev.Seq,
			"ts":      ev.Ts.UTC().Format("2006-01-02T15:04:05.000Z"),
			"kind":    ev.Kind,
			"payload": json.RawMessage(ev.Payload),
		})
	}
	var firstSeq int64
	if len(rows) > 0 {
		firstSeq = rows[0].Seq
	}
	return map[string]any{
		"events": events, "has_more": false, "has_older": more, "first_seq": firstSeq,
		"server_time": time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// Events is the transcript window: events after afterSeq, at most limit of
// them (<=0 or >400 means the default 200), plus whether more are waiting.
// Shared with the MCP get_events tool.
func (a *API) Events(ctx context.Context, principal RunnerPrincipal, sessionID string, afterSeq int64, limit int) (map[string]any, *APIError) {
	if _, apiErr := a.requireSessionAccess(ctx, principal, sessionID); apiErr != nil {
		return nil, apiErr
	}
	if limit <= 0 || limit > 400 {
		limit = 200
	}
	rows, err := db.ListAgentEvents(ctx, a.dbPool, sessionID, afterSeq, limit+1)
	if err != nil {
		return nil, apiErrorf(http.StatusInternalServerError, "event query failed")
	}
	hasMore := false
	if len(rows) > limit {
		hasMore = true
		rows = rows[:limit]
	}
	events := make([]map[string]any, 0, len(rows))
	for _, ev := range rows {
		events = append(events, map[string]any{
			"seq":     ev.Seq,
			"ts":      ev.Ts.UTC().Format("2006-01-02T15:04:05.000Z"),
			"kind":    ev.Kind,
			"payload": json.RawMessage(ev.Payload),
		})
	}
	return map[string]any{"events": events, "has_more": hasMore}, nil
}
