package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// spawnedOn reports whether a worker session exists for the card. Read from
// runner_sessions rather than the fake's request log because the session row is
// what "a worker is editing this repo" actually means.
func spawnedOn(t *testing.T, pool *pgxpool.Pool, cardID string) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM runner_sessions WHERE card_id = $1 AND role = 'worker'`,
		cardID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// tickUntilQuiet runs dispatcher passes until one changes nothing, so the
// assertions see a drained board rather than a half-drained one. Dispatch is
// serialized per board, so a pass may find the async dispatcher handleStartRun
// launched still holding the lock and correctly do nothing; ticking again is
// the next tick.
func tickUntilQuiet(t *testing.T, pool *pgxpool.Pool, boardID string, want int) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	for {
		srvAPI.RunTick(ctx)
		var claimed int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM cards WHERE board_id = $1 AND run_attempts > 0`, boardID).
			Scan(&claimed); err != nil {
			t.Fatal(err)
		}
		if claimed >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d cards dispatched after 15s", claimed, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestConcurrentDispatchNeverSpawnsACardAndItsBlocker is the reason this whole
// predicate exists. At concurrency 1 the hazard hid behind manual ranking: the
// dispatcher took one card per pass in rank order, so a human who happened to
// rank the blocker first got correct behaviour by luck. Raise concurrency and
// a single pass pulls several cards at once — here A (which depends on B) is
// ranked ABOVE B, so a rank-only picker takes A first and B second, and two
// workers edit the same repo toward the same feature with one of them building
// on a base that does not exist yet.
//
// The pass must spawn B and C and leave A alone; A becomes dispatchable only
// once B reaches a terminal column.
func TestConcurrentDispatchNeverSpawnsACardAndItsBlocker(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	apiSrv := srvAPI
	apiSrv.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	readyID := columnNamed(t, cols, "ready")

	mk := func(title string) db.Card {
		t.Helper()
		created, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
			Title: strPtr(title), Repos: &[]string{"blerg-board"}, ColumnID: &readyID,
		}, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		return created.Card
	}
	// rank order: A, B, C — the dependent deliberately outranks its blocker
	a, b, c := mk("A dependent"), mk("B blocker"), mk("C independent")
	if err := db.AddDependency(ctx, pool, a.ID, b.ID, db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}

	resp := request(t, srv, "POST", "/api/boards/"+board.ID+"/run", cookie, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start run: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	// The dial lives on the board and is read live on every tick (migration
	// 015), so turning it here applies to the run already going.
	setConcurrency(t, &serverT{srv: srv, cookie: cookie}, board.ID, 3)

	tickUntilQuiet(t, pool, board.ID, 2)

	if spawnedOn(t, pool, a.ID) {
		t.Fatal("dispatcher spawned a worker on A while its blocker B was still pending")
	}
	if !spawnedOn(t, pool, b.ID) {
		t.Fatal("dispatcher never took B, the blocker that was ranked below its dependent")
	}
	// C proves the blocked card was skipped over rather than stalled on: A is
	// ranked first, so a picker that stopped at A would never have reached C.
	if !spawnedOn(t, pool, c.ID) {
		t.Fatal("dispatcher stalled on the blocked card instead of skipping past it to C")
	}
	if n := fake.startCount(); n != 2 {
		t.Fatalf("expected exactly 2 spawns (B and C), got %d", n)
	}

	// The run status must name the blocked card, or a board waiting on its own
	// dependency graph looks inexplicably idle.
	resp = request(t, srv, "GET", "/api/boards/"+board.ID+"/run", cookie, nil, nil)
	var status struct {
		Ready   int `json:"ready"`
		Blocked int `json:"blocked"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if status.Blocked != 1 || status.Ready != 1 {
		t.Fatalf("run status ready=%d blocked=%d, want 1 and 1", status.Ready, status.Blocked)
	}

	// B lands in done: A is released on the next pass.
	doneID := columnNamed(t, cols, "done")
	if _, err := db.MoveCard(ctx, pool, b.ID, doneID, nil, nil, db.EventMeta{Actor: "service"}); err != nil {
		t.Fatal(err)
	}
	tickUntilDispatched(t, pool, a.ID)
	if !spawnedOn(t, pool, a.ID) {
		t.Fatal("A was never dispatched after its blocker reached a terminal column")
	}
}
