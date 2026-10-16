package db

// In-place writes to one tool entry of a grant (docs/design/mcp-tool-changes.md §3.1). Every write
// is a compare-and-set: it names the hash (or absence) it expects, so two gateway requests, an
// accept and a resume can race without one undoing another. The tool name is always a BOUND path
// element (ARRAY[$n]::text[]): names are upstream-controlled and may hold commas, quotes or braces.
// None of these functions rotates the grant's token; storeMCPGrants (launch, resume) does that.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is what the pin writes need: a pool or a transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// RepinGrantTool moves a pinned tool from oldHash to newHash and marks it unlisted, only if the
// row still pins oldHash. mode may tighten the entry to "propose"; "" keeps the pinned mode;
// "allow" is accepted only when the entry already is allow (a re-pin never widens). It reports
// whether the row changed; false means another writer moved the pin first, or the mode would widen.
func RepinGrantTool(ctx context.Context, q DBTX, sessionID, connectionID, tool, oldHash, newHash, mode string) (bool, error) {
	if tool == "" || oldHash == "" || newHash == "" {
		return false, errors.New("RepinGrantTool: tool, old hash and new hash are required")
	}
	if mode != "" && mode != "allow" && mode != "propose" {
		return false, fmt.Errorf("RepinGrantTool: mode %q is not allow or propose", mode)
	}
	tag, err := q.Exec(ctx, `
		UPDATE session_mcp_grants
		   SET tools = jsonb_set(tools, ARRAY[$3]::text[],
		         jsonb_build_object('mode', COALESCE(NULLIF($6, ''), tools->$3->>'mode'), 'hash', $5::text, 'listed', false))
		 WHERE session_id = $1 AND connection_id = $2
		   AND tools->$3->>'hash' = $4
		   AND ($6 = '' OR $6 = 'propose' OR tools->$3->>'mode' = $6)`,
		sessionID, connectionID, tool, oldHash, newHash, mode)
	if err != nil {
		return false, fmt.Errorf("RepinGrantTool: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// GrantTool adds a tool the grant does not have, at hash with mode, marked unlisted. It never
// touches an existing entry (so it can never turn a propose into an allow). It reports whether
// the row changed.
func GrantTool(ctx context.Context, q DBTX, sessionID, connectionID, tool, hash, mode string) (bool, error) {
	if tool == "" || hash == "" || (mode != "allow" && mode != "propose") {
		return false, errors.New("GrantTool: tool, hash and a mode of allow or propose are required")
	}
	tag, err := q.Exec(ctx, `
		UPDATE session_mcp_grants
		   SET tools = tools || jsonb_build_object($3::text, jsonb_build_object('mode', $5::text, 'hash', $4::text, 'listed', false))
		 WHERE session_id = $1 AND connection_id = $2 AND NOT (tools ? $3)`,
		sessionID, connectionID, tool, hash, mode)
	if err != nil {
		return false, fmt.Errorf("GrantTool: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// MarkListed records that a tools/list served the entry at hashServed: the listed flag is dropped
// (absent means listed) only when the entry still pins that very hash, so a list that raced a
// re-pin cannot mark the newer definition as seen.
func MarkListed(ctx context.Context, q DBTX, sessionID, connectionID, tool, hashServed string) (bool, error) {
	if tool == "" || hashServed == "" {
		return false, errors.New("MarkListed: tool and hash are required")
	}
	tag, err := q.Exec(ctx, `
		UPDATE session_mcp_grants
		   SET tools = tools #- ARRAY[$3, 'listed']::text[]
		 WHERE session_id = $1 AND connection_id = $2
		   AND tools->$3->>'hash' = $4 AND (tools->$3) ? 'listed'`,
		sessionID, connectionID, tool, hashServed)
	if err != nil {
		return false, fmt.Errorf("MarkListed: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
