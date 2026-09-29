package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── reviewTarget: what a reviewer would be pointed at ────────────────────────

func TestReviewTargetPrefersPRLinkOverBody(t *testing.T) {
	body := "a perfectly good spec"
	card := db.Card{Body: &body, Links: []db.Link{
		{Kind: "doc", URL: "https://example.com/d.md"},
		{Kind: "pr", URL: "https://github.com/o/r/pull/9"},
		{Kind: "pr", URL: testPRURL},
	}}
	// reviewTarget answers the KIND of artifact and, for a PR card, a link to
	// start from; which pr link is THE one is mergeTargetPR's call, made
	// against GitHub by every caller that acts on it (buildReviewPrompt, the
	// auto-merge sweep, Accept). The last-link answer here is only the seed.
	kind, pr := reviewTarget(card)
	if kind != reviewPR || pr != testPRURL {
		t.Fatalf("reviewTarget = (%v, %q), want (reviewPR, %q)", kind, pr, testPRURL)
	}
}

// A discuss/brainstorm card never grows a link — its output is the body. That
// is the whole point of this trigger: no "pr" and no "doc" link required.
func TestReviewTargetFallsBackToBodyWithNoLinks(t *testing.T) {
	body := "## Design\n\nDo the thing, then the other thing."
	kind, pr := reviewTarget(db.Card{Body: &body})
	if kind != reviewSpec || pr != "" {
		t.Fatalf("reviewTarget = (%v, %q), want (reviewSpec, \"\")", kind, pr)
	}
}

func TestReviewTargetIsNothingWithoutPRorBody(t *testing.T) {
	blank := "   \n\t "
	for name, card := range map[string]db.Card{
		"nil body":              {},
		"empty body":            {Body: strPtrOf("")},
		"whitespace-only body":  {Body: &blank},
		"non-pr link, no body":  {Links: []db.Link{{Kind: "session", URL: "https://x/1"}}},
		"doc link but no body":  {Links: []db.Link{{Kind: "doc", URL: "https://x/d.md"}}},
		"nil body, empty links": {Links: []db.Link{}},
	} {
		if kind, _ := reviewTarget(card); kind != reviewNothing {
			t.Errorf("%s: reviewTarget = %v, want reviewNothing", name, kind)
		}
	}
}

// ── prompt selection ─────────────────────────────────────────────────────────

func TestBuildReviewPromptPicksSpecBriefForBodyOnlyCards(t *testing.T) {
	a := New(nil, nil, nil)
	body := "the spec text that must reach the reviewer"
	board := db.Board{Name: "blerg-board"}

	spec, err := a.buildReviewPrompt(context.Background(), board, db.Card{Number: 7, Title: "a spec", Body: &body})
	if err != nil {
		t.Fatalf("buildReviewPrompt(spec card): %v", err)
	}
	if !strings.Contains(spec, body) {
		t.Errorf("spec brief does not carry the card body:\n%s", spec)
	}
	if strings.Contains(spec, "Fetch and check out the PR branch") {
		t.Errorf("spec card got the PR-diff brief:\n%s", spec)
	}
	if !strings.Contains(spec, "no pull request") {
		t.Errorf("spec brief never tells the reviewer there is no PR:\n%s", spec)
	}

	pr, err := a.buildReviewPrompt(context.Background(), board, db.Card{Number: 7, Title: "a fix", Body: &body,
		Links: []db.Link{{Kind: "pr", URL: testPRURL}}})
	if err != nil {
		t.Fatalf("buildReviewPrompt(pr card): %v", err)
	}
	if !strings.Contains(pr, testPRURL) || !strings.Contains(pr, "Fetch and check out the PR branch") {
		t.Errorf("PR card did not get the PR-diff brief:\n%s", pr)
	}
}

// ── the spawn path, against a real database ──────────────────────────────────

// specFakeRunner records what maybeSpawnReviewer asks the runner to do. The
// mutex is for the paths that wake a worker from a goroutine (the Accept
// conflict bounce); tests that drive the runner synchronously may read the
// fields directly.
type specFakeRunner struct {
	mu         sync.Mutex
	started    []runner.StartRequest
	messaged   []string
	messagedTo []string
	stopped    []string // external session ids the API asked to stop
	modelSet   []string // "<session>=<model>" per SetModel call
	startErr   error    // when set, Start fails instead of succeeding
}

