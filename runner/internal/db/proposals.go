package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Proposal states (mcp_proposals.state, migration 026).
const (
	ProposalPending   = "pending"
	ProposalExecuting = "executing"
	ProposalDone      = "done"
	ProposalRejected  = "rejected"
	ProposalExpired   = "expired"
	ProposalFailed    = "failed"
	ProposalUnknown   = "unknown"
)

// ProposalExpiry is how long a proposal stays pending: the sweeper expires older ones, and the
// approval refuses them even before the sweeper runs.
const ProposalExpiry = 7 * 24 * time.Hour

// MaxOpenProposalsListed bounds the unpaged open list (pending, executing, unknown). Pending ones
// are already capped per account at insert; this bounds executing and unknown, and never cuts a
// pending one because pending rows sort first.
const MaxOpenProposalsListed = 500

// MaxProposalArgumentsBytes is the most frozen argument JSON a proposal may hold (the
// column's CHECK is 64 KiB of stored jsonb).
const MaxProposalArgumentsBytes = 64 << 10

var (
	// ErrProposalNotFound is returned when a proposal does not exist, is malformed, or (for the
	// owner-scoped helpers) belongs to another account: the three are deliberately the same.
	ErrProposalNotFound = errors.New("proposal not found")
	// ErrProposalState is returned when a proposal exists but is not in the state the transition
	// needs, for example a second approval of one that is already executing.
	ErrProposalState = errors.New("proposal is not in the required state")
	// ErrProposalCap is returned by InsertProposal when the account already has the maximum
	// number of pending proposals.
	ErrProposalCap = errors.New("too many pending proposals")
	// ErrProposalSessionCap and ErrProposalCronCap are returned by InsertProposalLimited when one
	// session, or one cron across its sessions, already has its share of pending proposals.
	ErrProposalSessionCap = errors.New("too many pending proposals for this session")
	ErrProposalCronCap    = errors.New("too many pending proposals for this cron")
	// ErrProposalCursor is returned by ListDecidedProposals for a malformed cursor.
	ErrProposalCursor = errors.New("invalid cursor")
	// ErrProposalArguments is returned when the frozen arguments cannot be stored: over the size
	// cap, or JSON the database rejects.
	ErrProposalArguments = errors.New("proposal arguments cannot be stored")
)

var proposalUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Proposal is a row of mcp_proposals. Arguments is the frozen call, the authoritative record.
// SessionID and CronID are "" when the column is NULL.
type Proposal struct {
	ID             string
	AccountID      string
	SessionID      string
	CronID         string
	ConnectionID   string
	ConnectionName string
	URLSnapshot    string
	Tool           string
	ToolHash       string
	Arguments      json.RawMessage
	AgentSummary   string
	State          string
	Result         json.RawMessage
	CreatedAt      time.Time
	DecidedAt      *time.Time
	DecidedBy      *string
}

const proposalColumns = `id::text, account_id, COALESCE(session_id::text, ''), COALESCE(cron_id::text, ''),
	connection_id::text, connection_name, url_snapshot, tool, tool_hash, arguments::text,
	COALESCE(agent_summary, ''), state, result::text, created_at, decided_at, decided_by`

func scanProposal(row pgx.Row) (*Proposal, error) {
	var p Proposal
	var args string
	var result *string
	if err := row.Scan(&p.ID, &p.AccountID, &p.SessionID, &p.CronID, &p.ConnectionID, &p.ConnectionName,
		&p.URLSnapshot, &p.Tool, &p.ToolHash, &args, &p.AgentSummary, &p.State, &result,
		&p.CreatedAt, &p.DecidedAt, &p.DecidedBy); err != nil {
		return nil, err
	}
	p.Arguments = json.RawMessage(args)
	if result != nil {
		p.Result = json.RawMessage(*result)
	}
	return &p, nil
}

// ProposalLimits are the caps InsertProposalLimited enforces on pending proposals; zero means
// no cap at that level.
type ProposalLimits struct{ Account, Session, Cron int }

