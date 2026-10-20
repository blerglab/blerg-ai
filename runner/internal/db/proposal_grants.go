package db

// Session permissions for proposals (migration 067). A grant lets one session's proposals of one
// narrow scope be approved without asking: board cards on one board, or one MCP tool of one
// connection. The approval itself still runs the person's whole approval path; this file only
// stores and finds the grants.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Proposal grant kinds.
const (
	ProposalGrantBoard = "board_file"
	ProposalGrantMCP   = "mcp_tool"
)

// ErrProposalGrantSession is returned when the session is not the account's or can no longer
// hold a grant (ended, discarded or archived).
var ErrProposalGrantSession = errors.New("the session cannot hold this permission")

// ProposalGrant is a row of session_proposal_grants. ProofSessionID is the creator's login session
// and never leaves the server.
type ProposalGrant struct {
	ID                  string
	SessionID           string
	AccountID           string
	Kind                string
	BoardID             string
	BoardName           string
	ConnectionID        string
	ConnectionName      string
	Tool                string
	ToolHash            string // an MCP grant's pinned tool definition; "" for a board grant
	ProofSessionID      string
	ProjectScope        string
	CreatedVia          string
	CreatedFromProposal string
	CreatedAt           time.Time
	RevokedAt           *time.Time
}

const proposalGrantColumns = `g.id::text, g.session_id::text, g.account_id, g.kind, g.board_id, g.board_name,
	COALESCE(g.connection_id::text, ''), g.connection_name, g.tool, g.tool_hash, g.proof_session_id, g.project_scope,
	g.created_via, COALESCE(g.created_from_proposal::text, ''), g.created_at, g.revoked_at`

func scanProposalGrant(row pgx.Row) (*ProposalGrant, error) {
	var g ProposalGrant
	if err := row.Scan(&g.ID, &g.SessionID, &g.AccountID, &g.Kind, &g.BoardID, &g.BoardName, &g.ConnectionID,
		&g.ConnectionName, &g.Tool, &g.ToolHash, &g.ProofSessionID, &g.ProjectScope, &g.CreatedVia, &g.CreatedFromProposal,
		&g.CreatedAt, &g.RevokedAt); err != nil {
		return nil, err
	}
	return &g, nil
}

// proposalGrantLiveSQL holds for a grant g of session s (with its parent p, left-joined) that may
// still approve: not revoked, the session still its owner's, not discarded, archived or ended for
// good, the grant made after the session's last end, and no discard of its parent.
const proposalGrantLiveSQL = `g.revoked_at IS NULL AND s.spawning_account_id = g.account_id
	AND s.discard_requested_at IS NULL AND s.archived_at IS NULL AND p.discard_requested_at IS NULL
	AND NOT COALESCE((` + `s.ended_at IS NOT NULL AND (s.end_reason IN
		('stopped_by_user','stopped_by_agent','auto_stopped','process_exited',
		 'process_failed','start_failed','parent_ended') OR (s.end_reason='daemon_lost' AND s.status='stopped'))` + `), false)
	AND (s.delegation_ended_at IS NULL OR g.created_at > s.delegation_ended_at)`

const proposalGrantFrom = ` FROM session_proposal_grants g JOIN sessions s ON s.id = g.session_id
	LEFT JOIN sessions p ON p.id = s.parent_session_id `

