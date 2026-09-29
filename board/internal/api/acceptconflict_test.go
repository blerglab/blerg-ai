package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// mergeTestServer stubs the two GitHub endpoints mergePR calls: the PR (for
// the head ref) and the merge itself, which answers with the given status and
// message — the shape of a real refusal, `{"message": "..."}`.
func mergeTestServer(t *testing.T, status int, message string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/pulls/1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"head":{"ref":"card-1"}}`)
	})
	mux.HandleFunc("PUT /repos/o/r/pulls/1/merge", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"merged":false,"message":%q}`, message)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
}

// acceptCard puts a card with a PR link in the review column, ready for a
// human to press Accept on. auto_merge is turned OFF: autoMergeSweep selects
// on `WHERE c.auto_merge`, so for these cards Accept is the only merge path,
// and therefore the only conflict recovery they can ever get.
func acceptCard(t *testing.T, pool *pgxpool.Pool) (db.Card, []db.Column) {
	t.Helper()
	ctx := context.Background()
	_, card, cols := specCard(t, pool, "a card whose PR is about to conflict")
	links := []db.Link{{Kind: "pr", URL: testPRURL}}
	if _, err := db.UpdateCard(ctx, pool, card.ID, db.CardParams{Links: &links},
		db.EventMeta{Actor: "service"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE cards SET auto_merge = false WHERE id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}
	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if moved.AutoMerge {
		t.Fatalf("card #%d still has auto_merge — the fixture is meant to be Accept-only", moved.Number)
	}
	return moved, cols
}

// pressAccept runs handleAcceptCard as a human would reach it.
func pressAccept(t *testing.T, a *API, card db.Card) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost, "/api/cards/"+card.ID+"/accept", nil)
	req.SetPathValue("id", card.ID)
	rec := httptest.NewRecorder()
	// The native service key: admin everywhere (IsNativeService). This test
	// is about merge-conflict handling, not authorization.
	a.handleAcceptCard(rec, req, auth.Principal{Kind: auth.KindService})
	return rec
}

func cardColumnName(t *testing.T, cols []db.Column, card db.Card) string {
	t.Helper()
	if card.ColumnID == nil {
		return ""
	}
	for _, c := range cols {
		if c.ID == *card.ColumnID {
			return c.Name
		}
	}
	return "unknown"
}

