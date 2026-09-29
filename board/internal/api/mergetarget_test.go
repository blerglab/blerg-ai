package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// The two pull requests every test below is about: #1 landed and closed, #2 is
// the live one. Together they are the shape the card describes — a review
// finding fixed in a follow-up PR rather than an amend — and the whole point
// is that which one gets merged must not depend on which link sorts last.
// deadPRURL is the third shape a link can have: one GitHub won't answer for at
// all. No test server below registers #3, so every read of it 404s — the repo
// renamed, the PR deleted, a cross-repo PR the token can't see.
const (
	mergedPRURL = "https://github.com/o/r/pull/1"
	openPRURL   = "https://github.com/o/r/pull/2"
	deadPRURL   = "https://github.com/o/r/pull/3"
)

// fakePR is one pull request as the GitHub stub presents it.
type fakePR struct {
	state     string // "open" or "closed"
	createdAt string // RFC3339 — the tie-break when more than one is open
	sha       string // head commit, the one CI gets asked about
}

// ghCalls records what the stub was asked, so a test can assert on the calls
// that were NOT made as well as the ones that were.
type ghCalls struct {
	mu     sync.Mutex
	merged []string // PR numbers a merge was attempted on, in order
	pulls  int      // GETs of a pull request
}

func (c *ghCalls) mergeAttempts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.merged...)
}

func (c *ghCalls) pullReads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pulls
}

