package db_test

// H1/H2/M5 3b integration tests (real Postgres) for card-time attribution, per
// docs/design/card-time.md §2's failure matrix. Codex round 2: CardTimeTotal is a single bounded
// read (no ledger, no triggers) — these tests exercise that read directly, including its two
// non-boolean outcomes (pending vs. genuinely nothing) and that it is safe under a concurrent
// session_card_links write (there is nothing to race: one statement, one snapshot).
//
// Fixture ordering matters: BLG-424's own invalidate_insight_links trigger (139_insights_write_
// cache.sql) clears session_insight_timings for a session the moment a NEW session_card_links row
// is written for it. In real operation a session is usually linked before or during its work, and
// BLG-424's own turn_done/session-end hooks (agentevents.go, session_end.go) materialize the
// cache AFTER that, so the cache always wins the race in practice. These fixtures follow the same
// order: link, then run events, then materialize (refreshTiming) — mirroring the real sequence
// rather than fighting it.

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// runSession appends a status_changed/turn_done pair and sets started_at/ended_at, but does NOT
// materialize session_insight_timings — call refreshTiming afterward once every link for the
// session is already in place, the same order BLG-424's own turn_done hook would see in practice.
func runSession(t *testing.T, pool *pgxpool.Pool, id string, start time.Time, duration time.Duration) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE sessions SET started_at=$2,ended_at=$2::timestamptz+$3 WHERE id=$1`, id, start, duration); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEventAt(ctx, pool, id, newUUID(t, pool), "status_changed", `{"status":"running"}`, start); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEventAt(ctx, pool, id, newUUID(t, pool), "turn_done", `{}`, start.Add(duration)); err != nil {
		t.Fatal(err)
	}
}

func refreshTiming(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	if err := db.RefreshInsightTiming(context.Background(), pool, id); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, pool *pgxpool.Pool, id, board string, number int, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO session_card_links VALUES($1,$2,$3,'pin',$4)`, id, board, number, at); err != nil {
		t.Fatal(err)
	}
}

// seedCached links id to (board,number), runs its session, and materializes its timing cache, in
// that order — the common case most tests below need.
func seedCached(t *testing.T, pool *pgxpool.Pool, id, board string, number int, start time.Time, duration time.Duration) {
	t.Helper()
	link(t, pool, id, board, number, start)
	runSession(t, pool, id, start, duration)
	refreshTiming(t, pool, id)
}

// H1: a root/Main session's own time is never counted even if directly linked; only its non-root
// linked children count.
func TestCardTimeTotalExcludesRoot(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	root := mustSession(t, pool, daemon, owner)
	worker := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: owner, ParentSessionID: root})
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	seedCached(t, pool, root, "board-a", 1, now, 20*time.Minute)
	seedCached(t, pool, worker, "board-a", 1, now, 5*time.Minute)

	seconds, ok, pending, err := db.CardTimeTotal(context.Background(), pool, owner, "board-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || pending || seconds <= 0 {
		t.Fatalf("got (%v,ok=%v,pending=%v), want a known positive total", seconds, ok, pending)
	}
	// The root ran 4x longer than the worker; if the root's time leaked in, the total would be far
	// larger than the worker's own ~5 minutes.
	if seconds >= 10*60 {
		t.Fatalf("got %v seconds, want roughly the worker's own ~5 minutes, not the root's ~20 (the root must never count)", seconds)
	}
}

// New design, explicit requirement: a session sharing its link with another card simply drops out
// of this card's total on its own — it neither counts nor poisons the rest. A second, genuinely
// single-card session on the same card still counts in full.
func TestCardTimeTotalOneSharedPlusOneEligibleCountsOnlyTheEligibleOne(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	root := mustSession(t, pool, daemon, owner)
	eligible := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: owner, ParentSessionID: root})
	shared := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: owner, ParentSessionID: root})
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	link(t, pool, shared, "board-a", 1, now)
	link(t, pool, shared, "board-a", 2, now) // makes `shared` linked to two cards, before it ever runs
	seedCached(t, pool, eligible, "board-a", 1, now, 5*time.Minute)
	runSession(t, pool, shared, now, 50*time.Minute) // much longer, to prove it never counts
	refreshTiming(t, pool, shared)

	seconds, ok, pending, err := db.CardTimeTotal(context.Background(), pool, owner, "board-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || pending {
		t.Fatalf("got ok=%v pending=%v, want a known total from the eligible session alone", ok, pending)
	}
	if seconds <= 0 || seconds >= 10*60 {
		t.Fatalf("got %v seconds, want roughly the eligible session's own ~5 minutes, not the shared session's ~50 (it must never count)", seconds)
	}
}

