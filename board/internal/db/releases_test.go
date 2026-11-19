package db_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestReleaseLifecycle(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Open", "Done"}, TerminalColumns: []string{"Done"}})
	cols, e := db.ListColumns(ctx, pool, b.ID)
	if e != nil {
		t.Fatal(e)
	}
	r, e := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "First", Status: "planned"})
	if e != nil {
		t.Fatal(e)
	}
	next, e := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Next", Status: "planned"})
	if e != nil {
		t.Fatal(e)
	}
	title := "Finished work"
	done, e := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &cols[1].ID, ReleaseID: &r.ID}, db.EventMeta{Actor: "human"})
	if e != nil {
		t.Fatal(e)
	}
	title = "Open work"
	open, e := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &cols[0].ID, ReleaseID: &r.ID}, db.EventMeta{Actor: "human"})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.CloseRelease(ctx, pool, b.ID, r.ID, r.ID, db.EventMeta{Actor: "human"}); e == nil {
		t.Fatal("accepted self rollover")
	}
	if e = db.CloseRelease(ctx, pool, b.ID, r.ID, next.ID, db.EventMeta{Actor: "human"}); e != nil {
		t.Fatal(e)
	}
	archived, e := db.GetCard(ctx, pool, done.Card.ID)
	if e != nil {
		t.Fatal(e)
	}
	if archived.ArchivedAt == nil || archived.ReleaseID == nil || *archived.ReleaseID != r.ID {
		t.Fatalf("archive: %+v", archived)
	}
	moved, e := db.GetCard(ctx, pool, open.Card.ID)
	if e != nil {
		t.Fatal(e)
	}
	if moved.ArchivedAt != nil || moved.ReleaseID == nil || *moved.ReleaseID != next.ID {
		t.Fatalf("rollover: %+v", moved)
	}
	record, e := db.ReadRelease(ctx, pool, b.ID, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if record.Release.ReleasedAt == nil || record.Release.ReleasedBy == nil {
		t.Fatal("missing release attribution")
	}
	if record.Release.Status != "released" || len(record.Cards) != 1 || record.Cards[0].ID != archived.ID {
		t.Fatalf("record: %+v", record)
	}
	if e = db.CloseRelease(ctx, pool, b.ID, r.ID, next.ID, db.EventMeta{Actor: "human"}); e == nil {
		t.Fatal("closed twice")
	}
	if _, e = db.UpdateCard(ctx, pool, moved.ID, db.CardParams{ReleaseID: &r.ID}, db.EventMeta{Actor: "human"}); e == nil {
		t.Fatal("assigned released record")
	}
	other := mkBoard(t, pool, db.BoardParams{})
	if _, e = db.CreateCard(ctx, pool, other.ID, db.CardParams{Title: &title, ReleaseID: &next.ID}, db.EventMeta{Actor: "human"}); e == nil {
		t.Fatal("foreign release")
	}
}

func TestReleaseRollbackAndSearch(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Open", "Done"}, TerminalColumns: []string{"Done"}})
	cols, e := db.ListColumns(ctx, pool, b.ID)
	if e != nil {
		t.Fatal(e)
	}
	r, e := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "First", Status: "in progress"})
	if e != nil {
		t.Fatal(e)
	}
	title := "Shipping"
	card, e := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &cols[0].ID, ReleaseID: &r.ID}, db.EventMeta{Actor: "human"})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.CloseRelease(ctx, pool, b.ID, r.ID, "", db.EventMeta{Actor: "human"}); e == nil {
		t.Fatal("closed with open cards and no next release")
	}
	record, e := db.ReadRelease(ctx, pool, b.ID, r.ID)
	if e != nil || record.Release.Status != "in progress" || len(record.Cards) != 1 {
		t.Fatalf("partial close: %+v %v", record, e)
	}
	if _, e = db.MoveCard(ctx, pool, card.Card.ID, cols[1].ID, nil, nil, db.EventMeta{Actor: "human"}); e != nil {
		t.Fatal(e)
	}
	// Retention can archive Done before release day; it remains in the release record.
	if _, e = db.ArchiveCard(ctx, pool, card.Card.ID, nil, db.EventMeta{Actor: "human"}); e != nil {
		t.Fatal(e)
	}
	if e = db.CloseRelease(ctx, pool, b.ID, r.ID, "", db.EventMeta{Actor: "human"}); e != nil {
		t.Fatal(e)
	}
	record, e = db.ReadRelease(ctx, pool, b.ID, r.ID)
	if e != nil || record.Done != 1 || record.ByColumn["Done"] != 1 {
		t.Fatalf("lost archived progress: %+v %v", record, e)
	}
	found, e := db.SearchCards(ctx, pool, db.SearchParams{BoardID: b.ID, ReleaseID: r.ID, IncludeArchived: true, Query: "Shipping"})
	if e != nil || len(found) != 1 {
		t.Fatalf("archive search: %+v %v", found, e)
	}
	view, e := db.ReadBoardCardView(ctx, pool, b.ID, db.CardViewOptions{Filter: db.BoardFilter{Release: r.ID}})
	if e != nil || len(view.Cards) != 0 {
		t.Fatalf("archive visible in live view: %+v %v", view, e)
	}
}