// prTestServer stands up a GitHub whose repo o/r holds the given pull
// requests, keyed by number. CI answers green for every head, so a test that
// reaches a merge is one whose PR *selection* was the thing under examination.
// A merge on a PR the stub calls closed answers 405 with GitHub's real wording
// for a PR that already landed — the failure this card exists to prevent.
func prTestServer(t *testing.T, prs map[string]fakePR) *ghCalls {
	t.Helper()
	calls := &ghCalls{}
	mux := http.NewServeMux()
	for num, pr := range prs {
		num, pr := num, pr
		mux.HandleFunc("GET /repos/o/r/pulls/"+num, func(w http.ResponseWriter, _ *http.Request) {
			calls.mu.Lock()
			calls.pulls++
			calls.mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"state":%q,"created_at":%q,"head":{"sha":%q,"ref":"card-%s"}}`,
				pr.state, pr.createdAt, pr.sha, num)
		})
		mux.HandleFunc("PUT /repos/o/r/pulls/"+num+"/merge", func(w http.ResponseWriter, _ *http.Request) {
			calls.mu.Lock()
			calls.merged = append(calls.merged, num)
			calls.mu.Unlock()
			if pr.state != "open" {
				w.WriteHeader(http.StatusMethodNotAllowed)
				_, _ = fmt.Fprint(w, `{"merged":false,"message":"Pull Request is not mergeable"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"merged":true,"sha":"%s"}`, strings.Repeat(num, 40))
		})
		mux.HandleFunc("GET /repos/o/r/commits/"+pr.sha+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"check_runs":[{"status":"completed","conclusion":"success"}]}`)
		})
		mux.HandleFunc("GET /repos/o/r/commits/"+pr.sha+"/status", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"state":"success","total_count":0}`)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
	return calls
}

func prCard(urls ...string) db.Card {
	var links []db.Link
	for _, u := range urls {
		links = append(links, db.Link{Kind: "pr", URL: u})
	}
	return db.Card{Links: links}
}

// twoPRs is the fixture: #1 merged and closed at noon, #2 opened later and
// still open.
func twoPRs() map[string]fakePR {
	return map[string]fakePR{
		"1": {state: "closed", createdAt: "2026-08-05T09:00:00Z", sha: "sha1"},
		"2": {state: "open", createdAt: "2026-08-05T18:00:00Z", sha: "sha2"},
	}
}

// ── selection, no database needed ────────────────────────────────────────────

// The card's acceptance criterion, at the level of the selection itself: the
// open PR wins from either stored order. Under the old last-link-wins rule the
// second subtest picked the merged PR.
func TestMergeTargetPRPicksTheOpenPRInEitherLinkOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		links []string
	}{
		{"open link first", []string{openPRURL, mergedPRURL}},
		{"merged link first", []string{mergedPRURL, openPRURL}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prTestServer(t, twoPRs())

			got, err := mergeTargetPR(context.Background(), prCard(tc.links...))
			if err != nil {
				t.Fatalf("mergeTargetPR: %v", err)
			}
			if got != openPRURL {
				t.Fatalf("chose %s, want the open PR %s", got, openPRURL)
			}
		})
	}
}

// A card with one PR link is nearly every card, and it must not pay a GitHub
// round trip for a choice there is nothing to make. A lone merged link still
// fails at the merge exactly as it always did — the better error for it.
func TestMergeTargetPRAsksGitHubNothingForASingleLink(t *testing.T) {
	calls := prTestServer(t, twoPRs())

	got, err := mergeTargetPR(context.Background(), prCard(mergedPRURL))
	if err != nil {
		t.Fatalf("mergeTargetPR: %v", err)
	}
	if got != mergedPRURL {
		t.Fatalf("chose %s, want the card's only link %s", got, mergedPRURL)
	}
	if n := calls.pullReads(); n != 0 {
		t.Fatalf("a single-link card cost %d GitHub calls, want 0", n)
	}
}

// No PR at all is not an error — it is the ordinary spec card, and the sweep
// skips it exactly as it did before.
func TestMergeTargetPRIsSilentWithNoPRLinks(t *testing.T) {
	got, err := mergeTargetPR(context.Background(),
		db.Card{Links: []db.Link{{Kind: "session", URL: "https://sess.test/1"}}})
	if err != nil {
		t.Fatalf("mergeTargetPR on a card with no pr link: %v", err)
	}
	if got != "" {
		t.Fatalf("chose %q for a card with no pr link, want no target", got)
	}
}

// Every PR merged or closed is a distinct fact from "GitHub would not answer",
// and callers say the two differently, so the error has to be distinguishable.
func TestMergeTargetPRReportsWhenNothingIsOpen(t *testing.T) {
	prTestServer(t, map[string]fakePR{
		"1": {state: "closed", createdAt: "2026-08-05T09:00:00Z", sha: "sha1"},
		"2": {state: "closed", createdAt: "2026-08-05T18:00:00Z", sha: "sha2"},
	})

	_, err := mergeTargetPR(context.Background(), prCard(mergedPRURL, openPRURL))
	if !errors.Is(err, errNoOpenPR) {
		t.Fatalf("mergeTargetPR with everything closed = %v, want errNoOpenPR", err)
	}
}

// Two live PRs on one card is a tie the links can't settle either, so it is
// settled by the pull requests: newest created_at wins, in either link order.
func TestMergeTargetPRTakesTheNewestWhenSeveralAreOpen(t *testing.T) {
	for _, links := range [][]string{
		{mergedPRURL, openPRURL},
		{openPRURL, mergedPRURL},
	} {
		prTestServer(t, map[string]fakePR{
			"1": {state: "open", createdAt: "2026-08-05T09:00:00Z", sha: "sha1"},
			"2": {state: "open", createdAt: "2026-08-05T18:00:00Z", sha: "sha2"},
		})

		got, err := mergeTargetPR(context.Background(), prCard(links...))
		if err != nil {
			t.Fatalf("mergeTargetPR%v: %v", links, err)
		}
		if got != openPRURL {
			t.Fatalf("mergeTargetPR%v chose %s, want the newer open PR %s", links, got, openPRURL)
		}
	}
}

// An answer with no state field must be an error, never "closed" — defaulting
// a garbled response to closed would silently demote a live PR out of the
// selection, which is the exact failure this whole change removes.
func TestPRStateRefusesAnAnswerWithNoState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"number":2}`)
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	if _, err := prState(context.Background(), openPRURL); err == nil {
		t.Fatal("a pull request response with no state was accepted; want an error")
	}
	if _, err := mergeTargetPR(context.Background(), prCard(mergedPRURL, openPRURL)); err == nil {
		t.Fatal("mergeTargetPR swallowed a stateless answer; want the error surfaced")
	} else if errors.Is(err, errNoOpenPR) {
		t.Fatalf("an unreadable state was reported as nothing-open: %v", err)
	}
}

