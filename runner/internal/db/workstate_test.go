package db_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/checklist"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
)

func TestLongWorkState(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Seed the long recorded history in bulk. Exercise the real writer at the end,
	// while retaining all 6,500 events and the checkpoint/page replay assertions.
	p, _ := checklist.Apply(nil, checklist.Request{Op: "set", Items: []string{"Build", strings.Repeat("x", 200)}})
	events := make([][]any, 0, 6499)
	checkpoints := [][]any{}
	old := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 4999; i++ {
		before := p.Items
		if i > 0 {
			op := "tick"
			if i%2 == 0 {
				op = "untick"
			}
			p, err = checklist.Apply(before, checklist.Request{Op: op, Index: 1})
			if err != nil {
				t.Fatal(err)
			}
		}
		raw, e := json.Marshal(checklist.Compact(before, p))
		if e != nil {
			t.Fatal(e)
		}
		events = append(events, []any{sid, int64(i + 1), fmt.Sprintf("00000000-0000-4000-8000-%012d", i), "checklist", string(raw), old})
		if i%100 == 0 {
			full, e := json.Marshal(p)
			if e != nil {
				t.Fatal(e)
			}
			checkpoints = append(checkpoints, []any{sid, int64(i + 1), string(full)})
		}
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"agent_events"}, []string{"session_id", "seq", "client_event_id", "kind", "payload", "ts"}, pgx.CopyFromRows(events)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"checklist_checkpoints"}, []string{"session_id", "seq", "payload"}, pgx.CopyFromRows(checkpoints)); err != nil {
		t.Fatal(err)
	}
	p, err = checklist.Apply(p.Items, checklist.Request{Op: "tick", Index: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, err = db.AppendAgentEventTx(ctx, tx, sid, "00000000-0000-4000-8000-000000004999", "checklist", string(raw), old); err != nil {
		t.Fatal(err)
	}
	events = nil
	for i := 0; i < 1000; i++ {
		events = append(events, []any{sid, int64(5001 + i), fmt.Sprintf("00000000-0001-4000-8000-%012d", i), "topic", `{"id":"topic-a","action":"start","title":"Work"}`, old})
	}
	for i := 0; i < 499; i++ {
		events = append(events, []any{sid, int64(6001 + i), fmt.Sprintf("00000000-0002-4000-8000-%012d", i), "ticket", `{"action":"pin","ticket":{"source":"test","key":"A","title":"Work"}}`, old})
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"agent_events"}, []string{"session_id", "seq", "client_event_id", "kind", "payload", "ts"}, pgx.CopyFromRows(events)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.AppendAgentEventTx(ctx, tx, sid, "00000000-0002-4000-8000-000000000499", "ticket", `{"action":"pin","ticket":{"source":"test","key":"A","title":"Work"}}`, old); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	latest, err := db.LatestChecklist(ctx, pool, sid, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := checklist.Read([]byte(latest.Payload))
	if !ok || !got.Items[0].Done || len(got.Items) != 2 {
		t.Fatalf("live %+v", got)
	}
	historical, err := db.LatestChecklist(ctx, pool, sid, 4999)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = checklist.Read([]byte(historical.Payload))
	if got.Items[0].Done {
		t.Fatal("historical checkpoint replay")
	}
	page, err := db.ListAgentEvents(ctx, pool, sid, 4980, 20)
	if err != nil || len(page) != 20 {
		t.Fatalf("page %d %v", len(page), err)
	}
	state, ok := checklist.Read([]byte(page[0].Payload))
	if !ok || state.Delta || len(state.Items) != 2 {
		t.Fatalf("missing page baseline %+v", state)
	}
	for _, e := range page[1:] {
		change, _ := checklist.Read([]byte(e.Payload))
		state = checklist.Replay(state.Items, change)
	}
	if checklist.Done(state.Items) != 1 {
		t.Fatalf("page replay %+v", state)
	}
	var stored string
	if err = pool.QueryRow(ctx, `SELECT payload::text FROM agent_events WHERE session_id=$1 AND seq=5000`, sid).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, strings.Repeat("x", 200)) {
		t.Fatal("stored full list")
	}
	for _, kind := range []string{"checklist", "topic", "ticket"} {
		n, e := db.CountRecentWork(ctx, pool, sid, kind)
		if e != nil || n != 0 {
			t.Fatalf("rate %s: %d %v", kind, n, e)
		}
	}
	pins, err := db.ListTicketRows(ctx, pool, sid, 1<<62)
	if err != nil || len(pins) != 1 {
		t.Fatalf("pins %v %v", pins, err)
	}
}
