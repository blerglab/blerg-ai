package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/workbranch"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AssignSessionBranch resolves a collision and records the name once. The lock
// serializes concurrent launches; changing a preference never renames a session.
func AssignSessionBranch(ctx context.Context, pool *pgxpool.Pool, id, candidate string, custom ...bool) (string, error) {
	if !workbranch.Valid(candidate) {
		return "", errors.New("invalid work branch")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(254098)`); err != nil {
		return "", err
	}
	isCustom := len(custom) > 0 && custom[0]
	var recordedCustom bool
	var recorded string
	if err = tx.QueryRow(ctx, `SELECT work_branch, work_branch_custom FROM sessions WHERE id=$1 FOR UPDATE`, id).Scan(&recorded, &recordedCustom); err != nil {
		return "", err
	}
	var historicalCustom bool
	var historical string
	historyErr := tx.QueryRow(ctx, `SELECT branch, custom FROM session_branch_reservations WHERE session_id=$1`, id).Scan(&historical, &historicalCustom)
	if historyErr != nil && !errors.Is(historyErr, pgx.ErrNoRows) {
		return "", historyErr
	}
	switch {
	case historical != "":
		if recorded != "" && recorded != historical {
			return "", errors.New("recorded work branch differs from its reservation")
		}
		candidate = historical
		isCustom = historicalCustom
	case recorded != "":
		candidate = recorded
		isCustom = recordedCustom
	default:
		base := candidate
		if len(base) > 190 {
			base = strings.ToValidUTF8(base[:190], "")
		}
		for attempt := 0; ; attempt++ {
			var exists bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_branch_reservations WHERE branch=$1 UNION ALL SELECT 1 FROM sessions WHERE work_branch=$1)`, candidate).Scan(&exists); err != nil {
				return "", err
			}
			if !exists {
				break
			}
			if attempt == 0 {
				candidate = base + "-" + id
			} else {
				candidate = fmt.Sprintf("%s-%s-%d", base, id, attempt)
			}
		}
	}
	if !workbranch.Valid(candidate) {
		return "", errors.New("work branch is too long after collision suffix")
	}
	if historical == "" {
		if _, err = tx.Exec(ctx, `INSERT INTO session_branch_reservations(branch,session_id,custom) VALUES($1,$2,$3)`, candidate, id, isCustom); err != nil {
			return "", err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET work_branch=$2, work_branch_custom=$3 WHERE id=$1`, id, candidate, isCustom); err != nil {
		return "", err
	}
	return candidate, tx.Commit(ctx)
}
