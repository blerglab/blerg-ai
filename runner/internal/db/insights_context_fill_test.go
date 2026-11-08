package db_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestListContextFillComputesPercentAndFlagsCompaction(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	sid := "00000000-0000-4000-8000-000000000720"
	endTestSession(t, p, sid, "idle")
	for i, event := range []struct{ kind, payload string }{
		{"turn_done", `{"model":"m","context":{"tokens":50000,"window":200000}}`},
		{"turn_done", `{"model":"m","context":{"tokens":40000,"window":200000,"compacted":true,"compacted_from":190000}}`},
	} {
		if _, _, err := db.AppendAgentEvent(ctx, p, sid, fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), event.kind, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	rows, truncated, err := db.ListContextFill(ctx, p, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatalf("two readings should not report truncated")
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 readings, got %+v", rows)
	}
	if rows[0].SessionID != sid || rows[0].Percent != 25 || rows[0].Compacted {
		t.Fatalf("first reading: %+v", rows[0])
	}
	if rows[1].Percent != 20 || !rows[1].Compacted {
		t.Fatalf("second reading (compacted): %+v", rows[1])
	}
}

func TestListContextFillSkipsTurnsWithoutUsableContext(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	sid := "00000000-0000-4000-8000-000000000721"
	endTestSession(t, p, sid, "idle")
	for i, event := range []struct{ kind, payload string }{
		{"turn_done", `{"model":"m","usage":{"input_tokens":1}}`},                     // no context at all
		{"turn_done", `{"model":"m","context":{"tokens":0,"window":0}}`},              // zero window
		{"turn_done", `{"model":"m","context":{"tokens":-1,"window":200000}}`},        // negative tokens
		{"turn_done", `{"model":"m","context":{"tokens":"many","window":200000}}`},    // non-numeric tokens
		{"subagent_done", `{"model":"m","context":{"tokens":50000,"window":200000}}`}, // wrong kind
		{"turn_done", `{"model":"m","context":{"tokens":50000,"window":200000}}`},     // the one usable reading
	} {
		if _, _, err := db.AppendAgentEvent(ctx, p, sid, fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), event.kind, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, err := db.ListContextFill(ctx, p, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Percent != 25 {
		t.Fatalf("malformed/absent context readings should be skipped: %+v", rows)
	}
}

func TestListContextFillFiltersByOwnerAndRange(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	sidMine := "00000000-0000-4000-8000-000000000722"
	sidOther := "00000000-0000-4000-8000-000000000723"
	endTestSession(t, p, sidMine, "idle")
	endTestSession(t, p, sidOther, "idle")
	if _, err := p.Exec(ctx, `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, sidMine, "owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, sidOther, "owner-b"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, _, err := db.AppendAgentEventAt(ctx, p, sidMine, "00000000-0000-4000-8000-000000000731", "turn_done", `{"context":{"tokens":10000,"window":100000}}`, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEventAt(ctx, p, sidOther, "00000000-0000-4000-8000-000000000732", "turn_done", `{"context":{"tokens":20000,"window":100000}}`, now); err != nil {
		t.Fatal(err)
	}
	// Outside the requested range.
	if _, _, err := db.AppendAgentEventAt(ctx, p, sidMine, "00000000-0000-4000-8000-000000000733", "turn_done", `{"context":{"tokens":99000,"window":100000}}`, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, _, err := db.ListContextFill(ctx, p, now.Add(-time.Hour), now.Add(time.Hour), "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SessionID != sidMine || rows[0].Percent != 10 {
		t.Fatalf("owner/range filter: %+v", rows)
	}
}

// A workspace with many conversations should not have one long-lived conversation crowd out the
// rest (BLG-464 round 2): the cap applies per conversation, so a quiet conversation keeps every
// reading even while a busy one is capped.
func TestListContextFillCapsPerConversationNotTotal(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	sidSmall := "00000000-0000-4000-8000-000000000724"
	sidBig := "00000000-0000-4000-8000-000000000725"
	endTestSession(t, p, sidSmall, "idle")
	endTestSession(t, p, sidBig, "idle")
	now := time.Now().UTC()
	for i, at := 0, now; i < 3; i, at = i+1, at.Add(time.Minute) {
		if _, _, err := db.AppendAgentEventAt(ctx, p, sidSmall, fmt.Sprintf("00000000-0000-4000-8000-%012d", 740+i), "turn_done",
			fmt.Sprintf(`{"context":{"tokens":%d,"window":100000}}`, 10000*(i+1)), at); err != nil {
			t.Fatal(err)
		}
	}
	// One statement seeds a conversation past the per-conversation cap without 501 round trips.
	if _, err := p.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts)
		SELECT $1::uuid,n,gen_random_uuid(),'turn_done',jsonb_build_object('context',jsonb_build_object('tokens',n,'window',1000)),$2::timestamptz+n*interval '1 second'
		  FROM generate_series(1,501) n`, sidBig, now); err != nil {
		t.Fatal(err)
	}
	rows, truncated, err := db.ListContextFill(ctx, p, now.Add(-time.Minute), now.Add(time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("the busy conversation exceeding the cap should report truncated")
	}
	var small, big int
	for _, r := range rows {
		switch r.SessionID {
		case sidSmall:
			small++
		case sidBig:
			big++
		}
	}
	if small != 3 {
		t.Fatalf("the quiet conversation should keep every reading, got %d", small)
	}
	if big != 500 {
		t.Fatalf("the busy conversation should cap at 500, got %d", big)
	}
}
