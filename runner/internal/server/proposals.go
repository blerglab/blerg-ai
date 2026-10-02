package server

// MCP proposals (spec 9). A tool the person put in `propose` mode is not forwarded by the
// gateway: it freezes the call (mcpgw.DBProposalSink). This file is what a signed-in person does
// with those proposals: list them, read one, approve, reject, or say what happened to one whose
// outcome was unknown.
//
// Every route is human only (Kind "human" with a live login session id: the same judgement as the
// crons and the launch sheet), and owner only: another account's proposal, a missing one and a
// malformed id all answer the same 404. The routes authenticate with a bearer token, which a
// browser does not attach on its own, so they carry no CSRF check of their own, like the other
// runner state-changing routes.
//
// Approval. The approver's own login session is the proof for the credential, so a proposal
// cannot be approved after the person signed out. The order is fixed:
//
//  1. read the proposal (owner only); it must be pending;
//  2. snapshot checks, leaving the proposal pending when they fail: the connection must still
//     exist for this person with EXACTLY the recorded URL and be ready, and the tool's live
//     definition must still hash to the recorded tool_hash (a deleted connection also fails the
//     proposal, since it can never run);
//  3. compare-and-set pending -> executing (refused for a proposal past its expiry window even
//     when the sweeper has not expired it yet): one of any number of concurrent approvals wins, the
//     rest answer 409 and never reach the upstream. The CAS and every recording step run on a
//     context that outlives the request, so a client that disconnects cannot strand a row;
//  4. mcpgw.ExecuteApproved calls the upstream once with exactly the stored arguments, on a context
//     that outlives the HTTP request (a closed tab must not turn an action into an unknown one);
//  5. the outcome is recorded: done, failed (a definite refusal: a 4xx other than 408/429, a
//     request-level JSON-RPC error, a tool-reported error), or unknown (everything that may have
//     arrived after the side effect: 5xx, 408, 429, internal errors, unreadable replies, timeouts,
//     drops). An unknown outcome is never retried by anything; the owner decides with
//     POST .../resolve. When nothing was sent (credential, snapshot or tool-list errors, a
//     refused connect) the proposal goes back to pending instead, to be approved again.
//
// Nothing here logs or returns arguments, credentials or upstream addresses: the audit lines name
// the proposal, account, connection and tool only.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

const (
	proposalExpiry         = db.ProposalExpiry   // a pending proposal expires after this (spec 9)
	proposalRetention      = 30 * 24 * time.Hour // a decided proposal is pruned this long after its decision
	proposalExecutingStale = 15 * time.Minute    // an approval still executing after this lost its outcome
	proposalExecuteBudget  = 5 * time.Minute     // an approval's own bound, beyond the gateway's per-call timeout
	proposalDecidedPage    = 50                  // default size of a page of decided proposals
	proposalDecidedMax     = 100                 // the most a page of decided proposals may ask for
	proposalWriteBudget    = 10 * time.Second
	proposalMaintainEvery  = time.Hour
	proposalBodyLimit      = 4 << 10
)

const (
	msgProposalNotFound = "proposal not found"
	msgProposalHuman    = "proposals can only be decided by a signed-in person, not by an agent token or the runner key"
	msgProposalSignIn   = "your sign-in is no longer valid for proposals: sign in again and retry"
	msgProposalNoDB     = "proposals need the runner database"
	msgProposalNoGW     = "the MCP gateway is not configured on this server, so proposals cannot be run"
)

var proposalStates = map[string]bool{
	db.ProposalPending: true, db.ProposalExecuting: true, db.ProposalDone: true, db.ProposalRejected: true,
	db.ProposalExpired: true, db.ProposalFailed: true, db.ProposalUnknown: true,
}

// proposalExecutor is the gateway's execution of an approved proposal (mcpgw.Gateway.ExecuteApproved).
type proposalExecutor interface {
	ExecuteApproved(ctx context.Context, c mcpgw.ApprovedCall) (mcpgw.ApprovedResult, error)
}

// proposalView is one proposal as the browser sees it. The frozen arguments are the authoritative
// record (what an approval sends); agent_summary is a one-line convenience. The connection URL,
// the tool hash and every credential are deliberately absent.
type proposalView struct {
	ID             string          `json:"id"`
	State          string          `json:"state"`
	ConnectionID   string          `json:"connection_id"`
	ConnectionName string          `json:"connection_name"`
	Tool           string          `json:"tool"`
	Arguments      json.RawMessage `json:"arguments"`
	// ArgumentsRaw is the same frozen call as the exact stored jsonb text (what an approval sends),
	// as a string: the browser shows it without re-serialising, so number lexemes survive.
	ArgumentsRaw string          `json:"arguments_raw"`
	AgentSummary string          `json:"agent_summary"`
	SessionID    *string         `json:"session_id"`
	CronID       *string         `json:"cron_id"`
	CreatedAt    time.Time       `json:"created_at"`
	ExpiresAt    time.Time       `json:"expires_at"`
	DecidedAt    *time.Time      `json:"decided_at"`
	DecidedBy    *string         `json:"decided_by"`
	Result       json.RawMessage `json:"result"`
}

