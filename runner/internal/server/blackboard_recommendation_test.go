package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
)

// TestEvaluateBlackboardComplexity is BLG-454's own acceptance line: "thresholds are data with
// tests." Every threshold is exercised at its own boundary, on its own, and combined with another
// — the decision never touches a database or a board, so this is the whole contract.
func TestEvaluateBlackboardComplexity(t *testing.T) {
	cases := []struct {
		name    string
		metrics blackboardComplexityMetrics
		crossed bool
		reason  string
	}{
		{"nothing crossed", blackboardComplexityMetrics{CardsLinked: 7, DaysSpan: 2, RunningWorkers: 2}, false, ""},
		{"cards just under", blackboardComplexityMetrics{CardsLinked: 7}, false, ""},
		{"cards at threshold", blackboardComplexityMetrics{CardsLinked: 8}, true, "8 cards in progress or linked"},
		{"cards over threshold", blackboardComplexityMetrics{CardsLinked: 12}, true, "12 cards in progress or linked"},
		{"days just under", blackboardComplexityMetrics{DaysSpan: 2}, false, ""},
		{"days at threshold", blackboardComplexityMetrics{DaysSpan: 3}, true, "work spanning 3 days"},
		{"release linked", blackboardComplexityMetrics{LinkedRelease: true}, true, "a linked board release"},
		{"release not linked", blackboardComplexityMetrics{LinkedRelease: false}, false, ""},
		{"workers just under", blackboardComplexityMetrics{RunningWorkers: 2}, false, ""},
		{"workers at threshold", blackboardComplexityMetrics{RunningWorkers: 3}, true, "3 workers running at once"},
		{"two thresholds combine into one line", blackboardComplexityMetrics{CardsLinked: 8, RunningWorkers: 3}, true, "8 cards in progress or linked, 3 workers running at once"},
		{"all four thresholds", blackboardComplexityMetrics{CardsLinked: 9, DaysSpan: 4, LinkedRelease: true, RunningWorkers: 5}, true, "9 cards in progress or linked, work spanning 4 days, a linked board release, 5 workers running at once"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			crossed, reason := evaluateBlackboardComplexity(c.metrics)
			if crossed != c.crossed || reason != c.reason {
				t.Fatalf("evaluateBlackboardComplexity(%+v) = %v, %q; want %v, %q", c.metrics, crossed, reason, c.crossed, c.reason)
			}
		})
	}
}

// TestMaybeRecommendBlackboardFiresAtWorkerThreshold is the deterministic signal end to end: three
// running workers in a workspace with no blackboard produce exactly one pending recommendation,
// and a second check (simulating a later trigger, e.g. after a daemon restart, with a brand new
// API value sharing only the pool and hub — no in-process state to coincidentally make this
// idempotent) never appends a second one.
func TestMaybeRecommendBlackboardFiresAtWorkerThreshold(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		fx.childOf(fx.parent)
	}
	fx.api.maybeRecommendBlackboard(ctx, fx.parent)
	rec, err := latestBlackboardRecommendation(ctx, fx.pool, fx.parent)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.State != "pending" || !strings.Contains(rec.Reason, "3 workers running at once") {
		t.Fatalf("recommendation = %+v", rec)
	}
	first := rec.ID

	second := &API{dbPool: fx.pool, hub: fx.hub}
	second.maybeRecommendBlackboard(ctx, fx.parent)
	var n int
	if err := fx.pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='blackboard_recommendation'`, fx.parent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("offered once: %d blackboard_recommendation events", n)
	}
	again, err := latestBlackboardRecommendation(ctx, fx.pool, fx.parent)
	if err != nil || again == nil || again.ID != first {
		t.Fatalf("repeated check must not replace the first recommendation: %+v, %v", again, err)
	}
}

// TestMaybeRecommendBlackboardStaysQuietUnderThreshold: two workers never cross the 3-worker
// threshold, and nothing else here crosses any other one.
func TestMaybeRecommendBlackboardStaysQuietUnderThreshold(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	fx.childOf(fx.parent)
	fx.childOf(fx.parent)
	fx.api.maybeRecommendBlackboard(ctx, fx.parent)
	rec, err := latestBlackboardRecommendation(ctx, fx.pool, fx.parent)
	if err != nil {
		t.Fatal(err)
	}
	if rec != nil {
		t.Fatalf("no threshold crossed, expected no recommendation: %+v", rec)
	}
}

// TestMaybeRecommendBlackboardSkipsWhenBlackboardExists: once a workspace has committed a real
// blackboard, the recommendation never fires, however complex it gets afterward.
func TestMaybeRecommendBlackboardSkipsWhenBlackboardExists(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		fx.childOf(fx.parent)
	}
	owner := blackboard.Actor{ID: "owner-1", Kind: "human"}
	if err := fx.api.blackboardStore().Create(ctx, blackboard.Record{ID: blackboard.NewID(), Owner: owner, Version: 1, Name: "Plan", WorkspaceID: fx.parent}); err != nil {
		t.Fatal(err)
	}
	fx.api.maybeRecommendBlackboard(ctx, fx.parent)
	rec, err := latestBlackboardRecommendation(ctx, fx.pool, fx.parent)
	if err != nil {
		t.Fatal(err)
	}
	if rec != nil {
		t.Fatalf("a workspace that already has a blackboard must never get a recommendation: %+v", rec)
	}
}

// blackboardRecommendationFx mounts the decide routes plus what sending a message needs.
func blackboardRecommendationFx(t *testing.T) *delegFx {
	t.Helper()
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	fx.dc.SetInputReceipts(true) // sendBlackboardSetupRequest's deterministic client id needs receipts
	fx.api.RegisterCategoryRoutes(fx.mux)
	fx.api.RegisterBlackboardRoutes(fx.mux)
	return fx
}

func postPendingRecommendation(t *testing.T, fx *delegFx) blackboardRecommendation {
	t.Helper()
	for i := 0; i < 3; i++ {
		fx.childOf(fx.parent)
	}
	fx.api.maybeRecommendBlackboard(context.Background(), fx.parent)
	rec, err := latestBlackboardRecommendation(context.Background(), fx.pool, fx.parent)
	if err != nil || rec == nil {
		t.Fatalf("seed recommendation: %+v %v", rec, err)
	}
	return *rec
}

// TestBlackboardRecommendationDecideIsPersonOnly: the card is addressed to the person, same as
// channel_recommendation — an agent's own session token may never decide it.
func TestBlackboardRecommendationDecideIsPersonOnly(t *testing.T) {
	fx := blackboardRecommendationFx(t)
	rec := postPendingRecommendation(t, fx)
	path := "/api/sessions/" + fx.parent + "/blackboard-recommendation/" + rec.ID + "/decide"
	status, _, _ := fx.do("POST", path, fx.parentTok, map[string]any{"action": "not_now"}, nil)
	if status < 400 {
		t.Fatalf("an agent token decided the recommendation: %d", status)
	}
	status, _, _ = fx.do("POST", path, fx.human("someone-else"), map[string]any{"action": "not_now"}, nil)
	if status < 400 {
		t.Fatalf("a non-owner decided the recommendation: %d", status)
	}
}

// TestBlackboardRecommendationNotNowIsRemembered: "Not now" is terminal and never asked again.
func TestBlackboardRecommendationNotNowIsRemembered(t *testing.T) {
	fx := blackboardRecommendationFx(t)
	rec := postPendingRecommendation(t, fx)
	path := "/api/sessions/" + fx.parent + "/blackboard-recommendation/" + rec.ID + "/decide"
	status, body, _ := fx.do("POST", path, fx.human("owner-1"), map[string]any{"action": "not_now"}, nil)
	if status != 200 || body["state"] != "not_now" {
		t.Fatalf("not_now: %d %v", status, body)
	}
	// Crossing again (more workers) must never post a second card once one is decided: still
	// exactly the pending card's own event plus the one decision recorded above, never a third.
	fx.childOf(fx.parent)
	fx.api.maybeRecommendBlackboard(context.Background(), fx.parent)
	var n int
	if err := fx.pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='blackboard_recommendation'`, fx.parent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("a decided recommendation must never be followed by another: %d events", n)
	}
}