// InsertProposal stores a new pending proposal with only the per-account cap (see
// InsertProposalLimited).
func InsertProposal(ctx context.Context, pool *pgxpool.Pool, p Proposal, maxPending int) (*Proposal, error) {
	return InsertProposalLimited(ctx, pool, p, ProposalLimits{Account: maxPending})
}

// InsertProposalLimited stores a new pending proposal, refusing with ErrProposalCap when the
// account already has lim.Account pending, ErrProposalSessionCap when the session has lim.Session
// and ErrProposalCronCap when the cron the session belongs to has lim.Cron. The counts and the
// insert run under one per-account advisory lock, so concurrent proposals cannot overshoot a cap.
// cron_id is taken from the session's row.
func InsertProposalLimited(ctx context.Context, pool *pgxpool.Pool, p Proposal, lim ProposalLimits) (*Proposal, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("InsertProposal begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is returned
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('mcp_proposals:' || $1))`, p.AccountID); err != nil {
		return nil, fmt.Errorf("InsertProposal lock: %w", err)
	}
	var pending int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM mcp_proposals WHERE account_id = $1 AND state = 'pending'`,
		p.AccountID).Scan(&pending); err != nil {
		return nil, fmt.Errorf("InsertProposal count: %w", err)
	}
	if lim.Account > 0 && pending >= lim.Account {
		return nil, ErrProposalCap
	}
	if p.SessionID != "" && proposalUUID.MatchString(p.SessionID) {
		var n int
		var cronID *string
		if lim.Session > 0 {
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM mcp_proposals WHERE account_id = $1 AND state = 'pending' AND session_id = $2::uuid`,
				p.AccountID, p.SessionID).Scan(&n); err != nil {
				return nil, fmt.Errorf("InsertProposal session count: %w", err)
			}
			if n >= lim.Session {
				return nil, ErrProposalSessionCap
			}
		}
		if lim.Cron > 0 {
			err := tx.QueryRow(ctx, `SELECT cron_id::text FROM sessions WHERE id = $1::uuid`, p.SessionID).Scan(&cronID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("InsertProposal cron lookup: %w", err)
			}
			if cronID != nil {
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM mcp_proposals WHERE account_id = $1 AND state = 'pending' AND cron_id = $2::uuid`,
					p.AccountID, *cronID).Scan(&n); err != nil {
					return nil, fmt.Errorf("InsertProposal cron count: %w", err)
				}
				if n >= lim.Cron {
					return nil, ErrProposalCronCap
				}
			}
		}
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO mcp_proposals
		  (account_id, session_id, cron_id, connection_id, connection_name, url_snapshot, tool, tool_hash,
		   arguments, agent_summary)
		VALUES ($1, NULLIF($2, '')::uuid, (SELECT cron_id FROM sessions WHERE id = NULLIF($2, '')::uuid),
		        $3::uuid, $4, $5, $6, $7, $8::jsonb, NULLIF($9, ''))
		RETURNING `+proposalColumns,
		p.AccountID, p.SessionID, p.ConnectionID, p.ConnectionName, p.URLSnapshot, p.Tool, p.ToolHash,
		string(p.Arguments), p.AgentSummary)
	out, err := scanProposal(row)
	if err != nil {
		var pg *pgconn.PgError
		// 23514: the size CHECK. Class 22: data the database cannot store as jsonb (for example a
		// \u0000 escape).
		if errors.As(err, &pg) && (pg.Code == "23514" || strings.HasPrefix(pg.Code, "22")) {
			return nil, ErrProposalArguments
		}
		return nil, fmt.Errorf("InsertProposal: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("InsertProposal commit: %w", err)
	}
	return out, nil
}

// GetOwnedProposal reads one of the account's proposals. ErrProposalNotFound when it does not
// exist or is another account's.
func GetOwnedProposal(ctx context.Context, pool *pgxpool.Pool, accountID, id string) (*Proposal, error) {
	if !proposalUUID.MatchString(id) {
		return nil, ErrProposalNotFound
	}
	p, err := scanProposal(pool.QueryRow(ctx,
		`SELECT `+proposalColumns+` FROM mcp_proposals WHERE id = $1::uuid AND account_id = $2`, id, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProposalNotFound
	}
	return p, err
}

// ListOwnedProposals lists the account's proposals, newest first, optionally only those in
// state (empty: every state). It is capped at limit rows and is for callers that want a bounded
// sample; the API lists with ListOpenProposals and ListDecidedProposals.
func ListOwnedProposals(ctx context.Context, pool *pgxpool.Pool, accountID, state string, limit int) ([]Proposal, error) {
	if limit <= 0 {
		limit = 100
	}
	return queryProposals(ctx, pool, `SELECT `+proposalColumns+` FROM mcp_proposals
		WHERE account_id = $1 AND ($2 = '' OR state = $2) ORDER BY created_at DESC, id DESC LIMIT $3`,
		accountID, state, limit)
}

func queryProposals(ctx context.Context, pool *pgxpool.Pool, q string, args ...any) ([]Proposal, error) {
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	defer rows.Close()
	out := []Proposal{}
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, fmt.Errorf("list proposals scan: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ListOpenProposals lists the account's open proposals (pending, executing, unknown), unpaged:
// pending first, then executing, then unknown, each newest first. The pending cap keeps the
// first group small; the whole list is bounded by MaxOpenProposalsListed, cutting unknown ones
// last. state, when set, narrows it to one of the three.
func ListOpenProposals(ctx context.Context, pool *pgxpool.Pool, accountID, state string) ([]Proposal, error) {
	return queryProposals(ctx, pool, `SELECT `+proposalColumns+` FROM mcp_proposals
		WHERE account_id = $1 AND state IN ('pending','executing','unknown') AND ($2 = '' OR state = $2)
		ORDER BY CASE state WHEN 'pending' THEN 0 WHEN 'executing' THEN 1 ELSE 2 END, created_at DESC, id DESC
		LIMIT $3`, accountID, state, MaxOpenProposalsListed)
}

// DecidedCursor is the cursor after row p: where the next page of ListDecidedProposals starts.
func DecidedCursor(p *Proposal) string {
	return p.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + p.ID
}

func parseDecidedCursor(c string) (time.Time, string, error) {
	ts, id, ok := strings.Cut(c, "|")
	if !ok || !proposalUUID.MatchString(id) {
		return time.Time{}, "", ErrProposalCursor
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, "", ErrProposalCursor
	}
	return t, id, nil
}

// ListDecidedProposals lists the account's decided proposals (done, rejected, expired, failed),
// newest first, one page of at most limit (default 50, at most 100) after the cursor ("" for the
// first page). next is the cursor for the following page, "" when this was the last. state, when
// set, narrows the list to one decided state.
func ListDecidedProposals(ctx context.Context, pool *pgxpool.Pool, accountID, state, cursor string, limit int) ([]Proposal, string, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 100)
	var before any
	var beforeID any
	if cursor != "" {
		t, id, err := parseDecidedCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		before, beforeID = t, id
	}
	rows, err := queryProposals(ctx, pool, `SELECT `+proposalColumns+` FROM mcp_proposals
		WHERE account_id = $1 AND state IN ('done','rejected','expired','failed') AND ($2 = '' OR state = $2)
		  AND ($3::timestamptz IS NULL OR (created_at, id) < ($3::timestamptz, $4::uuid))
		ORDER BY created_at DESC, id DESC LIMIT $5`, accountID, state, before, beforeID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = DecidedCursor(&rows[limit-1])
	}
	return rows, next, nil
}

// CountPendingProposals is how many proposals the account has waiting for a decision.
func CountPendingProposals(ctx context.Context, pool *pgxpool.Pool, accountID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM mcp_proposals WHERE account_id = $1 AND state = 'pending'`, accountID).Scan(&n)
	return n, err
}

