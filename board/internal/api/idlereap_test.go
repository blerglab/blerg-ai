package api

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The idle reaper decides on clocks, so these tests write the clocks straight
// into the session rows: a session inserted here is one the runner started
// however long ago the test says, last heard from however long ago it says.
// Going through spawnSession instead would peg every timestamp to now() and
// leave nothing to reap.
func reapSession(t *testing.T, pool *pgxpool.Pool, board db.Board, cardID, role, ext, lifecycle,
	createdAgo, heardAgo string) {
	t.Helper()
	var card any
	if cardID != "" {
		card = cardID
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role,
			created_at, last_activity_at)
		VALUES ($1, $2, 'fake', $3, $4, $5, now() - $6::interval, now() - $7::interval)`,
		card, board.ID, ext, lifecycle, role, createdAgo, heardAgo); err != nil {
		t.Fatal(err)
	}
}

// lifecycleOf is the reaper's own record of what it did: it flips a stopped
// session's row to 'stopped'.
func lifecycleOf(t *testing.T, pool *pgxpool.Pool, ext string) string {
	t.Helper()
	var lc string
	if err := pool.QueryRow(context.Background(),
		`SELECT lifecycle FROM runner_sessions WHERE external_session_id = $1`, ext).Scan(&lc); err != nil {
		t.Fatal(err)
	}
	return lc
}

// verdict posts what an adversarial reviewer posts, aged by ago so a test can
// put it either side of the request that should supersede it.
func reapVerdict(t *testing.T, pool *pgxpool.Pool, cardID, text, ago string) {
	t.Helper()
	ctx := context.Background()
	if err := db.AppendComment(ctx, pool, cardID, text, db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE card_events SET created_at = now() - $2::interval
		WHERE card_id = $1 AND type = 'comment' AND data->>'text' = $3`, cardID, ago, text); err != nil {
		t.Fatal(err)
	}
}

const approved = "Adversarial review\n\nnothing to fix.\n\nVerdict: approve"

// The headline: a worker that pushed its PR and handed the card to review is
// idle for a reason — it is waiting on a verdict it will have to act on.
// Reviewers routinely outlast the worker's 30-minute idle threshold (checkout,
// build, full suite), and reaping the worker mid-review means the findings land
// on a session that has to re-clone and re-read the whole card to act on them.
func TestIdleWorkerSurvivesWhileItsReviewIsInFlight(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, _ := specCard(t, pool, "a card that reached review")

	reapSession(t, pool, board, card.ID, "worker", "ext-worker", "idle", "2 hours", "40 minutes")
	reapSession(t, pool, board, card.ID, "reviewer", "ext-reviewer", "running", "35 minutes", "4 minutes")

	a.reapIdleSessions(ctx)

	if lc := lifecycleOf(t, pool, "ext-worker"); lc != "idle" {
		t.Fatalf("worker lifecycle = %q after a sweep with its review still running, want idle", lc)
	}
	if len(fake.stopped) != 0 {
		t.Fatalf("the reaper stopped %v mid-review, want nothing", fake.stopped)
	}
}

// …and once the verdict is posted, handed over and gone cold, the reprieve is
// over: the worker is back on the ordinary 30-minute clock, which it is already
// past. The verdict is aged past verdictHandoffGrace here on purpose — a
// verdict fresher than that is the hand-off window, which the next test covers.
func TestIdleWorkerIsReapedOnceTheVerdictLands(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, _ := specCard(t, pool, "a card whose review finished")

	reapSession(t, pool, board, card.ID, "worker", "ext-worker", "idle", "2 hours", "40 minutes")
	reapSession(t, pool, board, card.ID, "reviewer", "ext-reviewer", "idle", "60 minutes", "5 minutes")
	reapVerdict(t, pool, card.ID, approved, "20 minutes")

	a.reapIdleSessions(ctx)

	if lc := lifecycleOf(t, pool, "ext-worker"); lc != "stopped" {
		t.Fatalf("worker lifecycle = %q after the verdict landed, want stopped", lc)
	}
	if len(fake.stopped) != 1 || fake.stopped[0] != "ext-worker" {
		t.Fatalf("runner was asked to stop %v, want [ext-worker]", fake.stopped)
	}
}

