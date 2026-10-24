package identity

import (
	"context"
	"errors"

	"github.com/blerglab/blerg-ai/contracts/updatenotice"
	"github.com/jackc/pgx/v5"
)

// UpdateNoticeEnabled uses the environment default until an administrator saves a setting.
func (s *Service) UpdateNoticeEnabled(ctx context.Context) (bool, error) {
	var enabled bool
	err := s.st.Pool().QueryRow(ctx, `SELECT enabled FROM update_notice_settings WHERE id`).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return updatenotice.Enabled(), nil
	}
	return enabled, err
}

func (s *Service) SetUpdateNotice(ctx context.Context, by string, enabled bool) error {
	_, err := s.st.Pool().Exec(ctx, `INSERT INTO update_notice_settings(id,enabled,updated_by)
 VALUES(true,$1,$2::uuid) ON CONFLICT(id) DO UPDATE SET enabled=EXCLUDED.enabled,
 updated_at=now(),updated_by=EXCLUDED.updated_by`, enabled, by)
	return err
}

// ReleaseChecker uses the saved install setting for both polling and public notices.
func (s *Service) ReleaseChecker(running, build string) *updatenotice.Checker {
	checker := updatenotice.NewChecker(running, build)
	// An environment value seeds the default; a saved administrator choice wins.
	checker.Disabled = !updatenotice.ValidRepo(checker.Repo)
	checker.Allowed = func(ctx context.Context) bool {
		enabled, err := s.UpdateNoticeEnabled(ctx)
		return err == nil && enabled
	}
	return checker
}
