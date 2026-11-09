package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	cid "github.com/blerglab/blerg-ai/contracts/identity"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livemcp"
	"github.com/jackc/pgx/v5"
)

func (d Deps) registerLiveDataRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/live-data/proposals", d.handleLiveProposal)
	mux.HandleFunc("POST /internal/live-data/proposal", d.handleLiveProposalRead)
	mux.HandleFunc("POST /internal/live-data/approve", d.handleLiveApproval)
	for _, path := range []string{"receipt", "cancel", "confirm"} {
		mux.HandleFunc("POST /internal/live-data/"+path, d.handleLiveReconcile)
	}
	mux.HandleFunc("POST /internal/live-data/policy", d.handleLivePolicy)
	mux.HandleFunc("GET /api/settings/live-data", d.handleLiveSettings)
	mux.HandleFunc("PUT /api/settings/live-data", d.handleLiveSettings)
	mux.HandleFunc("PUT /api/settings/live-data/policy", d.handleLivePolicy)
}

// Approval forwarding reuses Runner's existing component gate. It opens no
// mint/credential route: the verified person's own token is mandatory for every
// authority increase. The live-data issuer still has no production transport.
// The request-local marker is set only after successful component or human
// authentication. Headers and request bodies cannot opt into durable auditing.
type liveAuthenticatedKey struct{}

func liveMarkAuthenticated(r *http.Request) {
	*r = *r.WithContext(context.WithValue(r.Context(), liveAuthenticatedKey{}, true))
}

