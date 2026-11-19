package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// seedCardTimeSession links sid to (board,number), gives it a short real run, and materializes
// its timing cache via RefreshInsightTiming — CardTimeTotal only ever reads
// session_insight_timings now, so a test needs a real cached session, not a hand-inserted ledger
// row (the ledger is gone). Link happens before the run, matching the order BLG-424's own
// invalidate_insight_links trigger expects in practice (see internal/db/cardtime_test.go).
func seedCardTimeSession(t *testing.T, pool *pgxpool.Pool, sid, board string, number int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := pool.Exec(ctx, `INSERT INTO session_card_links VALUES($1,$2,$3,'pin',$4) ON CONFLICT DO NOTHING`, sid, board, number, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET started_at=$2,ended_at=$2::timestamptz+interval '4 hours' WHERE id=$1`, sid, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEventAt(ctx, pool, sid, newUUID(), "status_changed", `{"status":"running"}`, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEventAt(ctx, pool, sid, newUUID(), "turn_done", `{}`, now.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshInsightTiming(ctx, pool, sid); err != nil {
		t.Fatal(err)
	}
}

// Codex round 3: the glyph (and this route) must show a card's total and round count whenever it
// HAS linked work, even when that total has never crossed a budget threshold — only the
// plan-drift *notice* is gated on crossing a multiple; the glyph is not. seedCardTimeSession's
// session runs for exactly the default 4h interval with no card_time_threshold_alerts row ever
// written (nothing here ever runs the crossing check), so this would have come back "{}" under
// the old fired-only gate; it must not any more.
func TestCardTimeSummaryShowsKnownTotalEvenUnderBudget(t *testing.T) {
	fx := newTopicFx(t)
	api, _, _ := newLiveCardAPI(t)
	api.dbPool = fx.pool
	api.coreAuth = fx.api.coreAuth
	mux := http.NewServeMux()
	api.RegisterCardTimeSummaryRoute(mux)
	ctx := context.Background()
	// H1 excludes a root (parentless) session; fx.sess must look like a worker, not a Main.
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET parent_session_id=$2 WHERE id=$1`, fx.sess, fx.ended); err != nil {
		t.Fatal(err)
	}
	seedCardTimeSession(t, fx.pool, fx.sess, "board-a", 7)

	req := httptest.NewRequest("GET", "/api/board-cards/board-a/7/card-time", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"seconds":14400`, `"rounds":0`} {
		if !strings.Contains(body, want) {
			t.Fatalf("got %s, want it to contain %s (a known total shows even with no fired threshold)", body, want)
		}
	}
}

// No linked work at all: the total genuinely isn't known (CardTimeTotal finds no eligible
// session), so the route must still answer with nothing, not a zero.
func TestCardTimeSummaryNoLinkedWorkShowsNothing(t *testing.T) {
	fx := newTopicFx(t)
	api, _, _ := newLiveCardAPI(t)
	api.dbPool = fx.pool
	api.coreAuth = fx.api.coreAuth
	mux := http.NewServeMux()
	api.RegisterCardTimeSummaryRoute(mux)

	req := httptest.NewRequest("GET", "/api/board-cards/board-a/7/card-time", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200: %s", w.Code, w.Body.String())
	}
	body := strings.TrimSpace(w.Body.String())
	if body != "{}" {
		t.Fatalf("got %q, want {} for a card with no linked work", body)
	}
}

// H1: boardCardRequest alone (project scope, a project-limited sign-in's own board) is not a live
// visibility check. An account whose board access was revoked (or never granted — the board
// itself refuses the call) must be denied, never shown a total, even with fired thresholds and a
// ledger entry already sitting in the database for that card.
func TestCardTimeSummaryDeniedWhenBoardRefusesLiveAccess(t *testing.T) {
	fx := newTopicFx(t)
	api, board, _ := newLiveCardAPI(t)
	api.dbPool = fx.pool
	api.coreAuth = fx.api.coreAuth
	mux := http.NewServeMux()
	api.RegisterCardTimeSummaryRoute(mux)

	ctx := context.Background()
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET parent_session_id=$2 WHERE id=$1`, fx.sess, fx.ended); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO card_time_threshold_alerts(account_id,board_id,card_number,threshold_seconds) VALUES($1,'board-a',7,14400)`, topicOwner); err != nil {
		t.Fatal(err)
	}
	seedCardTimeSession(t, fx.pool, fx.sess, "board-a", 7)

	board.refuse = true
	req := httptest.NewRequest("GET", "/api/board-cards/board-a/7/card-time", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("board refused live access but the route answered 200: %s (H1: a revoked board grant must deny totals, not just skip the glyph)", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "14400") || strings.Contains(w.Body.String(), "seconds") {
		t.Fatalf("a denied board leaked card-time data: %s", w.Body.String())
	}
}

// Over budget: a fired multiple, a ledger total and a review mark all surface, and only to the
// owner — never another account's view of the same board/card number.
func TestCardTimeSummaryOverBudgetShowsOwnerScopedDetail(t *testing.T) {
	fx := newTopicFx(t)
	api, _, _ := newLiveCardAPI(t)
	api.dbPool = fx.pool
	api.coreAuth = fx.api.coreAuth
	mux := http.NewServeMux()
	api.RegisterCardTimeSummaryRoute(mux)

	ctx := context.Background()
	// H1 excludes a root (parentless) session; fx.sess must look like a worker, not a Main.
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET parent_session_id=$2 WHERE id=$1`, fx.sess, fx.ended); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO card_time_threshold_alerts(account_id,board_id,card_number,threshold_seconds) VALUES($1,'board-a',7,14400)`, topicOwner); err != nil {
		t.Fatal(err)
	}
	seedCardTimeSession(t, fx.pool, fx.sess, "board-a", 7)
	if _, _, err := db.AppendAgentEventAt(ctx, fx.pool, fx.sess, newUUID(), "mark",
		`{"kind":"review","text":"Review","by":"codex","board_id":"board-a","card":7,"verdict":"changes","target":"abcdef1","round":1,"findings":["high","medium","medium"]}`,
		time.Now()); err != nil {
		t.Fatal(err)
	}
	// BLG-424's own invalidate_insight_card_mark trigger clears cached timing for a card's linked
	// sessions when a review/failure mark lands (its rework classification may now differ); the
	// session's own next turn/refresh re-materializes it, simulated here explicitly.
	if err := db.RefreshInsightTiming(ctx, fx.pool, fx.sess); err != nil {
		t.Fatal(err)
	}

	get := func(account string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/board-cards/board-a/7/card-time", nil)
		req.Header.Set("Authorization", "Bearer "+fx.token(account, "human"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	w := get(topicOwner)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"seconds":14400`, `"rounds":1`, `"verdict":"changes"`, `"open_high":1`, `"open_medium":2`} {
		if !strings.Contains(body, want) {
			t.Fatalf("got %s, want it to contain %s", body, want)
		}
	}

	// A different account must never see this account's card-time detail for the same board/number.
	w = get(topicOther)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"seconds"`) {
		t.Fatalf("other account saw card-time detail: %d %s", w.Code, w.Body.String())
	}
}