func viewOfProposal(p *db.Proposal) proposalView {
	v := proposalView{
		ID: p.ID, State: p.State, ConnectionID: p.ConnectionID, ConnectionName: p.ConnectionName, Tool: p.Tool,
		Arguments: p.Arguments, ArgumentsRaw: string(p.Arguments), AgentSummary: p.AgentSummary, CreatedAt: p.CreatedAt, ExpiresAt: p.CreatedAt.Add(proposalExpiry),
		DecidedAt: p.DecidedAt, DecidedBy: p.DecidedBy, Result: p.Result,
	}
	if p.SessionID != "" {
		v.SessionID = &p.SessionID
	}
	if p.CronID != "" {
		v.CronID = &p.CronID
	}
	if len(v.Result) == 0 {
		v.Result = json.RawMessage("null")
	}
	return v
}

type proposalsHandler struct{ api *API }

// person authenticates the caller as a signed-in person: a missing or bad token is 401, an agent
// token or the runner key 403, a person without a login session 401.
func (h *proposalsHandler) person(w http.ResponseWriter, r *http.Request) (grantRequester, bool) {
	p, ok := h.api.authBrowser(w, r)
	if !ok {
		return grantRequester{}, false
	}
	who := requesterOf(p)
	switch {
	case who.Kind != "human":
		writeError(w, http.StatusForbidden, msgProposalHuman)
		return who, false
	case who.Account == "" || who.Sid == "":
		writeError(w, http.StatusUnauthorized, msgProposalSignIn)
		return who, false
	case h.api.dbPool == nil:
		writeError(w, http.StatusServiceUnavailable, msgProposalNoDB)
		return who, false
	}
	return who, true
}

// owned loads the caller's proposal or writes the uniform 404.
func (h *proposalsHandler) owned(w http.ResponseWriter, r *http.Request, who grantRequester) (*db.Proposal, bool) {
	p, err := db.GetOwnedProposal(r.Context(), h.api.dbPool, who.Account, r.PathValue("id"))
	switch {
	case errors.Is(err, db.ErrProposalNotFound):
		writeError(w, http.StatusNotFound, msgProposalNotFound)
		return nil, false
	case err != nil:
		log.Printf("proposals: read: %v", err)
		writeError(w, http.StatusInternalServerError, "could not read the proposal")
		return nil, false
	}
	return p, true
}

// transitionError answers a transition that matched no row: not the caller's (404) or not in the
// required state (409).
func transitionError(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, db.ErrProposalNotFound):
		writeError(w, http.StatusNotFound, msgProposalNotFound)
	case errors.Is(err, db.ErrProposalState):
		writeError(w, http.StatusConflict, what)
	default:
		log.Printf("proposals: transition: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update the proposal")
	}
}

// openStates and decidedStates split the proposal states for the list scopes.
var (
	openStates    = map[string]bool{db.ProposalPending: true, db.ProposalExecuting: true, db.ProposalUnknown: true}
	decidedStates = map[string]bool{db.ProposalDone: true, db.ProposalRejected: true, db.ProposalExpired: true, db.ProposalFailed: true}
)

