package db

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Sentinel errors ──────────────────────────────────────────────────────────

var (
	// ErrSelfDependency is returned by AddDependency when ticketID == dependsOnID.
	ErrSelfDependency = errors.New("self dependency")

	// ErrDependencyCycle is returned by AddDependency when the new edge would
	// create a directed cycle in the dependency graph.
	ErrDependencyCycle = errors.New("dependency cycle detected")

	// ErrCrossBoardDependency is returned by AddDependency when the two tickets
	// belong to different boards.
	ErrCrossBoardDependency = errors.New("cross-board dependency not allowed")

	// ErrTooFewChildren is returned by SplitTicket when fewer than 2 child
	// titles are supplied.
	ErrTooFewChildren = errors.New("split requires at least 2 child titles")
)

// ─── Types ────────────────────────────────────────────────────────────────────

// TicketEvent is a row from the ticket_events table.
type TicketEvent struct {
	ID           int64
	TicketID     string
	Type         string
	Actor        string
	FromColumnID *string
	ToColumnID   *string
	Data         json.RawMessage
	CreatedAt    time.Time
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// bfsCanReach returns true when targetID is reachable from startID by following
// directed dependency edges (ticket_id → depends_on_ticket_id). It runs inside
// an existing transaction so it sees the current (locked) graph state.
func bfsCanReach(ctx context.Context, tx pgx.Tx, startID, targetID string) (bool, error) {
	visited := map[string]bool{startID: true}
	queue := []string{startID}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		rows, err := tx.Query(ctx, `
			SELECT depends_on_ticket_id::text
			  FROM ticket_dependencies
			 WHERE ticket_id = $1
		`, cur)
		if err != nil {
			return false, fmt.Errorf("bfsCanReach query: %w", err)
		}

		var neighbors []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return false, fmt.Errorf("bfsCanReach scan: %w", err)
			}
			neighbors = append(neighbors, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return false, fmt.Errorf("bfsCanReach rows: %w", err)
		}

		for _, n := range neighbors {
			if n == targetID {
				return true, nil
			}
			if !visited[n] {
				visited[n] = true
				queue = append(queue, n)
			}
		}
	}
	return false, nil
}

// ─── Dependency management ────────────────────────────────────────────────────

