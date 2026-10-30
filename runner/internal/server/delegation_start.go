package server

// POST /api/sessions/{id}/children: a parent asks to start a child (spec §2, §3). The body is an
// allowlist; the server builds the start itself. Order: a standing rule, then a session grant
// (start now, 201), else a delegation request the owner decides on a card (202). Limits are
// counted and the start claimed under one advisory lock per account, so concurrent asks cannot
// pass a limit together.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/idempotency"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
)

const (
	delegationTitleDefaultRunes = 60
	childRuntimeMessage         = `runtime must be "docker" or "daemon"`
	childRuntimeMessageCluster  = `runtime must be "docker", "daemon" or "cluster"`
	childPromptRequired         = "prompt is required"
	childPromptTooLarge         = "prompt is too large"
	childPluginsSkipped         = "skipped"
)

// childStartReply is the 201 answer: the child as started and how it was allowed.
type childStartReply struct {
	Child   childStartChild `json:"child"`
	Via     string          `json:"via"`
	Plugins string          `json:"plugins"`
	Budget  *budgetEngine   `json:"budget,omitempty"`
}

type childStartChild struct {
	ID          string `json:"id"`
	Engine      string `json:"engine"`
	Model       string `json:"model"`
	Effort      string `json:"effort"`
	Runtime     string `json:"runtime"`
	Repo        string `json:"repo"`
	NoRepo      bool   `json:"no_repo"`
	Title       string `json:"title"`
	KeepRunning bool   `json:"keep_running"`
	// Boards are the board levels passed down to the child.
	Boards []childBoard `json:"boards,omitempty"`
}

// delegationRequestReply is the 202 (new or replayed request) and the 200 (same body pending).
type delegationRequestReply struct {
	RequestID      string        `json:"request_id"`
	Status         string        `json:"status"`
	Choice         string        `json:"choice"`
	ChildSessionID string        `json:"child_session_id"`
	StartError     string        `json:"start_error"`
	ExpiresAt      time.Time     `json:"expires_at"`
	Offered        []string      `json:"offered"`
	Budget         *budgetEngine `json:"budget,omitempty"`
}

// storedChildRequest is delegation_requests.request: the allowlisted start with the FULL prompt and
// the budget line at the time of asking. Task 5 starts from it (decoding it as a childStart).
type storedChildRequest struct {
	childStart
	Budget *budgetEngine `json:"budget,omitempty"`
}

// delegationRequestPayload is the delegation_request event in the parent's transcript.
type delegationRequestPayload struct {
	Members []protocol.WorkspaceMember `json:"members,omitempty"`

	RequestID       string        `json:"request_id"`
	Engine          string        `json:"engine"`
	Model           string        `json:"model,omitempty"`
	Effort          string        `json:"effort,omitempty"`
	Repo            string        `json:"repo,omitempty"`
	NoRepo          bool          `json:"no_repo,omitempty"`
	Runtime         string        `json:"runtime"`
	DaemonName      string        `json:"daemon_name"` // the card's "Always allow …" target: the daemon's name, or "the cluster"
	Title           string        `json:"title"`
	Prompt          string        `json:"prompt"` // first delegationPromptExcerpt runes
	PromptTruncated bool          `json:"prompt_truncated,omitempty"`
	Budget          *budgetEngine `json:"budget,omitempty"`
	// Boards are the board levels the child would get ("with Product · Full" on the card).
	Boards    []childBoard `json:"boards,omitempty"`
	Offered   []string     `json:"offered"`
	ExpiresAt time.Time    `json:"expires_at"`
}