// Unsharing restores immediately, in the sense that matters: ELIGIBILITY reflects the current
// session_card_links rows on the very next read, with no separate cleanup or "restore" step —
// unlike the old ledger design, where un-sharing left a stale, wrong row sitting in a table with
// no path back to correctness short of a second fix. (BLG-424's own invalidate_insight_links
// trigger also clears the now-un-shared session's cache on the DELETE that un-shares it, same as
// it does on the INSERT that shares it — so the total itself is "pending" immediately after, same
// as any other session whose cache was just invalidated. The right next read to check is the one
// after the next natural materialization, which here is a direct refreshTiming call standing in
// for the session's own next turn-end — and that read requires no special handling at all.)
func TestCardTimeTotalUnsharingRestoresImmediately(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	root := mustSession(t, pool, daemon, owner)
	worker := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: owner, ParentSessionID: root})
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	link(t, pool, worker, "board-a", 1, now)
	link(t, pool, worker, "board-a", 2, now)
	runSession(t, pool, worker, now, 5*time.Minute)
	refreshTiming(t, pool, worker)

	if _, ok, _, err := db.CardTimeTotal(context.Background(), pool, owner, "board-a", 1); err != nil || ok {
		t.Fatalf("got ok=%v err=%v while shared, want ok=false", ok, err)
	}

	if _, err := pool.Exec(context.Background(), `DELETE FROM session_card_links WHERE session_id=$1 AND board_id='board-a' AND card_number=2`, worker); err != nil {
		t.Fatal(err)
	}

	// Eligibility already reflects the change: the session is no longer shared, no matter that its
	// cache was just invalidated by the same write (the same query, run fresh, would stop
	// excluding it as "disqualified"; here it reports pending instead, since the cache is briefly
	// empty — never the old design's silent wrong number).
	_, ok, pending, err := db.CardTimeTotal(context.Background(), pool, owner, "board-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("got ok=true immediately after un-sharing with an invalidated cache, want ok=false (pending=%v) — never a guessed number", pending)
	}

	// No separate restore step exists or is needed: the session's own next materialization (its
	// next turn-end in practice) is a normal refresh, and the total is correct right after it.
	refreshTiming(t, pool, worker)
	seconds, ok, pending, err := db.CardTimeTotal(context.Background(), pool, owner, "board-a", 1)
	if err != nil || !ok || pending || seconds <= 0 {
		t.Fatalf("got (%v,ok=%v,pending=%v,%v) after un-sharing and the next normal refresh, want a known positive total with no special restore step", seconds, ok, pending, err)
	}
}

// Unlinked-then-later-linked: a session's full history (recorded before it had any card link)
// counts in full once a direct link exists — nothing about "when" the link was written matters,
// because every read is live against the current session_card_links rows. The link itself wipes
// any stale cache (BLG-424's own invalidate_insight_links trigger), so the session's own next
// turn-end materializes it correctly; this test simulates that next turn-end explicitly.
func TestCardTimeTotalIncludesHistoryRecordedBeforeTheLink(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	root := mustSession(t, pool, daemon, owner)
	worker := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: owner, ParentSessionID: root})
	workStart := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	linkedAt := workStart.Add(time.Hour) // the link is written an hour AFTER the work started
	runSession(t, pool, worker, workStart, 5*time.Minute)
	link(t, pool, worker, "board-a", 1, linkedAt)
	refreshTiming(t, pool, worker) // the session's next turn-end (simulated) materializes it

	seconds, ok, pending, err := db.CardTimeTotal(context.Background(), pool, owner, "board-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || pending || seconds <= 0 {
		t.Fatalf("got (%v,ok=%v,pending=%v), want a positive total: the work recorded before linked_at must still count in full", seconds, ok, pending)
	}
}

