// Package gate implements blerg-board's admission controller: a synchronous,
// LLM-backed gate on the agent write path. It can deny a submission with a
// reason or demand a revision — training signal delivered in-band, working on
// agents blerg-board did not write.
//
// Every invocation writes an admission_reviews row, whether or not a card
// results. The curator call happens BEFORE the write transaction opens: `move`
// is gated and board ordering serialises on the board row lock, so an
// in-transaction LLM call would block every write on the board.
package gate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/escalate"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Verdict is the structured result of one curator pass.
type Verdict struct {
	Decision    string  `json:"decision"` // accept|deny|revise
	Reason      string  `json:"reason"`
	DuplicateOf *int    `json:"duplicate_of,omitempty"` // card NUMBER (#42), not uuid
	Suggestion  string  `json:"suggestion,omitempty"`
	Confidence  float32 `json:"confidence,omitempty"`
}

// Input is everything a backend sees: the submission, the board's declared
// shape, and a bounded candidate set — never the whole board.
type Input struct {
	Board      db.Board
	Columns    []db.Column
	Operation  string
	Payload    json.RawMessage
	Candidates []db.Card
	// Current is the target card for update/move/archive/delete — the curator
	// judges mutations against it (nil for create).
	Current *db.Card
	// Dispute context (empty for a fresh submission).
	PriorDenialReason string
	Rebuttal          string
	// PriorRevise: the curator's own revise verdict from this token's previous
	// round — "your suggestion was followed; do not re-litigate".
	PriorReviseReason     string
	PriorReviseSuggestion string
}

// Backend is one curator implementation (OpenAI-compatible, Claude API, or a
// test fake).
type Backend interface {
	Name() string
	ModelID() string
	Review(ctx context.Context, in Input) (Verdict, error)
}

// Outcome tells the API layer what to do with the write.
type Outcome struct {
	Allowed  bool
	Held     bool
	Status   int            // HTTP status when !Allowed (409/422/202)
	Body     map[string]any // response body when !Allowed
	ReviewID *string        // admission_reviews row for event linkage
	GateFlag *string        // ungated|forced|tiebroken to stamp on the card
}

type Gate struct {
	pool     *pgxpool.Pool
	backends []Backend // ordered preference list
	tiebreak Backend   // stronger model for dispute adjudication
	timeout  time.Duration
	// repeatWindow bounds bare resubmission of an identical denied payload.
	repeatWindow time.Duration
	heldTTL      time.Duration
}

func New(pool *pgxpool.Pool, backends []Backend, tiebreak Backend) *Gate {
	return &Gate{
		pool: pool, backends: backends, tiebreak: tiebreak,
		timeout: 10 * time.Second, repeatWindow: time.Hour, heldTTL: 24 * time.Hour,
	}
}

// SetTimeout overrides the per-curator-call timeout (tests).
func (g *Gate) SetTimeout(d time.Duration) { g.timeout = d }

func hashPayload(op string, payload []byte) []byte {
	h := sha256.New()
	h.Write([]byte(op))
	h.Write([]byte{0})
	h.Write(payload)
	return h.Sum(nil)
}

func strPtr(s string) *string { return &s }

