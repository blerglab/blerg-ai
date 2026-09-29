package api

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Runner capacity, remembered.
//
// The runner has a global session cap (blerg-runner: "cluster session cap reached
// (12)"). Hitting it says nothing about the card whose spawn was refused, and
// there is no cheap way to ask the runner in advance — blerg-runner has no capacity
// route today, and answers unknown paths with its single-page app rather than
// a 404, so probing for one is not free either. What blerg-board can do is remember
// the last refusal: one spawn attempt establishes there is no slot, and every
// dispatch pass after it can read that answer out of Postgres for free instead
// of asking the runner again 20 seconds later.
//
// The record is process-independent on purpose — dispatch runs in every
// replica, and the runner they are all filling up is the same one.

// capacityBackoff is how long a refusal keeps dispatch off the runner's back.
// It is a ceiling, not a delay: runnerOutOfSlots stops reporting "full" the
// moment a session blerg-board is tracking settles, so a slot blerg-board can see free up
// is used on the very next tick. The ceiling is for the slots blerg-board CANNOT
// see — the cap is cluster-wide, and sessions blerg-board never started give slots
// back without blerg-board hearing about it.
const capacityBackoff = 2 * time.Minute

// detailMax bounds the runner's own words in the record. Generous — this is a
// breadcrumb for whoever reads the row, not a wire format.
const detailMax = 500

// safeDetail makes the runner's words storable, whatever they are.
//
// `detail` is decoration: nothing reads it back, and the row it rides on is
// the entire mechanism keeping dispatch off a full runner. So it must never
// be the reason the INSERT fails — and text Postgres refuses is exactly how
// that happens. A UTF8 database rejects an invalid byte sequence outright
// (SQLSTATE 22021) and takes the whole statement with it; noteCapacityRefusal
// can only log that, so the refusal goes unrecorded and the next tick asks a
// runner that is still full, 20 seconds later, and again after that.
//
// Two ways the bytes go bad, both reachable:
//   - Cutting at a byte offset splits a multibyte rune. BlergRunner.do formats
//     the runner's response body with %.200s, and Go counts RUNES there, so a
//     non-ASCII 503 page (an ingress error page, say) yields an error string
//     of up to ~840 bytes — over this limit, with the cut landing mid-rune.
//   - The body was never valid UTF-8 to begin with (a latin-1 error page).
//     Length has nothing to do with that one.
//
// So: drop NULs (text cannot hold them either), scrub invalid sequences, and
// only then cut — on a rune boundary.
func safeDetail(s string) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")
	if len(s) <= detailMax {
		return s
	}
	cut := detailMax
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// noteCapacityRefusal records that the runner turned a spawn away for lack of
// slots. Called from spawnSession, so it does not matter which caller ran into
// the cap — a reviewer spawn discovering the runner is full is exactly as good
// an answer for dispatch as a worker spawn discovering it.
func (a *API) noteCapacityRefusal(ctx context.Context, err error) {
	detail := ""
	if err != nil {
		detail = safeDetail(err.Error())
	}
	if _, e := a.Pool.Exec(ctx, `
		INSERT INTO runner_capacity (id, refused_at, detail) VALUES (true, now(), $1)
		ON CONFLICT (id) DO UPDATE SET refused_at = now(), detail = EXCLUDED.detail`,
		detail); e != nil {
		log.Printf("runner capacity: record refusal: %v", e)
	}
}

// noteCapacityAvailable forgets the last refusal: a spawn just succeeded, so
// whatever the runner was full of, it was not full of it a moment ago. That
// slot may well have been the last one — the next refusal will say so, and it
// costs nothing now that dispatch no longer claims a card to find out.
func (a *API) noteCapacityAvailable(ctx context.Context) {
	if _, err := a.Pool.Exec(ctx, `DELETE FROM runner_capacity`); err != nil {
		log.Printf("runner capacity: clear refusal: %v", err)
	}
}

// runnerOutOfSlots answers "is it worth asking the runner to start a session?"
// without asking it. False whenever blerg-board does not know better: no refusal on
// record, one old enough to have expired, or a session that has settled since
// it — a settled session is a freed slot, which is what makes the wait end on
// the first tick after capacity comes back rather than at the end of the
// window.
//
// A stopped session whose row is merely touched after the refusal (a late
// ingest pass) reads as a freed slot too. That costs one spawn attempt, which
// either succeeds — so the slot was real — or is refused and re-arms the
// window. No card is touched either way.
func (a *API) runnerOutOfSlots(ctx context.Context) bool {
	var full bool
	err := a.Pool.QueryRow(ctx, `
		SELECT rc.refused_at > now() - $1::interval
		   AND NOT EXISTS (
			SELECT 1 FROM runner_sessions rs
			WHERE rs.lifecycle IN ('stopped', 'error')
			  AND rs.last_activity_at > rc.refused_at)
		FROM runner_capacity rc`, capacityBackoff.String()).Scan(&full)
	if errors.Is(err, pgx.ErrNoRows) {
		return false // nothing on record: the runner has never said no
	}
	if err != nil {
		// Not knowing is not a reason to stop dispatching: fall through and
		// let the runner answer for itself, as it did before this record
		// existed.
		log.Printf("runner capacity: read last refusal: %v", err)
		return false
	}
	return full
}