// M5: two accounts with sessions on the same (board,card) never see each other's time.
func TestCardTimeTotalIsScopedByAccount(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	ownerA := "cardtime-a-" + newUUID(t, pool)
	ownerB := "cardtime-b-" + newUUID(t, pool)
	rootA := mustSession(t, pool, daemon, ownerA)
	workerA := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: ownerA, ParentSessionID: rootA})
	rootB := mustSession(t, pool, daemon, ownerB)
	workerB := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: ownerB, ParentSessionID: rootB})
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	// Same board/card number for both accounts, as if they shared a team board.
	seedCached(t, pool, workerA, "shared-board", 9, now, 5*time.Minute)
	seedCached(t, pool, workerB, "shared-board", 9, now, 5*time.Minute)

	secondsA, okA, pendingA, err := db.CardTimeTotal(context.Background(), pool, ownerA, "shared-board", 9)
	if err != nil {
		t.Fatal(err)
	}
	secondsB, okB, pendingB, err := db.CardTimeTotal(context.Background(), pool, ownerB, "shared-board", 9)
	if err != nil {
		t.Fatal(err)
	}
	if !okA || !okB || pendingA || pendingB {
		t.Fatalf("expected both accounts to see their own known total: okA=%v okB=%v pendingA=%v pendingB=%v", okA, okB, pendingA, pendingB)
	}
	if secondsA != secondsB {
		t.Fatalf("got secondsA=%v secondsB=%v: each worker ran the same duration alone, so the two totals (each one worker) must match, not be summed across accounts", secondsA, secondsB)
	}
}

// A card with no linked sessions at all answers ok=false, pending=false — nothing to show, not an
// unresolved state.
func TestCardTimeTotalEmptyCardIsNotPending(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	_ = mustSession(t, pool, daemon, owner)

	_, ok, pending, err := db.CardTimeTotal(context.Background(), pool, owner, "board-a", 404)
	if err != nil {
		t.Fatal(err)
	}
	if ok || pending {
		t.Fatalf("got ok=%v pending=%v for a card with no linked sessions, want both false", ok, pending)
	}
}