func (f *specFakeRunner) Name() string { return "fake" }
func (f *specFakeRunner) Start(_ context.Context, r runner.StartRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", f.startErr
	}
	f.started = append(f.started, r)
	return fmt.Sprintf("ext-%d", len(f.started)), nil
}
func (f *specFakeRunner) Status(_ context.Context, _ string) (runner.Status, error) {
	return runner.Status{Lifecycle: "running"}, nil
}
func (f *specFakeRunner) Message(_ context.Context, sessionID, text, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messaged = append(f.messaged, text)
	f.messagedTo = append(f.messagedTo, sessionID)
	return nil
}

// prompts/messages the runner has seen, copied under the lock.
func (f *specFakeRunner) seen() (starts []runner.StartRequest, msgs, msgTo []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runner.StartRequest(nil), f.started...),
		append([]string(nil), f.messaged...),
		append([]string(nil), f.messagedTo...)
}
func (f *specFakeRunner) Interrupt(_ context.Context, _ string) error { return nil }
func (f *specFakeRunner) Stop(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, sessionID)
	return nil
}
func (f *specFakeRunner) SetModel(_ context.Context, sessionID, model string) error {
	f.modelSet = append(f.modelSet, sessionID+"="+model)
	return nil
}
func (f *specFakeRunner) Events(_ context.Context, _ string, _ int64, _ int) ([]runner.Event, bool, error) {
	return nil, false, nil
}

// specTestAPI builds an API wired to the test database and a fake runner, so
// afterMove can be driven synchronously (production calls it in a goroutine).
func specTestAPI(t *testing.T) (*API, *pgxpool.Pool, *specFakeRunner) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE boards, tokens, admission_reviews, runner_capacity RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	a := New(pool, auth.New(pool, "svc-key-123"), nil)
	fake := &specFakeRunner{}
	a.SetRunner(RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})
	return a, pool, fake
}

// specCard creates a board + a card carrying body, and returns both plus the
// board's columns. The default column set is inbox/ready/in progress/done, so
// the review column the whole flow hangs off has to be added explicitly.
func specCard(t *testing.T, pool *pgxpool.Pool, body string) (db.Board, db.Card, []db.Column) {
	t.Helper()
	ctx := context.Background()
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateColumn(ctx, pool, board.ID, "review", false); err != nil {
		t.Fatal(err)
	}
	created, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtrOf("shape the thing"), Body: strPtrOf(body), Repos: &[]string{"blerg-board"},
	}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	return board, created.Card, cols
}

func specColumn(t *testing.T, cols []db.Column, want string) string {
	t.Helper()
	for _, c := range cols {
		if strings.Contains(strings.ToLower(c.Name), want) {
			return c.ID
		}
	}
	t.Fatalf("no column matching %q in %+v", want, cols)
	return ""
}

// The headline behaviour: a card with a body and NO link of any kind spawns
// an adversarial reviewer when it enters the review column.
func TestSpecCardEnteringReviewSpawnsReviewer(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "## Design\n\nA body long enough to be a real spec.")

	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)

	if len(fake.started) != 1 {
		t.Fatalf("spawned %d sessions for a body-only card in review, want 1", len(fake.started))
	}
	if !strings.Contains(fake.started[0].Prompt, "ADVERSARIAL REVIEWER") ||
		!strings.Contains(fake.started[0].Prompt, "A body long enough to be a real spec.") {
		t.Fatalf("reviewer did not get the spec brief with the body:\n%s", fake.started[0].Prompt)
	}
	var role string
	if err := pool.QueryRow(ctx,
		`SELECT role FROM runner_sessions WHERE card_id = $1`, card.ID).Scan(&role); err != nil {
		t.Fatalf("runner_sessions row: %v", err)
	}
	if role != "reviewer" {
		t.Fatalf("session role = %q, want reviewer", role)
	}
}

// A card with neither a PR nor a body has nothing to review — no session.
func TestBodylessCardEnteringReviewSpawnsNothing(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "")

	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)

	if len(fake.started) != 0 {
		t.Fatalf("spawned %d sessions for a card with nothing to review, want 0", len(fake.started))
	}
}