// Check runs the gate for one agent write. actor is the submitting
// principal's GateActorTokenID() — the real tokens-table id for a native
// token principal, nil for a core-issued one (humans are exempt upstream).
// disputeOf is the review being contested, "" for a fresh submission.
func (g *Gate) Check(ctx context.Context, board db.Board, columns []db.Column, current *db.Card, actor *string, operation string, payload json.RawMessage, disputeOf, rebuttal string) (Outcome, error) {
	hash := hashPayload(operation, payload)
	base := db.Review{
		BoardID: board.ID, ActorTokenID: actor, Operation: operation,
		PayloadHash: hash, Payload: payload,
	}
	if current != nil {
		base.TargetCardID = &current.ID
		v := current.Version
		base.TargetVersion = &v
	}

	// ── Dispute path ─────────────────────────────────────────────────────────
	if disputeOf != "" {
		prior, err := db.GetReview(ctx, g.pool, disputeOf)
		if err != nil || prior.BoardID != board.ID {
			return Outcome{}, fmt.Errorf("dispute_of review not found on this board")
		}
		disputed, err := db.HasDispute(ctx, g.pool, disputeOf)
		if err != nil {
			return Outcome{}, err
		}
		if disputed {
			// One dispute per review — enforceable because dispute_of exists.
			r := base
			r.PolicyApplied = "repeat_rejected"
			r.DisputeOf = &disputeOf
			rec, err := db.InsertReview(ctx, g.pool, r)
			if err != nil {
				return Outcome{}, err
			}
			return Outcome{Status: 409, Body: map[string]any{
				"decision":  "deny",
				"reason":    "this review has already been disputed once; the bound is final",
				"review_id": rec.ID,
			}}, nil
		}
		priorReason := ""
		if prior.Reason != nil {
			priorReason = *prior.Reason
		}
		switch board.GateOnDispute {
		case "open":
			r := base
			r.Verdict = strPtr("accept")
			r.PolicyApplied = "forced"
			r.DisputeOf = &disputeOf
			r.Reason = strPtr("dispute accepted by policy; original denial: " + priorReason)
			rec, err := db.InsertReview(ctx, g.pool, r)
			if err != nil {
				return Outcome{}, err
			}
			return Outcome{Allowed: true, ReviewID: &rec.ID, GateFlag: strPtr("forced")}, nil
		case "hold":
			return g.hold(ctx, base, &disputeOf)
		default: // tiebreak
			return g.tiebreakCheck(ctx, base, board, columns, current, operation, payload, disputeOf, priorReason, rebuttal)
		}
	}

	// ── Bare-resubmission bound ──────────────────────────────────────────────
	repeat, err := db.RecentDenialByHash(ctx, g.pool, board.ID, hash, actor, g.repeatWindow)
	if err != nil {
		return Outcome{}, err
	}
	if repeat {
		r := base
		r.PolicyApplied = "repeat_rejected"
		rec, err := db.InsertReview(ctx, g.pool, r)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Status: 409, Body: map[string]any{
			"decision":  "deny",
			"reason":    "identical submission was denied recently; revise it or dispute the original review",
			"review_id": rec.ID,
		}}, nil
	}

	// ── Curator ──────────────────────────────────────────────────────────────
	in, err := g.buildInput(ctx, board, columns, current, operation, payload)
	if err != nil {
		return Outcome{}, err
	}
	prior, err := db.LastGatedReview(ctx, g.pool, board.ID, actor, operation, 30*time.Minute)
	if err != nil {
		return Outcome{}, err
	}
	priorWasRevise := prior != nil && prior.Verdict != nil && *prior.Verdict == "revise"
	if priorWasRevise {
		if prior.Reason != nil {
			in.PriorReviseReason = *prior.Reason
		}
	}
	verdict, answered, err := g.runCurator(ctx, in)
	if err != nil {
		// Unavailable: every backend failed.
		switch board.GateOnUnavailable {
		case "hold":
			return g.hold(ctx, base, nil)
		default: // open
			r := base
			r.PolicyApplied = "ungated"
			// The reason is board-visible (the gate log, the card's
			// review); the backend error is not — it can carry a provider's
			// response body, a request URL, or what was wrong with a
			// credential. Viewers get a status code at most; the detail goes
			// to the server log.
			log.Printf("admission gate: board %s: curator unavailable: %v", board.ID, err)
			r.Reason = strPtr(unavailableReason(err))
			rec, insErr := db.InsertReview(ctx, g.pool, r)
			if insErr != nil {
				return Outcome{}, insErr
			}
			return Outcome{Allowed: true, ReviewID: &rec.ID, GateFlag: strPtr("ungated")}, nil
		}
	}

	// One revise per intent: if the previous round was already a revise, a
	// second revise is churn — the agent followed the suggestion. Accept with
	// an honest audit trail (policy 'override'), keeping deny available for
	// true duplicates.
	if verdict.Decision == "revise" && priorWasRevise {
		r := base
		r.Verdict = strPtr("revise")
		r.Reason = strPtr("revise-cap: curator returned a second revise after its suggestion was followed; accepted. Curator said: " + verdict.Reason)
		r.Model = strPtr(answered.Model)
		r.Backend = strPtr(answered.Step)
		r.PolicyApplied = "override"
		rec, err := db.InsertReview(ctx, g.pool, r)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Allowed: true, ReviewID: &rec.ID}, nil
	}

	return g.record(ctx, base, verdict, answered, nil)
}

// record writes the review row for a curator verdict and maps it to an
// outcome. who is the ladder rung that answered — its name and model are the
// audit row's provenance.
func (g *Gate) record(ctx context.Context, base db.Review, v Verdict, who escalate.Result, disputeOf *string) (Outcome, error) {
	r := base
	r.Verdict = strPtr(v.Decision)
	r.Reason = strPtr(v.Reason)
	r.Model = strPtr(who.Model)
	r.Backend = strPtr(who.Step)
	ms := int(who.Latency.Milliseconds())
	r.LatencyMS = &ms
	if v.Confidence > 0 {
		c := v.Confidence
		r.Confidence = &c
	}
	r.DisputeOf = disputeOf
	policy := "gated"
	var flag *string
	if disputeOf != nil {
		policy = "tiebroken"
		flag = strPtr("tiebroken")
	}
	r.PolicyApplied = policy

	var dupID *string
	if v.DuplicateOf != nil {
		if card, err := db.GetCardByNumber(ctx, g.pool, base.BoardID, *v.DuplicateOf); err == nil {
			dupID = &card.ID
		}
	}
	r.DuplicateOf = dupID

	rec, err := db.InsertReview(ctx, g.pool, r)
	if err != nil {
		return Outcome{}, err
	}

	switch v.Decision {
	case "accept":
		return Outcome{Allowed: true, ReviewID: &rec.ID, GateFlag: flag}, nil
	case "revise":
		return Outcome{Status: 422, Body: map[string]any{
			"decision": "revise", "reason": v.Reason, "suggestion": v.Suggestion,
			"review_id": rec.ID,
		}}, nil
	default: // deny
		body := map[string]any{
			"decision": "deny", "reason": v.Reason, "review_id": rec.ID,
		}
		if v.DuplicateOf != nil {
			body["duplicate_of"] = *v.DuplicateOf
		}
		if v.Confidence > 0 {
			body["confidence"] = v.Confidence
		}
		return Outcome{Status: 409, Body: body}, nil
	}
}

