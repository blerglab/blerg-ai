package db_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/contracts/releasecut"
)

func TestReleaseCutReceiptPreservesPreCutCards(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Open", "Done"}, TerminalColumns: []string{"Done"}})
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "First", Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Next", Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"Open scope", "Finished scope"} {
		if _, err = db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &cols[i].ID, ReleaseID: &r.ID}, db.EventMeta{Actor: "human"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.CloseRelease(ctx, pool, b.ID, r.ID, next.ID, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	view, err := db.ReadRelease(ctx, pool, b.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view)
	var out map[string]json.RawMessage
	_ = json.Unmarshal(raw, &out)
	var receipt struct {
		ID       string
		Snapshot []json.RawMessage `json:"snapshot"`
		Next     string            `json:"next_release_id"`
	}
	if len(out["cut_receipt"]) == 0 || json.Unmarshal(out["cut_receipt"], &receipt) != nil || receipt.ID == "" || len(receipt.Snapshot) != 2 || receipt.Next != next.ID {
		t.Fatalf("close did not preserve its immutable pre-cut evidence: %s", raw)
	}
}

func TestReleaseCutReceiptGuards(t *testing.T) {
	for _, scenario := range []string{"replay_and_immutable", "deadline", "changed_cards", "receipt_failure"} {
		t.Run(scenario, func(t *testing.T) {
			pool := testPool(t)
			ctx := t.Context()
			b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Open", "Done"}, TerminalColumns: []string{"Done"}})
			release, err := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Cut", Status: "planned"})
			if err != nil {
				t.Fatal(err)
			}
			next, err := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Next", Status: "planned"})
			if err != nil {
				t.Fatal(err)
			}
			var ask string
			if err = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&ask); err != nil {
				t.Fatal(err)
			}
			view, err := db.ReadRelease(ctx, pool, b.ID, release.ID)
			if err != nil {
				t.Fatal(err)
			}
			intent := releasecut.Intent{AskID: ask, OwnerID: "owner", CardDigest: view.CutPreview.Digest, Deadline: time.Now().Add(time.Hour)}
			if scenario == "deadline" {
				intent.Deadline = time.Now().Add(-time.Second)
			}
			if scenario == "changed_cards" || scenario == "receipt_failure" {
				cols, e := db.ListColumns(ctx, pool, b.ID)
				if e != nil {
					t.Fatal(e)
				}
				for i, title := range []string{"Open scope", "Done scope"} {
					if _, e = db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &cols[i].ID, ReleaseID: &release.ID}, db.EventMeta{Actor: "human"}); e != nil {
						t.Fatal(e)
					}
				}
				if scenario == "receipt_failure" {
					view, err = db.ReadRelease(ctx, pool, b.ID, release.ID)
					if err != nil {
						t.Fatal(err)
					}
					intent.CardDigest = view.CutPreview.Digest
					if _, err = pool.Exec(ctx, `CREATE FUNCTION refuse_cut_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected receipt failure'; END $$; CREATE TRIGGER refuse_cut_receipt BEFORE INSERT ON release_cut_receipts FOR EACH ROW EXECUTE FUNCTION refuse_cut_receipt()`); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = db.CloseReleaseChecked(ctx, pool, b.ID, release.ID, next.ID, db.EventMeta{Actor: "human"}, nil, intent)
			if scenario == "replay_and_immutable" {
				if err != nil {
					t.Fatal(err)
				}
				view, err = db.ReadRelease(ctx, pool, b.ID, release.ID)
				if err != nil {
					t.Fatal(err)
				}
				if view.CutReceipt == nil || view.CutReceipt.AskID != ask {
					t.Fatal("authorization cause is missing")
				}
				first := view.CutReceipt.ID
				if err = db.CloseReleaseChecked(ctx, pool, b.ID, release.ID, next.ID, db.EventMeta{Actor: "human"}, nil, intent); err != nil {
					t.Fatalf("same-cause replay: %v", err)
				}
				for _, sql := range []string{`UPDATE release_cut_receipts SET receipt='{}'`, `DELETE FROM release_cut_receipts`} {
					if _, err = pool.Exec(ctx, sql); err == nil {
						t.Fatal("receipt mutation succeeded")
					}
				}
				view, err = db.ReadRelease(ctx, pool, b.ID, release.ID)
				if err != nil || view.CutReceipt.ID != first {
					t.Fatal("replay or mutation rewrote evidence")
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe close succeeded")
			}
			view, err = db.ReadRelease(ctx, pool, b.ID, release.ID)
			if err != nil {
				t.Fatal(err)
			}
			if view.Release.Status != "planned" || view.CutReceipt != nil {
				t.Fatal("failed cut left released state or evidence")
			}
			if scenario == "changed_cards" || scenario == "receipt_failure" {
				var untouched int
				if err = pool.QueryRow(ctx, `SELECT count(*) FROM cards WHERE release_id=$1 AND archived_at IS NULL`, release.ID).Scan(&untouched); err != nil || untouched != 2 {
					t.Fatalf("partial card effects: %d %v", untouched, err)
				}
			}
		})
	}
}

func TestReleaseCutLocksColumnSnapshot(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Done"}, TerminalColumns: []string{"Done"}})
	columns, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	release, err := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Cut", Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	title := "Done"
	card, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &columns[0].ID, ReleaseID: &release.ID}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, `SELECT id FROM cards WHERE id=$1 FOR UPDATE`, card.Card.ID); err != nil {
		t.Fatal(err)
	}
	cutDone := make(chan error, 1)
	go func() { cutDone <- db.CloseRelease(ctx, pool, b.ID, release.ID, "", db.EventMeta{Actor: "human"}) }()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err = pool.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='transactionid' AND query LIKE '%FOR UPDATE OF c%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	renameCtx, cancelRename := context.WithTimeout(ctx, 250*time.Millisecond)
	name := "Changed"
	_, err = db.UpdateColumn(renameCtx, pool, columns[0].ID, &name, nil)
	cancelRename()
	if err == nil {
		t.Error("column changed between classification and the receipt snapshot")
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-cutDone; err != nil {
		t.Fatal(err)
	}
	view, err := db.ReadReleaseEvidence(ctx, pool, b.ID, release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.CutReceipt == nil || len(view.CutReceipt.Snapshot) != 1 || view.CutReceipt.Snapshot[0].ColumnName != "Done" {
		t.Fatal("cut snapshot changed under the cut")
	}
}

func TestReleaseCutDeadlineSurvivesLockWait(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	b := mkBoard(t, pool, db.BoardParams{Columns: []string{"Done"}, TerminalColumns: []string{"Done"}})
	columns, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	release, err := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "Cut", Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	title := "Done"
	card, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &columns[0].ID, ReleaseID: &release.ID}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := db.ReadReleaseEvidence(ctx, pool, b.ID, release.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ask string
	_ = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&ask)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, `SELECT id FROM cards WHERE id=$1 FOR UPDATE`, card.Card.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	done := make(chan error, 1)
	go func() {
		done <- db.CloseReleaseChecked(ctx, pool, b.ID, release.ID, "", db.EventMeta{Actor: "human"}, nil, releasecut.Intent{AskID: ask, OwnerID: "owner", Deadline: deadline, CardDigest: view.CutPreview.Digest})
	}()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err = pool.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='transactionid' AND query LIKE '%FOR UPDATE OF c%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	for {
		var expired bool
		if err = pool.QueryRow(waitCtx, `SELECT clock_timestamp() > $1`, deadline).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil {
		t.Fatal("card lock extended the execution deadline")
	}
	view, err = db.ReadReleaseEvidence(ctx, pool, b.ID, release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Release.Status != "planned" || view.CutReceipt != nil {
		t.Fatal("expired cut wrote effects")
	}
}