// The re-review debounce has to hold for spec cards too: once a verdict is
// posted and nothing has changed since, re-entering review must not spawn a
// second reviewer. Without counting card edits this is the case that used to
// be impossible to satisfy — a hand-written spec has no worker session at
// all, so the old "worker activity" query returned NULL.
func TestSpecReviewDebouncesWhenNothingChangedSinceVerdict(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a spec a human typed by hand")
	reviewID := specColumn(t, cols, "review")

	moved, err := db.MoveCard(ctx, pool, card.ID, reviewID, nil, nil, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)
	if len(fake.started) != 1 {
		t.Fatalf("first entry into review spawned %d sessions, want 1", len(fake.started))
	}

	// the reviewer verdicts, and its session settles
	if err := db.AppendComment(ctx, pool, card.ID,
		"Adversarial review\n\nno findings\n\nVerdict: approve", db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE runner_sessions SET lifecycle = 'stopped' WHERE card_id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}

	moved, err = db.GetCard(ctx, pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)
	if len(fake.started) != 1 {
		t.Fatalf("re-entering review with nothing changed spawned %d sessions, want 1", len(fake.started))
	}

	// now the body is actually revised — that must earn a fresh review
	if _, err := db.UpdateCard(ctx, pool, card.ID,
		db.CardParams{Body: strPtrOf("a spec a human revised by hand")},
		db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	moved, err = db.GetCard(ctx, pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)
	if len(fake.started) != 2 {
		t.Fatalf("re-entering review after a body edit spawned %d sessions total, want 2", len(fake.started))
	}
}

// Spawning a reviewer links the session onto the card — a service-actored
// "updated" event. If that counted as author activity the debounce would
// never hold and every sweep would spawn another reviewer.
func TestSpecReviewIgnoresItsOwnServiceEdits(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	ctx := context.Background()
	_, card, _ := specCard(t, pool, "a spec")

	if _, err := db.UpdateCard(ctx, pool, card.ID,
		db.CardParams{Links: &[]db.Link{{Kind: "session", URL: "https://runner/1"}}},
		db.EventMeta{Actor: "service"}); err != nil {
		t.Fatal(err)
	}
	if at := a.newestAuthorActivity(ctx, card.ID, reviewSpec); at != nil {
		t.Fatalf("newestAuthorActivity counted a service edit (%v) as author activity", at)
	}
	if _, err := db.UpdateCard(ctx, pool, card.ID,
		db.CardParams{Body: strPtrOf("revised")}, db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}
	if at := a.newestAuthorActivity(ctx, card.ID, reviewSpec); at == nil {
		t.Fatal("newestAuthorActivity = nil, want the agent's body edit")
	}
}

// A PR card's debounce is unchanged: card edits do not count as worker
// activity, so tagging or re-titling a card in review can't re-trigger a
// full code review.
func TestPRReviewDebounceIgnoresCardEdits(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	ctx := context.Background()
	_, card, _ := specCard(t, pool, "a spec")

	if _, err := db.UpdateCard(ctx, pool, card.ID,
		db.CardParams{Body: strPtrOf("revised")}, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	if at := a.newestAuthorActivity(ctx, card.ID, reviewPR); at != nil {
		t.Fatalf("newestAuthorActivity(reviewPR) = %v, want nil (no worker session ever ran)", at)
	}
}

// specLinkPR attaches a pr link to a card and returns the reloaded card.
func specLinkPR(t *testing.T, pool *pgxpool.Pool, cardID string) db.Card {
	t.Helper()
	card, err := db.UpdateCard(context.Background(), pool, cardID,
		db.CardParams{Links: &[]db.Link{{Kind: "pr", URL: testPRURL}}},
		db.EventMeta{Actor: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	return card
}

// On a PR card the findings must reach the WORKER even when a discuss session
// is newer — a discuss session cannot push the fix, and telling it to rewrite
// the card body over a code review loses the findings entirely. A human can
// open "Discuss" on any card at any time, and idle discuss sessions are reaped
// four times later than workers, so "newest live session" picks wrong often.
func TestFindingsRelayToWorkerNotNewerDiscussSessionOnPRCards(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a card that produced code")
	specLinkPR(t, pool, card.ID)

	if _, err := pool.Exec(ctx, `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role, created_at)
		VALUES ($1,$2,'fake','ext-worker','running','worker', now() - interval '1 hour'),
		       ($1,$2,'fake','ext-discuss','running','discuss', now())`, card.ID, board.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendComment(ctx, pool, card.ID,
		"Adversarial review\n\nthe diff drops an error\n\nVerdict: request-changes",
		db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}
	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "progress"), nil, nil,
		db.EventMeta{Actor: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)

	if len(fake.messaged) != 1 {
		t.Fatalf("relayed %d messages, want 1", len(fake.messaged))
	}
	if fake.messagedTo[0] != "ext-worker" {
		t.Fatalf("findings went to %q, want the worker session", fake.messagedTo[0])
	}
	if !strings.Contains(fake.messaged[0], "update the PR") {
		t.Errorf("worker did not get PR instructions:\n%s", fake.messaged[0])
	}
}

// request-changes on a spec card reaches the discuss session that wrote the
// body, with instructions it can actually follow — discuss sessions are told
// not to push code or move cards.
func TestFindingsRelayToDiscussSessionForSpecCards(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a spec")

	if _, err := pool.Exec(ctx, `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role)
		VALUES ($1,$2,'fake','ext-discuss','running','discuss')`, card.ID, board.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendComment(ctx, pool, card.ID,
		"Adversarial review\n\nthe body claims X but the code does Y\n\nVerdict: request-changes",
		db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}
	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "progress"), nil, nil,
		db.EventMeta{Actor: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)

	if len(fake.messaged) != 1 {
		t.Fatalf("relayed %d messages to the discuss session, want 1", len(fake.messaged))
	}
	msg := fake.messaged[0]
	if !strings.Contains(msg, "the body claims X but the code does Y") {
		t.Errorf("findings not relayed:\n%s", msg)
	}
	if strings.Contains(msg, "update the PR") {
		t.Errorf("discuss session told to update a PR it cannot push:\n%s", msg)
	}
	if !strings.Contains(msg, "revising the card body") {
		t.Errorf("discuss session not told to revise the body:\n%s", msg)
	}
}

// The stalled-review sweep may revive a spec review that already started, but
// must never cold-start one: entry into the review column is the only signal
// that a body is finished, so a backlog of no-PR cards parked in review can't
// stampede the runner.
func TestReviveStalledReviewsNeverColdStartsSpecReviews(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	_, card, cols := specCard(t, pool, "a spec parked in review since before this feature existed")

	if _, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	a.reviveStalledReviews(ctx)
	if len(fake.started) != 0 {
		t.Fatalf("the sweep cold-started %d spec reviews, want 0", len(fake.started))
	}
}

// ...but a spec review that DID start and then died must actually be revived.
// This is the case blerg-board's own breadcrumb used to block: the service comment
// "Adversarial review session spawned…" shares the "Adversarial review" prefix
// the debounce matched on, so spawning stamped a verdict newer than any author
// activity and the sweep bounced off it forever (for PR cards too).
func TestReviveStalledReviewsRespawnsADeadSpecReviewer(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a spec whose reviewer died mid-review")

	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)
	if len(fake.started) != 1 {
		t.Fatalf("first entry into review spawned %d sessions, want 1", len(fake.started))
	}

	// the reviewer pod dies without posting a verdict, and everything ages
	// past the quiet grace the sweep waits out
	if _, err := pool.Exec(ctx,
		`UPDATE runner_sessions SET lifecycle = 'error' WHERE card_id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE card_events SET created_at = created_at - interval '1 hour' WHERE card_id = $1`,
		card.ID); err != nil {
		t.Fatal(err)
	}

	a.reviveStalledReviews(ctx)
	if len(fake.started) != 2 {
		t.Fatalf("the sweep revived %d reviews for a dead reviewer, want 1 (total spawns %d, want 2)",
			len(fake.started)-1, len(fake.started))
	}
}

// The same breadcrumb bug on the PR path: a card whose reviewer died must be
// revivable, and the spawn comment must not read as a verdict.
func TestReviveStalledReviewsRespawnsADeadPRReviewer(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a card with code")
	specLinkPR(t, pool, card.ID)

	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)
	if len(fake.started) != 1 {
		t.Fatalf("first entry into review spawned %d sessions, want 1", len(fake.started))
	}
	if _, err := pool.Exec(ctx,
		`UPDATE runner_sessions SET lifecycle = 'error' WHERE card_id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE card_events SET created_at = created_at - interval '1 hour' WHERE card_id = $1`,
		card.ID); err != nil {
		t.Fatal(err)
	}

	a.reviveStalledReviews(ctx)
	if len(fake.started) != 2 {
		t.Fatalf("total spawns after reviving a dead PR reviewer = %d, want 2", len(fake.started))
	}
}

// specSweepWindow advances the clock past one workerQuietGrace window with the
// reviewer pod dead. Everything the review flow timestamps ages by the same
// interval — sessions as well as events — because only the distance from now()
// should change; the ORDER of a spawn relative to the comment that recorded it
// is what reviewRoundStart reads, and skewing the two would make the harness
// lie about which round a session belongs to.
func specSweepWindow(t *testing.T, pool *pgxpool.Pool, cardID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`UPDATE runner_sessions SET lifecycle = 'error' WHERE card_id = $1`, cardID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE card_events SET created_at = created_at - interval '1 hour' WHERE card_id = $1`,
		cardID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE runner_sessions
		SET created_at = created_at - interval '1 hour',
		    last_activity_at = last_activity_at - interval '1 hour'
		WHERE card_id = $1`, cardID); err != nil {
		t.Fatal(err)
	}
}

// Reviving stalled reviews must not become an unbounded respawn loop. A
// reviewer that dies every time gets reviewerAttemptCap tries, then the card
// is flagged stuck so a human sees it — the same contract reapQuietWorkers
// gives a worker that will not finish. Without the cap this is ~144 sessions
// a day, forever, with nothing surfaced.
func TestReviewerRespawnIsCappedAndFlagsStuck(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a spec whose reviewer keeps dying")

	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)

	for i := 0; i < 6; i++ { // six grace windows — an hour of ticking
		specSweepWindow(t, pool, card.ID)
		a.reviveStalledReviews(ctx)
	}

	if len(fake.started) != reviewerAttemptCap {
		t.Fatalf("spawned %d reviewers over 6 sweep windows, want the cap of %d",
			len(fake.started), reviewerAttemptCap)
	}
	var stuckAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuckAt); err != nil {
		t.Fatal(err)
	}
	if stuckAt == nil {
		t.Fatal("card was never flagged stuck — the loop gave up silently")
	}
	var notes int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM card_events WHERE card_id = $1 AND type = 'comment'
		  AND data->>'text' LIKE '%flagged stuck%'`, card.ID).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if notes != 1 {
		t.Fatalf("posted %d stuck comments, want exactly 1 (no repeat every sweep)", notes)
	}
}

// specCapOut drives a card to the reviewer cap and returns the spawn count.
func specCapOut(t *testing.T, a *API, pool *pgxpool.Pool, board db.Board, card db.Card, reviewCol string) {
	t.Helper()
	ctx := context.Background()
	moved, err := db.MoveCard(ctx, pool, card.ID, reviewCol, nil, nil, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)
	for i := 0; i < 5; i++ {
		specSweepWindow(t, pool, card.ID)
		a.reviveStalledReviews(ctx)
	}
	var stuckAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuckAt); err != nil {
		t.Fatal(err)
	}
	if stuckAt == nil {
		t.Fatal("setup: card never reached the cap")
	}
}

// The cap must not be a one-way door. Once it fires no reviewer can spawn, so
// no verdict can ever be written — so counting attempts since the last verdict
// alone would mean the card can never be reviewed again, by anyone, ever.
// Pressing Run is the platform's standard retry for a stuck card
// (handleStartRun clears stuck_at board-wide, and both the stuck chip and the
// worker stuck comment tell the human to use it), so it has to work here too.
func TestPressingRunReArmsTheReviewerCap(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a spec whose reviewers keep dying")
	specCapOut(t, a, pool, board, card, specColumn(t, cols, "review"))
	if len(fake.started) != reviewerAttemptCap {
		t.Fatalf("setup: %d spawns, want the cap of %d", len(fake.started), reviewerAttemptCap)
	}

	// exactly what handleStartRun does for the board
	if _, err := pool.Exec(ctx,
		`UPDATE cards SET stuck_at = NULL, run_attempts = 0 WHERE id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}
	specSweepWindow(t, pool, card.ID)
	a.reviveStalledReviews(ctx)

	if len(fake.started) != reviewerAttemptCap+1 {
		t.Fatalf("after pressing Run the sweep spawned %d reviewers total, want %d — "+
			"the cap never re-arms and the card is inert forever",
			len(fake.started), reviewerAttemptCap+1)
	}
}