func (g *Gate) hold(ctx context.Context, base db.Review, disputeOf *string) (Outcome, error) {
	r := base
	r.Verdict = strPtr("held")
	r.PolicyApplied = "held"
	r.DisputeOf = disputeOf
	exp := time.Now().Add(g.heldTTL)
	r.HeldExpiresAt = &exp
	rec, err := db.InsertReview(ctx, g.pool, r)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Held: true, Status: 202, Body: map[string]any{
		"decision": "held", "review_id": rec.ID,
		"expires_at": exp.UTC().Format(time.RFC3339),
	}, ReviewID: &rec.ID}, nil
}

func (g *Gate) tiebreakCheck(ctx context.Context, base db.Review, board db.Board, columns []db.Column, current *db.Card, operation string, payload json.RawMessage, disputeOf, priorReason, rebuttal string) (Outcome, error) {
	// The adjudicator is the reserve rung — the tiebreak model, or the
	// strongest configured backend when none is set. No adjudicator at all,
	// or one that fails: hold so nothing is lost.
	l := g.ladder()
	if !l.CanAdjudicate() {
		return g.hold(ctx, base, &disputeOf)
	}
	in, err := g.buildInput(ctx, board, columns, current, operation, payload)
	if err != nil {
		return Outcome{}, err
	}
	in.PriorDenialReason = priorReason
	in.Rebuttal = rebuttal
	v, answered, err := l.Adjudicate(ctx, in)
	if err != nil {
		return g.hold(ctx, base, &disputeOf)
	}
	return g.record(ctx, base, v, answered, &disputeOf)
}

// buildInput assembles the bounded candidate set: top-10 search results for
// the submission's title, plus the board schema and column names.
func (g *Gate) buildInput(ctx context.Context, board db.Board, columns []db.Column, current *db.Card, operation string, payload json.RawMessage) (Input, error) {
	in := Input{Board: board, Columns: columns, Operation: operation, Payload: payload, Current: current}
	var p struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	_ = json.Unmarshal(payload, &p)
	// Candidates serve duplicate detection, which applies to CREATE only.
	if operation == "create" && p.Title != "" {
		cards, err := db.SearchCards(ctx, g.pool, db.SearchParams{
			BoardID: board.ID, Query: p.Title + " " + p.Body, Limit: 10, AnyWord: true,
		})
		if err != nil {
			return in, err
		}
		in.Candidates = cards
	}
	return in, nil
}

// ladder builds the curator escalation ladder: the configured backends in
// order (cheapest first — local inference before the Claude API), two
// attempts each, plus a single-attempt adjudicator that only disputes reach
// — the tiebreak model, or the strongest backend when none is configured.
func (g *Gate) ladder() escalate.Ladder[Input, Verdict] {
	l := escalate.Ladder[Input, Verdict]{
		Timeout:  g.timeout,
		Validate: validDecision,
	}
	for _, b := range g.backends {
		l.Steps = append(l.Steps, backendStep(b, 2))
	}
	// The adjudicator gets one shot: a dispute it cannot answer is held for a
	// human rather than retried. With no tiebreak configured the strongest
	// backend stands in — and it is built here explicitly, at one attempt,
	// so it does not inherit the two attempts it gets as a curator rung.
	adj := g.tiebreak
	if adj == nil && len(g.backends) > 0 {
		adj = g.backends[len(g.backends)-1]
	}
	if adj != nil {
		s := backendStep(adj, 1)
		l.Adjudicator = &s
	}
	return l
}

func backendStep(b Backend, attempts int) escalate.Step[Input, Verdict] {
	return escalate.Step[Input, Verdict]{
		Name: b.Name(), Model: b.ModelID(), Attempts: attempts,
		Call: b.Review,
	}
}

// validDecision rejects an answer that is not one of the three verdicts —
// a backend that invents a decision has failed, and the ladder climbs on.
func validDecision(v Verdict) error {
	switch v.Decision {
	case "accept", "deny", "revise":
		return nil
	}
	return fmt.Errorf("backend returned invalid decision %q", v.Decision)
}

// runCurator climbs the ladder. "Unavailable" = every backend failed.
func (g *Gate) runCurator(ctx context.Context, in Input) (Verdict, escalate.Result, error) {
	v, res, err := g.ladder().Run(ctx, in)
	if errors.Is(err, escalate.ErrNoSteps) {
		err = errNoBackends
	}
	return v, res, err
}

// errNoBackends: the gate has no curator configured at all.
var errNoBackends = errors.New("no curator backends configured")
