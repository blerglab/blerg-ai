package db

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Review is one admission_reviews row. A row is written for EVERY gate
// invocation, whether or not a card results — a denied submission never
// becomes a card, so without this table rejections would be invisible.
type Review struct {
	ID               string          `json:"id"`
	BoardID          string          `json:"board_id"`
	SubmittedAt      time.Time       `json:"submitted_at"`
	ActorTokenID     *string         `json:"actor_token_id"`
	Operation        string          `json:"operation"`
	PayloadHash      []byte          `json:"-"`
	Payload          json.RawMessage `json:"payload"`
	Verdict          *string         `json:"verdict"`
	Reason           *string         `json:"reason"`
	DuplicateOf      *string         `json:"duplicate_of"`
	Model            *string         `json:"model"`
	Backend          *string         `json:"backend"`
	LatencyMS        *int            `json:"latency_ms"`
	Confidence       *float32        `json:"confidence"`
	PolicyApplied    string          `json:"policy_applied"`
	DisputeOf        *string         `json:"dispute_of"`
	TiebreakReviewID *string         `json:"tiebreak_review_id"`
	ResolvedBy       *string         `json:"resolved_by"`
	ResolvedAt       *time.Time      `json:"resolved_at"`
	HeldExpiresAt    *time.Time      `json:"held_expires_at"`
	CardID           *string         `json:"card_id"`
	// TargetCardID/TargetVersion snapshot the card an update/move/archive/
	// delete was gated against, so a later approve can re-fetch it and detect
	// drift. Nil for create (no target) and for rows predating this field.
	TargetCardID  *string `json:"target_card_id"`
	TargetVersion *int    `json:"target_version"`
}

const reviewCols = `id, board_id, submitted_at, actor_token_id, operation, payload_hash,
	payload, verdict, reason, duplicate_of, model, backend, latency_ms, confidence,
	policy_applied, dispute_of, tiebreak_review_id, resolved_by, resolved_at,
	held_expires_at, card_id, target_card_id, target_version`