// TestBlackboardRecommendationSetupWithMainRelays: the card's "Set it up with Main" action ends
// up at exactly the same relay as the empty blackboard screen's own button
// (sendBlackboardSetupRequest), so Main is asked to do the same thing the same way either way.
func TestBlackboardRecommendationSetupWithMainRelays(t *testing.T) {
	fx := blackboardRecommendationFx(t)
	rec := postPendingRecommendation(t, fx)
	path := "/api/sessions/" + fx.parent + "/blackboard-recommendation/" + rec.ID + "/decide"
	status, body, _ := fx.do("POST", path, fx.human("owner-1"), map[string]any{"action": "setup_with_main"}, nil)
	if status != 200 || body["state"] != "setup_with_main" {
		t.Fatalf("setup_with_main: %d %v", status, body)
	}
	note := fx.parentNote(fx.parent, 5*time.Second)
	if note.Source != blackboardSetupSource || note.Text == "" {
		t.Fatalf("expected the same relay as HandleRequestBlackboardSetup: %+v", note)
	}
}

// TestBlackboardRecommendationSetupMyselfIsRecorded: "Set up myself" needs no relay (the person
// opens the blackboard tab themselves), but is just as terminal as the other two actions.
func TestBlackboardRecommendationSetupMyselfIsRecorded(t *testing.T) {
	fx := blackboardRecommendationFx(t)
	rec := postPendingRecommendation(t, fx)
	path := "/api/sessions/" + fx.parent + "/blackboard-recommendation/" + rec.ID + "/decide"
	status, body, _ := fx.do("POST", path, fx.human("owner-1"), map[string]any{"action": "setup_myself"}, nil)
	if status != 200 || body["state"] != "setup_myself" {
		t.Fatalf("setup_myself: %d %v", status, body)
	}
	if n := fx.parentNote(fx.parent, 300*time.Millisecond); n.Type != "" {
		t.Fatalf("setup_myself must not relay to Main: %+v", n)
	}
}

// TestBlackboardRecommendationDecideRejectsUnknownAction and a GET of a decided recommendation
// both guard the handler's own input and replay paths, the same shape channel_recommendation's
// decide handler is tested with.
func TestBlackboardRecommendationDecideRejectsUnknownAction(t *testing.T) {
	fx := blackboardRecommendationFx(t)
	rec := postPendingRecommendation(t, fx)
	path := "/api/sessions/" + fx.parent + "/blackboard-recommendation/" + rec.ID
	status, _, _ := fx.do("POST", path+"/decide", fx.human("owner-1"), map[string]any{"action": "approve"}, nil)
	if status != 422 {
		t.Fatalf("unknown action: %d", status)
	}
	status, body, _ := fx.do("GET", path, fx.human("owner-1"), nil, nil)
	if status != 200 || body["state"] != "pending" {
		t.Fatalf("get pending: %d %v", status, body)
	}
}
