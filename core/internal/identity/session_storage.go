package identity

import (
	"context"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/contracts/sessionstorage"
	"github.com/jackc/pgx/v5"
)

// SessionStorageState is the saved install-wide session storage, or the environment seeds.
type SessionStorageState struct {
	sessionstorage.Limits
	Set       bool
	UpdatedAt *time.Time
}

// SessionStorage reads the saved settings; no row means the environment seeds.
func (s *Service) SessionStorage(ctx context.Context) (SessionStorageState, error) {
	var st SessionStorageState
	err := s.st.Pool().QueryRow(ctx, `SELECT default_upload_mb, max_upload_mb, updated_at FROM session_storage_limits WHERE id`).
		Scan(&st.DefaultUploadMB, &st.MaxUploadMB, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionStorageState{Limits: s.SessionStorageDefaults()}, nil
	}
	if err != nil {
		return SessionStorageState{}, err
	}
	st.Set = true
	return st, nil
}

// SessionStorageDefaults are the environment seeds (sessionstorage.FromEnv at startup).
func (s *Service) SessionStorageDefaults() sessionstorage.Limits {
	if s.sessionStorageDefaults.DefaultUploadMB == 0 {
		return sessionstorage.FromEnv()
	}
	return s.sessionStorageDefaults
}

// SetSessionStorage saves the settings and an audit row (who, from what, to what) in one
// transaction, so a change is never in effect without its record.
func (s *Service) SetSessionStorage(ctx context.Context, by string, l sessionstorage.Limits) (before SessionStorageState, err error) {
	if err = l.Validate(); err != nil {
		return before, err
	}
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return before, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	before.Limits = s.SessionStorageDefaults()
	err = tx.QueryRow(ctx, `SELECT default_upload_mb, max_upload_mb, updated_at FROM session_storage_limits WHERE id FOR UPDATE`).
		Scan(&before.DefaultUploadMB, &before.MaxUploadMB, &before.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return before, err
	default:
		before.Set = true
	}
	if _, err = tx.Exec(ctx, `INSERT INTO session_storage_limits(id,default_upload_mb,max_upload_mb,updated_at,updated_by)
 VALUES(true,$1,$2,now(),$3::uuid) ON CONFLICT(id) DO UPDATE SET default_upload_mb=EXCLUDED.default_upload_mb,
 max_upload_mb=EXCLUDED.max_upload_mb,updated_at=now(),updated_by=EXCLUDED.updated_by`, l.DefaultUploadMB, l.MaxUploadMB, by); err != nil {
		return before, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO session_storage_limits_audit(actor,previous_default_mb,previous_max_mb,default_upload_mb,max_upload_mb)
 VALUES($1::uuid,$2,$3,$4,$5)`, by, before.DefaultUploadMB, before.MaxUploadMB, l.DefaultUploadMB, l.MaxUploadMB); err != nil {
		return before, err
	}
	return before, tx.Commit(ctx)
}