// parseChildStart decodes and checks the allowlisted body against where the parent's children run
// (host: its daemon, or the cluster), and returns the host this child runs on: the cluster for a
// desktop parent's runtime "cluster" child (host.forRuntime), else host. Any key outside
// delegationAllowedFields is refused before anything else is read.
func parseChildStart(raw []byte, host childHost) (childStart, childHost, *APIError) {
	fail := func(e *APIError) (childStart, childHost, *APIError) { return childStart{}, host, e }
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fail(apiErrorf(http.StatusBadRequest, "the body must be a JSON object"))
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !delegationAllowedFields[k] {
			return fail(apiErrorf(http.StatusUnprocessableEntity, "field %q is not accepted here", k))
		}
	}
	var cs childStart
	if err := json.Unmarshal(raw, &cs); err != nil {
		return fail(apiErrorf(http.StatusUnprocessableEntity, "a field has the wrong type"))
	}
	if strings.TrimSpace(cs.Prompt) == "" {
		return fail(apiErrorf(http.StatusUnprocessableEntity, "%s", childPromptRequired))
	}
	if len(cs.Prompt) > maxDelegationPromptBytes {
		return fail(apiErrorf(http.StatusRequestEntityTooLarge, "%s", childPromptTooLarge))
	}
	cs.Title = cleanSessionTitle(cs.Title)
	if utf8.RuneCountInString(cs.Title) > maxDelegationTitleRunes {
		return fail(apiErrorf(http.StatusUnprocessableEntity, "title is longer than %d characters", maxDelegationTitleRunes))
	}
	if cs.Title == "" {
		cs.Title = defaultChildTitle(cs.Prompt)
	}
	// A desktop parent's child asks for the cluster explicitly; it is never the default there.
	if !host.cluster && cs.Runtime == runnerRuntimeCluster {
		if !host.clusterOn {
			return fail(apiErrorf(http.StatusUnprocessableEntity, "%s", msgNoClusterRuntime))
		}
		host = host.forRuntime(runnerRuntimeCluster)
	}
	dc := host.dc
	switch {
	case host.workspace != nil:
		if host.workspace.Runtime == "cluster" {
			if apiErr := selectClusterChild(&cs, *host.workspace); apiErr != nil {
				return fail(apiErr)
			}
		}
		if apiErr := selectWorkspaceChild(&cs, *host.workspace); apiErr != nil {
			return fail(apiErr)
		}
	case len(cs.Repos) > 0 || cs.AllRepos:
		return fail(apiErrorf(422, "repository member selection requires a workspace parent"))
	case cs.NoRepo:
		if msg := noRepoProblem(cs.Repo, "", "", false, false); msg != "" {
			return fail(apiErrorf(http.StatusUnprocessableEntity, "%s", msg))
		}
	default:
		if cs.Repo == "" {
			return fail(apiErrorf(http.StatusUnprocessableEntity, "repo is required"))
		}
		if err := validateRepoName(cs.Repo); err != nil {
			return fail(apiErrorf(http.StatusUnprocessableEntity,
				"repo must be a folder name or org/name (one slash at most, no '..', no leading dot)"))
		}
		// The pinned daemon skips DaemonForRepo's folder check; a start for a repo it lacks
		// would answer "started" and fail later. A cluster child clones its repo, as a cluster
		// launch does, and a launch refuses a folder name the cluster cannot resolve.
		if !host.cluster && !containsString(dc.CheckedOutRepos(), cs.Repo) {
			return fail(apiErrorf(http.StatusUnprocessableEntity, "the parent's daemon has no repo %s", cs.Repo))
		}
		if host.cluster {
			repo, provider, problem := resolveClusterRepoName(cs.Repo, cs.Provider, host.repoSource, host.bareRepo)
			if problem == msgClusterBareRepo {
				problem = msgClusterChildBareRepo
			}
			if problem != "" {
				return fail(apiErrorf(http.StatusUnprocessableEntity, "%s", problem))
			}
			cs.Repo, cs.Provider = repo, provider
		}
	}
	if host.cluster {
		// A cluster parent's children run on the cluster; its own runtime is the default.
		switch cs.Runtime {
		case "", runnerRuntimeCluster:
			cs.Runtime = runnerRuntimeCluster
		default:
			return fail(apiErrorf(http.StatusUnprocessableEntity, "%s", msgClusterChildRuntime))
		}
		cs.Engine = models.NormalizeEngine(cs.Engine)
		if d, ok := engines.Lookup(cs.Engine); !ok || !d.SupportsRuntime(runnerRuntimeCluster) {
			return fail(apiErrorf(http.StatusUnprocessableEntity, "the cluster cannot run %s", engineLabel(cs.Engine)))
		}
		return cs, host, nil
	}
	switch cs.Runtime {
	case "":
		cs.Runtime = resolveDaemonRuntime("", dc.SandboxAvailable())
	case runnerRuntimeDocker, runnerRuntimeDaemon:
	default:
		if host.clusterOn {
			return fail(apiErrorf(http.StatusUnprocessableEntity, "%s", childRuntimeMessageCluster))
		}
		return fail(apiErrorf(http.StatusUnprocessableEntity, "%s", childRuntimeMessage))
	}
	cs.Engine = models.NormalizeEngine(cs.Engine)
	if !hostMatchesSelection(dc, []HostSelection{{Kind: "agent", Engine: cs.Engine, Runtime: cs.Runtime}}) {
		return fail(apiErrorf(http.StatusUnprocessableEntity, "the parent's daemon cannot run %s in %s",
			engineLabel(cs.Engine), cs.Runtime))
	}
	return cs, host, nil
}