// Adapted to BLG-424's reworked timing cache (3f010401): a live (not yet ended) session is never
// cached, and an ended session isn't cached until something backfills it. CardTimeTotal must
// report that as pending — explicitly, not as zero — and the crossing check (server side) skips
// a pending card rather than guessing. CardTimeTotal itself never calls RefreshInsightTiming
// (round 2: "never recomputes history" at read time) — only the test fixture does, to prove the
// uncached case is reported correctly before any materialization happens.
func TestCardTimeTotalPendingForAnUncachedEndedSession(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	root := mustSession(t, pool, daemon, owner)
	worker := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: owner, ParentSessionID: root})
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	ctx := context.Background()
	link(t, pool, worker, "board-a", 1, now)
	runSession(t, pool, worker, now, 5*time.Minute)
	// Ended (runSession sets ended_at via its duration offset), but deliberately never refreshed
	// or backfilled — the exact "pending" state.
	var cached int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_insight_timings WHERE session_id=$1`, worker).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if cached != 0 {
		t.Fatalf("got %d cached rows before the test even checks, want 0 (fixture assumption broken)", cached)
	}

	seconds, ok, pending, err := db.CardTimeTotal(ctx, pool, owner, "board-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if ok || !pending || seconds != 0 {
		t.Fatalf("got (%v,ok=%v,pending=%v), want (0,ok=false,pending=true): an uncached ended session must be labeled pending, never read as zero", seconds, ok, pending)
	}

	// Once something backfills it, the next read sees the real total with no special handling.
	refreshTiming(t, pool, worker)
	seconds, ok, pending, err = db.CardTimeTotal(ctx, pool, owner, "board-a", 1)
	if err != nil || !ok || pending || seconds <= 0 {
		t.Fatalf("got (%v,ok=%v,pending=%v,%v) after backfill, want a known positive total", seconds, ok, pending, err)
	}
}

// Race: a concurrent session_card_links write and a CardTimeTotal read must never error, panic or
// deadlock (there is nothing to race in the data-correctness sense within a single read — each is
// one snapshot) — AND, once things settle, the committed states themselves must be right: shared
// work excluded the moment the second link commits, and restored (after an explicit refresh —
// Codex round 2's own point: nothing to race, nothing to restore as a separate step) once it's
// removed again. A version of this test that only checked for errors (the Codex round 3 L finding)
// would pass even if the eligibility guard in CardTimeTotal's `eligible` CTE were deleted; this one
// would not (verified by hand: removing the `WHERE (SELECT count(...))=1` clause left `ok=true`
// while shared, below, failing the test).
func TestCardTimeTotalConcurrentLinkCommitsExcludeSharedWork(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	root := mustSession(t, pool, daemon, owner)
	worker := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: owner, ParentSessionID: root})
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	seedCached(t, pool, worker, "board-a", 1, now, 5*time.Minute)

	solo, ok, pending, err := db.CardTimeTotal(context.Background(), pool, owner, "board-a", 1)
	if err != nil || !ok || pending || solo <= 0 {
		t.Fatalf("got (%v,ok=%v,pending=%v,%v) before sharing, want a known positive total", solo, ok, pending, err)
	}

	// A reader hammers CardTimeTotal concurrently with the single write below — proving the query
	// is safe to run against an in-flight session_card_links write, not just a quiescent one.
	const rounds = 50
	var wg sync.WaitGroup
	errs := make(chan error, rounds)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if _, _, _, err := db.CardTimeTotal(context.Background(), pool, owner, "board-a", 1); err != nil {
				errs <- err
			}
		}
	}()
	if _, err := pool.Exec(context.Background(), `INSERT INTO session_card_links VALUES($1,'board-a',2,'pin',$2)`, worker, now); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// The INSERT itself (BLG-424's invalidate_insight_links trigger) marks the cache pending,
	// which alone is enough to make CardTimeTotal report ok=false right now — that would be true
	// whether or not the eligibility guard still excludes a truly shared session. Refresh the
	// cache before asserting, so the only thing that can still make this ok=false is the
	// eligibility guard itself (verified by hand: deleting the guard's WHERE clause from
	// CardTimeTotal's `eligible` CTE made this assertion fail with ok=true and the stale solo
	// total, once this refresh was in place).
	refreshTiming(t, pool, worker)
	if _, ok, _, err := db.CardTimeTotal(context.Background(), pool, owner, "board-a", 1); err != nil || ok {
		t.Fatalf("got ok=%v err=%v after the second link committed and refreshed, want ok=false (shared work excluded)", ok, err)
	}

	// Removing the second link and letting the session's own next refresh materialize restores
	// the original total — no special "restore" step, just the next normal read.
	if _, err := pool.Exec(context.Background(), `DELETE FROM session_card_links WHERE session_id=$1 AND board_id='board-a' AND card_number=2`, worker); err != nil {
		t.Fatal(err)
	}
	refreshTiming(t, pool, worker)
	seconds, ok, pending, err := db.CardTimeTotal(context.Background(), pool, owner, "board-a", 1)
	if err != nil || !ok || pending || seconds != solo {
		t.Fatalf("got (%v,ok=%v,pending=%v,%v) after unsharing and refresh, want the original total %v restored", seconds, ok, pending, err, solo)
	}
}

// L8/review marks: marks for one board's card never leak into another board's same-numbered card.
func TestCardReviewMarksKeyedByBoardAndNumber(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	s := mustSession(t, pool, daemon, owner)
	now := time.Now()
	mustMark := func(board string, card, round int) {
		payload := `{"kind":"review","board_id":"` + board + `","card":` + strconv.Itoa(card) + `,"round":` + strconv.Itoa(round) + `,"verdict":"changes","findings":["high"]}`
		if _, _, err := db.AppendAgentEventAt(context.Background(), pool, s, newUUID(t, pool), "mark", payload, now); err != nil {
			t.Fatal(err)
		}
	}
	mustMark("board-a", 7, 1)
	mustMark("board-a", 7, 1) // a retried post with the same round: still a distinct mark (L8)
	mustMark("board-a", 7, 3)
	mustMark("board-b", 7, 1) // same card NUMBER, different board: must not leak in

	marks, err := db.CardReviewMarks(context.Background(), pool, owner, "board-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 3 {
		t.Fatalf("got %d marks for board-a#7, want 3 (board-b's mark must not leak in)", len(marks))
	}
}

// H2: RootSessionID walks to the session with no parent.
func TestRootSessionIDWalksToRoot(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	root := mustSession(t, pool, daemon, owner)
	mid := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: owner, ParentSessionID: root})
	leaf := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: owner, ParentSessionID: mid})

	got, err := db.RootSessionID(context.Background(), pool, leaf)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("got %s, want root %s", got, root)
	}
	// A root given to itself returns itself.
	got2, err := db.RootSessionID(context.Background(), pool, root)
	if err != nil {
		t.Fatal(err)
	}
	if got2 != root {
		t.Fatalf("root given to itself: got %s, want %s", got2, root)
	}
}

// f2867117 (140_insights_pending.sql): sessions.insight_pending is the authoritative "is this
// cache entry current" signal — not merely "does a row exist." insight_cache_ready (AFTER
// INSERT/UPDATE ON session_insight_timings) sets insight_pending=true itself whenever a cached
// row's own fingerprint_inputs version doesn't match the current one, WITHOUT deleting the row —
// the stale timing value is left sitting there for an old client that already computed it, but a
// fresh reader must never sum it as if it were current. This directly exercises that: it writes a
// cached row with an outdated fingerprint version by hand (the real write path never produces
// one; this stands in for "a backfill from a previous deploy, before the version bump").
func TestCardTimeTotalTreatsAStaleCachedRowAsPending(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	root := mustSession(t, pool, daemon, owner)
	worker := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: owner, ParentSessionID: root})
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	link(t, pool, worker, "board-a", 1, now)
	runSession(t, pool, worker, now, 5*time.Minute)

	ctx := context.Background()
	// A hand-written cache row with a non-current fingerprint version: insight_cache_ready marks
	// the session pending again as soon as this lands, but the row (and its real-looking timing
	// value) stays right there.
	if _, err := pool.Exec(ctx, `INSERT INTO session_insight_timings(session_id,fingerprint,timing,fingerprint_inputs)
 VALUES($1,'stale-fp','{"seconds":{"model":99999,"idle":0,"waiting":0,"read":0,"edit":0,"other":0,"tests":0,"build_lint":0,"git":0,"waiting_on_person":0,"unknown":0},"phases":{}}','{"version":1}')`,
		worker); err != nil {
		t.Fatal(err)
	}
	var insightPending bool
	if err := pool.QueryRow(ctx, `SELECT insight_pending FROM sessions WHERE id=$1`, worker).Scan(&insightPending); err != nil {
		t.Fatal(err)
	}
	if !insightPending {
		t.Fatalf("got insight_pending=false right after inserting a stale-version cache row, want true (fixture assumption broken: insight_cache_ready did not fire as expected)")
	}
	var stillCached int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_insight_timings WHERE session_id=$1`, worker).Scan(&stillCached); err != nil {
		t.Fatal(err)
	}
	if stillCached != 1 {
		t.Fatalf("got %d cached rows, want 1 (the stale row must still be sitting there, not deleted, for this test to prove the new check matters)", stillCached)
	}

	seconds, ok, pending, err := db.CardTimeTotal(ctx, pool, owner, "board-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if ok || !pending || seconds != 0 {
		t.Fatalf("got (%v,ok=%v,pending=%v), want (0,ok=false,pending=true): a stale cached row (wrong fingerprint version) must be reported pending, never summed as if current", seconds, ok, pending)
	}

	// Once re-materialized with the current fingerprint, the fresh value is used again.
	refreshTiming(t, pool, worker)
	seconds, ok, pending, err = db.CardTimeTotal(ctx, pool, owner, "board-a", 1)
	if err != nil || !ok || pending || seconds <= 0 || seconds >= 99999 {
		t.Fatalf("got (%v,ok=%v,pending=%v,%v) after re-materializing, want a known positive total close to the real ~5 minutes, not the stale row's 99999", seconds, ok, pending, err)
	}
}

