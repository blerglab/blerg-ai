package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A full runner is a fact about the runner, not about the card at the top of
// ready. Dispatch used to find that out the expensive way — claim the card,
// ask the runner, move it back when the runner said no — which cost two
// `moved` events per tick, every 20s, for as long as the runner stayed full
// (a live card collected 42 of them in 8 minutes). These tests pin the two
// halves of the fix: a refused tick must not touch the card at all, and the
// refusal must be remembered so the following ticks don't ask again.

// capacityFixture: a board with one ready card and an active run, ticked by
// hand. The run row goes in directly rather than through POST /run, which
// fires a dispatch pass of its own that a test cannot join — spawn counts
// here are exact.
func capacityFixture(t *testing.T, pool *pgxpool.Pool, fake *fakeRunner) (boardID, cardID, readyID, workID string) {
	t.Helper()
	ctx := context.Background()
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "capped", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	readyID, workID = columnNamed(t, cols, "ready"), columnNamed(t, cols, "in progress")

	created, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("a card the runner has no room for"), Repos: &[]string{"blerg-board"}},
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MoveCard(ctx, pool, created.Card.ID, readyID, nil, nil,
		db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO board_runs (board_id) VALUES ($1)`, board.ID); err != nil {
		t.Fatal(err)
	}
	return board.ID, created.Card.ID, readyID, workID
}

// capacityStatus: GET /api/boards/{id}/run for a plain server+cookie pair.
// The decoding lives in runStatus (boardconcurrency_test.go) so the run
// chip's payload has ONE shape across these tests — the chip reads capacity
// and the concurrency dial off the same response.
func capacityStatus(t *testing.T, srv *httptest.Server, cookie string, boardID string) runStatusBody {
	t.Helper()
	return runStatus(t, &serverT{srv: srv, cookie: cookie}, boardID)
}

func movedEvents(t *testing.T, pool *pgxpool.Pool, cardID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM card_events WHERE card_id = $1 AND type = 'moved'`, cardID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// cardState: the three things a refused tick must leave alone.
func cardState(t *testing.T, pool *pgxpool.Pool, cardID string) (colID string, attempts int, stuck bool) {
	t.Helper()
	var stuckAt *string
	if err := pool.QueryRow(context.Background(),
		`SELECT column_id, run_attempts, stuck_at::text FROM cards WHERE id = $1`, cardID).
		Scan(&colID, &attempts, &stuckAt); err != nil {
		t.Fatal(err)
	}
	return colID, attempts, stuckAt != nil
}

// TestFullRunnerLeavesTheReadyCardAlone: the acceptance case. Ten ticks
// against a runner at its cap, and the card's history gains nothing — it was
// never claimed, so there is nothing to un-claim.
func TestFullRunnerLeavesTheReadyCardAlone(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)
	fake := &fakeRunner{lifecycle: "running", startErr: errors.New("cluster session cap reached (12)")}
	boardID, cardID, readyID, _ := capacityFixture(t, pool, fake)

	before := movedEvents(t, pool, cardID)
	for range 10 {
		srvAPI.RunTick(ctx)
	}

	if got := movedEvents(t, pool, cardID) - before; got != 0 {
		t.Fatalf("a full runner cost the card %d `moved` events across 10 ticks, want 0", got)
	}
	col, attempts, stuck := cardState(t, pool, cardID)
	if col != readyID {
		t.Fatal("the card left the ready column although no session ever started")
	}
	if attempts != 0 || stuck {
		t.Fatalf("card marked by a capacity refusal: run_attempts=%d stuck=%v, want 0/false — "+
			"the runner being full is not this card's fault", attempts, stuck)
	}
	// One attempt establishes there is no slot; the recorded refusal is what
	// spares the runner the other nine.
	if n := fake.attemptCount(); n != 1 {
		t.Fatalf("dispatch asked a full runner %d times in 10 ticks, want 1", n)
	}
	// A board with ready work and nothing moving has to say why: the chip
	// renders this, and the flapping used to be the only sign.
	if st := capacityStatus(t, srv, cookie, boardID); !st.RunnerFull || st.Ready != 1 {
		t.Fatalf("run status while the runner is full = runner_full:%v ready:%d, want true/1",
			st.RunnerFull, st.Ready)
	}
}

