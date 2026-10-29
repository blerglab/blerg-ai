package identity

import (
	"context"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/contracts/childlimits"
	"github.com/jackc/pgx/v5"
)

type ChildLimitsState struct {
	childlimits.Limits
	Set       bool
	UpdatedAt *time.Time
}

func (s *Service) ChildLimits(ctx context.Context) (ChildLimitsState, error) {
	var st ChildLimitsState
	err := s.st.Pool().QueryRow(ctx, `SELECT parallel, starts_per_hour, updated_at FROM child_start_limits WHERE id`).Scan(&st.Parallel, &st.StartsPerHour, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChildLimitsState{Limits: s.childLimitDefaults}, nil
	}
	if err != nil {
		return ChildLimitsState{}, err
	}
	st.Set = true
	return st, nil
}

func (s *Service) ChildLimitDefaults() childlimits.Limits { return s.childLimitDefaults }

func (s *Service) SetChildLimits(ctx context.Context, by string, l childlimits.Limits) error {
	if err := l.Validate(); err != nil {
		return err
	}
	_, err := s.st.Pool().Exec(ctx, `INSERT INTO child_start_limits(id,parallel,starts_per_hour,updated_at,updated_by)
 VALUES(true,$1,$2,now(),$3::uuid) ON CONFLICT(id) DO UPDATE SET parallel=EXCLUDED.parallel,
 starts_per_hour=EXCLUDED.starts_per_hour,updated_at=now(),updated_by=EXCLUDED.updated_by`, l.Parallel, l.StartsPerHour, by)
	return err
}
