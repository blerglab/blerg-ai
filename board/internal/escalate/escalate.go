// Package escalate is blerg-board's cheap-model-first ladder: ask the local box
// first at ~0 marginal cost, fall through to progressively more expensive
// models when it fails, and hold the strongest one back as the adjudicator
// for contested answers.
//
// The admission gate is the pattern's first user (local inference first,
// Claude as the dispute tiebreak) but nothing here knows about admission
// review — a Ladder carries whatever In and Out its steps agree on, so any
// feature offloading work to local inference gets the same fall-through and
// the same reserve adjudicator without importing internal/gate.
package escalate

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNoSteps means the ladder has no rung to call — every model was left
// unconfigured. Callers treat it like any other failure, but it is worth
// distinguishing in a log: nothing was tried.
var ErrNoSteps = errors.New("escalate: no steps configured")

// Step is one rung: a named model and the call that asks it.
type Step[In, Out any] struct {
	// Name identifies the rung in results and logs (e.g. "openai").
	Name string
	// Model is the model id, recorded alongside the answer.
	Model string
	// Attempts is how many times this rung is tried before falling through.
	// Zero means one attempt. Give the cheap local rung more than the paid
	// one: retrying it costs nothing.
	Attempts int
	// Call asks this rung. It is handed a context already bounded by the
	// ladder's Timeout.
	Call func(ctx context.Context, in In) (Out, error)
}

// Ladder runs Steps in order until one answers.
type Ladder[In, Out any] struct {
	// Steps are tried in order — cheapest first by convention.
	Steps []Step[In, Out]
	// Adjudicator is the reserve rung for contested cases, reached only via
	// Adjudicate. Nil falls back to the last (strongest) of Steps.
	Adjudicator *Step[In, Out]
	// Timeout bounds each attempt. Zero adds no deadline of its own.
	Timeout time.Duration
	// Validate rejects a structurally bad answer — a failed validation
	// counts as a failed attempt and the ladder keeps climbing. It guards
	// Run only: the Adjudicator is asked because the cheap rungs were not
	// trusted, so its answer is final by definition.
	Validate func(Out) error
}

// Result says which rung answered and how long it took — everything a caller
// needs to record the answer's provenance.
type Result struct {
	Step    string
	Model   string
	Latency time.Duration
}

// Run climbs the ladder and returns the first answer that passes Validate.
// The error is the last rung's failure, or ErrNoSteps when there was nothing
// to call. A cancelled caller context stops the climb immediately and returns
// ctx.Err() — no point asking the next model on a dead request.
func (l Ladder[In, Out]) Run(ctx context.Context, in In) (Out, Result, error) {
	var zero Out
	var lastErr error
	for _, s := range l.Steps {
		attempts := s.Attempts
		if attempts < 1 {
			attempts = 1
		}
		for i := 0; i < attempts; i++ {
			out, res, err := l.call(ctx, s, in, l.Validate)
			if err == nil {
				return out, res, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				return zero, Result{}, ctx.Err()
			}
		}
	}
	if lastErr == nil {
		lastErr = ErrNoSteps
	}
	return zero, Result{}, lastErr
}

// CanAdjudicate reports whether there is any rung to adjudicate with. It
// lets a caller skip expensive prep — the gate assembles a candidate set
// before it asks — when the answer would be "nothing to ask".
func (l Ladder[In, Out]) CanAdjudicate() bool {
	return l.Adjudicator != nil || len(l.Steps) > 0
}

// Adjudicate asks the reserve rung — the strong model held back for cases the
// cheap ones are not trusted to settle. With no Adjudicator set it asks the
// last of Steps exactly as that step is declared, its own Attempts included:
// a caller that wants adjudication on a different attempt budget than the
// same model gets as a Run rung must set Adjudicator explicitly. With no
// steps at all it returns ErrNoSteps so the caller can apply its own
// fallback (the gate holds the write for a human).
func (l Ladder[In, Out]) Adjudicate(ctx context.Context, in In) (Out, Result, error) {
	var zero Out
	s := l.Adjudicator
	if s == nil && len(l.Steps) > 0 {
		s = &l.Steps[len(l.Steps)-1]
	}
	if s == nil {
		return zero, Result{}, ErrNoSteps
	}
	attempts := s.Attempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		out, res, err := l.call(ctx, *s, in, nil)
		if err == nil {
			return out, res, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return zero, Result{}, ctx.Err()
		}
	}
	return zero, Result{}, lastErr
}

// call runs one attempt under the ladder's timeout.
func (l Ladder[In, Out]) call(ctx context.Context, s Step[In, Out], in In, validate func(Out) error) (Out, Result, error) {
	var zero Out
	if s.Call == nil {
		return zero, Result{}, fmt.Errorf("escalate: step %q has no call", s.Name)
	}
	cctx := ctx
	if l.Timeout > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, l.Timeout)
		defer cancel()
	}
	start := time.Now()
	out, err := s.Call(cctx, in)
	if err != nil {
		return zero, Result{}, err
	}
	if validate != nil {
		if err := validate(out); err != nil {
			return zero, Result{}, fmt.Errorf("%s: %w", s.Name, err)
		}
	}
	return out, Result{Step: s.Name, Model: s.Model, Latency: time.Since(start)}, nil
}