// TestPressingRunClearsACachedRefusal: several service comments tell a human
// to press Run to retry, and handleStartRun clears every stuck flag because
// "a human pressing Run is consent". A remembered refusal has to yield to the
// same consent — otherwise Run is a no-op for up to two minutes, which from
// the outside is indistinguishable from broken.
func TestPressingRunClearsACachedRefusal(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)
	fake := &fakeRunner{lifecycle: "running", startErr: errors.New("cluster session cap reached (12)")}
	boardID, cardID, _, workID := capacityFixture(t, pool, fake)

	srvAPI.RunTick(ctx) // refused; the refusal is now on record
	fake.startErr = nil // room, but nothing blerg-board tracks has settled

	resp := request(t, srv, "POST", "/api/boards/"+boardID+"/run", cookie, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start run: %d", resp.StatusCode)
	}
	if st := capacityStatus(t, srv, cookie, boardID); st.RunnerFull {
		t.Fatal("pressing Run left the capacity refusal on record")
	}
	tickUntilDispatched(t, pool, cardID)

	if col, attempts, _ := cardState(t, pool, cardID); col != workID || attempts != 1 {
		t.Fatalf("after Run: column==work %v, run_attempts %d, want true/1", col == workID, attempts)
	}
}

// TestClosingASessionFreesTheSlotForDispatch: the "a slot freed" signal reads
// settled sessions out of the database, and the paths where BLERG BOARD stops a
// session (this one, the idle reaper, the done-card cleanup) know a slot has
// come back without waiting to be told. Dispatch should spend it on the next
// tick rather than sitting out the backoff ceiling.
func TestClosingASessionFreesTheSlotForDispatch(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)
	fake := &fakeRunner{lifecycle: "running", startErr: errors.New("cluster session cap reached (12)")}
	boardID, cardID, _, workID := capacityFixture(t, pool, fake)

	// A session blerg-board is holding a slot with, and a refusal on record.
	var sessionID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO runner_sessions (board_id, runner, external_session_id, lifecycle, role)
		VALUES ($1, 'fake', 'ext-held', 'running', 'board') RETURNING id`, boardID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	srvAPI.RunTick(ctx)
	fake.startErr = nil

	resp := request(t, srv, "POST", "/api/runner-sessions/"+sessionID+"/close", cookie, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("close session: %d", resp.StatusCode)
	}
	srvAPI.RunTick(ctx)

	if n := fake.startCount(); n != 1 {
		t.Fatalf("the tick after blerg-board freed a slot itself started %d sessions, want 1", n)
	}
	if col, _, _ := cardState(t, pool, cardID); col != workID {
		t.Fatal("the card was not picked up after blerg-board freed a slot")
	}
}

// TestSessionStartedAsTheContextDiesStillClaimsItsCard: spawn-then-claim puts
// an irreversible side effect (a session live on the runner) before the write
// that records it. If the pass's context dies in that window — it is the
// server's context, so every deploy cancels one mid-pass — the claim, the
// session row and the link must land anyway. A started session whose card is
// still in ready is the state that double-spawns: the next pass reads the same
// top-of-ready card and puts a second worker on the same repo.
func TestSessionStartedAsTheContextDiesStillClaimsItsCard(t *testing.T) {
	_, pool := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &fakeRunner{lifecycle: "running"}
	_, cardID, _, workID := capacityFixture(t, pool, fake)

	// The deploy lands between "the runner has the session" and "blerg-board has
	// written that down".
	fake.onStart = cancel

	srvAPI.RunTick(ctx)

	col, attempts, stuck := cardState(t, pool, cardID)
	if col != workID {
		t.Fatal("a session started but its card is still in ready — the next pass would spawn a second worker")
	}
	if attempts != 1 || stuck {
		t.Fatalf("claimed card: run_attempts=%d stuck=%v, want 1/false", attempts, stuck)
	}
	var rows int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM runner_sessions WHERE card_id = $1`, cardID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d runner_sessions rows for a started session, want 1 — a session blerg-board has no row for "+
			"is invisible to ingest, to the reaper and to the board", rows)
	}

	// And the next pass, on a live context, must not start a second worker.
	fake.onStart = nil
	srvAPI.RunTick(context.Background())
	if n := fake.startCount(); n != 1 {
		t.Fatalf("%d sessions started for one card, want 1", n)
	}
}

