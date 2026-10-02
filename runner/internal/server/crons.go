package server

// The crons API (spec 7.7). Every route is for a signed-in person only: Kind "human" with an
// account and a live login session id. The runner's browser gate (authBrowser) accepts any core
// token holding session.start, agent tokens included, so this file adds the stricter check on
// top of it and every handler goes through cronsHandler.person.
//
// A cron's identity is a core agent-token row created with the creator's login session as proof
// (POST /internal/tokens/mint). Its id is kept in crons.token_id and never leaves this package:
// no response body carries it, only its expiry.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/cron"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

const (
	// cronTokenDays is the lifetime of a cron's token: the most core allows (spec 7.4). The UI
	// shows the expiry and offers renew.
	cronTokenDays = 365
	// cronPausedByOwner is the paused_reason of a pause the owner asked for.
	cronPausedByOwner = "paused by you"

	cronMaxBody      = 256 << 10
	cronRunsDefault  = 50
	cronRunsMax      = 200
	cronRunsOpenScan = 200 // recent runs looked at for open (claimed or held) ones
	cronRollbackTime = 10 * time.Second
	cronTokenNameMax = 64 // core's limit on a token name
)

var (
	// errCronMintCap is core refusing a mint because the account holds its maximum of live agent tokens.
	errCronMintCap = errors.New("too many active agent tokens")
	// errCronMintProof is core not accepting the login session as a live proof (or the account).
	errCronMintProof = errors.New("the sign-in behind this request is no longer valid")

	cronUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// ---- core: token mint -----------------------------------------------------------------------

// CronMinter creates a cron's token at core (POST /internal/tokens/mint).
type CronMinter interface {
	// MintToken creates a cron-kind token for accountID on the proof of a live login session and
	// returns its id and expiry. errCronMintCap and errCronMintProof are the two typed refusals.
	MintToken(ctx context.Context, accountID, sessionID, name string, days int) (string, time.Time, error)
}

// HTTPCronMinter is CronMinter over POST /internal/tokens/mint.
type HTTPCronMinter struct {
	BaseURL     string
	InternalKey string
	HTTP        *http.Client // nil: a client with a 10 second timeout
}

// MintToken implements CronMinter.
func (m *HTTPCronMinter) MintToken(ctx context.Context, accountID, sessionID, name string, days int) (string, time.Time, error) {
	if m.BaseURL == "" || m.InternalKey == "" {
		return "", time.Time{}, errors.New("core is not configured")
	}
	raw, err := json.Marshal(map[string]any{
		"account_id": accountID, "session_id": sessionID, "name": name, "expires_in_days": days,
	})
	if err != nil {
		return "", time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.BaseURL, "/")+"/internal/tokens/mint", bytes.NewReader(raw))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", m.InternalKey)
	resp, err := coreHTTPClient(m.HTTP).Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("core unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			ID        string `json:"id"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
			return "", time.Time{}, fmt.Errorf("decode core response: %w", err)
		}
		exp, err := time.Parse(time.RFC3339, out.ExpiresAt)
		if err != nil || out.ID == "" {
			return "", time.Time{}, errors.New("core returned an unusable token")
		}
		return out.ID, exp, nil
	case http.StatusConflict:
		return "", time.Time{}, errCronMintCap
	case http.StatusNotFound:
		if !isCoreNotFound(resp) {
			return "", time.Time{}, errors.New("core has no token mint route: is it up to date?")
		}
		return "", time.Time{}, errCronMintProof
	default:
		return "", time.Time{}, fmt.Errorf("core returned %d", resp.StatusCode)
	}
}

// ---- handler ----------------------------------------------------------------------------------

type cronsHandler struct {
	api    *API
	minter CronMinter
	svc    *CronService
	// sched is a scheduler used only for its management calls (create, update, run now); it never
	// ticks, so it never takes the scheduling lock. Nil when crons are not enabled.
	sched *cron.Scheduler
}

func newCronsHandler(a *API, minter CronMinter) *cronsHandler {
	h := &cronsHandler{api: a, minter: minter, svc: a.Cron()}
	if h.svc != nil && a.dbPool != nil {
		s, err := cron.New(cron.Config{Pool: a.dbPool, Starter: h.svc, Sessions: h.svc, OnPaused: h.svc.OnPaused})
		if err != nil {
			log.Printf("crons api: %v", err)
		} else {
			h.sched = s
		}
	}
	return h
}