// waitFor polls until cond holds — the worker wake runs in a goroutine so the
// Accept response doesn't wait on a pod start.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The headline behaviour: Accept on a PR that conflicts with main is not a
// failure. The card goes back to the work column, a worker is put on the
// conflict, and the human is told that in words — never "HTTP 405".
func TestAcceptOnConflictBouncesCardToWorker(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	mergeTestServer(t, http.StatusMethodNotAllowed, "Pull Request has merge conflicts")
	card, cols := acceptCard(t, pool)

	rec := pressAccept(t, a, card)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("Accept on a conflicted PR = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body %q: %v", rec.Body.String(), err)
	}
	if body["status"] != "bounced" {
		t.Errorf("status = %q, want bounced (body %v)", body["status"], body)
	}
	if low := strings.ToLower(rec.Body.String()); strings.Contains(low, "405") ||
		strings.Contains(low, "github") {
		t.Errorf("raw GitHub error reached the UI: %s", rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(body["note"]), "conflict") {
		t.Errorf("note does not say what happened: %q", body["note"])
	}

	after, err := db.GetCard(context.Background(), pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if name := cardColumnName(t, cols, after); !isWorkColumn(name) {
		t.Errorf("card landed in %q, want the work column", name)
	}
	if after.RunAttempts != card.RunAttempts+1 {
		t.Errorf("run_attempts = %d, want %d", after.RunAttempts, card.RunAttempts+1)
	}
	if after.MergedSHA != nil {
		t.Errorf("nothing merged, but merged_sha = %v", *after.MergedSHA)
	}

	var bounce string
	for _, c := range cardComments(t, a, card.ID) {
		if strings.Contains(c, "merge conflicts with main") {
			bounce = c
		}
		if strings.Contains(c, "Accepted by human review") {
			t.Errorf("card claims it was accepted: %q", c)
		}
	}
	if bounce == "" {
		t.Fatalf("no comment explaining the bounce: %v", cardComments(t, a, card.ID))
	}
	if strings.Contains(strings.ToLower(bounce), "auto-merge hit") {
		t.Errorf("human Accept blamed auto-merge: %q", bounce)
	}

	// no live worker session on this card, so the bounce spawns one
	waitFor(t, "a worker session on the conflict", func() bool {
		starts, _, _ := fake.seen()
		return len(starts) == 1
	})
	starts, _, _ := fake.seen()
	if !strings.Contains(starts[0].Prompt, "merge conflicts with main") ||
		!strings.Contains(starts[0].Prompt, testPRURL) {
		t.Errorf("spawned worker was not told to resolve the conflict:\n%s", starts[0].Prompt)
	}
	if strings.Contains(starts[0].Prompt, "auto-merge is waiting on you") {
		t.Errorf("worker was told auto-merge is waiting, but a human pressed Accept:\n%s", starts[0].Prompt)
	}
}

// A live worker gets a message instead of a second session.
func TestAcceptOnConflictMessagesTheLiveWorker(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	mergeTestServer(t, http.StatusMethodNotAllowed, "Pull Request has merge conflicts")
	card, _ := acceptCard(t, pool)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role, last_activity_at)
		VALUES ($1, $2, 'fake', 'ext-live', 'running', 'worker', now())`,
		card.ID, card.BoardID); err != nil {
		t.Fatal(err)
	}

	if rec := pressAccept(t, a, card); rec.Code != http.StatusAccepted {
		t.Fatalf("Accept = %d, want 202: %s", rec.Code, rec.Body.String())
	}

	waitFor(t, "the live worker to be messaged", func() bool {
		_, msgs, _ := fake.seen()
		return len(msgs) == 1
	})
	starts, msgs, msgTo := fake.seen()
	if len(starts) != 0 {
		t.Errorf("spawned %d sessions despite a live worker", len(starts))
	}
	if msgTo[0] != "ext-live" || !strings.Contains(msgs[0], "resolve every conflict") {
		t.Errorf("message to %q: %q", msgTo[0], msgs[0])
	}
}

// Attempts are finite: the fourth conflict on the same card stops bouncing and
// says so, without moving the card or spending another attempt.
func TestAcceptStopsBouncingOnceAttemptsAreSpent(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	mergeTestServer(t, http.StatusMethodNotAllowed, "Pull Request has merge conflicts")
	card, cols := acceptCard(t, pool)
	if _, err := pool.Exec(context.Background(),
		`UPDATE cards SET run_attempts = $2 WHERE id = $1`, card.ID, conflictBounceLimit); err != nil {
		t.Fatal(err)
	}

	rec := pressAccept(t, a, card)

	if rec.Code != http.StatusConflict {
		t.Fatalf("exhausted bounce = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if low := strings.ToLower(rec.Body.String()); strings.Contains(low, "405") ||
		strings.Contains(low, "github") {
		t.Errorf("raw GitHub error reached the UI: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "conflicts with main") {
		t.Errorf("error does not say what happened: %s", rec.Body.String())
	}
	// run_attempts counts every worker run — dispatches and quiet-worker
	// respawns as much as conflict bounces — so the message may claim a spent
	// budget but must not claim that many conflicts were ever resolved.
	if low := strings.ToLower(rec.Body.String()); strings.Contains(low, "resolve attempts") ||
		strings.Contains(low, "attempts have already failed") {
		t.Errorf("409 asserts a conflict count run_attempts does not measure: %s", rec.Body.String())
	}

	after, err := db.GetCard(context.Background(), pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if name := cardColumnName(t, cols, after); !isReviewColumn(name) {
		t.Errorf("card moved to %q; a card nobody can fix stays in review", name)
	}
	if after.RunAttempts != conflictBounceLimit {
		t.Errorf("run_attempts = %d, want %d — a refused bounce costs nothing",
			after.RunAttempts, conflictBounceLimit)
	}
	if starts, msgs, _ := fake.seen(); len(starts) != 0 || len(msgs) != 0 {
		t.Errorf("woke a worker anyway: %d starts, %d messages", len(starts), len(msgs))
	}
}

// A merge failure that isn't a conflict is still a plain 502 with the reason.
func TestAcceptNonConflictMergeFailureStillReturns502(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	mergeTestServer(t, http.StatusInternalServerError, "Server Error")
	card, cols := acceptCard(t, pool)

	rec := pressAccept(t, a, card)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("non-conflict merge failure = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "merge failed") ||
		!strings.Contains(rec.Body.String(), "Server Error") {
		t.Errorf("502 lost the reason: %s", rec.Body.String())
	}
	after, err := db.GetCard(context.Background(), pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if name := cardColumnName(t, cols, after); !isReviewColumn(name) {
		t.Errorf("card moved to %q on a non-conflict failure", name)
	}
	if starts, msgs, _ := fake.seen(); len(starts) != 0 || len(msgs) != 0 {
		t.Errorf("woke a worker on a non-conflict failure: %d starts, %d messages", len(starts), len(msgs))
	}
}

// A board with no column isWorkColumn recognises has nowhere to bounce to.
// Better a truthful 502 than "sent it back" with nothing sent back.
func TestAcceptOnConflictWithNoWorkColumnFallsBackToTheError(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	mergeTestServer(t, http.StatusMethodNotAllowed, "Pull Request has merge conflicts")
	card, _ := acceptCard(t, pool)
	// rename the one column isWorkColumn would have matched
	if _, err := pool.Exec(context.Background(),
		`UPDATE board_columns SET name = 'building' WHERE board_id = $1 AND lower(name) LIKE '%progress%'`,
		card.BoardID); err != nil {
		t.Fatal(err)
	}

	rec := pressAccept(t, a, card)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unbounceable conflict = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	cols, err := db.ListColumns(context.Background(), pool, card.BoardID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := db.GetCard(context.Background(), pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if name := cardColumnName(t, cols, after); !isReviewColumn(name) {
		t.Errorf("card moved to %q with no work column to move to", name)
	}
	if after.RunAttempts != card.RunAttempts {
		t.Errorf("run_attempts = %d, want %d — the bounce never happened", after.RunAttempts, card.RunAttempts)
	}
	for _, c := range cardComments(t, a, card.ID) {
		if strings.Contains(c, "went back to the worker") {
			t.Errorf("card claims a bounce that did not happen: %q", c)
		}
	}
	if starts, msgs, _ := fake.seen(); len(starts) != 0 || len(msgs) != 0 {
		t.Errorf("woke a worker with nowhere to bounce to: %d starts, %d messages", len(starts), len(msgs))
	}
}

// A card with a PR link but no repo has nothing for a fresh session to be
// cloned into. spawnSession indexes Repos[0], and Accept wakes the worker from
// a detached goroutine — where net/http cannot recover a panic, so the whole
// process would go down on a button press. Bounce refused, plain error, alive.
func TestAcceptOnConflictWithNoRepoDoesNotSpawn(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	mergeTestServer(t, http.StatusMethodNotAllowed, "Pull Request has merge conflicts")
	card, cols := acceptCard(t, pool)
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM card_repos WHERE card_id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}
	card, err := db.GetCard(context.Background(), pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}

	rec := pressAccept(t, a, card)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("conflict on a repo-less card = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	after, err := db.GetCard(context.Background(), pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if name := cardColumnName(t, cols, after); !isReviewColumn(name) {
		t.Errorf("card moved to %q with no repo to work it in", name)
	}
	// give the goroutine the wake would have run a moment to not exist
	time.Sleep(100 * time.Millisecond)
	if starts, msgs, _ := fake.seen(); len(starts) != 0 || len(msgs) != 0 {
		t.Errorf("woke a worker for a repo-less card: %d starts, %d messages", len(starts), len(msgs))
	}
}

// The same guard, exercised where the panic would actually be thrown —
// spawnSession indexes Repos[0]. Called synchronously, so this test IS the
// goroutine: an unguarded index takes the test binary down rather than racing
// the pool teardown, which is exactly what it would do to the server.
func TestWakeWorkerForConflictSurvivesACardWithNoRepo(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	card, _ := acceptCard(t, pool)
	if _, err := pool.Exec(ctx, `DELETE FROM card_repos WHERE card_id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}
	card, err := db.GetCard(ctx, pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	board, err := db.GetBoard(ctx, pool, card.BoardID)
	if err != nil {
		t.Fatal(err)
	}

	a.wakeWorkerForConflict(ctx, board, card, testPRURL, conflictHumanAccept)

	if starts, _, _ := fake.seen(); len(starts) != 0 {
		t.Errorf("started %d sessions for a card with no repo to clone", len(starts))
	}
	var noted bool
	for _, c := range cardComments(t, a, card.ID) {
		if strings.Contains(c, "no worker could be started") && strings.Contains(c, "no repo") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("nothing on the card says no worker was started: %v", cardComments(t, a, card.ID))
	}
}

