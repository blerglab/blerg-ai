package gate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/localinfer"
)

// These cover the curator ladder and the OpenAI-compatible backend without a
// database — the end-to-end policy behavior lives in gate_test.go, which
// needs Postgres.

func curatorInput() Input {
	return Input{Board: db.Board{Name: "b"}, Operation: "create", Payload: []byte(`{"title":"x"}`)}
}

// ── OpenAIBackend over the shared local-inference client ─────────────────────

func TestOpenAIBackendReviewsOverTheSharedClient(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotPath, gotBody = r.URL.Path, string(raw)
		w.Header().Set("Content-Type", "application/json")
		// Fenced output with prose around it: what small local models do.
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Sure!\n`+
			"```json\\n"+`{\"decision\":\"deny\",\"reason\":\"dup\",\"duplicate_of\":7,\"confidence\":0.8}`+"\\n```"+`"}}]}`)
	}))
	defer srv.Close()

	b := NewOpenAIBackend(localinfer.New(localinfer.Config{BaseURL: srv.URL, ChatModel: "qwen"}), "")
	if b.Name() != "openai" || b.ModelID() != "qwen" {
		t.Fatalf("backend identity wrong: %s/%s", b.Name(), b.ModelID())
	}

	v, err := b.Review(context.Background(), curatorInput())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, "admission curator") {
		t.Errorf("the curator prompt must reach the endpoint, got %q", gotBody)
	}
	if !strings.Contains(gotBody, `"temperature":0`) {
		t.Errorf("curator calls must stay deterministic, got %q", gotBody)
	}
	if v.Decision != "deny" || v.Reason != "dup" || v.DuplicateOf == nil || *v.DuplicateOf != 7 {
		t.Errorf("verdict parsed wrong: %+v", v)
	}
}

func TestOpenAIBackendModelOverride(t *testing.T) {
	c := localinfer.New(localinfer.Config{BaseURL: "http://box:8000", ChatModel: "qwen"})
	if got := NewOpenAIBackend(c, "other").ModelID(); got != "other" {
		t.Errorf("ModelID = %q, want the override", got)
	}
	if got := NewOpenAIBackend(c, "").ModelID(); got != "qwen" {
		t.Errorf("ModelID = %q, want the client's configured chat model", got)
	}
}

func TestOpenAIBackendSurfacesUnavailableAsTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listening

	b := NewOpenAIBackend(localinfer.New(localinfer.Config{BaseURL: url, ChatModel: "qwen"}), "")
	_, err := b.Review(context.Background(), curatorInput())
	if !localinfer.Unavailable(err) {
		t.Fatalf("err = %v, want a typed ErrUnavailable the gate can act on", err)
	}
}

// ── Curator ladder ───────────────────────────────────────────────────────────

type scriptedBackend struct {
	name    string
	model   string
	verdict Verdict
	err     error
	calls   int
}

func (s *scriptedBackend) Name() string    { return s.name }
func (s *scriptedBackend) ModelID() string { return s.model }
func (s *scriptedBackend) Review(context.Context, Input) (Verdict, error) {
	s.calls++
	return s.verdict, s.err
}

func TestRunCuratorFailsOverAfterTwoAttempts(t *testing.T) {
	down := &scriptedBackend{name: "openai", model: "qwen", err: errors.New("unreachable")}
	up := &scriptedBackend{name: "claude", model: "claude-opus-5",
		verdict: Verdict{Decision: "accept", Reason: "ok"}}
	g := New(nil, []Backend{down, up}, nil)

	v, who, err := g.runCurator(context.Background(), curatorInput())
	if err != nil {
		t.Fatal(err)
	}
	if v.Decision != "accept" {
		t.Errorf("verdict = %+v", v)
	}
	if down.calls != 2 {
		t.Errorf("first backend got %d attempts, want 2", down.calls)
	}
	if up.calls != 1 {
		t.Errorf("second backend got %d calls, want 1", up.calls)
	}
	if who.Step != "claude" || who.Model != "claude-opus-5" {
		t.Errorf("audit provenance wrong: %+v", who)
	}
}