// The other remedy, and the one the stuck comment names: move the card out of
// review and back. That must both re-arm the cap and clear the stuck flag —
// a card blerg-board is actively reviewing must not still read as stuck.
func TestMovingBackIntoReviewReArmsTheCapAndClearsStuck(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a spec whose reviewers keep dying")
	reviewCol, workCol := specColumn(t, cols, "review"), specColumn(t, cols, "progress")
	specCapOut(t, a, pool, board, card, reviewCol)

	if _, err := db.MoveCard(ctx, pool, card.ID, workCol, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	moved, err := db.MoveCard(ctx, pool, card.ID, reviewCol, nil, nil, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)

	if len(fake.started) != reviewerAttemptCap+1 {
		t.Fatalf("after moving out of review and back, %d spawns total, want %d — "+
			"the advice on the stuck comment does nothing", len(fake.started), reviewerAttemptCap+1)
	}
	var stuckAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuckAt); err != nil {
		t.Fatal(err)
	}
	if stuckAt != nil {
		t.Fatal("card still reads stuck while a reviewer is running on it")
	}
}

// Re-arming must not defeat the bound: a re-armed round gets a fresh budget of
// exactly reviewerAttemptCap, not an unlimited one.
func TestReArmedRoundIsItselfCapped(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a spec with a hopeless reviewer")
	specCapOut(t, a, pool, board, card, specColumn(t, cols, "review"))

	if _, err := pool.Exec(ctx, `UPDATE cards SET stuck_at = NULL WHERE id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		specSweepWindow(t, pool, card.ID)
		a.reviveStalledReviews(ctx)
	}
	if len(fake.started) != 2*reviewerAttemptCap {
		t.Fatalf("a re-armed round spawned %d reviewers in total, want %d "+
			"(one full budget, then stuck again)", len(fake.started), 2*reviewerAttemptCap)
	}
	var stuckAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuckAt); err != nil {
		t.Fatal(err)
	}
	if stuckAt == nil {
		t.Fatal("the re-armed round never flagged stuck again")
	}
}

// A verdict ends the round, so the next one starts with a full budget rather
// than inheriting a spent one.
func TestVerdictResetsTheReviewerAttemptBudget(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a spec on its second round")

	// a spent round: cap-many dead reviewers, all older than the verdict
	for i := 0; i < reviewerAttemptCap; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role, created_at)
			VALUES ($1,$2,'fake',$3,'error','reviewer', now() - interval '2 hours')`,
			card.ID, board.ID, fmt.Sprintf("ext-old-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AppendComment(ctx, pool, card.ID,
		"Adversarial review\n\nfindings\n\nVerdict: request-changes", db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}
	// the author then revises the body, so the debounce lets a new round start
	if _, err := db.UpdateCard(ctx, pool, card.ID,
		db.CardParams{Body: strPtrOf("a spec, revised")}, db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}
	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)

	if len(fake.started) != 1 {
		t.Fatalf("spawned %d reviewers for a fresh round, want 1 — the previous round's "+
			"attempts should not count against it", len(fake.started))
	}
}