// ...but a repo-less card whose worker is still alive can be told about the
// conflict without spawning anything, so it still bounces. The repo guard is
// about the spawn, not about the bounce.
func TestAcceptOnConflictWithNoRepoStillMessagesALiveWorker(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	mergeTestServer(t, http.StatusMethodNotAllowed, "Pull Request has merge conflicts")
	card, cols := acceptCard(t, pool)
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM card_repos WHERE card_id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role, last_activity_at)
		VALUES ($1, $2, 'fake', 'ext-live', 'running', 'worker', now())`,
		card.ID, card.BoardID); err != nil {
		t.Fatal(err)
	}
	card, err := db.GetCard(context.Background(), pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}

	if rec := pressAccept(t, a, card); rec.Code != http.StatusAccepted {
		t.Fatalf("conflict with a live worker = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "the live worker to be messaged", func() bool {
		_, msgs, _ := fake.seen()
		return len(msgs) == 1
	})
	if starts, _, _ := fake.seen(); len(starts) != 0 {
		t.Errorf("spawned %d sessions for a repo-less card", len(starts))
	}
	after, err := db.GetCard(context.Background(), pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if name := cardColumnName(t, cols, after); !isWorkColumn(name) {
		t.Errorf("card landed in %q, want the work column", name)
	}
}

// The bounce moved the card and told the human a worker is on it. When the
// spawn then fails, the card has to say so — otherwise it waits in the work
// column on a session that never started.
func TestBounceRecordsThatNoWorkerCouldBeStarted(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	mergeTestServer(t, http.StatusMethodNotAllowed, "Pull Request has merge conflicts")
	card, _ := acceptCard(t, pool)
	fake.startErr = fmt.Errorf("no capacity")

	if rec := pressAccept(t, a, card); rec.Code != http.StatusAccepted {
		t.Fatalf("Accept = %d, want 202: %s", rec.Code, rec.Body.String())
	}

	waitFor(t, "the card to record the failed wake", func() bool {
		for _, c := range cardComments(t, a, card.ID) {
			if strings.Contains(c, "no worker could be started") {
				return true
			}
		}
		return false
	})
	var note string
	for _, c := range cardComments(t, a, card.ID) {
		if strings.Contains(c, "no worker could be started") {
			note = c
		}
	}
	if !strings.Contains(note, "no capacity") {
		t.Errorf("failed wake does not say why: %q", note)
	}
}

// The exhausted-attempts 409 explains a recovery this board has; a board with
// no work column never had one, so a spent budget is not the story to tell —
// the honest answer there is still the merge failure itself.
func TestAcceptWithAttemptsSpentAndNoWorkColumnStillReturnsTheError(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	mergeTestServer(t, http.StatusMethodNotAllowed, "Pull Request has merge conflicts")
	card, _ := acceptCard(t, pool)
	if _, err := pool.Exec(context.Background(),
		`UPDATE cards SET run_attempts = $2 WHERE id = $1`, card.ID, conflictBounceLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE board_columns SET name = 'building' WHERE board_id = $1 AND lower(name) LIKE '%progress%'`,
		card.BoardID); err != nil {
		t.Fatal(err)
	}

	rec := pressAccept(t, a, card)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unbounceable conflict with attempts spent = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "retry budget") {
		t.Errorf("blamed a spent budget on a board that could never bounce: %s", rec.Body.String())
	}
}