// msgSandboxCloneDaemon refuses any sandboxed session (launch sheet, board, cron or child) on a
// daemon that would mount the person's checkout, .git included, instead of a clone of its own.
const msgSandboxCloneDaemon = "update the daemon before starting Local sandbox sessions: this daemon would mount your checkout, .git included, into the sandbox"

// validateChildStart checks the model and effort against the engine and this daemon's own list; for
// a cluster child, against the cluster's catalog and its readiness for the engine (as delegate p).
func (a *API) validateChildStart(ctx context.Context, cs childStart, host childHost, p runnerPrincipal) *APIError {
	if host.cluster {
		if msg := a.modelEffortProblem(ctx, cs.Engine, cs.Model, cs.Effort, ""); msg != "" {
			return apiErrorf(http.StatusUnprocessableEntity, "%s", msg)
		}
		if msg := a.clusterEngineProblem(ctx, cs.Engine, p); msg != "" {
			return apiErrorf(http.StatusUnprocessableEntity, "%s", msg)
		}
		return nil
	}
	dc := host.dc
	if !dc.RecoveryReadyCapable {
		return apiErrorf(http.StatusConflict, "update the daemon before starting child sessions")
	}
	if cs.Runtime == runnerRuntimeDocker && !dc.CanSandboxClones() {
		return apiErrorf(http.StatusConflict, "%s", msgSandboxCloneDaemon)
	}
	if msg := a.modelEffortProblem(ctx, cs.Engine, cs.Model, cs.Effort, dc.ID); msg != "" {
		return apiErrorf(http.StatusUnprocessableEntity, "%s", msg)
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// defaultChildTitle is the prompt's first non-empty line, cut to delegationTitleDefaultRunes runes.
func defaultChildTitle(prompt string) string {
	line := ""
	for _, l := range strings.Split(prompt, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	return cutRunes(cleanSessionTitle(line), delegationTitleDefaultRunes)
}

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// engineLabel is an engine's display name ("Codex"), else its id.
func engineLabel(engine string) string {
	if d, ok := engines.Lookup(engine); ok && d.Name != "" {
		return d.Name
	}
	return engine
}

// trustDirect: the person launched this parent directly. Restricted-tool parents and every
// other parent are untrusted-input, and rules and grants never apply to them.
//
// It reads who started the session, never whether it is private: private is visibility (only the
// owner sees it), and a person's own launch that picked a board or MCP connection is private too
// (BLG-338). Every other private session carries its own mark: a cron its cron id, a grant start
// through the contract RestrictTools and its principal's started_by_kind (recordStartedBy, which
// fails the start rather than leave it unmarked), a child its parent.
func trustDirect(parent *db.SessionRow) bool {
	if parent == nil || isChildSession(parent) || parent.RestrictTools {
		return false
	}
	if parent.StartedByKind != "" && parent.StartedByKind != "human" {
		return false
	}
	return derefOrEmpty(parent.BoardID) == "" && derefOrEmpty(parent.CronID) == ""
}

// sandboxClusterChildrenEnv says whether a sandboxed (docker) desktop session's cluster children may
// be preapproved: "once" (the default: a card per start) or "any" (session grants and standing rules
// apply, as for an unsandboxed desktop session). A cluster pod holds the owner's engine credentials
// and git token, which a desktop sandbox child never gets, so the default asks each time.
const sandboxClusterChildrenEnv = "BLERG_RUNNER_SANDBOX_CLUSTER_CHILDREN"

const (
	sandboxClusterOnce = "once"
	sandboxClusterAny  = "any"
)

// msgSandboxedClusterChildren is the Cluster row's reason for a sandboxed desktop session under "once".
const msgSandboxedClusterChildren = "A sandboxed session asks you for each cluster child, because the pod gets your credentials."

// sandboxClusterChildren is the one reading of BLERG_RUNNER_SANDBOX_CLUSTER_CHILDREN: "any" when set
// to that, else "once" (unset, empty or any other value fails closed). Read per call, so a change
// needs only a restart and no other code reads the variable.
func sandboxClusterChildren() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(sandboxClusterChildrenEnv)), sandboxClusterAny) {
		return sandboxClusterAny
	}
	return sandboxClusterOnce
}

