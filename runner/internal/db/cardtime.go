package db

// Card-time attribution (BLG-444, docs/design/card-time.md §3). Deliberately does NOT reuse
// InsightActivityScope's/CardData's ancestor-walking CTEs: those answer "whose account is this
// session's work," a different question from "did THIS session itself do work on THIS card" (H1).
// A session counts here only when it has its OWN session_card_links row for the exact card, is
// not a root/coordinator session, and the query below finds it linked to exactly one card — never
// inherited from or credited to an ancestor.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"github.com/blerglab/blerg-ai/runner/internal/cardtime"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultCardTimeBudgetIntervalHours is the default interval (owner's decision 3: 4 hours).
const DefaultCardTimeBudgetIntervalHours = 4

// CardTimeBudgetIntervalHours reads accountID's own budget interval, defaulting when unset.
// Per-account (owner's decision 4, corrected: "account-level" means each account's own setting,
// not one install-wide value — account A must never see or change account B's interval; M4).
func CardTimeBudgetIntervalHours(ctx context.Context, pool *pgxpool.Pool, accountID string) (int, error) {
	var hours int
	err := pool.QueryRow(ctx, `SELECT interval_hours FROM card_time_budget_settings WHERE account_id=$1`, accountID).Scan(&hours)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultCardTimeBudgetIntervalHours, nil
	}
	if err != nil {
		return DefaultCardTimeBudgetIntervalHours, err
	}
	return hours, nil
}

// SetCardTimeBudgetIntervalHours stores accountID's own budget interval, recording who changed it.
func SetCardTimeBudgetIntervalHours(ctx context.Context, pool *pgxpool.Pool, accountID string, hours int, by string) error {
	_, err := pool.Exec(ctx, `INSERT INTO card_time_budget_settings(account_id,interval_hours,updated_by,updated_at)
 VALUES($1,$2,$3,now()) ON CONFLICT(account_id) DO UPDATE SET interval_hours=EXCLUDED.interval_hours,updated_by=EXCLUDED.updated_by,updated_at=now()`,
		accountID, hours, by)
	return err
}

// FiredCardTimeThresholds is the set of absolute budget thresholds (whole seconds) already
// recorded as fired for (accountID,boardID,cardNumber) — the durable "already fired" lock (M7),
// keyed on each threshold's own absolute value so a later change to the interval can never
// re-derive and re-fire one already recorded (owner's correction; see cardtime.ThresholdsCrossed).
func FiredCardTimeThresholds(ctx context.Context, q Querier, accountID, boardID string, cardNumber int) (map[int64]bool, error) {
	rows, err := q.Query(ctx, `SELECT threshold_seconds FROM card_time_threshold_alerts WHERE account_id=$1 AND board_id=$2 AND card_number=$3`,
		accountID, boardID, cardNumber)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var threshold int64
		if err := rows.Scan(&threshold); err != nil {
			return nil, err
		}
		out[threshold] = true
	}
	return out, rows.Err()
}