// TestDispatchResumesOnTheFirstTickAfterASlotFrees: backing off must not turn
// into a fixed wait. A settled session is a freed slot, and the very next tick
// spends it.
func TestDispatchResumesOnTheFirstTickAfterASlotFrees(t *testing.T) {
	_, pool := testServer(t)
	ctx := context.Background()
	fake := &fakeRunner{lifecycle: "running", startErr: errors.New("cluster session cap reached (12)")}
	_, cardID, _, workID := capacityFixture(t, pool, fake)

	srvAPI.RunTick(ctx) // refused: the runner is full, and blerg-board now knows it
	before := movedEvents(t, pool, cardID)

	// Room appears, but nothing has told blerg-board yet: the refusal still holds.
	fake.startErr = nil
	srvAPI.RunTick(ctx)
	if n := fake.attemptCount(); n != 1 {
		t.Fatalf("dispatch asked again inside the backoff window (%d attempts), want 1", n)
	}

	// A session settles — that is a slot, and blerg-board can see it.
	card, err := db.GetCard(ctx, pool, cardID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO runner_sessions (board_id, runner, external_session_id, lifecycle, role, last_activity_at)
		VALUES ($1, 'fake', 'ext-freed', 'stopped', 'worker', now())`, card.BoardID); err != nil {
		t.Fatal(err)
	}

	srvAPI.RunTick(ctx)
	if n := fake.startCount(); n != 1 {
		t.Fatalf("the tick after a slot freed started %d sessions, want 1", n)
	}
	col, attempts, stuck := cardState(t, pool, cardID)
	if col != workID {
		t.Fatal("the dispatched card is not in the work column")
	}
	if attempts != 1 || stuck {
		t.Fatalf("dispatched card: run_attempts=%d stuck=%v, want 1/false", attempts, stuck)
	}
	if got := movedEvents(t, pool, cardID) - before; got != 1 {
		t.Fatalf("picking the card up wrote %d `moved` events, want exactly 1 (the claim)", got)
	}
}

// TestHardSpawnFailureFlagsStuckWithoutClaiming: a refusal that is NOT about
// capacity is this card's own problem. It never reaches the work column now,
// so the flag has to carry the explanation the column move used to imply — and
// the flag is also what keeps the queue moving past it.
func TestHardSpawnFailureFlagsStuckWithoutClaiming(t *testing.T) {
	_, pool := testServer(t)
	ctx := context.Background()
	fake := &fakeRunner{lifecycle: "running", startErr: errors.New("image pull backoff")}
	boardID, cardID, readyID, _ := capacityFixture(t, pool, fake)

	before := movedEvents(t, pool, cardID)
	srvAPI.RunTick(ctx)
	srvAPI.RunTick(ctx)

	col, _, stuck := cardState(t, pool, cardID)
	if col != readyID {
		t.Fatal("a card whose session never started was moved out of ready")
	}
	if !stuck {
		t.Fatal("a hard spawn failure left the card unflagged — dispatch would retry it forever")
	}
	if got := movedEvents(t, pool, cardID) - before; got != 0 {
		t.Fatalf("a failed spawn wrote %d `moved` events, want 0", got)
	}
	if n := fake.attemptCount(); n != 1 {
		t.Fatalf("dispatch retried a stuck card (%d attempts), want 1", n)
	}
	var explained int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM card_events WHERE card_id = $1 AND type = 'comment'
		  AND data->>'text' LIKE 'Board run: no session could be started%'`, cardID).Scan(&explained); err != nil {
		t.Fatal(err)
	}
	if explained != 1 {
		t.Fatalf("the stuck flag came with %d explanations, want 1", explained)
	}
	// and the run's own tally, which is what the board chip's "N stuck" reads
	var tally int
	if err := pool.QueryRow(ctx,
		`SELECT cards_stuck FROM board_runs WHERE board_id = $1`, boardID).Scan(&tally); err != nil {
		t.Fatal(err)
	}
	if tally != 1 {
		t.Fatalf("board_runs.cards_stuck = %d after one stuck card, want 1", tally)
	}
}