// confinementOK: a rule or grant may start this runtime for this parent without a card. A docker
// child always; a host child only for a parent that itself runs on the host (R2); a cluster child (a
// throwaway pod that holds the owner's credentials) for a cluster parent, an unsandboxed desktop
// parent, and a sandboxed one only under BLERG_RUNNER_SANDBOX_CLUSTER_CHILDREN=any. Everything that
// may skip the card follows from this: the card's offered choices, rule and grant matching, the
// decision's re-check, and the Permissions row (whose reason is confinementReason).
func confinementOK(parent *db.SessionRow, runtime string) bool {
	switch runtime {
	case runnerRuntimeDocker:
		return true
	case runnerRuntimeDaemon:
		return derefOrEmpty(parent.Runtime) == runnerRuntimeDaemon
	case runnerRuntimeCluster:
		if onClusterParent(parent) {
			return true
		}
		if !onDesktopDaemon(parent) {
			return false
		}
		// A legacy row with no runtime fails closed, as a sandboxed one.
		return derefOrEmpty(parent.Runtime) == runnerRuntimeDaemon || sandboxClusterChildren() == sandboxClusterAny
	}
	return false
}

// confinementReason is why confinementOK refuses runtime for this parent, for the Permissions row.
func confinementReason(runtime string) string {
	if runtime == runnerRuntimeCluster {
		return msgSandboxedClusterChildren
	}
	return "A sandboxed parent cannot preapprove unsandboxed children."
}

// standingRulesApply: the person's standing rules for runtime may start this parent's child. A rule
// is keyed on (account, engine, runtime, daemon), and a cluster rule is on the cluster's daemon, so
// one rule covers the cluster children of every direct session of the person: cluster sessions and
// (0.4.4) desktop sessions alike. This is the one place to narrow that (for example, to rules made
// for desktop sessions only, which would need the rule's origin recorded); confinementOK still
// gates it for sandboxed sessions.
func standingRulesApply(parent *db.SessionRow, runtime string) bool {
	return parent != nil && runtime != ""
}

// offeredChoices: "once" and "deny" always; "session" and "always" only for a direct parent and a
// runtime that does not widen its confinement.
func offeredChoices(parent *db.SessionRow, cs childStart) []string {
	if trustDirect(parent) && confinementOK(parent, cs.Runtime) {
		return []string{"once", "session", "always", "deny"}
	}
	return []string{"once", "deny"}
}

// matchDelegation: a standing rule first, then a session grant; "" when neither (or when the parent
// may not use either). Returns "rule:<id>" or "grant:<id>". A cluster child whose owner credentials
// nothing live authorizes (host.ownerLive false: no proof, or core refuses it) is never started by a
// rule or grant: the person is asked, and their decision carries a fresh proof.
func (a *API) matchDelegation(ctx context.Context, parent *db.SessionRow, host childHost, cs childStart) (string, error) {
	if !trustDirect(parent) || !confinementOK(parent, cs.Runtime) {
		return "", nil
	}
	if host.cluster && !host.ownerLive {
		return "", nil
	}
	var rules []db.DelegationRule
	if standingRulesApply(parent, cs.Runtime) {
		var err error
		rules, err = db.CandidateDelegationRules(ctx, a.dbPool, derefOrEmpty(parent.SpawningAccountID), cs.Engine, cs.Runtime, childDaemonID(parent, cs.Runtime))
		if err != nil {
			return "", err
		}
	}
	for _, r := range rules {
		if r.LocalOnly && !host.localEngine(cs.Engine) {
			continue
		}
		return "rule:" + r.ID, nil
	}
	g, err := db.FindDelegationGrant(ctx, a.dbPool, parent.ID, cs.Engine, cs.Runtime, childDaemonID(parent, cs.Runtime))
	if err != nil {
		return "", err
	}
	if g != nil {
		return "grant:" + g.ID, nil
	}
	return "", nil
}