// A spawn that fails writes no runner_sessions row, so it must write its own
// breadcrumb: that comment is the ONLY thing rate-limiting the sweep, and the
// board ticker runs every 20s. Capacity is transient — back off, don't flag.
func TestCapacityFailureBacksOffInsteadOfHammering(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a spec the runner has no room for")
	fake.startErr = errors.New("runner cap reached")

	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)

	var stuckAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuckAt); err != nil {
		t.Fatal(err)
	}
	if stuckAt != nil {
		t.Fatal("a capacity refusal flagged the card stuck — it is transient, retry later")
	}
	var breadcrumbs int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM card_events WHERE card_id = $1 AND type = 'comment'
		  AND data->>'text' LIKE 'Adversarial review%'`, card.ID).Scan(&breadcrumbs); err != nil {
		t.Fatal(err)
	}
	if breadcrumbs != 1 {
		t.Fatalf("failed spawn left %d review-flow comments, want 1 to arm the sweep's rate limit", breadcrumbs)
	}

	// three immediate ticks — the rate limit must hold them all off
	fake.startErr = nil
	for i := 0; i < 3; i++ {
		a.reviveStalledReviews(ctx)
	}
	if len(fake.started) != 0 {
		t.Fatalf("the sweep retried %d times inside one grace window, want 0", len(fake.started))
	}
}

// A non-capacity spawn failure is this card's problem, not a transient one:
// flag it stuck rather than retrying forever, matching dispatchBoard.
func TestNonCapacitySpawnFailureFlagsStuck(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a spec whose runner rejects it outright")
	fake.startErr = errors.New("image pull backoff")

	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)

	var stuckAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuckAt); err != nil {
		t.Fatal(err)
	}
	if stuckAt == nil {
		t.Fatal("a hard spawn failure left the card unflagged — it would retry forever")
	}
	// and a stuck card drops out of the sweep entirely
	specSweepWindow(t, pool, card.ID)
	fake.startErr = nil
	a.reviveStalledReviews(ctx)
	if len(fake.started) != 0 {
		t.Fatalf("the sweep picked up a stuck card (%d spawns), want 0", len(fake.started))
	}
}

// A real verdict still debounces: the narrowed pattern must not stop matching
// the thing it exists to match.
func TestRealVerdictStillBlocksRespawn(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a reviewed spec")

	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)
	if err := db.AppendComment(ctx, pool, card.ID,
		"Adversarial review\n\nchecked it\n\nVerdict: approve", db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE runner_sessions SET lifecycle = 'error' WHERE card_id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE card_events SET created_at = created_at - interval '1 hour' WHERE card_id = $1`,
		card.ID); err != nil {
		t.Fatal(err)
	}

	a.reviveStalledReviews(ctx)
	if len(fake.started) != 1 {
		t.Fatalf("a card with a fresh approve verdict was re-reviewed (%d spawns), want 1", len(fake.started))
	}
}