func TestReleaseLiveFilterAndClear(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{})
	r, e := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Next", Status: "planned"})
	if e != nil {
		t.Fatal(e)
	}
	title := "Targeted"
	c, e := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ReleaseID: &r.ID}, db.EventMeta{Actor: "human"})
	if e != nil {
		t.Fatal(e)
	}
	view, e := db.ReadBoardCardView(ctx, pool, b.ID, db.CardViewOptions{Filter: db.BoardFilter{Release: r.ID}})
	if e != nil || len(view.Cards) != 1 {
		t.Fatalf("release filter: %+v %v", view, e)
	}
	empty := ""
	updated, e := db.UpdateCard(ctx, pool, c.Card.ID, db.CardParams{ReleaseID: &empty}, db.EventMeta{Actor: "human"})
	if e != nil || updated.ReleaseID != nil {
		t.Fatalf("clear: %+v %v", updated, e)
	}
	view, e = db.ReadBoardCardView(ctx, pool, b.ID, db.CardViewOptions{Filter: db.BoardFilter{Release: "none"}})
	if e != nil || len(view.Cards) != 1 {
		t.Fatalf("unassigned filter: %+v %v", view, e)
	}
}

func TestReleaseCutRefusesActiveSessions(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Done"}, TerminalColumns: []string{"Done"}})
	r, e := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "First", Status: "planned"})
	if e != nil {
		t.Fatal(e)
	}
	title := "Still running"
	card, e := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ReleaseID: &r.ID}, db.EventMeta{Actor: "human"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO runner_sessions(card_id,board_id,runner,external_session_id,lifecycle,role) VALUES($1,$2,'fixture','release-session','running','worker')`, card.Card.ID, b.ID); e != nil {
		t.Fatal(e)
	}
	if e = db.CloseRelease(ctx, pool, b.ID, r.ID, "", db.EventMeta{Actor: "human"}); e == nil {
		t.Fatal("archived active session card")
	}
	after, e := db.GetCard(ctx, pool, card.Card.ID)
	if e != nil || after.ArchivedAt != nil || after.Version != card.Card.Version {
		t.Fatalf("partial write: %+v %v", after, e)
	}
}

func TestReleaseRestore(t *testing.T) {
	for _, cut := range []bool{false, true} {
		for _, prearchive := range []bool{false, true} {
			t.Run(fmt.Sprintf("cut=%v/prearchive=%v", cut, prearchive), func(t *testing.T) {
				pool := testPool(t)
				ctx := t.Context()
				ev := db.EventMeta{Actor: "human"}
				b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Open", "Done"}, TerminalColumns: []string{"Done"}})
				cols, e := db.ListColumns(ctx, pool, b.ID)
				if e != nil {
					t.Fatal(e)
				}
				r, e := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "First", Status: "planned"})
				if e != nil {
					t.Fatal(e)
				}
				title := "Restore me"
				c, e := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &cols[1].ID, ReleaseID: &r.ID}, ev)
				if e != nil {
					t.Fatal(e)
				}
				if prearchive || !cut {
					if _, e = db.ArchiveCard(ctx, pool, c.Card.ID, nil, ev); e != nil {
						t.Fatal(e)
					}
				}
				if cut {
					if e = db.CloseRelease(ctx, pool, b.ID, r.ID, "", ev); e != nil {
						t.Fatal(e)
					}
				}
				restored, e := db.MoveCard(ctx, pool, c.Card.ID, cols[0].ID, nil, nil, ev)
				if e != nil {
					t.Fatal(e)
				}
				if cut && restored.ReleaseID != nil {
					t.Fatalf("restored released card retains release: %+v", restored)
				}
				record, e := db.ReadRelease(ctx, pool, b.ID, r.ID)
				if e != nil {
					t.Fatal(e)
				}
				if record.Done != 0 || len(record.DoneCardIDs) != 0 {
					t.Fatalf("restored card counted Done: %+v", record)
				}
				if cut && (len(record.RestoredCardIDs) != 1 || record.RestoredCardIDs[0] != restored.ID) {
					t.Fatal("missing restored note")
				}
				if !cut && len(record.Cards) != 1 {
					t.Fatal("active restored card missing")
				}
			})
		}
	}
}

func TestArchivedDoneCannotRetarget(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	ev := db.EventMeta{Actor: "human"}
	b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Done"}, TerminalColumns: []string{"Done"}})
	r, e := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "First", Status: "planned"})
	if e != nil {
		t.Fatal(e)
	}
	title := "Archived"
	c, e := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ReleaseID: &r.ID}, ev)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.ArchiveCard(ctx, pool, c.Card.ID, nil, ev); e != nil {
		t.Fatal(e)
	}
	empty := ""
	if _, e = db.UpdateCard(ctx, pool, c.Card.ID, db.CardParams{ReleaseID: &empty}, ev); e == nil {
		t.Fatal("retargeted archived Done card")
	}
}

func TestReleaseCutSessionScope(t *testing.T) {
	for _, lifecycle := range []string{"idle", "disconnected", "running"} {
		for _, terminal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/done=%v", lifecycle, terminal), func(t *testing.T) {
				pool := testPool(t)
				ctx := t.Context()
				ev := db.EventMeta{Actor: "human"}
				b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Open", "Done"}, TerminalColumns: []string{"Done"}})
				cols, e := db.ListColumns(ctx, pool, b.ID)
				if e != nil {
					t.Fatal(e)
				}
				r, e := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "First", Status: "planned"})
				if e != nil {
					t.Fatal(e)
				}
				next, e := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Next", Status: "planned"})
				if e != nil {
					t.Fatal(e)
				}
				col := cols[0].ID
				if terminal {
					col = cols[1].ID
				}
				title := "Session blocker"
				c, e := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &col, ReleaseID: &r.ID}, ev)
				if e != nil {
					t.Fatal(e)
				}
				_, e = pool.Exec(ctx, `INSERT INTO runner_sessions(card_id,board_id,runner,external_session_id,lifecycle,role) VALUES($1,$2,'fixture','session',$3,'worker')`, c.Card.ID, b.ID, lifecycle)
				if e != nil {
					t.Fatal(e)
				}
				e = db.CloseRelease(ctx, pool, b.ID, r.ID, next.ID, ev)
				if terminal && lifecycle == "running" {
					if e == nil || !strings.Contains(e.Error(), "#1 Session blocker") {
						t.Fatalf("missing named blocker: %v", e)
					}
				} else if e != nil {
					t.Fatal(e)
				}
			})
		}
	}
}

func TestReleaseDeletionAndInvalidID(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{})
	if _, e := db.ReadRelease(ctx, pool, b.ID, "bad"); e == nil {
		t.Fatal("bad ID accepted")
	} else {
		var field *db.FieldError
		if !errors.As(e, &field) {
			t.Fatalf("not a field error: %v", e)
		}
	}
	r, e := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Mistake", Status: "planned"})
	if e != nil {
		t.Fatal(e)
	}
	title := "Keep card"
	c, e := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ReleaseID: &r.ID}, db.EventMeta{Actor: "human"})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.DeleteRelease(ctx, pool, b.ID, r.ID); e != nil {
		t.Fatal(e)
	}
	card, e := db.GetCard(ctx, pool, c.Card.ID)
	if e != nil || card.ReleaseID != nil {
		t.Fatalf("assignment: %+v %v", card, e)
	}
}

func TestReleaseCutRejectsChangedReviewSnapshot(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	ev := db.EventMeta{Actor: "human"}
	b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Done"}, TerminalColumns: []string{"Done"}})
	r, e := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "First", Status: "planned"})
	if e != nil {
		t.Fatal(e)
	}
	title := "Reviewed"
	c, e := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ReleaseID: &r.ID}, ev)
	if e != nil {
		t.Fatal(e)
	}
	reviews := map[string]db.ReleaseArchiveReview{c.Card.ID: {Version: c.Card.Version, Meta: ev}}
	title = "Changed"
	if _, e = db.UpdateCard(ctx, pool, c.Card.ID, db.CardParams{Title: &title}, ev); e != nil {
		t.Fatal(e)
	}
	if e = db.CloseReleaseChecked(ctx, pool, b.ID, r.ID, "", ev, reviews); !errors.Is(e, db.ErrStaleVersion) {
		t.Fatalf("cut stale snapshot: %v", e)
	}
	card, e := db.GetCard(ctx, pool, c.Card.ID)
	if e != nil || card.ArchivedAt != nil {
		t.Fatalf("partial cut %+v %v", card, e)
	}
}

func TestReleaseBurnHistory(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Open", "Done"}, TerminalColumns: []string{"Done"}})
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Burn", Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	title := "Work"
	size := "M"
	tags := []string{"to-test", "milestone-a"}
	c, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &cols[0].ID, ReleaseID: &r.ID, Size: &size, Tags: &tags}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MoveCard(ctx, pool, c.Card.ID, cols[1].ID, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	tags = []string{"milestone-a"}
	if _, err = db.UpdateCard(ctx, pool, c.Card.ID, db.CardParams{Tags: &tags}, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE card_events SET created_at=now()-interval '4 days'+(id-(SELECT min(id) FROM card_events WHERE card_id=$1))*interval '1 day' WHERE card_id=$1`, c.Card.ID); err != nil {
		t.Fatal(err)
	}
	chart, err := db.ReleaseBurn(ctx, pool, b.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chart.Count) < 3 || chart.Count[1].Done != 1 || chart.Count[1].Confirmed != 0 || chart.Count[2].Confirmed != 1 || chart.Size[2].Scope != 2 {
		t.Fatalf("%+v", chart)
	}
	tags = []string{}
	if _, err = db.UpdateCard(ctx, pool, c.Card.ID, db.CardParams{Tags: &tags}, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	milestone, err := db.ReleaseBurn(ctx, pool, b.ID, "tag:milestone-a")
	if err != nil {
		t.Fatal(err)
	}
	if milestone.Count[len(milestone.Count)-1].Scope != 0 {
		t.Fatalf("removed milestone %+v", milestone)
	}

	// Legacy move events still backfill Done dates, with an explicit history notice.
	if _, err = pool.Exec(ctx, `UPDATE card_events SET data=data-'burn_after'-'burn_before'-'from_release'-'to_release' WHERE card_id=$1`, c.Card.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err := db.ReleaseBurn(ctx, pool, b.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.HistoryNote == "" || legacy.Count[0].Done != 0 || legacy.Count[1].Done != 1 {
		t.Fatalf("legacy backfill %+v", legacy)
	}
}

func TestMilestoneBurnPreservesArchivedDone(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Open", "Done"}, TerminalColumns: []string{"Done"}})
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	tags := []string{"milestone-a"}
	title := "Work"
	for _, col := range cols {
		c, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &col.ID, Tags: &tags}, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.ArchiveCard(ctx, pool, c.Card.ID, nil, db.EventMeta{Actor: "human"}); err != nil {
			t.Fatal(err)
		}
		if err = db.AppendComment(ctx, pool, c.Card.ID, "Recorded", db.EventMeta{Actor: "human"}); err != nil {
			t.Fatal(err)
		}
		// A later comment must preserve completion even when the archive predates snapshots.
		if _, err = pool.Exec(ctx, `UPDATE card_events SET data=data-'burn_after' WHERE card_id=$1`, c.Card.ID); err != nil {
			t.Fatal(err)
		}
		if err = db.AppendComment(ctx, pool, c.Card.ID, "Legacy update", db.EventMeta{Actor: "human"}); err != nil {
			t.Fatal(err)
		}
	}
	chart, err := db.ReleaseBurn(ctx, pool, b.ID, "tag:milestone-a")
	if err != nil {
		t.Fatal(err)
	}
	last := chart.Count[len(chart.Count)-1]
	if last.Scope != 1 || last.Done != 1 {
		t.Fatalf("archived progress %+v", last)
	}
}

func TestLegacyCutRetainsOpenHistoryAndRestoredMarker(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Open", "Done"}, TerminalColumns: []string{"Done"}})
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Legacy", Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	title := "Work"
	meta := db.EventMeta{Actor: "human"}
	c, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &cols[0].ID, ReleaseID: &r.ID}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MoveCard(ctx, pool, c.Card.ID, cols[1].ID, nil, nil, meta); err != nil {
		t.Fatal(err)
	}
	if err = db.CloseRelease(ctx, pool, b.ID, r.ID, "", meta); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE card_events SET data=data-'burn_after'-'burn_before',created_at=date_trunc('day',now())-interval '3 days'+(id-(SELECT min(id) FROM card_events WHERE card_id=$1))*interval '1 day' WHERE card_id=$1`, c.Card.ID); err != nil {
		t.Fatal(err)
	}
	chart, err := db.ReleaseBurn(ctx, pool, b.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chart.Count) < 3 || chart.Count[0].Scope != 1 || chart.Count[0].Done != 0 || chart.Count[1].Done != 1 {
		t.Fatalf("legacy cut lost open history: %+v", chart)
	}
	if _, err = db.MoveCard(ctx, pool, c.Card.ID, cols[0].ID, nil, nil, meta); err != nil {
		t.Fatal(err)
	}
	view, err := db.ReadRelease(ctx, pool, b.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(view.Burn)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.RestoredCardIDs) != 1 || !strings.Contains(string(raw), `"restored":1`) {
		t.Fatalf("missing restored marker: %+v", view)
	}
}

func TestBurnLegacyFlowAndUnrelatedHistory(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Open", "Done"}, TerminalColumns: []string{"Done"}})
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Flow", Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	title := "Work"
	meta := db.EventMeta{Actor: "human"}
	c, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &cols[1].ID, ReleaseID: &r.ID}, meta)
	if err != nil {
		t.Fatal(err)
	}
	// A legacy flow snapshot remains authoritative after column semantics change.
	if _, err = pool.Exec(ctx, `UPDATE card_events SET data=data-'burn_after'-'burn_before' WHERE card_id=$1`, c.Card.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE board_columns SET is_terminal=false WHERE id=$1`, cols[1].ID); err != nil {
		t.Fatal(err)
	}
	chart, err := db.ReleaseBurn(ctx, pool, b.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if chart.Count[len(chart.Count)-1].Done != 1 {
		t.Fatalf("lost flow snapshot: %+v", chart)
	}
	// A different release with complete history does not inherit that warning.
	other, err := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Other", Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ReleaseID: &other.ID}, meta); err != nil {
		t.Fatal(err)
	}
	chart, err = db.ReleaseBurn(ctx, pool, b.ID, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if chart.HistoryNote != "" {
		t.Fatalf("unrelated history warning: %+v", chart)
	}
	// Broken optional chart history must not hide the release's cards and totals.
	if _, err = pool.Exec(ctx, `UPDATE card_events SET data=jsonb_set(data,'{burn_after}','"invalid"'::jsonb) WHERE card_id=$1`, c.Card.ID); err != nil {
		t.Fatal(err)
	}
	view, err := db.ReadRelease(ctx, pool, b.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Cards) != 1 || !view.Burn.Unavailable {
		t.Fatalf("release lost on chart error: %+v", view)
	}
}
