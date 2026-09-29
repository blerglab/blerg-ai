package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// tickUntilDispatched runs dispatcher passes until the card has been claimed,
// returning as soon as its run_attempts moves off zero — the last write a
// dispatch step makes, so by then the spawn and its session row have landed
// too. Callers assert the exact spawn count afterwards; an over-claim is still
// a failure.
//
// The loop is not a retry-until-green: dispatch is serialized per board, so a
// pass that finds another dispatcher already draining the board — the async
// one handleStartRun launches, which a test cannot join — correctly does
// nothing, exactly as in production where the next tick picks the work up.
// Ticking again is that next tick.
func tickUntilDispatched(t *testing.T, pool *pgxpool.Pool, cardID string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	for {
		srvAPI.RunTick(ctx)
		var attempts int
		if err := pool.QueryRow(ctx, `SELECT run_attempts FROM cards WHERE id = $1`, cardID).
			Scan(&attempts); err != nil {
			t.Fatal(err)
		}
		if attempts > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("card not dispatched after 15s: run_attempts still 0")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestConcurrentTicksSpawnOneWorkerPerCard: dispatcher passes overlap in
// production — pressing Run fires a tick of its own while the 20s ticker may
// already be mid-pass — and without mutual exclusion two passes read the same
// top-of-ready card, both move it to the work column (the second move is a
// no-op that still succeeds) and both spawn a worker: two sessions editing one
// repo. Dispatch is serialized per board, so however many passes overlap, a
// ready card is claimed exactly once.
func TestConcurrentTicksSpawnOneWorkerPerCard(t *testing.T) {
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

	created, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("only card"), Repos: &[]string{"blerg-board"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MoveCard(ctx, pool, created.Card.ID, readyID, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}

	resp := request(t, srv, "POST", "/api/boards/"+board.ID+"/run", cookie, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start run: %d", resp.StatusCode)
	}

	// Eight passes released together, on top of the one handleStartRun
	// launched — the gate widens the overlap so the race, if it is still
	// there, shows up rather than hiding behind goroutine start latency.
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			apiSrv.RunTick(ctx)
		}()
	}
	close(gate)
	wg.Wait()

	// One more pass, waiting for the claim to land: if every gated pass lost
	// the lock to the async one, the claim is still in flight.
	tickUntilDispatched(t, pool, created.Card.ID)

	if n := fake.startCount(); n != 1 {
		t.Fatalf("concurrent dispatch passes spawned %d workers for one ready card, want 1", n)
	}
	var sessions int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM runner_sessions WHERE card_id = $1 AND role = 'worker'`,
		created.Card.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Fatalf("card has %d worker sessions, want 1", sessions)
	}
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT run_attempts FROM cards WHERE id = $1`,
		created.Card.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("card run_attempts = %d, want 1", attempts)
	}
}

// TestDispatchYieldsToAnotherProcessHoldingTheBoard: the in-process half of
// the guard (one pass at a time per server) says nothing about a second
// replica, so the claim itself is held in Postgres. Standing in for that other
// replica: an independent connection holding the board's dispatch lock. While
// it is held this server must claim nothing, and once it is released the next
// pass picks the card up — a skipped pass defers work, it never drops it.
//
// The lock key is spelled out here on purpose: it is the contract two
// processes agree on, so a change to it should break this test.
func TestDispatchYieldsToAnotherProcessHoldingTheBoard(t *testing.T) {
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

	created, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("contested card"), Repos: &[]string{"blerg-board"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MoveCard(ctx, pool, created.Card.ID, readyID, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}

	// Take the board first, so even the tick handleStartRun launches sees it
	// held — no window where this server could have claimed the card.
	other, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := "blerg_board_dispatch:" + board.ID
	// However this test ends, hand the lock and the connection back: a
	// t.Fatal partway through would otherwise return a still-locked
	// connection to the pool (poisoning later tests) and wedge pool.Close.
	unlocked := false
	t.Cleanup(func() {
		if !unlocked {
			_, _ = other.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, key)
		}
		other.Release()
	})
	var held bool
	if err := other.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, key).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("could not take the board's dispatch lock")
	}

	resp := request(t, srv, "POST", "/api/boards/"+board.ID+"/run", cookie, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start run: %d", resp.StatusCode)
	}
	// Several passes, spaced: one pass runs per process at a time, so a bare
	// RunTick can return without having dispatched simply because the tick
	// handleStartRun launched was still in flight — which would make this
	// assertion pass for the wrong reason.
	for range 5 {
		apiSrv.RunTick(ctx)
		time.Sleep(25 * time.Millisecond)
	}
	if n := fake.startCount(); n != 0 {
		t.Fatalf("dispatched %d workers while another process held the board, want 0", n)
	}
	var col string
	if err := pool.QueryRow(ctx, `SELECT column_id FROM cards WHERE id = $1`, created.Card.ID).
		Scan(&col); err != nil {
		t.Fatal(err)
	}
	if col != readyID {
		t.Fatal("card left the ready column while another process held the board")
	}

	// The other process finishes; the work is still there for the next pass.
	if _, err := other.Exec(ctx, `SELECT pg_advisory_unlock(hashtext($1))`, key); err != nil {
		t.Fatal(err)
	}
	unlocked = true
	other.Release()

	tickUntilDispatched(t, pool, created.Card.ID)
	if n := fake.startCount(); n != 1 {
		t.Fatalf("spawned %d workers after the board was released, want 1", n)
	}
}