// person authenticates the caller as a signed-in person. The order is deliberate: a missing or
// bad token is 401 whatever is configured, an agent token is 403, a person without a login
// session is 401 (the mint and the connection list need that session), and only then does a
// server without crons answer 503.
func (h *cronsHandler) person(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	p, ok := h.api.authBrowser(w, r)
	if !ok {
		return p, false
	}
	switch {
	case p.Kind != "human":
		writeError(w, http.StatusForbidden, "crons can only be managed by a signed-in person, not by an agent token or the runner key")
		return p, false
	case p.Sub == "" || p.Sid == "":
		writeError(w, http.StatusUnauthorized, "your sign-in is no longer valid for crons: sign in again and retry")
		return p, false
	case h.sched == nil || h.svc == nil:
		writeError(w, http.StatusServiceUnavailable, "crons are not enabled on this server (they need the runner database and blerg-core)")
		return p, false
	}
	return p, true
}

// owned loads the cron named by the path, or writes the uniform 404 (missing, malformed and
// another account's look the same).
func (h *cronsHandler) owned(w http.ResponseWriter, r *http.Request, p identity.Principal) (*db.Cron, bool) {
	id := r.PathValue("id")
	if !cronUUID.MatchString(id) {
		writeError(w, http.StatusNotFound, "cron not found")
		return nil, false
	}
	c, err := db.GetOwnedCron(r.Context(), h.api.dbPool, p.Sub, id)
	if err != nil {
		cronErrorResponse(w, err)
		return nil, false
	}
	return c, true
}

// cronErrorResponse maps an engine or database error to a response.
func cronErrorResponse(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrCronNotFound):
		writeError(w, http.StatusNotFound, "cron not found")
	case errors.Is(err, db.ErrCronLimit):
		writeError(w, http.StatusConflict, fmt.Sprintf("you already have the maximum of %d crons: delete one first", cron.DefaultMaxCronsPerAccount))
	case errors.Is(err, cron.ErrAlreadyRunning):
		writeError(w, http.StatusConflict, "the cron's previous run is still active")
	case errors.Is(err, cron.ErrRunOpen):
		writeError(w, http.StatusConflict, fmt.Sprintf(errRunClaimedFmt, "now"))
	case errors.Is(err, cron.ErrConflict):
		writeError(w, http.StatusConflict, "the cron changed while this request was being handled (it was renewed, paused or resumed): reload and try again")
	case errors.Is(err, cron.ErrInvalid):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), cron.ErrInvalid.Error()+": "))
	default:
		log.Printf("crons api: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func decodeCronBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, cronMaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return false
	}
	return true
}

// rollbackContext outlives a cancelled request so a rollback still reaches core.
func rollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cronRollbackTime)
}

// ---- views ----------------------------------------------------------------------------------

type cronRunView struct {
	ID           string     `json:"id"`
	CronID       string     `json:"cron_id"`
	ScheduledFor time.Time  `json:"scheduled_for"`
	ClaimedAt    time.Time  `json:"claimed_at"`
	StartedAt    *time.Time `json:"started_at"`
	SessionID    *string    `json:"session_id"`
	Status       string     `json:"status"`
	Reason       *string    `json:"reason"`
	Late         bool       `json:"late"`
	Manual       bool       `json:"manual"`
}

func runView(r db.CronRun) cronRunView {
	return cronRunView{ID: r.ID, CronID: r.CronID, ScheduledFor: r.ScheduledFor, ClaimedAt: r.ClaimedAt, StartedAt: r.StartedAt,
		SessionID: r.SessionID, Status: r.Status, Reason: r.Reason, Late: r.Late, Manual: r.Manual}
}