// The reason changedFields exists: a spec card the reviewer already approved,
// parked in review waiting for the human, must not buy a fresh review round
// because someone tagged it. Agents write the cards, humans curate them, so
// metadata edits on cards in review are the normal case.
func TestSpecReviewDebounceIgnoresMetadataEdits(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a spec a human typed by hand")

	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)
	if len(fake.started) != 1 {
		t.Fatalf("first entry into review spawned %d sessions, want 1", len(fake.started))
	}
	if err := db.AppendComment(ctx, pool, card.ID,
		"Adversarial review\n\nno findings\n\nVerdict: approve", db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE runner_sessions SET lifecycle = 'stopped' WHERE card_id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}

	// Every kind of curation a human does to an approved card in review.
	for _, edit := range []struct {
		what  string
		patch db.CardParams
	}{
		{"tagged", db.CardParams{Tags: &[]string{"design"}}},
		{"re-prioritised", db.CardParams{Priority: strPtrOf("high")}},
		{"sized", db.CardParams{Size: strPtrOf("L")}},
		{"retitled", db.CardParams{Title: strPtrOf("shape the thing, precisely")}},
		{"re-sent unchanged", db.CardParams{Body: strPtrOf("a spec a human typed by hand")}},
	} {
		if _, err := db.UpdateCard(ctx, pool, card.ID, edit.patch, db.EventMeta{Actor: "human"}); err != nil {
			t.Fatalf("%s: %v", edit.what, err)
		}
		cur, err := db.GetCard(ctx, pool, card.ID)
		if err != nil {
			t.Fatal(err)
		}
		a.afterMove(context.Background(), board, cur)
		if len(fake.started) != 1 {
			t.Fatalf("card %s while in review spawned a second reviewer (%d sessions total)",
				edit.what, len(fake.started))
		}
	}

	// The body itself changing is still author activity — that is the case
	// the debounce must NOT swallow.
	if _, err := db.UpdateCard(ctx, pool, card.ID,
		db.CardParams{Body: strPtrOf("a spec a human revised by hand")},
		db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	cur, err := db.GetCard(ctx, pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, cur)
	if len(fake.started) != 2 {
		t.Fatalf("a real body rewrite spawned %d sessions total, want 2", len(fake.started))
	}
}

