package db_test

import (
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/testsupport"
)

func TestRetentionReviewMigrationRecoversEntryNotReorder(t *testing.T) {
	pool := testsupport.Pool(t, db.RunMigrations)
	ctx := t.Context()
	b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Legacy retention"})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := cols[len(cols)-1].ID
	ids := []string{}
	for _, name := range []string{"unknown history", "moved then reordered", "created in done"} {
		col := done
		if name == "moved then reordered" {
			col = cols[0].ID
		}
		c, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &name, ColumnID: &col}, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.Card.ID)
		if _, err = pool.Exec(ctx, `UPDATE cards SET created_at=now()-interval '60 days' WHERE id=$1`, c.Card.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `UPDATE card_events SET created_at=now()-interval '60 days' WHERE card_id=$1`, c.Card.ID); err != nil {
			t.Fatal(err)
		}
		if name == "unknown history" {
			if _, err = pool.Exec(ctx, `DELETE FROM card_events WHERE card_id=$1`, c.Card.ID); err != nil {
				t.Fatal(err)
			}
		}
		if name == "moved then reordered" {
			if _, err = db.MoveCard(ctx, pool, c.Card.ID, done, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `UPDATE card_events SET created_at=now()-interval '20 days' WHERE card_id=$1 AND type='moved'`, c.Card.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = db.MoveCard(ctx, pool, c.Card.ID, done, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Recreate the pre-034 schema, then exercise the real migration runner.
	for _, sql := range []string{`DROP TRIGGER card_column_entry ON cards`, `DROP FUNCTION track_card_column_entry()`, `ALTER TABLE cards DROP COLUMN column_entered_at`, `ALTER TABLE boards DROP COLUMN auto_archive_keep_tags`, `DELETE FROM schema_migrations WHERE filename='034_retention_safety.sql'`} {
		if _, err = pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		var age float64
		if err = pool.QueryRow(ctx, `SELECT extract(epoch FROM now()-column_entered_at)/86400 FROM cards WHERE id=$1`, id).Scan(&age); err != nil {
			t.Fatal(err)
		}
		want := []float64{0, 20, 60}[i]
		if age < want || age > want+0.1 {
			t.Errorf("entry clock %d age=%f days, want %f", i, age, want)
		}
	}
}