// proposalMiss says why an owner-scoped transition matched no row: the proposal is not the
// account's (ErrProposalNotFound) or is in another state (ErrProposalState).
func proposalMiss(ctx context.Context, pool *pgxpool.Pool, accountID, id string) error {
	var one int
	err := pool.QueryRow(ctx, `SELECT 1 FROM mcp_proposals WHERE id = $1::uuid AND account_id = $2`, id, accountID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProposalNotFound
	}
	if err != nil {
		return err
	}
	return ErrProposalState
}

// transitionOwned moves an owner's proposal from one state to another atomically. set names
// extra assignments (with their arguments numbered from $5).
func transitionOwned(ctx context.Context, pool *pgxpool.Pool, accountID, id, from, to, set string, args ...any) (*Proposal, error) {
	return transitionOwnedWhere(ctx, pool, accountID, id, from, to, set, "", args...)
}

// transitionOwnedWhere is transitionOwned with an extra condition (written without parameters).
func transitionOwnedWhere(ctx context.Context, pool *pgxpool.Pool, accountID, id, from, to, set, where string, args ...any) (*Proposal, error) {
	if !proposalUUID.MatchString(id) {
		return nil, ErrProposalNotFound
	}
	q := `UPDATE mcp_proposals SET state = $4` + set + ` WHERE id = $1::uuid AND account_id = $2 AND state = $3` + where + ` RETURNING ` + proposalColumns
	p, err := scanProposal(pool.QueryRow(ctx, q, append([]any{id, accountID, from, to}, args...)...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, proposalMiss(ctx, pool, accountID, id)
	}
	return p, err
}

// BeginProposalExecution is the approval's compare-and-set: pending -> executing for the owner,
// recording who decided and when. Exactly one of any number of concurrent approvals wins; the
// rest get ErrProposalState. A proposal older than ProposalExpiry is refused even when the
// sweeper has not expired it yet.
func BeginProposalExecution(ctx context.Context, pool *pgxpool.Pool, accountID, id, decidedBy string) (*Proposal, error) {
	return transitionOwnedWhere(ctx, pool, accountID, id, ProposalPending, ProposalExecuting,
		`, decided_by = $5, decided_at = now()`,
		fmt.Sprintf(` AND created_at > now() - make_interval(secs => %d)`, int64(ProposalExpiry.Seconds())), decidedBy)
}

// RejectProposal moves a pending proposal to rejected.
func RejectProposal(ctx context.Context, pool *pgxpool.Pool, accountID, id, decidedBy string) (*Proposal, error) {
	return transitionOwned(ctx, pool, accountID, id, ProposalPending, ProposalRejected,
		`, decided_by = $5, decided_at = now()`, decidedBy)
}

// ResolveProposal records a human's decision about a proposal whose outcome was unknown: what
// really happened, done or failed. Only the owner, only from unknown.
func ResolveProposal(ctx context.Context, pool *pgxpool.Pool, accountID, id, outcome, decidedBy string) (*Proposal, error) {
	if outcome != ProposalDone && outcome != ProposalFailed {
		return nil, fmt.Errorf("a proposal is resolved to %q or %q, not %q", ProposalDone, ProposalFailed, outcome)
	}
	return transitionOwned(ctx, pool, accountID, id, ProposalUnknown, outcome,
		`, decided_by = $5, decided_at = now()`, decidedBy)
}

// FinishProposal records how an approval ended: executing -> done, failed or unknown, with the
// (already sanitised) result. The approval holds the executing state, so it needs no owner check.
//
// The transition is ALWAYS recorded: the tool already ran, so a result the database rejects as
// jsonb (a \u0000 escape, invalid JSON) must not leave the row executing. The update is retried
// with a minimal result that is known to be storable.
func FinishProposal(ctx context.Context, pool *pgxpool.Pool, id, state string, result json.RawMessage) (*Proposal, error) {
	if state != ProposalDone && state != ProposalFailed && state != ProposalUnknown {
		return nil, fmt.Errorf("an approval ends as %q, %q or %q, not %q", ProposalDone, ProposalFailed, ProposalUnknown, state)
	}
	if !proposalUUID.MatchString(id) {
		return nil, ErrProposalNotFound
	}
	var res *string
	if len(result) > 0 {
		s := string(result)
		res = &s
	}
	p, err := finishProposal(ctx, pool, id, state, res)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && strings.HasPrefix(pg.Code, "22") {
		fallback := `{"error":"result could not be stored"}`
		p, err = finishProposal(ctx, pool, id, state, &fallback)
	}
	return p, err
}

func finishProposal(ctx context.Context, pool *pgxpool.Pool, id, state string, res *string) (*Proposal, error) {
	p, err := scanProposal(pool.QueryRow(ctx, `UPDATE mcp_proposals SET state = $2, result = $3::jsonb
		WHERE id = $1::uuid AND state = 'executing' RETURNING `+proposalColumns, id, state, res))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProposalState
	}
	return p, err
}