// A link GitHub won't answer for must not veto a link it answered "open" to.
// Fail-closed here parks a card whose live PR is right there and mergeable —
// and closes the human's escape hatch with it, because Accept goes through the
// same selection and would 502 for as long as the dead link stays attached.
func TestMergeTargetPRIgnoresAnUnreadableLinkWhenAnotherIsOpen(t *testing.T) {
	for _, links := range [][]string{
		{deadPRURL, openPRURL},
		{openPRURL, deadPRURL},
	} {
		prTestServer(t, twoPRs()) // #3 is not registered: every read of it 404s

		got, err := mergeTargetPR(context.Background(), prCard(links...))
		if err != nil {
			t.Fatalf("mergeTargetPR%v: %v — one dead link blocked a card with an open PR", links, err)
		}
		if got != openPRURL {
			t.Fatalf("mergeTargetPR%v chose %s, want the open PR %s", links, got, openPRURL)
		}
	}
}

// Tolerance stops exactly where the evidence does. With nothing open among the
// links that DID answer, "everything is closed" is a claim the unread link can
// contradict — so the error stands, and the caller reports GitHub-unreadable
// rather than nothing-to-merge.
func TestMergeTargetPRStillFailsWhenTheOnlyUnreadAnswerCouldBeTheOpenOne(t *testing.T) {
	prTestServer(t, twoPRs())

	_, err := mergeTargetPR(context.Background(), prCard(mergedPRURL, deadPRURL))
	if err == nil {
		t.Fatal("mergeTargetPR chose a target with one link closed and the other unreadable")
	}
	if errors.Is(err, errNoOpenPR) {
		t.Fatalf("an unreadable link was reported as nothing-open: %v", err)
	}
}

// ── the reviewer brief ──────────────────────────────────────────────────────

// The reviewer must be pointed at the PR the sweep will merge. An approval
// carries no PR identity — hasFreshApproval matches the newest "Verdict:
// approve" comment and nothing else — so a reviewer briefed on the merged PR
// writes the approval that merges the open one, unreviewed.
func TestBuildReviewPromptBriefsTheReviewerOnTheOpenPR(t *testing.T) {
	for _, tc := range []struct {
		name  string
		links []string
	}{
		{"open link first", []string{openPRURL, mergedPRURL}},
		{"merged link last", []string{mergedPRURL, openPRURL}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prTestServer(t, twoPRs())
			a := New(nil, nil, nil)
			card := prCard(tc.links...)
			card.Number, card.Title = 7, "a fix"

			prompt, err := a.buildReviewPrompt(context.Background(), db.Board{Name: "blerg-board"}, card)
			if err != nil {
				t.Fatalf("buildReviewPrompt: %v", err)
			}
			if !strings.Contains(prompt, openPRURL) {
				t.Fatalf("the brief never names the open PR %s:\n%s", openPRURL, prompt)
			}
			if strings.Contains(prompt, mergedPRURL) {
				t.Fatalf("the brief points the reviewer at the merged PR %s:\n%s", mergedPRURL, prompt)
			}
		})
	}
}

// The tempting fallback — GitHub is down, keep the last link, a review of a
// plausible PR beats no review — is the original bug wearing a different hat:
// the reviewer approves one PR while the sweep merges another. Refusing the
// brief costs a retry (reviveStalledReviews comes back); guessing costs the
// review.
func TestBuildReviewPromptRefusesToGuessWhenGitHubIsUnreadable(t *testing.T) {
	ghFailing(t, http.StatusServiceUnavailable, `{"message":"Server Error"}`)
	a := New(nil, nil, nil)
	card := prCard(mergedPRURL, openPRURL)
	card.Number, card.Title = 7, "a fix"

	prompt, err := a.buildReviewPrompt(context.Background(), db.Board{Name: "blerg-board"}, card)
	if err == nil {
		t.Fatalf("buildReviewPrompt rendered a brief on a guess:\n%s", prompt)
	}
	if !errors.Is(err, errPickReviewPR) {
		t.Fatalf("buildReviewPrompt = %v, want it wrapped in errPickReviewPR so the spawn path can retry", err)
	}
	if prompt != "" {
		t.Fatalf("a failed brief still returned text:\n%s", prompt)
	}
}

