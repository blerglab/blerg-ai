package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/prompts"
)

// Adversarial review: when a card enters a review column, blerg-board spawns a
// second session in the reviewer role. The reviewer posts a comment starting
// "Adversarial review" with "Verdict: approve" or "Verdict: request-changes";
// on request-changes it moves the card back to in-progress, and blerg-board relays
// the findings to the authoring session. The human only engages once a card
// sits in review with an approve verdict.
//
// What gets reviewed depends on what the card produced — see reviewTarget.

var verdictRE = regexp.MustCompile(`(?i)verdict:\s*(approve|request[- ]changes)`)

// reviewerAttemptCap bounds how many reviewer sessions blerg-board will start for
// one review round before flagging the card stuck and leaving it to a human.
// Without a cap a reviewer that dies on start would be respawned every sweep
// window, forever, invisibly.
//
// Mirrors the worker path in both halves, which matters: reapQuietWorkers
// gives a quiet worker two respawns and then stuck_at + a comment, and its
// counter (cards.run_attempts) is reset to 0 by pressing Run. A give-up rule
// with no re-arm is a trap, not a bound — see reviewRoundStart for what
// re-arms this one.
const reviewerAttemptCap = 3

// reviewTargetKind is the artifact an adversarial reviewer would judge.
type reviewTargetKind int

const (
	reviewNothing reviewTargetKind = iota // nothing on this card to review
	reviewPR                              // a pull request diff
	reviewSpec                            // the card body itself
)

// reviewTarget decides what a reviewer spawned for this card would review.
//
// A PR link wins: if code was written, the diff is the artifact. With no PR
// but a non-blank body, the body IS the artifact. Discuss/brainstorm sessions
// are barred from writing or pushing code (prompts/discuss.md) — they hold
// only card.write and write their output straight into card.Body, which the
// UI already renders as markdown in place. So a spec card never grows a link
// to trigger on, and demanding one would mean spawning a second, push-capable
// session purely to commit a doc that already exists as the card's own body.
//
// The readiness signal is the same for both kinds and unchanged: somebody
// moved the card into the review column. That is the answer to "is this body
// a finished spec or a mid-brainstorm scratchpad?" — a body is ready when the
// card is put in review, and in practice that means a human, because
// discuss.md tells discuss sessions not to move cards. That is a prompt
// convention, not a capability boundary: every spawned session is minted with
// column.write, so a discuss session that ignored its brief could move its
// own card. Nothing here depends on it being impossible — only on the move
// being a deliberate act. Body edits alone never trigger a review.
func reviewTarget(card db.Card) (reviewTargetKind, string) {
	pr := ""
	for _, l := range card.Links {
		if l.Kind == "pr" {
			// Last wins, but nothing acts on this URL: which PR is the real
			// target is GitHub's answer, not the slice's — see mergeTargetPR,
			// which every caller that merges, or briefs a reviewer, asks
			// first. This function stays pure and network-free because two of
			// its three callers only want the KIND (a PR card or a spec card),
			// and that answer doesn't depend on which PR.
			pr = l.URL
		}
	}
	if pr != "" {
		return reviewPR, pr
	}
	if card.Body != nil && strings.TrimSpace(*card.Body) != "" {
		return reviewSpec, ""
	}
	return reviewNothing, ""
}

// errNoOpenPR: the card carries `pr` links, but GitHub says every one of them
// is merged or closed. Distinct from "no PR at all", which is not an error.
var errNoOpenPR = errors.New("no open pull request among the card's pr links")

// errPickReviewPR wraps whatever stopped buildReviewPrompt from deciding WHICH
// pull request the reviewer should judge. It exists so the spawn path can tell
// that failure apart from a template that won't render: the two need different
// words on the card and, for the transient half, different handling.
var errPickReviewPR = errors.New("choosing the PR to review")

// prInfo is the slice of a GitHub pull request that decides whether it is the
// one to act on.
type prInfo struct {
	Open      bool
	CreatedAt time.Time
}