// M (Codex round 3): CardReviewMarks previously compared `(e.payload->>'card')::int` against the
// card number, a different expression than agent_events_review_card_idx's own
// ((payload->>'board_id'),(payload->>'card'),session_id). With that mismatch, Postgres still
// scans the index by name (its leading board_id expression still matches) but can only use
// board_id as the actual Index Cond, falling back to a row-by-row Filter for the cast card
// expression — checking for the index's name alone would not have caught this (verified by
// hand: that weaker plan still names agent_events_review_card_idx). The fixed query compares as
// text, so both expressions land in the one Index Cond and nothing needs filtering afterward;
// this asserts that specifically.
func TestCardReviewMarksUsesTheReviewCardIndex(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `EXPLAIN
 SELECT e.ts, coalesce((e.payload->>'round')::int,0), coalesce(e.payload->>'verdict',''),
   coalesce((SELECT array_agg(f) FROM jsonb_array_elements_text(e.payload->'findings') f),'{}')
 FROM agent_events e JOIN sessions s ON s.id=e.session_id
 WHERE s.spawning_account_id='00000000-0000-4000-8000-000000000000' AND e.kind='mark' AND e.payload->>'kind'='review'
   AND e.payload->>'board_id'='board-a' AND e.payload->>'card'='7'::text
 ORDER BY e.ts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	got := plan.String()
	if !strings.Contains(got, "agent_events_review_card_idx") {
		t.Fatalf("agent_events_review_card_idx not used:\n%s", got)
	}
	if !strings.Contains(got, `Index Cond: (((payload ->> 'board_id'::text) = 'board-a'::text) AND ((payload ->> 'card'::text) = '7'::text))`) {
		t.Fatalf("card was not part of the index's own condition (fell back to a row Filter instead):\n%s", got)
	}
}
