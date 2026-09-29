package escalate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/escalate"
)

// rung is a scripted step: it fails failures times, then answers.
type rung struct {
	name     string
	answer   string
	failures int
	calls    int
	block    time.Duration
}

func (r *rung) step(attempts int) escalate.Step[string, string] {
	return escalate.Step[string, string]{
		Name: r.name, Model: r.name + "-model", Attempts: attempts,
		Call: func(ctx context.Context, in string) (string, error) {
			r.calls++
			if r.block > 0 {
				select {
				case <-time.After(r.block):
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			if r.calls <= r.failures {
				return "", errors.New(r.name + " unavailable")
			}
			return r.answer + ":" + in, nil
		},
	}
}

func TestRunPrefersTheCheapestRungThatAnswers(t *testing.T) {
	cheap := &rung{name: "local", answer: "cheap"}
	pricey := &rung{name: "claude", answer: "pricey"}
	l := escalate.Ladder[string, string]{Steps: []escalate.Step[string, string]{
		cheap.step(2), pricey.step(1),
	}}

	out, res, err := l.Run(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if out != "cheap:q" {
		t.Errorf("out = %q, want the first rung's answer", out)
	}
	if res.Step != "local" || res.Model != "local-model" {
		t.Errorf("result must name the rung that answered, got %+v", res)
	}
	if pricey.calls != 0 {
		t.Error("the expensive rung must not be called when the cheap one answers")
	}
}

func TestRunSpendsEveryAttemptBeforeClimbing(t *testing.T) {
	down := &rung{name: "local", failures: 99}
	up := &rung{name: "claude", answer: "ok"}
	l := escalate.Ladder[string, string]{Steps: []escalate.Step[string, string]{
		down.step(2), up.step(1),
	}}

	out, res, err := l.Run(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok:q" || res.Step != "claude" {
		t.Errorf("failover wrong: out=%q res=%+v", out, res)
	}
	if down.calls != 2 {
		t.Errorf("first rung got %d attempts, want its configured 2", down.calls)
	}
	if up.calls != 1 {
		t.Errorf("second rung got %d calls, want 1", up.calls)
	}
}

func TestRunRetriesTheSameRungWithinItsAttempts(t *testing.T) {
	flaky := &rung{name: "local", answer: "ok", failures: 1}
	l := escalate.Ladder[string, string]{Steps: []escalate.Step[string, string]{flaky.step(2)}}
	if _, res, err := l.Run(context.Background(), "q"); err != nil || res.Step != "local" {
		t.Fatalf("a rung that fails once must be retried, got res=%+v err=%v", res, err)
	}
	if flaky.calls != 2 {
		t.Errorf("calls = %d, want 2", flaky.calls)
	}
}

func TestRunTreatsFailedValidationAsAFailedAttempt(t *testing.T) {
	bad := &rung{name: "local", answer: "garbage"}
	good := &rung{name: "claude", answer: "good"}
	l := escalate.Ladder[string, string]{
		Steps:    []escalate.Step[string, string]{bad.step(2), good.step(1)},
		Validate: func(s string) error { return validatePrefix(s, "good") },
	}

	out, res, err := l.Run(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if out != "good:q" || res.Step != "claude" {
		t.Errorf("a rung answering nonsense must be climbed past, got %q from %+v", out, res)
	}
	if bad.calls != 2 {
		t.Errorf("invalid answers should still spend the rung's attempts, got %d", bad.calls)
	}
}

func validatePrefix(s, want string) error {
	if strings.HasPrefix(s, want) {
		return nil
	}
	return errors.New("unexpected answer " + s)
}

func TestRunReportsTheLastFailureWhenEveryRungFails(t *testing.T) {
	a := &rung{name: "local", failures: 99}
	b := &rung{name: "claude", failures: 99}
	l := escalate.Ladder[string, string]{Steps: []escalate.Step[string, string]{a.step(2), b.step(2)}}

	_, _, err := l.Run(context.Background(), "q")
	if err == nil || !strings.Contains(err.Error(), "claude unavailable") {
		t.Fatalf("err = %v, want the last rung's failure", err)
	}
	if errors.Is(err, escalate.ErrNoSteps) {
		t.Error("rungs were tried; this is not ErrNoSteps")
	}
}

func TestRunWithNoStepsIsDistinguishable(t *testing.T) {
	var l escalate.Ladder[string, string]
	if _, _, err := l.Run(context.Background(), "q"); !errors.Is(err, escalate.ErrNoSteps) {
		t.Fatalf("err = %v, want ErrNoSteps", err)
	}
}

func TestRunBoundsEachAttemptWithTheLadderTimeout(t *testing.T) {
	slow := &rung{name: "local", answer: "late", block: 2 * time.Second}
	fast := &rung{name: "claude", answer: "quick"}
	l := escalate.Ladder[string, string]{
		Steps:   []escalate.Step[string, string]{slow.step(1), fast.step(1)},
		Timeout: 50 * time.Millisecond,
	}

	start := time.Now()
	out, _, err := l.Run(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if out != "quick:q" {
		t.Errorf("out = %q, want the fast rung after the slow one timed out", out)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s — the per-attempt timeout did not bound the slow rung", elapsed)
	}
}

func TestRunStopsWhenTheCallerGivesUp(t *testing.T) {
	first := &rung{name: "local", failures: 99}
	second := &rung{name: "claude", answer: "ok"}
	l := escalate.Ladder[string, string]{Steps: []escalate.Step[string, string]{first.step(2), second.step(1)}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := l.Run(ctx, "q")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if second.calls != 0 {
		t.Error("a dead request must not climb to the next rung")
	}
}

// ── Adjudicator ──────────────────────────────────────────────────────────────

func TestAdjudicateAsksTheReserveRung(t *testing.T) {
	cheap := &rung{name: "local", answer: "cheap"}
	strong := &rung{name: "claude", answer: "strong"}
	adj := strong.step(1)
	l := escalate.Ladder[string, string]{
		Steps:       []escalate.Step[string, string]{cheap.step(2)},
		Adjudicator: &adj,
	}

	out, res, err := l.Adjudicate(context.Background(), "contested")
	if err != nil {
		t.Fatal(err)
	}
	if out != "strong:contested" || res.Step != "claude" {
		t.Errorf("adjudication went to the wrong rung: %q %+v", out, res)
	}
	if cheap.calls != 0 {
		t.Error("adjudication must not spend the cheap rung")
	}
}

func TestAdjudicateFallsBackToTheStrongestStep(t *testing.T) {
	cheap := &rung{name: "local", answer: "cheap"}
	strong := &rung{name: "claude", answer: "strong"}
	l := escalate.Ladder[string, string]{Steps: []escalate.Step[string, string]{
		cheap.step(2), strong.step(1),
	}}

	out, res, err := l.Adjudicate(context.Background(), "contested")
	if err != nil {
		t.Fatal(err)
	}
	if out != "strong:contested" || res.Step != "claude" {
		t.Errorf("with no adjudicator set the last step adjudicates, got %q %+v", out, res)
	}
}

// The fallback is the last step exactly as declared — its own Attempts
// included. A caller wanting adjudication on a tighter budget than the same
// model gets as a Run rung has to set Adjudicator itself; the ladder does not
// silently re-budget it.
func TestAdjudicateFallbackUsesTheStepsOwnAttempts(t *testing.T) {
	strong := &rung{name: "claude", failures: 99}
	l := escalate.Ladder[string, string]{Steps: []escalate.Step[string, string]{strong.step(2)}}

	if _, _, err := l.Adjudicate(context.Background(), "q"); err == nil {
		t.Fatal("a failing adjudicator must error")
	}
	if strong.calls != 2 {
		t.Errorf("calls = %d, want the step's declared 2", strong.calls)
	}
}

func TestAdjudicateSkipsValidationAndKeepsItsAttemptBudget(t *testing.T) {
	strong := &rung{name: "claude", answer: "garbage"}
	adj := strong.step(1)
	l := escalate.Ladder[string, string]{
		Adjudicator: &adj,
		Validate:    func(s string) error { return validatePrefix(s, "never-matches") },
	}

	out, _, err := l.Adjudicate(context.Background(), "q")
	if err != nil {
		t.Fatalf("the adjudicator's answer is final; Validate must not reject it: %v", err)
	}
	if out != "garbage:q" {
		t.Errorf("out = %q", out)
	}
	if strong.calls != 1 {
		t.Errorf("calls = %d, want the adjudicator's own single attempt", strong.calls)
	}
}

func TestAdjudicateWithNothingConfigured(t *testing.T) {
	var l escalate.Ladder[string, string]
	if l.CanAdjudicate() {
		t.Error("an empty ladder has nothing to adjudicate with")
	}
	if _, _, err := l.Adjudicate(context.Background(), "q"); !errors.Is(err, escalate.ErrNoSteps) {
		t.Fatalf("err = %v, want ErrNoSteps so the caller can apply its own fallback", err)
	}
}

func TestCanAdjudicate(t *testing.T) {
	only := (&rung{name: "local"}).step(1)
	if !(escalate.Ladder[string, string]{Steps: []escalate.Step[string, string]{only}}).CanAdjudicate() {
		t.Error("steps alone can adjudicate — the strongest one stands in")
	}
	if !(escalate.Ladder[string, string]{Adjudicator: &only}).CanAdjudicate() {
		t.Error("an adjudicator with no steps can still adjudicate")
	}
}

func TestAdjudicateFailureSurfacesToTheCaller(t *testing.T) {
	down := &rung{name: "claude", failures: 99}
	adj := down.step(1)
	l := escalate.Ladder[string, string]{Adjudicator: &adj}
	if _, _, err := l.Adjudicate(context.Background(), "q"); err == nil {
		t.Fatal("a failed adjudication must return an error, not a zero answer")
	}
	if down.calls != 1 {
		t.Errorf("calls = %d, want 1", down.calls)
	}
}

func TestStepWithoutACallIsAnError(t *testing.T) {
	l := escalate.Ladder[string, string]{Steps: []escalate.Step[string, string]{{Name: "empty"}}}
	if _, _, err := l.Run(context.Background(), "q"); err == nil {
		t.Fatal("a step with no Call must fail, not panic")
	}
}
