package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Blocker is a card's direct hard dependency, resolved for display: the card
// it depends on plus whether that card has landed yet.
type Blocker struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	// Satisfied: this blocker no longer holds the dependent back.
	Satisfied bool `json:"satisfied"`
}

// blockerJoinSQL joins card_dependencies to the blocker card (`bl`) and its
// column (`bc`), leaving the caller to supply the `d.card_id = …` filter.
const blockerJoinSQL = `FROM card_dependencies d
		JOIN cards bl ON bl.id = d.depends_on_card_id
		LEFT JOIN board_columns bc ON bc.id = bl.column_id`

// blockerSatisfiedSQL is THE definition of a cleared hard dependency, over the
// aliases blockerJoinSQL establishes. A blocker clears when it reaches a
// terminal column, or when it is archived — archiving is the deliberate act of
// taking a card out of the flow, and treating a dangling edge to an off-board
// card as permanent would strand the dependent with nothing on the board to
// point a human at. Both the dispatcher's picker and the blocked_by the UI
// renders read this one expression.
var blockerSatisfiedSQL = `(bl.archived_at IS NOT NULL OR ` + TerminalColumnSQL("bc") + `)`

// hasUnsatisfiedBlockerSQL is a correlated EXISTS body: "the card named by
// cardIDExpr is held back by at least one dependency that has not landed".
func hasUnsatisfiedBlockerSQL(cardIDExpr string) string {
	return `EXISTS (SELECT 1 ` + blockerJoinSQL + `
		WHERE d.card_id = ` + cardIDExpr + ` AND NOT ` + blockerSatisfiedSQL + `)`
}

// canReach reports whether `from` transitively depends on `to` — a BFS over
// card_dependencies inside the caller's transaction. Ported from blerg-runner's
// ticket_graph.go acyclicity core.
func canReach(ctx context.Context, tx pgx.Tx, from, to string) (bool, error) {
	if from == to {
		return true, nil
	}
	visited := map[string]bool{from: true}
	frontier := []string{from}
	for len(frontier) > 0 {
		rows, err := tx.Query(ctx,
			`SELECT depends_on_card_id FROM card_dependencies WHERE card_id = ANY($1)`,
			frontier)
		if err != nil {
			return false, err
		}
		var next []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return false, err
			}
			if id == to {
				rows.Close()
				return true, nil
			}
			if !visited[id] {
				visited[id] = true
				next = append(next, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return false, err
		}
		frontier = next
	}
	return false, nil
}

// AddDependency records "card depends on blocker", refusing cycles and
// cross-board edges. Bumps version on BOTH cards — the edge changes the
// meaning of each.
func AddDependency(ctx context.Context, pool *pgxpool.Pool, cardID, blockerID string, ev EventMeta) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned

	var cardBoard, blockerBoard string
	if err := tx.QueryRow(ctx, `SELECT board_id FROM cards WHERE id = $1 FOR UPDATE`, cardID).Scan(&cardBoard); err != nil {
		return ErrNotFound
	}
	if err := tx.QueryRow(ctx, `SELECT board_id FROM cards WHERE id = $1 FOR UPDATE`, blockerID).Scan(&blockerBoard); err != nil {
		return ErrNotFound
	}
	if cardBoard != blockerBoard {
		return ErrInvalidRepos // cross-board dependency; reuse a 400-mapped error
	}
	// Cycle iff the blocker already (transitively) depends on this card.
	cyclic, err := canReach(ctx, tx, blockerID, cardID)
	if err != nil {
		return err
	}
	if cyclic {
		return ErrDependencyCycle
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO card_dependencies (card_id, depends_on_card_id)
		VALUES ($1,$2) ON CONFLICT DO NOTHING`, cardID, blockerID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE cards SET version = version + 1, updated_at = now()
		WHERE id = ANY($1)`, []string{cardID, blockerID}); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, cardID, "dependency_added", ev, nil, nil,
		map[string]any{"depends_on": blockerID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RemoveDependency deletes the edge; bumps version on both cards.
func RemoveDependency(ctx context.Context, pool *pgxpool.Pool, cardID, blockerID string, ev EventMeta) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned
	tag, err := tx.Exec(ctx, `
		DELETE FROM card_dependencies WHERE card_id = $1 AND depends_on_card_id = $2`,
		cardID, blockerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `
		UPDATE cards SET version = version + 1, updated_at = now()
		WHERE id = ANY($1)`, []string{cardID, blockerID}); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, cardID, "dependency_removed", ev, nil, nil,
		map[string]any{"depends_on": blockerID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
