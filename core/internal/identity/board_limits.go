package identity

// Board call limits (0.4.5): how much board traffic a session's board calls may make through the
// runner, and how many live exchange tokens an account may hold. An administrator sets them under
// Settings (PUT /api/settings/board-limits); with nothing set, DefaultBoardLimits apply. Core
// enforces LiveExchangeTokens itself, read on every exchange; the runner reads the other three and
// caches them for a minute.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// BoardLimits are the board call limits. JSON names are the wire's (core's settings API and the
// runner's internal read).
type BoardLimits struct {
	// CallsPerMinute is one session's board requests in a rolling minute.
	CallsPerMinute int `json:"calls_per_minute"`
	// CallsInFlight is one session's board calls running at once (each holds an exchange token).
	CallsInFlight int `json:"calls_in_flight"`
	// UnrevokedPerAccount is how many of an account's session board credentials may still be live
	// after a failed revoke before the runner stops minting more for it.
	UnrevokedPerAccount int `json:"unrevoked_per_account"`
	// LiveExchangeTokens is how many live exchange tokens one account may hold at core.
	LiveExchangeTokens int `json:"live_exchange_tokens"`
}

// DefaultBoardLimits apply while an administrator has set none.
var DefaultBoardLimits = BoardLimits{
	CallsPerMinute:      600,
	CallsInFlight:       8,
	UnrevokedPerAccount: 20,
	LiveExchangeTokens:  maxLiveExchangeTokens,
}

// BoardLimitRange is the range each limit may be set to: the migration's CHECKs.
var BoardLimitRange = map[string][2]int{
	"calls_per_minute":      {1, 10000},
	"calls_in_flight":       {1, 64},
	"unrevoked_per_account": {1, 1000},
	"live_exchange_tokens":  {10, 10000},
}

// ErrInvalidBoardLimits is a limit out of its range, or limits that contradict each other.
var ErrInvalidBoardLimits = errors.New("identity: invalid board limits")

// Validate checks each limit against BoardLimitRange, and that the per-account allowances fit in
// the account's live exchange tokens: an account's unrevoked credentials and one session's calls in
// flight must each leave room below LiveExchangeTokens.
func (l BoardLimits) Validate() error {
	// In the order the settings page shows them, so the first bad one is named.
	for _, f := range []struct {
		name string
		v    int
	}{
		{"calls_per_minute", l.CallsPerMinute}, {"calls_in_flight", l.CallsInFlight},
		{"unrevoked_per_account", l.UnrevokedPerAccount}, {"live_exchange_tokens", l.LiveExchangeTokens},
	} {
		r := BoardLimitRange[f.name]
		if f.v < r[0] || f.v > r[1] {
			return fmt.Errorf("%w: %s must be between %d and %d", ErrInvalidBoardLimits, f.name, r[0], r[1])
		}
	}
	if l.UnrevokedPerAccount >= l.LiveExchangeTokens {
		return fmt.Errorf("%w: unrevoked_per_account must be below live_exchange_tokens", ErrInvalidBoardLimits)
	}
	if l.CallsInFlight >= l.LiveExchangeTokens {
		return fmt.Errorf("%w: calls_in_flight must be below live_exchange_tokens", ErrInvalidBoardLimits)
	}
	return nil
}

// BoardLimitsState is the limits in force and, when an administrator set them, when and by whom.
type BoardLimitsState struct {
	BoardLimits
	Set       bool
	UpdatedAt *time.Time
	UpdatedBy string
}

// BoardLimits reads the limits in force (DefaultBoardLimits when none are set).
func (s *Service) BoardLimits(ctx context.Context) (BoardLimitsState, error) {
	var st BoardLimitsState
	var at time.Time
	var by *string
	err := s.st.Pool().QueryRow(ctx, `SELECT calls_per_minute, calls_in_flight, unrevoked_per_account, live_exchange_tokens,
		updated_at, updated_by::text FROM board_call_limits WHERE id`).
		Scan(&st.CallsPerMinute, &st.CallsInFlight, &st.UnrevokedPerAccount, &st.LiveExchangeTokens, &at, &by)
	if errors.Is(err, pgx.ErrNoRows) {
		return BoardLimitsState{BoardLimits: DefaultBoardLimits}, nil
	}
	if err != nil {
		return BoardLimitsState{}, err
	}
	st.Set, st.UpdatedAt = true, &at
	if by != nil {
		st.UpdatedBy = *by
	}
	return st, nil
}

// SetBoardLimits stores l (validated) as set by the administrator account by.
func (s *Service) SetBoardLimits(ctx context.Context, by string, l BoardLimits) error {
	if err := l.Validate(); err != nil {
		return err
	}
	_, err := s.st.Pool().Exec(ctx, `INSERT INTO board_call_limits (id, calls_per_minute, calls_in_flight, unrevoked_per_account, live_exchange_tokens, updated_at, updated_by)
		VALUES (true, $1, $2, $3, $4, now(), $5::uuid)
		ON CONFLICT (id) DO UPDATE SET calls_per_minute = EXCLUDED.calls_per_minute, calls_in_flight = EXCLUDED.calls_in_flight,
		  unrevoked_per_account = EXCLUDED.unrevoked_per_account, live_exchange_tokens = EXCLUDED.live_exchange_tokens,
		  updated_at = now(), updated_by = EXCLUDED.updated_by`,
		l.CallsPerMinute, l.CallsInFlight, l.UnrevokedPerAccount, l.LiveExchangeTokens, by)
	return err
}
