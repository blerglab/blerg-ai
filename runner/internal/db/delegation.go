package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Child sessions (migration 059): the person's approval of a session starting another one is a
// delegation request (a card in the chat), a grant (for this session) or a rule (always, per
// account). This file is the storage for all three, and the counters behind the start limits.

var (
	ErrDelegationNotFound = errors.New("delegation request not found")
	ErrDelegationState    = errors.New("delegation request is not pending")
)

// DelegationRequest is one stored start request awaiting (or past) the person's decision.
type DelegationRequest struct {
	ID, ParentSessionID, AccountID, DaemonID, IdempotencyKey, RequestHash string
	Request                                                               json.RawMessage
	Offered                                                               []string
	Status, Choice                                                        string // Choice "" unless allowed
	ChildSessionID                                                        string
	StartError                                                            string
	DecidedBy                                                             string
	DecidedSid                                                            string
	CreatedAt                                                             time.Time
	DecidedAt                                                             *time.Time
	ExpiresAt                                                             time.Time
	StartRecordedAt                                                       *time.Time
}

const delegationRequestCols = `id::text, parent_session_id::text, account_id, daemon_id::text, idempotency_key,
	request_hash, request, offered, status, COALESCE(choice, ''), COALESCE(child_session_id::text, ''),
	COALESCE(start_error, CASE WHEN start_recorded_at IS NOT NULL AND child_session_id IS NULL
		THEN 'the started session was deleted' ELSE '' END),
	COALESCE(decided_by, ''), COALESCE(decided_sid, ''), created_at, decided_at, expires_at, start_recorded_at`

func scanDelegationRequest(row pgx.Row) (DelegationRequest, error) {
	var r DelegationRequest
	var req []byte
	err := row.Scan(&r.ID, &r.ParentSessionID, &r.AccountID, &r.DaemonID, &r.IdempotencyKey,
		&r.RequestHash, &req, &r.Offered, &r.Status, &r.Choice, &r.ChildSessionID,
		&r.StartError, &r.DecidedBy, &r.DecidedSid, &r.CreatedAt, &r.DecidedAt, &r.ExpiresAt, &r.StartRecordedAt)
	r.Request = json.RawMessage(req)
	return r, err
}

