package db

// Tool definitions by pin hash (docs/design/mcp-tool-changes.md §3.2): the exact canonical bytes
// a hash was computed over, written wherever a launch or picker listing is taken and whenever the
// gateway records a drifted hash. A review looks the old and the new text up by hash; an accept
// recomputes the hash over the stored text and refuses on a mismatch.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Why a definition cannot be reviewed (mcp_tool_definitions.unsafe); "" means it can.
const (
	UnsafeHiddenChars = "hidden_chars"
	UnsafeNUL         = "nul"
	UnsafeTooLarge    = "too_large"
)

// ErrToolDefinitionNotFound is returned when no definition has the hash.
var ErrToolDefinitionNotFound = errors.New("tool definition not found")

// ToolDefinition is a row of mcp_tool_definitions. Canonical is empty when Unsafe is set to
// UnsafeTooLarge; otherwise it is the full text, never truncated.
type ToolDefinition struct {
	Hash      string
	Name      string
	Canonical string
	Size      int
	Unsafe    string
	FirstSeen time.Time
	LastSeen  time.Time
}

// UpsertToolDefinition records a definition, or refreshes last_seen (at most once a day) when the
// hash is already known. A hash is content-addressed, so the stored text never changes.
func UpsertToolDefinition(ctx context.Context, q DBTX, d ToolDefinition) error {
	if d.Hash == "" || d.Name == "" {
		return errors.New("UpsertToolDefinition: hash and name are required")
	}
	var unsafe *string
	if d.Unsafe != "" {
		if d.Unsafe != UnsafeTooLarge && d.Unsafe != UnsafeNUL && d.Unsafe != UnsafeHiddenChars {
			return fmt.Errorf("UpsertToolDefinition: unknown unsafe value %q", d.Unsafe)
		}
		unsafe = &d.Unsafe
	}
	if d.Unsafe == UnsafeTooLarge {
		d.Canonical = "" // never store an oversized text
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO mcp_tool_definitions (hash, name, canonical, size, unsafe)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (hash) DO UPDATE SET last_seen = now()
		  WHERE mcp_tool_definitions.last_seen < now() - interval '1 day'`,
		d.Hash, d.Name, d.Canonical, d.Size, unsafe); err != nil {
		return fmt.Errorf("UpsertToolDefinition: %w", err)
	}
	return nil
}

// GetToolDefinition returns the definition with the hash, or ErrToolDefinitionNotFound.
func GetToolDefinition(ctx context.Context, q DBTX, hash string) (*ToolDefinition, error) {
	var d ToolDefinition
	var unsafe *string
	err := q.QueryRow(ctx, `SELECT hash, name, canonical, size, unsafe, first_seen, last_seen
		FROM mcp_tool_definitions WHERE hash = $1`, hash).Scan(&d.Hash, &d.Name, &d.Canonical, &d.Size, &unsafe, &d.FirstSeen, &d.LastSeen)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrToolDefinitionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetToolDefinition: %w", err)
	}
	if unsafe != nil {
		d.Unsafe = *unsafe
	}
	return &d, nil
}

// PruneToolDefinitions deletes definitions not seen for olderThan that no grant pins, no pending
// change names and no pin event names. It returns how many went.
func PruneToolDefinitions(ctx context.Context, pool *pgxpool.Pool, olderThan time.Duration) (int64, error) {
	tag, err := pool.Exec(ctx, `
		DELETE FROM mcp_tool_definitions d
		 WHERE d.last_seen < now() - make_interval(secs => $1)
		   AND NOT EXISTS (SELECT 1 FROM session_mcp_grants g, jsonb_each(g.tools) t WHERE t.value->>'hash' = d.hash)
		   AND NOT EXISTS (SELECT 1 FROM mcp_tool_changes c WHERE c.state = 'pending' AND (c.old_hash = d.hash OR c.new_hash = d.hash))
		   AND NOT EXISTS (SELECT 1 FROM mcp_pin_events e WHERE e.old_hash = d.hash OR e.new_hash = d.hash)`,
		olderThan.Seconds())
	if err != nil {
		return 0, fmt.Errorf("PruneToolDefinitions: %w", err)
	}
	return tag.RowsAffected(), nil
}