// RecordCardTimeCrossings inserts every newly-reached threshold's dedup row and, for each one
// actually inserted (not a conflict with a concurrent writer), the corresponding plan-drift fact
// for rootSessionID — the worker's root ancestor, never the worker itself (H2). q must be a
// transaction: the dedup rows and the plan facts must commit together or not at all (M7 commit
// atomicity; not to be confused with the migration-numbering M7).
func RecordCardTimeCrossings(ctx context.Context, q Querier, accountID, boardID string, cardNumber int, rootSessionID string, thresholds []int64, textFor func(thresholdSeconds int64) string) error {
	for _, threshold := range thresholds {
		tag, err := q.Exec(ctx, `INSERT INTO card_time_threshold_alerts(account_id,board_id,card_number,threshold_seconds) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			accountID, boardID, cardNumber, threshold)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue // already fired by a concurrent check
		}
		key := "cardtime:" + accountID + ":" + boardID + ":" + strconv.Itoa(cardNumber) + ":" + strconv.FormatInt(threshold, 10)
		if err := AddPlanFact(ctx, q, rootSessionID, key, textFor(threshold)); err != nil {
			return err
		}
	}
	return nil
}

func logCardTimeCrossing(err error) {
	if err != nil {
		failurelog.Default.Event("card_time_crossing_check", slog.LevelWarn, "operation", "card_time_crossing_check", "cause", failurelog.Cause(err))
	}
}

// CheckCardTimeCrossing re-evaluates every card sessionID is directly linked to and records any
// newly-reached budget threshold, for sessionID's root ancestor (H2). Call this only once
// sessionID's own timing has actually been materialized into session_insight_timings — from
// BLG-424's insight-timing queue drain (ProcessQueuedInsightTimings) or its ended-session backfill
// (backfillInsightTiming) — never at turn-end itself (Codex round 3 H): turn-end only QUEUES that
// materialization (agentevents.go), it never waits for it, so a read made independently at
// turn-end would almost always see "pending" and, with nothing else ever retrying on its behalf, a
// worker finishing its last turn above budget could produce no notice at all. Both call sites
// above already run on their own goroutine, decoupled from any request or turn-check, so M3/H2
// (never suppress or delay unrelated work) hold without this function doing anything special.
func CheckCardTimeCrossing(ctx context.Context, pool *pgxpool.Pool, sessionID string) error {
	var parentID *string
	var accountID *string
	err := pool.QueryRow(ctx, `SELECT parent_session_id, spawning_account_id FROM sessions WHERE id=$1`, sessionID).Scan(&parentID, &accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if parentID == nil || accountID == nil {
		return nil // a root/coordinator session's own materialization is not "a worker's" turn.
	}
	accountIDv := *accountID
	links, err := SessionCardLinks(ctx, pool, sessionID)
	if err != nil || len(links) == 0 {
		return err
	}
	intervalHours, err := CardTimeBudgetIntervalHours(ctx, pool, accountIDv)
	if err != nil {
		return err
	}
	var rootID string
	for _, link := range links {
		seconds, ok, pending, err := CardTimeTotal(ctx, pool, accountIDv, link.BoardID, link.CardNumber)
		if err != nil {
			return err
		}
		if !ok || pending {
			continue
		}
		fired, err := FiredCardTimeThresholds(ctx, pool, accountIDv, link.BoardID, link.CardNumber)
		if err != nil {
			return err
		}
		thresholds := cardtime.ThresholdsCrossed(seconds, intervalHours, fired)
		if len(thresholds) == 0 {
			continue
		}
		marks, err := CardReviewMarks(ctx, pool, accountIDv, link.BoardID, link.CardNumber)
		if err != nil {
			return err
		}
		rounds := cardtime.Rounds(marks)
		if rootID == "" {
			rootID, err = RootSessionID(ctx, pool, sessionID)
			if err != nil {
				return err
			}
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		boardID, cardNumber := link.BoardID, link.CardNumber
		err = RecordCardTimeCrossings(ctx, tx, accountIDv, boardID, cardNumber, rootID, thresholds, func(threshold int64) string {
			return cardtime.NoticeText(boardID, cardNumber, threshold, rounds)
		})
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// CardLink is one (board,card) a session has its own direct session_card_links row for.
type CardLink struct {
	BoardID    string
	CardNumber int
}

// SessionCardLinks is the distinct set of cards sessionID is itself directly linked to (any
// source). Used at that session's own turn-end to decide which cards' budgets to recheck.
func SessionCardLinks(ctx context.Context, q Querier, sessionID string) ([]CardLink, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT board_id, card_number FROM session_card_links WHERE session_id=$1`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CardLink
	for rows.Next() {
		var l CardLink
		if err := rows.Scan(&l.BoardID, &l.CardNumber); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CardTimeTotal is the card's current worker-time total, read as ONE bounded query with no
// stored intermediate state (Codex round 2 redesign: a separately-maintained ledger drifted from
// the live session_card_links/session_insight_timings truth through check-then-insert races and
// an invalidation trigger that cleared eligibility going one way but never restored it going the
// other. A ledger is gone entirely). Eligibility — direct link, not root, linked to exactly this
// one card, account-scoped (H1/M5) — and the sum are evaluated together in the same statement, so
// there is nothing to race and nothing to restore: a session that becomes shared or stops being
// shared is simply included or excluded by the very next read.
//
// Three outcomes:
//   - known: every eligible session's timing is cached (session_insight_timings) and trustworthy
//     -> (seconds, ok=true, pending=false, nil).
//   - pending: at least one eligible session's timing is not cached yet — a still-running session
//     (BLG-424 never caches a live session's timing), or an ended one not yet backfilled — so the
//     total is knowingly incomplete and must never be reported, shown or acted on as zero
//     -> (0, ok=false, pending=true, nil).
//   - nothing: no eligible sessions at all, or an eligible session's cached timing itself carries
//     unclassified ("unknown") seconds or unknown native active time (a confirmed data-quality
//     problem, not merely "not computed yet") -> (0, ok=false, pending=false, nil).
func CardTimeTotal(ctx context.Context, pool *pgxpool.Pool, accountID, boardID string, cardNumber int) (seconds float64, ok, pending bool, err error) {
	rows, err := pool.Query(ctx, `
WITH direct AS (
 SELECT DISTINCT l.session_id, s.insight_pending FROM session_card_links l JOIN sessions s ON s.id=l.session_id
 WHERE l.board_id=$2 AND l.card_number=$3 AND s.spawning_account_id=$1 AND s.parent_session_id IS NOT NULL
), eligible AS (
 SELECT d.session_id, d.insight_pending FROM direct d
 WHERE (SELECT count(DISTINCT (l2.board_id,l2.card_number)) FROM session_card_links l2 WHERE l2.session_id=d.session_id)=1
)
SELECT e.insight_pending, c.timing,
 EXISTS(SELECT 1 FROM agent_events ev WHERE ev.session_id=e.session_id AND ev.kind='subagent_started')
FROM eligible e LEFT JOIN session_insight_timings c ON c.session_id=e.session_id`,
		accountID, boardID, cardNumber)
	if err != nil {
		return 0, false, false, err
	}
	defer rows.Close()
	found, disqualified := false, false
	for rows.Next() {
		found = true
		var insightPending bool
		var raw json.RawMessage
		var nativeUnknown bool
		if err := rows.Scan(&insightPending, &raw, &nativeUnknown); err != nil {
			return 0, false, false, err
		}
		// sessions.insight_pending (139_insights_pending.sql) is the authoritative signal now —
		// not merely "no cached row at all" but also "a cached row exists but its fingerprint
		// version is stale, or it was invalidated and not yet re-materialized." A stale cached
		// value must never be summed as if it were current.
		if insightPending || raw == nil {
			pending = true
			continue
		}
		var t activity.Timing
		if err := json.Unmarshal(raw, &t); err != nil {
			return 0, false, false, err
		}
		active, unknownSeconds := cardtime.ActiveSecondsFromTiming(t)
		if nativeUnknown || unknownSeconds > 0 {
			disqualified = true
			continue
		}
		seconds += active
	}
	if err := rows.Err(); err != nil {
		return 0, false, false, err
	}
	switch {
	case !found, disqualified:
		return 0, false, false, nil
	case pending:
		return 0, false, true, nil
	default:
		return seconds, true, false, nil
	}
}

// CardReviewMarks reads accepted review marks for (boardID,cardNumber), scoped to accountID (M5),
// ordered oldest first. Reuses the same (board_id,card) key `HandlePostMarks` already enforces a
// mark was accepted under (`marks.go`'s allowed-check), and — like `carddata.go`'s own card-mark
// reads — compares card as text against the existing agent_events_review_card_idx index
// expression ((payload->>'board_id'),(payload->>'card'),session_id): casting the extracted text
// to int (as this query previously did) is a different expression than the index's own, so
// Postgres could never match it and fell back to a sequential scan.
func CardReviewMarks(ctx context.Context, q Querier, accountID, boardID string, cardNumber int) ([]cardtime.ReviewMark, error) {
	rows, err := q.Query(ctx, `
 SELECT e.ts, coalesce((e.payload->>'round')::int,0), coalesce(e.payload->>'verdict',''),
   coalesce((SELECT array_agg(f) FROM jsonb_array_elements_text(e.payload->'findings') f),'{}')
 FROM agent_events e JOIN sessions s ON s.id=e.session_id
 WHERE s.spawning_account_id=$1 AND e.kind='mark' AND e.payload->>'kind'='review'
   AND e.payload->>'board_id'=$2 AND e.payload->>'card'=$3::text
 ORDER BY e.ts`, accountID, boardID, strconv.Itoa(cardNumber))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cardtime.ReviewMark
	for rows.Next() {
		var m cardtime.ReviewMark
		var at time.Time
		if err := rows.Scan(&at, &m.Round, &m.Verdict, &m.Findings); err != nil {
			return nil, err
		}
		m.At = at
		out = append(out, m)
	}
	return out, rows.Err()
}

// RootSessionID walks parent_session_id up from sessionID to the session with none — the
// workspace's Main/coordinator (H2: the plan-drift fact for a worker's crossing belongs to its
// root, never the worker itself). Returns sessionID unchanged if it is already a root or is not
// found. Bounded to 64 hops to tolerate a cyclic/corrupt chain rather than looping forever.
func RootSessionID(ctx context.Context, q Querier, sessionID string) (string, error) {
	current := sessionID
	for range 64 {
		var parent *string
		err := q.QueryRow(ctx, `SELECT parent_session_id FROM sessions WHERE id=$1`, current).Scan(&parent)
		if err != nil {
			return sessionID, err
		}
		if parent == nil {
			return current, nil
		}
		current = *parent
	}
	return current, nil
}
