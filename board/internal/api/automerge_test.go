package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// ── the block table, no database needed ──────────────────────────────────────

// redReport is a red read of head deadbeef, aged by however long ago its
// checks finished — the input blockCIRedFor writes its note from.
func redReport(finishedAgo time.Duration, failed ...string) ciReport {
	return ciReport{
		state:   ciRed,
		headSHA: "deadbeef1234567",
		failed:  failed,
		newest:  time.Now().Add(-finishedAgo),
	}
}

// Every block comment must OPEN with the prefix that rate-limits it. If the
// two drift apart the LIKE never matches, the limit never arms, and the sweep
// posts the same comment onto the card every 20 seconds.
func TestCIBlockTextStartsWithItsRateLimitPrefix(t *testing.T) {
	blocks := []ciBlock{blockCIRed, blockCIPending, blockCINone, blockCIError(fmt.Errorf("boom")),
		blockNoOpenPR, blockPRStateError(fmt.Errorf("boom")),
		blockCIRedFor(redReport(time.Minute, "go"), 0),
		blockCIRedFor(redReport(2*time.Hour, "go"), 1),
		blockCIRedFor(redReport(2*time.Hour, "go", "web"), maxCIReruns)}
	for _, b := range blocks {
		if !strings.HasPrefix(b.text, b.prefix) {
			t.Errorf("comment %q does not start with its prefix %q", b.text, b.prefix)
		}
		if b.why == "" || b.fix == "" || b.grace <= 0 {
			t.Errorf("block %q needs a why, a fix and a grace, got why=%q fix=%q grace=%v",
				b.prefix, b.why, b.fix, b.grace)
		}
	}
}

// Pressing Run clears stuck_at board-wide but changes nothing about a repo
// that has no CI workflow, so a stuck note telling that human to press Run is
// telling them to re-raise the same flag twenty seconds later.
func TestNoCIStuckNoteDoesNotRecommendRun(t *testing.T) {
	if strings.Contains(strings.ToLower(blockCINone.fix), "run") {
		t.Errorf("the no-CI remedy sends the human to a button that can't help: %q", blockCINone.fix)
	}
	for _, want := range []string{"workflow", "Accept"} {
		if !strings.Contains(blockCINone.fix, want) {
			t.Errorf("the no-CI remedy should mention %q, got %q", want, blockCINone.fix)
		}
	}
}

// Distinct prefixes are what let a card whose reason CHANGES speak up on the
// next sweep instead of waiting out the previous reason's window. A prefix
// that is a prefix of another would silently swallow it.
func TestCIBlockPrefixesAreMutuallyDistinct(t *testing.T) {
	blocks := []ciBlock{blockCIRed, blockCIPending, blockCINone, blockCIError(fmt.Errorf("boom")),
		blockNoOpenPR, blockPRStateError(fmt.Errorf("boom"))}
	for i, a := range blocks {
		for j, b := range blocks {
			if i != j && strings.HasPrefix(b.prefix, a.prefix) {
				t.Errorf("prefix %q swallows %q — one reason would suppress the other", a.prefix, b.prefix)
			}
		}
	}
	// and none of them collides with the stuck note, which must always post
	for _, b := range blocks {
		if strings.HasPrefix("Auto-merge stuck:", b.prefix) { //nolint:gocritic // argOrder: deliberate; asks whether the block prefix is a prefix of the stuck note
			t.Errorf("prefix %q swallows the stuck note", b.prefix)
		}
	}
}

func TestCIBlockForMapsEveryNonGreenState(t *testing.T) {
	for _, tc := range []struct {
		ci   ciState
		want ciBlock
	}{
		{ciRed, blockCIRed},
		{ciNone, blockCINone},
		{ciPending, blockCIPending},
	} {
		got := ciBlockFor(ciReport{state: tc.ci}, 0)
		if !strings.HasPrefix(got.prefix, tc.want.prefix) {
			t.Errorf("ciBlockFor(%v).prefix = %q, want it under %q", tc.ci, got.prefix, tc.want.prefix)
		}
	}
}

// ── what a red note actually says ───────────────────────────────────────────

// "CI is failing" sends the author to GitHub to find out what the board
// already knows. The note has to name the job.
func TestRedNoteNamesTheFailingChecks(t *testing.T) {
	b := blockCIRedFor(redReport(time.Minute, "go", "golangci-lint (timed out)"), 0)
	for _, want := range []string{"go", "golangci-lint (timed out)"} {
		if !strings.Contains(b.text, want) {
			t.Errorf("red note does not name %q: %q", want, b.text)
		}
		if !strings.Contains(b.why, want) {
			t.Errorf("the stuck note's reason does not name %q: %q", want, b.why)
		}
	}
}

// The card's second ask: a red that was decided hours ago and re-read ever
// since must not read like a failure that just happened.
func TestRedNoteDistinguishesAStaleRedFromAFreshOne(t *testing.T) {
	fresh := blockCIRedFor(redReport(2*time.Minute, "go"), 0).text
	stale := blockCIRedFor(redReport(3*time.Hour, "go"), 0).text
	if strings.Contains(fresh, "nothing has changed") {
		t.Errorf("a two-minute-old failure was described as stale: %q", fresh)
	}
	if !strings.Contains(stale, "nothing has changed on this head since") {
		t.Errorf("a three-hour-old failure was not described as stale: %q", stale)
	}
	if !strings.Contains(stale, "3h") {
		t.Errorf("the stale note does not date the failure: %q", stale)
	}
}

