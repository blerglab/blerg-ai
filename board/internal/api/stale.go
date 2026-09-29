package api

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// staleGrace: an in-progress card with no non-terminal runner session and no
// activity (any card mutation bumps cards.updated_at, so that column doubles
// as the activity signal) for this long reads as claimed-and-abandoned, not
// actively worked. See card #11.
const staleGrace = 24 * time.Hour

// StartStaleSweep launches the periodic staleness sweep. Unlike
// StartBoardRuns, this runs independent of the runner subsystem — staleness
// is a plain "has anything happened" check that applies even to boards with
// no runner configured.
func (a *API) StartStaleSweep(ctx context.Context) {
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.SweepStaleCards(ctx)
			}
		}
	}()
}

// SweepStaleCards clears flags that no longer apply, then flags newly-stale
// cards (posting a one-time nudge comment on each).
func (a *API) SweepStaleCards(ctx context.Context) {
	a.clearStaleCards(ctx)
	a.flagStaleCards(ctx)
}

// clearStaleCards drops the flag on cards that got real activity or picked
// up a session since being flagged. "Real" excludes the sweep's own nudge
// comment: flagStaleCards posts that comment BEFORE stamping stale_at, so a
// later updated_at bump only ever reflects activity that happened after the
// flag was set.
func (a *API) clearStaleCards(ctx context.Context) {
	rows, err := a.Pool.Query(ctx, `
		SELECT id, board_id FROM cards c
		WHERE stale_at IS NOT NULL
		  AND (
		    updated_at > stale_at
		    OR EXISTS (
		      SELECT 1 FROM runner_sessions rs
		      WHERE rs.card_id = c.id AND rs.lifecycle NOT IN ('stopped','error')
		    )
		  )`)
	if err != nil {
		log.Printf("stale sweep clear: %v", err)
		return
	}
	type cleared struct{ id, boardID string }
	var found []cleared
	for rows.Next() {
		var f cleared
		if rows.Scan(&f.id, &f.boardID) == nil {
			found = append(found, f)
		}
	}
	rows.Close()
	for _, f := range found {
		if _, err := a.Pool.Exec(ctx,
			`UPDATE cards SET stale_at = NULL WHERE id = $1`, f.id); err == nil {
			a.Hub.Broadcast(f.boardID, "card_changed")
		}
	}
}

// flagStaleCards marks — and nudges — in-progress cards nobody is moving.
// Candidate selection is a cheap bulk query; each candidate is then flagged
// under its own row lock so a concurrent write can't land between the
// eligibility check and the nudge comment (see flagCardStale).
func (a *API) flagStaleCards(ctx context.Context) {
	// Column-name matching happens in Go via isWorkColumn (review.go), the
	// codebase's one canonical "is this the in-progress column" check — it
	// also matches "working", which a hand-copied SQL LIKE would silently
	// miss for a board that named its column that.
	rows, err := a.Pool.Query(ctx, `
		SELECT c.id, bc.name FROM cards c
		JOIN board_columns bc ON bc.id = c.column_id
		WHERE c.archived_at IS NULL AND c.stale_at IS NULL
		  AND c.updated_at < now() - $1::interval
		  AND NOT EXISTS (
		    SELECT 1 FROM runner_sessions rs
		    WHERE rs.card_id = c.id AND rs.lifecycle NOT IN ('stopped','error')
		  )`, staleGrace.String())
	if err != nil {
		log.Printf("stale sweep flag: %v", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id, colName string
		if rows.Scan(&id, &colName) == nil && isWorkColumn(colName) {
			ids = append(ids, id)
		}
	}
	rows.Close()

	for _, id := range ids {
		a.flagCardStale(ctx, id)
	}
}

// flagCardStale re-checks staleness under a row lock, then — atomically with
// that same lock held — posts the nudge comment and stamps stale_at to the
// exact updated_at the comment just produced. That makes the flag-time
// updated_at bump indistinguishable from "no activity" (clearStaleCards only
// clears on updated_at > stale_at, and here they're set equal in the same
// statement), while any genuine write has to wait for the lock and is
// either seen by the recheck (skips the flag) or lands after commit (clears
// on the next sweep) — no window where it's silently shadowed.
func (a *API) flagCardStale(ctx context.Context, id string) {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned

	var boardID, colName string
	var archived, alreadyStale, quietLongEnough, hasActiveSession bool
	err = tx.QueryRow(ctx, `
		SELECT c.board_id, bc.name, c.archived_at IS NOT NULL, c.stale_at IS NOT NULL,
		  c.updated_at < now() - $2::interval,
		  EXISTS (
		    SELECT 1 FROM runner_sessions rs
		    WHERE rs.card_id = c.id AND rs.lifecycle NOT IN ('stopped','error')
		  )
		FROM cards c JOIN board_columns bc ON bc.id = c.column_id
		WHERE c.id = $1
		FOR UPDATE OF c`, id, staleGrace.String()).
		Scan(&boardID, &colName, &archived, &alreadyStale, &quietLongEnough, &hasActiveSession)
	if err != nil || archived || alreadyStale || !quietLongEnough || hasActiveSession || !isWorkColumn(colName) {
		return
	}

	if err := db.AppendCommentTx(ctx, tx, id, fmt.Sprintf(
		"Stale claim: this card has sat in progress for over %s with no active session and no other activity. "+
			"If you're still on it, post an update; otherwise release it back to ready so someone else can pick it up.",
		staleGrace), db.EventMeta{Actor: "service"}); err != nil {
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE cards SET stale_at = updated_at WHERE id = $1`, id); err != nil {
		return
	}
	if err := tx.Commit(ctx); err == nil {
		a.Hub.Broadcast(boardID, "card_changed")
	}
}