func scanDelegationRequests(rows pgx.Rows) ([]DelegationRequest, error) {
	defer rows.Close()
	var out []DelegationRequest
	for rows.Next() {
		r, err := scanDelegationRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertDelegationRequest inserts r (status pending) unless (parent, key) exists, or the parent
// already has a pending request with the same request_hash; created=false then returns that
// existing row unchanged (the same-key row first).
func InsertDelegationRequest(ctx context.Context, pool *pgxpool.Pool, r DelegationRequest) (DelegationRequest, bool, error) {
	offered := r.Offered
	if offered == nil {
		offered = []string{}
	}
	req := []byte(r.Request)
	if len(req) == 0 {
		req = []byte(`{}`)
	}
	got, err := scanDelegationRequest(pool.QueryRow(ctx, `
		INSERT INTO delegation_requests (parent_session_id, account_id, daemon_id, idempotency_key,
		                                 request_hash, request, offered, expires_at)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6::jsonb, $7, $8)
		ON CONFLICT DO NOTHING
		RETURNING `+delegationRequestCols,
		r.ParentSessionID, r.AccountID, r.DaemonID, r.IdempotencyKey, r.RequestHash, req, offered, r.ExpiresAt))
	if err == nil {
		return got, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return DelegationRequest{}, false, err
	}
	existing, err := GetDelegationRequestByKey(ctx, pool, r.ParentSessionID, r.IdempotencyKey)
	if err != nil {
		return DelegationRequest{}, false, err
	}
	if existing != nil {
		return *existing, false, nil
	}
	same, err := scanDelegationRequest(pool.QueryRow(ctx,
		`SELECT `+delegationRequestCols+` FROM delegation_requests
		  WHERE parent_session_id = $1::uuid AND request_hash = $2 AND status = 'pending'`, r.ParentSessionID, r.RequestHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return DelegationRequest{}, false, ErrDelegationNotFound
	}
	if err != nil {
		return DelegationRequest{}, false, err
	}
	return same, false, nil
}

// GetDelegationRequest returns the request with this id; nil, nil when absent.
func GetDelegationRequest(ctx context.Context, pool *pgxpool.Pool, id string) (*DelegationRequest, error) {
	r, err := scanDelegationRequest(pool.QueryRow(ctx,
		`SELECT `+delegationRequestCols+` FROM delegation_requests WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetDelegationRequestByKey returns the parent's request with this idempotency key; nil, nil when absent.
func GetDelegationRequestByKey(ctx context.Context, pool *pgxpool.Pool, parentID, key string) (*DelegationRequest, error) {
	r, err := scanDelegationRequest(pool.QueryRow(ctx,
		`SELECT `+delegationRequestCols+` FROM delegation_requests WHERE parent_session_id = $1::uuid AND idempotency_key = $2`,
		parentID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// LockDelegationParent fences the policy/lifecycle fields used to authorize a
// decision. Call it before locking the request, consistently across decisions,
// so no pooled connection or inverted parent/request lock order is needed.
// It returns nil, nil when the parent no longer exists.
func LockDelegationParent(ctx context.Context, tx pgx.Tx, sid string) (*SessionRow, error) {
	var r SessionRow
	err := tx.QueryRow(ctx, `SELECT id::text,daemon_id::text,status,spawning_account_id,runtime,private,
		parent_session_id::text,board_id::text,cron_id::text,COALESCE(started_by_kind,''),
		COALESCE(delegation_via,''),ended_at,end_reason,delegation_ended_at,restrict_tools,discard_requested_at FROM sessions WHERE id=$1::uuid FOR UPDATE`, sid).
		Scan(&r.ID, &r.DaemonID, &r.Status, &r.SpawningAccountID, &r.Runtime, &r.Private,
			&r.ParentSessionID, &r.BoardID, &r.CronID, &r.StartedByKind, &r.DelegationVia, &r.EndedAt, &r.EndReason, &r.DelegationEndedAt, &r.RestrictTools, &r.DiscardRequestedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// DecideDelegationRequestTx: CAS from pending (and not expired) for the owner. status is
// "allowed" or "denied"; choice "" for denied. ErrDelegationNotFound when no row for (id, account);
// ErrDelegationState when it is no longer pending (the current row is returned with it).
func DecideDelegationRequestTx(ctx context.Context, tx pgx.Tx, id, account, status, choice, decidedBy, decidedSid string) (DelegationRequest, error) {
	// Lock first, then evaluate current wall time in a separate statement.
	// now() is frozen at BEGIN, and a predicate in an UPDATE alone may have
	// been evaluated before its row-lock wait.
	cur, err := scanDelegationRequest(tx.QueryRow(ctx,
		`SELECT `+delegationRequestCols+` FROM delegation_requests WHERE id=$1::uuid AND account_id=$2 FOR UPDATE`, id, account))
	if errors.Is(err, pgx.ErrNoRows) {
		return DelegationRequest{}, ErrDelegationNotFound
	}
	if err != nil {
		return DelegationRequest{}, err
	}
	got, err := scanDelegationRequest(tx.QueryRow(ctx, `
		UPDATE delegation_requests
		   SET status = $3, choice = NULLIF($4, ''), decided_by = NULLIF($5, ''), decided_sid = NULLIF($6, ''),
	       decided_at = clock_timestamp()
	 WHERE id = $1::uuid AND account_id = $2 AND status = 'pending' AND expires_at > clock_timestamp()
		RETURNING `+delegationRequestCols, id, account, status, choice, decidedBy, decidedSid))
	if err == nil {
		return got, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return DelegationRequest{}, err
	}
	return cur, ErrDelegationState
}

// ExpireDelegationRequest: CAS pending→expired for the parent's own request. ErrDelegationState with
// the current row otherwise (ErrDelegationNotFound when the parent has no such request).
func ExpireDelegationRequest(ctx context.Context, pool *pgxpool.Pool, id, parentID string) (DelegationRequest, error) {
	got, err := scanDelegationRequest(pool.QueryRow(ctx, `
		UPDATE delegation_requests SET status = 'expired'
		 WHERE id = $1::uuid AND parent_session_id = $2::uuid AND status = 'pending'
		RETURNING `+delegationRequestCols, id, parentID))
	if err == nil {
		return got, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return DelegationRequest{}, err
	}
	cur, err := scanDelegationRequest(pool.QueryRow(ctx,
		`SELECT `+delegationRequestCols+` FROM delegation_requests WHERE id = $1::uuid AND parent_session_id = $2::uuid`, id, parentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return DelegationRequest{}, ErrDelegationNotFound
	}
	if err != nil {
		return DelegationRequest{}, err
	}
	return cur, ErrDelegationState
}

// ExpireStaleDelegationRequests expires every pending request whose expires_at has passed.
func ExpireStaleDelegationRequests(ctx context.Context, pool *pgxpool.Pool) ([]DelegationRequest, error) {
	rows, err := pool.Query(ctx, `
		UPDATE delegation_requests SET status = 'expired'
		 WHERE status = 'pending' AND expires_at <= now()
		RETURNING `+delegationRequestCols)
	if err != nil {
		return nil, err
	}
	return scanDelegationRequests(rows)
}

// ExpirePendingDelegationRequestsForParent expires the parent's pending requests (its end, a pause).
func ExpirePendingDelegationRequestsForParent(ctx context.Context, pool *pgxpool.Pool, parentID string) ([]DelegationRequest, error) {
	rows, err := pool.Query(ctx, `
		UPDATE delegation_requests SET status = 'expired'
		 WHERE parent_session_id = $1::uuid AND status = 'pending'
		RETURNING `+delegationRequestCols, parentID)
	if err != nil {
		return nil, err
	}
	return scanDelegationRequests(rows)
}

// SetDelegationRequestStart records the outcome of starting the child of an allowed request: the
// child id, or the start error (either may be empty).
func SetDelegationRequestStart(ctx context.Context, pool *pgxpool.Pool, id, childID, startError string) error {
	tag, err := pool.Exec(ctx, `
		UPDATE delegation_requests
	   SET child_session_id = NULLIF($2, '')::uuid, start_error = NULLIF($3, ''),start_recorded_at=clock_timestamp()
	 WHERE id = $1::uuid AND status='allowed' AND start_recorded_at IS NULL`, id, childID, startError)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delegation_requests WHERE id=$1::uuid)`, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrDelegationState
		}
		return ErrDelegationNotFound
	}
	return nil
}

// MarkInterruptedDelegationStarts recovers missing outcomes even after long
// outages. A durable marker distinguishes an already completed start whose
// child was deleted. Active workers hold the matching session-level advisory
// lock, so the sweeper skips their accounts without blocking other accounts.
func MarkInterruptedDelegationStarts(ctx context.Context, pool *pgxpool.Pool, olderThan time.Duration, startError string) ([]DelegationRequest, error) {
	rows, err := pool.Query(ctx, `
		UPDATE delegation_requests SET start_error = $2,start_recorded_at=clock_timestamp()
		 WHERE status = 'allowed' AND child_session_id IS NULL AND start_error IS NULL AND start_recorded_at IS NULL
		   AND decided_at <= clock_timestamp() - make_interval(secs => $1)
		   AND pg_try_advisory_xact_lock(hashtext('delegation:'||account_id))
		RETURNING `+delegationRequestCols, olderThan.Seconds(), startError)
	if err != nil {
		return nil, err
	}
	return scanDelegationRequests(rows)
}

// HostDaemonName returns the name of a workstation daemon (any daemon row but a cluster pod's,
// mode "runner"); ok=false when there is no such row.
func HostDaemonName(ctx context.Context, pool *pgxpool.Pool, id string) (string, bool, error) {
	var name, mode string
	err := pool.QueryRow(ctx, `SELECT name, mode FROM daemons WHERE id = $1::uuid`, id).Scan(&name, &mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return name, mode != "runner", nil
}

// ListPendingDelegationRequests returns the parent's pending requests, oldest first.
func ListPendingDelegationRequests(ctx context.Context, pool *pgxpool.Pool, parentID string) ([]DelegationRequest, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+delegationRequestCols+` FROM delegation_requests
		 WHERE parent_session_id = $1::uuid AND status = 'pending'
		 ORDER BY created_at, id`, parentID)
	if err != nil {
		return nil, err
	}
	return scanDelegationRequests(rows)
}

// DelegationGrant approves this parent, engine, runtime and daemon across all repositories.
// Repo and NoRepo record the original approved request for audit only.
type DelegationGrant struct {
	ID, ParentSessionID, AccountID, Engine, Runtime, DaemonID, Repo string
	NoRepo                                                          bool
	CreatedFromRequest                                              string
	CreatedAt                                                       time.Time
	RevokedAt                                                       *time.Time
}

const delegationGrantCols = `id::text, parent_session_id::text, account_id, engine, runtime, daemon_id::text, repo,
	no_repo, COALESCE(created_from_request::text, ''), created_at, revoked_at`

func scanDelegationGrant(row pgx.Row) (DelegationGrant, error) {
	var g DelegationGrant
	err := row.Scan(&g.ID, &g.ParentSessionID, &g.AccountID, &g.Engine, &g.Runtime, &g.DaemonID, &g.Repo,
		&g.NoRepo, &g.CreatedFromRequest, &g.CreatedAt, &g.RevokedAt)
	return g, err
}

func scanDelegationGrants(rows pgx.Rows) ([]DelegationGrant, error) {
	defer rows.Close()
	var out []DelegationGrant
	for rows.Next() {
		g, err := scanDelegationGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// InsertDelegationGrantTx returns the one live grant for this scope, preserving its original
// audit fields. The parent fence serializes renewal with lifetime changes; obsolete or foreign-account grants
// cannot occupy the unique scope while asynchronous end cleanup is still outstanding.
func InsertDelegationGrantTx(ctx context.Context, tx pgx.Tx, g DelegationGrant) (DelegationGrant, error) {
	var cutoff *time.Time
	var owner string
	if err := tx.QueryRow(ctx, `SELECT delegation_ended_at, COALESCE(spawning_account_id, '') FROM sessions WHERE id=$1::uuid FOR UPDATE`, g.ParentSessionID).Scan(&cutoff, &owner); err != nil {
		return DelegationGrant{}, err
	}
	if owner == "" || owner != g.AccountID {
		return DelegationGrant{}, errors.New("delegation grant account does not own the parent")
	}
	if _, err := tx.Exec(ctx, `UPDATE delegation_grants SET revoked_at=now()
		WHERE parent_session_id=$1::uuid AND revoked_at IS NULL
		  AND (account_id <> $3 OR created_at <= $2)`, g.ParentSessionID, cutoff, owner); err != nil {
		return DelegationGrant{}, err
	}
	return scanDelegationGrant(tx.QueryRow(ctx, `
		INSERT INTO delegation_grants (parent_session_id, account_id, engine, runtime, daemon_id, repo, no_repo,
		                               created_from_request)
		VALUES ($1::uuid, $2, $3, $4, $5::uuid, $6, $7, NULLIF($8, '')::uuid)
		ON CONFLICT (parent_session_id, engine, runtime, daemon_id) WHERE revoked_at IS NULL
		DO UPDATE SET id=delegation_grants.id
		RETURNING `+delegationGrantCols,
		g.ParentSessionID, g.AccountID, g.Engine, g.Runtime, g.DaemonID, g.Repo, g.NoRepo, g.CreatedFromRequest))
}

// FindDelegationGrant finds a live grant for exactly this parent, engine, runtime and daemon.
// The requested repository (including no repository) does not affect authorization.
func FindDelegationGrant(ctx context.Context, pool *pgxpool.Pool, parentID, engine, runtime, daemonID string) (*DelegationGrant, error) {
	g, err := scanDelegationGrant(pool.QueryRow(ctx, `
		SELECT `+delegationGrantCols+` FROM delegation_grants
		 WHERE parent_session_id = $1::uuid AND revoked_at IS NULL AND engine = $2 AND runtime = $3
		   AND account_id = (SELECT spawning_account_id FROM sessions WHERE id=$1)
		   AND daemon_id = $4::uuid
		   AND NOT EXISTS (SELECT 1 FROM sessions p WHERE p.id=$1
		     AND (COALESCE((`+terminallyEndedSQL("p")+`),false)
		       OR delegation_grants.created_at <= p.delegation_ended_at))
		 ORDER BY created_at, id LIMIT 1`, parentID, engine, runtime, daemonID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// ListLiveDelegationGrants returns the parent's unrevoked grants, oldest first.
func ListLiveDelegationGrants(ctx context.Context, pool Querier, parentID string) ([]DelegationGrant, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+delegationGrantCols+` FROM delegation_grants
		 WHERE parent_session_id = $1::uuid AND revoked_at IS NULL
		   AND account_id = (SELECT spawning_account_id FROM sessions WHERE id=$1)
		   AND NOT EXISTS (SELECT 1 FROM sessions p WHERE p.id=$1
		     AND (COALESCE((`+terminallyEndedSQL("p")+`),false)
		       OR delegation_grants.created_at <= p.delegation_ended_at))
		 ORDER BY created_at, id`, parentID)
	if err != nil {
		return nil, err
	}
	return scanDelegationGrants(rows)
}

// RevokeDelegationGrant revokes a live grant of the account; false when there was none.
func RevokeDelegationGrant(ctx context.Context, pool Querier, id, account string) (bool, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE delegation_grants SET revoked_at = now()
		 WHERE id = $1::uuid AND account_id = $2 AND revoked_at IS NULL`, id, account)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RevokeDelegationGrantsForParent revokes every live grant of the parent (its end) and counts them.
func RevokeDelegationGrantsForParent(ctx context.Context, pool *pgxpool.Pool, parentID string) (int64, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE delegation_grants SET revoked_at = now()
		 WHERE parent_session_id = $1::uuid AND revoked_at IS NULL`, parentID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DelegationRule is a standing "always" approval of an engine on a runtime and daemon.
type DelegationRule struct {
	ID, AccountID, Engine, Runtime, DaemonID, DaemonName string
	LocalOnly                                            bool
	CreatedVia                                           string
	CreatedAt                                            time.Time
}

// InsertDelegationRuleTx inserts a rule inside the decision's transaction.
func InsertDelegationRuleTx(ctx context.Context, tx pgx.Tx, r DelegationRule) (DelegationRule, error) {
	return insertDelegationRule(ctx, tx, r)
}

// InsertDelegationRule inserts a rule.
func InsertDelegationRule(ctx context.Context, pool *pgxpool.Pool, r DelegationRule) (DelegationRule, error) {
	return insertDelegationRule(ctx, pool, r)
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func insertDelegationRule(ctx context.Context, q rowQuerier, r DelegationRule) (DelegationRule, error) {
	var out DelegationRule
	err := q.QueryRow(ctx, `
		WITH ins AS (
		  INSERT INTO delegation_rules (account_id, engine, runtime, daemon_id, local_only, created_via)
		  VALUES ($1, $2, $3, $4::uuid, $5, $6)
		  RETURNING id, account_id, engine, runtime, daemon_id, local_only, created_via, created_at)
		SELECT ins.id::text, ins.account_id, ins.engine, ins.runtime, ins.daemon_id::text, d.name,
		       ins.local_only, ins.created_via, ins.created_at
		  FROM ins JOIN daemons d ON d.id = ins.daemon_id`,
		r.AccountID, r.Engine, r.Runtime, r.DaemonID, r.LocalOnly, r.CreatedVia,
	).Scan(&out.ID, &out.AccountID, &out.Engine, &out.Runtime, &out.DaemonID, &out.DaemonName,
		&out.LocalOnly, &out.CreatedVia, &out.CreatedAt)
	return out, err
}

func scanDelegationRules(rows pgx.Rows) ([]DelegationRule, error) {
	defer rows.Close()
	var out []DelegationRule
	for rows.Next() {
		var r DelegationRule
		if err := rows.Scan(&r.ID, &r.AccountID, &r.Engine, &r.Runtime, &r.DaemonID, &r.DaemonName,
			&r.LocalOnly, &r.CreatedVia, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const delegationRuleSelect = `
	SELECT r.id::text, r.account_id, r.engine, r.runtime, r.daemon_id::text, d.name, r.local_only, r.created_via, r.created_at
	  FROM delegation_rules r JOIN daemons d ON d.id = r.daemon_id`

// ListDelegationRules returns the account's live rules with the daemon's name, oldest first.
func ListDelegationRules(ctx context.Context, pool *pgxpool.Pool, account string) ([]DelegationRule, error) {
	rows, err := pool.Query(ctx, delegationRuleSelect+`
		 WHERE r.account_id = $1 AND r.revoked_at IS NULL
		 ORDER BY r.created_at, r.id`, account)
	if err != nil {
		return nil, err
	}
	return scanDelegationRules(rows)
}

// CandidateDelegationRules: live rules of the account for engine+runtime+daemon (local_only is the
// server's to check).
func CandidateDelegationRules(ctx context.Context, pool Querier, account, engine, runtime, daemonID string) ([]DelegationRule, error) {
	rows, err := pool.Query(ctx, delegationRuleSelect+`
		 WHERE r.account_id = $1 AND r.revoked_at IS NULL AND r.engine = $2 AND r.runtime = $3
		   AND r.daemon_id = $4::uuid
		 ORDER BY r.created_at, r.id`, account, engine, runtime, daemonID)
	if err != nil {
		return nil, err
	}
	return scanDelegationRules(rows)
}

// RevokeDelegationRule revokes a live rule of the account; false when there was none.
func RevokeDelegationRule(ctx context.Context, pool *pgxpool.Pool, id, account string) (bool, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE delegation_rules SET revoked_at = now()
		 WHERE id = $1::uuid AND account_id = $2 AND revoked_at IS NULL`, id, account)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ChildSession is one child of a parent, as the parent's Live tab and the sidebar show it.
type ChildSession struct {
	ID, Title, Engine, Model, Effort, Runtime, Status, DelegationVia string
	SpawningAccountID                                                string
	StartedAt                                                        time.Time
	EndedAt                                                          *time.Time
	KeepRunning                                                      bool
	OpenAsks                                                         int
}

// ListChildSessions returns the parent's children, started_at ascending.
func ListChildSessions(ctx context.Context, pool *pgxpool.Pool, parentID string) ([]ChildSession, error) {
	rows, err := pool.Query(ctx, `
		SELECT s.id::text, COALESCE(s.title, ''), COALESCE(s.engine, ''), COALESCE(s.model, ''),
		       COALESCE(s.effort, ''), COALESCE(s.runtime, ''), s.status, COALESCE(s.delegation_via, ''),
		       s.started_at, s.ended_at, s.keep_running,
		       (SELECT count(*) FROM messages m WHERE m.session_id = s.id AND m.kind = 'ask' AND m.status = 'open' AND NOT m.no_wait),
		       COALESCE(s.spawning_account_id,'')
		  FROM sessions s
		 WHERE s.parent_session_id = $1::uuid AND s.archived_at IS NULL
		 ORDER BY s.started_at, s.id`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChildSession
	for rows.Next() {
		var c ChildSession
		if err := rows.Scan(&c.ID, &c.Title, &c.Engine, &c.Model, &c.Effort, &c.Runtime, &c.Status,
			&c.DelegationVia, &c.StartedAt, &c.EndedAt, &c.KeepRunning, &c.OpenAsks, &c.SpawningAccountID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChildrenDigest is a cheap fingerprint of a parent's children: it changes when a child is added,
// changes status, ends, asks, or appends an event.
type ChildrenDigest struct {
	Count      int
	Statuses   string // string_agg(status, ',' ORDER BY id)
	MaxEndedAt *time.Time
	OpenAsks   int
	MaxSeq     int64 // max agent_events.seq over the children (LATERAL per child)
}

// ReadChildrenDigest reads the digest of the parent's children.
func ReadChildrenDigest(ctx context.Context, pool *pgxpool.Pool, parentID string) (ChildrenDigest, error) {
	var d ChildrenDigest
	err := pool.QueryRow(ctx, `
		SELECT count(*)::int,
		       COALESCE(string_agg(s.status, ',' ORDER BY s.id), ''),
		       max(s.ended_at),
		       COALESCE(sum(a.n), 0)::int,
		       COALESCE(max(e.seq), 0)::bigint
		  FROM sessions s
		  LEFT JOIN LATERAL (SELECT count(*) AS n FROM messages m
		                      WHERE m.session_id = s.id AND m.kind = 'ask' AND m.status = 'open' AND NOT m.no_wait) a ON true
		  LEFT JOIN LATERAL (SELECT max(ev.seq) AS seq FROM agent_events ev WHERE ev.session_id = s.id) e ON true
		 WHERE s.parent_session_id = $1::uuid`, parentID,
	).Scan(&d.Count, &d.Statuses, &d.MaxEndedAt, &d.OpenAsks, &d.MaxSeq)
	return d, err
}

func countOne(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) (int, error) {
	var n int
	err := pool.QueryRow(ctx, sql, args...).Scan(&n)
	return n, err
}

// CountRunningChildren counts the parent's children that have not ended ("running" = ended_at IS NULL).
func CountRunningChildren(ctx context.Context, pool *pgxpool.Pool, parentID string) (int, error) {
	return countOne(ctx, pool, `SELECT count(*)::int FROM sessions WHERE parent_session_id = $1::uuid AND ended_at IS NULL`, parentID)
}

// ParentStartWindow counts a parent's starts and approval attempts in the rolling
// window. Linked requests count once, via the child; failed launches do not count.
// Pending, denied, expired and allowed-but-unstarted requests consume rate budget
// too, preventing approval loops. excludeRequest is the approval currently being
// started, which already reserved its slot. The returned timestamp is the start
// whose expiry leaves room under limit, even if configuration lowered the limit.
func ParentStartWindow(ctx context.Context, pool *pgxpool.Pool, parentID string, since time.Time, excludeRequest string, limit int) (int, *time.Time, error) {
	var n int
	var freeingStart *time.Time
	err := pool.QueryRow(ctx, `
		WITH starts AS (
		  SELECT started_at AS at FROM sessions
		   WHERE parent_session_id = $1::uuid AND started_at > $2
		     AND end_reason IS DISTINCT FROM '`+EndReasonStartFailed+`'
		  UNION ALL
		  SELECT created_at FROM delegation_requests
		   WHERE parent_session_id = $1::uuid AND created_at > $2
		     AND id::text <> $3 AND (status <> 'allowed' OR child_session_id IS NULL)
		)
		SELECT (SELECT count(*)::int FROM starts),
		       (SELECT at FROM starts ORDER BY at
		        OFFSET greatest((SELECT count(*) FROM starts) - $4, 0) LIMIT 1)
	`, parentID, since, excludeRequest, limit).Scan(&n, &freeingStart)
	return n, freeingStart, err
}

// CountAccountRunningChildren counts the children of the account's sessions that have not ended.
func CountAccountRunningChildren(ctx context.Context, pool *pgxpool.Pool, account string) (int, error) {
	return countOne(ctx, pool, `
		SELECT count(*)::int FROM sessions
		 WHERE parent_session_id IS NOT NULL AND spawning_account_id = $1 AND ended_at IS NULL`, account)
}

// AccountStartsSince counts the starts for the account since t, and the oldest of them (for
// Retry-After): its children, plus its requests that are pending, denied or expired, or allowed
// without a child (an allowed request with a child is already counted as that child). A child whose
// launch failed (end_reason start_failed) is not a start.
func AccountStartsSince(ctx context.Context, pool *pgxpool.Pool, account string, since time.Time) (int, *time.Time, error) {
	var n int
	var oldest *time.Time
	err := pool.QueryRow(ctx, `
		SELECT count(*)::int, min(at) FROM (
		  SELECT started_at AS at FROM sessions
		   WHERE parent_session_id IS NOT NULL AND spawning_account_id = $1 AND started_at >= $2
		     AND end_reason IS DISTINCT FROM '`+EndReasonStartFailed+`'
		  UNION ALL
		  SELECT created_at FROM delegation_requests
		   WHERE account_id = $1 AND created_at >= $2 AND (status <> 'allowed' OR child_session_id IS NULL)
		) x`, account, since).Scan(&n, &oldest)
	return n, oldest, err
}

// ParentOf returns the id of the session's parent, "" when it has none (or does not exist).
func ParentOf(ctx context.Context, pool *pgxpool.Pool, sessionID string) (string, error) {
	var p string
	err := pool.QueryRow(ctx, `SELECT COALESCE(parent_session_id::text, '') FROM sessions WHERE id = $1::uuid`, sessionID).Scan(&p)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return p, err
}

// OrphanedChildren includes recoverable children, not just rows without ended_at.
// Already-finalized children are handled by the caller's repeat-kill check.
func OrphanedChildren(ctx context.Context, pool *pgxpool.Pool, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT c.id::text
		  FROM sessions c LEFT JOIN sessions p ON p.id = c.parent_session_id
		 WHERE c.id = ANY($1::uuid[]) AND NOT COALESCE((`+terminallyEndedSQL("c")+`),false)
		   AND (`+delegationResumeBlockedSQL("c", "p")+`)
		 ORDER BY c.started_at, c.id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ChildrenToStop includes recoverable child lifetimes; a lost daemon's timestamp
// must not hide a child that could subsequently be adopted again.
func ChildrenToStop(ctx context.Context, pool *pgxpool.Pool, parentID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.id::text FROM sessions c
		 WHERE c.parent_session_id = $1::uuid AND NOT c.keep_running
		   AND NOT COALESCE((`+terminallyEndedSQL("c")+`),false)
		 ORDER BY started_at, id`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