func (d Deps) liveComponent(w http.ResponseWriter, r *http.Request) bool {
	if d.Store == nil || d.InternalKey == "" {
		d.liveFail(w, r, 503, "service_unavailable", "approve")
		return false
	}
	if !validInternalKey(r, d.InternalKey) {
		d.liveFail(w, r, 401, "service_identity_required", "approve")
		return false
	}
	liveMarkAuthenticated(r)
	w.Header().Set("Cache-Control", "no-store")
	return true
}
func liveDecode(w http.ResponseWriter, r *http.Request, out any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		return ld.ErrDenied
	}
	canonical, err := livemcp.CanonicalObject(raw)
	if err != nil {
		return ld.ErrDenied
	}
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil {
		return ld.ErrDenied
	}
	return nil
}
func (d Deps) liveFail(w http.ResponseWriter, r *http.Request, status int, cause, operation string) {
	failurelog.Default.Event("live_data_approval:"+cause, slog.LevelWarn, "component", "core", "cause", cause)
	if d.Store != nil && r.Context().Value(liveAuthenticatedKey{}) == true {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
		_, err := d.Store.Pool().Exec(ctx, `INSERT INTO live_data_approval_audit(operation,outcome) VALUES($1,'denied')`, operation)
		cancel()
		if err != nil {
			failurelog.Default.Event("live_data_approval_audit", slog.LevelWarn, "component", "core", "cause", "audit_unavailable")
			status = 503
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	if cause == "decision_revoked" || cause == "service_identity_required" || cause == "runner_identity_mismatch" {
		w.Header().Set("X-Blerg-Live-Data-Cause", cause)
	}
	if cause == "unsupported_live_card" {
		http.Error(w, ld.NativeCardSetupRefusal, status)
		return
	}
	http.Error(w, "Live data decision unavailable.", status)
}

// Never accept sid + component key as a human approval. Core verifies the exact
// forwarded person's token, including its Runner audience and live login chain.
func (d Deps) livePerson(r *http.Request, token, account, audience string) (cid.Principal, error) {
	svc, ok := d.identitySvc()
	if !ok || d.Identity == nil || d.Store == nil || len(token) > 8192 {
		return cid.Principal{}, ld.ErrDenied
	}
	keys, err := d.Identity.JWKS(r.Context())
	if err != nil {
		return cid.Principal{}, ld.ErrDenied
	}
	checker, err := d.revChecker(r.Context())
	if err != nil {
		return cid.Principal{}, ld.ErrDenied
	}
	p, err := cid.Verify(token, audience, keys, checker, sensitiveCaps)
	if err != nil || p.Kind != "human" || p.OnBehalfOf != "" || p.Sid == "" || !p.Has("card.read") || account != "" && p.Sub != account {
		return cid.Principal{}, ld.ErrDenied
	}
	_, live, err := svc.HumanSessionLive(r.Context(), p.Sub, p.Sid)
	if err != nil || !live {
		return cid.Principal{}, ld.ErrDenied
	}
	state, err := svc.AccountState(r.Context(), p.Sub)
	if err != nil || state.Disabled || state.MustChangePassword {
		return cid.Principal{}, ld.ErrDenied
	}
	liveMarkAuthenticated(r)
	return p, nil
}
func (d Deps) liveHumanTx(r *http.Request, p cid.Principal) (pgx.Tx, error) {
	tx, err := d.Store.Pool().Begin(r.Context())
	if err != nil {
		return nil, err
	}
	svc, ok := d.identitySvc()
	if !ok {
		_ = tx.Rollback(r.Context())
		return nil, ld.ErrDenied
	}
	if err := svc.LockLiveHumanApprovalTx(r.Context(), tx, p.Sub, p.Sid); err != nil {
		_ = tx.Rollback(r.Context())
		return nil, err
	}
	if p.ExpiresAt <= time.Now().Unix() {
		_ = tx.Rollback(r.Context())
		return nil, ld.ErrDenied
	}
	return tx, nil
}

type liveProposalRequest struct {
	internalMCPProof
	Configuration ld.Revision `json:"configuration"`
	AskID         string      `json:"ask_id"`
}

func (d Deps) liveProposalProof(w http.ResponseWriter, r *http.Request, body any, proof *internalMCPProof) bool {
	if !d.liveComponent(w, r) {
		return false
	}
	if liveDecode(w, r, body) != nil || validateProofShape(proof.AccountID, proof.TokenID, proof.SessionID) != "" {
		d.liveFail(w, r, 400, "invalid_proposal", "approve")
		return false
	}
	_, _, live, err := d.principalLiveness(r.Context(), proof.AccountID, proof.TokenID, proof.SessionID)
	if err != nil || !live {
		d.liveFail(w, r, 404, "proposal_owner_unavailable", "approve")
		return false
	}
	return true
}
func (d Deps) handleLiveProposal(w http.ResponseWriter, r *http.Request) {
	var body liveProposalRequest
	if !d.liveProposalProof(w, r, &body, &body.internalMCPProof) {
		return
	}
	if !body.Configuration.Card.Native() {
		d.liveFail(w, r, 400, "unsupported_live_card", "approve")
		return
	}
	if !uuidShape.MatchString(body.AskID) {
		d.liveFail(w, r, 400, "invalid_ask", "approve")
		return
	}
	runner, err := d.liveRunner(r)
	if err != nil {
		d.liveFail(w, r, 403, "runner_identity_required", "approve")
		return
	}
	config := body.Configuration
	config.Scope.Installation = runner.Installation
	config.Scope.Runner = runner.Runner
	if d.LiveDataAuthorityEpoch == nil || d.LiveDataAuthorityEpoch() < 1 {
		d.liveFail(w, r, 503, "authority_epoch_unavailable", "approve")
		return
	}
	config.Scope.AuthorityEpoch = d.LiveDataAuthorityEpoch()
	if config.Scope.Owner != body.AccountID || !uuidShape.MatchString(config.Scope.Binding) || !uuidShape.MatchString(config.Scope.Connection) || config.Scope.Revision < 1 {
		d.liveFail(w, r, 400, "invalid_proposal", "approve")
		return
	}
	tx, err := d.Store.Pool().Begin(r.Context())
	if err != nil {
		d.liveFail(w, r, 503, "store_unavailable", "approve")
		return
	}
	defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
	err = tx.QueryRow(r.Context(), `SELECT a.live_data_epoch,c.live_data_generation FROM accounts a JOIN mcp_connections c ON c.account_id=a.id WHERE a.id::text=$1 AND c.id::text=$2 AND a.disabled_at IS NULL AND NOT a.must_change_password AND c.runner_session_id IS NULL AND c.status='ok'`, body.AccountID, config.Scope.Connection).Scan(&config.Scope.OwnerEpoch, &config.Scope.ConnectionGeneration)
	if err != nil {
		d.liveFail(w, r, 404, "connection_unavailable", "approve")
		return
	}
	config, canonical, err := config.Canonical(time.Now())
	if err != nil {
		d.liveFail(w, r, 400, "invalid_or_secret_configuration", "approve")
		return
	}
	if err = d.liveFreshTool(r.Context(), config, body.internalMCPProof); err != nil {
		d.liveFail(w, r, 409, "live_tool_not_read_only_or_changed", "approve")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO live_data_proposal_heads(binding_id,owner_id,revision) VALUES($1,$2,0) ON CONFLICT DO NOTHING`, config.Scope.Binding, body.AccountID); err != nil {
		d.liveFail(w, r, 503, "store_unavailable", "approve")
		return
	}
	var owner string
	var head int64
	err = tx.QueryRow(r.Context(), `SELECT owner_id::text,revision FROM live_data_proposal_heads WHERE binding_id=$1 FOR UPDATE`, config.Scope.Binding).Scan(&owner, &head)
	if err != nil || owner != body.AccountID || head != config.Scope.Revision-1 {
		d.liveFail(w, r, 409, "stale_revision", "approve")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO live_data_proposals(binding_id,revision,owner_id,digest,configuration,ask_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, config.Scope.Binding, config.Scope.Revision, owner, config.Scope.Digest, canonical, body.AskID, d.liveNow()); err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE live_data_proposal_heads SET revision=$2 WHERE binding_id=$1`, config.Scope.Binding, config.Scope.Revision)
	}

	if err != nil || tx.Commit(r.Context()) != nil {
		d.liveFail(w, r, 503, "proposal_commit_failed", "approve")
		return
	}
	writeJSON(w, ld.Proposal{Configuration: config, Digest: config.Scope.Digest, Status: "pending", AskID: body.AskID})
}

type liveProposalRead struct {
	internalMCPProof
	BindingID string `json:"binding_id"`
	Revision  int64  `json:"revision"`
}

func (d Deps) handleLiveProposalRead(w http.ResponseWriter, r *http.Request) {
	var body liveProposalRead
	if !d.liveProposalProof(w, r, &body, &body.internalMCPProof) {
		return
	}
	proposal, err := readLiveProposal(r.Context(), d.Store.Pool(), body.AccountID, body.BindingID, body.Revision, false)
	if err != nil {
		d.liveFail(w, r, 409, "stale_revision", "approve")
		return
	}
	writeJSON(w, proposal)
}

type liveQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readLiveProposal(ctx context.Context, q liveQuery, owner, binding string, revision int64, lock bool, lenient ...bool) (ld.Proposal, error) {
	sql := `SELECT p.configuration,p.digest,p.status,p.created_at,p.ask_id::text FROM live_data_proposals p JOIN live_data_proposal_heads h ON h.binding_id=p.binding_id WHERE p.binding_id::text=$1 AND p.revision=$2 AND p.owner_id::text=$3 AND h.owner_id=p.owner_id AND h.revision=p.revision`
	if len(lenient) == 0 || !lenient[0] {
		sql += ` AND p.decision_deadline>clock_timestamp()`
	}
	if len(lenient) > 0 && lenient[0] {
		sql = strings.Replace(sql, " AND h.revision=p.revision", "", 1)
	}
	if lock {
		sql += " FOR UPDATE OF p,h"
	}
	var raw []byte
	var out ld.Proposal
	var created time.Time
	if err := q.QueryRow(ctx, sql, binding, revision, owner).Scan(&raw, &out.Digest, &out.Status, &created, &out.AskID); err != nil {
		return out, ld.ErrDenied
	}
	if json.Unmarshal(raw, &out.Configuration) != nil {
		return out, ld.ErrDenied
	}
	config, canonical, err := out.Configuration.Canonical(created)
	if err != nil || config.Scope.Digest != out.Digest || !bytes.Equal(raw, canonical) {
		return out, ld.ErrDenied
	}
	out.Configuration = config
	out.CreatedAt = created
	return out, nil
}

func (d Deps) handleLiveApproval(w http.ResponseWriter, r *http.Request) {
	if !d.liveComponent(w, r) {
		return
	}
	var body ld.Decision
	if liveDecode(w, r, &body) != nil || !uuidShape.MatchString(body.AskID) {
		d.liveFail(w, r, 400, "invalid_decision", "approve")
		return
	}
	p, err := d.livePerson(r, body.AccessToken, body.AccountID, "blerg-runner")
	if err != nil {
		d.liveFail(w, r, 404, "human_credential_required", "approve")
		return
	}
	// Remote metadata work must precede the account-session and proposal locks.
	// The immutable revision and exact hash are checked again under those locks.
	preflight, err := readLiveProposal(r.Context(), d.Store.Pool(), p.Sub, body.BindingID, body.Revision, false, !body.Approve)
	if err != nil || preflight.Digest != body.Digest || preflight.AskID != body.AskID {
		d.liveFail(w, r, 409, "stale_revision", "approve")
		return
	}
	runnerPreflight, identityErr := d.liveRunner(r)
	if identityErr != nil || runnerPreflight.Installation != preflight.Configuration.Scope.Installation || runnerPreflight.Runner != preflight.Configuration.Scope.Runner {
		d.liveFail(w, r, 403, "runner_identity_mismatch", "approve")
		return
	}
	var listedAt, postureAt time.Time
	postureSafe := d.LiveDataSafeRuntime
	if body.Approve && preflight.Status == "pending" {
		if !time.Unix(p.IssuedAt, 0).After(preflight.CreatedAt) {
			d.liveFail(w, r, 401, "fresh_login_required", "approve")
			return
		}
		if err = d.liveFreshTool(r.Context(), preflight.Configuration, internalMCPProof{AccountID: p.Sub, SessionID: p.Sid}); err != nil {
			d.liveFail(w, r, 409, "live_tool_not_read_only_or_changed", "approve")
			return
		}
		listedAt = d.liveNow()
		if d.LiveDataPosture != nil {
			postureSafe, err = d.LiveDataPosture(r.Context())
			if err != nil {
				d.liveFail(w, r, 503, "host_posture_unavailable", "approve")
				return
			}
		}
		postureAt = d.liveNow()
	}
	tx, err := d.liveHumanTx(r, p)
	if err != nil {
		d.liveFail(w, r, 404, "login_unavailable", "approve")
		return
	}
	defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
	proposal, err := readLiveProposal(r.Context(), tx, p.Sub, body.BindingID, body.Revision, true, !body.Approve)
	if err != nil || proposal.Digest != body.Digest || body.Approve && (!proposal.Configuration.ExpiresAt.After(time.Now()) || d.LiveDataAuthorityEpoch == nil || proposal.Configuration.Scope.AuthorityEpoch != d.LiveDataAuthorityEpoch()) {
		d.liveFail(w, r, 409, "stale_revision", "approve")
		return
	}
	if body.AskID != proposal.AskID {
		d.liveFail(w, r, 409, "ask_mismatch", "approve")
		return
	}
	if body.Approve && !time.Unix(p.IssuedAt, 0).After(proposal.CreatedAt) {
		d.liveFail(w, r, 401, "fresh_login_required", "approve")
		return
	}
	runner, identityErr := d.liveRunner(r)
	if identityErr != nil || runner.Installation != proposal.Configuration.Scope.Installation || runner.Runner != proposal.Configuration.Scope.Runner {
		d.liveFail(w, r, 403, "runner_identity_mismatch", "approve")
		return
	}
	status := "declined"
	operation := "decline"
	if body.Approve {
		status = "approved"
		operation = "approve"
	}
	if proposal.Status != "pending" && (body.Approve || proposal.Status == "declined") {
		var priorAsk string
		err = tx.QueryRow(r.Context(), `SELECT ask_id::text FROM live_data_proposals WHERE binding_id=$1 AND revision=$2`, body.BindingID, body.Revision).Scan(&priorAsk)
		if err != nil || priorAsk != body.AskID || proposal.Status != status {
			d.liveFail(w, r, 409, "decision_replay_conflict", operation)
			return
		}
		// Exact duplicate is a receipt lookup, never approval renewal or policy change.
		writeJSON(w, ld.DecisionReceipt{BindingID: body.BindingID, Revision: body.Revision, Digest: body.Digest, AskID: body.AskID, Status: status})
		return
	}
	if !body.Approve {
		if _, err = tx.Exec(r.Context(), `UPDATE live_data_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE binding_id=$1 AND revision=$2`, body.BindingID, body.Revision); err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE live_data_proposals SET status='declined',approved_sid=coalesce(approved_sid,$3) WHERE binding_id=$1 AND revision=$2`, body.BindingID, body.Revision, p.Sid)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO live_data_approval_audit(owner_id,binding_id,revision,digest,ask_id,operation,outcome)VALUES($1,$2,$3,$4,$5,'decline','ok')`, p.Sub, body.BindingID, body.Revision, body.Digest, body.AskID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			d.liveFail(w, r, 503, "decline_unavailable", "decline")
			return
		}
		writeJSON(w, ld.DecisionReceipt{BindingID: body.BindingID, Revision: body.Revision, Digest: body.Digest, AskID: body.AskID, Status: "declined"})
		return
	}
	config := proposal.Configuration
	if preflight.Digest != proposal.Digest || preflight.Configuration.Scope.Hash != config.Scope.Hash || listedAt.IsZero() || d.liveNow().Sub(listedAt) > 10*time.Second {
		d.liveFail(w, r, 409, "live_tool_not_read_only_or_changed", "approve")
		return
	}
	var epoch, generation int64
	err = tx.QueryRow(r.Context(), `SELECT a.live_data_epoch,c.live_data_generation FROM accounts a JOIN mcp_connections c ON c.account_id=a.id WHERE a.id::text=$1 AND c.id::text=$2 AND a.disabled_at IS NULL AND NOT a.must_change_password AND c.runner_session_id IS NULL AND c.status='ok' FOR SHARE OF a,c`, p.Sub, config.Scope.Connection).Scan(&epoch, &generation)
	if err != nil || epoch != config.Scope.OwnerEpoch || generation != config.Scope.ConnectionGeneration {
		d.liveFail(w, r, 409, "authority_changed", operation)
		return
	}
	if postureAt.IsZero() || d.liveNow().Sub(postureAt) > 5*time.Second {
		d.liveFail(w, r, 409, "host_posture_stale", "approve")
		return
	}
	unsafe := !postureSafe || d.LiveDataDesktop
	var consent bool
	if err = tx.QueryRow(r.Context(), `SELECT coalesce((SELECT unsafe_host_opt_in AND consent_epoch=$2 FROM live_data_owner_settings WHERE account_id=$1),false)`, p.Sub, config.Scope.AuthorityEpoch).Scan(&consent); err != nil {
		d.liveFail(w, r, 503, "settings_unavailable", operation)
		return
	}
	if body.Approve {
		if unsafe && !consent {
			d.liveFail(w, r, 409, "unsafe_host_opt_in_required", operation)
			return
		}
		var allowed bool
		err = tx.QueryRow(r.Context(), `SELECT mode='allow' AND hash=$3 FROM live_data_policy WHERE connection_id=$1 AND tool=$2 FOR SHARE`, config.Scope.Connection, config.Scope.Tool, config.Scope.Hash).Scan(&allowed)
		if err != nil || !allowed {
			d.liveFail(w, r, 409, "live_policy_not_allow", operation)
			return
		}
		if _, err = tx.Exec(r.Context(), `UPDATE live_data_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE binding_id=$1 AND revision<>$2`, body.BindingID, body.Revision); err != nil {
			d.liveFail(w, r, 503, "replacement_unavailable", "approve")
			return
		}
		scope, _ := json.Marshal(config.Scope)
		_, canonical, _ := config.Canonical(config.ExpiresAt.Add(-time.Hour))
		_, err = tx.Exec(r.Context(), `INSERT INTO live_data_grants(binding_id,revision,owner_id,connection_id,scope,arguments,approved_sid,approved_at,expires_at,unsafe_host_opt_in,approved_configuration) VALUES($1,$2,$3,$4,$5,$6,$7,clock_timestamp(),$8,$9,$10)`, body.BindingID, body.Revision, p.Sub, config.Scope.Connection, scope, config.Arguments, p.Sid, config.ExpiresAt, unsafe && consent, canonical)
		if err != nil {
			d.liveFail(w, r, 409, "grant_conflict", operation)
			return
		}
	}
	_, err = tx.Exec(r.Context(), `UPDATE live_data_proposals SET status=$3,ask_id=$4,approved_sid=$5 WHERE binding_id=$1 AND revision=$2`, body.BindingID, body.Revision, status, body.AskID, p.Sid)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO live_data_approval_audit(owner_id,binding_id,revision,digest,ask_id,operation,outcome) VALUES($1,$2,$3,$4,$5,$6,'ok')`, p.Sub, body.BindingID, body.Revision, body.Digest, body.AskID, operation)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		d.liveFail(w, r, 503, "approval_commit_failed", operation)
		return
	}
	writeJSON(w, ld.DecisionReceipt{BindingID: body.BindingID, Revision: body.Revision, Digest: body.Digest, AskID: body.AskID, Status: status})
}

func (d Deps) handleLivePolicy(w http.ResponseWriter, r *http.Request) {
	internal := strings.HasPrefix(r.URL.Path, "/internal/")
	if internal && !d.liveComponent(w, r) {
		return
	}
	if !internal && !d.requireSameOrigin(w, r) {
		return
	}
	var body ld.PolicyWrite
	if liveDecode(w, r, &body) != nil || body.Mode != "allow" && body.Mode != "propose" && body.Mode != "off" {
		d.liveFail(w, r, 400, "invalid_policy", "policy")
		return
	}
	if internal && body.Mode == "allow" {
		d.liveFail(w, r, 403, "settings_required", "policy")
		return
	}
	var person cid.Principal
	var err error
	if !internal {
		person, err = d.livePerson(r, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "", "blerg-core")
		body.AccountID = person.Sub
	}
	if err != nil || !uuidShape.MatchString(body.AccountID) {
		d.liveFail(w, r, 404, "human_credential_required", "policy")
		return
	}
	var tx pgx.Tx
	if person.Sub != "" {
		tx, err = d.liveHumanTx(r, person)
	} else {
		tx, err = d.Store.Pool().Begin(r.Context())
	}
	if err != nil {
		d.liveFail(w, r, 503, "store_unavailable", "policy")
		return
	}
	defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
	proposal, err := readLiveProposal(r.Context(), tx, body.AccountID, body.BindingID, body.Revision, true, body.Mode != "allow")
	if err != nil || proposal.Digest != body.Digest {
		d.liveFail(w, r, 409, "stale_revision", "policy")
		return
	}
	scope := proposal.Configuration.Scope
	if body.Mode == "allow" {
		if !proposal.Configuration.ExpiresAt.After(time.Now()) || d.LiveDataAuthorityEpoch == nil || scope.AuthorityEpoch != d.LiveDataAuthorityEpoch() {
			d.liveFail(w, r, 409, "stale_revision", "policy")
			return
		}
		var epoch, generation int64
		err = tx.QueryRow(r.Context(), `SELECT a.live_data_epoch,c.live_data_generation FROM accounts a JOIN mcp_connections c ON c.account_id=a.id WHERE a.id::text=$1 AND c.id::text=$2 AND a.disabled_at IS NULL AND NOT a.must_change_password AND c.runner_session_id IS NULL AND c.status='ok' FOR SHARE OF a,c`, person.Sub, scope.Connection).Scan(&epoch, &generation)
		if err != nil || epoch != scope.OwnerEpoch || generation != scope.ConnectionGeneration {
			d.liveFail(w, r, 409, "authority_changed", "policy")
			return
		}
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO live_data_policy(connection_id,tool,mode,hash) VALUES($1,$2,$3,$4) ON CONFLICT(connection_id,tool) DO UPDATE SET mode=EXCLUDED.mode,hash=EXCLUDED.hash`, scope.Connection, scope.Tool, body.Mode, scope.Hash)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO live_data_approval_audit(owner_id,binding_id,revision,digest,operation,outcome) VALUES($1,$2,$3,$4,'policy','ok')`, body.AccountID, body.BindingID, body.Revision, body.Digest)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		d.liveFail(w, r, 503, "policy_commit_failed", "policy")
		return
	}
	writeJSON(w, map[string]string{"mode": body.Mode})
}

func (d Deps) handleLiveSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && !d.requireSameOrigin(w, r) {
		return
	}
	p, err := d.livePerson(r, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "", "blerg-core")
	if err != nil {
		d.liveFail(w, r, 404, "human_credential_required", "host_opt_in")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "PUT" {
		var body struct {
			UnsafeHostOptIn bool   `json:"unsafe_host_opt_in"`
			Acknowledgement string `json:"acknowledgement"`
		}
		if liveDecode(w, r, &body) != nil || body.UnsafeHostOptIn && body.Acknowledgement != ld.UnsafeHostWarning {
			d.liveFail(w, r, 400, "acknowledgement_required", "host_opt_in")
			return
		}
		tx, err := d.liveHumanTx(r, p)
		if err != nil {
			d.liveFail(w, r, 404, "login_unavailable", "host_opt_in")
			return
		}
		defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
		_, err = tx.Exec(r.Context(), `INSERT INTO live_data_owner_settings(account_id,unsafe_host_opt_in,consent_epoch) VALUES($1,$2,$3) ON CONFLICT(account_id) DO UPDATE SET unsafe_host_opt_in=EXCLUDED.unsafe_host_opt_in,consent_epoch=EXCLUDED.consent_epoch`, p.Sub, body.UnsafeHostOptIn, d.liveConsentEpoch())
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO live_data_approval_audit(owner_id,operation,outcome) VALUES($1,'host_opt_in','ok')`, p.Sub)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			d.liveFail(w, r, 503, "settings_commit_failed", "host_opt_in")
			return
		}
	}
	var consent bool
	if err = d.Store.Pool().QueryRow(r.Context(), `SELECT coalesce((SELECT unsafe_host_opt_in AND consent_epoch=$2 FROM live_data_owner_settings WHERE account_id=$1),false)`, p.Sub, d.liveConsentEpoch()).Scan(&consent); err != nil {
		d.liveFail(w, r, 503, "settings_unavailable", "host_opt_in")
		return
	}
	rows, err := d.Store.Pool().Query(r.Context(), `SELECT p.binding_id::text,p.revision,p.digest,p.status,c.name,p.configuration,coalesce(lp.mode,'off') FROM live_data_proposals p JOIN live_data_proposal_heads h ON h.binding_id=p.binding_id AND h.revision=p.revision JOIN mcp_connections c ON c.id=(convert_from(p.configuration,'UTF8')::jsonb->'scope'->>'connection')::uuid LEFT JOIN live_data_policy lp ON lp.connection_id=c.id AND lp.tool=convert_from(p.configuration,'UTF8')::jsonb->'scope'->>'tool' WHERE c.runner_session_id IS NULL AND p.owner_id=$1 ORDER BY p.created_at DESC LIMIT 20`, p.Sub)
	if err != nil {
		d.liveFail(w, r, 503, "settings_unavailable", "host_opt_in")
		return
	}
	defer rows.Close()
	bindings := []map[string]any{}
	for rows.Next() {
		var id, digest, status, name, mode string
		var revision int64
		var raw []byte
		if rows.Scan(&id, &revision, &digest, &status, &name, &raw, &mode) != nil {
			d.liveFail(w, r, 503, "settings_unavailable", "host_opt_in")
			return
		}
		var config ld.Revision
		if json.Unmarshal(raw, &config) != nil {
			d.liveFail(w, r, 503, "settings_unavailable", "host_opt_in")
			return
		}
		bindings = append(bindings, map[string]any{"binding_id": id, "revision": revision, "digest": digest, "status": status, "connection_name": name, "tool": config.Scope.Tool, "hash": config.Scope.Hash, "tool_definition": config.ToolDefinition, "mode": mode})
	}
	if rows.Err() != nil {
		d.liveFail(w, r, 503, "settings_unavailable", "host_opt_in")
		return
	}
	writeJSON(w, map[string]any{"unsafe_host_opt_in": consent, "polling_enabled": false, "bindings": bindings})
}

func (d Deps) liveNow() time.Time {
	if d.LiveDataNow != nil {
		return d.LiveDataNow().UTC()
	}
	// Keep the monotonic reading for the listing-age bound across wall-clock changes.
	return time.Now()
}

func (d Deps) liveRunner(r *http.Request) (ld.RunnerIdentity, error) {
	if d.LiveDataRunnerIdentity != nil {
		return d.LiveDataRunnerIdentity(r)
	}
	return ld.AuthenticatedRunner(r)
}

func (d Deps) liveConsentEpoch() int64 {
	if d.LiveDataAuthorityEpoch == nil {
		return 0
	}
	return d.LiveDataAuthorityEpoch()
}
