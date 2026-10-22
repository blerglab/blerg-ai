package db_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// Resolving a bare repo renames it on the board and on every card of that board that names it;
// a card that already names the owner/name keeps one entry, and another board is untouched.
func TestResolveBoardRepo(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"app", "lib", "example-org/app"}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "other", Repos: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(boardID string, repos []string) db.Card {
		res, err := db.CreateCard(ctx, pool, boardID, db.CardParams{Title: strp("c"), Repos: &repos}, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		return res.Card
	}
	plain := mk(board.ID, []string{"app", "lib"})
	both := mk(board.ID, []string{"lib", "app", "example-org/app"})
	primary := mk(board.ID, []string{"app", "lib", "example-org/app"})
	foreign := mk(other.ID, []string{"app"})

	got, err := db.ResolveBoardRepo(ctx, pool, board.ID, "app", "example-org/app")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Repos, []string{"example-org/app", "lib"}) {
		t.Fatalf("board repos = %v", got.Repos)
	}
	for _, tc := range []struct {
		card db.Card
		want []string
	}{{plain, []string{"example-org/app", "lib"}}, {both, []string{"lib", "example-org/app"}}, {primary, []string{"example-org/app", "lib"}}, {foreign, []string{"app"}}} {
		c, err := db.GetCard(ctx, pool, tc.card.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(c.Repos, tc.want) {
			t.Errorf("card repos = %v, want %v", c.Repos, tc.want)
		}
		if tc.card.ID != foreign.ID && c.Version <= tc.card.Version {
			t.Errorf("card version did not move: %d", c.Version)
		}
	}
	if _, err := db.ResolveBoardRepo(ctx, pool, board.ID, "app", "example-org/app"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("second resolve = %v, want db.ErrNotFound", err)
	}
}

// The board's first repo is its sessions' working directory: resolving it keeps it first, even
// when the owner/name sorts after another repo.
func TestResolveBoardRepoKeepsTheWorkingDirectory(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"app", "beta"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveBoardRepo(ctx, pool, board.ID, "app", "example-org/app"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetBoard(ctx, pool, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Repos, []string{"example-org/app", "beta"}) {
		t.Fatalf("board repos = %v; the working directory must stay the resolved app", got.Repos)
	}
	// A board's repos keep the order they were given.
	if _, err := db.UpdateBoard(ctx, pool, board.ID, db.BoardParams{Repos: []string{"zeta", "beta", "example-org/app"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetBoard(ctx, pool, board.ID); !slices.Equal(got.Repos, []string{"zeta", "beta", "example-org/app"}) {
		t.Fatalf("board repos after update = %v", got.Repos)
	}
}

// A card created, or given repos, while a repo is being renamed either sees the old name (and is
// renamed with the board) or the new one: never a repo the board no longer has.
func TestResolveBoardRepoConcurrentCardWrites(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"app", "lib"}})
		if err != nil {
			t.Fatal(err)
		}
		existing, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strp("c"), Repos: &[]string{"lib"}}, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		errs := make(chan error, 3)
		go func() {
			_, err := db.ResolveBoardRepo(ctx, pool, board.ID, "app", "example-org/app")
			errs <- err
		}()
		go func() {
			_, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strp("new"), Repos: &[]string{"app"}}, db.EventMeta{Actor: "human"})
			if errors.Is(err, db.ErrInvalidRepos) {
				err = nil // created after the rename: "app" is gone, refused
			}
			errs <- err
		}()
		go func() {
			_, err := db.UpdateCard(ctx, pool, existing.Card.ID, db.CardParams{Repos: &[]string{"app"}}, db.EventMeta{Actor: "human"})
			if errors.Is(err, db.ErrInvalidRepos) {
				err = nil
			}
			errs <- err
		}()
		for range 3 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		var stray int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM card_repos cr JOIN cards c ON c.id = cr.card_id
			 WHERE c.board_id = $1 AND NOT EXISTS (SELECT 1 FROM board_repos b WHERE b.board_id = c.board_id AND b.repo = cr.repo)`, board.ID).Scan(&stray); err != nil {
			t.Fatal(err)
		}
		if stray != 0 {
			t.Fatalf("round %d: %d card repos name a repo the board no longer has", i, stray)
		}
	}
}
