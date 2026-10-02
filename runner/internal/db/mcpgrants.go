package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrMCPGrantNotFound is returned when no grant matches a token hash.
var ErrMCPGrantNotFound = errors.New("mcp grant not found")

// MCPGrantTool is one tool entry of a grant: its mode ("allow" or "propose"; any
// other value, and any tool not listed, means off) and the hash of its
// description and input schema the person confirmed.
type MCPGrantTool struct {
	Mode string `json:"mode"`
	Hash string `json:"hash"`
}

// MCPGrant is a row of session_mcp_grants. The proof is what the gateway presents to
// core when it fetches the connection's credential.
type MCPGrant struct {
	SessionID    string
	ConnectionID string
	Name         string
	AccountID    string
	Tools        map[string]MCPGrantTool
	URLSnapshot  string
	ProofKind    string // "token_id" or "session_id"
	ProofValue   string
	TokenHash    []byte
	CallBudget   int
	CallsUsed    int
	CreatedAt    time.Time
	// Builtin names a connection the runner provides itself instead of core ("board", migration
	// 027); empty for a user's connection. Its ConnectionID is a fixed sentinel and BuiltinRef
	// carries what the built-in needs (for "board", the target board id).
	Builtin    string
	BuiltinRef string
}

const mcpGrantColumns = `session_id::text, connection_id::text, name, account_id, tools, url_snapshot,
	proof_kind, proof_value, token_hash, call_budget, calls_used, created_at, builtin, builtin_ref`

func scanMCPGrant(row pgx.Row) (*MCPGrant, error) {
	var g MCPGrant
	var tools []byte
	if err := row.Scan(&g.SessionID, &g.ConnectionID, &g.Name, &g.AccountID, &tools, &g.URLSnapshot,
		&g.ProofKind, &g.ProofValue, &g.TokenHash, &g.CallBudget, &g.CallsUsed, &g.CreatedAt, &g.Builtin, &g.BuiltinRef); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(tools, &g.Tools); err != nil {
		return nil, fmt.Errorf("decode grant tools: %w", err)
	}
	if g.Tools == nil {
		g.Tools = map[string]MCPGrantTool{}
	}
	return &g, nil
}

// InsertMCPGrants stores the grants in one transaction: all or none. A grant for a
// (session, connection) pair that already exists is an error; delete the session's
// grants first when issuing new tokens on resume (or use ReplaceMCPGrants).
func InsertMCPGrants(ctx context.Context, pool *pgxpool.Pool, grants []MCPGrant) error {
	return storeMCPGrants(ctx, pool, "", grants)
}

// ReplaceMCPGrants deletes the session's grants and stores the new ones in ONE
// transaction, so a failure leaves the old grants (and their tokens) untouched and a
// session is never left without the grants it had.
func ReplaceMCPGrants(ctx context.Context, pool *pgxpool.Pool, sessionID string, grants []MCPGrant) error {
	if sessionID == "" {
		return errors.New("ReplaceMCPGrants: a session id is required")
	}
	return storeMCPGrants(ctx, pool, sessionID, grants)
}