// The verdict comment is not the hand-off. Posting it and moving the card are
// separate reviewer actions, and only the move runs maybeRelayFindings — so
// between them the worker would be an ordinary 40-minute-idle session again,
// with a 20s dispatcher tick to fall into. Reaping there is the worst of both
// worlds: the relay finds nobody, the findings sit on the card, and the next
// worker is a cold rebuild. A verdict younger than verdictHandoffGrace that
// this worker has not acted on keeps it.
func TestIdleWorkerSurvivesTheVerdictToMoveGap(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, _ := specCard(t, pool, "a card whose verdict just landed")

	reapSession(t, pool, board, card.ID, "worker", "ext-worker", "idle", "2 hours", "40 minutes")
	reapSession(t, pool, board, card.ID, "reviewer", "ext-reviewer", "idle", "60 minutes", "1 minute")
	reapVerdict(t, pool, card.ID,
		"Adversarial review\n\nthe error path is untested.\n\nVerdict: request-changes", "30 seconds")

	a.reapIdleSessions(ctx)

	if lc := lifecycleOf(t, pool, "ext-worker"); lc != "idle" {
		t.Fatalf("worker lifecycle = %q in the verdict-to-move gap, want idle", lc)
	}
	if len(fake.stopped) != 0 {
		t.Fatalf("the reaper stopped %v before the findings were relayed, want nothing", fake.stopped)
	}
}

// The same gap, end to end and in the order the reviewer actually works in:
// post the verdict, let a dispatcher tick land, then move the card. The point
// of sparing the worker is that the findings reach a session that already has
// the card in its head — so this asserts the relay, not just the lifecycle.
func TestFindingsReachTheWorkerAcrossAReapTick(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a card being handed its findings")
	specLinkPR(t, pool, card.ID)

	reapSession(t, pool, board, card.ID, "worker", "ext-worker", "idle", "2 hours", "40 minutes")
	reapSession(t, pool, board, card.ID, "reviewer", "ext-reviewer", "running", "60 minutes", "1 minute")
	reapVerdict(t, pool, card.ID,
		"Adversarial review\n\nthe error path is untested.\n\nVerdict: request-changes", "30 seconds")

	a.reapIdleSessions(ctx) // the tick that used to land in the gap

	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "progress"), nil, nil,
		db.EventMeta{Actor: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)

	if len(fake.messagedTo) != 1 || fake.messagedTo[0] != "ext-worker" {
		t.Fatalf("findings relayed to %v, want [ext-worker] — the session was reaped in the hand-off gap",
			fake.messagedTo)
	}
}

// The hand-off grace does not depend on a reviewer still breathing: what the
// worker is waiting for is the move, and the reviewer that will make it may
// already have finished and been stopped. It is bounded by the clock alone.
func TestVerdictHandoffGraceOutlivesTheReviewerSession(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	ctx := context.Background()
	board, card, _ := specCard(t, pool, "a card whose reviewer signed off and stopped")

	reapSession(t, pool, board, card.ID, "worker", "ext-worker", "idle", "2 hours", "40 minutes")
	reapSession(t, pool, board, card.ID, "reviewer", "ext-reviewer", "stopped", "60 minutes", "1 minute")
	reapVerdict(t, pool, card.ID, approved, "1 minute")

	a.reapIdleSessions(ctx)

	if lc := lifecycleOf(t, pool, "ext-worker"); lc != "idle" {
		t.Fatalf("worker lifecycle = %q inside the hand-off grace, want idle", lc)
	}
}

