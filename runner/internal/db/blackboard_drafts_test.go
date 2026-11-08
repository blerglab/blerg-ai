package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestBlackboardDraftRoundTrip(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	workspace := "ws-" + blackboard.NewID()

	if got, err := db.GetBlackboardDraft(ctx, pool, workspace); err != nil || got != nil {
		t.Fatalf("no draft yet: %+v %v", got, err)
	}
	if err := db.SaveBlackboardDraft(ctx, pool, workspace, "agent-1", []byte(`{"vision":"v1"}`)); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetBlackboardDraft(ctx, pool, workspace)
	if err != nil || got == nil || got.CreatedBy != "agent-1" || !strings.Contains(string(got.Draft), `"v1"`) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	id := got.ID
	if id == "" || got.Revision != 0 {
		t.Fatalf("initial identity: %+v", got)
	}
	// Regenerating replaces wholesale, not merges.
	if err := db.SaveBlackboardDraft(ctx, pool, workspace, "agent-2", []byte(`{"vision":"v2"}`)); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetBlackboardDraft(ctx, pool, workspace)
	if err != nil || got == nil || got.CreatedBy != "agent-2" || strings.Contains(string(got.Draft), `"v1"`) {
		t.Fatalf("replace: %+v %v", got, err)
	}
	if got.ID != id || got.Revision != 1 {
		t.Fatalf("new revision: %+v", got)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM blackboard_draft_revisions WHERE id=$1`, id).Scan(&count); err != nil || count != 2 {
		t.Fatalf("revision history: %d %v", count, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE blackboard_draft_revisions SET draft='{}' WHERE id=$1`, id); err == nil {
		t.Fatal("history was mutable")
	}
	if err := db.DeleteBlackboardDraft(ctx, pool, workspace); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetBlackboardDraft(ctx, pool, workspace); err != nil || got != nil {
		t.Fatalf("discarded: %+v %v", got, err)
	}
	// Discarding twice is not an error.
	if err := db.DeleteBlackboardDraft(ctx, pool, workspace); err != nil {
		t.Fatal(err)
	}
}

func TestBlackboardDraftOversizedRejected(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	huge := `{"vision":"` + strings.Repeat("x", db.MaxBlackboardDraftBytes) + `"}`
	if err := db.SaveBlackboardDraft(ctx, pool, "ws-"+blackboard.NewID(), "agent", []byte(huge)); err == nil {
		t.Fatal("expected the oversized draft to be refused")
	}
}
