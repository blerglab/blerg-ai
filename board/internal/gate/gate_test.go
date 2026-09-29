package gate_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/gate"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fake is a scripted curator backend.
type fake struct {
	name    string
	verdict gate.Verdict
	err     error
	calls   int
}

func (f *fake) Name() string    { return f.name }
func (f *fake) ModelID() string { return "fake-model" }
func (f *fake) Review(_ context.Context, _ gate.Input) (gate.Verdict, error) {
	f.calls++
	return f.verdict, f.err
}

func testPool(t *testing.T) *pgxpool.Pool {
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
	_, err = pool.Exec(context.Background(),
		`TRUNCATE boards, tokens, admission_reviews RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

type fixture struct {
	pool  *pgxpool.Pool
	board db.Board
	cols  []db.Column
	token db.Token
}

func setup(t *testing.T, onUnavailable, onDispute string) fixture {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	gt, f := true, false
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{
		Name: "gated", GateEnabled: &gt, RequireRepo: &f,
		GateOnUnavailable: &onUnavailable, GateOnDispute: &onDispute,
	})
	if err != nil {
		t.Fatal(err)
	}
	cols, _ := db.ListColumns(ctx, pool, board.ID)
	tok, _, err := db.MintToken(ctx, pool, &board.ID, "agent", "test agent",
		[]string{"card.read", "card.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{pool: pool, board: board, cols: cols, token: tok}
}

func payload(title string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"title": title})
	return b
}

func reviewCount(t *testing.T, pool *pgxpool.Pool) int {
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM admission_reviews`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGateVerdicts(t *testing.T) {
	fx := setup(t, "open", "open")
	ctx := context.Background()

	cases := []struct {
		verdict gate.Verdict
		allowed bool
		status  int
	}{
		{gate.Verdict{Decision: "accept", Reason: "fine"}, true, 0},
		{gate.Verdict{Decision: "deny", Reason: "duplicate of #1", Confidence: 0.9}, false, 409},
		{gate.Verdict{Decision: "revise", Reason: "ambiguous", Suggestion: "split it"}, false, 422},
	}
	for i, tc := range cases {
		g := gate.New(fx.pool, []gate.Backend{&fake{name: "f", verdict: tc.verdict}}, nil)
		before := reviewCount(t, fx.pool)
		out, err := g.Check(ctx, fx.board, fx.cols, nil, &fx.token.ID, "create",
			payload(tc.verdict.Decision+" case "+string(rune('a'+i))), "", "")
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if out.Allowed != tc.allowed {
			t.Errorf("case %d: allowed=%v want %v", i, out.Allowed, tc.allowed)
		}
		if !tc.allowed && out.Status != tc.status {
			t.Errorf("case %d: status=%d want %d", i, out.Status, tc.status)
		}
		// A review row is written for EVERY invocation, including denials.
		if reviewCount(t, fx.pool) != before+1 {
			t.Errorf("case %d: no admission_reviews row written", i)
		}
	}
}

func TestGateUnavailableOpen(t *testing.T) {
	fx := setup(t, "open", "open")
	g := gate.New(fx.pool, []gate.Backend{&fake{name: "down", err: errors.New("conn refused")}}, nil)
	g.SetTimeout(time.Second)
	out, err := g.Check(context.Background(), fx.board, fx.cols, nil, &fx.token.ID, "create", payload("x"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allowed || out.GateFlag == nil || *out.GateFlag != "ungated" {
		t.Errorf("unavailable-open must allow with flag ungated, got %+v", out)
	}
	if reviewCount(t, fx.pool) != 1 {
		t.Error("ungated write must still leave an audit row")
	}
}

func TestGateUnavailableHold(t *testing.T) {
	fx := setup(t, "hold", "open")
	g := gate.New(fx.pool, []gate.Backend{&fake{name: "down", err: errors.New("boom")}}, nil)
	out, err := g.Check(context.Background(), fx.board, fx.cols, nil, &fx.token.ID, "create", payload("x"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Allowed || !out.Held || out.Status != 202 {
		t.Errorf("unavailable-hold must 202, got %+v", out)
	}
	held, err := db.ListHeldReviews(context.Background(), fx.pool, fx.board.ID)
	if err != nil || len(held) != 1 {
		t.Fatalf("expected 1 held review, got %d (%v)", len(held), err)
	}
	if held[0].HeldExpiresAt == nil {
		t.Error("held review missing TTL")
	}
}

func TestGateRepeatRejected(t *testing.T) {
	fx := setup(t, "open", "open")
	ctx := context.Background()
	deny := &fake{name: "f", verdict: gate.Verdict{Decision: "deny", Reason: "dup"}}
	g := gate.New(fx.pool, []gate.Backend{deny}, nil)

	if _, err := g.Check(ctx, fx.board, fx.cols, nil, &fx.token.ID, "create", payload("same"), "", ""); err != nil {
		t.Fatal(err)
	}
	callsAfterFirst := deny.calls
	out, err := g.Check(ctx, fx.board, fx.cols, nil, &fx.token.ID, "create", payload("same"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Allowed || out.Status != 409 {
		t.Errorf("identical resubmission must 409, got %+v", out)
	}
	if deny.calls != callsAfterFirst {
		t.Error("repeat rejection must not spend a curator call")
	}
}

func TestGateDisputeBoundAndTiebreak(t *testing.T) {
	fx := setup(t, "open", "tiebreak")
	ctx := context.Background()
	deny := &fake{name: "curator", verdict: gate.Verdict{Decision: "deny", Reason: "duplicate of #7"}}
	tb := &fake{name: "tiebreaker", verdict: gate.Verdict{Decision: "accept", Reason: "distinct after all"}}
	g := gate.New(fx.pool, []gate.Backend{deny}, tb)

	out, err := g.Check(ctx, fx.board, fx.cols, nil, &fx.token.ID, "create", payload("contested"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	reviewID := out.Body["review_id"].(string)

	// Dispute → tiebreaker adjudicates → accepted with flag tiebroken.
	out, err = g.Check(ctx, fx.board, fx.cols, nil, &fx.token.ID, "create", payload("contested"),
		reviewID, "it is a different subsystem")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allowed || out.GateFlag == nil || *out.GateFlag != "tiebroken" {
		t.Fatalf("tiebreak accept expected, got %+v", out)
	}
	if tb.calls != 1 {
		t.Errorf("tiebreaker calls = %d, want 1", tb.calls)
	}

	// Second dispute of the same review is rejected outright.
	out, err = g.Check(ctx, fx.board, fx.cols, nil, &fx.token.ID, "create", payload("contested"),
		reviewID, "again")
	if err != nil {
		t.Fatal(err)
	}
	if out.Allowed || out.Status != 409 {
		t.Errorf("second dispute must be repeat_rejected, got %+v", out)
	}
	if tb.calls != 1 {
		t.Error("second dispute must not reach the tiebreaker")
	}
}

func TestGateDisputeOpenPolicy(t *testing.T) {
	fx := setup(t, "open", "open")
	ctx := context.Background()
	deny := &fake{name: "curator", verdict: gate.Verdict{Decision: "deny", Reason: "nope"}}
	g := gate.New(fx.pool, []gate.Backend{deny}, nil)

	out, _ := g.Check(ctx, fx.board, fx.cols, nil, &fx.token.ID, "create", payload("c2"), "", "")
	reviewID := out.Body["review_id"].(string)

	out, err := g.Check(ctx, fx.board, fx.cols, nil, &fx.token.ID, "create", payload("c2"), reviewID, "wrong")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allowed || out.GateFlag == nil || *out.GateFlag != "forced" {
		t.Errorf("dispute-open must allow with flag forced, got %+v", out)
	}
}

func TestGateBackendFailover(t *testing.T) {
	fx := setup(t, "open", "open")
	down := &fake{name: "openai", err: errors.New("unreachable")}
	up := &fake{name: "claude", verdict: gate.Verdict{Decision: "accept", Reason: "ok"}}
	g := gate.New(fx.pool, []gate.Backend{down, up}, nil)
	out, err := g.Check(context.Background(), fx.board, fx.cols, nil, &fx.token.ID, "create", payload("fo"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allowed || out.GateFlag != nil {
		t.Errorf("failover accept expected (no flag), got %+v", out)
	}
	if down.calls != 2 {
		t.Errorf("first backend should get 2 attempts, got %d", down.calls)
	}
	if up.calls != 1 {
		t.Errorf("second backend should serve on attempt 1, got %d", up.calls)
	}
}

// reviseBot always returns revise — the churn behavior seen live (the curator
// rejecting its own suggested titles round after round).
type reviseBot struct{ calls int }

func (r *reviseBot) Name() string    { return "revisebot" }
func (r *reviseBot) ModelID() string { return "fake" }
func (r *reviseBot) Review(_ context.Context, in gate.Input) (gate.Verdict, error) {
	r.calls++
	return gate.Verdict{Decision: "revise", Reason: "title lacks specificity",
		Suggestion: "clarify"}, nil
}

func TestReviseCapBreaksChurn(t *testing.T) {
	fx := setup(t, "open", "open")
	ctx := context.Background()
	bot := &reviseBot{}
	g := gate.New(fx.pool, []gate.Backend{bot}, nil)

	// Round 1: revise — legitimate.
	out, err := g.Check(ctx, fx.board, fx.cols, nil, &fx.token.ID, "create",
		payload("add aftercare stage"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Allowed || out.Status != 422 {
		t.Fatalf("first revise should 422, got %+v", out)
	}

	// Round 2: the agent revised; the curator revises AGAIN → cap converts to
	// accept with policy 'override'.
	out, err = g.Check(ctx, fx.board, fx.cols, nil, &fx.token.ID, "create",
		payload("add aftercare stage after climax recording preferences"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allowed {
		t.Fatalf("second revise must be capped to accept, got %+v", out)
	}
	reviews, _ := db.ListReviews(ctx, fx.pool, fx.board.ID, 5)
	if reviews[0].PolicyApplied != "override" {
		t.Errorf("capped review policy = %q, want override", reviews[0].PolicyApplied)
	}
	// The audit keeps the curator's actual verdict.
	if reviews[0].Verdict == nil || *reviews[0].Verdict != "revise" {
		t.Errorf("capped review must record the curator's revise verdict honestly")
	}
}