// TestQuietWorkerIsNotFlaggedStuckByAFullRunner: the reaper respawns a card
// whose worker went silent, and used to flag the card stuck when that respawn
// failed for ANY reason — including the runner being full, which says nothing
// about the card. It is the same rule dispatch follows: capacity is transient
// and global, so it never earns a flag.
func TestQuietWorkerIsNotFlaggedStuckByAFullRunner(t *testing.T) {
	_, pool := testServer(t)
	ctx := context.Background()
	fake := &fakeRunner{lifecycle: "running", startErr: errors.New("cluster session cap reached (12)")}
	_, cardID, _, workID := capacityFixture(t, pool, fake)

	// The card is in the work column with no session and no activity: a
	// worker that went quiet, past the grace period.
	if _, err := db.MoveCard(ctx, pool, cardID, workID, nil, nil, db.EventMeta{Actor: "service"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE cards SET updated_at = now() - interval '1 hour' WHERE id = $1`, cardID); err != nil {
		t.Fatal(err)
	}

	srvAPI.RunTick(ctx)
	srvAPI.RunTick(ctx)

	if _, _, stuck := cardState(t, pool, cardID); stuck {
		t.Fatal("a full runner flagged a quiet card stuck — capacity is not the card's fault")
	}
	if n := fake.attemptCount(); n != 1 {
		t.Fatalf("the reaper asked a full runner %d times, want 1", n)
	}

	// A slot frees: the respawn happens, still without a stuck flag.
	fake.startErr = nil
	card, err := db.GetCard(ctx, pool, cardID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO runner_sessions (board_id, runner, external_session_id, lifecycle, role, last_activity_at)
		VALUES ($1, 'fake', 'ext-freed', 'stopped', 'worker', now())`, card.BoardID); err != nil {
		t.Fatal(err)
	}
	srvAPI.RunTick(ctx)
	if n := fake.startCount(); n != 1 {
		t.Fatalf("the tick after a slot freed respawned %d sessions, want 1", n)
	}
	if _, _, stuck := cardState(t, pool, cardID); stuck {
		t.Fatal("the respawned card is flagged stuck")
	}
}

// TestRefusalTextTheDatabaseWouldRejectStillRecordsTheRefusal: the refusal
// row is the whole mechanism keeping dispatch off a full runner, and `detail`
// — the runner's own words, which nothing reads back — rides on the same
// INSERT. A UTF8 database rejects an invalid byte sequence outright (SQLSTATE
// 22021) and takes the statement with it, so a decorative field could silently
// cost the record: no refusal on file, and every tick after asks a runner that
// is still full, exactly the 20s re-ask this card exists to stop.
//
// NOTE these only bite on a UTF8 database. CI's postgres:16-alpine is one; an
// `initdb` with no locale gives SQL_ASCII, which stores the bad bytes happily.
// They assert the ROW rather than the text, so they stay honest on either.
func TestRefusalTextTheDatabaseWouldRejectStillRecordsTheRefusal(t *testing.T) {
	// Two ways the runner's words go bad, both reachable through BlergRunner.do,
	// which formats the response body into the error with %.200s.
	cases := map[string]error{
		// %.200s counts RUNES, so a non-ASCII 503 page yields an error string
		// of several hundred bytes. The 500-byte cut then lands mid-rune —
		// the fixture below checks it really does, so the test cannot quietly
		// stop testing anything if the limit moves.
		"cut lands mid-rune": errors.New(midRuneCapacityError(t)),
		// and a body that was never valid UTF-8 to begin with (a latin-1
		// error page from an ingress). Length has nothing to do with this one.
		"never valid utf-8": errors.New("cap reached: \xff\xfe not utf-8"),
		// text cannot hold a NUL either, at any length
		"NUL in the runner's words": errors.New("cap reached: \x00 truncated"),
	}
	for name, spawnErr := range cases {
		t.Run(name, func(t *testing.T) {
			_, pool := testServer(t)
			ctx := context.Background()
			fake := &fakeRunner{lifecycle: "running", startErr: spawnErr}
			_, cardID, readyID, _ := capacityFixture(t, pool, fake)

			srvAPI.RunTick(ctx)

			var rows int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM runner_capacity`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 1 {
				t.Fatalf("%d runner_capacity rows after the refusal, want 1 — "+
					"an unrecorded refusal puts the every-20s re-ask straight back", rows)
			}
			// and the record does its job on the following tick
			srvAPI.RunTick(ctx)
			if n := fake.attemptCount(); n != 1 {
				t.Fatalf("dispatch asked a full runner %d times across 2 ticks, want 1", n)
			}
			if col, attempts, stuck := cardState(t, pool, cardID); col != readyID || attempts != 0 || stuck {
				t.Fatalf("the card paid for the runner's error text: column==ready %v attempts=%d stuck=%v",
					col == readyID, attempts, stuck)
			}
		})
	}
}

// midRuneCapacityError builds a capacity error long enough to be truncated,
// whose byte-500 boundary falls INSIDE a rune — and fails the test if it does
// not, because then it would prove nothing.
func midRuneCapacityError(t *testing.T) string {
	t.Helper()
	// 500 is api.detailMax, which an external test package cannot name.
	const cut = 500
	s := "HTTP 503 from the runner: cap reached: " + strings.Repeat("\u2026", 400)
	if len(s) <= cut {
		t.Fatalf("fixture is %d bytes, needs to exceed the %d-byte cut", len(s), cut)
	}
	if utf8.ValidString(s[:cut]) {
		t.Fatalf("fixture no longer cuts mid-rune at %d bytes — it would pass without the fix", cut)
	}
	return s
}

// TestHumanMoveDuringTheSpawnWindowIsNotOverwritten: spawning before claiming
// puts a pod start — seconds — between reading the card and moving it. A human
// who files the card away inside that window must win: an unguarded claim
// would drag it back into the work column, and MoveCard clears archived_at, so
// it would un-archive one they had just archived. That is the same "dispatch
// moved a card nobody asked it to move" this whole change exists to stop.
func TestHumanMoveDuringTheSpawnWindowIsNotOverwritten(t *testing.T) {
	_, pool := testServer(t)
	ctx := context.Background()
	fake := &fakeRunner{lifecycle: "running"}
	boardID, cardID, _, workID := capacityFixture(t, pool, fake)

	cols, err := db.ListColumns(ctx, pool, boardID)
	if err != nil {
		t.Fatal(err)
	}
	doneID := columnNamed(t, cols, "done")
	// the human acts while the runner is starting the pod
	fake.onStart = func() {
		if _, err := db.MoveCard(ctx, pool, cardID, doneID, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
			t.Error(err)
		}
	}

	srvAPI.RunTick(ctx)

	col, attempts, stuck := cardState(t, pool, cardID)
	if col != doneID {
		t.Fatalf("dispatch overwrote a human's move made during the spawn window (card is in %s, human put it in done)",
			map[bool]string{true: "the work column", false: col}[col == workID])
	}
	if attempts != 1 {
		t.Fatalf("run_attempts=%d after a session really did start, want 1 — the slot was spent", attempts)
	}
	if stuck {
		t.Fatal("a human moving their own card is not a stuck card")
	}
	// The session is real, so the card has to say where it went.
	var notes int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM card_events
		WHERE card_id = $1 AND type = 'comment' AND data->>'text' LIKE '%left the ready column%'`,
		cardID).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if notes != 1 {
		t.Fatalf("%d comments explaining the live session, want 1 — a session nobody is told about is an orphan", notes)
	}
	// and no second worker on the next pass
	fake.onStart = nil
	srvAPI.RunTick(ctx)
	if n := fake.startCount(); n != 1 {
		t.Fatalf("%d sessions started for one card, want 1", n)
	}
}

// TestArchivingDuringTheSpawnWindowLeavesTheCardArchived: the same guard, in
// its worst form. MoveCard sets archived_at = NULL, so an unguarded claim
// resurrects a card a human archived seconds earlier — with a live session on
// it, in the work column, as if the board had disagreed with them.
func TestArchivingDuringTheSpawnWindowLeavesTheCardArchived(t *testing.T) {
	_, pool := testServer(t)
	ctx := context.Background()
	fake := &fakeRunner{lifecycle: "running"}
	_, cardID, _, _ := capacityFixture(t, pool, fake)

	fake.onStart = func() {
		if _, err := db.ArchiveCard(ctx, pool, cardID, nil, db.EventMeta{Actor: "human"}); err != nil {
			t.Error(err)
		}
	}

	srvAPI.RunTick(ctx)

	var archived bool
	var colID *string
	if err := pool.QueryRow(ctx,
		`SELECT archived_at IS NOT NULL, column_id FROM cards WHERE id = $1`, cardID).
		Scan(&archived, &colID); err != nil {
		t.Fatal(err)
	}
	if !archived {
		t.Fatal("dispatch un-archived a card a human archived during the spawn window")
	}
	if colID != nil {
		t.Fatalf("an archived card was put back in a column (%s)", *colID)
	}
}
