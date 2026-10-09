package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// TestSessionModelPerRole walks the whole resolution chain end to end through
// the HTTP surface: worker spawns stay on the board default while discuss and
// board-header chat sessions ride their own overrides, and a card's own model
// beats all of them.
func TestSessionModelPerRole(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	apiSrv := srvAPI
	apiSrv.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{
		Name: "work", Repos: []string{"blerg-board"},
		Model: strPtr("board-default"),
	})
	if err != nil {
		t.Fatal(err)
	}
	mkCard := func(title string, model *string) db.Card {
		t.Helper()
		p := db.CardParams{Title: strPtr(title), Repos: &[]string{"blerg-board"}, Model: model}
		res, err := db.CreateCard(ctx, pool, board.ID, p, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		return res.Card
	}
	// lastModel spawns and returns the model the runner was asked for.
	spawnCard := func(cardID string, body map[string]string) string {
		t.Helper()
		resp := request(t, srv, "POST", "/api/cards/"+cardID+"/spawn", cookie, nil, body)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("spawn: %d", resp.StatusCode)
		}
		_ = resp.Body.Close()
		return fake.started[len(fake.started)-1].Model
	}
	spawnBoardChat := func() string {
		t.Helper()
		resp := request(t, srv, "POST", "/api/boards/"+board.ID+"/sessions", cookie, nil, map[string]string{})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("board session: %d", resp.StatusCode)
		}
		_ = resp.Body.Close()
		return fake.started[len(fake.started)-1].Model
	}

	// With only board.model set, every role falls back to it.
	plain := mkCard("build the thing", nil)
	if got := spawnCard(plain.ID, map[string]string{}); got != "board-default" {
		t.Errorf("worker model = %q, want board-default", got)
	}
	if got := spawnCard(plain.ID, map[string]string{"mode": "discuss"}); got != "board-default" {
		t.Errorf("discuss model without an override = %q, want board-default", got)
	}
	if got := spawnBoardChat(); got != "board-default" {
		t.Errorf("board chat model without an override = %q, want board-default", got)
	}

	// Each of those three said who is in the chat: nobody watches a card's
	// run; a person opened the discussion and the board session to talk.
	if len(fake.started) != 3 {
		t.Fatalf("started %d sessions, want 3", len(fake.started))
	}
	for i, want := range []struct{ what, mode string }{
		{"a card run", "unattended"}, {"a discussion", "interactive"}, {"the board session", "interactive"},
	} {
		if got := fake.started[i].Interaction; got != want.mode {
			t.Errorf("%s started with interaction %q, want %s", want.what, got, want.mode)
		}
	}

	// PATCH the board's discuss/chat models — the acceptance criterion: they
	// take effect on the next spawn of those roles and leave workers alone.
	resp := request(t, srv, "PATCH", "/api/boards/"+board.ID, cookie, nil, map[string]string{
		"discuss_model": "discuss-strong", "chat_model": "chat-strong",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch board: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	if got := spawnCard(plain.ID, map[string]string{"mode": "discuss"}); got != "discuss-strong" {
		t.Errorf("discuss model = %q, want discuss-strong", got)
	}
	if got := spawnBoardChat(); got != "chat-strong" {
		t.Errorf("board chat model = %q, want chat-strong", got)
	}
	if got := spawnCard(plain.ID, map[string]string{}); got != "board-default" {
		t.Errorf("worker model after the patch = %q, want board-default (untouched)", got)
	}

	// A card's own model wins over every board-level model…
	pinned := mkCard("run this one hot", strPtr("card-pinned"))
	if got := spawnCard(pinned.ID, map[string]string{}); got != "card-pinned" {
		t.Errorf("worker model on a pinned card = %q, want card-pinned", got)
	}
	if got := spawnCard(pinned.ID, map[string]string{"mode": "discuss"}); got != "card-pinned" {
		t.Errorf("discuss model on a pinned card = %q, want card-pinned", got)
	}
	// …and an explicit per-spawn model still wins over the card's.
	if got := spawnCard(pinned.ID, map[string]string{"model": "one-off"}); got != "one-off" {
		t.Errorf("explicit spawn model = %q, want one-off", got)
	}

	// Clearing the card's model returns it to the board's chain.
	if _, err := db.UpdateCard(ctx, pool, pinned.ID, db.CardParams{Model: strPtr("")},
		db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	if got := spawnCard(pinned.ID, map[string]string{}); got != "board-default" {
		t.Errorf("worker model after clearing the card override = %q, want board-default", got)
	}
}