// The same distinction at the unit the debounce actually reads.
func TestNewestAuthorActivityCountsBodyEditsOnly(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	ctx := context.Background()
	_, card, _ := specCard(t, pool, "a spec")

	for _, edit := range []struct {
		what  string
		patch db.CardParams
	}{
		{"a tag", db.CardParams{Tags: &[]string{"design"}}},
		{"a priority", db.CardParams{Priority: strPtrOf("high")}},
		{"a title", db.CardParams{Title: strPtrOf("a better title")}},
		{"a no-op body write", db.CardParams{Body: strPtrOf("a spec")}},
	} {
		if _, err := db.UpdateCard(ctx, pool, card.ID, edit.patch, db.EventMeta{Actor: "human"}); err != nil {
			t.Fatalf("%s: %v", edit.what, err)
		}
		if at := a.newestAuthorActivity(ctx, card.ID, reviewSpec); at != nil {
			t.Fatalf("%s counted as author activity (%v)", edit.what, at)
		}
	}

	if _, err := db.UpdateCard(ctx, pool, card.ID,
		db.CardParams{Body: strPtrOf("the spec, revised")}, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	if at := a.newestAuthorActivity(ctx, card.ID, reviewSpec); at == nil {
		t.Fatal("newestAuthorActivity = nil after a body rewrite, want the edit")
	}
}

// Events written before UpdateCard recorded a field list carry no list at
// all. They keep counting: a card already sitting in review must not lose the
// ability to be re-reviewed the moment this deploys.
func TestNewestAuthorActivityCountsPreFieldListEdits(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	ctx := context.Background()
	_, card, _ := specCard(t, pool, "a spec")

	if _, err := pool.Exec(ctx,
		`INSERT INTO card_events (card_id, type, actor, data) VALUES ($1,'updated','human','{}')`,
		card.ID); err != nil {
		t.Fatal(err)
	}
	if at := a.newestAuthorActivity(ctx, card.ID, reviewSpec); at == nil {
		t.Fatal("a pre-field-list 'updated' event stopped counting as author activity")
	}
}

// The flip side of excluding title, pinned so nobody has to rediscover it:
// once a verdict is current, a body edit is the ONLY thing that earns a fresh
// review. Moving the card out of review and back is not author activity, and
// neither is pressing Run — both land on the same debounce.
func TestMoveOutAndBackDoesNotReTriggerReviewAfterARetitle(t *testing.T) {
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()
	board, card, cols := specCard(t, pool, "a spec a human typed by hand")
	reviewID, workID := specColumn(t, cols, "review"), specColumn(t, cols, "progress")

	moved, err := db.MoveCard(ctx, pool, card.ID, reviewID, nil, nil, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, moved)
	if len(fake.started) != 1 {
		t.Fatalf("first entry into review spawned %d sessions, want 1", len(fake.started))
	}
	if err := db.AppendComment(ctx, pool, card.ID,
		"Adversarial review\n\nno findings\n\nVerdict: approve", db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE runner_sessions SET lifecycle = 'stopped' WHERE card_id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.UpdateCard(ctx, pool, card.ID,
		db.CardParams{Title: strPtrOf("shape the thing, precisely")},
		db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{workID, reviewID} {
		m, err := db.MoveCard(ctx, pool, card.ID, to, nil, nil, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		a.afterMove(context.Background(), board, m)
	}
	if len(fake.started) != 1 {
		t.Fatalf("retitle + move out of review and back spawned %d sessions, want 1 — "+
			"if this now passes at 2, the comment on newestAuthorActivity needs updating with it",
			len(fake.started))
	}

	// The recovery that does work.
	if _, err := db.UpdateCard(ctx, pool, card.ID,
		db.CardParams{Body: strPtrOf("a spec a human typed by hand, retitled and reworded")},
		db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	cur, err := db.GetCard(ctx, pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.afterMove(context.Background(), board, cur)
	if len(fake.started) != 2 {
		t.Fatalf("body edit after the retitle spawned %d sessions total, want 2", len(fake.started))
	}
}
