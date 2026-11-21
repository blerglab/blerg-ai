package api_test

import (
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestBoardHygieneRecentAndAutoArchive(t *testing.T) {
	srv, pool := testServer(t)
	ctx := t.Context()
	b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Hygiene"})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var done, live string
	for _, col := range cols {
		if col.Terminal() {
			done = col.ID
		} else {
			live = col.ID
		}
	}
	makeCard := func(title, col, age string) string {
		t.Helper()
		r, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &col}, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `UPDATE cards SET column_entered_at=now()-$2::interval,updated_at=now()-$2::interval WHERE id=$1`, r.Card.ID, age); err != nil {
			t.Fatal(err)
		}
		return r.Card.ID
	}
	old := makeCard("Old done", done, "30 days")
	recent := makeCard("Recent done", done, "1 day")
	active := makeCard("Old unfinished", live, "30 days")
	path := "/api/boards/" + b.ID + "/cards/view"
	var view struct {
		Cards []db.Card      `json:"cards"`
		Older map[string]int `json:"older"`
	}
	resp := request(t, srv, "GET", path, adminToken(t), nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("view status: %d", resp.StatusCode)
	}
	decodeBody(t, resp, &view)
	if len(view.Cards) != 2 || view.Older[done] != 1 {
		t.Fatalf("recent view: %+v", view)
	}
	resp = request(t, srv, "GET", path+"?expand=foreign-column", adminToken(t), nil, nil)
	decodeBody(t, resp, &view)
	if len(view.Cards) != 2 {
		t.Fatal("foreign expansion exposed older cards")
	}
	resp = request(t, srv, "GET", path+"?expand="+done, adminToken(t), nil, nil)
	decodeBody(t, resp, &view)
	if len(view.Cards) != 3 {
		t.Fatalf("expanded: %+v", view)
	}
	// Disabled by default, including old done cards.
	srvAPI.SweepStaleCards(ctx)
	c, _ := db.GetCard(ctx, pool, old)
	if c.ArchivedAt != nil {
		t.Fatal("archived without opt-in")
	}
	resp = request(t, srv, "PATCH", "/api/boards/"+b.ID, adminToken(t), nil, map[string]any{"auto_archive_done_days": 14})
	var settings map[string]json.RawMessage
	decodeBody(t, resp, &settings)
	if string(settings["auto_archive_done_days"]) != "14" {
		t.Fatalf("setting not saved: %s", settings["auto_archive_done_days"])
	}
	working := makeCard("Done with a live session", done, "30 days")
	if _, err = pool.Exec(ctx, `INSERT INTO runner_sessions (card_id,board_id,runner,external_session_id,lifecycle,role) VALUES ($1,$2,'fixture','active-session','running','worker')`, working, b.ID); err != nil {
		t.Fatal(err)
	}
	srvAPI.SweepStaleCards(ctx)
	for _, id := range []string{old, recent, active, working} {
		c, err := db.GetCard(ctx, pool, id)
		if err != nil {
			t.Fatal(err)
		}
		if (c.ArchivedAt != nil) != (id == old) {
			t.Fatalf("archive state for %s: %+v", id, c)
		}
	}
	resp = request(t, srv, "GET", "/api/boards/"+b.ID+"/cards/search?archived=1&q=Old+done", adminToken(t), nil, nil)
	var archived []db.Card
	decodeBody(t, resp, &archived)
	if len(archived) != 1 || archived[0].ID != old || archived[0].ArchivedAt == nil {
		t.Fatalf("archived search: %+v", archived)
	}
	resp = request(t, srv, "PATCH", "/api/boards/"+b.ID, adminToken(t), nil, map[string]any{"auto_archive_done_days": 0})
	decodeBody(t, resp, &settings)
	if string(settings["auto_archive_done_days"]) != "0" {
		t.Fatal("could not disable archiving")
	}
	events, err := db.ListCardEvents(ctx, pool, old, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == "archived" && e.Actor == "service" {
			found = true
		}
	}
	if !found {
		t.Fatal("auto-archive needs an audit event")
	}
	resp = request(t, srv, "PATCH", "/api/boards/"+b.ID, adminToken(t), nil, map[string]any{"auto_archive_done_days": -1})
	if resp.StatusCode != 422 {
		t.Fatalf("negative days status %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAutoArchiveSkipsLockedCardAndRespectsConcurrentMove(t *testing.T) {
	_, pool := testServer(t)
	ctx := t.Context()
	days := 14
	b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Retention race", AutoArchiveDoneDays: &days})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var done, live string
	for _, col := range cols {
		if col.Terminal() {
			done = col.ID
		} else {
			live = col.ID
		}
	}
	title := "Being reopened"
	r, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &done}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE cards SET column_entered_at=now()-interval '30 days',updated_at=now()-interval '30 days' WHERE id=$1`, r.Card.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) // rollback after commit is a no-op
	if _, err = tx.Exec(ctx, `SELECT id FROM cards WHERE id=$1 FOR UPDATE`, r.Card.ID); err != nil {
		t.Fatal(err)
	}
	changed, err := db.AutoArchiveDone(ctx, pool)
	if err != nil || len(changed) != 0 {
		t.Fatalf("locked card: %v %v", changed, err)
	}
	if _, err = tx.Exec(ctx, `UPDATE cards SET column_id=$2 WHERE id=$1`, r.Card.ID, live); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AutoArchiveDone(ctx, pool); err != nil {
		t.Fatal(err)
	}
	card, err := db.GetCard(ctx, pool, r.Card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if card.ArchivedAt != nil || card.ColumnID == nil || *card.ColumnID != live {
		t.Fatalf("concurrent move overwritten: %+v", card)
	}
}