// list answers GET /api/proposals?scope=open|decided&state=&before=&limit=.
//
// scope=open (the default) returns every open proposal (pending, executing, unknown) unpaged,
// pending first. scope=decided returns done, rejected, expired and failed proposals newest first,
// one page at a time: decided_next is the cursor to pass as ?before= for the following page, or
// null on the last page. pending_count is always the account's pending total.
func (h *proposalsHandler) list(w http.ResponseWriter, r *http.Request) {
	who, ok := h.person(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	scope, state := q.Get("scope"), q.Get("state")
	if scope == "" {
		scope = "open"
	}
	states := openStates
	switch scope {
	case "open":
		if q.Get("before") != "" || q.Get("limit") != "" {
			writeError(w, http.StatusBadRequest, "before and limit apply only to scope=decided; the open list is not paged")
			return
		}
	case "decided":
		states = decidedStates
	default:
		writeError(w, http.StatusBadRequest, "unknown scope: use open or decided")
		return
	}
	if state != "" && !states[state] {
		if proposalStates[state] {
			writeError(w, http.StatusBadRequest, "state "+state+" is not in scope "+scope)
		} else {
			writeError(w, http.StatusBadRequest, "unknown state: use pending, executing, done, rejected, expired, failed or unknown")
		}
		return
	}
	limit := proposalDecidedPage
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = min(n, proposalDecidedMax)
	}
	var rows []db.Proposal
	var err error
	var next *string
	if scope == "open" {
		rows, err = db.ListOpenProposals(r.Context(), h.api.dbPool, who.Account, state)
	} else {
		var cursor string
		rows, cursor, err = db.ListDecidedProposals(r.Context(), h.api.dbPool, who.Account, state, q.Get("before"), limit)
		if cursor != "" {
			next = &cursor
		}
	}
	if errors.Is(err, db.ErrProposalCursor) {
		writeError(w, http.StatusBadRequest, "before is not a cursor this list returned")
		return
	}
	if err != nil {
		log.Printf("proposals: list: %v", err)
		writeError(w, http.StatusInternalServerError, "could not list proposals")
		return
	}
	pending, err := db.CountPendingProposals(r.Context(), h.api.dbPool, who.Account)
	if err != nil {
		log.Printf("proposals: count: %v", err)
		writeError(w, http.StatusInternalServerError, "could not list proposals")
		return
	}
	out := make([]proposalView, 0, len(rows))
	for i := range rows {
		out = append(out, viewOfProposal(&rows[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposals": out, "decided_next": next, "pending_count": pending})
}

// count answers GET /api/proposals/count with only {"pending_count": n}: one cheap query, for the
// navigation badge's polling.
func (h *proposalsHandler) count(w http.ResponseWriter, r *http.Request) {
	who, ok := h.person(w, r)
	if !ok {
		return
	}
	pending, err := db.CountPendingProposals(r.Context(), h.api.dbPool, who.Account)
	if err != nil {
		log.Printf("proposals: count: %v", err)
		writeError(w, http.StatusInternalServerError, "could not count proposals")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"pending_count": pending})
}

func (h *proposalsHandler) get(w http.ResponseWriter, r *http.Request) {
	who, ok := h.person(w, r)
	if !ok {
		return
	}
	p, ok := h.owned(w, r, who)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, viewOfProposal(p))
}

func (h *proposalsHandler) reject(w http.ResponseWriter, r *http.Request) {
	who, ok := h.person(w, r)
	if !ok {
		return
	}
	if _, ok := h.owned(w, r, who); !ok {
		return
	}
	p, err := db.RejectProposal(r.Context(), h.api.dbPool, who.Account, r.PathValue("id"), who.Account)
	if err != nil {
		transitionError(w, err, "this proposal is no longer pending")
		return
	}
	log.Printf("proposals: rejected id=%s account=%s connection=%s tool=%q", p.ID, p.AccountID, p.ConnectionID, p.Tool)
	writeJSON(w, http.StatusOK, viewOfProposal(p))
}

func (h *proposalsHandler) resolve(w http.ResponseWriter, r *http.Request) {
	who, ok := h.person(w, r)
	if !ok {
		return
	}
	if _, ok := h.owned(w, r, who); !ok {
		return
	}
	var body struct {
		Outcome string `json:"outcome"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, proposalBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || (body.Outcome != db.ProposalDone && body.Outcome != db.ProposalFailed) {
		writeError(w, http.StatusBadRequest, `the body must be {"outcome": "done"} or {"outcome": "failed"}`)
		return
	}
	p, err := db.ResolveProposal(r.Context(), h.api.dbPool, who.Account, r.PathValue("id"), body.Outcome, who.Account)
	if err != nil {
		transitionError(w, err, "only a proposal whose outcome is unknown can be resolved")
		return
	}
	log.Printf("proposals: resolved id=%s account=%s connection=%s tool=%q outcome=%s", p.ID, p.AccountID, p.ConnectionID, p.Tool, p.State)
	writeJSON(w, http.StatusOK, viewOfProposal(p))
}

func (h *proposalsHandler) approve(w http.ResponseWriter, r *http.Request) {
	who, ok := h.person(w, r)
	if !ok {
		return
	}
	p, ok := h.owned(w, r, who)
	if !ok {
		return
	}
	if p.State != db.ProposalPending {
		writeError(w, http.StatusConflict, "this proposal is no longer pending ("+p.State+")")
		return
	}
	if time.Since(p.CreatedAt) >= proposalExpiry {
		writeError(w, http.StatusConflict, "this proposal has expired (it is older than 7 days) and can no longer be approved: let the agent propose again")
		return
	}
	cfg := h.api.hub.mcpStart.get()
	var exec proposalExecutor
	if cfg != nil {
		exec, _ = cfg.Hasher.(proposalExecutor)
	}
	if cfg == nil || cfg.Core == nil || cfg.Hasher == nil || exec == nil {
		writeError(w, http.StatusServiceUnavailable, msgProposalNoGW)
		return
	}
	proof := mcpgw.Proof{AccountID: who.Account, SessionID: who.Sid}

	// Snapshot checks. A refusal leaves the proposal pending (or, for a connection that no longer
	// exists, failed: it can never run).
	if !h.snapshotHolds(w, r, cfg, proof, p) {
		return
	}

	// The compare-and-set and everything after it run on a context that outlives the request: a
	// client that disconnects as the UPDATE commits must not leave the row executing with nothing
	// sent, or an action half way.
	casCtx, cancelCAS := context.WithTimeout(context.WithoutCancel(r.Context()), proposalWriteBudget)
	defer cancelCAS()
	if _, err := db.BeginProposalExecution(casCtx, h.api.dbPool, who.Account, p.ID, who.Account); err != nil {
		transitionError(w, err, "this proposal was already decided by another request, or has expired")
		return
	}
	log.Printf("proposals: approved id=%s account=%s connection=%s tool=%q", p.ID, p.AccountID, p.ConnectionID, p.Tool)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), proposalExecuteBudget)
	defer cancel()
	res, err := exec.ExecuteApproved(ctx, mcpgw.ApprovedCall{
		Proof: proof, ConnectionID: p.ConnectionID, URLSnapshot: p.URLSnapshot,
		Tool: p.Tool, ToolHash: p.ToolHash, Arguments: p.Arguments,
	})
	rec, cancelRec := context.WithTimeout(context.WithoutCancel(r.Context()), proposalWriteBudget)
	defer cancelRec()
	state, result := db.ProposalDone, res.Result
	var ae *mcpgw.ApprovedError
	switch {
	case err != nil:
		state = db.ProposalFailed
		msg := "the action could not be run"
		if errors.As(err, &ae) {
			msg = ae.Msg
		}
		if ae != nil && ae.NotSent {
			h.releaseUnsent(rec, w, p, ae)
			return
		}
		if errors.Is(err, mcpgw.ErrOutcomeUnknown) {
			state = db.ProposalUnknown
		}
		result, _ = json.Marshal(map[string]string{"error": msg})
	case res.IsError:
		state = db.ProposalFailed
	}
	done, ferr := db.FinishProposal(rec, h.api.dbPool, p.ID, state, result)
	if ferr != nil {
		// The call was sent. The row stays executing; the maintenance pass turns it into unknown.
		log.Printf("proposals: could not record the outcome of id=%s (%s): %v", p.ID, state, ferr)
		writeError(w, http.StatusInternalServerError, "could not confirm whether the action ran (its outcome could not be recorded): "+
			"check the target service, then this proposal shortly")
		return
	}
	log.Printf("proposals: finished id=%s account=%s connection=%s tool=%q state=%s", p.ID, p.AccountID, p.ConnectionID, p.Tool, state)
	writeJSON(w, http.StatusOK, viewOfProposal(done))
}

// releaseUnsent answers an approval that failed before any byte of the call was sent: the proposal
// goes back from executing to pending (no decision recorded), so it can be approved again once
// the cause is gone.
func (h *proposalsHandler) releaseUnsent(ctx context.Context, w http.ResponseWriter, p *db.Proposal, ae *mcpgw.ApprovedError) {
	if _, err := db.ReleaseProposal(ctx, h.api.dbPool, p.ID); err != nil {
		// Nothing was sent, but the row is stuck executing; the maintenance pass will mark it
		// unknown, which the person can then resolve.
		log.Printf("proposals: could not release id=%s: %v", p.ID, err)
		writeError(w, http.StatusInternalServerError, "nothing was sent, but the proposal could not be put back to pending: try again shortly")
		return
	}
	log.Printf("proposals: released id=%s account=%s connection=%s tool=%q (nothing was sent)", p.ID, p.AccountID, p.ConnectionID, p.Tool)
	status := http.StatusBadGateway
	if ae.Changed {
		status = http.StatusConflict
	}
	writeError(w, status, ae.Msg+": nothing was sent, and the proposal is still pending")
}

// snapshotHolds checks the approval's preconditions and writes the refusal when one fails.
func (h *proposalsHandler) snapshotHolds(w http.ResponseWriter, r *http.Request, cfg *mcpStartConfig, proof mcpgw.Proof, p *db.Proposal) bool {
	conns, err := cfg.Core.ListConnections(r.Context(), proof)
	switch {
	case errors.Is(err, ErrMCPProofInvalid):
		writeError(w, http.StatusUnauthorized, msgProposalSignIn)
		return false
	case err != nil:
		log.Printf("proposals: core list failed: %v", err)
		writeError(w, http.StatusBadGateway, "could not reach blerg-core to check the connection: try again shortly")
		return false
	}
	var conn *MCPConnection
	for i := range conns {
		if conns[i].ID == p.ConnectionID {
			conn = &conns[i]
		}
	}
	if conn == nil {
		h.connectionGone(w, r, p)
		return false
	}
	if conn.URL != p.URLSnapshot {
		writeError(w, http.StatusConflict, "the connection's address changed since the agent proposed this, so it was not run: "+
			"reject this proposal and let the agent propose again")
		return false
	}
	if conn.Status != "ok" {
		writeError(w, http.StatusConflict, "the connection needs attention ("+conn.Status+"): fix it in blerg-core settings, then approve again")
		return false
	}
	live, err := cfg.Hasher.LiveToolHashes(r.Context(), proof, conn.ID, conn.URL)
	switch {
	case errors.Is(err, mcpgw.ErrConnectionGone):
		h.connectionGone(w, r, p)
		return false
	case err != nil:
		// The error can name hosts and addresses: log it, never send it.
		log.Printf("proposals: live tool check for %s failed: %v", p.ConnectionID, err)
		writeError(w, http.StatusBadGateway, "could not check the tool with the MCP server: it was unreachable or refused the request; try again shortly")
		return false
	}
	if hash := live[p.Tool]; hash == "" || hash != p.ToolHash {
		writeError(w, http.StatusConflict, "the tool's definition changed (or the server no longer offers it) since the agent proposed this, so it was not run")
		return false
	}
	return true
}

// connectionGone answers an approval whose connection no longer exists for this person: its
// pending proposals can never run, so they are failed, and the approval is refused.
func (h *proposalsHandler) connectionGone(w http.ResponseWriter, r *http.Request, p *db.Proposal) {
	if _, err := db.FailPendingProposals(r.Context(), h.api.dbPool, p.AccountID, []string{p.ConnectionID}, proposalGoneReason); err != nil {
		log.Printf("proposals: fail proposals of a deleted connection: %v", err)
	}
	writeError(w, http.StatusConflict, "the connection no longer exists, so this proposal can no longer be run")
}

// proposalGoneReason is the result stored on a pending proposal whose connection was deleted.
const proposalGoneReason = "the connection was deleted, so this action can no longer be run"

// ---- maintenance ---------------------------------------------------------------------------------------

// proposalMaintenance is what one maintenance pass did.
type proposalMaintenance struct{ Expired, Stale, Pruned int64 }

// maintainProposals expires pending proposals after 7 days, turns approvals that lost their outcome
// (executing for too long) into unknown, and prunes decided proposals 30 days after the decision.
// Every step is idempotent, so replicas may all run it.
func (a *API) maintainProposals(ctx context.Context) proposalMaintenance {
	var rep proposalMaintenance
	if a.dbPool == nil {
		return rep
	}
	var err error
	if rep.Expired, err = db.ExpirePendingProposals(ctx, a.dbPool, proposalExpiry); err != nil {
		log.Printf("proposals: expire: %v", err)
	}
	if rep.Stale, err = db.UnknownStaleExecuting(ctx, a.dbPool, proposalExecutingStale); err != nil {
		log.Printf("proposals: stale approvals: %v", err)
	}
	if rep.Pruned, err = db.PruneDecidedProposals(ctx, a.dbPool, proposalRetention); err != nil {
		log.Printf("proposals: prune: %v", err)
	}
	if rep.Expired+rep.Stale+rep.Pruned > 0 {
		log.Printf("proposals: expired %d, %d lost their outcome (now unknown), pruned %d", rep.Expired, rep.Stale, rep.Pruned)
	}
	return rep
}

// StartProposalMaintenance starts the maintenance loop in the background when the runner
// database exists. It does not depend on crons or blerg-core: proposals exist with only the
// gateway. It reports whether it started.
func StartProposalMaintenance(ctx context.Context, api *API) bool {
	if api == nil || api.dbPool == nil {
		return false
	}
	go api.RunProposalMaintenance(ctx)
	return true
}

// RunProposalMaintenance runs maintainProposals now and every hour until ctx is done.
func (a *API) RunProposalMaintenance(ctx context.Context) {
	a.maintainProposals(ctx)
	t := time.NewTicker(proposalMaintainEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.maintainProposals(ctx)
		}
	}
}