// ReleaseProposal puts an executing proposal back to pending, clearing the decision: used when
// the approval found, after winning the compare-and-set, that nothing was sent (a snapshot check
// that could not be made before it).
func ReleaseProposal(ctx context.Context, pool *pgxpool.Pool, id string) (*Proposal, error) {
	if !proposalUUID.MatchString(id) {
		return nil, ErrProposalNotFound
	}
	p, err := scanProposal(pool.QueryRow(ctx, `UPDATE mcp_proposals SET state = 'pending', decided_at = NULL, decided_by = NULL, result = NULL
		WHERE id = $1::uuid AND state = 'executing' RETURNING `+proposalColumns, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProposalState
	}
	return p, err
}

// FailPendingProposals marks the account's pending proposals for the given connections failed,
// with reason as the result. It is how the sweeper closes the proposals of a connection that no
// longer exists. It returns how many it changed.
func FailPendingProposals(ctx context.Context, pool *pgxpool.Pool, accountID string, connectionIDs []string, reason string) (int64, error) {
	if len(connectionIDs) == 0 {
		return 0, nil
	}
	res, _ := json.Marshal(map[string]string{"error": reason})
	tag, err := pool.Exec(ctx, `UPDATE mcp_proposals SET state = 'failed', result = $3::jsonb, decided_at = now()
		WHERE account_id = $1 AND state = 'pending' AND connection_id = ANY($2::uuid[])`,
		accountID, connectionIDs, string(res))
	if err != nil {
		return 0, fmt.Errorf("FailPendingProposals: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ExpirePendingProposals marks proposals still pending after maxAge expired (spec 9: 7 days).
func ExpirePendingProposals(ctx context.Context, pool *pgxpool.Pool, maxAge time.Duration) (int64, error) {
	tag, err := pool.Exec(ctx, `UPDATE mcp_proposals SET state = 'expired', decided_at = now()
		WHERE state = 'pending' AND created_at < now() - make_interval(secs => $1)`, maxAge.Seconds())
	if err != nil {
		return 0, fmt.Errorf("ExpirePendingProposals: %w", err)
	}
	return tag.RowsAffected(), nil
}

// UnknownStaleExecuting ends approvals that have been executing longer than maxAge: the runner
// stopped (or lost the request) between the compare-and-set and the recording of the outcome, so
// whether the action ran is not known. They become unknown, never pending and never retried.
func UnknownStaleExecuting(ctx context.Context, pool *pgxpool.Pool, maxAge time.Duration) (int64, error) {
	res, _ := json.Marshal(map[string]string{"error": "the approval did not finish: the runner stopped before it recorded an outcome, so it is not known whether the action ran"})
	tag, err := pool.Exec(ctx, `UPDATE mcp_proposals SET state = 'unknown', result = $2::jsonb
		WHERE state = 'executing' AND decided_at < now() - make_interval(secs => $1)`, maxAge.Seconds(), string(res))
	if err != nil {
		return 0, fmt.Errorf("UnknownStaleExecuting: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PruneDecidedProposals deletes decided proposals (done, rejected, expired, failed) whose decision
// is older than retention (spec 9: 30 days). Open ones (pending, executing) and unknown ones,
// which still wait for a person to say what happened, are never pruned.
func PruneDecidedProposals(ctx context.Context, pool *pgxpool.Pool, retention time.Duration) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM mcp_proposals
		WHERE state IN ('done','rejected','expired','failed') AND decided_at < now() - make_interval(secs => $1)`,
		retention.Seconds())
	if err != nil {
		return 0, fmt.Errorf("PruneDecidedProposals: %w", err)
	}
	return tag.RowsAffected(), nil
}
