package api

import (
	"context"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// A column removal's cards have moved once it commits: the move reaction (here, the adversarial
// reviewer a card entering review gets) runs even when the request is cancelled right after.
func TestColumnRemovalReactionSurvivesACancelledRequest(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	board, card, cols := specCard(t, pool, "## Design\n\nA body long enough to be a real spec.")
	from, err := db.CreateColumn(context.Background(), pool, board.ID, "old", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MoveCard(context.Background(), pool, card.ID, from.ID, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	columnRemovalCommitted = cancel
	defer func() { columnRemovalCommitted = nil }()
	human := auth.Principal{Kind: auth.KindService, FromCore: true, CoreAccountID: "acct-1",
		Token: &db.Token{ID: "core:acct-1", Kind: "human", Capabilities: []string{"card.read", "card.write", "column.write"}}}
	moved, denied, err := a.DeleteColumnMovingCardsGated(ctx, human, from, specColumn(t, cols, "review"))
	if err != nil || denied != nil || len(moved) != 1 {
		t.Fatalf("removal: moved %v denied %v err %v", moved, denied, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		fake.mu.Lock()
		n := len(fake.started)
		fake.mu.Unlock()
		if n == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reviewers started after a cancelled removal: %d, want 1", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