// cronView is a cron as the UI sees it. It has no token id: only when the token expires.
type cronView struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	Status              string          `json:"status"` // active | disabled | paused | expired
	Enabled             bool            `json:"enabled"`
	Schedule            string          `json:"schedule"`
	Timezone            string          `json:"timezone"`
	Prompt              string          `json:"prompt"`
	Engine              string          `json:"engine"`
	Model               *string         `json:"model"`
	Effort              *string         `json:"effort"`
	Runtime             string          `json:"runtime"`
	DaemonID            *string         `json:"daemon_id"`
	BoardID             *string         `json:"board_id"`
	MCP                 json.RawMessage `json:"mcp"`
	TokenExpiresAt      time.Time       `json:"token_expires_at"`
	GraceSeconds        int             `json:"grace_seconds"`
	MaxRuntimeSeconds   int             `json:"max_runtime_seconds"`
	NextRunAt           time.Time       `json:"next_run_at"`
	LastRunAt           *time.Time      `json:"last_run_at"`
	ConsecutiveFailures int             `json:"consecutive_failures"`
	PausedReason        *string         `json:"paused_reason"`
	LastRun             *cronRunView    `json:"last_run"`
	ConnectionProblems  []string        `json:"connection_problems"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

func (h *cronsHandler) view(ctx context.Context, c *db.Cron) cronView {
	status := "active"
	switch {
	case c.PausedReason != nil:
		status = "paused"
	case !c.TokenExpiresAt.After(time.Now()):
		status = "expired"
	case !c.Enabled:
		status = "disabled"
	}
	v := cronView{
		ID: c.ID, Name: c.Name, Status: status, Enabled: c.Enabled, Schedule: c.Schedule, Timezone: c.Timezone, Prompt: c.Prompt, Engine: c.Engine,
		Model: c.Model, Effort: c.Effort, Runtime: c.Runtime, DaemonID: c.DaemonID, BoardID: c.BoardID, MCP: c.MCP,
		TokenExpiresAt: c.TokenExpiresAt, GraceSeconds: c.GraceSeconds, MaxRuntimeSeconds: c.MaxRuntimeSeconds,
		NextRunAt: c.NextRunAt, LastRunAt: c.LastRunAt, ConsecutiveFailures: c.ConsecutiveFailures, PausedReason: c.PausedReason,
		ConnectionProblems: h.svc.MissingConnections(c.ID), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if len(v.MCP) == 0 {
		v.MCP = json.RawMessage("[]")
	}
	if v.ConnectionProblems == nil {
		v.ConnectionProblems = []string{}
	}
	if runs, err := db.ListCronRuns(ctx, h.api.dbPool, c.ID, 1); err == nil && len(runs) > 0 {
		rv := runView(runs[0])
		v.LastRun = &rv
	}
	return v
}

// ---- validation -------------------------------------------------------------------------------

// checkMCP validates a cron's `mcp` value exactly as a session start does (resolveGrantProof: the
// connections are the caller's own, every tool's hash matches the live listing) and additionally
// requires every entry to name its tools with an explicit mode: nothing is taken from a
// connection's default_tools. It returns the normalised JSON to store. `propose` is accepted and
// stored as is (proposals are not delivered until they exist).
func (h *cronsHandler) checkMCP(ctx context.Context, p identity.Principal, raw json.RawMessage) (json.RawMessage, *APIError) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("[]"), nil
	}
	var sel []MCPSelection
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sel); err != nil {
		return nil, apiErrorf(http.StatusBadRequest, "mcp must be a list of {connection, tools}: %v", err)
	}
	if len(sel) == 0 {
		return json.RawMessage("[]"), nil
	}
	for _, s := range sel {
		if s.Connection == "" {
			return nil, apiErrorf(http.StatusBadRequest, "every MCP entry needs a connection")
		}
		if len(s.Tools) == 0 {
			return nil, apiErrorf(http.StatusBadRequest,
				"connection %q selects no tools: a cron must name each tool and its mode explicitly", s.Connection)
		}
	}
	// A cron runs in a cluster pod or the Docker sandbox on an agent session: judge the selection
	// as a cluster agent session (the daemon and runtime are chosen at fire time).
	proof := mcpgw.Proof{AccountID: p.Sub, SessionID: p.Sid}
	if _, apiErr := h.api.resolveGrantProof(ctx, proof, sel, grantTarget{Runtime: runnerRuntimeCluster, Engine: "claude", Kind: "agent"}); apiErr != nil {
		return nil, apiErr
	}
	out, err := json.Marshal(sel)
	if err != nil {
		return nil, apiErrorf(http.StatusInternalServerError, "internal error")
	}
	return out, nil
}

// sameMCP reports whether two `mcp` values mean the same thing: equal as JSON, whatever the key
// order or whitespace, and null, empty and [] all being "no connections".
func sameMCP(a, b json.RawMessage) bool {
	norm := func(raw json.RawMessage) any {
		raw = bytes.TrimSpace(raw)
		var v any
		if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &v) != nil {
			return []any{}
		}
		if arr, ok := v.([]any); ok && len(arr) == 0 {
			return []any{}
		}
		return v
	}
	return reflect.DeepEqual(norm(a), norm(b))
}

// mint asks core for a token on the caller's login session, mapping its refusals to responses.
func (h *cronsHandler) mint(w http.ResponseWriter, r *http.Request, p identity.Principal, cronName string) (string, time.Time, bool) {
	name := "cron: " + cronName
	if utf8.RuneCountInString(name) > cronTokenNameMax {
		name = string([]rune(name)[:cronTokenNameMax])
	}
	id, exp, err := h.minter.MintToken(r.Context(), p.Sub, p.Sid, name, cronTokenDays)
	switch {
	case errors.Is(err, errCronMintCap):
		writeError(w, http.StatusConflict, "you have reached the limit of 50 active agent tokens, and every cron holds one: revoke an unused token in settings first")
	case errors.Is(err, errCronMintProof):
		writeError(w, http.StatusUnauthorized, "your sign-in is no longer valid for crons: sign in again and retry")
	case err != nil:
		log.Printf("crons api: mint token: %v", err)
		writeError(w, http.StatusBadGateway, "could not create the cron's access token at blerg-core")
	default:
		return id, exp, true
	}
	return "", time.Time{}, false
}

// revokeFresh rolls back a token no cron ended up holding.
func (h *cronsHandler) revokeFresh(ctx context.Context, account, tokenID string) {
	rctx, cancel := rollbackContext(ctx)
	defer cancel()
	if err := h.svc.revokeOwed(rctx, account, tokenID); err != nil {
		log.Printf("crons api: rolling back token %s: %v", tokenID, err)
	}
}

// openRunProblem explains why a token swap must wait: a run that is `claimed` is being started
// (or waits for the reaper) with the current token as its proof. A run that is merely held or
// started does not block a swap: nothing was written under the token for the first, and the
// second is answered by the running-session check. The idempotency scope is per cron, so the swap
// itself cannot make a run start twice. "" means none. This is a courtesy check that spares a
// pointless mint; the swap itself is a compare-and-set (cron.CronPatch.RefuseWhileClaimed).
func (h *cronsHandler) openRunProblem(ctx context.Context, cronID string) (string, error) {
	runs, err := db.ListCronRuns(ctx, h.api.dbPool, cronID, cronRunsOpenScan)
	if err != nil {
		return "", err
	}
	for _, r := range runs {
		if r.Status == db.CronRunClaimed {
			return fmt.Sprintf(errRunClaimedFmt, r.ScheduledFor.UTC().Format("2006-01-02 15:04 UTC")), nil
		}
	}
	return "", nil
}

const errRunClaimedFmt = "a run of this cron is being started (slot %s) with its current access token: try again in a minute"

// ---- routes -----------------------------------------------------------------------------------

func (h *cronsHandler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := h.person(w, r)
	if !ok {
		return
	}
	rows, err := db.ListCrons(r.Context(), h.api.dbPool, p.Sub)
	if err != nil {
		cronErrorResponse(w, err)
		return
	}
	out := make([]cronView, 0, len(rows))
	for i := range rows {
		out = append(out, h.view(r.Context(), &rows[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"crons": out})
}

func (h *cronsHandler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := h.person(w, r)
	if !ok {
		return
	}
	c, ok := h.owned(w, r, p)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.view(r.Context(), c))
}

type cronCreateRequest struct {
	Name     string          `json:"name"`
	Schedule string          `json:"schedule"`
	Timezone string          `json:"timezone"`
	Prompt   string          `json:"prompt"`
	Engine   string          `json:"engine"`
	Model    *string         `json:"model"`
	Effort   *string         `json:"effort"`
	Runtime  string          `json:"runtime"`
	DaemonID *string         `json:"daemon_id"`
	BoardID  *string         `json:"board_id"`
	MCP      json.RawMessage `json:"mcp"`
	// GraceSeconds is a pointer so an explicit 0 (no catch-up window) is told from an absent field,
	// which takes the default.
	GraceSeconds      *int `json:"grace_seconds"`
	MaxRuntimeSeconds int  `json:"max_runtime_seconds"`
}

func (h *cronsHandler) create(w http.ResponseWriter, r *http.Request) {
	p, ok := h.person(w, r)
	if !ok {
		return
	}
	var req cronCreateRequest
	if !decodeCronBody(w, r, &req) {
		return
	}
	if req.Engine != "" && req.Engine != "claude" {
		writeError(w, http.StatusBadRequest, `crons run with the "claude" engine only`)
		return
	}
	ctx := r.Context()
	// Everything that can be judged without side effects comes before the mint, so a bad request
	// never creates (and has to revoke) a token.
	sch, err := cron.ParseSchedule(req.Schedule, req.Timezone)
	if err == nil {
		err = sch.Validate(time.Now())
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mcp, apiErr := h.checkMCP(ctx, p, req.MCP)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	if existing, err := db.ListCrons(ctx, h.api.dbPool, p.Sub); err != nil {
		cronErrorResponse(w, err)
		return
	} else if len(existing) >= cron.DefaultMaxCronsPerAccount {
		cronErrorResponse(w, db.ErrCronLimit)
		return
	}
	tokenID, exp, ok := h.mint(w, r, p, req.Name)
	if !ok {
		return
	}
	row, err := h.sched.CreateCron(ctx, cron.NewCron{
		Owner: p.Sub, Name: req.Name, Enabled: true, Schedule: req.Schedule, Timezone: req.Timezone, Prompt: req.Prompt,
		Model: req.Model, Effort: req.Effort, Runtime: req.Runtime, DaemonID: req.DaemonID, BoardID: req.BoardID, MCP: mcp,
		TokenID: tokenID, TokenExpiresAt: exp, GraceSeconds: req.GraceSeconds, MaxRuntimeSeconds: req.MaxRuntimeSeconds,
	})
	if err != nil {
		h.revokeFresh(ctx, p.Sub, tokenID)
		cronErrorResponse(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, h.view(ctx, row))
}

type cronPatchRequest struct {
	Name              *string          `json:"name"`
	Enabled           *bool            `json:"enabled"`
	Schedule          *string          `json:"schedule"`
	Timezone          *string          `json:"timezone"`
	Prompt            *string          `json:"prompt"`
	Engine            *string          `json:"engine"`
	Model             *string          `json:"model"`
	Effort            *string          `json:"effort"`
	Runtime           *string          `json:"runtime"`
	DaemonID          *string          `json:"daemon_id"`
	BoardID           *string          `json:"board_id"`
	MCP               *json.RawMessage `json:"mcp"`
	GraceSeconds      *int             `json:"grace_seconds"`
	MaxRuntimeSeconds *int             `json:"max_runtime_seconds"`
}

func (h *cronsHandler) patch(w http.ResponseWriter, r *http.Request) {
	p, ok := h.person(w, r)
	if !ok {
		return
	}
	c, ok := h.owned(w, r, p)
	if !ok {
		return
	}
	var req cronPatchRequest
	if !decodeCronBody(w, r, &req) {
		return
	}
	if req.Engine != nil && *req.Engine != "claude" {
		writeError(w, http.StatusBadRequest, `crons run with the "claude" engine only`)
		return
	}
	patch := cron.CronPatch{
		Name: req.Name, Enabled: req.Enabled, Schedule: req.Schedule, Timezone: req.Timezone, Prompt: req.Prompt, Model: req.Model, Effort: req.Effort,
		Runtime: req.Runtime, DaemonID: req.DaemonID, BoardID: req.BoardID, GraceSeconds: req.GraceSeconds, MaxRuntimeSeconds: req.MaxRuntimeSeconds,
	}
	// A PATCH that leaves `mcp` as it is (the form sends it back unchanged, or not at all) is not
	// judged against the live upstreams: a cron whose connection is gone, needs signing in or is
	// down must stay editable for a prompt or schedule change. Only a real change is validated.
	if req.MCP != nil && !sameMCP(*req.MCP, c.MCP) {
		mcp, apiErr := h.checkMCP(r.Context(), p, *req.MCP)
		if apiErr != nil {
			writeAPIError(w, apiErr)
			return
		}
		patch.MCP = &mcp
	}
	row, err := h.sched.UpdateCron(r.Context(), p.Sub, c.ID, patch)
	if err != nil {
		cronErrorResponse(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.view(r.Context(), row))
}

func (h *cronsHandler) remove(w http.ResponseWriter, r *http.Request) {
	p, ok := h.person(w, r)
	if !ok {
		return
	}
	c, ok := h.owned(w, r, p)
	if !ok {
		return
	}
	ctx := r.Context()
	// Revoke first and stop if core cannot do it: a cron that is gone but whose token lives on
	// would be an identity nobody can see or delete.
	if err := h.svc.RevokeCronToken(ctx, c); err != nil {
		log.Printf("crons api: delete %s: revoke token: %v", c.ID, err)
		writeError(w, http.StatusBadGateway, "could not revoke the cron's access token at blerg-core; the cron was not deleted")
		return
	}
	deleted, err := h.sched.DeleteCron(ctx, p.Sub, c.ID)
	if err != nil {
		cronErrorResponse(w, err)
		return
	}
	// DeleteCron recorded the deleted row's CURRENT token as owed, under the row lock. A renewal can
	// land between the read above and the delete, so that token may not be the one revoked first:
	// revoke it now (kept and retried if core cannot). When it is the same token the record is
	// simply cleared.
	if deleted.TokenID != c.TokenID {
		if err := h.svc.revokeOwed(ctx, deleted.OwnerAccountID, deleted.TokenID); err != nil {
			log.Printf("crons api: delete %s: revoke renewed token (kept and retried): %v", deleted.ID, err)
		}
	} else if err := db.DeleteTokenRevocation(ctx, h.api.dbPool, deleted.TokenID); err != nil {
		log.Printf("crons api: delete %s: clear revocation record: %v", deleted.ID, err)
	}
	if _, err := h.svc.StopCronSessions(ctx, deleted.ID); err != nil {
		log.Printf("crons api: delete %s: stop sessions: %v", deleted.ID, err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *cronsHandler) runNow(w http.ResponseWriter, r *http.Request) {
	p, ok := h.person(w, r)
	if !ok {
		return
	}
	c, ok := h.owned(w, r, p)
	if !ok {
		return
	}
	run, err := h.sched.RunNow(r.Context(), p.Sub, c.ID)
	switch {
	case errors.Is(err, cron.ErrInvalid):
		// Paused or an expired token: the state is what is wrong, not the request.
		writeError(w, http.StatusConflict, strings.TrimPrefix(err.Error(), cron.ErrInvalid.Error()+": "))
		return
	case err != nil:
		cronErrorResponse(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, runView(*run))
}

func (h *cronsHandler) pause(w http.ResponseWriter, r *http.Request) {
	p, ok := h.person(w, r)
	if !ok {
		return
	}
	c, ok := h.owned(w, r, p)
	if !ok {
		return
	}
	ctx := r.Context()
	changed, err := h.sched.PauseCron(ctx, p.Sub, c.ID, cronPausedByOwner)
	if err != nil {
		cronErrorResponse(w, err)
		return
	}
	row, err := db.GetOwnedCron(ctx, h.api.dbPool, p.Sub, c.ID)
	if err != nil {
		cronErrorResponse(w, err)
		return
	}
	if changed {
		// Pausing revokes the cron's token and stops what it is running (spec 7.6). It is the
		// token the paused row holds NOW (a renewal may have swapped it since c was read; a renewal
		// of a paused cron is refused, so this one is final).
		h.svc.OnPaused(ctx, row, cronPausedByOwner)
	}
	writeJSON(w, http.StatusOK, h.view(ctx, row))
}

// resume clears a pause. Every pause revoked the cron's token, so resuming mints a new one on the
// caller's login session, then resumes and schedules the next slot from now.
func (h *cronsHandler) resume(w http.ResponseWriter, r *http.Request) {
	p, ok := h.person(w, r)
	if !ok {
		return
	}
	c, ok := h.owned(w, r, p)
	if !ok {
		return
	}
	ctx := r.Context()
	if c.PausedReason == nil {
		writeError(w, http.StatusConflict, "the cron is not paused")
		return
	}
	if msg, err := h.openRunProblem(ctx, c.ID); err != nil {
		cronErrorResponse(w, err)
		return
	} else if msg != "" {
		writeError(w, http.StatusConflict, msg)
		return
	}
	tokenID, exp, ok := h.mint(w, r, p, c.Name)
	if !ok {
		return
	}
	// A compare-and-set on the token this request read, refused while a run is claimed, under the
	// cron's row lock (the scheduler's claim takes it too).
	row, err := h.sched.UpdateCron(ctx, p.Sub, c.ID, cron.CronPatch{
		TokenID: &tokenID, TokenExpiresAt: &exp, Resume: true, ExpectTokenID: c.TokenID, RefuseWhileClaimed: true,
	})
	if err != nil {
		h.revokeFresh(ctx, p.Sub, tokenID)
		cronErrorResponse(w, err)
		return
	}
	// The pause revoked the old token; make sure of it (idempotent, and kept until core says so).
	if err := h.svc.revokeOwed(ctx, c.OwnerAccountID, c.TokenID); err != nil {
		log.Printf("crons api: resume %s: revoke old token (kept and retried): %v", c.ID, err)
	}
	writeJSON(w, http.StatusOK, h.view(ctx, row))
}

// renew mints a replacement token and swaps it in. It waits while a run is open or running: an
// open run's idempotency scope and a running session's proof both name the current token.
func (h *cronsHandler) renew(w http.ResponseWriter, r *http.Request) {
	p, ok := h.person(w, r)
	if !ok {
		return
	}
	c, ok := h.owned(w, r, p)
	if !ok {
		return
	}
	ctx := r.Context()
	if c.PausedReason != nil {
		writeError(w, http.StatusConflict, "the cron is paused: resume it instead, which issues a fresh access token")
		return
	}
	if msg, err := h.openRunProblem(ctx, c.ID); err != nil {
		cronErrorResponse(w, err)
		return
	} else if msg != "" {
		writeError(w, http.StatusConflict, msg)
		return
	}
	if active, err := db.ListActiveCronSessions(ctx, h.api.dbPool, c.ID); err != nil {
		cronErrorResponse(w, err)
		return
	} else if len(active) > 0 {
		writeError(w, http.StatusConflict, "a run of this cron is in progress and uses the current access token: renew when it has finished")
		return
	}
	tokenID, exp, ok := h.mint(w, r, p, c.Name)
	if !ok {
		return
	}
	row, err := h.sched.UpdateCron(ctx, p.Sub, c.ID, cron.CronPatch{
		TokenID: &tokenID, TokenExpiresAt: &exp, ExpectTokenID: c.TokenID, RefuseWhileClaimed: true, RequireActive: true,
	})
	if err != nil {
		h.revokeFresh(ctx, p.Sub, tokenID)
		cronErrorResponse(w, err)
		return
	}
	if err := h.svc.revokeOwed(ctx, c.OwnerAccountID, c.TokenID); err != nil {
		log.Printf("crons api: renew %s: revoke old token (kept and retried): %v", c.ID, err)
	}
	writeJSON(w, http.StatusOK, h.view(ctx, row))
}

func (h *cronsHandler) runs(w http.ResponseWriter, r *http.Request) {
	p, ok := h.person(w, r)
	if !ok {
		return
	}
	c, ok := h.owned(w, r, p)
	if !ok {
		return
	}
	limit := cronRunsDefault
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = min(n, cronRunsMax)
	}
	rows, err := db.ListCronRuns(r.Context(), h.api.dbPool, c.ID, limit)
	if err != nil {
		cronErrorResponse(w, err)
		return
	}
	out := make([]cronRunView, 0, len(rows))
	for _, run := range rows {
		out = append(out, runView(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}