// The prefix is the rate limit. Scoping it to the head SHA is what lets a
// pushed-then-red-again card speak up instead of being swallowed by the
// previous head's 30-minute window — but it must still extend the base
// prefix, or the ledger of "how many times have we said this" loses its
// history and the escalation never fires.
func TestRedNotePrefixIsPerHeadAndExtendsTheBase(t *testing.T) {
	a := blockCIRedFor(ciReport{state: ciRed, headSHA: "aaaaaaa1111", failed: []string{"go"}}, 0)
	b := blockCIRedFor(ciReport{state: ciRed, headSHA: "bbbbbbb2222", failed: []string{"go"}}, 0)
	if a.prefix == b.prefix {
		t.Fatalf("two different heads share the prefix %q — the second red would be silenced", a.prefix)
	}
	for _, got := range []ciBlock{a, b} {
		if !strings.HasPrefix(got.prefix, blockCIRed.prefix) {
			t.Errorf("prefix %q does not extend the base %q", got.prefix, blockCIRed.prefix)
		}
	}
	if !strings.Contains(a.prefix, "aaaaaaa") {
		t.Errorf("prefix %q does not name its head", a.prefix)
	}
}

// Once the budget is spent, the advice changes: "push a fix" is finally the
// right thing to say, and the note has to explain that a retry was tried.
func TestRedNoteChangesOnceTheRerunsAreSpent(t *testing.T) {
	spent := blockCIRedFor(redReport(time.Hour, "go"), maxCIReruns)
	if !strings.Contains(spent.text, "unlikely to be a flake") {
		t.Errorf("an exhausted retry budget reads like the first red: %q", spent.text)
	}
	if !strings.Contains(spent.fix, "stayed red") {
		t.Errorf("the stuck remedy does not mention the re-runs already tried: %q", spent.fix)
	}
	// ...and a note written when nothing could be re-run must not claim one.
	none := blockCIRedFor(redReport(time.Minute, "buildkite/test"), 0)
	if strings.Contains(none.text, "asked GitHub to re-run") {
		t.Errorf("a note with no re-run behind it claims one: %q", none.text)
	}
}