// A review nobody is running is not a review. The reviewer pod died before
// posting anything, so the request stands unanswered forever — without the
// liveness half of the test that latch would keep the worker alive forever
// too, which is exactly the slot rent this reaper exists to collect.
func TestIdleWorkerIsReapedWhenTheReviewerIsGone(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	ctx := context.Background()
	board, card, _ := specCard(t, pool, "a card whose reviewer died")

	reapSession(t, pool, board, card.ID, "worker", "ext-worker", "idle", "2 hours", "40 minutes")
	reapSession(t, pool, board, card.ID, "reviewer", "ext-reviewer", "error", "35 minutes", "30 minutes")

	a.reapIdleSessions(ctx)

	if lc := lifecycleOf(t, pool, "ext-worker"); lc != "stopped" {
		t.Fatalf("worker lifecycle = %q with a dead reviewer and no verdict, want stopped", lc)
	}
}

// The other way a reviewer stops being able to post a verdict: its pod is
// wedged in 'running' and has not been heard from in hours. Nothing in the
// lifecycle says so, so the reprieve is bounded by the clock instead.
func TestMidReviewReprieveExpiresWithASilentReviewer(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	ctx := context.Background()
	board, card, _ := specCard(t, pool, "a card whose reviewer went quiet")

	reapSession(t, pool, board, card.ID, "worker", "ext-worker", "idle", "5 hours", "40 minutes")
	reapSession(t, pool, board, card.ID, "reviewer", "ext-reviewer", "running", "4 hours", "3 hours")

	a.reapIdleSessions(ctx)

	if lc := lifecycleOf(t, pool, "ext-worker"); lc != "stopped" {
		t.Fatalf("worker lifecycle = %q past the reprieve on a silent reviewer, want stopped", lc)
	}
}

// A re-review asked of an existing reviewer session writes no new session row —
// the request is the service comment maybeSpawnReviewer leaves. That round is
// as outstanding as a freshly spawned one, and its worker is as much waiting on
// it, even though the newest reviewer session predates the last verdict.
func TestReReviewRequestSparesTheWorkerAgain(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, _ := specCard(t, pool, "a card in its second review round")

	reapSession(t, pool, board, card.ID, "worker", "ext-worker", "idle", "3 hours", "35 minutes")
	reapSession(t, pool, board, card.ID, "reviewer", "ext-reviewer", "idle", "90 minutes", "12 minutes")
	reapVerdict(t, pool, card.ID,
		"Adversarial review\n\nthe error path is untested.\n\nVerdict: request-changes", "40 minutes")
	reapVerdict(t, pool, card.ID,
		"Adversarial re-review requested from the existing reviewer session.", "10 minutes")

	a.reapIdleSessions(ctx)

	if lc := lifecycleOf(t, pool, "ext-worker"); lc != "idle" {
		t.Fatalf("worker lifecycle = %q during a re-review round, want idle", lc)
	}
	if len(fake.stopped) != 0 {
		t.Fatalf("the reaper stopped %v during a re-review, want nothing", fake.stopped)
	}
}

// The exemption is about who will need the verdict, so it covers workers only.
// On the very card whose review is in flight, an idle reviewer past its own
// threshold and an idle discuss session past its (longer) one are reaped
// exactly as before — the reviewer that stops here is what lets the worker's
// reprieve lapse on the next sweep if no verdict ever arrives.
func TestMidReviewExemptionIsWorkerOnly(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	ctx := context.Background()
	board, card, _ := specCard(t, pool, "a card with a full cast of sessions")

	reapSession(t, pool, board, card.ID, "worker", "ext-worker", "idle", "3 hours", "40 minutes")
	reapSession(t, pool, board, card.ID, "reviewer", "ext-reviewer", "idle", "90 minutes", "45 minutes")
	reapSession(t, pool, board, card.ID, "discuss", "ext-discuss", "idle", "5 hours", "3 hours")

	a.reapIdleSessions(ctx)

	if lc := lifecycleOf(t, pool, "ext-worker"); lc != "idle" {
		t.Fatalf("worker lifecycle = %q with its review outstanding, want idle", lc)
	}
	if lc := lifecycleOf(t, pool, "ext-reviewer"); lc != "stopped" {
		t.Fatalf("reviewer lifecycle = %q past 30 idle minutes, want stopped", lc)
	}
	if lc := lifecycleOf(t, pool, "ext-discuss"); lc != "stopped" {
		t.Fatalf("discuss lifecycle = %q past 2 idle hours, want stopped", lc)
	}
}

