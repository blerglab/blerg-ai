package db

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrAnswerConflict = errors.New("answer request changed")

type AnswerRequest struct {
	ID              string          `json:"request_id"`
	SessionID       string          `json:"session_id"`
	ArtifactID      string          `json:"artifact_id"`
	ArtifactName    string          `json:"artifact_name"`
	ArtifactVersion int             `json:"artifact_version"`
	Ask             string          `json:"ask"`
	Schema          json.RawMessage `json:"schema"`
	Result          json.RawMessage `json:"result,omitempty"`
	AnsweredBy      string          `json:"-"`
	Delivered       bool            `json:"delivered"`
	Superseded      bool            `json:"-"`
}

const answerCols = `id::text,session_id::text,artifact_id,artifact_name,artifact_version,ask,schema,result,coalesce(answered_by,''),delivered,superseded`

func scanAnswer(row pgx.Row) (AnswerRequest, error) {
	var r AnswerRequest
	err := row.Scan(&r.ID, &r.SessionID, &r.ArtifactID, &r.ArtifactName, &r.ArtifactVersion, &r.Ask, &r.Schema, &r.Result, &r.AnsweredBy, &r.Delivered, &r.Superseded)
	return r, err
}
func GetAnswerRequest(ctx context.Context, pool *pgxpool.Pool, sid, aid string) (*AnswerRequest, error) {
	r, err := scanAnswer(pool.QueryRow(ctx, `SELECT `+answerCols+` FROM answer_requests WHERE session_id=$1::uuid AND artifact_id=$2`, sid, aid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}
func InsertAnswerRequest(ctx context.Context, pool *pgxpool.Pool, r AnswerRequest) (AnswerRequest, error) {
	// Same lock as artifact version allocation: an obsolete version cannot acquire a new request.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return r, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "artifacts:"+r.SessionID); err != nil {
		return r, err
	}
	var current bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (
  SELECT 1 FROM session_artifacts a JOIN sessions s ON s.id=a.session_id
  WHERE a.id=$1 AND a.session_id=$2::uuid AND a.origin='agent' AND s.kind='agent'
    AND s.status NOT IN ('ended','stopped','error')
    AND s.archived_at IS NULL AND s.discard_requested_at IS NULL
    AND NOT EXISTS (SELECT 1 FROM session_artifacts b
      WHERE b.session_id=a.session_id AND b.origin=a.origin AND b.name=a.name AND b.version>a.version)
 )`, r.ArtifactID, r.SessionID).Scan(&current)
	if err != nil {
		return r, err
	}
	if !current {
		return r, ErrAnswerConflict
	}
	got, err := scanAnswer(tx.QueryRow(ctx, `INSERT INTO answer_requests(session_id,artifact_id,artifact_name,artifact_version,ask,schema) VALUES($1::uuid,$2,$3,$4,$5,$6) ON CONFLICT(artifact_id) DO UPDATE SET artifact_id=excluded.artifact_id RETURNING `+answerCols, r.SessionID, r.ArtifactID, r.ArtifactName, r.ArtifactVersion, r.Ask, r.Schema))
	if err != nil {
		return got, err
	}
	if got.Superseded {
		return got, ErrAnswerConflict
	}
	return got, tx.Commit(ctx)
}

// AcceptAnswer freezes the first valid result. Identical retries return it; changed retries fail.
func AcceptAnswer(ctx context.Context, pool *pgxpool.Pool, sid, aid, account string, result json.RawMessage) (AnswerRequest, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return AnswerRequest{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "artifacts:"+sid); err != nil {
		return AnswerRequest{}, err
	}
	var current bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (
  SELECT 1 FROM session_artifacts a JOIN sessions s ON s.id=a.session_id
  WHERE a.id=$1 AND a.session_id=$2::uuid AND a.origin='agent'
    AND s.spawning_account_id=$3 AND s.kind='agent' AND s.status NOT IN ('ended','stopped','error')
    AND s.archived_at IS NULL AND s.discard_requested_at IS NULL
    AND NOT EXISTS (SELECT 1 FROM session_artifacts b
      WHERE b.session_id=a.session_id AND b.origin=a.origin AND b.name=a.name AND b.version>a.version)
 )`, aid, sid, account).Scan(&current)
	if err != nil {
		return AnswerRequest{}, err
	}
	if !current {
		return AnswerRequest{}, ErrAnswerConflict
	}
	got, err := scanAnswer(tx.QueryRow(ctx, `UPDATE answer_requests SET result=COALESCE(result,$3::json),answered_by=$4
 WHERE session_id=$1::uuid AND artifact_id=$2 AND NOT superseded AND (result IS NULL OR result::text=$3::text)
 RETURNING `+answerCols, sid, aid, result, account))
	if errors.Is(err, pgx.ErrNoRows) {
		return got, ErrAnswerConflict
	}
	if err != nil {
		return got, err
	}
	return got, tx.Commit(ctx)
}
func MarkAnswerDelivered(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `UPDATE answer_requests SET delivered=true WHERE id=$1::uuid`, id)
	return err
}
func PendingAnswers(ctx context.Context, pool *pgxpool.Pool, sid string) ([]AnswerRequest, error) {
	rows, err := pool.Query(ctx, `SELECT `+answerCols+` FROM answer_requests r
 WHERE session_id=$1::uuid AND NOT delivered AND NOT superseded AND EXISTS (
  SELECT 1 FROM session_artifacts a WHERE a.id=r.artifact_id AND NOT EXISTS (
   SELECT 1 FROM session_artifacts b
   WHERE b.session_id=a.session_id AND b.origin=a.origin AND b.name=a.name AND b.version>a.version))
 ORDER BY created_at`, sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AnswerRequest{}
	for rows.Next() {
		r, err := scanAnswer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CloseArtifactAnswer lets either artifact deletion route retire its open request and
// emit one close event. The row remains as immutable request/result history.
func CloseArtifactAnswer(ctx context.Context, pool *pgxpool.Pool, sid, aid string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `UPDATE answer_requests SET superseded=true
 WHERE session_id=$1::uuid AND artifact_id=$2 AND NOT delivered AND NOT superseded
 RETURNING id::text`, sid, aid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}