// checkDelegationLimits refuses a start over a limit (429). Called under the account's lock. self is
// the allowed request whose child is being started (nil for a new ask): the counters already count
// it as a start, so it is not counted a second time.
func (a *API) checkDelegationLimits(ctx context.Context, parent *db.SessionRow, self *db.DelegationRequest) *APIError {
	account := derefOrEmpty(parent.SpawningAccountID)
	limitErr := func(retry int, format string, args ...any) *APIError {
		e := apiErrorf(http.StatusTooManyRequests, format, args...)
		e.RetryAfter = retry
		return e
	}
	dbErr := func(err error) *APIError {
		log.Printf("delegation limits %s: %v", parent.ID, err)
		return apiErrorf(http.StatusInternalServerError, "could not check the start limits")
	}
	n, err := db.CountRunningChildren(ctx, a.dbPool, parent.ID)
	if err != nil {
		return dbErr(err)
	}
	limits := a.effectiveChildLimits(ctx)
	if n >= limits.parallel {
		return limitErr(60, "this session already runs %d sessions at once (the limit is %d); stop one first", n, limits.parallel)
	}
	now := time.Now()
	since := now.Add(-time.Hour)
	exclude := ""
	if self != nil && self.ParentSessionID == parent.ID && self.ChildSessionID == "" {
		exclude = self.ID
	}
	n, freeingStart, err := db.ParentStartWindow(ctx, a.dbPool, parent.ID, since, exclude, limits.hourly)
	if err != nil {
		return dbErr(err)
	}
	if n >= limits.hourly {
		if freeingStart == nil {
			return dbErr(errors.New("missing start window timestamp"))
		}
		next := freeingStart.Add(time.Hour)
		retry := max(1, int(math.Ceil(next.Sub(now).Seconds())))
		return limitErr(retry, "this session started %d sessions in the last hour (the limit is %d); the next start is possible at %s", n, limits.hourly, next.UTC().Format("15:04:05 UTC"))
	}
	n, err = db.CountAccountRunningChildren(ctx, a.dbPool, account)
	if err != nil {
		return dbErr(err)
	}
	if n >= maxRunningChildrenPerAccount {
		return limitErr(60, "your sessions already run %d started sessions; stop one first", maxRunningChildrenPerAccount)
	}
	n, oldest, err := db.AccountStartsSince(ctx, a.dbPool, account, since)
	if err != nil {
		return dbErr(err)
	}
	if self != nil && self.AccountID == account && self.ChildSessionID == "" && !self.CreatedAt.Before(since) {
		n--
	}
	if n >= maxStartsPerAccountPerHour {
		retry := 60
		if oldest != nil {
			retry = int(math.Ceil(oldest.Add(time.Hour).Sub(now).Seconds()))
		}
		retry = max(1, min(retry, 3600))
		return limitErr(retry, "your sessions have started %d sessions in the last hour; try again later", maxStartsPerAccountPerHour)
	}
	return nil
}

// delegationLockWait bounds how long a start waits for another start of the same account.
const delegationLockWait = 30 * time.Second

// errDelegationBusy: the account's lock could not be had within delegationLockWait.
var errDelegationBusy = errors.New("delegation lock busy")

// delegationLocks is an in-process, context-aware mutex per account: a waiter holds no database
// connection while it waits.
type delegationLocks struct {
	mu sync.Mutex
	m  map[string]*delegationLock
}

type delegationLock struct {
	ch   chan struct{}
	refs int
}