func TestRunCuratorClimbsPastAnInvalidDecision(t *testing.T) {
	nonsense := &scriptedBackend{name: "openai", model: "qwen",
		verdict: Verdict{Decision: "maybe", Reason: "shrug"}}
	up := &scriptedBackend{name: "claude", model: "claude-opus-5",
		verdict: Verdict{Decision: "revise", Reason: "ambiguous"}}
	g := New(nil, []Backend{nonsense, up}, nil)

	v, who, err := g.runCurator(context.Background(), curatorInput())
	if err != nil {
		t.Fatal(err)
	}
	if v.Decision != "revise" || who.Step != "claude" {
		t.Errorf("a backend inventing a decision must be treated as failed: %+v %+v", v, who)
	}
	if nonsense.calls != 2 {
		t.Errorf("invalid decisions still spend the backend's attempts, got %d", nonsense.calls)
	}
}

func TestRunCuratorWithNoBackends(t *testing.T) {
	g := New(nil, nil, nil)
	_, _, err := g.runCurator(context.Background(), curatorInput())
	if err == nil || !strings.Contains(err.Error(), "no curator backends configured") {
		t.Fatalf("err = %v, want the unavailable reason recorded on the audit row", err)
	}
}

func TestRunCuratorReportsTheLastFailure(t *testing.T) {
	a := &scriptedBackend{name: "openai", err: errors.New("box down")}
	b := &scriptedBackend{name: "claude", err: errors.New("api down")}
	g := New(nil, []Backend{a, b}, nil)
	if _, _, err := g.runCurator(context.Background(), curatorInput()); err == nil ||
		!strings.Contains(err.Error(), "api down") {
		t.Fatalf("err = %v, want the last backend's failure", err)
	}
}

func TestAdjudicatorIsTheTiebreakThenTheStrongestBackend(t *testing.T) {
	cheap := &scriptedBackend{name: "openai", model: "qwen",
		verdict: Verdict{Decision: "deny", Reason: "dup"}}
	strong := &scriptedBackend{name: "claude", model: "claude-opus-5",
		verdict: Verdict{Decision: "accept", Reason: "distinct"}}

	// With a tiebreak set, only it adjudicates.
	tb := &scriptedBackend{name: "tiebreak", model: "claude-opus-5",
		verdict: Verdict{Decision: "accept", Reason: "adjudicated"}}
	g := New(nil, []Backend{cheap, strong}, tb)
	v, who, err := g.ladder().Adjudicate(context.Background(), curatorInput())
	if err != nil {
		t.Fatal(err)
	}
	if who.Step != "tiebreak" || v.Reason != "adjudicated" {
		t.Errorf("tiebreak must adjudicate: %+v %+v", v, who)
	}
	if cheap.calls != 0 || strong.calls != 0 {
		t.Error("adjudication must not spend the curator backends")
	}
	if tb.calls != 1 {
		t.Errorf("tiebreak calls = %d, want a single shot before the write is held", tb.calls)
	}

	// With none set, the last (strongest) backend adjudicates.
	g = New(nil, []Backend{cheap, strong}, nil)
	if _, who, err = g.ladder().Adjudicate(context.Background(), curatorInput()); err != nil {
		t.Fatal(err)
	}
	if who.Step != "claude" {
		t.Errorf("adjudicator fallback = %q, want the last backend", who.Step)
	}
}

// A dispute gets ONE adjudication attempt and is then held for a human —
// including when the adjudicator is the fallback (no tiebreak configured,
// e.g. a local-inference-only deployment). Retrying it would auto-adjudicate
// disputes the old code held.
func TestFallbackAdjudicatorGetsASingleAttempt(t *testing.T) {
	cheap := &scriptedBackend{name: "openai", model: "qwen", err: errors.New("down")}
	strong := &scriptedBackend{name: "claude", model: "claude-opus-5", err: errors.New("down")}
	g := New(nil, []Backend{cheap, strong}, nil)

	if _, _, err := g.ladder().Adjudicate(context.Background(), curatorInput()); err == nil {
		t.Fatal("a failing adjudicator must error so Check holds the write")
	}
	if strong.calls != 1 {
		t.Errorf("fallback adjudicator called %d times, want 1 — it must not inherit its curator attempts", strong.calls)
	}
	if cheap.calls != 0 {
		t.Errorf("cheap backend called %d times during adjudication, want 0", cheap.calls)
	}
}

func TestAdjudicatorAbsentEntirely(t *testing.T) {
	g := New(nil, nil, nil)
	if _, _, err := g.ladder().Adjudicate(context.Background(), curatorInput()); err == nil {
		t.Fatal("no adjudicator must error so Check can hold the write")
	}
}