// Nothing open is a different fact from GitHub being unreadable, and the spawn
// path treats them differently (terminal vs retry), so the brief has to keep
// them apart.
func TestBuildReviewPromptSaysWhenNoLinkedPRIsOpen(t *testing.T) {
	prTestServer(t, map[string]fakePR{
		"1": {state: "closed", createdAt: "2026-08-05T09:00:00Z", sha: "sha1"},
		"2": {state: "closed", createdAt: "2026-08-05T18:00:00Z", sha: "sha2"},
	})
	a := New(nil, nil, nil)
	card := prCard(mergedPRURL, openPRURL)
	card.Number, card.Title = 7, "a fix"

	if _, err := a.buildReviewPrompt(context.Background(), db.Board{Name: "blerg-board"}, card); !errors.Is(err, errNoOpenPR) {
		t.Fatalf("buildReviewPrompt with everything closed = %v, want errNoOpenPR", err)
	}
}

// A single-link card is nearly every card, and the brief must not pay a GitHub
// round trip — nor become unrenderable when GitHub is down, which would stop
// reviews blerg-board used to run happily offline.
func TestBuildReviewPromptAsksGitHubNothingForASingleLink(t *testing.T) {
	ghFailing(t, http.StatusServiceUnavailable, `{"message":"Server Error"}`)
	a := New(nil, nil, nil)
	card := prCard(openPRURL)
	card.Number, card.Title = 7, "a fix"

	prompt, err := a.buildReviewPrompt(context.Background(), db.Board{Name: "blerg-board"}, card)
	if err != nil {
		t.Fatalf("buildReviewPrompt on a single-link card: %v", err)
	}
	if !strings.Contains(prompt, openPRURL) {
		t.Fatalf("the brief never names the card's only PR:\n%s", prompt)
	}
}

// ── the spawn path, against the test database ───────────────────────────────