func (l *delegationLocks) acquire(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*delegationLock{}
	}
	e := l.m[key]
	if e == nil {
		e = &delegationLock{ch: make(chan struct{}, 1)}
		l.m[key] = e
	}
	e.refs++
	l.mu.Unlock()
	drop := func() {
		l.mu.Lock()
		if e.refs--; e.refs == 0 {
			delete(l.m, key)
		}
		l.mu.Unlock()
	}
	select {
	case e.ch <- struct{}{}:
		return func() { <-e.ch; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

// withDelegationLock runs fn while holding the account's start lock, so count-and-insert (requests)
// and count-and-claim (inline starts) are serialized per account across every replica.
//
// Two layers, neither of which holds a pool connection while it waits:
//   - in this process, a per-account mutex (delegationLocks), taken before anything touches the
//     database, so at most one start per account per replica is past this point;
//   - across replicas, a session-level pg_advisory_lock on a connection of its OWN, opened outside
//     the pool (pgx.ConnectConfig), held for the whole section and dropped with that connection.
//
// fn runs its queries and the start on the pool as usual; since the lock connection is not a pool
// connection, a section waiting for a pool connection never waits on a connection another waiter
// holds. Both waits are bounded by delegationLockWait (errDelegationBusy).
func (a *API) withDelegationLock(ctx context.Context, account string, fn func() error) error {
	wctx, cancel := context.WithTimeout(ctx, delegationLockWait)
	defer cancel()
	release, err := a.delegLocks.acquire(wctx, account)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errDelegationBusy
	}
	defer release()
	conn, err := pgx.ConnectConfig(wctx, a.dbPool.Config().ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }() // closing also drops the lock
	if _, err := conn.Exec(wctx, `SELECT pg_advisory_lock(hashtext('delegation:' || $1))`, account); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if wctx.Err() != nil {
			return errDelegationBusy
		}
		return err
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext('delegation:' || $1))`, account); err != nil {
			log.Printf("delegation unlock: %v", err)
		}
	}()
	return fn()
}

// childRequestHash is the hash of the canonical (resolved) childStart.
func childRequestHash(cs childStart) string {
	cs.Boards = childBoardsHashKey(cs.Boards)
	raw, _ := json.Marshal(cs)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// childRunnerRequest is the contract request startChild builds; the replay check rebuilds it to
// compare the stored idempotency hash.
func childRunnerRequest(parent *db.SessionRow, cs childStart, via string) runnerStartRequest {
	repo := cs.Repo
	if cs.Workspace != nil {
		repo = "workspace"
		if cs.Workspace.Runtime == "cluster" {
			repo = ""
		}
	}
	var clusterRepos []string
	var clusterLaunch string
	if cs.Runtime == "cluster" {
		clusterRepos, clusterLaunch = cs.Repos, cs.WorkspaceLaunch
	}
	return runnerStartRequest{
		Workspace:      cs.Workspace,
		WorkspaceRepos: clusterRepos, WorkspaceLaunch: clusterLaunch,
		Engine: cs.Engine, Model: cs.Model, Effort: cs.Effort,
		Repo: repo, NoRepo: cs.NoRepo, Runtime: cs.Runtime, Provider: cs.Provider, resolvedRepoProvider: cs.Provider != "",
		Title: cs.Title, Prompt: workspacePrompt(cs.Prompt, cs.Workspace),
		Interaction:     protocol.InteractionUnattended,
		DaemonID:        childDaemonID(parent, cs.Runtime),
		ParentSessionID: parent.ID,
		ParentEpoch:     parent.DelegationEndedAt,
		DelegationVia:   via,
		KeepRunning:     cs.KeepRunning,
		ChildBoards:     cs.Boards,
	}
}

func childReply(id string, cs childStart, via string, budget *budgetEngine) childStartReply {
	return childStartReply{
		Child: childStartChild{ID: id, Engine: cs.Engine, Model: cs.Model, Effort: cs.Effort, Runtime: cs.Runtime,
			Repo: cs.Repo, NoRepo: cs.NoRepo, Title: cs.Title, KeepRunning: cs.KeepRunning, Boards: cs.Boards},
		Via: via, Plugins: childPluginsSkipped, Budget: budget,
	}
}

func requestReply(r db.DelegationRequest) delegationRequestReply {
	var stored storedChildRequest
	_ = json.Unmarshal(r.Request, &stored)
	offered := r.Offered
	if offered == nil {
		offered = []string{}
	}
	return delegationRequestReply{RequestID: r.ID, Status: r.Status, Choice: r.Choice, ChildSessionID: r.ChildSessionID,
		StartError: r.StartError, ExpiresAt: r.ExpiresAt, Offered: offered, Budget: stored.Budget}
}

// childOutcome is the answer the locked section decided on; it is written after the lock is
// released, so a slow client never holds the account's lock.
type childOutcome struct {
	status   int
	body     any
	replayed bool
	retry    int // Retry-After seconds, when positive
	created  *db.DelegationRequest
}

func outcomeError(e *APIError) childOutcome {
	return childOutcome{status: e.Status, body: map[string]string{"error": e.Message}, retry: e.RetryAfter}
}

func (o childOutcome) write(w http.ResponseWriter) {
	if o.replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	if o.retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(o.retry))
	}
	writeJSON(w, o.status, o.body)
}

// storedRequestOutcome answers a request found again (a replayed key): 201 with its child once one
// was started for it, else its state (202).
func storedRequestOutcome(r db.DelegationRequest, replayed bool) childOutcome {
	if r.Status == "allowed" && r.ChildSessionID != "" {
		var stored storedChildRequest
		_ = json.Unmarshal(r.Request, &stored)
		return childOutcome{status: http.StatusCreated, replayed: replayed,
			body: childReply(r.ChildSessionID, stored.childStart, "request:"+r.ID, stored.Budget)}
	}
	return childOutcome{status: http.StatusAccepted, replayed: replayed, body: requestReply(r)}
}

const (
	msgChildKeyDeleted = "the session started under this key was deleted; use a new key"
	msgDelegationBusy  = "another start for this account is in progress; retry"
	msgRequestNotShown = "the request could not be shown to the person; retry"
)

// HandlePostChildren is POST /api/sessions/{id}/children.
func (a *API) HandlePostChildren(w http.ResponseWriter, r *http.Request) {
	parent, dc, ok := a.delegateParent(w, r, delegateOpts{})
	if !ok {
		return
	}
	key, keyErr := requestIdempotencyKey(r, true)
	if keyErr != nil {
		writeAPIError(w, keyErr)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDelegationPromptBytes+8<<10))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, childPromptTooLarge)
			return
		}
		writeError(w, http.StatusBadRequest, "could not read the body")
		return
	}
	ctx := r.Context()
	parentHost := a.parentHost(parent, dc)
	if ws, err := db.GetSessionWorkspace(ctx, a.dbPool, parent.ID); err != nil {
		writeError(w, 409, "parent workspace unavailable")
		return
	} else if ws != nil {
		parentHost.workspace = ws.Manifest
	}
	cs, host, apiErr := parseChildStart(raw, parentHost)
	// A cluster child's delegate carries the owner's proof (delegateFor); a desktop child's none.
	delegate := a.delegateFor(ctx, parent, cs.Runtime, "")
	if apiErr == nil {
		// The name an agent typed becomes the catalog's routing id before anything is checked,
		// hashed or stored.
		cs.Model, apiErr = a.resolveChildModel(ctx, cs.Engine, cs.Model, host, delegate)
	}
	if apiErr == nil {
		apiErr = a.validateChildStart(ctx, cs, host, delegate)
	}
	if apiErr == nil {
		// Board levels passed down resolve to the parent's boards before the start is hashed.
		cs.Boards, apiErr = a.resolveChildBoards(ctx, parent, cs.Boards)
	}
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	if host.cluster {
		// Checked with core before the account lock: it is a network call.
		host.ownerLive = a.clusterOwnerLive(ctx, delegate)
		host.local = a.clusterLocalEngines(ctx, parent, delegate)
	}
	hash := childRequestHash(cs)
	now := time.Now()
	budget := a.budgetEngineForHost(ctx, parent, host, delegate, cs.Engine, now)
	account := derefOrEmpty(parent.SpawningAccountID)

	conflict := childOutcome{status: http.StatusConflict, body: map[string]string{"error": idempotencyConflictMessage}}
	var out childOutcome
	err = a.withDelegationLock(ctx, account, func() error {
		// Re-read under the lock: a parent that ended since delegateParent read it asks nothing.
		cur, err := db.GetSession(ctx, a.dbPool, parent.ID)
		if err != nil {
			return err
		}
		if cur == nil || cur.DiscardRequestedAt != nil || terminalSessionStatus(cur.Status) {
			out = outcomeError(apiErrorf(http.StatusConflict, "%s", msgDelegateEnded))
			return nil
		}
		parent = cur
		// Replay: a request stored under this key, or a child started under it.
		prev, err := db.GetDelegationRequestByKey(ctx, a.dbPool, parent.ID, key)
		if err != nil {
			return err
		}
		if prev != nil {
			if prev.RequestHash != hash {
				out = conflict
			} else {
				out = storedRequestOutcome(*prev, true)
			}
			return nil
		}
		idem, err := db.GetIdempotencyKey(ctx, a.dbPool, idempotency.Scope(sessionDelegateKind, parent.ID), key)
		if err != nil {
			return err
		}
		if idem != nil {
			child, err := db.GetSession(ctx, a.dbPool, idem.SessionID)
			if err != nil {
				return err
			}
			if child == nil {
				out = outcomeError(apiErrorf(http.StatusGone, "%s", msgChildKeyDeleted))
				return nil
			}
			want, err := startRequestHash(childRunnerRequest(parent, cs, child.DelegationVia))
			if err != nil {
				return err
			}
			if want != idem.RequestHash {
				out = conflict
				return nil
			}
			out = childOutcome{status: http.StatusCreated, replayed: true, body: childReply(idem.SessionID, cs, child.DelegationVia, budget)}
			return nil
		}
		// At most one pending request per identical body, whatever the key.
		pending, err := db.ListPendingDelegationRequests(ctx, a.dbPool, parent.ID)
		if err != nil {
			return err
		}
		for _, p := range pending {
			if p.RequestHash == hash {
				out = childOutcome{status: http.StatusOK, body: requestReply(p)}
				return nil
			}
		}
		if apiErr := a.checkDelegationLimits(ctx, parent, nil); apiErr != nil {
			out = outcomeError(apiErr)
			return nil
		}
		via, err := a.matchDelegation(ctx, parent, host, cs)
		if err != nil {
			return err
		}
		if via != "" {
			resp, apiErr := a.startChild(ctx, parent, cs, via, key)
			if apiErr != nil {
				out = outcomeError(apiErr)
				return nil
			}
			log.Printf("delegation: parent %s started child %s via %s", parent.ID, resp.SessionID, via)
			out = childOutcome{status: http.StatusCreated, body: childReply(resp.SessionID, cs, via, budget)}
			return nil
		}
		request, err := json.Marshal(storedChildRequest{childStart: cs, Budget: budget})
		if err != nil {
			return err
		}
		req, isNew, err := db.InsertDelegationRequest(ctx, a.dbPool, db.DelegationRequest{
			ParentSessionID: parent.ID, AccountID: account, DaemonID: childDaemonID(parent, cs.Runtime),
			IdempotencyKey: key, RequestHash: hash, Request: request,
			Offered: offeredChoices(parent, cs), ExpiresAt: now.Add(delegationRequestTTL),
		})
		if err != nil {
			return err
		}
		switch {
		case isNew:
			out = childOutcome{status: http.StatusAccepted, created: &req, body: requestReply(req)}
		case req.IdempotencyKey == key && req.RequestHash != hash:
			out = conflict // lost a race the lock should prevent; answer what is stored
		default:
			out = storedRequestOutcome(req, req.IdempotencyKey == key)
		}
		return nil
	})
	switch {
	case errors.Is(err, errDelegationBusy):
		out = outcomeError(&APIError{Status: http.StatusServiceUnavailable, Message: msgDelegationBusy, RetryAfter: 5})
	case err != nil:
		writeDatabaseFailure(w, "start the session", "delegation children", err)
		return
	}
	if out.created != nil {
		if err := a.announceDelegationRequest(context.WithoutCancel(ctx), *parent, host, cs, *out.created, budget); err != nil {
			// No card means nobody can decide it: withdraw the request rather than leave it pending.
			log.Printf("delegation: request %s not shown: %v", out.created.ID, err)
			if _, xerr := db.ExpireDelegationRequest(context.WithoutCancel(ctx), a.dbPool, out.created.ID, parent.ID); xerr != nil {
				log.Printf("delegation: expire unshown request %s: %v", out.created.ID, xerr)
			}
			out = outcomeError(&APIError{Status: http.StatusServiceUnavailable, Message: msgRequestNotShown, RetryAfter: 5})
		}
	}
	out.write(w)
}

// announceDelegationRequest writes the delegation_request event in the parent's transcript and
// pushes it (not for private or headless parents).
func (a *API) announceDelegationRequest(ctx context.Context, parent db.SessionRow, host childHost, cs childStart,
	req db.DelegationRequest, budget *budgetEngine) error {
	excerpt := cutRunes(cs.Prompt, delegationPromptExcerpt)
	daemonName := host.name()
	payload := delegationRequestPayload{
		Members:   childWorkspaceMembers(cs),
		RequestID: req.ID, Engine: cs.Engine, Model: cs.Model, Effort: cs.Effort, Repo: cs.Repo, NoRepo: cs.NoRepo,
		Runtime: cs.Runtime, DaemonName: daemonName, Title: cs.Title, Prompt: excerpt,
		PromptTruncated: excerpt != cs.Prompt, Budget: budget, Boards: cs.Boards, Offered: req.Offered, ExpiresAt: req.ExpiresAt,
	}
	if err := appendServerAgentEvent(ctx, a.hub, a.dbPool, parent.ID, delegationEventID(req.ID, "delegation_request"), "delegation_request", payload); err != nil {
		return err
	}
	log.Printf("delegation: parent %s asked for request %s", parent.ID, req.ID)
	a.pushForDelegation(parent, req, engineLabel(cs.Engine)) //nolint:contextcheck // push delivery is best-effort background work with no tie to this request
	return nil
}

// delegationEventID is the stable client event id of a request's event of this kind
// (agent_events.client_event_id is a uuid): a uuid v5 of the request id and the kind, so a retry
// never records it twice. Task 5's delegation_decided uses the same function.
func delegationEventID(requestID, kind string) string {
	return protocol.SessionMessageEventID("delegation:"+requestID, kind)
}

// pushForDelegation pushes a new request like an ask (pushForMessage's rules): never for a private
// parent (subscriptions belong to no account) nor a headless one (nobody attends it).
func (a *API) pushForDelegation(parent db.SessionRow, req db.DelegationRequest, engineLabel string) {
	if parent.Private || headlessSession(&parent) {
		return
	}
	var stored storedChildRequest
	_ = json.Unmarshal(req.Request, &stored)
	heading := sessionLabel(derefOrEmpty(parent.Title), parent.Repo, parent.ID) + " — wants to start a session"
	body := fmt.Sprintf("Start a %s session: %s", engineLabel, stored.Title)
	link := "/sessions/" + parent.ID
	if a.delegationPush != nil {
		a.delegationPush(heading, body, link)
		return
	}
	SendPush(a.dbPool, heading, body, link)
}