// AddDependency adds the directed edge "ticketID depends on dependsOnID".
//
// Errors:
//   - ErrSelfDependency        – ticketID == dependsOnID
//   - ErrCrossBoardDependency  – tickets are on different boards
//   - ErrDependencyCycle       – edge would create a cycle
//
// The cycle check runs inside a transaction that first locks the board row
// (SELECT … FOR UPDATE), so two concurrent adds cannot jointly form a cycle.
// The insert is idempotent (ON CONFLICT DO NOTHING).
// A "dependency_added" event is written with the supplied actor.
func AddDependency(ctx context.Context, pool *pgxpool.Pool, ticketID, dependsOnID, actor string) error {
	if ticketID == dependsOnID {
		return ErrSelfDependency
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("AddDependency begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is what gets returned

	// Resolve board IDs and verify both tickets are on the same board.
	var boardA, boardB string
	if err := tx.QueryRow(ctx,
		`SELECT board_id::text FROM tickets WHERE id = $1`, ticketID,
	).Scan(&boardA); err != nil {
		return fmt.Errorf("AddDependency get ticket board: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT board_id::text FROM tickets WHERE id = $1`, dependsOnID,
	).Scan(&boardB); err != nil {
		return fmt.Errorf("AddDependency get dependsOn board: %w", err)
	}
	if boardA != boardB {
		return fmt.Errorf("AddDependency: tickets %s and %s are on different boards: %w", ticketID, dependsOnID, ErrCrossBoardDependency)
	}

	// Lock the board row to serialise concurrent AddDependency calls on the
	// same board. Without this lock, two concurrent calls could both pass the
	// cycle check before either commits, jointly forming a cycle.
	if _, err := tx.Exec(ctx,
		`SELECT id FROM boards WHERE id = $1 FOR UPDATE`, boardA,
	); err != nil {
		return fmt.Errorf("AddDependency lock board: %w", err)
	}

	// Cycle check: would dependsOnID already reach ticketID?
	// If yes, adding ticketID→dependsOnID would close the loop.
	reachable, err := bfsCanReach(ctx, tx, dependsOnID, ticketID)
	if err != nil {
		return fmt.Errorf("AddDependency cycle check: %w", err)
	}
	if reachable {
		return ErrDependencyCycle
	}

	// Insert edge (idempotent).
	if _, err := tx.Exec(ctx, `
		INSERT INTO ticket_dependencies (ticket_id, depends_on_ticket_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, ticketID, dependsOnID); err != nil {
		return fmt.Errorf("AddDependency insert edge: %w", err)
	}

	// Append event.
	eventData, err := json.Marshal(map[string]string{"depends_on": dependsOnID})
	if err != nil {
		return fmt.Errorf("AddDependency marshal event data: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ticket_events (ticket_id, type, actor, data)
		VALUES ($1, 'dependency_added', $2, $3)
	`, ticketID, actor, eventData); err != nil {
		return fmt.Errorf("AddDependency append event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("AddDependency commit: %w", err)
	}
	return nil
}

// RemoveDependency removes the directed edge "ticketID depends on dependsOnID".
// A "dependency_removed" event is written with the supplied actor.
func RemoveDependency(ctx context.Context, pool *pgxpool.Pool, ticketID, dependsOnID, actor string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("RemoveDependency begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is what gets returned

	tag, err := tx.Exec(ctx, `
		DELETE FROM ticket_dependencies
		 WHERE ticket_id = $1 AND depends_on_ticket_id = $2
	`, ticketID, dependsOnID)
	if err != nil {
		return fmt.Errorf("RemoveDependency delete: %w", err)
	}

	// Idempotent no-op: if no edge existed, don't write a phantom event.
	if tag.RowsAffected() == 0 {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("RemoveDependency commit: %w", err)
		}
		return nil
	}

	eventData, err := json.Marshal(map[string]string{"depends_on": dependsOnID})
	if err != nil {
		return fmt.Errorf("RemoveDependency marshal event data: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ticket_events (ticket_id, type, actor, data)
		VALUES ($1, 'dependency_removed', $2, $3)
	`, ticketID, actor, eventData); err != nil {
		return fmt.Errorf("RemoveDependency append event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("RemoveDependency commit: %w", err)
	}
	return nil
}

// ─── Split ────────────────────────────────────────────────────────────────────

// SplitTicket splits originID into N children (len(childTitles) ≥ 2).
//
// In a single transaction:
//  1. Creates N children in origin's column, each inheriting repos, tags, and
//     priority from the origin. Size is left unset (nil).
//  2. Archives the origin (archived_at=now(), column_id=NULL).
//  3. Repoints inbound edges: every ticket T that depended on origin now depends
//     on every child (T→origin removed, T→child1 … T→childN added).
//  4. Copies outbound edges: every dep D that origin depended on is copied to
//     each child (child→D). Origin's own outbound edges are then removed.
//  5. Writes a "split" event on origin (actor="system") whose data jsonb lists
//     the child IDs under key "children".
func SplitTicket(ctx context.Context, pool *pgxpool.Pool, originID string, childTitles []string) ([]TicketRow, error) {
	if len(childTitles) < 2 {
		return nil, ErrTooFewChildren
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("SplitTicket begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is what gets returned

	// Lock and read origin.
	origin, err := scanTicketRow(tx.QueryRow(ctx,
		`SELECT `+ticketCols+` FROM tickets WHERE id = $1 FOR UPDATE`, originID,
	))
	if err != nil {
		return nil, fmt.Errorf("SplitTicket get origin: %w", err)
	}
	if origin.ColumnID == nil {
		return nil, fmt.Errorf("SplitTicket: origin %s has no column (already archived?)", originID)
	}

	// Lock board row to serialise against concurrent dependency changes.
	if _, err := tx.Exec(ctx,
		`SELECT id FROM boards WHERE id = $1 FOR UPDATE`, origin.BoardID,
	); err != nil {
		return nil, fmt.Errorf("SplitTicket lock board: %w", err)
	}

	// Read origin's repos (rank order).
	repoRows, err := tx.Query(ctx, `
		SELECT repo FROM ticket_repos WHERE ticket_id = $1 ORDER BY rank ASC
	`, originID)
	if err != nil {
		return nil, fmt.Errorf("SplitTicket query repos: %w", err)
	}
	var repos []string
	for repoRows.Next() {
		var r string
		if err := repoRows.Scan(&r); err != nil {
			repoRows.Close()
			return nil, fmt.Errorf("SplitTicket scan repo: %w", err)
		}
		repos = append(repos, r)
	}
	repoRows.Close()
	if err := repoRows.Err(); err != nil {
		return nil, fmt.Errorf("SplitTicket repos rows: %w", err)
	}

	// Read origin's tags.
	tagRows, err := tx.Query(ctx,
		`SELECT tag FROM ticket_tags WHERE ticket_id = $1`, originID,
	)
	if err != nil {
		return nil, fmt.Errorf("SplitTicket query tags: %w", err)
	}
	var tags []string
	for tagRows.Next() {
		var tag string
		if err := tagRows.Scan(&tag); err != nil {
			tagRows.Close()
			return nil, fmt.Errorf("SplitTicket scan tag: %w", err)
		}
		tags = append(tags, tag)
	}
	tagRows.Close()
	if err := tagRows.Err(); err != nil {
		return nil, fmt.Errorf("SplitTicket tags rows: %w", err)
	}

	// Read inbound deps (tickets that depend on origin).
	inRows, err := tx.Query(ctx, `
		SELECT ticket_id::text FROM ticket_dependencies WHERE depends_on_ticket_id = $1
	`, originID)
	if err != nil {
		return nil, fmt.Errorf("SplitTicket query inbound: %w", err)
	}
	var inboundIDs []string
	for inRows.Next() {
		var id string
		if err := inRows.Scan(&id); err != nil {
			inRows.Close()
			return nil, fmt.Errorf("SplitTicket scan inbound: %w", err)
		}
		inboundIDs = append(inboundIDs, id)
	}
	inRows.Close()
	if err := inRows.Err(); err != nil {
		return nil, fmt.Errorf("SplitTicket inbound rows: %w", err)
	}

	// Read outbound deps (what origin depends on).
	outRows, err := tx.Query(ctx, `
		SELECT depends_on_ticket_id::text FROM ticket_dependencies WHERE ticket_id = $1
	`, originID)
	if err != nil {
		return nil, fmt.Errorf("SplitTicket query outbound: %w", err)
	}
	var outboundIDs []string
	for outRows.Next() {
		var id string
		if err := outRows.Scan(&id); err != nil {
			outRows.Close()
			return nil, fmt.Errorf("SplitTicket scan outbound: %w", err)
		}
		outboundIDs = append(outboundIDs, id)
	}
	outRows.Close()
	if err := outRows.Err(); err != nil {
		return nil, fmt.Errorf("SplitTicket outbound rows: %w", err)
	}

	// Compute base rank: place children after the last live ticket in the column.
	var prevRank string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(rank), '')
		  FROM tickets
		 WHERE board_id = $1 AND column_id = $2 AND archived_at IS NULL
	`, origin.BoardID, *origin.ColumnID).Scan(&prevRank); err != nil {
		return nil, fmt.Errorf("SplitTicket get max rank: %w", err)
	}

	// Create children.
	var children []TicketRow
	for _, title := range childTitles {
		rank, err := RankBetween(prevRank, "")
		if err != nil {
			return nil, fmt.Errorf("SplitTicket compute rank for %q: %w", title, err)
		}

		child, err := scanTicketRow(tx.QueryRow(ctx, `
			INSERT INTO tickets (board_id, column_id, title, priority, rank)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING `+ticketCols,
			origin.BoardID, *origin.ColumnID, title, origin.Priority, rank,
		))
		if err != nil {
			return nil, fmt.Errorf("SplitTicket create child %q: %w", title, err)
		}

		if err := insertTicketRepos(ctx, tx, child.ID, repos); err != nil {
			return nil, fmt.Errorf("SplitTicket inherit repos for %q: %w", title, err)
		}

		for _, tag := range tags {
			if _, err := tx.Exec(ctx, `
				INSERT INTO ticket_tags (ticket_id, tag) VALUES ($1, $2)
				ON CONFLICT DO NOTHING
			`, child.ID, tag); err != nil {
				return nil, fmt.Errorf("SplitTicket inherit tag %q for %q: %w", tag, title, err)
			}
		}

		children = append(children, child)
		prevRank = rank
	}

	// Archive origin.
	if _, err := tx.Exec(ctx, `
		UPDATE tickets
		   SET archived_at = now(), column_id = NULL, updated_at = now()
		 WHERE id = $1
	`, originID); err != nil {
		return nil, fmt.Errorf("SplitTicket archive origin: %w", err)
	}

	// Repoint inbound deps: T→origin becomes T→child for each child.
	for _, inID := range inboundIDs {
		for _, child := range children {
			if _, err := tx.Exec(ctx, `
				INSERT INTO ticket_dependencies (ticket_id, depends_on_ticket_id)
				VALUES ($1, $2) ON CONFLICT DO NOTHING
			`, inID, child.ID); err != nil {
				return nil, fmt.Errorf("SplitTicket repoint inbound %s→%s: %w", inID, child.ID, err)
			}
		}
		// Remove old inbound edge T→origin.
		if _, err := tx.Exec(ctx, `
			DELETE FROM ticket_dependencies
			 WHERE ticket_id = $1 AND depends_on_ticket_id = $2
		`, inID, originID); err != nil {
			return nil, fmt.Errorf("SplitTicket remove inbound %s→origin: %w", inID, err)
		}
	}

	// Copy outbound deps: origin→D becomes child→D for each child.
	for _, outID := range outboundIDs {
		for _, child := range children {
			if _, err := tx.Exec(ctx, `
				INSERT INTO ticket_dependencies (ticket_id, depends_on_ticket_id)
				VALUES ($1, $2) ON CONFLICT DO NOTHING
			`, child.ID, outID); err != nil {
				return nil, fmt.Errorf("SplitTicket copy outbound child→%s: %w", outID, err)
			}
		}
	}

	// Remove origin's own outbound edges (inbound already removed above).
	if _, err := tx.Exec(ctx,
		`DELETE FROM ticket_dependencies WHERE ticket_id = $1`, originID,
	); err != nil {
		return nil, fmt.Errorf("SplitTicket remove origin outbound: %w", err)
	}

	// Write split event listing child IDs.
	childIDs := make([]string, len(children))
	for i, c := range children {
		childIDs[i] = c.ID
	}
	splitData, err := json.Marshal(map[string][]string{"children": childIDs})
	if err != nil {
		return nil, fmt.Errorf("SplitTicket marshal event data: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ticket_events (ticket_id, type, actor, data)
		VALUES ($1, 'split', 'system', $2)
	`, originID, splitData); err != nil {
		return nil, fmt.Errorf("SplitTicket write event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("SplitTicket commit: %w", err)
	}
	return children, nil
}

// ─── Events ───────────────────────────────────────────────────────────────────

// AppendTicketEvent inserts an event row into ticket_events.
// The ID and CreatedAt fields of e are ignored; the DB supplies them.
func AppendTicketEvent(ctx context.Context, pool *pgxpool.Pool, e TicketEvent) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO ticket_events (ticket_id, type, actor, from_column_id, to_column_id, data)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, e.TicketID, e.Type, e.Actor, e.FromColumnID, e.ToColumnID, e.Data)
	if err != nil {
		return fmt.Errorf("AppendTicketEvent: %w", err)
	}
	return nil
}

// encodeEventCursor encodes a bigserial event ID as an opaque base64 cursor.
func encodeEventCursor(id int64) string {
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

// decodeEventCursor reverses encodeEventCursor.
func decodeEventCursor(cursor string) (int64, error) {
	b, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("invalid event cursor: %w", err)
	}
	id, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid event cursor value: %w", err)
	}
	return id, nil
}

// ListTicketEvents returns events for ticketID ordered by id DESC (newest first).
// limit defaults to 50, capped at 200. cursor is the opaque token returned by a
// prior call; pass "" to start from the newest event. Returns (events,
// nextCursor, err); nextCursor is "" when no further pages exist.
func ListTicketEvents(ctx context.Context, pool *pgxpool.Pool, ticketID string, limit int, cursor string) ([]TicketEvent, string, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	args := []any{ticketID}
	where := "ticket_id = $1"

	if cursor != "" {
		afterID, err := decodeEventCursor(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("ListTicketEvents: %w", err)
		}
		args = append(args, afterID)
		where += fmt.Sprintf(" AND id < $%d", len(args))
	}

	args = append(args, limit+1)
	q := fmt.Sprintf(`
		SELECT id, ticket_id::text, type, actor,
		       from_column_id::text, to_column_id::text,
		       data, created_at
		  FROM ticket_events
		 WHERE %s
		 ORDER BY id DESC
		 LIMIT $%d
	`, where, len(args))

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("ListTicketEvents query: %w", err)
	}
	defer rows.Close()

	var events []TicketEvent
	for rows.Next() {
		var e TicketEvent
		var fromColID, toColID *string
		var rawData []byte
		if err := rows.Scan(
			&e.ID, &e.TicketID, &e.Type, &e.Actor,
			&fromColID, &toColID,
			&rawData, &e.CreatedAt,
		); err != nil {
			return nil, "", fmt.Errorf("ListTicketEvents scan: %w", err)
		}
		e.FromColumnID = fromColID
		e.ToColumnID = toColID
		e.Data = json.RawMessage(rawData)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("ListTicketEvents rows: %w", err)
	}

	var nextCursor string
	if len(events) > limit {
		events = events[:limit]
		nextCursor = encodeEventCursor(events[len(events)-1].ID)
	}

	return events, nextCursor, nil
}