// reviewCardLinked is a PR card sitting in the review column with its `pr`
// links in the given order and no verdict yet — the state maybeSpawnReviewer
// is asked to act on. Unlike sweepCardLinked it leaves auto_merge alone and
// posts no approval: the question here is which PR the REVIEWER is sent to.
func reviewCardLinked(t *testing.T, prURLs ...string) (*API, db.Board, db.Card, *specFakeRunner) {
	t.Helper()
	a, pool, fake := specTestAPI(t)
	ctx := context.Background()

	board, card, cols := specCard(t, pool, "the work")
	links := make([]db.Link, 0, len(prURLs))
	for _, u := range prURLs {
		links = append(links, db.Link{Kind: "pr", URL: u})
	}
	if _, err := db.UpdateCard(ctx, pool, card.ID, db.CardParams{Links: &links},
		db.EventMeta{Actor: "service"}); err != nil {
		t.Fatal(err)
	}
	moved, err := db.MoveCard(ctx, pool, card.ID, specColumn(t, cols, "review"), nil, nil,
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	return a, board, moved, fake
}

func cardIsStuck(t *testing.T, a *API, cardID string) bool {
	t.Helper()
	var stuckAt *time.Time
	if err := a.Pool.QueryRow(context.Background(),
		`SELECT stuck_at FROM cards WHERE id = $1`, cardID).Scan(&stuckAt); err != nil {
		t.Fatal(err)
	}
	return stuckAt != nil
}

// The end-to-end shape of the fallback this change removes: GitHub can't say
// which PR is open, so NO reviewer starts. A reviewer briefed on a guess can
// approve the merged PR and hand that approval to the sweep, which spends it
// merging the open one — an unreviewed diff, auto-merged.
//
// Not starting is only acceptable because it self-heals: no stuck flag, and a
// note that both tells the human what happened and rate-limits
// reviveStalledReviews, which retries on its own.
func TestReviewSpawnRefusesToBriefAReviewerOnAGuess(t *testing.T) {
	a, board, card, fake := reviewCardLinked(t, mergedPRURL, openPRURL)
	ghFailing(t, http.StatusServiceUnavailable, `{"message":"Server Error"}`)

	a.maybeSpawnReviewer(context.Background(), board, card)

	if starts, _, _ := fake.seen(); len(starts) != 0 {
		t.Fatalf("spawned %d reviewer(s) without knowing which PR to brief them on:\n%s",
			len(starts), starts[0].Prompt)
	}
	comments := cardComments(t, a, card.ID)
	if n := countWithPrefix(comments, noReviewerPrefix); n != 1 {
		t.Fatalf("want exactly one %q note, got %d in %q", noReviewerPrefix, n, comments)
	}
	if cardIsStuck(t, a, card.ID) {
		t.Fatal("one 503 flagged the card stuck; a transient GitHub error must be retried, not escalated")
	}
	var sessions int
	if err := a.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM runner_sessions WHERE card_id = $1`, card.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("%d runner_sessions row(s) written for a review that never started", sessions)
	}
}

// Retrying is not the same as retrying forever. A link that will never be
// readable — a deleted PR, a repo the token lost — would otherwise leave the
// card alternating between silence and the same note every grace period, which
// is the never-changing loop the whole auto-merge reporting effort exists to
// end. After a few rounds it becomes a human's problem, and the note says
// which human problem it is.
func TestReviewSpawnEscalatesWhenGitHubNeverAnswers(t *testing.T) {
	a, board, card, fake := reviewCardLinked(t, mergedPRURL, openPRURL)
	ghFailing(t, http.StatusServiceUnavailable, `{"message":"Server Error"}`)
	ctx := context.Background()

	for i := 0; i < retryNoReviewerNotes; i++ {
		a.maybeSpawnReviewer(ctx, board, card)
		if cardIsStuck(t, a, card.ID) {
			t.Fatalf("card flagged stuck after %d attempt(s), want %d", i+1, retryNoReviewerNotes+1)
		}
	}
	a.maybeSpawnReviewer(ctx, board, card)

	if !cardIsStuck(t, a, card.ID) {
		t.Fatalf("still retrying after %d attempts; comments were %q",
			retryNoReviewerNotes+1, cardComments(t, a, card.ID))
	}
	comments := cardComments(t, a, card.ID)
	last := comments[len(comments)-1]
	if !strings.Contains(last, "flagged stuck") || !strings.Contains(last, "pr links") {
		t.Fatalf("the escalation note doesn't point at the card's links: %q", last)
	}
	if starts, _, _ := fake.seen(); len(starts) != 0 {
		t.Fatalf("spawned %d reviewer(s) along the way", len(starts))
	}
}

// Every linked PR merged or closed is terminal, not transient: no retry
// produces a diff to review. Say so once, flag it, and name the remedy.
func TestReviewSpawnFlagsStuckWhenNoLinkedPRIsOpen(t *testing.T) {
	a, board, card, fake := reviewCardLinked(t, mergedPRURL, openPRURL)
	prTestServer(t, map[string]fakePR{
		"1": {state: "closed", createdAt: "2026-08-05T09:00:00Z", sha: "sha1"},
		"2": {state: "closed", createdAt: "2026-08-05T18:00:00Z", sha: "sha2"},
	})

	a.maybeSpawnReviewer(context.Background(), board, card)

	if starts, _, _ := fake.seen(); len(starts) != 0 {
		t.Fatalf("spawned %d reviewer(s) for a card with no open PR", len(starts))
	}
	if !cardIsStuck(t, a, card.ID) {
		t.Fatalf("nothing open is terminal but the card wasn't flagged; comments %q",
			cardComments(t, a, card.ID))
	}
	comments := cardComments(t, a, card.ID)
	if n := countWithPrefix(comments, noReviewerPrefix); n != 0 {
		t.Fatalf("a terminal state was reported as a retry (%d %q notes): %q", n, noReviewerPrefix, comments)
	}
	if !strings.Contains(comments[len(comments)-1], "merged or closed") {
		t.Fatalf("the note doesn't say what is wrong: %q", comments[len(comments)-1])
	}
}

// The re-review nudge is the OTHER way an approval can land on the wrong PR,
// and it is the likelier one: the round that follows a request-changes verdict
// is exactly when a card grows a second pr link (the author fixed the finding
// in a follow-up PR), and the session being nudged was briefed on the first.
// Told only to "re-review the PR diff", it re-reads the PR it already knows.
func TestReReviewNudgeNamesThePRThatWillMerge(t *testing.T) {
	a, board, card, fake := reviewCardLinked(t, mergedPRURL, openPRURL)
	prTestServer(t, twoPRs())
	if _, err := a.Pool.Exec(context.Background(), `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role)
		VALUES ($1,$2,'fake','ext-rev','idle','reviewer')`, card.ID, board.ID); err != nil {
		t.Fatal(err)
	}

	a.maybeSpawnReviewer(context.Background(), board, card)

	starts, msgs, msgTo := fake.seen()
	if len(starts) != 0 {
		t.Fatalf("spawned %d reviewer(s) instead of nudging the settled one", len(starts))
	}
	if len(msgs) != 1 || msgTo[0] != "ext-rev" {
		t.Fatalf("nudges %v to %v, want one to ext-rev", msgs, msgTo)
	}
	if !strings.Contains(msgs[0], openPRURL) {
		t.Fatalf("the nudge never names the open PR %s: %q", openPRURL, msgs[0])
	}
	if strings.Contains(msgs[0], mergedPRURL) {
		t.Fatalf("the nudge points the reviewer at the merged PR: %q", msgs[0])
	}
}

// ── the sweep, against the test database ────────────────────────────────────

// The card's acceptance criterion end to end: the sweep merges the open PR
// whichever way round the links are stored. The assertion is on the merge
// ATTEMPTS, not just the outcome — under last-link-wins the second subtest
// PUT /pulls/1/merge, got 405, and bounced the card to a worker to resolve a
// conflict that did not exist.
func TestAutoMergeSweepMergesTheOpenPRInEitherLinkOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		links []string
	}{
		{"merged link stored first", []string{mergedPRURL, openPRURL}},
		{"merged link stored last", []string{openPRURL, mergedPRURL}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, card := sweepCardLinked(t, tc.links...)
			calls := prTestServer(t, twoPRs())

			a.autoMergeSweep(context.Background())

			attempts := calls.mergeAttempts()
			if len(attempts) != 1 || attempts[0] != "2" {
				t.Fatalf("merge attempts %v, want exactly [2] — the open PR", attempts)
			}
			var mergedSHA *string
			if err := a.Pool.QueryRow(context.Background(),
				`SELECT merged_sha FROM cards WHERE id = $1`, card.ID).Scan(&mergedSHA); err != nil {
				t.Fatal(err)
			}
			if mergedSHA == nil {
				t.Fatalf("the card records no merge; comments were %q", cardComments(t, a, card.ID))
			}
			for _, c := range cardComments(t, a, card.ID) {
				if strings.HasPrefix(c, "Auto-merge failed") || strings.HasPrefix(c, "Auto-merge blocked") {
					t.Fatalf("a clean merge still complained: %q", c)
				}
			}
		})
	}
}

// Nothing open is terminal, and a card that has earned its merge must be told
// so rather than left looking like one still under review. Crucially the flag
// survives: linking the live PR is enough to unblock, with no human re-arming
// auto_merge.
func TestAutoMergeSweepReportsWhenNoLinkedPRIsOpen(t *testing.T) {
	a, card := sweepCardLinked(t, mergedPRURL, openPRURL)
	calls := prTestServer(t, map[string]fakePR{
		"1": {state: "closed", createdAt: "2026-08-05T09:00:00Z", sha: "sha1"},
		"2": {state: "closed", createdAt: "2026-08-05T18:00:00Z", sha: "sha2"},
	})

	a.autoMergeSweep(context.Background())

	comments := cardComments(t, a, card.ID)
	if n := countWithPrefix(comments, blockNoOpenPR.prefix); n != 1 {
		t.Fatalf("want exactly one %q comment, got %d in %q", blockNoOpenPR.prefix, n, comments)
	}
	if attempts := calls.mergeAttempts(); len(attempts) != 0 {
		t.Fatalf("merge was attempted on %v with nothing open", attempts)
	}
	var autoMerge bool
	if err := a.Pool.QueryRow(context.Background(),
		`SELECT auto_merge FROM cards WHERE id = $1`, card.ID).Scan(&autoMerge); err != nil {
		t.Fatal(err)
	}
	if !autoMerge {
		t.Fatal("auto_merge was cleared; linking the live PR should be enough to unblock the card")
	}
}

// A GitHub that won't say which PR is open is a different fact from nothing
// being open, and it is timed by how long the error has held — a single 5xx
// on a multi-PR card must not reach the card at all.
func TestAutoMergeSweepReportsAnUnreadablePRState(t *testing.T) {
	a, card := sweepCardLinked(t, mergedPRURL, openPRURL)
	ghFailing(t, http.StatusServiceUnavailable, `{"message":"Server Error"}`)

	a.autoMergeSweep(context.Background()) // first failure: inside the quiet window
	for _, c := range cardComments(t, a, card.ID) {
		if strings.HasPrefix(c, "Auto-merge") {
			t.Fatalf("one 503 was narrated onto the card: %q", c)
		}
	}

	errorHeldFor(a, card.ID, blockPRStateError(fmt.Errorf("x")).quiet+time.Minute)
	a.autoMergeSweep(context.Background())

	comments := cardComments(t, a, card.ID)
	if n := countWithPrefix(comments, prStateErrorPrefix); n != 1 {
		t.Fatalf("want exactly one %q comment, got %d in %q", prStateErrorPrefix, n, comments)
	}
	if countWithPrefix(comments, blockNoOpenPR.prefix) != 0 {
		t.Fatalf("an unreadable state was reported as nothing-open: %q", comments)
	}
}

// ── the human Accept button ─────────────────────────────────────────────────

// Accept shares the selection, per the trail on this card: the sweep getting
// it right while Accept keeps merging the closed PR would be half a fix.
func TestAcceptMergesTheOpenPRInEitherLinkOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		links []db.Link
	}{
		{"merged link stored first", []db.Link{{Kind: "pr", URL: mergedPRURL}, {Kind: "pr", URL: openPRURL}}},
		{"merged link stored last", []db.Link{{Kind: "pr", URL: openPRURL}, {Kind: "pr", URL: mergedPRURL}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, pool, _ := specTestAPI(t)
			card, _ := acceptCard(t, pool)
			if _, err := db.UpdateCard(context.Background(), pool, card.ID,
				db.CardParams{Links: &tc.links}, db.EventMeta{Actor: "service"}); err != nil {
				t.Fatal(err)
			}
			card, err := db.GetCard(context.Background(), pool, card.ID)
			if err != nil {
				t.Fatal(err)
			}
			calls := prTestServer(t, twoPRs())

			rec := pressAccept(t, a, card)

			if rec.Code != http.StatusOK {
				t.Fatalf("Accept = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			attempts := calls.mergeAttempts()
			if len(attempts) != 1 || attempts[0] != "2" {
				t.Fatalf("merge attempts %v, want exactly [2] — the open PR", attempts)
			}
		})
	}
}

// Accept on a card whose every PR already landed used to 405, read as a merge
// conflict, and bounce the card to a worker to resolve a conflict that does
// not exist. Now it says what is actually wrong and changes nothing.
func TestAcceptSaysSoWhenNoLinkedPRIsOpen(t *testing.T) {
	a, pool, _ := specTestAPI(t)
	card, cols := acceptCard(t, pool)
	links := []db.Link{{Kind: "pr", URL: mergedPRURL}, {Kind: "pr", URL: openPRURL}}
	if _, err := db.UpdateCard(context.Background(), pool, card.ID,
		db.CardParams{Links: &links}, db.EventMeta{Actor: "service"}); err != nil {
		t.Fatal(err)
	}
	card, err := db.GetCard(context.Background(), pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls := prTestServer(t, map[string]fakePR{
		"1": {state: "closed", createdAt: "2026-08-05T09:00:00Z", sha: "sha1"},
		"2": {state: "closed", createdAt: "2026-08-05T18:00:00Z", sha: "sha2"},
	})

	rec := pressAccept(t, a, card)

	if rec.Code != http.StatusConflict {
		t.Fatalf("Accept with nothing open = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "merged or closed") {
		t.Fatalf("the 409 does not say what is wrong: %s", rec.Body.String())
	}
	if attempts := calls.mergeAttempts(); len(attempts) != 0 {
		t.Fatalf("merge was attempted on %v with nothing open", attempts)
	}
	after, err := db.GetCard(context.Background(), pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := cardColumnName(t, cols, after); got != "review" {
		t.Fatalf("the card moved to %q; a refused Accept must leave it alone", got)
	}
}