// Without a runner there is no worker to hand the conflict to, so a bounce
// would move the card out of review and buy nothing. Say what went wrong
// instead of promising a worker that cannot exist.
func TestAcceptOnConflictWithNoRunnerDoesNotClaimAWorker(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	mergeTestServer(t, http.StatusMethodNotAllowed, "Pull Request has merge conflicts")
	card, cols := acceptCard(t, pool)
	a.runner = nil

	rec := pressAccept(t, a, card)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("conflict with no runner = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "worker is on the conflict") {
		t.Errorf("promised a worker on a runner-less deployment: %s", rec.Body.String())
	}
	after, err := db.GetCard(context.Background(), pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if name := cardColumnName(t, cols, after); !isReviewColumn(name) {
		t.Errorf("card moved to %q with no runner to work it", name)
	}
	if after.RunAttempts != card.RunAttempts {
		t.Errorf("run_attempts = %d, want %d — no bounce happened", after.RunAttempts, card.RunAttempts)
	}
}

// ── the same recovery from the sweep's side ─────────────────────────────────

// sweepConflictCard is sweepCard against a GitHub that reports green CI and
// then refuses the merge with the 405 the field reported — the exact state
// autoMergeSweep's conflict branch acts on.
func sweepConflictCard(t *testing.T) (*API, *specFakeRunner, db.Card, []db.Column) {
	t.Helper()
	a, card := sweepCard(t)
	fake, ok := a.runner.Driver.(*specFakeRunner)
	if !ok {
		t.Fatalf("test API is not wired to the fake runner: %T", a.runner.Driver)
	}
	mux := ciMux(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[{"status":"completed","conclusion":"success"}]}`)
	}, nil)
	mux.HandleFunc("PUT /repos/o/r/pulls/1/merge", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = fmt.Fprint(w, `{"merged":false,"message":"Pull Request has merge conflicts"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	cols, err := db.ListColumns(context.Background(), a.Pool, card.BoardID)
	if err != nil {
		t.Fatal(err)
	}
	return a, fake, card, cols
}

// The sweep's own conflict path, which the Accept work refactored underneath:
// bounce, keep auto_merge armed, and don't file the card as a failure.
func TestAutoMergeSweepBouncesAConflictedPRToTheWorker(t *testing.T) {
	a, fake, card, cols := sweepConflictCard(t)

	a.autoMergeSweep(context.Background())

	after, err := db.GetCard(context.Background(), a.Pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if name := cardColumnName(t, cols, after); !isWorkColumn(name) {
		t.Errorf("card landed in %q, want the work column", name)
	}
	if after.RunAttempts != card.RunAttempts+1 {
		t.Errorf("run_attempts = %d, want %d", after.RunAttempts, card.RunAttempts+1)
	}
	if !after.AutoMerge {
		t.Error("auto_merge cleared by a conflict the sweep recovered from")
	}
	comments := cardComments(t, a, card.ID)
	if countWithPrefix(comments, "Auto-merge failed:") != 0 {
		t.Errorf("bounced card filed as a failure: %v", comments)
	}
	var bounced bool
	for _, c := range comments {
		if strings.Contains(c, "Auto-merge hit merge conflicts with main") {
			bounced = true
		}
	}
	if !bounced {
		t.Errorf("no comment explaining the bounce: %v", comments)
	}
	starts, _, _ := fake.seen()
	if len(starts) != 1 {
		t.Fatalf("spawned %d workers on the conflict, want 1", len(starts))
	}
	if !strings.Contains(starts[0].Prompt, "auto-merge is waiting on you") {
		t.Errorf("sweep-spawned worker was not told who is waiting:\n%s", starts[0].Prompt)
	}
}

// The sweep now learns when a bounce didn't happen. A board with no work
// column can't recover, so the card is filed as a failure and auto-merge is
// disarmed — instead of the old silent retry every 20 seconds, forever.
func TestAutoMergeSweepOnAnUnbounceableConflictStopsRetrying(t *testing.T) {
	a, fake, card, _ := sweepConflictCard(t)
	if _, err := a.Pool.Exec(context.Background(),
		`UPDATE board_columns SET name = 'building' WHERE board_id = $1 AND lower(name) LIKE '%progress%'`,
		card.BoardID); err != nil {
		t.Fatal(err)
	}

	a.autoMergeSweep(context.Background())

	after, err := db.GetCard(context.Background(), a.Pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(context.Background(), a.Pool, card.BoardID)
	if err != nil {
		t.Fatal(err)
	}
	if name := cardColumnName(t, cols, after); !isReviewColumn(name) {
		t.Errorf("card moved to %q with no work column to move to", name)
	}
	if after.AutoMerge {
		t.Error("auto_merge still armed on a conflict nothing can recover — the sweep will retry forever")
	}
	if after.RunAttempts != card.RunAttempts {
		t.Errorf("run_attempts = %d, want %d — the bounce never happened", after.RunAttempts, card.RunAttempts)
	}
	comments := cardComments(t, a, card.ID)
	if countWithPrefix(comments, "Auto-merge failed:") != 1 {
		t.Errorf("failure not reported once: %v", comments)
	}
	if starts, msgs, _ := fake.seen(); len(starts) != 0 || len(msgs) != 0 {
		t.Errorf("woke a worker with nowhere to bounce to: %d starts, %d messages", len(starts), len(msgs))
	}
}

// blockCardMoves makes the database refuse to move any card, the way a
// transient error would, and puts it back afterwards. Fault injection at the
// one seam the bounce cannot control.
func blockCardMoves(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION blerg_board_test_block_move() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'transient move failure'; END $$;
		CREATE TRIGGER blerg_board_test_block_move BEFORE UPDATE OF column_id ON cards
		FOR EACH ROW EXECUTE FUNCTION blerg_board_test_block_move();`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			DROP TRIGGER IF EXISTS blerg_board_test_block_move ON cards;
			DROP FUNCTION IF EXISTS blerg_board_test_block_move();`)
	})
}

// A bounce that merely lost this round is not a card that can never be
// recovered. The sweep used to retry a failed bounce every 20 seconds; it must
// still do that rather than disarm auto-merge over one database blip.
func TestAutoMergeSweepRetriesAfterATransientBounceFailure(t *testing.T) {
	a, fake, card, cols := sweepConflictCard(t)
	blockCardMoves(t, a.Pool)

	a.autoMergeSweep(context.Background())

	after, err := db.GetCard(context.Background(), a.Pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.AutoMerge {
		t.Error("auto_merge disarmed by a transient failure — the card lost its recovery to a database blip")
	}
	if name := cardColumnName(t, cols, after); !isReviewColumn(name) {
		t.Errorf("card moved to %q despite the move failing", name)
	}
	if after.RunAttempts != card.RunAttempts {
		t.Errorf("run_attempts = %d, want %d — the move never happened", after.RunAttempts, card.RunAttempts)
	}
	comments := cardComments(t, a, card.ID)
	if countWithPrefix(comments, "Auto-merge failed:") != 0 {
		t.Errorf("transient failure reported as a dead end: %v", comments)
	}
	if starts, msgs, _ := fake.seen(); len(starts) != 0 || len(msgs) != 0 {
		t.Errorf("woke a worker for a card that never moved: %d starts, %d messages", len(starts), len(msgs))
	}
}

// The same failure, classified: "the board has nowhere to bounce to" and "this
// attempt errored" are different answers, and only the first is permanent.
func TestBounceCardToWorkSeparatesCannotFromDidNot(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	card, _ := acceptCard(t, pool)

	blockCardMoves(t, pool)
	if _, _, res := a.bounceCardToWork(context.Background(), card, conflictHumanAccept); res != bounceFailed {
		t.Errorf("a database error classified as %v, want bounceFailed", res)
	}
}

// Both merge paths share conflictBounceLimit, so the sweep gives up at the
// same point Accept does.
func TestAutoMergeSweepStopsBouncingOnceAttemptsAreSpent(t *testing.T) {
	a, fake, card, _ := sweepConflictCard(t)
	if _, err := a.Pool.Exec(context.Background(),
		`UPDATE cards SET run_attempts = $2 WHERE id = $1`, card.ID, conflictBounceLimit); err != nil {
		t.Fatal(err)
	}

	a.autoMergeSweep(context.Background())

	after, err := db.GetCard(context.Background(), a.Pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AutoMerge {
		t.Error("auto_merge still armed after the last bounce was refused")
	}
	if after.RunAttempts != conflictBounceLimit {
		t.Errorf("run_attempts = %d, want %d — a refused bounce costs nothing",
			after.RunAttempts, conflictBounceLimit)
	}
	if countWithPrefix(cardComments(t, a, card.ID), "Auto-merge failed:") != 1 {
		t.Errorf("exhausted card not handed to a human: %v", cardComments(t, a, card.ID))
	}
	if starts, msgs, _ := fake.seen(); len(starts) != 0 || len(msgs) != 0 {
		t.Errorf("woke a worker anyway: %d starts, %d messages", len(starts), len(msgs))
	}
}

// The two merge paths say different things on the card, and only the sweep's
// version mentions auto-merge.
func TestConflictSourceWordingSeparatesTheTwoMergePaths(t *testing.T) {
	if conflictAutoMerge.actor() != "service" || conflictHumanAccept.actor() != "human" {
		t.Errorf("actors = %q / %q", conflictAutoMerge.actor(), conflictHumanAccept.actor())
	}
	if !strings.Contains(conflictAutoMerge.comment(), "Auto-merge") ||
		strings.Contains(conflictHumanAccept.comment(), "Auto-merge") {
		t.Errorf("comments = %q / %q", conflictAutoMerge.comment(), conflictHumanAccept.comment())
	}
	if !strings.Contains(conflictAutoMerge.waitingOn(), "auto-merge") ||
		!strings.Contains(conflictHumanAccept.waitingOn(), "Accept") {
		t.Errorf("worker instructions = %q / %q",
			conflictAutoMerge.waitingOn(), conflictHumanAccept.waitingOn())
	}
}

// isConflictErr has to keep matching the string the field actually reported.
func TestIsConflictErrMatchesTheReportedGitHubRefusal(t *testing.T) {
	if !isConflictErr(fmt.Errorf("github: HTTP 405: Pull Request has merge conflicts")) {
		t.Error("the reported 405 is not recognised as a conflict")
	}
	if isConflictErr(fmt.Errorf("github: HTTP 500: Server Error")) {
		t.Error("a server error is being treated as a conflict")
	}
}
