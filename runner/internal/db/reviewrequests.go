package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Review requests (migration 069): a session asks its owner to review one published file or diff
// at an exact version (blerg-runner review request). The owner approves it or requests changes; the
// session withdraws it; a newer version of the same file supersedes it while it is pending.

var (
	ErrReviewRequestNotFound = errors.New("review request not found")
	ErrReviewRequestState    = errors.New("review request is not pending")
)

// Review request states.
const (
	ReviewPending          = "pending"
	ReviewApproved         = "approved"
	ReviewChangesRequested = "changes_requested"
	ReviewWithdrawn        = "withdrawn"
	ReviewSuperseded       = "superseded"
)

// ReviewRequest is one review_requests row.
type ReviewRequest struct {
	ID, SessionID                            string
	ArtifactID, ArtifactName, ArtifactOrigin string
	ArtifactVersion                          int
	ArtifactView                             string
	Ask, State, Note                         string
	SupersededBy                             int // the version that superseded it; 0 otherwise
	DecidedBy                                string
	DecidedAt                                *time.Time
	CreatedAt                                time.Time
}

const reviewRequestCols = `id::text, session_id::text, artifact_id, artifact_name, artifact_origin, artifact_version,
	artifact_view, ask, state, note, COALESCE(superseded_by, 0), COALESCE(decided_by, ''), decided_at, created_at`

func scanReviewRequest(row pgx.Row) (ReviewRequest, error) {
	var r ReviewRequest
	err := row.Scan(&r.ID, &r.SessionID, &r.ArtifactID, &r.ArtifactName, &r.ArtifactOrigin, &r.ArtifactVersion,
		&r.ArtifactView, &r.Ask, &r.State, &r.Note, &r.SupersededBy, &r.DecidedBy, &r.DecidedAt, &r.CreatedAt)
	return r, err
}

