package identity

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestSessionRetryableDatabaseErrors(t *testing.T) {
	for _, code := range []string{"55P03", "40P01", "40001"} {
		t.Run(code, func(t *testing.T) {
			if !errors.Is(sessionBusy(&pgconn.PgError{Code: code}), ErrSessionBusy) {
				t.Fatal("retryable database abort must not end a session")
			}
		})
	}
}

func TestSessionRevokeRetry(t *testing.T) {
	for _, code := range []string{"55P03", "40P01", "40001"} {
		t.Run(code, func(t *testing.T) {
			attempts := 0
			err := retryBusy(func() error {
				attempts++
				if attempts < 3 {
					return &pgconn.PgError{Code: code}
				}
				return nil
			})
			if err != nil || attempts != 3 {
				t.Fatalf("retry attempts=%d err=%v", attempts, err)
			}
		})
	}
	permanent := errors.New("permanent")
	attempts := 0
	if err := retryBusy(func() error { attempts++; return permanent }); !errors.Is(err, permanent) || attempts != 1 {
		t.Fatal("permanent failure retried")
	}
}
