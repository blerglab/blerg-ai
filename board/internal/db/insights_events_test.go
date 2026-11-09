package db_test

import (
	"github.com/blerglab/blerg-ai/board/internal/db"
	"strings"
	"testing"
	"time"
)

func TestInsightCardMetadataIsBoundedByBoardAndNumbers(t *testing.T) {
	pool := testPool(t)
	a := mkBoard(t, pool, db.BoardParams{})
	b := mkBoard(t, pool, db.BoardParams{})
	if _, err := pool.Exec(t.Context(), `INSERT INTO cards(board_id,number,title,body,type,priority,rank) VALUES($1,1,'public','canary-private-body','task','medium','a'),($2,1,'foreign','canary-foreign','task','medium','a'),($1,2,'not requested','canary-unrequested','task','medium','b')`, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	raw, err := db.InsightCardMetadata(t.Context(), pool, a.ID, []int{1})
	if err != nil || strings.Contains(string(raw), "canary") || strings.Contains(string(raw), b.ID) || strings.Contains(string(raw), `"number": 2`) {
		t.Fatalf("unsafe metadata: %s %v", raw, err)
	}
	if !strings.Contains(string(raw), `"number": 1`) {
		t.Fatal("requested metadata missing")
	}
}

func TestBoardTimeEventsUseRecordedDatesAndBoard(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	a := mkBoard(t, pool, db.BoardParams{})
	b := mkBoard(t, pool, db.BoardParams{})
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO deployments(board_id,env,status,detail,created_at) VALUES($1,'production','ok','private detail',$3),($2,'other','ok','private detail',$3),($1,'old','failed','private detail',$3::timestamptz-interval '8 days')`, a.ID, b.ID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	release, err := db.SaveRelease(ctx, pool, a.ID, "", db.ReleaseParams{Name: "0.6.0", Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE board_releases SET status='released',released_at=$2 WHERE id=$1`, release.ID, now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	events, err := db.ListBoardTimeEvents(ctx, pool, a.ID, now.Add(-7*24*time.Hour), now)
	if err != nil || len(events) != 2 || events[0].Label != "Deployed production" || events[1].Label != "Release 0.6.0" {
		t.Fatalf("events: %+v %v", events, err)
	}
}
