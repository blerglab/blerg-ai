package db_test

import (
	"errors"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestBoardKeys(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	key := "BLG"
	b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Blerg", Key: &key})
	if err != nil {
		t.Fatal(err)
	}
	if b.Key != key {
		t.Fatalf("key = %q", b.Key)
	}
	title := "Work"
	created, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title}, db.EventMeta{})
	c := created.Card
	if err != nil {
		t.Fatal(err)
	}
	if c.Reference != "BLG-1" {
		t.Fatalf("reference = %q", c.Reference)
	}
	renamed := "NEW"
	b, err = db.UpdateBoard(ctx, pool, b.ID, db.BoardParams{Key: &renamed})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{key, renamed} {
		id, err := db.BoardIDForKey(ctx, pool, k)
		if err != nil || id != b.ID {
			t.Fatalf("resolve %s = %s: %v", k, id, err)
		}
		if _, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Other", Key: &k}); err == nil {
			t.Fatalf("reused reserved key %s", k)
		}
	}
	c, err = db.GetCardByNumber(ctx, pool, b.ID, 1)
	if err != nil || c.Reference != "NEW-1" {
		t.Fatalf("canonical rename = %s: %v", c.Reference, err)
	}
	for _, invalid := range []string{"", "A", "abcdef", "ABCDEF", "AB-", "AbC", "12", "2026", "1AB"} {
		_, err := db.UpdateBoard(ctx, pool, b.ID, db.BoardParams{Key: &invalid})
		var field *db.FieldError
		if !errors.As(err, &field) || field.Key != "key" {
			t.Fatalf("invalid %q must fail validation before SQL: %v", invalid, err)
		}
	}
}

func TestBoardKeySuggestions(t *testing.T) {
	for name, want := range map[string]string{"Blerg": "BLG", "AfterDark": "AD", "Alpha Team": "AT", "!!!": "BD", "2026": "BD", "1 Board": "BD"} {
		if got := db.SuggestBoardKey(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

// Reconstruct the pre-upgrade schema in a private fixture, retaining its boards and cards.
func TestBoardKeysUpgradeRequiresConfirmation(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{Name: "Blerg"})
	created, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: strp("Retain me")}, db.EventMeta{})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE board_keys`,
		`ALTER TABLE boards DROP COLUMN board_key`,
		`DELETE FROM schema_migrations WHERE filename IN ('029_board_keys.sql','030_board_key_letter.sql')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := db.RunMigrations(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	upgraded, err := db.GetBoard(ctx, pool, b.ID)
	if err != nil || upgraded.Key != "" || upgraded.SuggestedKey != "BLG" {
		t.Fatalf("upgrade = %+v: %v", upgraded, err)
	}
	c, err := db.GetCardByNumber(ctx, pool, b.ID, created.Card.Number)
	if err != nil || c.ID != created.Card.ID || c.Reference != "" {
		t.Fatalf("legacy card = %+v: %v", c, err)
	}
	key := upgraded.SuggestedKey
	if _, err := db.UpdateBoard(ctx, pool, b.ID, db.BoardParams{Key: &key}); err != nil {
		t.Fatal(err)
	}
	c, err = db.GetCard(ctx, pool, c.ID)
	if err != nil || c.Reference != "BLG-1" {
		t.Fatalf("confirmed card = %+v: %v", c, err)
	}
}

func TestBoardKeyConcurrentClaims(t *testing.T) {
	pool := testPool(t)
	key := "SAME"
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Concurrent", Key: &key})
			results <- err
		}()
	}
	successes := 0
	for range 2 {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful claims = %d", successes)
	}
	boards, err := db.ListBoards(t.Context(), pool)
	if err != nil || len(boards) != 1 {
		t.Fatalf("partial board leaked: %+v: %v", boards, err)
	}
}

func TestBoardKeySuggestionsAvoidReservedAliases(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{Name: "Blerg"})
	next := "NEW"
	if _, err := db.UpdateBoard(ctx, pool, b.ID, db.BoardParams{Key: &next}); err != nil {
		t.Fatal(err)
	}
	key, err := db.AvailableBoardKey(ctx, pool, "Blerg")
	if err != nil || key != "BLG1" {
		t.Fatalf("suggestion = %q, %v", key, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE boards SET board_key='' WHERE id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	boards, err := db.ListBoards(ctx, pool)
	if err != nil || boards[0].SuggestedKey != "BLG1" {
		t.Fatalf("list suggestion = %+v, %v", boards, err)
	}
	for _, invalid := range []string{"12", "1AB"} {
		if _, err := pool.Exec(ctx, `UPDATE boards SET board_key=$1 WHERE id=$2`, invalid, b.ID); err == nil {
			t.Fatalf("DB accepted current key %s", invalid)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO board_keys(key,board_id) VALUES($1,$2)`, invalid, b.ID); err == nil {
			t.Fatalf("DB accepted alias %s", invalid)
		}
	}
}