// The exemption is for the session the findings will actually reach, and
// maybeRelayFindings picks exactly one: the newest non-terminal author session.
// A card can carry more than one — reapQuietWorkers respawns after ten quiet
// minutes while the first worker is still merely idle — and the older ones are
// waiting on nothing. They pay ordinary rent.
func TestMidReviewExemptionSparesOnlyTheWorkerFindingsReach(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, _ := specCard(t, pool, "a card that outlived its first worker")

	reapSession(t, pool, board, card.ID, "worker", "ext-old-worker", "idle", "6 hours", "5 hours")
	reapSession(t, pool, board, card.ID, "worker", "ext-worker", "idle", "2 hours", "40 minutes")
	reapSession(t, pool, board, card.ID, "reviewer", "ext-reviewer", "running", "35 minutes", "4 minutes")

	a.reapIdleSessions(ctx)

	if lc := lifecycleOf(t, pool, "ext-worker"); lc != "idle" {
		t.Fatalf("newest worker lifecycle = %q with its review running, want idle", lc)
	}
	if lc := lifecycleOf(t, pool, "ext-old-worker"); lc != "stopped" {
		t.Fatalf("superseded worker lifecycle = %q, want stopped", lc)
	}
	if len(fake.stopped) != 1 || fake.stopped[0] != "ext-old-worker" {
		t.Fatalf("runner was asked to stop %v, want [ext-old-worker]", fake.stopped)
	}
}

// A card a human archived, or one the board has already flagged stuck, is not a
// card whose worker is about to be handed findings — whatever its reviewer
// session is doing. The clock would eventually collect these anyway; this says
// so at the moment the signal arrives instead of two hours later.
func TestMidReviewExemptionSkipsArchivedAndStuckCards(t *testing.T) {
	for _, tc := range []struct {
		name, set string
	}{
		{"archived", `UPDATE cards SET archived_at = now() WHERE id = $1`},
		{"stuck", `UPDATE cards SET stuck_at = now() WHERE id = $1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, pool, _ := specTestAPI(t)
			ctx := context.Background()
			board, card, _ := specCard(t, pool, "a card nobody is waiting on")

			reapSession(t, pool, board, card.ID, "worker", "ext-worker", "idle", "2 hours", "40 minutes")
			reapSession(t, pool, board, card.ID, "reviewer", "ext-reviewer", "running", "35 minutes", "4 minutes")
			if _, err := pool.Exec(ctx, tc.set, card.ID); err != nil {
				t.Fatal(err)
			}

			a.reapIdleSessions(ctx)

			if lc := lifecycleOf(t, pool, "ext-worker"); lc != "stopped" {
				t.Fatalf("worker lifecycle = %q on a %s card, want stopped", lc, tc.name)
			}
		})
	}
}

// The base rule, unchanged and worth pinning: no review has ever been asked
// for on this card, so nothing defers the 30-minute clock. A card-less
// standing session goes the same way — the exemption reads the card, and there
// isn't one.
func TestIdleSessionsWithNoReviewAreReapedAsBefore(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	ctx := context.Background()
	board, card, _ := specCard(t, pool, "a card that never reached review")

	reapSession(t, pool, board, card.ID, "worker", "ext-worker", "idle", "2 hours", "40 minutes")
	reapSession(t, pool, board, "", "board", "ext-board", "idle", "1 day", "3 hours")

	a.reapIdleSessions(ctx)

	if lc := lifecycleOf(t, pool, "ext-worker"); lc != "stopped" {
		t.Fatalf("worker lifecycle = %q with no review in sight, want stopped", lc)
	}
	if lc := lifecycleOf(t, pool, "ext-board"); lc != "stopped" {
		t.Fatalf("board-chat lifecycle = %q past 2 idle hours, want stopped", lc)
	}
}