func scanReview(row pgx.Row) (Review, error) {
	var r Review
	err := row.Scan(&r.ID, &r.BoardID, &r.SubmittedAt, &r.ActorTokenID, &r.Operation,
		&r.PayloadHash, &r.Payload, &r.Verdict, &r.Reason, &r.DuplicateOf, &r.Model,
		&r.Backend, &r.LatencyMS, &r.Confidence, &r.PolicyApplied, &r.DisputeOf,
		&r.TiebreakReviewID, &r.ResolvedBy, &r.ResolvedAt, &r.HeldExpiresAt, &r.CardID,
		&r.TargetCardID, &r.TargetVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func InsertReview(ctx context.Context, pool *pgxpool.Pool, r Review) (Review, error) {
	return scanReview(pool.QueryRow(ctx, `
		INSERT INTO admission_reviews (board_id, actor_token_id, operation, payload_hash,
			payload, verdict, reason, duplicate_of, model, backend, latency_ms, confidence,
			policy_applied, dispute_of, tiebreak_review_id, resolved_by, resolved_at,
			held_expires_at, card_id, target_card_id, target_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		RETURNING `+reviewCols,
		r.BoardID, r.ActorTokenID, r.Operation, r.PayloadHash, r.Payload, r.Verdict,
		r.Reason, r.DuplicateOf, r.Model, r.Backend, r.LatencyMS, r.Confidence,
		r.PolicyApplied, r.DisputeOf, r.TiebreakReviewID, r.ResolvedBy, r.ResolvedAt,
		r.HeldExpiresAt, r.CardID, r.TargetCardID, r.TargetVersion))
}

func GetReview(ctx context.Context, pool *pgxpool.Pool, id string) (Review, error) {
	return scanReview(pool.QueryRow(ctx,
		`SELECT `+reviewCols+` FROM admission_reviews WHERE id = $1`, id))
}

// SetReviewCard back-fills card_id once the accepted write lands.
func SetReviewCard(ctx context.Context, pool *pgxpool.Pool, reviewID, cardID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE admission_reviews SET card_id = $2 WHERE id = $1`, reviewID, cardID)
	return err
}

// HasDispute reports whether a review is already the target of a dispute —
// the enforceable "one dispute per review" bound.
func HasDispute(ctx context.Context, pool *pgxpool.Pool, reviewID string) (bool, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM admission_reviews WHERE dispute_of = $1`, reviewID).Scan(&n)
	return n > 0, err
}

// RecentDenialByHash bounds bare resubmission: an identical payload denied
// within the window, from the same actor, is rejected without a curator
// call. actor is nil for core-issued principals (see gate.Check) — compared
// with IS NOT DISTINCT FROM so a nil actor still dedups against other rows
// with a nil actor_token_id rather than matching nothing (= never matches
// itself under plain equality, since NULL = NULL is unknown, not true).
func RecentDenialByHash(ctx context.Context, pool *pgxpool.Pool, boardID string, hash []byte, actor *string, window time.Duration) (bool, error) {
	var n int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM admission_reviews
		WHERE board_id = $1 AND payload_hash = $2 AND actor_token_id IS NOT DISTINCT FROM $3
		  AND verdict = 'deny' AND submitted_at > now() - $4::interval`,
		boardID, hash, actor, window.String()).Scan(&n)
	return n > 0, err
}

// ListHeldReviews returns the unresolved held queue for a board ("" = all).
func ListHeldReviews(ctx context.Context, pool *pgxpool.Pool, boardID string) ([]Review, error) {
	where := `verdict = 'held' AND resolved_at IS NULL`
	args := []any{}
	if boardID != "" {
		where += ` AND board_id = $1`
		args = append(args, boardID)
	}
	rows, err := pool.Query(ctx,
		`SELECT `+reviewCols+` FROM admission_reviews WHERE `+where+` ORDER BY submitted_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResolveHeldReview is the single permitted mutation of an admission_reviews
// row: a held row gains verdict/resolved_by/resolved_at (+card_id on approve).
func ResolveHeldReview(ctx context.Context, pool *pgxpool.Pool, id, verdict, resolvedBy string, cardID *string) (Review, error) {
	r, err := scanReview(pool.QueryRow(ctx, `
		UPDATE admission_reviews
		SET verdict = $2, resolved_by = $3, resolved_at = now(), card_id = coalesce($4, card_id)
		WHERE id = $1 AND verdict = 'held' AND resolved_at IS NULL
		RETURNING `+reviewCols, id, verdict, resolvedBy, cardID))
	if errors.Is(err, ErrNotFound) {
		return r, ErrNotFound
	}
	return r, err
}

// ExpiredHeldReviews returns held rows past their TTL for the timeout sweep.
func ExpiredHeldReviews(ctx context.Context, pool *pgxpool.Pool) ([]Review, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+reviewCols+` FROM admission_reviews
		WHERE verdict = 'held' AND resolved_at IS NULL AND held_expires_at < now()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListReviews is the gate log (most recent first).
func ListReviews(ctx context.Context, pool *pgxpool.Pool, boardID string, limit int) ([]Review, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where, args := ``, []any{}
	if boardID != "" {
		where = `WHERE board_id = $1`
		args = append(args, boardID)
	}
	args = append(args, limit)
	rows, err := pool.Query(ctx,
		`SELECT `+reviewCols+` FROM admission_reviews `+where+
			` ORDER BY submitted_at DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastGatedReview returns the actor's most recent curator-judged review on a
// board for one operation within the window — the gate's anti-churn memory.
// actor is nil for core-issued principals (see gate.Check); IS NOT DISTINCT
// FROM matches the actor_token_id column the same way for both cases (see
// RecentDenialByHash).
func LastGatedReview(ctx context.Context, pool *pgxpool.Pool, boardID string, actor *string, operation string, window time.Duration) (*Review, error) {
	r, err := scanReview(pool.QueryRow(ctx, `
		SELECT `+reviewCols+` FROM admission_reviews
		WHERE board_id = $1 AND actor_token_id IS NOT DISTINCT FROM $2 AND operation = $3
		  AND policy_applied IN ('gated','override')
		  AND submitted_at > now() - $4::interval
		ORDER BY submitted_at DESC LIMIT 1`,
		boardID, actor, operation, window.String()))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
