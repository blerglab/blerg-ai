package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func assertQueueDepth(t *testing.T, pool *pgxpool.Pool, agentID string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM standing_agent_queue WHERE standing_agent_id = $1 AND state IN ('pending','running')`,
		agentID).Scan(&got); err != nil {
		t.Fatalf("queue depth: %v", err)
	}
	if got != want {
		t.Fatalf("queue depth = %d, want %d", got, want)
	}
}

func boolp(b bool) *bool { return &b }

func TestStandingAgentEnqueueOnCreateAndMove(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil || len(cols) < 2 {
		t.Fatalf("columns: %v (%d)", err, len(cols))
	}
	firstCol, secondCol := cols[0], cols[1]

	agent, err := db.CreateStandingAgent(ctx, pool, b.ID, secondCol.ID, "reviewer-bot", "blerg-runner", "review this card", "per_card")
	if err != nil {
		t.Fatalf("CreateStandingAgent: %v", err)
	}

	// Creating a card directly into the agent's column enqueues.
	res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("card"), Repos: &[]string{"blerg-board"}, ColumnID: &secondCol.ID,
	}, human)
	if err != nil {
		t.Fatalf("CreateCard: %v", err)
	}
	assertQueueDepth(t, pool, agent.ID, 1)

	// Coalescing: moving the still-pending card out and back in must not
	// enqueue a second row for the same (agent, card) pair.
	if _, err := db.MoveCard(ctx, pool, res.Card.ID, firstCol.ID, nil, nil, human); err != nil {
		t.Fatalf("MoveCard away: %v", err)
	}
	if _, err := db.MoveCard(ctx, pool, res.Card.ID, secondCol.ID, nil, nil, human); err != nil {
		t.Fatalf("MoveCard back: %v", err)
	}
	assertQueueDepth(t, pool, agent.ID, 1)

	// A second, distinct card entering the same column gets its own row.
	res2, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("card 2"), Repos: &[]string{"blerg-board"}, ColumnID: &firstCol.ID,
	}, human)
	if err != nil {
		t.Fatalf("CreateCard 2: %v", err)
	}
	if _, err := db.MoveCard(ctx, pool, res2.Card.ID, secondCol.ID, nil, nil, human); err != nil {
		t.Fatalf("MoveCard 2: %v", err)
	}
	assertQueueDepth(t, pool, agent.ID, 2)
}

// Re-ranking a card within the SAME column (drag reorder) is not "entering"
// it and must not enqueue.
func TestStandingAgentSameColumnReorderDoesNotEnqueue(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil || len(cols) < 1 {
		t.Fatalf("columns: %v", err)
	}
	col := cols[0]
	agent, err := db.CreateStandingAgent(ctx, pool, b.ID, col.ID, "watcher", "blerg-runner", "watch", "per_card")
	if err != nil {
		t.Fatalf("CreateStandingAgent: %v", err)
	}
	res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("card"), Repos: &[]string{"blerg-board"}, ColumnID: &col.ID,
	}, human)
	if err != nil {
		t.Fatalf("CreateCard: %v", err)
	}
	assertQueueDepth(t, pool, agent.ID, 1)
	// Drain the initial enqueue so the coalescing index can't mask the bug.
	if _, err := pool.Exec(ctx, `UPDATE standing_agent_queue SET state = 'done' WHERE standing_agent_id = $1`, agent.ID); err != nil {
		t.Fatalf("settle: %v", err)
	}
	// Re-rank within the same column.
	if _, err := db.MoveCard(ctx, pool, res.Card.ID, col.ID, nil, nil, human); err != nil {
		t.Fatalf("MoveCard (reorder): %v", err)
	}
	assertQueueDepth(t, pool, agent.ID, 0)
}

func TestStandingAgentReentrancyGuard(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil || len(cols) < 2 {
		t.Fatalf("columns: %v", err)
	}
	firstCol, secondCol := cols[0], cols[1]

	agent, err := db.CreateStandingAgent(ctx, pool, b.ID, secondCol.ID, "self-mover", "blerg-runner", "iterate", "per_card")
	if err != nil {
		t.Fatalf("CreateStandingAgent: %v", err)
	}

	// A token sharing the agent's own lineage — as if a session it spawned
	// is the one making the move.
	ownTok, _, err := db.MintTokenForLineage(ctx, pool, &b.ID, agent.TokenLineageID, "agent", "self", []string{}, time.Hour)
	if err != nil {
		t.Fatalf("MintTokenForLineage: %v", err)
	}
	// An unrelated agent token — a normal worker session, not this agent.
	otherTok, _, err := db.MintToken(ctx, pool, &b.ID, "agent", "other", []string{}, time.Hour)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}

	res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("card"), Repos: &[]string{"blerg-board"}, ColumnID: &firstCol.ID,
	}, human)
	if err != nil {
		t.Fatalf("CreateCard: %v", err)
	}

	// The agent's own lineage moves the card into its own column: skipped.
	selfMove := db.EventMeta{Actor: "agent", ActorTokenID: &ownTok.ID}
	if _, err := db.MoveCard(ctx, pool, res.Card.ID, secondCol.ID, nil, nil, selfMove); err != nil {
		t.Fatalf("MoveCard (self): %v", err)
	}
	assertQueueDepth(t, pool, agent.ID, 0)

	// Move it out and have a DIFFERENT agent move it back in: enqueues.
	if _, err := db.MoveCard(ctx, pool, res.Card.ID, firstCol.ID, nil, nil, human); err != nil {
		t.Fatalf("MoveCard out: %v", err)
	}
	otherMove := db.EventMeta{Actor: "agent", ActorTokenID: &otherTok.ID}
	if _, err := db.MoveCard(ctx, pool, res.Card.ID, secondCol.ID, nil, nil, otherMove); err != nil {
		t.Fatalf("MoveCard (other): %v", err)
	}
	assertQueueDepth(t, pool, agent.ID, 1)
}

func TestStandingAgentDisabledDoesNotEnqueue(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil || len(cols) < 1 {
		t.Fatalf("columns: %v", err)
	}
	col := cols[0]
	agent, err := db.CreateStandingAgent(ctx, pool, b.ID, col.ID, "off", "blerg-runner", "do nothing", "per_card")
	if err != nil {
		t.Fatalf("CreateStandingAgent: %v", err)
	}
	if _, err := db.UpdateStandingAgent(ctx, pool, agent.ID, db.StandingAgentParams{Enabled: boolp(false)}); err != nil {
		t.Fatalf("UpdateStandingAgent: %v", err)
	}
	if _, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("card"), Repos: &[]string{"blerg-board"}, ColumnID: &col.ID,
	}, human); err != nil {
		t.Fatalf("CreateCard: %v", err)
	}
	assertQueueDepth(t, pool, agent.ID, 0)
}

func TestStandingQueueClaimBackoffAndReap(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil || len(cols) < 1 {
		t.Fatalf("columns: %v", err)
	}
	col := cols[0]
	agent, err := db.CreateStandingAgent(ctx, pool, b.ID, col.ID, "worker", "blerg-runner", "go", "per_card")
	if err != nil {
		t.Fatalf("CreateStandingAgent: %v", err)
	}
	if _, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("card"), Repos: &[]string{"blerg-board"}, ColumnID: &col.ID,
	}, human); err != nil {
		t.Fatalf("CreateCard: %v", err)
	}

	item, ok, err := db.ClaimStandingQueueItem(ctx, pool)
	if err != nil || !ok {
		t.Fatalf("ClaimStandingQueueItem: ok=%v err=%v", ok, err)
	}
	if item.StandingAgentID != agent.ID {
		t.Fatalf("claimed wrong agent: %s", item.StandingAgentID)
	}
	// A second claim finds nothing live — the row is 'running'.
	if _, ok, err := db.ClaimStandingQueueItem(ctx, pool); err != nil || ok {
		t.Fatalf("expected no claimable item while running, got ok=%v err=%v", ok, err)
	}

	// A failed attempt backs off rather than becoming immediately claimable.
	permanent, err := db.FailStandingQueueItem(ctx, pool, item.ID, item.Attempts, "boom")
	if err != nil || permanent {
		t.Fatalf("FailStandingQueueItem: permanent=%v err=%v", permanent, err)
	}
	if _, ok, err := db.ClaimStandingQueueItem(ctx, pool); err != nil || ok {
		t.Fatalf("expected the backed-off item to stay unclaimable, got ok=%v err=%v", ok, err)
	}

	// Force it due and reclaim it, then exhaust retries to 'failed'.
	if _, err := pool.Exec(ctx, `UPDATE standing_agent_queue SET next_attempt_at = now() WHERE id = $1`, item.ID); err != nil {
		t.Fatalf("force due: %v", err)
	}
	item2, ok, err := db.ClaimStandingQueueItem(ctx, pool)
	if err != nil || !ok || item2.ID != item.ID {
		t.Fatalf("reclaim after backoff: ok=%v err=%v item2=%+v", ok, err, item2)
	}
	for i := 0; i < 10; i++ {
		permanent, err = db.FailStandingQueueItem(ctx, pool, item2.ID, item2.Attempts+i, "still broken")
		if err != nil {
			t.Fatalf("FailStandingQueueItem retry %d: %v", i, err)
		}
		if permanent {
			break
		}
	}
	if !permanent {
		t.Fatal("expected the item to settle at 'failed' after enough attempts")
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM standing_agent_queue WHERE id = $1`, item.ID).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != "failed" {
		t.Fatalf("state = %q, want failed", state)
	}

	// Restart durability: a 'running' item whose lease has expired is
	// reclaimable again after ReapStaleStandingQueue.
	if _, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("card 2"), Repos: &[]string{"blerg-board"}, ColumnID: &col.ID,
	}, human); err != nil {
		t.Fatalf("CreateCard 2: %v", err)
	}
	item3, ok, err := db.ClaimStandingQueueItem(ctx, pool)
	if err != nil || !ok {
		t.Fatalf("claim item3: ok=%v err=%v", ok, err)
	}
	// Simulate a crashed worker: the lease is in the past.
	if _, err := pool.Exec(ctx,
		`UPDATE standing_agent_queue SET next_attempt_at = now() - interval '1 minute' WHERE id = $1`, item3.ID); err != nil {
		t.Fatalf("simulate expired lease: %v", err)
	}
	if err := db.ReapStaleStandingQueue(ctx, pool); err != nil {
		t.Fatalf("ReapStaleStandingQueue: %v", err)
	}
	item4, ok, err := db.ClaimStandingQueueItem(ctx, pool)
	if err != nil || !ok || item4.ID != item3.ID {
		t.Fatalf("expected the reaped item reclaimable: ok=%v err=%v item4=%+v", ok, err, item4)
	}
}