// storeMCPGrants inserts grants in one transaction, first deleting replaceSession's
// grants when that is set.
func storeMCPGrants(ctx context.Context, pool *pgxpool.Pool, replaceSession string, grants []MCPGrant) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("InsertMCPGrants begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is returned
	// A replace (a resume) issues new tokens but must not reset the call budget:
	// what each connection already spent carries over.
	used := map[string]int{}
	if replaceSession != "" {
		rows, err := tx.Query(ctx, `DELETE FROM session_mcp_grants WHERE session_id = $1
			RETURNING connection_id::text, calls_used`, replaceSession)
		if err != nil {
			return fmt.Errorf("ReplaceMCPGrants delete: %w", err)
		}
		for rows.Next() {
			var cid string
			var n int
			if err := rows.Scan(&cid, &n); err != nil {
				rows.Close()
				return fmt.Errorf("ReplaceMCPGrants delete scan: %w", err)
			}
			used[cid] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("ReplaceMCPGrants delete: %w", err)
		}
	}
	for _, g := range grants {
		tools, err := json.Marshal(g.Tools)
		if err != nil {
			return fmt.Errorf("InsertMCPGrants encode tools: %w", err)
		}
		if string(tools) == "null" {
			tools = []byte("{}")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO session_mcp_grants
			  (session_id, connection_id, name, account_id, tools, url_snapshot,
			   proof_kind, proof_value, token_hash, call_budget, calls_used, builtin, builtin_ref)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			g.SessionID, g.ConnectionID, g.Name, g.AccountID, tools, g.URLSnapshot,
			g.ProofKind, g.ProofValue, g.TokenHash, g.CallBudget, used[g.ConnectionID], g.Builtin, g.BuiltinRef); err != nil {
			return fmt.Errorf("InsertMCPGrants insert: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// MCPGrantByTokenHash returns the grant whose gateway token hashes to tokenHash, or
// ErrMCPGrantNotFound.
func MCPGrantByTokenHash(ctx context.Context, pool *pgxpool.Pool, tokenHash []byte) (*MCPGrant, error) {
	g, err := scanMCPGrant(pool.QueryRow(ctx,
		`SELECT `+mcpGrantColumns+` FROM session_mcp_grants WHERE token_hash = $1`, tokenHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMCPGrantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("MCPGrantByTokenHash: %w", err)
	}
	return g, nil
}

// ListMCPGrantsForSession returns a session's grants, ordered by name.
func ListMCPGrantsForSession(ctx context.Context, pool *pgxpool.Pool, sessionID string) ([]MCPGrant, error) {
	rows, err := pool.Query(ctx,
		`SELECT `+mcpGrantColumns+` FROM session_mcp_grants WHERE session_id = $1 ORDER BY name`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("ListMCPGrantsForSession: %w", err)
	}
	defer rows.Close()
	var out []MCPGrant
	for rows.Next() {
		g, err := scanMCPGrant(rows)
		if err != nil {
			return nil, fmt.Errorf("ListMCPGrantsForSession scan: %w", err)
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// DeleteMCPGrantsForSession removes every grant (and so every gateway token) of a
// session, returning how many rows went. Tokens stop working immediately.
func DeleteMCPGrantsForSession(ctx context.Context, pool *pgxpool.Pool, sessionID string) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM session_mcp_grants WHERE session_id = $1`, sessionID)
	if err != nil {
		return 0, fmt.Errorf("DeleteMCPGrantsForSession: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ConsumeMCPCall atomically spends one call from the grant's budget. It reports false
// when the budget is exhausted (or the grant is gone). The conditional UPDATE is the
// only counter, so concurrent callers can never overspend.
func ConsumeMCPCall(ctx context.Context, pool *pgxpool.Pool, sessionID, connectionID string) (bool, error) {
	var used int
	err := pool.QueryRow(ctx, `
		UPDATE session_mcp_grants SET calls_used = calls_used + 1
		WHERE session_id = $1 AND connection_id = $2 AND calls_used < call_budget
		RETURNING calls_used`, sessionID, connectionID).Scan(&used)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("ConsumeMCPCall: %w", err)
	}
	return true, nil
}

// MCPCallLogEntry is one gateway call as recorded in mcp_call_log. It deliberately
// has no field for arguments or results.
type MCPCallLogEntry struct {
	SessionID    string
	ConnectionID string
	Tool         string
	Mode         string // list, allow, propose or off
	Outcome      string
	DurationMS   int
}

// InsertMCPCallLog records one gateway call.
func InsertMCPCallLog(ctx context.Context, pool *pgxpool.Pool, e MCPCallLogEntry) error {
	if _, err := pool.Exec(ctx, `
		INSERT INTO mcp_call_log (session_id, connection_id, tool, mode, outcome, duration_ms)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		e.SessionID, e.ConnectionID, e.Tool, e.Mode, e.Outcome, e.DurationMS); err != nil {
		return fmt.Errorf("InsertMCPCallLog: %w", err)
	}
	return nil
}

// PruneMCPCallLog deletes call-log rows older than olderThan and returns the count.
func PruneMCPCallLog(ctx context.Context, pool *pgxpool.Pool, olderThan time.Duration) (int64, error) {
	tag, err := pool.Exec(ctx,
		`DELETE FROM mcp_call_log WHERE created_at < now() - make_interval(secs => $1)`,
		olderThan.Seconds())
	if err != nil {
		return 0, fmt.Errorf("PruneMCPCallLog: %w", err)
	}
	return tag.RowsAffected(), nil
}

// BoardExchangeSub is an exchange token issued to a built-in board grant and not yet revoked
// (migration 028). Sub is what core revokes it by.
type BoardExchangeSub struct {
	Sub       string
	TokenHash []byte
	SessionID string
	AccountID string
	ExpiresAt time.Time
}

// InsertBoardExchangeSub remembers an issued exchange token.
func InsertBoardExchangeSub(ctx context.Context, pool *pgxpool.Pool, s BoardExchangeSub) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO board_exchange_subs (sub, token_hash, session_id, account_id, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (sub) DO NOTHING`, s.Sub, s.TokenHash, s.SessionID, s.AccountID, s.ExpiresAt)
	if err != nil {
		return fmt.Errorf("InsertBoardExchangeSub: %w", err)
	}
	return nil
}

// DeleteBoardExchangeSub forgets a token that was revoked (or is already gone).
func DeleteBoardExchangeSub(ctx context.Context, pool *pgxpool.Pool, sub string) error {
	if _, err := pool.Exec(ctx, `DELETE FROM board_exchange_subs WHERE sub = $1`, sub); err != nil {
		return fmt.Errorf("DeleteBoardExchangeSub: %w", err)
	}
	return nil
}

// DeleteExpiredBoardExchangeSubs drops the rows whose tokens expired on their own (nothing to revoke).
func DeleteExpiredBoardExchangeSubs(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM board_exchange_subs WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("DeleteExpiredBoardExchangeSubs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListBoardExchangeSubsOwed returns the unexpired exchange tokens whose grant no longer exists
// (no session_mcp_grants row with their token hash): they are owed a revocation. sessionID, when
// not empty, narrows it to one session. At most limit rows, oldest first.
func ListBoardExchangeSubsOwed(ctx context.Context, pool *pgxpool.Pool, sessionID string, now time.Time, limit int) ([]BoardExchangeSub, error) {
	rows, err := pool.Query(ctx, `
		SELECT s.sub, s.token_hash, s.session_id, s.account_id, s.expires_at
		  FROM board_exchange_subs s
		 WHERE s.expires_at > $1
		   AND ($2 = '' OR s.session_id = $2)
		   AND NOT EXISTS (SELECT 1 FROM session_mcp_grants g WHERE g.token_hash = s.token_hash)
		 ORDER BY s.created_at
		 LIMIT $3`, now, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("ListBoardExchangeSubsOwed: %w", err)
	}
	return scanBoardExchangeSubs(rows, "ListBoardExchangeSubsOwed")
}

// ListBoardExchangeSubsForGrant returns the unexpired exchange tokens issued for one grant (by token hash).
func ListBoardExchangeSubsForGrant(ctx context.Context, pool *pgxpool.Pool, tokenHash []byte, now time.Time) ([]BoardExchangeSub, error) {
	rows, err := pool.Query(ctx, `
		SELECT sub, token_hash, session_id, account_id, expires_at
		  FROM board_exchange_subs WHERE token_hash = $1 AND expires_at > $2 ORDER BY created_at`, tokenHash, now)
	if err != nil {
		return nil, fmt.Errorf("ListBoardExchangeSubsForGrant: %w", err)
	}
	return scanBoardExchangeSubs(rows, "ListBoardExchangeSubsForGrant")
}

func scanBoardExchangeSubs(rows pgx.Rows, who string) ([]BoardExchangeSub, error) {
	defer rows.Close()
	var out []BoardExchangeSub
	for rows.Next() {
		var s BoardExchangeSub
		if err := rows.Scan(&s.Sub, &s.TokenHash, &s.SessionID, &s.AccountID, &s.ExpiresAt); err != nil {
			return nil, fmt.Errorf("%s scan: %w", who, err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
