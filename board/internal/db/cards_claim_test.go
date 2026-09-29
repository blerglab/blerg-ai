package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// ClaimCard is MoveCard for a caller whose read of the card is old: the board
// dispatcher spawns a session (a pod start, seconds) before it moves the card,
// and whatever a human did to the card in that window has to survive. These
// tests are the guard itself; boardruncapacity_test.go exercises it through
// the dispatcher.

func TestClaimCardMovesFromTheExpectedColumn(t *testing.T) {
	f := newDispatchFixture(t)
	c := f.card("a claimable card", f.ready)

	got, err := db.ClaimCard(context.Background(), f.pool, c.ID, f.ready.ID, f.work.ID,
		db.EventMeta{Actor: "service"})
	if err != nil {
		t.Fatalf("ClaimCard: %v", err)
	}
	if got.ColumnID == nil || *got.ColumnID != f.work.ID {
		t.Fatalf("card is in %v, want the work column", got.ColumnID)
	}
}

func TestClaimCardRefusesACardThatMovedOn(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	c := f.card("a card the human filed away", f.ready)
	f.moveTo(c, f.terminal) // the human, mid-spawn
	before := movedCount(t, f, c.ID)

	_, err := db.ClaimCard(ctx, f.pool, c.ID, f.ready.ID, f.work.ID, db.EventMeta{Actor: "service"})
	if !errors.Is(err, db.ErrCardMoved) {
		t.Fatalf("ClaimCard on a moved card = %v, want ErrCardMoved", err)
	}
	after, err := db.GetCard(ctx, f.pool, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ColumnID == nil || *after.ColumnID != f.terminal.ID {
		t.Fatalf("the refused claim moved the card anyway (now in %v)", after.ColumnID)
	}
	if got := movedCount(t, f, c.ID); got != before {
		t.Fatalf("a refused claim wrote %d `moved` event(s) — the flapping this guard exists to stop",
			got-before)
	}
}

func movedCount(t *testing.T, f *dispatchFixture, cardID string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM card_events WHERE card_id = $1 AND type = 'moved'`, cardID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The archived case is the loud one: MoveCard sets archived_at = NULL, so an
// unguarded claim brings a card the human just archived back to life.
func TestClaimCardRefusesAnArchivedCard(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	c := f.card("a card the human archived", f.ready)
	if _, err := db.ArchiveCard(ctx, f.pool, c.ID, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}

	_, err := db.ClaimCard(ctx, f.pool, c.ID, f.ready.ID, f.work.ID, db.EventMeta{Actor: "service"})
	if !errors.Is(err, db.ErrCardMoved) {
		t.Fatalf("ClaimCard on an archived card = %v, want ErrCardMoved", err)
	}
	after, err := db.GetCard(ctx, f.pool, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ArchivedAt == nil {
		t.Fatal("the claim un-archived the card")
	}
	if after.ColumnID != nil {
		t.Fatalf("the claim put an archived card back in a column (%s)", *after.ColumnID)
	}
}

// MoveCard keeps its old behaviour: no guard, no surprise for the callers that
// legitimately move a card wherever it happens to be (drag and drop, the API's
// move endpoint, finishCard).
func TestMoveCardStillMovesFromAnywhere(t *testing.T) {
	f := newDispatchFixture(t)
	c := f.card("a card being dragged", f.ready)
	f.moveTo(c, f.terminal)
	f.moveTo(c, f.work) // from the terminal column, no guard, no error
}
