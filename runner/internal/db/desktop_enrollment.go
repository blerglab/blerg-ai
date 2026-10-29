package db

import (
	"context"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DesktopEnrollment struct {
	Owner      string
	TokenID    string
	RequestID  string
	Generation string
	Revision   int64
}

func GetDesktopEnrollment(ctx context.Context, pool *pgxpool.Pool, id string) (DesktopEnrollment, error) {
	var p DesktopEnrollment
	err := pool.QueryRow(ctx, `SELECT COALESCE(enrollment_owner_id,''),COALESCE(enrollment_token_id,''),COALESCE(enrollment_request_id::text,''),COALESCE(enrollment_generation::text,''),enrollment_revision FROM daemons WHERE id=$1`, id).Scan(&p.Owner, &p.TokenID, &p.RequestID, &p.Generation, &p.Revision)
	return p, err
}

// Only binding metadata is persisted. Every retry reserves a higher revision.
func BeginDesktopEnrollment(ctx context.Context, pool *pgxpool.Pool, id string, p DesktopEnrollment) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `UPDATE daemons SET enrollment_owner_id=$2,enrollment_token_id=$3,enrollment_request_id=$4,enrollment_generation=$5,enrollment_revision=$6 WHERE id=$1`, id, p.Owner, p.TokenID, p.RequestID, p.Generation, p.Revision)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO desktop_enrollment_audit(desktop_id,actor_id,action,ownership_revision,connection_generation) VALUES($1,$2,'issued',$3,$4)`, id, p.Owner, p.Revision, p.Generation)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// The caller holds the desktop fence and proves the exact socket's ack or
// possession of its pending credential on a new authenticated hello.
func CompleteDesktopEnrollment(ctx context.Context, pool *pgxpool.Pool, id string, p DesktopEnrollment) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=$3,desktop_compatibility=false,connection_generation=NULL,enrollment_token_id=NULL,enrollment_request_id=NULL,enrollment_generation=NULL,status='disconnected',status_reason='Desktop enrolled; reconnecting with its personal credential.' WHERE id=$1 AND enrollment_owner_id=$2 AND enrollment_revision=$3 AND enrollment_token_id=$4 AND enrollment_request_id=$5 AND enrollment_generation=$6 AND connection_generation=$6 AND (desktop_owner_id IS NULL OR desktop_owner_id=$2)`, id, p.Owner, p.Revision, p.TokenID, p.RequestID, p.Generation)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrDesktopAuthority
	}
	_, err = tx.Exec(ctx, `INSERT INTO desktop_enrollment_audit(desktop_id,actor_id,action,ownership_revision,connection_generation) VALUES($1,$2,'enrolled',$3,$4)`, id, p.Owner, p.Revision, p.Generation)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// A preflight may recognize lost-ack recovery, but must not commit it. Match
// every pending identity and generation predicate used by completion.
func PendingDesktopBinding(ctx context.Context, pool *pgxpool.Pool, b desktopauth.Binding) bool {
	var ok bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM daemons WHERE id=$1
 AND enrollment_token_id=$2 AND enrollment_owner_id=$3 AND enrollment_revision=$4
 AND enrollment_token_id<>'' AND enrollment_request_id IS NOT NULL
 AND enrollment_generation=connection_generation
 AND (desktop_owner_id IS NULL OR desktop_owner_id=$3) AND mode<>'runner')`, b.DesktopID, b.TokenID, b.OwnerID, b.Revision).Scan(&ok)
	return err == nil && ok
}

// The caller has authenticated a person and checked that they may enroll this
// desktop. Outcomes contain binding metadata only, never the issuer's response.
func RecordDesktopEnrollmentOutcome(ctx context.Context, pool *pgxpool.Pool, id, actor, outcome, generation string, revision int64) error {
	_, err := pool.Exec(ctx, `INSERT INTO desktop_enrollment_audit(desktop_id,actor_id,action,ownership_revision,connection_generation) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid)`, id, actor, outcome, revision, generation)
	return err
}
