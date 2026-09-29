package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// readyCards creates n cards and drops them into the ready column, top-down in
// creation order.
func readyCards(t *testing.T, pool *pgxpool.Pool, boardID, readyID string, titles ...string) []db.Card {
	t.Helper()
	ctx := context.Background()
	var out []db.Card
	for _, title := range titles {
		created, err := db.CreateCard(ctx, pool, boardID, db.CardParams{
			Title: strPtr(title), Repos: &[]string{"blerg-board"}}, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		moved, err := db.MoveCard(ctx, pool, created.Card.ID, readyID, nil, nil, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, moved)
	}
	return out
}

// tickUntilInflight runs dispatcher passes until the work column holds want
// cards, then returns. It never asserts the count itself: a pass that finds
// another dispatcher holding the board correctly does nothing, so the loop is
// how a test stands in for "the next tick". Callers assert the exact numbers
// afterwards, so an OVER-dispatch still fails — this only guards against
// reading the board before dispatch has finished.
func tickUntilInflight(t *testing.T, pool *pgxpool.Pool, workColID string, want int) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	for {
		srvAPI.RunTick(ctx)
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM cards WHERE column_id = $1 AND archived_at IS NULL AND stuck_at IS NULL`,
			workColID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d card(s) in the work column after 15s, want %d", n, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runStatusBody is GET /api/boards/{id}/run, the shape the run chip reads.
//
// Concurrency is a POINTER on purpose. The interesting value is 0, so decoding
// into an int would let "the server stopped sending the field at all" pass as
// "the board is parked" — the parked test would then assert nothing.
type runStatusBody struct {
	State       string `json:"state"`
	Ready       int    `json:"ready"`
	InFlight    int    `json:"in_flight"`
	CardsDone   int    `json:"cards_done"`
	CardsStuck  int    `json:"cards_stuck"`
	Concurrency *int   `json:"concurrency"`
	// the other half of "on but dispatching nothing": the runner has no
	// session slots left (see boardruncapacity_test.go)
	RunnerFull bool `json:"runner_full"`
}

// reportedConcurrency is the run status's concurrency, failing rather than
// defaulting when the key is absent.
func (r runStatusBody) reportedConcurrency(t *testing.T) int {
	t.Helper()
	if r.Concurrency == nil {
		t.Fatal("run status omitted `concurrency` — the run chip needs it to tell a parked board from a hung one")
	}
	return *r.Concurrency
}

func runStatus(t *testing.T, st *serverT, boardID string) runStatusBody {
	t.Helper()
	resp := request(t, st.srv, "GET", "/api/boards/"+boardID+"/run", st.cookie, nil, nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET run: %d", resp.StatusCode)
	}
	var status runStatusBody
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// TestRaisingConcurrencyDispatchesInParallelOnTheLiveRun: the dial's whole
// point. A board raised to 3 pulls three cards out of ready instead of one —
// and it does so on the run that is ALREADY going, because the operator turns
// this to re-prioritise a board now. Stopping and restarting the run to apply
// it would throw away that run's counters, which is exactly what parking is
// meant to avoid.
func TestRaisingConcurrencyDispatchesInParallelOnTheLiveRun(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)
	st := &serverT{srv: srv, cookie: cookie}

	fake := &fakeRunner{lifecycle: "running"}
	apiSrv := srvAPI
	apiSrv.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "busy", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	if board.Concurrency != 1 {
		t.Fatalf("new board concurrency = %d, want the 1 every existing board keeps", board.Concurrency)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	readyID, workID := columnNamed(t, cols, "ready"), columnNamed(t, cols, "in progress")
	cards := readyCards(t, pool, board.ID, readyID, "first", "second", "third", "fourth")

	resp := request(t, srv, "POST", "/api/boards/"+board.ID+"/run", cookie, nil, nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start run: %d", resp.StatusCode)
	}
	// At the default of 1 the run takes exactly one card, however many ticks
	// it gets — this is the behaviour every board has today.
	tickUntilDispatched(t, pool, cards[0].ID)
	apiSrv.RunTick(ctx)
	apiSrv.RunTick(ctx)
	if n := fake.startCount(); n != 1 {
		t.Fatalf("at concurrency 1 the run spawned %d workers, want 1", n)
	}

	// Turn the dial mid-run.
	setConcurrency(t, st, board.ID, 3)

	tickUntilInflight(t, pool, workID, 3)
	apiSrv.RunTick(ctx) // one more: an over-dispatch past 3 must still fail below
	if n := fake.startCount(); n != 3 {
		t.Fatalf("at concurrency 3 the run spawned %d workers, want 3", n)
	}
	var inWork, inReady int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cards WHERE column_id = $1`, workID).Scan(&inWork); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cards WHERE column_id = $1`, readyID).Scan(&inReady); err != nil {
		t.Fatal(err)
	}
	if inWork != 3 || inReady != 1 {
		t.Fatalf("work column has %d cards and ready has %d, want 3 and 1", inWork, inReady)
	}
	if got := runStatus(t, st, board.ID); got.reportedConcurrency(t) != 3 || got.InFlight != 3 {
		t.Fatalf("run status = concurrency %d, in_flight %d; want 3 and 3", got.reportedConcurrency(t), got.InFlight)
	}
}

// TestConcurrencyZeroParksTheBoard: 0 is a wanted state, not a broken one —
// deprioritise a board without stopping its run and losing the run's counters.
// The run stays 'running', in-flight cards stay put, and nothing new is pulled
// out of ready. The status must also SAY concurrency 0, or a parked board is
// indistinguishable from a hung one.
func TestConcurrencyZeroParksTheBoard(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)
	st := &serverT{srv: srv, cookie: cookie}

	fake := &fakeRunner{lifecycle: "running"}
	apiSrv := srvAPI
	apiSrv.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "parked", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	readyID, workID := columnNamed(t, cols, "ready"), columnNamed(t, cols, "in progress")
	cards := readyCards(t, pool, board.ID, readyID, "in flight already", "should not start", "nor this")

	resp := request(t, srv, "POST", "/api/boards/"+board.ID+"/run", cookie, nil, nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start run: %d", resp.StatusCode)
	}
	tickUntilDispatched(t, pool, cards[0].ID)
	if n := fake.startCount(); n != 1 {
		t.Fatalf("expected 1 spawn before parking, got %d", n)
	}

	setConcurrency(t, st, board.ID, 0)

	for range 5 {
		apiSrv.RunTick(ctx)
		time.Sleep(15 * time.Millisecond)
	}
	if n := fake.startCount(); n != 1 {
		t.Fatalf("a parked board dispatched %d workers, want the 1 that was already in flight", n)
	}
	var inWork, inReady int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cards WHERE column_id = $1`, workID).Scan(&inWork); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cards WHERE column_id = $1`, readyID).Scan(&inReady); err != nil {
		t.Fatal(err)
	}
	if inWork != 1 || inReady != 2 {
		t.Fatalf("parked board moved cards: work %d, ready %d; want 1 and 2", inWork, inReady)
	}

	// The run is parked, NOT stopped: it is still active and still reporting
	// the counters a stop would have ended.
	got := runStatus(t, st, board.ID)
	if got.State != "running" {
		t.Fatalf("parked run state = %q, want %q — parking must not end the run", got.State, "running")
	}
	if c := got.reportedConcurrency(t); c != 0 {
		t.Fatalf("parked run reports concurrency %d, want 0 — the UI needs this to tell parked from hung", c)
	}

	// Unparking resumes the same run, no re-press of Run.
	setConcurrency(t, st, board.ID, 2)
	tickUntilInflight(t, pool, workID, 2)
	if n := fake.startCount(); n < 2 {
		t.Fatalf("unparking spawned %d workers in total, want at least 2", n)
	}
}

// TestNegativeConcurrencyRejected: the loop reads any negative the same way it
// reads 0, so a typo would silently park a board instead of erroring. The
// rejection has to name the field, or an agent PATCHing several settings at
// once cannot tell which one it got wrong.
func TestNegativeConcurrencyRejected(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)
	st := &serverT{srv: srv, cookie: cookie}

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "picky", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	setConcurrency(t, st, board.ID, 4)

	resp := patchConcurrency(t, st, board.ID, -1)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("PATCH concurrency -1 = %d, want a 4xx", resp.StatusCode)
	}
	var body struct{ Error, Field string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Field != "concurrency" {
		t.Fatalf("rejection named field %q, want %q (body: %q)", body.Field, "concurrency", body.Error)
	}

	// Rejected means rejected: the earlier value survives.
	reread, err := db.GetBoard(ctx, pool, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Concurrency != 4 {
		t.Fatalf("board concurrency = %d after a rejected PATCH, want the previous 4", reread.Concurrency)
	}

	// Creation is guarded too — otherwise a bad board is one POST away.
	created := request(t, srv, "POST", "/api/boards", cookie, nil,
		map[string]any{"name": "born bad", "concurrency": -2})
	defer func() { _ = created.Body.Close() }()
	if created.StatusCode < 400 || created.StatusCode >= 500 {
		t.Fatalf("POST board with concurrency -2 = %d, want a 4xx", created.StatusCode)
	}
	var cbody struct{ Error, Field string }
	if err := json.NewDecoder(created.Body).Decode(&cbody); err != nil {
		t.Fatal(err)
	}
	if cbody.Field != "concurrency" {
		t.Fatalf("create rejection named field %q, want %q (body: %q)", cbody.Field, "concurrency", cbody.Error)
	}
}

// TestConcurrencyThroughTheMCPTool: blerg_board_update is the surface an AGENT
// administers a board through — the operator's whole priority workflow runs
// over it — and it decodes into the same db.BoardParams by a different path, so
// a wire-format slip here (an "integer" schema a client renders as 3.0, a field
// dropped from the tool's inputSchema) would not show up in any REST test.
func TestConcurrencyThroughTheMCPTool(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "mcp dial", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := db.MintToken(ctx, pool, &board.ID, "agent", "board admin",
		[]string{"card.read", "card.write", "board.admin"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	update := func(args map[string]any) map[string]any {
		t.Helper()
		args["board_id"] = board.ID
		resp := request(t, srv, "POST", "/mcp", raw, nil, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "blerg_board_update", "arguments": args},
		})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("mcp call: HTTP %d", resp.StatusCode)
		}
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		result, _ := out["result"].(map[string]any)
		if result == nil {
			t.Fatalf("mcp response has no result: %v", out)
		}
		return result
	}

	if result := update(map[string]any{"concurrency": 3}); result["isError"] == true {
		t.Fatalf("blerg_board_update concurrency 3 errored: %v", result)
	}
	if got, err := db.GetBoard(ctx, pool, board.ID); err != nil || got.Concurrency != 3 {
		t.Fatalf("after MCP update: concurrency %d (err %v), want 3", got.Concurrency, err)
	}
	// An update that says nothing about concurrency must not reset it.
	if result := update(map[string]any{"description": "unrelated"}); result["isError"] == true {
		t.Fatalf("unrelated MCP update errored: %v", result)
	}
	if got, _ := db.GetBoard(ctx, pool, board.ID); got.Concurrency != 3 {
		t.Fatalf("concurrency = %d after an MCP update that omitted it, want 3", got.Concurrency)
	}
	// And a negative is refused on this path too, naming the field.
	result := update(map[string]any{"concurrency": -3})
	if result["isError"] != true {
		t.Fatalf("blerg_board_update accepted concurrency -3: %v", result)
	}
	if text, _ := json.Marshal(result); !strings.Contains(string(text), "concurrency") {
		t.Fatalf("MCP rejection does not name the field: %s", text)
	}
	if got, _ := db.GetBoard(ctx, pool, board.ID); got.Concurrency != 3 {
		t.Fatalf("concurrency = %d after a rejected MCP update, want the previous 3", got.Concurrency)
	}
}

// TestConcurrencyRoundTripsThroughTheBoardAPI: an agent or UI has to be able
// to read back what it set — GET is the only way to answer "which board is
// prioritised right now?" across eleven boards.
func TestConcurrencyRoundTripsThroughTheBoardAPI(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)
	st := &serverT{srv: srv, cookie: cookie}

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "readable", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	// Existing boards need no migration action: the default is the old behaviour.
	if got := getBoardConcurrency(t, st, board.ID); got != 1 {
		t.Fatalf("GET board concurrency = %d on a fresh board, want 1", got)
	}
	setConcurrency(t, st, board.ID, 5)
	if got := getBoardConcurrency(t, st, board.ID); got != 5 {
		t.Fatalf("GET board concurrency = %d after PATCH 5, want 5", got)
	}
	// A PATCH that doesn't mention concurrency leaves it alone — the field is
	// a pointer, so an omitted key must not read as 0 and park the board.
	resp := request(t, srv, "PATCH", "/api/boards/"+board.ID, cookie, nil,
		map[string]any{"description": "still prioritised"})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH description: %d", resp.StatusCode)
	}
	if got := getBoardConcurrency(t, st, board.ID); got != 5 {
		t.Fatalf("concurrency = %d after an unrelated PATCH, want 5 — an omitted field must not park a board", got)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// serverT bundles the two things every request in this file needs.
type serverT struct {
	srv    *httptest.Server
	cookie string
}

// patchConcurrency PATCHes the board's concurrency and hands back the response
// with its body unread, so a caller can assert on a rejection's `field` and not
// just its status. Callers close it.
func patchConcurrency(t *testing.T, st *serverT, boardID string, n int) *http.Response {
	t.Helper()
	return request(t, st.srv, "PATCH", "/api/boards/"+boardID, st.cookie, nil,
		map[string]any{"concurrency": n})
}

// setConcurrency is the success path: PATCH and require it to have landed.
func setConcurrency(t *testing.T, st *serverT, boardID string, n int) {
	t.Helper()
	resp := patchConcurrency(t, st, boardID, n)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH concurrency %d = %d, want 200", n, resp.StatusCode)
	}
}

func getBoardConcurrency(t *testing.T, st *serverT, boardID string) int {
	t.Helper()
	resp := request(t, st.srv, "GET", "/api/boards/"+boardID, st.cookie, nil, nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET board: %d", resp.StatusCode)
	}
	var b struct {
		Concurrency int `json:"concurrency"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		t.Fatal(err)
	}
	return b.Concurrency
}