// prState reads a pull request's state from GitHub.
//
// A response carrying no `state` field is an error, never "closed". The whole
// point of asking is to avoid acting on a PR whose state we don't know, and
// defaulting a garbled or truncated answer to closed would silently demote a
// live PR out of the selection below — the exact failure this is here to stop.
func prState(ctx context.Context, prURL string) (prInfo, error) {
	m := prURLRE.FindStringSubmatch(prURL)
	if m == nil {
		return prInfo{}, fmt.Errorf("unrecognized PR URL %q", prURL)
	}
	owner, repo, num := m[1], m[2], m[3]
	var pr struct {
		State     string    `json:"state"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := ghGetJSON(ctx,
		fmt.Sprintf("%s/repos/%s/%s/pulls/%s", githubAPIBase, owner, repo, num), &pr); err != nil {
		return prInfo{}, err
	}
	if pr.State == "" {
		return prInfo{}, fmt.Errorf("github: %s: no state in the pull request response", prURL)
	}
	return prInfo{Open: pr.State == "open", CreatedAt: pr.CreatedAt}, nil
}

// mergeTargetPR picks which of a card's `pr` links is THE pull request: the
// one to merge, and the one an adversarial reviewer should judge.
//
// It replaces the "last pr link wins" convention that accept, the auto-merge
// sweep and reviewTarget shared (96800f6 aligned them on purpose, so this
// changes the rule in one place for all three rather than splitting them
// apart again). Position in card.Links was only ever a proxy for "the live
// PR", and not a load-bearing one: db.GetCard orders links by
// card_links.rank, replaceLinks writes rank as the slice index of the last
// full links write, so the order is insertion order by convention and any
// client that PATCHes a reordered links array renumbers every row. Cards
// routinely carry more than one pr link — a review finding fixed in a
// follow-up PR instead of an amend leaves the first merged and closed — so on
// the wrong ordering the old rule handed mergePR an already-merged PR, which
// answers 405, reads as a merge conflict (isConflictErr) and bounces the card
// to a worker to resolve conflicts that do not exist.
//
// The rule:
//
//   - no pr links → "" and no error. Callers that need one skip, as before.
//   - exactly one → that link, with no round trip. There is nothing to choose
//     between, and a lone merged link still fails loudly at the merge exactly
//     as it always has — the better error for that case, and it keeps the
//     single-PR path (nearly every card) free of extra GitHub calls.
//   - two or more → the open one; several open (a card carrying two live PRs)
//     tie-breaks on the newest created_at, so even the tie is settled by the
//     pull requests rather than by rank.
//   - two or more, none open → errNoOpenPR. There is nothing to merge, and
//     saying so beats 405-ing against a PR that already landed.
//
// An unreadable link does not veto a readable answer. A card can carry a `pr`
// link that no longer resolves — repo renamed, PR deleted, a cross-repo PR
// this token can't see, a URL prURLRE doesn't match — and failing the whole
// card on it would park a card whose live PR is right there and mergeable,
// with the human's Accept button answering 502 for as long as the dead link
// stays attached. So an open PR wins even when a sibling link errored; the
// error only decides the answer when nothing came back open, because then
// "everything is closed" is a claim the unread link could contradict.
//
// The one thing tolerance costs is the tie-break: if an unreadable link was
// itself an open PR newer than the one chosen, the newest-open rule picked
// second-newest. Choosing a live PR the card links beats refusing to choose.
//
// GitHub errors come back as-is: not knowing the state is a different fact
// from there being nothing to merge, and callers report the two differently.
func mergeTargetPR(ctx context.Context, card db.Card) (string, error) {
	var prs []string
	for _, l := range card.Links {
		if l.Kind == "pr" {
			prs = append(prs, l.URL)
		}
	}
	switch len(prs) {
	case 0:
		return "", nil
	case 1:
		return prs[0], nil
	}
	var best string
	var bestAt time.Time
	var firstErr error
	for _, u := range prs {
		info, err := prState(ctx, u)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !info.Open {
			continue
		}
		if best == "" || info.CreatedAt.After(bestAt) {
			best, bestAt = u, info.CreatedAt
		}
	}
	switch {
	case best != "":
		return best, nil
	case firstErr != nil:
		return "", firstErr
	default:
		return "", errNoOpenPR
	}
}

func isReviewColumn(name string) bool {
	return strings.Contains(strings.ToLower(name), "review")
}

func isWorkColumn(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "progress") || strings.Contains(n, "doing") || strings.Contains(n, "working")
}

// afterMove is called (async) after any successful card move. It owns both
// review-flow reactions: spawning a reviewer on entry into review, and
// relaying reviewer findings back to the worker on the bounce-back move.
func (a *API) afterMove(ctx context.Context, board db.Board, card db.Card) {
	if a.runner == nil || card.ColumnID == nil {
		return
	}
	if board.DrivenBy != nil && *board.DrivenBy != "" {
		return // an external system owns this board's card lifecycle
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cols, err := db.ListColumns(ctx, a.Pool, card.BoardID)
	if err != nil {
		return
	}
	var colName string
	for _, c := range cols {
		if c.ID == *card.ColumnID {
			colName = c.Name
		}
	}
	switch {
	case isReviewColumn(colName):
		a.maybeSpawnReviewer(ctx, board, card)
	case isWorkColumn(colName):
		a.maybeRelayFindings(ctx, card)
	}
}

func (a *API) maybeSpawnReviewer(ctx context.Context, board db.Board, card db.Card) {
	if len(card.Repos) == 0 {
		return
	}
	kind, _ := reviewTarget(card)
	if kind == reviewNothing {
		return
	}
	// skip if the newest adversarial verdict is newer than the newest author
	// activity — the review already happened for this iteration.
	//
	// A *verdict* means a comment carrying a Verdict line. The prefix alone
	// isn't enough: the service breadcrumbs this function itself posts
	// ("Adversarial review session spawned…", "Adversarial re-review
	// requested…") share it, so matching on the prefix made spawning a
	// reviewer stamp a verdict newer than any author activity — after which
	// the card could never be re-reviewed and reviveStalledReviews bounced
	// off blerg-board's own breadcrumb forever. The regexp mirrors verdictRE.
	var verdictAt *time.Time
	_ = a.Pool.QueryRow(ctx, `
		SELECT max(created_at) FROM card_events
		WHERE card_id = $1 AND type = 'comment'
		  AND data->>'text' LIKE 'Adversarial review%'
		  AND data->>'text' ~* 'verdict:[[:space:]]*(approve|request[- ]changes)'`,
		card.ID).Scan(&verdictAt)
	workAt := a.newestAuthorActivity(ctx, card.ID, kind)
	if verdictAt != nil && (workAt == nil || verdictAt.After(*workAt)) {
		return
	}
	// an existing reviewer: leave it alone mid-review; nudge it to re-review
	// if it's settled (idle sessions never reach a terminal lifecycle, so
	// "count active" would block every re-review forever)
	var revExt, revLC string
	err := a.Pool.QueryRow(ctx, `
		SELECT external_session_id, lifecycle FROM runner_sessions
		WHERE card_id = $1 AND role = 'reviewer' AND lifecycle NOT IN ('stopped','error')
		ORDER BY created_at DESC LIMIT 1`, card.ID).Scan(&revExt, &revLC)
	if err == nil {
		if revLC == "running" || revLC == "starting" {
			return // mid-review
		}
		what := "the card body as it now reads (verify the revisions actually answer your findings, and re-check any new claims against the code)"
		if kind == reviewPR {
			// Name the PR, don't say "the PR diff". The round this nudge opens
			// is the one where a card GROWS a second pr link — the author fixed
			// the findings in a follow-up PR instead of an amend — and the
			// session being nudged was briefed on the first one. Left generic,
			// it re-reads the PR it already knows, approves that, and the sweep
			// spends the approval merging the other one.
			target, err := mergeTargetPR(ctx, card)
			if err != nil {
				log.Printf("auto-review: card #%d: choosing the PR to re-review: %v", card.Number, err)
				a.noteNoReviewerFor(ctx, card, a.reviewRoundStart(ctx, card, verdictAt),
					fmt.Errorf("%w: %w", errPickReviewPR, err))
				return
			}
			what = fmt.Sprintf("the diff at %s (verify the fixes are real, re-run what the changes touch) — "+
				"check that URL rather than assuming it is the PR you reviewed last round: this is the pull "+
				"request blerg-board will merge", target)
		}
		nudge := fmt.Sprintf("Card #%d is back in review — the author updated it to address your findings. "+
			"Re-review %s and post a fresh 'Adversarial review' comment with a Verdict line.", card.Number, what)
		if a.runner.Driver.Message(ctx, revExt, nudge, "blerg-board") == nil {
			_ = db.AppendComment(ctx, a.Pool, card.ID,
				"Adversarial re-review requested from the existing reviewer session.",
				db.EventMeta{Actor: "service"})
			a.Hub.Broadcast(card.BoardID, "card_changed")
			return
		}
		// unreachable reviewer (dead pod) — fall through and spawn a fresh one
	}
	// A reviewer that keeps dying must surface to a human, not loop — but the
	// bound has to be re-armable, or it is a one-way door: once it fires no
	// reviewer can spawn, so no verdict can be written, so a count taken since
	// the last verdict could never reset. Attempts are counted from the start
	// of the current round instead (see reviewRoundStart), which every way of
	// saying "try again" moves forward.
	roundStart := a.reviewRoundStart(ctx, card, verdictAt)
	var attempts int
	_ = a.Pool.QueryRow(ctx, `
		SELECT count(*) FROM runner_sessions
		WHERE card_id = $1 AND role = 'reviewer'
		  AND ($2::timestamptz IS NULL OR created_at > $2)`, card.ID, roundStart).Scan(&attempts)
	if attempts >= reviewerAttemptCap {
		a.flagReviewStuck(ctx, card, fmt.Sprintf(
			"Adversarial review: %d reviewer session(s) started for this round without a verdict — "+
				"flagged stuck rather than spawning more. Look at the reviewer sessions, then press Run on "+
				"the board to retry stuck cards, or move the card out of review and back.", attempts))
		return
	}
	// Starting a round means the card is no longer stuck. Clearing it here (and
	// not, say, in the move handler) keeps the flag honest for the case a human
	// moves a still-stuck card back into review: the retry and the flag can't
	// disagree, because the same code path does both.
	if card.StuckAt != nil {
		_, _ = a.Pool.Exec(ctx, `UPDATE cards SET stuck_at = NULL WHERE id = $1`, card.ID)
	}
	prompt, err := a.buildReviewPrompt(ctx, board, card)
	if err != nil {
		log.Printf("auto-review: render prompt for card #%d: %v", card.Number, err)
		a.noteNoReviewerFor(ctx, card, roundStart, err)
		return
	}
	if _, _, err := a.spawnSession(ctx, board, card, "reviewer", prompt, ""); err != nil {
		log.Printf("auto-review: spawn reviewer for card #%d: %v", card.Number, err)
		// A failed spawn writes no runner_sessions row and would otherwise
		// write no comment either — and the "spawned" comment below is the
		// ONLY thing rate-limiting reviveStalledReviews. With no breadcrumb
		// the limit never arms and the 20s board ticker retries ~3x a minute,
		// hammering a runner that is out of slots exactly when it asked us to
		// back off. Capacity is transient (retry next window, never stuck-
		// flag); anything else is this card's problem, same split as
		// dispatchBoard makes for workers.
		if isCapacityErr(err) {
			_ = db.AppendComment(ctx, a.Pool, card.ID,
				"Adversarial review session could not start — the runner is out of slots. Retrying later.",
				db.EventMeta{Actor: "service"})
			a.Hub.Broadcast(card.BoardID, "card_changed")
			return
		}
		a.flagReviewStuck(ctx, card,
			"Adversarial review session failed to start ("+err.Error()+") — flagged stuck.")
		return
	}
	_ = db.AppendComment(ctx, a.Pool, card.ID,
		"Adversarial review session spawned — the card stays here until a verdict is posted.",
		db.EventMeta{Actor: "service"})
	a.Hub.Broadcast(card.BoardID, "card_changed")
}

// reviewRoundStart is when the current review round began. Reviewer sessions
// started before it belong to a finished round and don't count against
// reviewerAttemptCap, so every legitimate way of saying "try again" re-arms
// the bound:
//
//   - a real verdict — the previous round reached its conclusion;
//   - a move into this (review) column — somebody deliberately put the card
//     back up for review;
//   - a "flagged stuck" comment on a card that is no longer stuck — the human
//     used the platform's standard retry, pressing Run, which clears stuck_at
//     for the whole board (handleStartRun). Nothing else records that event,
//     so the cleared flag is the signal.
//
// The last two are what keep the cap from being a trap. Without them the only
// reset is a verdict, and a capped card can never produce one.
func (a *API) reviewRoundStart(ctx context.Context, card db.Card, verdictAt *time.Time) *time.Time {
	var colID string
	if card.ColumnID != nil {
		colID = *card.ColumnID
	}
	var at *time.Time
	_ = a.Pool.QueryRow(ctx, `
		SELECT max(t) FROM (
			SELECT $2::timestamptz AS t
			UNION ALL
			SELECT max(created_at) FROM card_events
				WHERE card_id = $1 AND type = 'moved' AND to_column_id = $3::uuid
			UNION ALL
			SELECT max(created_at) FROM card_events
				WHERE card_id = $1 AND type = 'comment' AND $4::boolean
				  AND data->>'text' LIKE 'Adversarial review%flagged stuck%'
		) s`, card.ID, verdictAt, nullIfEmpty(colID), card.StuckAt == nil).Scan(&at)
	return at
}

// nullIfEmpty keeps an empty id out of a ::uuid cast, which would error.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// flagReviewStuck marks a card as needing a human and says why, once. The
// comment is prefixed "Adversarial review" so it also arms the sweep's rate
// limit, and stuck_at drops the card out of reviveStalledReviews entirely —
// the same "stop and surface" contract reapQuietWorkers gives a worker that
// will not finish.
//
// The auto-merge sweep flags stuck through flagAutoMergeStuck instead: its
// note has to outlive the flag, because pressing Run clears the flag without
// changing anything about the jam it reports.
func (a *API) flagReviewStuck(ctx context.Context, card db.Card, note string) {
	tag, err := a.Pool.Exec(ctx,
		`UPDATE cards SET stuck_at = now() WHERE id = $1 AND stuck_at IS NULL`, card.ID)
	if err != nil || tag.RowsAffected() == 0 {
		return // already flagged — don't repeat the comment every sweep
	}
	_ = db.AppendComment(ctx, a.Pool, card.ID, note, db.EventMeta{Actor: "service"})
	a.Hub.Broadcast(card.BoardID, "card_changed")
}

// noReviewerPrefix opens every "couldn't start a reviewer, will try again"
// note. It is what makes those notes countable (the escalation below) and, via
// its "Adversarial review" head, what arms reviveStalledReviews' rate limit.
const noReviewerPrefix = "Adversarial review: no reviewer started —"

// retryNoReviewerNotes is how many times one round says "retrying" about the
// same unreadable GitHub before calling it a jam. Revive's grace period paces
// the notes, so this is roughly half an hour of trying.
const retryNoReviewerNotes = 3

// noteNoReviewerFor reports why no reviewer was spawned and decides whether
// this card should keep trying:
//
//   - errNoOpenPR — terminal. Every pull request the card links is merged or
//     closed, so there is no diff to judge and no amount of retrying makes
//     one. Flag it and say what fixes it.
//   - any other PR-choice failure — GitHub was unreadable, which is the same
//     shape as the runner being out of slots: leave a note, don't flag stuck,
//     let reviveStalledReviews come back. The note is not decoration — it is
//     the only thing rate-limiting revive, and without it the 20s board ticker
//     would retry ~3x a minute against a GitHub that just failed us. But
//     "retry forever" isn't a contract either: once a round has spent
//     retryNoReviewerNotes on this, GitHub isn't blinking, this card's links
//     are broken, and that is a human's problem.
//   - anything else — the brief itself wouldn't render. Terminal, as before.
func (a *API) noteNoReviewerFor(ctx context.Context, card db.Card, roundStart *time.Time, err error) {
	switch {
	case errors.Is(err, errNoOpenPR):
		a.flagReviewStuck(ctx, card, "Adversarial review: every pull request linked to this card is "+
			"already merged or closed on GitHub, so there is no diff to review and no reviewer was "+
			"started — flagged stuck. Link the live PR to the card, or move it to done if the work "+
			"already landed.")
	case errors.Is(err, errPickReviewPR):
		var notes int
		_ = a.Pool.QueryRow(ctx, `
			SELECT count(*) FROM card_events
			WHERE card_id = $1 AND type = 'comment'
			  AND data->>'text' LIKE $2
			  AND ($3::timestamptz IS NULL OR created_at > $3)`,
			card.ID, noReviewerPrefix+"%", roundStart).Scan(&notes)
		if notes >= retryNoReviewerNotes {
			a.flagReviewStuck(ctx, card, fmt.Sprintf("Adversarial review: this card carries more than one "+
				"pull request and GitHub still can't say which of them is open (%v) after %d attempts, so no "+
				"reviewer has been started — flagged stuck. Check the card's pr links: one of them probably "+
				"points at a pull request that no longer exists or that blerg-board's token can't read. Remove it, "+
				"then press Run on the board.", err, notes))
			return
		}
		_ = db.AppendComment(ctx, a.Pool, card.ID, fmt.Sprintf("%s this card carries more than one pull "+
			"request and GitHub could not say which is open (%v). Briefing a reviewer on a guess would let it "+
			"approve one PR while auto-merge merges another, so nothing was started. Retrying later.",
			noReviewerPrefix, err), db.EventMeta{Actor: "service"})
		a.Hub.Broadcast(card.BoardID, "card_changed")
	default:
		a.flagReviewStuck(ctx, card,
			"Adversarial review: the reviewer brief failed to render, so no review can start — flagged stuck.")
	}
}

// newestAuthorActivity is the "has the author done anything since the last
// verdict?" timestamp the re-review debounce compares against.
//
// For a PR card that is worker-session activity, exactly as before. A spec
// card's author is usually a discuss session (that is what writes card
// bodies), and may be a human editing the body by hand with no session at
// all — so a non-service "updated" card event counts too, otherwise a
// hand-written spec would be reviewable exactly once, ever. Service-actored
// updates are excluded on purpose: spawning a reviewer links the session onto
// the card, which is itself an "updated" event, and counting it would make
// every card look freshly worked and re-review forever.
//
// Which edit, though, matters: the spec reviewer's artifact is the body, and
// this board's whole premise is that humans curate cards agents wrote — so
// tagging, re-prioritising or re-titling a card parked in review is normal
// curation, not the author revising the spec, and must not buy a fresh review
// round. db.UpdateCard records the fields a patch actually changed in the
// event's data, and only "body" counts here. Title is deliberately excluded
// even though the reviewer brief renders it: a retitle is curation, not the
// spec being revised.
//
// Which means, stated plainly because nothing else re-arms this: editing the
// body is the ONLY way to earn a re-review of a card whose verdict is
// current. Moving the card out of review and back does not — a "moved" event
// is not author activity — and neither does pressing Run, which reaches this
// same debounce through reviveStalledReviews. Retitling a spec whose title
// carried real meaning therefore needs a body edit to go with it; the
// comparison is exact, so any change to the body counts.
//
// Events written before that data existed carry no field list; they still
// count, so a card already sitting in review doesn't silently lose its ability
// to be re-reviewed at deploy time.
func (a *API) newestAuthorActivity(ctx context.Context, cardID string, kind reviewTargetKind) *time.Time {
	roles := []string{"worker"}
	if kind == reviewSpec {
		roles = append(roles, "discuss")
	}
	var at *time.Time
	_ = a.Pool.QueryRow(ctx, `
		SELECT max(last_activity_at) FROM runner_sessions
		WHERE card_id = $1 AND role = ANY($2)`, cardID, roles).Scan(&at)
	if kind != reviewSpec {
		return at
	}
	var edited *time.Time
	_ = a.Pool.QueryRow(ctx, `
		SELECT max(created_at) FROM card_events
		WHERE card_id = $1 AND type = 'updated' AND actor <> 'service'
		  AND (NOT jsonb_exists(data, 'fields') OR jsonb_exists(data->'fields', 'body'))`,
		cardID).Scan(&edited)
	if edited != nil && (at == nil || edited.After(*at)) {
		return edited
	}
	return at
}

// maybeRelayFindings: a card moved back into a work column while its newest
// adversarial verdict says request-changes → hand the findings to a session
// that can act on them: the worker for PR cards, worker or discuss for specs.
func (a *API) maybeRelayFindings(ctx context.Context, card db.Card) {
	events, err := db.ListCardEvents(ctx, a.Pool, card.ID, 0, 500)
	if err != nil {
		return
	}
	var findings string
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Type != "comment" {
			continue
		}
		var d struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(e.Data, &d)
		text := d.Text
		if strings.HasPrefix(text, "Adversarial review") {
			if m := verdictRE.FindStringSubmatch(text); m != nil && strings.HasPrefix(strings.ToLower(m[1]), "request") {
				findings = text
			}
			break // only the newest adversarial comment counts
		}
	}
	if findings == "" {
		return
	}
	// Who can act on these findings is decided by what was reviewed, NOT by
	// which session happens to be newest. A discuss session can be spawned on
	// any card at any time (mode:"discuss"), and idle discuss sessions are
	// reaped four times later than workers — so "newest live session" would
	// hand a code review to a session that cannot push, and leave the worker
	// that can uninformed. For a PR card the worker is the only valid
	// recipient, exactly as before this became target-aware.
	kind, _ := reviewTarget(card)
	roles := []string{"worker"}
	if kind == reviewSpec {
		roles = append(roles, "discuss")
	}
	var ext, role string
	err = a.Pool.QueryRow(ctx, `
		SELECT external_session_id, role FROM runner_sessions
		WHERE card_id = $1 AND role = ANY($2) AND lifecycle NOT IN ('stopped','error')
		ORDER BY created_at DESC LIMIT 1`, card.ID, roles).Scan(&ext, &role)
	if err != nil {
		return // no live author session — the findings sit on the card for the next one
	}
	// The artifact to fix comes from the card; who moves it back comes from
	// the session — discuss sessions are told not to move cards themselves.
	var todo string
	switch {
	case kind == reviewPR:
		todo = "Address the findings, update the PR, refresh the 'Review guide' comment " +
			"(including the Checks block), and move the card back to review."
	case role == "discuss":
		todo = "Address the findings by revising the card body (PATCH /api/cards/{id}), post a comment " +
			"summarising what you changed and why, and tell the human the spec is ready for re-review — " +
			"they move the card back to the review column, you don't."
	default: // a worker session that produced a spec rather than a PR
		todo = "Address the findings by revising the card body (PATCH /api/cards/{id}), post a comment " +
			"summarising what you changed and why, and move the card back to review."
	}
	msg := "Adversarial review requested changes on card #" + fmt.Sprint(card.Number) + ":\n\n" + findings +
		"\n\n" + todo
	if err := a.runner.Driver.Message(ctx, ext, msg, "blerg-board"); err != nil {
		log.Printf("auto-review: relay findings to %s: %v", role, err)
	}
}

// buildReviewPrompt renders the reviewer brief for whichever artifact this
// card actually produced — reviewer.md judges a PR diff, specreview.md judges
// the card body.
//
// For a PR card the brief names the PR mergeTargetPR would merge, so the
// reviewer judges the diff that auto-merge and Accept will act on.
//
// If that choice can't be made, the brief fails rather than naming a PR on a
// guess. The tempting fallback — keep the last link, a review of a plausible
// PR beats no review — is the bug this whole change exists to remove, just
// moved one site over: an approval carries no PR identity (hasFreshApproval
// matches the newest "Verdict: approve" comment and nothing else), so a
// reviewer briefed on the merged PR #3 writes the approval that the sweep
// then spends merging the open PR #6, and an unreviewed diff lands itself.
// Refusing to brief a reviewer costs a retry; briefing the wrong one costs
// the review.
func (a *API) buildReviewPrompt(ctx context.Context, board db.Board, card db.Card) (string, error) {
	kind, pr := reviewTarget(card)
	if kind == reviewPR {
		target, err := mergeTargetPR(ctx, card)
		if err != nil {
			return "", fmt.Errorf("%w: %w", errPickReviewPR, err)
		}
		// Unconditional, not "if target != """: reviewPR means the card has at
		// least one pr link, and mergeTargetPR only answers "" for a card with
		// none. A guard here would be dead code pretending the seed from
		// reviewTarget is still a fallback — it isn't, and that pretence is
		// what the reviewer would be briefed on if it ever came true.
		pr = target
	}
	role := "reviewer"
	if kind == reviewSpec {
		role = "specreview"
	}
	d := a.promptData()
	d.Card = cardInfo(card)
	d.Board = prompts.BoardInfo{Name: board.Name}
	d.PR = pr
	return a.Prompts.Render(role, d)
}

// ── accept: merge the PR, then the done-move ─────────────────────────────────

// handleAcceptCard is the human "Accept" action, with three outcomes: the PR
// merges (branch deleted, merge recorded, card moved to done — and cards
// without a PR take that path minus the merge); the PR conflicts with main and
// the card is bounced back to a worker to resolve it (202, card in the work
// column, nothing merged); or the merge fails for any other reason (502, card
// untouched).
func (a *API) handleAcceptCard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	// Admin-on-this-board before anything else; an unknown card is a 403
	// for a non-admin rather than a 404 that confirms the id.
	if !p.IsAdmin() {
		writeError(w, http.StatusForbidden, "accept requires board.admin")
		return
	}
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireAdmin(card.BoardID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	prURL, err := mergeTargetPR(r.Context(), card)
	if err != nil {
		// Only a card with two or more pr links can reach either branch:
		// mergeTargetPR answers a single link without asking GitHub, so the
		// ordinary card's Accept is exactly as offline as it ever was.
		if errors.Is(err, errNoOpenPR) {
			writeError(w, http.StatusConflict,
				"Every pull request linked to this card is already merged or closed on GitHub, so there is "+
					"nothing left to merge. Nothing was changed. Link the live PR to the card, or move it to "+
					"done by hand if the work already landed.")
			return
		}
		writeError(w, http.StatusBadGateway, fmt.Sprintf(
			"This card carries more than one pull request, and GitHub could not say which is open: %v. "+
				"Nothing was merged and the card is untouched — try again, or merge the right PR on GitHub.",
			err))
		return
	}
	note := "Accepted by human review."
	if prURL != "" {
		sha, mergeErr := mergePR(r.Context(), prURL)
		if mergeErr != nil {
			// A conflict with main is recoverable and blerg-board already knows how:
			// send the card back to a worker instead of handing the human a
			// GitHub status code. canBounceConflict gates the whole branch, so
			// a card that could never be bounced (no runner, no work column,
			// nobody to put on it) falls through to the plain failure instead
			// of explaining a recovery that isn't available to it.
			if isConflictErr(mergeErr) && a.canBounceConflict(r.Context(), card) {
				if card.RunAttempts >= conflictBounceLimit {
					// run_attempts is blerg-board's general worker-run counter, not a
					// conflict counter — say budget, don't claim N conflicts.
					writeError(w, http.StatusConflict, fmt.Sprintf(
						"The PR conflicts with main and this card has already used its retry budget (%d worker runs — dispatches, respawns and earlier conflict bounces all count), so it was not sent back again. Nothing was merged and the card stays here. This one needs hands: merge main into the branch, resolve the conflict, then Accept again.",
						conflictBounceLimit))
					return
				}
				if board, moved, res := a.bounceCardToWork(r.Context(), card, conflictHumanAccept); res == bounced {
					// the wake can spawn a pod — off the request path, and on a
					// context of its own so it survives the response
					wakeCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
					go func() {
						defer cancel()
						a.wakeWorkerForConflict(wakeCtx, board, moved, prURL, conflictHumanAccept)
					}()
					a.Hub.Broadcast(card.BoardID, "card_changed")
					writeJSON(w, http.StatusAccepted, map[string]string{
						"status": "bounced",
						"note":   "The PR conflicts with main, so nothing was merged. The card went back to the work column and a worker is on the conflict — it'll come back to review once the branch merges cleanly.",
					})
					return
				}
			}
			writeError(w, http.StatusBadGateway, "merge failed: "+mergeErr.Error())
			return
		}
		note = fmt.Sprintf("Accepted by human review — merged %s as %.7s.", prURL, sha)
		_, _ = a.Pool.Exec(r.Context(),
			`UPDATE cards SET merged_sha = $2, merged_at = now() WHERE id = $1`, card.ID, sha)
	}
	_ = db.AppendComment(r.Context(), a.Pool, card.ID, note, db.EventMeta{Actor: "human"})
	cols, err := db.ListColumns(r.Context(), a.Pool, card.BoardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	var doneID string
	for _, c := range cols {
		if c.Terminal() {
			doneID = c.ID
			break
		}
	}
	if doneID != "" && (card.ColumnID == nil || *card.ColumnID != doneID) {
		if _, err := db.MoveCard(r.Context(), a.Pool, card.ID, doneID, nil, nil,
			db.EventMeta{Actor: "human"}); err != nil {
			log.Printf("accept: move card #%d to done: %v", card.Number, err)
		}
	}
	// the card is done — stop any sessions still attached to it (best-effort;
	// settled sessions never reach a terminal lifecycle, interrupt is idempotent)
	if a.runner != nil {
		rows, err := a.Pool.Query(r.Context(), `
			SELECT external_session_id FROM runner_sessions
			WHERE card_id = $1 AND lifecycle NOT IN ('stopped','error')`, card.ID)
		if err == nil {
			var exts []string
			for rows.Next() {
				var e string
				if rows.Scan(&e) == nil {
					exts = append(exts, e)
				}
			}
			rows.Close()
			for _, e := range exts {
				if err := a.runner.Driver.Stop(r.Context(), e); err != nil {
					_ = a.runner.Driver.Interrupt(r.Context(), e)
				}
			}
		}
	}
	a.Hub.Broadcast(card.BoardID, "card_changed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted", "note": note})
}

// ghRequest issues an authenticated GitHub REST API request.
func ghRequest(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	return client.Do(req)
}

// ghGetJSON GETs url and decodes the JSON body into out. A non-2xx response
// is an error — never decoded, so a rate limit or auth failure can't be
// mistaken for a valid (zero-value) result by the caller.
func ghGetJSON(ctx context.Context, url string, out any) error {
	resp, err := ghRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github: GET %s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// githubAPIBase is the GitHub REST API root; overridden in tests.
var githubAPIBase = "https://api.github.com"

// mergePR merges a GitHub PR and deletes its head branch. Returns merge sha.
func mergePR(ctx context.Context, prURL string) (string, error) {
	m := prURLRE.FindStringSubmatch(prURL)
	if m == nil {
		return "", fmt.Errorf("unrecognized PR URL %q", prURL)
	}
	owner, repo, num := m[1], m[2], m[3]

	// head ref first, so the branch can be cleaned up after the merge
	var head string
	if resp, err := ghRequest(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/%s/pulls/%s", githubAPIBase, owner, repo, num), nil); err == nil {
		var pr struct {
			Head struct {
				Ref string `json:"ref"`
			} `json:"head"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&pr)
		_ = resp.Body.Close()
		head = pr.Head.Ref
	}

	resp, err := ghRequest(ctx, http.MethodPut,
		fmt.Sprintf("%s/repos/%s/%s/pulls/%s/merge", githubAPIBase, owner, repo, num),
		strings.NewReader(`{"merge_method":"merge"}`))
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		SHA     string `json:"sha"`
		Merged  bool   `json:"merged"`
		Message string `json:"message"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK || !out.Merged {
		return "", fmt.Errorf("github: HTTP %d: %s", resp.StatusCode, out.Message)
	}
	if head != "" && head != "main" && head != "master" {
		if resp, err := ghRequest(ctx, http.MethodDelete,
			fmt.Sprintf("%s/repos/%s/%s/git/refs/heads/%s", githubAPIBase, owner, repo, head), nil); err == nil {
			_ = resp.Body.Close()
		}
	}
	return out.SHA, nil
}

// ciState is the result of checking a PR head's CI: ciPending means checks
// haven't all reported yet (retry later), ciRed means at least one failed,
// ciNone means nothing reported at all.
type ciState int

const (
	// ciPending is deliberately the zero value: every error path below hands
	// it back, so a caller that drops the error still waits instead of merging.
	ciPending ciState = iota
	ciGreen
	ciRed
	// ciNone: neither the Checks API nor the legacy status API knows anything
	// about this head commit — what a repo with no CI wired up looks like.
	// Like ciPending it is not a merge signal, but unlike ciPending nothing is
	// coming: there is no run in flight to finish, so waiting is waiting
	// forever. Callers that only ask "is it green?" behave exactly as before;
	// callers that report to a human need the difference.
	ciNone
)

func (s ciState) String() string {
	switch s {
	case ciGreen:
		return "green"
	case ciRed:
		return "red"
	case ciNone:
		return "none"
	default:
		return "pending"
	}
}

// ciReport is everything one read of a PR head's CI found. The state alone
// answers "may this merge?", but a card that is NOT merging has to tell a
// human what broke and whether the answer is even new, and auto-merge has to
// know which head it is talking about before it may spend a re-run on it.
type ciReport struct {
	state ciState
	// headSHA is the commit the checks below belong to. It scopes everything
	// the sweep remembers about a red: a push produces a different SHA, which
	// is a different question with a fresh re-run budget.
	headSHA string
	// failed names the check runs / status contexts that came back non-green,
	// in GitHub's order. Empty for every state but ciRed.
	failed []string
	// newest is the most recent completion timestamp among the checks that
	// reported. On a red head nothing changes it until someone pushes or
	// re-runs, so its age is exactly "how stale is this failure" — the
	// difference between CI failing and CI having failed, once, hours ago.
	newest time.Time
	// unchecked marks a report the sweep promoted from ciNone to ciGreen
	// because the board's ci_policy is if_present. GitHub never said green —
	// nothing ran at all — and the merge comment has to say so, or the card
	// trail reads as if a machine check passed when none exists.
	//
	// Never set by prCIReport: this is the sweep's own annotation.
	unchecked bool
}

// prCIReport reads the combined CI state of a PR's head commit: GitHub
// Actions results (surfaced via the Checks API — the "GitHub statuses API"
// alone doesn't see Actions runs, only legacy external-CI statuses) plus any
// legacy commit status, the same two sources GitHub's own merge box combines.
//
// Unlike a plain "is it green?" it reads every check before answering, because
// a partial answer would be the wrong one twice over: the card can only name
// the failing job if all of them have been looked at, and an unfinished check
// outranks a finished failure. Pending outranking red matters — GitHub's merge
// box says pending there too, and rerun-failed-jobs refuses a workflow run
// that is still going, so treating a half-reported head as red would burn a
// re-run on a run that hasn't finished failing yet.
func prCIReport(ctx context.Context, prURL string) (ciReport, error) {
	m := prURLRE.FindStringSubmatch(prURL)
	if m == nil {
		return ciReport{}, fmt.Errorf("unrecognized PR URL %q", prURL)
	}
	owner, repo, num := m[1], m[2], m[3]

	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := ghGetJSON(ctx, fmt.Sprintf("%s/repos/%s/%s/pulls/%s", githubAPIBase, owner, repo, num), &pr); err != nil {
		return ciReport{}, err
	}
	if pr.Head.SHA == "" {
		return ciReport{}, fmt.Errorf("github: could not resolve PR head sha")
	}
	rep := ciReport{headSHA: pr.Head.SHA}

	seen, pending := false, false
	completed := func(ts string) {
		if t, err := time.Parse(time.RFC3339, ts); err == nil && t.After(rep.newest) {
			rep.newest = t
		}
	}

	var checks struct {
		CheckRuns []struct {
			Name        string `json:"name"`
			Status      string `json:"status"`
			Conclusion  string `json:"conclusion"`
			CompletedAt string `json:"completed_at"`
		} `json:"check_runs"`
	}
	// filter=latest is GitHub's default, but spell it out: it is what makes a
	// re-run visible here. Re-running a job adds a new attempt to the same
	// head, and only the latest-per-name view retires the failure that
	// prompted the re-run. Under filter=all the old red would be returned
	// forever alongside the new green, and the self-healing path below would
	// never clear a thing.
	if err := ghGetJSON(ctx,
		fmt.Sprintf("%s/repos/%s/%s/commits/%s/check-runs?filter=latest&per_page=100",
			githubAPIBase, owner, repo, pr.Head.SHA),
		&checks); err != nil {
		return ciReport{headSHA: pr.Head.SHA}, err
	}
	for _, c := range checks.CheckRuns {
		seen = true
		if c.Status != "completed" {
			pending = true
			continue
		}
		completed(c.CompletedAt)
		switch c.Conclusion {
		case "success", "neutral", "skipped":
		default:
			rep.failed = append(rep.failed, checkLabel(c.Name, c.Conclusion))
		}
	}

	var combined struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
		Statuses   []struct {
			State     string `json:"state"`
			Context   string `json:"context"`
			UpdatedAt string `json:"updated_at"`
		} `json:"statuses"`
	}
	if err := ghGetJSON(ctx,
		fmt.Sprintf("%s/repos/%s/%s/commits/%s/status", githubAPIBase, owner, repo, pr.Head.SHA),
		&combined); err != nil {
		return ciReport{headSHA: pr.Head.SHA}, err
	}
	if combined.TotalCount > 0 {
		seen = true
		for _, s := range combined.Statuses {
			switch s.State {
			case "failure", "error":
				completed(s.UpdatedAt)
				rep.failed = append(rep.failed, checkLabel(s.Context, s.State))
			case "success":
				completed(s.UpdatedAt)
			}
		}
		switch combined.State {
		case "success":
		case "failure", "error":
			// the combined verdict is red even if per-status detail was
			// unavailable, so trust it over the loop above
			if len(rep.failed) == 0 {
				rep.failed = append(rep.failed, "a commit status")
			}
		default:
			pending = true
		}
	}

	switch {
	case pending:
		rep.state = ciPending
		rep.failed = nil // not the card's business until every check has reported
	case len(rep.failed) > 0:
		rep.state = ciRed
	case !seen:
		rep.state = ciNone
	default:
		rep.state = ciGreen
	}
	return rep, nil
}

// checkLabel renders one failing check the way the card should read it: the
// job name, plus the conclusion when it is something other than a plain
// failure, because "cancelled" and "timed out" send the reader somewhere
// different from "the assertions failed".
func checkLabel(name, conclusion string) string {
	if name == "" {
		name = "an unnamed check"
	}
	switch conclusion {
	case "", "failure":
		return name
	default:
		return name + " (" + strings.ReplaceAll(conclusion, "_", " ") + ")"
	}
}

// shortSHA abbreviates a commit the way git and the GitHub UI do. Used in
// comment prefixes, so it must be stable: the same head has to produce the
// same prefix on every sweep or the rate limit never matches itself.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// rerunFailedCIJobs asks GitHub to re-run the failed jobs of every Actions
// workflow run sitting on this head, and returns the names of the runs it
// re-ran. This is the whole self-healing mechanism: a flake and a real
// breakage are indistinguishable from one red result, and the only thing that
// tells them apart is running it again.
//
// Only the failed jobs are re-run, not the whole run, so a long green job
// isn't paid for twice; GitHub reuses the same run id, so the check-runs the
// next sweep reads go back to "in progress" on the same head rather than
// appearing as a second, competing result.
//
// A run that is not completed is skipped — GitHub rejects rerun-failed-jobs
// for one still in flight, and prCIReport would have called the head pending
// anyway. Errors from individual runs are collected rather than returned
// immediately: re-running three of four workflows is still progress.
func rerunFailedCIJobs(ctx context.Context, prURL, headSHA string) ([]string, error) {
	m := prURLRE.FindStringSubmatch(prURL)
	if m == nil {
		return nil, fmt.Errorf("unrecognized PR URL %q", prURL)
	}
	owner, repo := m[1], m[2]

	var runs struct {
		WorkflowRuns []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if err := ghGetJSON(ctx, fmt.Sprintf("%s/repos/%s/%s/actions/runs?head_sha=%s&per_page=100",
		githubAPIBase, owner, repo, headSHA), &runs); err != nil {
		return nil, err
	}

	var rerun []string
	var firstErr error
	for _, r := range runs.WorkflowRuns {
		if r.Status != "completed" {
			continue
		}
		switch r.Conclusion {
		case "failure", "timed_out", "cancelled", "startup_failure":
		default:
			continue
		}
		resp, err := ghRequest(ctx, http.MethodPost,
			fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d/rerun-failed-jobs", githubAPIBase, owner, repo, r.ID), nil)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		code := resp.StatusCode
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if code < 200 || code >= 300 {
			if firstErr == nil {
				firstErr = fmt.Errorf("github: rerun run %d: HTTP %d: %s", r.ID, code, strings.TrimSpace(string(body)))
			}
			continue
		}
		name := r.Name
		if name == "" {
			name = fmt.Sprintf("run %d", r.ID)
		}
		rerun = append(rerun, name)
	}
	if len(rerun) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return rerun, nil
}

// ── deployment signals ───────────────────────────────────────────────────────

// handleRecordDeployment lets deploy systems (a deploy script, CI)
// report per-environment state: POST {board_id, env, sha, status, detail}.
func (a *API) handleRecordDeployment(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var req struct {
		BoardID string `json:"board_id"`
		Env     string `json:"env"`
		SHA     string `json:"sha"`
		Status  string `json:"status"`
		Detail  string `json:"detail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BoardID == "" || req.Env == "" {
		writeError(w, http.StatusBadRequest, "board_id and env required")
		return
	}
	if err := p.RequireAdmin(req.BoardID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if req.Status == "" {
		req.Status = "ok"
	}
	if _, err := a.Pool.Exec(r.Context(), `
		INSERT INTO deployments (board_id, env, sha, status, detail)
		VALUES ($1,$2,$3,$4,$5)`,
		req.BoardID, req.Env, req.SHA, req.Status, req.Detail); err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(req.BoardID, "card_changed")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "recorded"})
}

// handleBoardDeployments returns the latest deployment per environment.
func (a *API) handleBoardDeployments(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	rows, err := a.Pool.Query(r.Context(), `
		SELECT DISTINCT ON (env) env, sha, status, detail, created_at
		FROM deployments WHERE board_id = $1
		ORDER BY env, created_at DESC`, boardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	type dep struct {
		Env       string    `json:"env"`
		SHA       string    `json:"sha"`
		Status    string    `json:"status"`
		Detail    string    `json:"detail"`
		CreatedAt time.Time `json:"created_at"`
	}
	out := []dep{}
	for rows.Next() {
		var d dep
		if rows.Scan(&d.Env, &d.SHA, &d.Status, &d.Detail, &d.CreatedAt) == nil {
			out = append(out, d)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
