package api_test

import (
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestRetentionReviewProtectsCards(t *testing.T) {
	_, pool := testServer(t)
	ctx := t.Context()
	days := 14
	for _, name := range []string{"Not done yet", "Backlog (undone)", "stuck", "mirror"} {
		driven := ""
		if name == "mirror" {
			driven = "external"
		}
		b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: name, AutoArchiveDoneDays: &days, DrivenBy: &driven})
		if name == "mirror" {
			if err == nil {
				t.Fatal("allowed retention on a mirror")
			}
			b, err = db.CreateBoard(ctx, pool, db.BoardParams{Name: name, DrivenBy: &driven})
			if err == nil {
				_, err = pool.Exec(ctx, `UPDATE boards SET auto_archive_done_days=14 WHERE id=$1`, b.ID)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		col, err := db.CreateColumn(ctx, pool, b.ID, name, name == "stuck" || name == "mirror")
		if err != nil {
			t.Fatal(err)
		}
		title := "Keep this card"
		r, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &col.ID}, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `UPDATE cards SET column_entered_at=now()-interval '30 days',updated_at=now()-interval '30 days',stuck_at=CASE WHEN $2 THEN now() END WHERE id=$1`, r.Card.ID, name == "stuck"); err != nil {
			t.Fatal(err)
		}
		if _, err = db.AutoArchiveDone(ctx, pool); err != nil {
			t.Fatal(err)
		}
		got, err := db.GetCard(ctx, pool, r.Card.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ArchivedAt != nil {
			t.Errorf("archived protected %s card", name)
		}
	}
}

func TestArchiveReviewRejectsDirectAgent(t *testing.T) {
	srv, pool := testServer(t)
	ctx := t.Context()
	no := false
	b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Authority", RequireRepo: &no, GateEnabled: &no})
	if err != nil {
		t.Fatal(err)
	}
	title := "Must not archive"
	r, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	tok := projectToken(t, b.ID, []string{"card.read", "card.write"})
	resp := request(t, srv, "POST", "/api/cards/"+r.Card.ID+"/archive", tok, nil, nil)
	if resp.StatusCode != 403 {
		t.Errorf("direct agent archive = %d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestRetentionReviewClockTagsPreviewAndFairness(t *testing.T) {
	srv, pool := testServer(t)
	ctx := t.Context()
	days := 14
	ids := []string{}
	for boardIndex := range 2 {
		b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Retention", AutoArchiveDoneDays: &days})
		if err != nil {
			t.Fatal(err)
		}
		col, err := db.CreateColumn(ctx, pool, b.ID, "Finished", true)
		if err != nil {
			t.Fatal(err)
		}
		count := 103
		if boardIndex == 1 {
			count = 1
		}
		for i := range count {
			title := "Eligible"
			tags := []string{}
			if i == 0 && boardIndex == 0 {
				tags = []string{"to-test"}
			}
			c, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &col.ID, Tags: &tags}, db.EventMeta{Actor: "human"})
			if err != nil {
				t.Fatal(err)
			}
			entry := "30 days"
			if i == 1 && boardIndex == 0 {
				entry = "1 day"
			}
			if _, err = pool.Exec(ctx, `UPDATE cards SET column_entered_at=now()-$2::interval,updated_at=now() WHERE id=$1`, c.Card.ID, entry); err != nil {
				t.Fatal(err)
			}
			if boardIndex == 0 && i < 2 {
				ids = append(ids, c.Card.ID)
			}
			if boardIndex == 1 {
				ids = append(ids, c.Card.ID)
			}
		}
		resp := request(t, srv, "POST", "/api/boards/"+b.ID+"/archive-preview", adminToken(t), nil, map[string]any{"auto_archive_done_days": 14, "auto_archive_keep_tags": []string{"to-test"}})
		var preview struct{ Count int }
		decodeBody(t, resp, &preview)
		want := 101
		if boardIndex == 1 {
			want = 1
		}
		if preview.Count != want {
			t.Fatalf("preview %d want %d", preview.Count, want)
		}
	}
	changed, err := db.AutoArchiveDone(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 2 {
		t.Fatalf("unfair sweep: %v", changed)
	}
	for i, id := range ids {
		c, err := db.GetCard(ctx, pool, id)
		if err != nil {
			t.Fatal(err)
		}
		if (c.ArchivedAt != nil) != (i == 2) {
			t.Fatalf("clock/tag/fairness card %d archived=%v", i, c.ArchivedAt)
		}
	}
}

func TestRetentionReviewMovesResetEntryClock(t *testing.T) {
	_, pool := testServer(t)
	ctx := t.Context()
	days := 14
	b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Clock", AutoArchiveDoneDays: &days})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	title := "Moved recently"
	r, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &cols[0].ID}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE cards SET column_entered_at=now()-interval '30 days',updated_at=now()-interval '30 days' WHERE id=$1`, r.Card.ID); err != nil {
		t.Fatal(err)
	}
	done := cols[len(cols)-1].ID
	if _, err = db.MoveCard(ctx, pool, r.Card.ID, done, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE cards SET updated_at=now()-interval '30 days' WHERE id=$1`, r.Card.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AutoArchiveDone(ctx, pool); err != nil {
		t.Fatal(err)
	}
	c, err := db.GetCard(ctx, pool, r.Card.ID)
	if err != nil || c.ArchivedAt != nil {
		t.Fatalf("new entry archived: %+v %v", c, err)
	}
	custom := []string{"keep"}
	if _, err = db.UpdateBoard(ctx, pool, b.ID, db.BoardParams{AutoArchiveKeepTags: &custom}); err != nil {
		t.Fatal(err)
	}
	tags := []string{"keep"}
	if _, err = db.UpdateCard(ctx, pool, r.Card.ID, db.CardParams{Tags: &tags}, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE cards SET column_entered_at=now()-interval '30 days' WHERE id=$1`, r.Card.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AutoArchiveDone(ctx, pool); err != nil {
		t.Fatal(err)
	}
	c, err = db.GetCard(ctx, pool, r.Card.ID)
	if err != nil || c.ArchivedAt != nil {
		t.Fatalf("custom keep tag archived: %+v %v", c, err)
	}
}
