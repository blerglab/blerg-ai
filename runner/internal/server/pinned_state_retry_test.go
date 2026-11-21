package server

import (
	"errors"
	"fmt"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPinnedStateRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("wrapped: %w", db.ErrDesktopAuthority), false},
		{errors.Join(db.ErrDesktopAuthority, &pgconn.PgError{Code: "55P03"}), false},
		{errPinnedStateRetry, true},
		{&pgconn.PgError{Code: "55P03"}, true},
		{errors.New("other"), false},
	} {
		if got := retryPinnedState(tc.err); got != tc.want {
			t.Errorf("retry(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
