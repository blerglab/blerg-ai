package db_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// TestBoardSessionModels: the per-role model fields persist, patch
// independently, and leave the others alone when omitted.
func TestBoardSessionModels(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	b := mkBoard(t, pool, db.BoardParams{
		Repos: []string{"blerg-board"},
		Model: strp("worker-model"), ReviewerModel: strp("reviewer-model"),
		DiscussModel: strp("discuss-model"), ChatModel: strp("chat-model"),
	})
	if b.DiscussModel != "discuss-model" || b.ChatModel != "chat-model" {
		t.Fatalf("create: discuss=%q chat=%q", b.DiscussModel, b.ChatModel)
	}

	// A patch that names only discuss_model must not disturb the rest.
	up, err := db.UpdateBoard(ctx, pool, b.ID, db.BoardParams{DiscussModel: strp("discuss-stronger")})
	if err != nil {
		t.Fatal(err)
	}
	if up.DiscussModel != "discuss-stronger" {
		t.Errorf("discuss_model = %q", up.DiscussModel)
	}
	if up.Model != "worker-model" || up.ReviewerModel != "reviewer-model" || up.ChatModel != "chat-model" {
		t.Errorf("patch bled into other models: %+v", up)
	}

	// Empty string clears an override (back to the board default).
	up, err = db.UpdateBoard(ctx, pool, b.ID, db.BoardParams{ChatModel: strp("")})
	if err != nil {
		t.Fatal(err)
	}
	if up.ChatModel != "" {
		t.Errorf("chat_model after clear = %q, want empty", up.ChatModel)
	}

	// Boards created without the fields default to unset, not NULL.
	plain := mkBoard(t, pool, db.BoardParams{Name: "plain", Repos: []string{"blerg-board"}})
	if plain.DiscussModel != "" || plain.ChatModel != "" {
		t.Errorf("defaults: discuss=%q chat=%q", plain.DiscussModel, plain.ChatModel)
	}
}

// TestCardModelOverride: the per-card model round-trips, clears on "", and
// survives a dedup_key refresh (a sweep re-filing a card must not silently
// drop the human's model pin, same rule as column and priority).
func TestCardModelOverride(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})

	res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("hot card"), Repos: &[]string{"blerg-board"},
		Model: strp(" card-model "), DedupKey: strp("k1"),
	}, human)
	if err != nil {
		t.Fatal(err)
	}
	if res.Card.Model != "card-model" {
		t.Fatalf("model = %q, want card-model (trimmed)", res.Card.Model)
	}

	got, err := db.GetCard(ctx, pool, res.Card.ID)
	if err != nil || got.Model != "card-model" {
		t.Fatalf("reload: model=%q err=%v", got.Model, err)
	}

	// A dedup refresh rewrites content only.
	again, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("hot card, restated"), Repos: &[]string{"blerg-board"}, DedupKey: strp("k1"),
	}, human)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Refreshed || again.Card.Model != "card-model" {
		t.Errorf("dedup refresh dropped the model: refreshed=%v model=%q", again.Refreshed, again.Card.Model)
	}

	// Omitting model on update leaves it; "" clears it.
	up, err := db.UpdateCard(ctx, pool, res.Card.ID, db.CardParams{Title: strp("renamed")}, human)
	if err != nil || up.Model != "card-model" {
		t.Fatalf("update without model: model=%q err=%v", up.Model, err)
	}
	up, err = db.UpdateCard(ctx, pool, res.Card.ID, db.CardParams{Model: strp("")}, human)
	if err != nil {
		t.Fatal(err)
	}
	if up.Model != "" {
		t.Errorf("model after clear = %q, want empty", up.Model)
	}
}