// TestRunStaysActiveWhenReadyDrains: a board run is a persistent toggle, not
// a one-shot job. Once ready empties and nothing is in flight, the run must
// stay 'running' (idle, waiting for more work) rather than flipping itself
// to 'done' — a card dropped into ready afterward is picked up on the next
// dispatcher tick with no click required. Only an explicit stop ends it.
func TestRunStaysActiveWhenReadyDrains(t *testing.T) {
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

	created, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("first card"), Repos: &[]string{"blerg-board"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MoveCard(ctx, pool, created.Card.ID, readyID, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}

	// Check the box: start the run.
	resp := request(t, srv, "POST", "/api/boards/"+board.ID+"/run", cookie, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start run: %d", resp.StatusCode)
	}

	// Dispatch: the one ready card is claimed and a worker spawned.
	tickUntilDispatched(t, pool, created.Card.ID)
	if n := fake.startCount(); n != 1 {
		t.Fatalf("expected 1 spawn after first tick, got %d", n)
	}

	// The worker's card leaves the work column (simulating it finishing up
	// to done) so ready and in-flight both go to zero.
	card, err := db.GetCard(ctx, pool, created.Card.ID)
	if err != nil {
		t.Fatal(err)
	}
	doneID := columnNamed(t, cols, "done")
	if _, err := db.MoveCard(ctx, pool, card.ID, doneID, nil, nil, db.EventMeta{Actor: "service"}); err != nil {
		t.Fatal(err)
	}

	// A second tick sees ready==0 and in_flight==0 — the run must stay
	// 'running' (idle), not flip itself to 'done'.
	apiSrv.RunTick(ctx)
	var status struct {
		State    string `json:"state"`
		Ready    int    `json:"ready"`
		InFlight int    `json:"in_flight"`
	}
	resp = request(t, srv, "GET", "/api/boards/"+board.ID+"/run", cookie, nil, nil)
	_ = json.NewDecoder(resp.Body).Decode(&status)
	if status.State != "running" {
		t.Fatalf("run state after drain = %q, want %q (idle, still running)", status.State, "running")
	}
	if status.Ready != 0 || status.InFlight != 0 {
		t.Fatalf("run counts after drain = ready:%d in_flight:%d, want 0/0", status.Ready, status.InFlight)
	}

	// New work dropped into ready is picked up automatically — no re-click.
	created2, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("second card"), Repos: &[]string{"blerg-board"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MoveCard(ctx, pool, created2.Card.ID, readyID, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	tickUntilDispatched(t, pool, created2.Card.ID)
	if n := fake.startCount(); n != 2 {
		t.Fatalf("expected second card auto-dispatched while idle-running, got %d spawns", n)
	}

	// Unchecking stops automatic advancement.
	resp = request(t, srv, "POST", "/api/boards/"+board.ID+"/run/stop", cookie, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop run: %d", resp.StatusCode)
	}
	created3, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("third card"), Repos: &[]string{"blerg-board"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MoveCard(ctx, pool, created3.Card.ID, readyID, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	apiSrv.RunTick(ctx)
	if n := fake.startCount(); n != 2 {
		t.Fatalf("expected no dispatch after stop, got %d spawns", n)
	}
}