func scanReviewRequests(rows pgx.Rows) ([]ReviewRequest, error) {
	defer rows.Close()
	out := []ReviewRequest{}
	for rows.Next() {
		r, err := scanReviewRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertReviewRequest stores a pending request for r's artifact. When that exact version already has
// a pending request, created=false and the existing row is returned unchanged.
func InsertReviewRequest(ctx context.Context, pool *pgxpool.Pool, r ReviewRequest) (ReviewRequest, bool, error) {
	origin := r.ArtifactOrigin
	if origin == "" {
		origin = OriginAgent
	}
	got, err := scanReviewRequest(pool.QueryRow(ctx, `
		INSERT INTO review_requests (session_id, artifact_id, artifact_name, artifact_origin, artifact_version, artifact_view, ask)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		ON CONFLICT DO NOTHING
		RETURNING `+reviewRequestCols,
		r.SessionID, r.ArtifactID, r.ArtifactName, origin, r.ArtifactVersion, r.ArtifactView, r.Ask))
	if err == nil {
		return got, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ReviewRequest{}, false, err
	}
	existing, err := scanReviewRequest(pool.QueryRow(ctx, `SELECT `+reviewRequestCols+` FROM review_requests
		WHERE session_id = $1::uuid AND artifact_id = $2 AND state = 'pending'`, r.SessionID, r.ArtifactID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewRequest{}, false, ErrReviewRequestState // decided between the insert and the read
	}
	if err != nil {
		return ReviewRequest{}, false, err
	}
	return existing, false, nil
}

// GetReviewRequest returns the request with this id; nil, nil when absent or not a uuid.
func GetReviewRequest(ctx context.Context, pool *pgxpool.Pool, id string) (*ReviewRequest, error) {
	r, err := scanReviewRequest(pool.QueryRow(ctx,
		`SELECT `+reviewRequestCols+` FROM review_requests WHERE id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListReviewRequests is the session's requests, oldest first (at most the newest 500).
func ListReviewRequests(ctx context.Context, pool *pgxpool.Pool, sessionID string) ([]ReviewRequest, error) {
	rows, err := pool.Query(ctx, `SELECT * FROM (SELECT `+reviewRequestCols+` FROM review_requests
		WHERE session_id = $1::uuid ORDER BY created_at DESC, id DESC LIMIT 500) r ORDER BY created_at, id`, sessionID)
	if err != nil {
		return nil, err
	}
	return scanReviewRequests(rows)
}

// ListPendingReviewRequests is the session's pending requests, oldest first.
func ListPendingReviewRequests(ctx context.Context, pool *pgxpool.Pool, sessionID string) ([]ReviewRequest, error) {
	rows, err := pool.Query(ctx, `SELECT `+reviewRequestCols+` FROM review_requests
		WHERE session_id = $1::uuid AND state = 'pending' ORDER BY created_at, id`, sessionID)
	if err != nil {
		return nil, err
	}
	return scanReviewRequests(rows)
}

// settleReviewRequest moves a pending request of the session to state (compare-and-set). It
// returns ErrReviewRequestNotFound for no such request of the session, and ErrReviewRequestState
// with the row as it is for one no longer pending.
func settleReviewRequest(ctx context.Context, pool *pgxpool.Pool, id, sessionID, state, note, decidedBy string) (ReviewRequest, error) {
	got, err := scanReviewRequest(pool.QueryRow(ctx, `
		UPDATE review_requests SET state = $3, note = $4, decided_by = NULLIF($5, ''), decided_at = now()
		 WHERE id::text = $1 AND session_id = $2::uuid AND state = 'pending'
		RETURNING `+reviewRequestCols, id, sessionID, state, note, decidedBy))
	if err == nil {
		return got, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ReviewRequest{}, err
	}
	cur, err := scanReviewRequest(pool.QueryRow(ctx,
		`SELECT `+reviewRequestCols+` FROM review_requests WHERE id::text = $1 AND session_id = $2::uuid`, id, sessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewRequest{}, ErrReviewRequestNotFound
	}
	if err != nil {
		return ReviewRequest{}, err
	}
	return cur, ErrReviewRequestState
}

// DecideReviewRequest records the owner's decision (ReviewApproved or ReviewChangesRequested).
func DecideReviewRequest(ctx context.Context, pool *pgxpool.Pool, id, sessionID, state, note, account string) (ReviewRequest, error) {
	if state != ReviewApproved && state != ReviewChangesRequested {
		return ReviewRequest{}, errors.New("not a decision")
	}
	return settleReviewRequest(ctx, pool, id, sessionID, state, note, account)
}

// WithdrawReviewRequest is the session taking its own pending request back.
func WithdrawReviewRequest(ctx context.Context, pool *pgxpool.Pool, id, sessionID string) (ReviewRequest, error) {
	return settleReviewRequest(ctx, pool, id, sessionID, ReviewWithdrawn, "", "")
}

// SupersedeReviewRequests marks every pending request for an older version of the session's file
// (same name and origin) superseded by version, and returns them.
func SupersedeReviewRequests(ctx context.Context, pool *pgxpool.Pool, sessionID, origin, name string, version int) ([]ReviewRequest, error) {
	rows, err := pool.Query(ctx, `
		UPDATE review_requests SET state = 'superseded', superseded_by = $4, decided_at = now()
		 WHERE session_id = $1::uuid AND artifact_origin = $2 AND artifact_name = $3
		   AND artifact_version < $4 AND state = 'pending'
		RETURNING `+reviewRequestCols, sessionID, origin, name, version)
	if err != nil {
		return nil, err
	}
	return scanReviewRequests(rows)
}

// WithdrawReviewRequestsForArtifact withdraws the pending requests of a file that was deleted, and
// returns them.
func WithdrawReviewRequestsForArtifact(ctx context.Context, pool *pgxpool.Pool, sessionID, artifactID string) ([]ReviewRequest, error) {
	rows, err := pool.Query(ctx, `
		UPDATE review_requests SET state = 'withdrawn', note = 'the file was deleted', decided_at = now()
		 WHERE session_id = $1::uuid AND artifact_id = $2 AND state = 'pending'
		RETURNING `+reviewRequestCols, sessionID, artifactID)
	if err != nil {
		return nil, err
	}
	return scanReviewRequests(rows)
}