// A wide matrix fails in dozens of places at once. Naming the job saves the
// reader a trip to GitHub; naming ninety of them doesn't.
func TestJoinChecksReadsAsASentenceAndStaysShort(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{nil, "the PR head's checks"},
		{[]string{"go"}, "go"},
		{[]string{"go", "web"}, "go and web"},
		{[]string{"a", "b", "c", "d"}, "a, b, c and d"},
		{[]string{"a", "b", "c", "d", "e"}, "a, b, c, d and 1 more"},
		{[]string{"a", "b", "c", "d", "e", "f", "g"}, "a, b, c, d and 3 more"},
	} {
		if got := joinChecks(tc.in); got != tc.want {
			t.Errorf("joinChecks(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTimes(t *testing.T) {
	for in, want := range map[int]string{1: "once", 2: "twice", 3: "3 times", 7: "7 times"} {
		if got := times(in); got != want {
			t.Errorf("times(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestOrdinal(t *testing.T) {
	for in, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th",
		12: "12th", 13: "13th", 21: "21st", 22: "22nd", 23: "23rd", 100: "100th"} {
		if got := ordinal(in); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanAgo(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "moments"},
		{4 * time.Minute, "4m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "1h0m"},
		{7*time.Hour + 3*time.Minute, "7h3m"},
	} {
		if got := humanAgo(tc.in); got != tc.want {
			t.Errorf("humanAgo(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHumanDur(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{time.Hour, "1h"},
		{2 * time.Hour, "2h"},
		{10 * time.Minute, "10m"},
		{90 * time.Minute, "90m"},
		{90 * time.Second, "1m30s"},
	} {
		if got := humanDur(tc.in); got != tc.want {
			t.Errorf("humanDur(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ── the sweep, against the test database ────────────────────────────────────

// approveAge back-dates the approval comment so a test can reach a grace
// period without sleeping through it.
func approveAge(t *testing.T, a *API, cardID string, age time.Duration) {
	t.Helper()
	if _, err := a.Pool.Exec(context.Background(), `
		UPDATE card_events SET created_at = now() - $2::interval
		WHERE card_id = $1 AND type = 'comment' AND data->>'text' LIKE 'Adversarial review%'`,
		cardID, age.String()); err != nil {
		t.Fatal(err)
	}
}

// sweepCard sets up a board, a review column, and a card carrying auto_merge,
// a PR link and a fresh "Verdict: approve" — the state autoMergeSweep acts on.
func sweepCard(t *testing.T) (*API, db.Card) {
	t.Helper()
	return sweepCardLinked(t, testPRURL)
}

// sweepCardLinked is sweepCard with the card's `pr` links spelled out, IN
// ORDER. They go in through db.UpdateCard, which writes card_links.rank as the
// slice index, and db.GetCard reads them back ORDER BY rank — so the argument
// order here is exactly the order the sweep sees, which is what lets a test
// pin behaviour to link ordering rather than to link content.
func sweepCardLinked(t *testing.T, prURLs ...string) (*API, db.Card) {
	t.Helper()
	a, pool, _ := specTestAPI(t)
	ctx := context.Background()

	_, card, cols := specCard(t, pool, "the work")
	links := make([]db.Link, 0, len(prURLs))
	for _, u := range prURLs {
		links = append(links, db.Link{Kind: "pr", URL: u})
	}
	if _, err := db.UpdateCard(ctx, pool, card.ID, db.CardParams{Links: &links},
		db.EventMeta{Actor: "service"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE cards SET auto_merge = true WHERE id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendComment(ctx, pool, card.ID,
		"Adversarial review of the diff.\n\nVerdict: approve", db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}
	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	return a, moved
}

// cardComments returns every comment on the card, oldest first.
func cardComments(t *testing.T, a *API, cardID string) []string {
	t.Helper()
	rows, err := a.Pool.Query(context.Background(), `
		SELECT data->>'text' FROM card_events
		WHERE card_id = $1 AND type = 'comment' ORDER BY created_at, id`, cardID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

func countWithPrefix(comments []string, prefix string) int {
	n := 0
	for _, c := range comments {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// The headline case from the bug: a PR on a repo with no CI at all. The card
// used to get nothing — the sweep skipped it every 20s, forever, silently.
func TestAutoMergeSweepReportsARepoWithNoChecks(t *testing.T) {
	a, card := sweepCard(t)
	ciTestServer(t, nil, nil) // no check-runs, no commit statuses: no CI wired up

	a.autoMergeSweep(context.Background())

	comments := cardComments(t, a, card.ID)
	if n := countWithPrefix(comments, blockCINone.prefix); n != 1 {
		t.Fatalf("want exactly one %q comment, got %d in %q", blockCINone.prefix, n, comments)
	}
	if countWithPrefix(comments, blockCIPending.prefix) != 0 {
		t.Fatalf("no-checks was reported as still-running: %q", comments)
	}
}

// The acceptance criterion's second half: a repeat sweep must not spam.
func TestAutoMergeSweepDoesNotRepeatTheSameReason(t *testing.T) {
	a, card := sweepCard(t)
	ciTestServer(t, nil, nil)

	for i := 0; i < 5; i++ {
		a.autoMergeSweep(context.Background())
	}

	if n := countWithPrefix(cardComments(t, a, card.ID), blockCINone.prefix); n != 1 {
		t.Fatalf("five sweeps posted %d no-checks comments, want 1", n)
	}
}

// Checks that are genuinely running get their own, different sentence — the
// distinction the card asks for, because the remedies differ (wait vs. add a
// workflow).
func TestAutoMergeSweepReportsRunningChecksDistinctly(t *testing.T) {
	a, card := sweepCard(t)
	ciTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[{"status":"in_progress","conclusion":""}]}`)
	}, nil)
	approveAge(t, a, card.ID, blockCIPending.quiet+time.Minute)

	a.autoMergeSweep(context.Background())

	comments := cardComments(t, a, card.ID)
	if n := countWithPrefix(comments, blockCIPending.prefix); n != 1 {
		t.Fatalf("want exactly one %q comment, got %d in %q", blockCIPending.prefix, n, comments)
	}
	if countWithPrefix(comments, blockCINone.prefix) != 0 {
		t.Fatalf("running checks were reported as no-CI: %q", comments)
	}
}

// The ordinary case stays quiet: checks that started seconds ago are the
// system working, and a card that narrates them on the way to a clean merge
// teaches humans to skip auto-merge comments — which is how the signal this
// change adds would get lost.
func TestAutoMergeSweepStaysQuietWhileChecksAreFresh(t *testing.T) {
	a, card := sweepCard(t)
	ciTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[{"status":"in_progress","conclusion":""}]}`)
	}, nil) // approval is seconds old — inside blockCIPending.quiet

	a.autoMergeSweep(context.Background())

	for _, c := range cardComments(t, a, card.ID) {
		if strings.HasPrefix(c, "Auto-merge") {
			t.Fatalf("a CI run that just started got narrated: %q", c)
		}
	}
}

// A repo with no CI is terminal, so it is said at once — no quiet window to
// wait out. This is the acceptance criterion's first half.
func TestAutoMergeSweepReportsNoChecksImmediately(t *testing.T) {
	a, card := sweepCard(t)
	ciTestServer(t, nil, nil)

	a.autoMergeSweep(context.Background()) // approval seconds old

	if n := countWithPrefix(cardComments(t, a, card.ID), blockCINone.prefix); n != 1 {
		t.Fatalf("terminal no-CI state was held back, got %d comments", n)
	}
}

// Rate limiting is per reason, not per card: a card whose CI goes from running
// to failing must say so on the next sweep, not wait out the first reason's
// window.
func TestAutoMergeSweepSpeaksUpWhenTheReasonChanges(t *testing.T) {
	a, card := sweepCard(t)
	approveAge(t, a, card.ID, blockCIPending.quiet+time.Minute)

	running := httptest.NewServer(ciMux(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[{"status":"in_progress","conclusion":""}]}`)
	}, nil))
	t.Cleanup(running.Close)
	failed := httptest.NewServer(ciMux(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[{"status":"completed","conclusion":"failure"}]}`)
	}, nil))
	t.Cleanup(failed.Close)
	old := githubAPIBase
	t.Cleanup(func() { githubAPIBase = old })

	githubAPIBase = running.URL
	a.autoMergeSweep(context.Background())
	githubAPIBase = failed.URL
	a.autoMergeSweep(context.Background())

	comments := cardComments(t, a, card.ID)
	if n := countWithPrefix(comments, blockCIPending.prefix); n != 1 {
		t.Fatalf("want one running note, got %d in %q", n, comments)
	}
	if n := countWithPrefix(comments, blockCIRed.prefix); n != 1 {
		t.Fatalf("the switch to red was swallowed by the running note's window: %q", comments)
	}
}

// ghFailing points the GitHub client at a server that fails every request.
func ghFailing(t *testing.T, code int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
}

// errorHeldFor back-dates the card's run of CI-status errors, so a test can
// reach the error block's quiet/grace without sweeping for two real minutes.
func errorHeldFor(a *API, cardID string, d time.Duration) {
	a.ciErrMu.Lock()
	defer a.ciErrMu.Unlock()
	if a.ciErrSince == nil {
		a.ciErrSince = map[string]time.Time{}
	}
	a.ciErrSince[cardID] = time.Now().Add(-d)
}

// A GitHub API failure used to reach log.Printf and nowhere else — e039de9
// made the CI read return the error; this puts it on the card, once the error
// has actually held.
func TestAutoMergeSweepReportsAnUnreadableCIStatus(t *testing.T) {
	a, card := sweepCard(t)
	ghFailing(t, http.StatusForbidden, `{"message":"API rate limit exceeded"}`)
	errorHeldFor(a, card.ID, blockCIError(fmt.Errorf("x")).quiet+time.Minute)

	a.autoMergeSweep(context.Background())

	comments := cardComments(t, a, card.ID)
	if n := countWithPrefix(comments, ciErrorPrefix); n != 1 {
		t.Fatalf("want exactly one CI-error comment, got %d in %q", n, comments)
	}
	var note string
	for _, c := range comments {
		if strings.HasPrefix(c, ciErrorPrefix) {
			note = c
		}
	}
	if !strings.Contains(note, "403") {
		t.Fatalf("comment %q does not name the underlying GitHub failure", note)
	}
}

// The error block's clock is the ERROR's age, not the approval's. A single
// 502 on a card that has been merge-eligible for an hour is a blip — GitHub
// 5xx and secondary rate limits are routine — and must not reach the card.
func TestAutoMergeSweepIgnoresASingleCIStatusBlip(t *testing.T) {
	a, card := sweepCard(t)
	ghFailing(t, http.StatusBadGateway, `{"message":"Server Error"}`)
	approveAge(t, a, card.ID, autoMergeBlockedGrace+time.Minute) // long-eligible card

	a.autoMergeSweep(context.Background()) // first errored sweep

	for _, c := range cardComments(t, a, card.ID) {
		if strings.HasPrefix(c, "Auto-merge") {
			t.Fatalf("one transient 502 was reported as a jam: %q", c)
		}
	}
	var stuck *time.Time
	if err := a.Pool.QueryRow(context.Background(),
		`SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuck); err != nil {
		t.Fatal(err)
	}
	if stuck != nil {
		t.Fatal("one transient 502 flagged the card stuck")
	}
}

// ...and an error that genuinely persists does get the flag, timed from the
// error rather than from the approval.
func TestAutoMergeSweepFlagsStuckOnSustainedCIStatusErrors(t *testing.T) {
	a, card := sweepCard(t)
	ghFailing(t, http.StatusInternalServerError, `{"message":"Server Error"}`)
	errorHeldFor(a, card.ID, autoMergeBlockedGrace+time.Minute)

	a.autoMergeSweep(context.Background())

	var stuck *time.Time
	if err := a.Pool.QueryRow(context.Background(),
		`SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuck); err != nil {
		t.Fatal(err)
	}
	if stuck == nil {
		t.Fatalf("an error held for over %v did not flag the card stuck", autoMergeBlockedGrace)
	}
}

// A sweep that gets an answer out of GitHub ends the run of errors, so the
// next failure starts counting from zero rather than inheriting the old run.
func TestAutoMergeSweepResetsTheErrorClockWhenGitHubAnswers(t *testing.T) {
	a, card := sweepCard(t)
	errorHeldFor(a, card.ID, time.Hour)
	ciTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[{"status":"in_progress","conclusion":""}]}`)
	}, nil)

	a.autoMergeSweep(context.Background())

	a.ciErrMu.Lock()
	_, still := a.ciErrSince[card.ID]
	a.ciErrMu.Unlock()
	if still {
		t.Fatal("GitHub answered, but the card kept its old error clock")
	}
}

// Cards that leave the sweep — merged, moved out, auto_merge cleared — must
// not leave their error clock behind in the map.
func TestSweepForgetsErrorClocksForCardsItNoLongerSees(t *testing.T) {
	a, card := sweepCard(t)
	errorHeldFor(a, "11111111-1111-1111-1111-111111111111", time.Hour) // a card long gone
	errorHeldFor(a, card.ID, time.Hour)
	ciTestServer(t, nil, nil)

	a.autoMergeSweep(context.Background())

	a.ciErrMu.Lock()
	defer a.ciErrMu.Unlock()
	if _, ok := a.ciErrSince["11111111-1111-1111-1111-111111111111"]; ok {
		t.Fatal("a card the sweep no longer sees kept its error clock")
	}
}

// Pressing Run clears stuck_at board-wide, and for a repo with no CI the very
// next sweep re-earns the flag. The flag must come back — that is what Run
// means — but the note must not be appended again every press.
func TestAutoMergeSweepDoesNotRepeatTheStuckNoteAfterRun(t *testing.T) {
	a, card := sweepCard(t)
	ciTestServer(t, nil, nil)
	approveAge(t, a, card.ID, autoMergeNoCIGrace+time.Minute)

	a.autoMergeSweep(context.Background()) // explains + flags stuck
	// exactly what pressing Run does (handleStartRun)
	if _, err := a.Pool.Exec(context.Background(),
		`UPDATE cards SET stuck_at = NULL WHERE id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}
	a.autoMergeSweep(context.Background())

	var stuck *time.Time
	if err := a.Pool.QueryRow(context.Background(),
		`SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuck); err != nil {
		t.Fatal(err)
	}
	if stuck == nil {
		t.Fatal("the flag did not come back after Run — the jam is real and still unresolved")
	}
	if n := countWithPrefix(cardComments(t, a, card.ID), "Auto-merge stuck:"); n != 1 {
		t.Fatalf("Run appended a duplicate stuck note: %d, want 1", n)
	}
}

// A card flagged stuck for something else entirely (a dead reviewer, a quiet
// worker) still needs to be told about the auto-merge jam once, or it carries
// a chip that explains the wrong thing while the real jam stays invisible.
func TestAutoMergeSweepExplainsEvenWhenAlreadyStuckForAnotherReason(t *testing.T) {
	a, card := sweepCard(t)
	ciTestServer(t, nil, nil)
	if _, err := a.Pool.Exec(context.Background(),
		`UPDATE cards SET stuck_at = now() WHERE id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}

	a.autoMergeSweep(context.Background())
	a.autoMergeSweep(context.Background()) // and still only once

	if n := countWithPrefix(cardComments(t, a, card.ID), blockCINone.prefix); n != 1 {
		t.Fatalf("want exactly one explanation on an already-stuck card, got %d", n)
	}
}

// A jam that clears is a jam that's over: the merge must not carry a stuck
// chip into the done column.
func TestAutoMergeClearsTheStuckFlagOnSuccess(t *testing.T) {
	a, card := sweepCard(t)
	mux := ciMux(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[{"status":"completed","conclusion":"success"}]}`)
	}, nil)
	mux.HandleFunc("/repos/o/r/pulls/1/merge", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"sha":"cafebabe1234567","merged":true}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
	if _, err := a.Pool.Exec(context.Background(),
		`UPDATE cards SET stuck_at = now() WHERE id = $1`, card.ID); err != nil {
		t.Fatal(err)
	}

	a.autoMergeSweep(context.Background())

	var stuck *time.Time
	var sha *string
	if err := a.Pool.QueryRow(context.Background(),
		`SELECT stuck_at, merged_sha FROM cards WHERE id = $1`, card.ID).Scan(&stuck, &sha); err != nil {
		t.Fatal(err)
	}
	if sha == nil || *sha != "cafebabe1234567" {
		t.Fatalf("card did not merge: merged_sha = %v", sha)
	}
	if stuck != nil {
		t.Fatalf("the merged card carried its stuck flag to done (%v)", stuck)
	}
}

// Past the grace period the board itself has to show the jam, not just the
// card's comment log: stuck_at is what lights the ⏸ chip in BoardView.
func TestAutoMergeSweepFlagsStuckPastTheGrace(t *testing.T) {
	a, card := sweepCard(t)
	ciTestServer(t, nil, nil)
	approveAge(t, a, card.ID, autoMergeNoCIGrace+time.Minute)

	a.autoMergeSweep(context.Background())

	var stuck *time.Time
	if err := a.Pool.QueryRow(context.Background(),
		`SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuck); err != nil {
		t.Fatal(err)
	}
	if stuck == nil {
		t.Fatalf("card blocked past %v is not flagged stuck", autoMergeNoCIGrace)
	}
	if n := countWithPrefix(cardComments(t, a, card.ID), "Auto-merge stuck:"); n != 1 {
		t.Fatalf("want one stuck note, got %d", n)
	}
}

// Inside the grace the card gets its explanation but no stuck flag — a run
// that is genuinely still going must not be branded a jam.
func TestAutoMergeSweepDoesNotFlagStuckInsideTheGrace(t *testing.T) {
	a, card := sweepCard(t)
	ciTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[{"status":"queued","conclusion":""}]}`)
	}, nil)
	approveAge(t, a, card.ID, blockCIPending.quiet+time.Minute)

	a.autoMergeSweep(context.Background())

	var stuck *time.Time
	if err := a.Pool.QueryRow(context.Background(),
		`SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuck); err != nil {
		t.Fatal(err)
	}
	if stuck != nil {
		t.Fatalf("card blocked for a minute was flagged stuck at %v", stuck)
	}
	if n := countWithPrefix(cardComments(t, a, card.ID), blockCIPending.prefix); n != 1 {
		t.Fatalf("want the waiting explanation anyway, got %d", n)
	}
}

// Once flagged AND explained, the reason stops repeating: the chip is up, the
// card has been told why, and a note every 30 minutes is noise to scroll past.
func TestAutoMergeSweepStopsNarratingOnceStuck(t *testing.T) {
	a, card := sweepCard(t)
	ciTestServer(t, nil, nil)
	approveAge(t, a, card.ID, autoMergeNoCIGrace+time.Minute)

	a.autoMergeSweep(context.Background()) // explains, then flags stuck
	// wind the block comment out of its rate-limit window; only the stuck flag
	// should keep it quiet now
	if _, err := a.Pool.Exec(context.Background(), `
		UPDATE card_events SET created_at = now() - interval '2 hours'
		WHERE card_id = $1 AND type = 'comment'`, card.ID); err != nil {
		t.Fatal(err)
	}
	a.autoMergeSweep(context.Background())

	if n := countWithPrefix(cardComments(t, a, card.ID), blockCINone.prefix); n != 1 {
		t.Fatalf("stuck card kept narrating: %d no-checks comments, want 1", n)
	}
}

// ── recovering from a flake ─────────────────────────────────────────────────

// ciRerunServer stands up a GitHub whose head is red and whose failed jobs can
// be re-run. greenOnRerun is the whole distinction the card is about: true is
// a flake (the re-run reports green), false is a genuinely broken PR (it stays
// red however many times you ask). It counts the re-run calls so a test can
// assert the budget is actually bounded.
type ciRerunServer struct {
	mu         sync.Mutex
	reruns     int
	conclusion string
	merged     bool
}

func newCIRerunServer(t *testing.T, greenOnRerun bool) *ciRerunServer {
	t.Helper()
	s := &ciRerunServer{conclusion: "failure"}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"head":{"sha":"deadbeef","ref":"wip/x"}}`)
	})
	mux.HandleFunc("/repos/o/r/commits/deadbeef/check-runs", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"check_runs":[{"name":"go","status":"completed","conclusion":%q,
			"completed_at":"2026-08-06T04:46:44Z"}]}`, s.conclusion)
	})
	mux.HandleFunc("/repos/o/r/commits/deadbeef/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"state":"pending","total_count":0}`)
	})
	mux.HandleFunc("/repos/o/r/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.conclusion != "failure" {
			_, _ = fmt.Fprint(w, `{"workflow_runs":[{"id":7,"name":"CI","status":"completed","conclusion":"success"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"workflow_runs":[{"id":7,"name":"CI","status":"completed","conclusion":"failure"}]}`)
	})
	mux.HandleFunc("/repos/o/r/actions/runs/7/rerun-failed-jobs", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.reruns++
		if greenOnRerun {
			s.conclusion = "success"
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/repos/o/r/pulls/1/merge", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.merged = true
		s.mu.Unlock()
		_, _ = fmt.Fprint(w, `{"sha":"merged00000000","merged":true}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
	return s
}

func (s *ciRerunServer) rerunCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reruns
}

// settleReruns back-dates the re-run ledger so the next sweep is allowed to
// spend the next attempt without waiting out ciRerunSettle for real. Both
// halves of the interval: the durable ledger of re-runs that happened, and the
// in-memory clock on the asking.
func settleReruns(t *testing.T, a *API, cardID string) {
	t.Helper()
	if _, err := a.Pool.Exec(context.Background(), `
		UPDATE card_events SET created_at = now() - $2::interval
		WHERE card_id = $1 AND type = 'comment' AND data->>'text' LIKE $3`,
		cardID, (ciRerunSettle + time.Minute).String(), ciRerunPrefix+"%"); err != nil {
		t.Fatal(err)
	}
	a.ciRerunMu.Lock()
	delete(a.ciRerunLast, cardID)
	a.ciRerunMu.Unlock()
}

// The acceptance criterion's first half: a red that goes green on a re-run
// reaches merged with nobody touching it.
func TestAutoMergeSweepRecoversFromAFlakeByRerunning(t *testing.T) {
	a, card := sweepCard(t)
	gh := newCIRerunServer(t, true)

	a.autoMergeSweep(context.Background()) // red: asks for the re-run
	a.autoMergeSweep(context.Background()) // re-run came back green: merges

	if gh.rerunCount() != 1 {
		t.Fatalf("asked GitHub for %d re-runs, want 1", gh.rerunCount())
	}
	var sha *string
	if err := a.Pool.QueryRow(context.Background(),
		`SELECT merged_sha FROM cards WHERE id = $1`, card.ID).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	if sha == nil {
		t.Fatalf("a flake that cleared on re-run did not merge: %q", cardComments(t, a, card.ID))
	}
	comments := cardComments(t, a, card.ID)
	if n := countWithPrefix(comments, ciRerunPrefix); n != 1 {
		t.Fatalf("want one re-run note, got %d in %q", n, comments)
	}
	// and it must not have nagged the author to fix a diff that was fine
	if n := countWithPrefix(comments, blockCIRed.prefix); n != 0 {
		t.Fatalf("a flake produced %d push-a-fix notes: %q", n, comments)
	}
}

// The re-run note names the head and the failing job, so a human reading the
// card knows what was retried without opening GitHub.
func TestRerunNoteNamesTheHeadAndTheFailingJob(t *testing.T) {
	a, card := sweepCard(t)
	newCIRerunServer(t, true)

	a.autoMergeSweep(context.Background())

	var note string
	for _, c := range cardComments(t, a, card.ID) {
		if strings.HasPrefix(c, ciRerunPrefix) {
			note = c
		}
	}
	if note == "" {
		t.Fatalf("no re-run note on the card: %q", cardComments(t, a, card.ID))
	}
	for _, want := range []string{"deadbee", "go", "CI", "attempt 1 of 2"} {
		if !strings.Contains(note, want) {
			t.Errorf("re-run note does not mention %q: %q", want, note)
		}
	}
}

// Bounded, and bounded per head SHA: a genuinely broken PR must not be re-run
// in a loop, and the ledger has to be the card's own comments so a restart
// doesn't hand it a fresh budget.
func TestAutoMergeSweepBoundsRerunsPerHead(t *testing.T) {
	a, card := sweepCard(t)
	gh := newCIRerunServer(t, false) // stays red however often you ask

	for i := 0; i < 6; i++ {
		a.autoMergeSweep(context.Background())
		settleReruns(t, a, card.ID) // let the settle window elapse each round
	}

	if got := gh.rerunCount(); got != maxCIReruns {
		t.Fatalf("re-ran a permanently red head %d times, want %d", got, maxCIReruns)
	}
	if n := countWithPrefix(cardComments(t, a, card.ID), ciRerunPrefix); n != maxCIReruns {
		t.Fatalf("want %d re-run notes on the card, got %d", maxCIReruns, n)
	}
}

// Consecutive 20s sweeps must not burn the budget before GitHub has even
// re-queued the jobs — the check-runs API keeps serving the old failure for a
// moment after the re-run is accepted.
func TestAutoMergeSweepDoesNotSpendTheBudgetInOneMinute(t *testing.T) {
	a, _ := sweepCard(t)
	gh := newCIRerunServer(t, false)

	for i := 0; i < 4; i++ {
		a.autoMergeSweep(context.Background())
	}

	if got := gh.rerunCount(); got != 1 {
		t.Fatalf("four back-to-back sweeps spent %d re-runs, want 1", got)
	}
}

// An ask that re-queues nothing writes no ledger entry, so nothing in the
// database remembers it happened. Without the in-memory half of the interval
// the sweep would ask GitHub again every twenty seconds, for as long as the
// card sat in review — a red with no Actions run behind it (an external CI's
// commit status) never stops looking like a fresh candidate for a retry.
func TestAutoMergeSweepStopsAskingWhenNothingCanBeRerun(t *testing.T) {
	a, card := sweepCard(t)
	var mu sync.Mutex
	asked := 0
	srv := httptest.NewServer(ciMux(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `{"check_runs":[{"name":"buildkite","status":"completed",
				"conclusion":"failure","completed_at":"2026-08-06T04:46:44Z"}]}`)
		}, nil,
		func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			asked++
			mu.Unlock()
			_, _ = fmt.Fprint(w, `{"workflow_runs":[]}`) // nothing here can be re-run
		}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	for i := 0; i < 5; i++ {
		a.autoMergeSweep(context.Background())
	}

	mu.Lock()
	got := asked
	mu.Unlock()
	if got != 1 {
		t.Fatalf("five sweeps asked GitHub for this head's runs %d times, want 1", got)
	}
	// ...and the card still gets told, since nothing was done about the red.
	if n := countWithPrefix(cardComments(t, a, card.ID), blockCIRed.prefix); n != 1 {
		t.Fatalf("declining to re-ask also silenced the red note: %q", cardComments(t, a, card.ID))
	}
}

// The ask's rate limit, on its own: one per head per settle window, reset by a
// push rather than by waiting, and forgotten when the card leaves the sweep.
func TestMayAttemptRerunRateLimitsTheAskPerHead(t *testing.T) {
	a := &API{}
	const card = "11111111-1111-1111-1111-111111111111"

	if !a.mayAttemptRerun(card, "deadbeef") {
		t.Fatal("the first ask on a head was refused")
	}
	if a.mayAttemptRerun(card, "deadbeef") {
		t.Error("a second ask inside the settle window was allowed")
	}
	// A push is a different question, and deserves an answer now rather than
	// the tail of the previous head's wait.
	if !a.mayAttemptRerun(card, "cafe1234") {
		t.Error("a new head had to wait out the old head's window")
	}
	// Waiting it out works too.
	a.ciRerunMu.Lock()
	a.ciRerunLast[card] = ciRerunAttempt{headSHA: "cafe1234", at: time.Now().Add(-ciRerunSettle - time.Minute)}
	a.ciRerunMu.Unlock()
	if !a.mayAttemptRerun(card, "cafe1234") {
		t.Error("the window never expired")
	}

	a.forgetCIReruns([]string{"22222222-2222-2222-2222-222222222222"})
	a.ciRerunMu.Lock()
	_, still := a.ciRerunLast[card]
	a.ciRerunMu.Unlock()
	if still {
		t.Error("a card that left the sweep kept its attempt clock")
	}
}

// The acceptance criterion's second half: a head that is genuinely, repeatedly
// red gets flagged for a human — not an unbounded repeat of one comment.
func TestAutoMergeSweepEscalatesInsteadOfRepeatingTheRedNote(t *testing.T) {
	a, card := sweepCard(t)
	ciTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[{"name":"go","status":"completed","conclusion":"failure",
			"completed_at":"2026-08-06T04:46:44Z"}]}`)
	}, nil) // no Actions runs behind it: nothing to re-run, straight to the note

	// Each round: sweep, then wind the note out of its 30-minute window, which
	// is what the passage of hours does on the real board.
	for i := 0; i < blockCIRed.repeats+2; i++ {
		a.autoMergeSweep(context.Background())
		if _, err := a.Pool.Exec(context.Background(), `
			UPDATE card_events SET created_at = now() - interval '40 minutes'
			WHERE card_id = $1 AND type = 'comment' AND data->>'text' LIKE $2`,
			card.ID, blockCIRed.prefix+"%"); err != nil {
			t.Fatal(err)
		}
	}

	var stuck *time.Time
	if err := a.Pool.QueryRow(context.Background(),
		`SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuck); err != nil {
		t.Fatal(err)
	}
	if stuck == nil {
		t.Fatalf("a head red through %d notes was never flagged for a human: %q",
			blockCIRed.repeats, cardComments(t, a, card.ID))
	}
	comments := cardComments(t, a, card.ID)
	if n := countWithPrefix(comments, blockCIRed.prefix); n != blockCIRed.repeats {
		t.Fatalf("want the note to stop at %d copies, got %d: %q", blockCIRed.repeats, n, comments)
	}
	var escalation string
	for _, c := range comments {
		if strings.HasPrefix(c, "Auto-merge stuck:") {
			escalation = c
		}
	}
	if !strings.Contains(escalation, "needs a human") {
		t.Fatalf("the escalation reads like the nag it replaces: %q", escalation)
	}
	if !strings.Contains(escalation, "go") {
		t.Fatalf("the escalation does not name the failing check: %q", escalation)
	}
}

// Inside the budget the card must NOT be flagged — one red is not a jam, it's
// the thing auto-merge is about to try to fix by itself.
func TestAutoMergeSweepDoesNotFlagStuckOnTheFirstRed(t *testing.T) {
	a, card := sweepCard(t)
	newCIRerunServer(t, false)

	a.autoMergeSweep(context.Background())

	var stuck *time.Time
	if err := a.Pool.QueryRow(context.Background(),
		`SELECT stuck_at FROM cards WHERE id = $1`, card.ID).Scan(&stuck); err != nil {
		t.Fatal(err)
	}
	if stuck != nil {
		t.Fatalf("the first red flagged the card stuck at %v before anything was retried", stuck)
	}
}

// A new head is a new question: the re-run budget resets, because the diff
// that produced the old failure no longer exists.
func TestRerunBudgetIsScopedToTheHeadSHA(t *testing.T) {
	a, card := sweepCard(t)
	ctx := context.Background()
	for i := 0; i < maxCIReruns; i++ {
		if err := db.AppendComment(ctx, a.Pool, card.ID,
			ciRerunPrefix+"oldhead — spent", db.EventMeta{Actor: "service"}); err != nil {
			t.Fatal(err)
		}
	}

	if n, _ := a.ciRerunsFor(ctx, card.ID, "oldhead0000"); n != maxCIReruns {
		t.Fatalf("spent budget on the old head reads as %d, want %d", n, maxCIReruns)
	}
	n, settled := a.ciRerunsFor(ctx, card.ID, "newhead0000")
	if n != 0 || !settled {
		t.Fatalf("a fresh head inherited the old head's budget: n=%d settled=%v", n, settled)
	}
}

// An unreadable ledger has to decline the spend WITHOUT telling the author
// auto-merge already retried. The count feeds the note as well as the budget,
// so answering "the budget is spent" would be the safe number attached to a
// false sentence — the card would say re-runs happened and stayed red when
// none were ever asked for, which is the misleading-nag failure this change
// exists to end.
func TestRerunLedgerErrorDeclinesTheSpendWithoutClaimingARerun(t *testing.T) {
	a, _ := sweepCard(t)

	// not a uuid: the query errors rather than returning a row
	n, settled := a.ciRerunsFor(context.Background(), "not-a-card-id", "deadbeef000")
	if settled {
		t.Errorf("an unreadable ledger reported itself settled — the sweep would spend a re-run on it")
	}
	if n != 0 {
		t.Errorf("an unreadable ledger reported %d re-runs spent, want 0", n)
	}
	if note := blockCIRedFor(redReport(time.Hour, "go"), n); strings.Contains(note.text, "re-run") &&
		strings.Contains(note.text, "already") {
		t.Errorf("the red note claims a re-run that never happened: %q", note.text)
	}
}

// A card without a fresh approval hasn't earned a merge, so it isn't blocked —
// it's just under review, and must stay silent.
func TestAutoMergeSweepSaysNothingWithoutAnApproval(t *testing.T) {
	a, card := sweepCard(t)
	ciTestServer(t, nil, nil)
	if _, err := a.Pool.Exec(context.Background(), `
		UPDATE card_events SET data = jsonb_set(data, '{text}',
			to_jsonb('Adversarial review of the diff.\n\nVerdict: request-changes'::text))
		WHERE card_id = $1 AND type = 'comment' AND data->>'text' LIKE 'Adversarial review%'`,
		card.ID); err != nil {
		t.Fatal(err)
	}

	a.autoMergeSweep(context.Background())

	for _, c := range cardComments(t, a, card.ID) {
		if strings.HasPrefix(c, "Auto-merge") {
			t.Fatalf("un-approved card got an auto-merge note: %q", c)
		}
	}
}

// ── ci_policy: what an ABSENT check means ───────────────────────────────────

// The one guarantee worth more than the feature: no setting of ci_policy may
// merge a PR whose checks failed. Only ciNone is negotiable.
func TestCIPolicyNeverPromotesARedOrPendingHead(t *testing.T) {
	for _, required := range []bool{true, false} {
		for _, state := range []ciState{ciRed, ciPending, ciGreen} {
			in := ciReport{state: state, headSHA: "deadbeef"}
			got := ciAfterPolicy(in, required)
			if got.state != state {
				t.Errorf("ciAfterPolicy(%v, required=%v).state = %v, want it unchanged",
					state, required, got.state)
			}
			if got.unchecked {
				t.Errorf("ciAfterPolicy(%v, required=%v) marked a reported head unchecked",
					state, required)
			}
		}
	}
}

func TestCIPolicyRequiredLeavesAnAbsentCheckBlocking(t *testing.T) {
	got := ciAfterPolicy(ciReport{state: ciNone, headSHA: "deadbeef"}, true)
	if got.state != ciNone {
		t.Errorf("state = %v, want ciNone — a required-CI board must keep waiting", got.state)
	}
	if got.unchecked {
		t.Error("unchecked set on a board that still requires CI")
	}
}

func TestCIPolicyIfPresentPassesAnAbsentCheckAndMarksIt(t *testing.T) {
	got := ciAfterPolicy(ciReport{state: ciNone, headSHA: "deadbeef"}, false)
	if got.state != ciGreen {
		t.Errorf("state = %v, want ciGreen — nothing reported is nothing to fail", got.state)
	}
	if !got.unchecked {
		t.Error("unchecked not set: the merge comment would claim a check passed when none ran")
	}
}

// The card trail has to record that the review was the only gate. A merge
// comment that reads identically to a CI-backed merge is how a board setting
// becomes an invisible one.
func TestMergeCommentSaysWhenNoCIRan(t *testing.T) {
	note := mergedWithoutCINote(ciReport{state: ciGreen, unchecked: true})
	if note == "" {
		t.Fatal("unchecked merge produced no note")
	}
	for _, want := range []string{"No CI ran", db.CIIfPresent, "adversarial review"} {
		if !strings.Contains(note, want) {
			t.Errorf("note does not mention %q: %q", want, note)
		}
	}
	if got := mergedWithoutCINote(ciReport{state: ciGreen}); got != "" {
		t.Errorf("a genuine green merge gained a note: %q", got)
	}
}
