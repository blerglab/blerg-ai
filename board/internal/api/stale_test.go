package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestStaleSweepFlagsAndClears(t *testing.T) {
	_, pool := testServer(t)
	ctx := context.Background()

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	var progressCol string
	for _, c := range cols {
		if strings.Contains(strings.ToLower(c.Name), "progress") {
			progressCol = c.ID
		}
	}
	if progressCol == "" {
		t.Fatal("no in-progress column on a default board")
	}

	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("ship the thing"), Repos: &[]string{"blerg-board"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	card, err := db.MoveCard(ctx, pool, res.Card.ID, progressCol, nil, nil, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}

	// Fresh claim: not stale yet.
	srvAPI.SweepStaleCards(ctx)
	card, _ = db.GetCard(ctx, pool, card.ID)
	if card.StaleAt != nil {
		t.Fatal("flagged stale before the grace period elapsed")
	}

	// Backdate past the grace period — simulates a card nobody has touched
	// in over 24h (every mutation bumps updated_at, so this is the signal).
	if _, err := pool.Exec(ctx,
		`UPDATE cards SET updated_at = now() - interval '25 hours' WHERE id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}
	srvAPI.SweepStaleCards(ctx)
	card, _ = db.GetCard(ctx, pool, card.ID)
	if card.StaleAt == nil {
		t.Fatal("expected stale flag after the grace period")
	}
	events, err := db.ListCardEvents(ctx, pool, card.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	nudged := false
	for _, e := range events {
		if e.Type == "comment" && e.Actor == "service" {
			nudged = true
		}
	}
	if !nudged {
		t.Fatal("expected a service nudge comment from the sweep")
	}

	// A second sweep must not clear the flag via its own nudge comment.
	srvAPI.SweepStaleCards(ctx)
	card, _ = db.GetCard(ctx, pool, card.ID)
	if card.StaleAt == nil {
		t.Fatal("stale flag self-cleared from the sweep's own nudge comment")
	}

	// Real activity clears it.
	if err := db.AppendComment(ctx, pool, card.ID, "still working on this", db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	srvAPI.SweepStaleCards(ctx)
	card, _ = db.GetCard(ctx, pool, card.ID)
	if card.StaleAt != nil {
		t.Fatal("expected stale flag cleared after real activity")
	}
}