// InsertProposalGrant stores a live grant for the session, or returns the live grant that already
// has exactly this scope (a second "allow" never widens or duplicates one). The session must be
// the account's and able to hold it (ErrProposalGrantSession otherwise); the caller has checked
// the rest of the policy.
func InsertProposalGrant(ctx context.Context, pool *pgxpool.Pool, g ProposalGrant) (*ProposalGrant, error) {
	if !proposalUUID.MatchString(g.SessionID) {
		return nil, ErrProposalGrantSession
	}
	switch {
	case g.Kind == ProposalGrantBoard && g.BoardID != "" && g.ConnectionID == "" && g.Tool == "" && g.ToolHash == "":
	case g.Kind == ProposalGrantMCP && g.BoardID == "" && proposalUUID.MatchString(g.ConnectionID) && g.Tool != "" && g.ToolHash != "":
	default:
		return nil, fmt.Errorf("invalid proposal grant scope")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	var owner string
	var dead bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(s.spawning_account_id, ''),
		s.discard_requested_at IS NOT NULL OR s.archived_at IS NOT NULL OR COALESCE(p.discard_requested_at IS NOT NULL, false)
		  OR COALESCE((`+terminallyEndedSQL("s")+`), false)
		FROM sessions s LEFT JOIN sessions p ON p.id = s.parent_session_id WHERE s.id = $1::uuid FOR UPDATE OF s`, g.SessionID).Scan(&owner, &dead)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (owner == "" || owner != g.AccountID || dead) {
		return nil, ErrProposalGrantSession
	}
	if err != nil {
		return nil, err
	}
	// A live grant from before the session's last end is an obsolete lifetime: retire it so the
	// unique live scope is free.
	if _, err := tx.Exec(ctx, `UPDATE session_proposal_grants g SET revoked_at = now(), revoked_reason = 'session_ended'
		FROM sessions s WHERE s.id = g.session_id AND g.session_id = $1::uuid AND g.revoked_at IS NULL
		  AND s.delegation_ended_at IS NOT NULL AND g.created_at <= s.delegation_ended_at`, g.SessionID); err != nil {
		return nil, err
	}
	existing, err := scanProposalGrant(tx.QueryRow(ctx, `SELECT `+proposalGrantColumns+proposalGrantFrom+`
		WHERE g.session_id = $1::uuid AND g.kind = $2 AND g.board_id = $3 AND COALESCE(g.connection_id::text, '') = $4
		  AND g.tool = $5 AND g.revoked_at IS NULL`, g.SessionID, g.Kind, g.BoardID, g.ConnectionID, g.Tool))
	switch {
	case err == nil && existing.ProofSessionID == g.ProofSessionID && existing.ToolHash == g.ToolHash:
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return existing, nil
	case err == nil:
		// The same scope given again from another sign-in, or on a changed tool definition: the new
		// one replaces it (the old one may be dormant: its sign-in ended, or its definition changed).
		if _, err := tx.Exec(ctx, `UPDATE session_proposal_grants SET revoked_at = now(), revoked_reason = 'renewed' WHERE id = $1::uuid`, existing.ID); err != nil {
			return nil, err
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	var conn any
	if g.ConnectionID != "" {
		conn = g.ConnectionID
	}
	var from any
	if g.CreatedFromProposal != "" {
		from = g.CreatedFromProposal
	}
	out, err := scanProposalGrant(tx.QueryRow(ctx, `WITH g AS (
		INSERT INTO session_proposal_grants (session_id, account_id, kind, board_id, board_name, connection_id,
		  connection_name, tool, tool_hash, proof_session_id, project_scope, created_via, created_from_proposal)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7, $8, $9, $10, $11, $12, $13::uuid) RETURNING *)
		SELECT `+proposalGrantColumns+` FROM g`, g.SessionID, g.AccountID, g.Kind, g.BoardID, g.BoardName, conn,
		g.ConnectionName, g.Tool, g.ToolHash, g.ProofSessionID, g.ProjectScope, g.CreatedVia, from))
	if err != nil {
		return nil, fmt.Errorf("insert proposal grant: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// ListLiveProposalGrants lists the session's grants that may still approve, oldest first.
func ListLiveProposalGrants(ctx context.Context, pool *pgxpool.Pool, sessionID string) ([]ProposalGrant, error) {
	if !proposalUUID.MatchString(sessionID) {
		return []ProposalGrant{}, nil
	}
	rows, err := pool.Query(ctx, `SELECT `+proposalGrantColumns+proposalGrantFrom+`
		WHERE g.session_id = $1::uuid AND `+proposalGrantLiveSQL+` ORDER BY g.created_at, g.id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list proposal grants: %w", err)
	}
	defer rows.Close()
	out := []ProposalGrant{}
	for rows.Next() {
		g, err := scanProposalGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// LiveProposalGrantFor finds the live grant that covers exactly this scope for the session and
// account, or nil. A board grant matches its board only; an MCP grant its connection and tool, at
// the pinned definition (toolHash) it was given on.
func LiveProposalGrantFor(ctx context.Context, pool *pgxpool.Pool, sessionID, accountID, kind, boardID, connectionID, tool, toolHash string) (*ProposalGrant, error) {
	if !proposalUUID.MatchString(sessionID) || accountID == "" {
		return nil, nil
	}
	g, err := scanProposalGrant(pool.QueryRow(ctx, `SELECT `+proposalGrantColumns+proposalGrantFrom+`
		WHERE g.session_id = $1::uuid AND g.account_id = $2 AND g.kind = $3 AND g.board_id = $4
		  AND COALESCE(g.connection_id::text, '') = $5 AND g.tool = $6 AND g.tool_hash = $7 AND `+proposalGrantLiveSQL+`
		ORDER BY g.created_at LIMIT 1`, sessionID, accountID, kind, boardID, connectionID, tool, toolHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return g, err
}

// GetOwnedProposalGrant reads one of the account's grants (live or not), or nil.
func GetOwnedProposalGrant(ctx context.Context, pool *pgxpool.Pool, id, accountID string) (*ProposalGrant, error) {
	if !proposalUUID.MatchString(id) {
		return nil, nil
	}
	g, err := scanProposalGrant(pool.QueryRow(ctx, `SELECT `+proposalGrantColumns+proposalGrantFrom+`
		WHERE g.id = $1::uuid AND g.account_id = $2`, id, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return g, err
}

// RevokeProposalGrant revokes one of the account's grants. It reports whether a live row changed.
func RevokeProposalGrant(ctx context.Context, pool *pgxpool.Pool, id, accountID, reason string) (bool, error) {
	if !proposalUUID.MatchString(id) {
		return false, nil
	}
	tag, err := pool.Exec(ctx, `UPDATE session_proposal_grants SET revoked_at = now(), revoked_reason = $3
		WHERE id = $1::uuid AND account_id = $2 AND revoked_at IS NULL`, id, accountID, reason)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RevokeSessionProposalGrants ends every live grant of the session and, with children, of the
// sessions it started. It returns how many it ended.
func RevokeSessionProposalGrants(ctx context.Context, pool *pgxpool.Pool, sessionID, reason string, children bool) (int64, error) {
	if !proposalUUID.MatchString(sessionID) {
		return 0, nil
	}
	tag, err := pool.Exec(ctx, `UPDATE session_proposal_grants SET revoked_at = now(), revoked_reason = $2
		WHERE revoked_at IS NULL AND (session_id = $1::uuid OR ($3 AND session_id IN
		  (SELECT id FROM sessions WHERE parent_session_id = $1::uuid)))`, sessionID, reason, children)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CountGrantApprovalsSince is how many proposals the grant approved on its own since then.
func CountGrantApprovalsSince(ctx context.Context, pool *pgxpool.Pool, grantID string, since time.Time) (int, error) {
	if !proposalUUID.MatchString(grantID) {
		return 0, nil
	}
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM mcp_proposals WHERE approved_by_grant = $1::uuid AND decided_at >= $2`, grantID, since).Scan(&n)
	return n, err
}

// RecentBoardFilings lists the session's board filings since then that are not closed for good
// (pending, executing, done or unknown), newest first: the candidates a repeated filing returns
// instead of filing again.
func RecentBoardFilings(ctx context.Context, pool *pgxpool.Pool, accountID, sessionID string, since time.Time) ([]Proposal, error) {
	if !proposalUUID.MatchString(sessionID) {
		return nil, nil
	}
	return queryProposals(ctx, pool, `SELECT `+proposalColumns+` FROM mcp_proposals
		WHERE account_id = $1 AND session_id = $2::uuid AND created_at >= $3 AND connection_id = $4::uuid
		  AND connection_name = 'board-file' AND tool = 'file_ticket' AND tool_hash = 'board-file-v1'
		  AND state IN ('pending','executing','done','unknown')
		ORDER BY created_at DESC, id DESC LIMIT 50`, accountID, sessionID, since, BoardFilingConnectionID)
}
