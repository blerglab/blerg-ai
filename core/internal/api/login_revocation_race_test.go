package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// waitForFenceWait returns once some backend waits on an account session fence.
func waitForFenceWait(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid() AND wait_event_type = 'Lock'
			  AND starts_with(query, 'SELECT pg_advisory_xact_lock(hashtextextended(''human_sessions:''')
			  AND cardinality(pg_blocking_pids(pid)) > 0`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the login never waited on the session fence")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type loginResult struct {
	status int
	cookie bool
}

func postLogin(srv *httptest.Server, subject, password string) loginResult {
	form := url.Values{"provider_subject": {subject}, "password": {password}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := srv.Client().Do(req)
	if err != nil {
		return loginResult{status: -1}
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == refreshCookieNameForTest && c.Value != "" {
			return loginResult{resp.StatusCode, true}
		}
	}
	return loginResult{resp.StatusCode, false}
}

// A login whose password check ran before a password change or a log-out-everywhere committed
// must not get a session once it reaches the fence after it, and must not clear the account's
// "sub" revocation (review I2).
func TestLoginStartedBeforeRevocationDoesNotSurviveIt(t *testing.T) {
	for _, tc := range []struct {
		name           string
		changePassword bool
	}{{"password change", true}, {"log out everywhere", false}} {
		t.Run(tc.name, func(t *testing.T) {
			deps, pool := newTestDeps(t, []string{"http://board.example.com"}, nil)
			svc := deps.Identity.(*identity.Service)
			ctx := context.Background()
			const oldPassword, newPassword = "old-password-horse", "new-password-battery"
			insertLocalAccount(t, pool, "racer", oldPassword)
			var acct string
			if err := pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE provider_subject = 'racer'`).Scan(&acct); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(api.NewRouter(deps))
			defer srv.Close()
			// The test pool has two connections, both for the login; the racing revocation and the
			// observer use their own.
			side, err := pgxpool.NewWithConfig(ctx, pool.Config())
			if err != nil {
				t.Fatal(err)
			}
			defer side.Close()

			// The revocation (and the new password) is written but not committed: it holds the
			// account's session fence, as the real password change does.
			tx, err := side.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if err := svc.RevokeAccountEverywhereTx(ctx, tx, acct); err != nil {
				t.Fatal(err)
			}
			if tc.changePassword {
				hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.MinCost)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, `UPDATE accounts SET password_hash = $2 WHERE id = $1`, acct, string(hash)); err != nil {
					t.Fatal(err)
				}
			}

			// The login checks the old password against the committed hash, then waits on the fence.
			done := make(chan loginResult, 1)
			go func() { done <- postLogin(srv, "racer", oldPassword) }()
			waitForFenceWait(t, side)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var res loginResult
			select {
			case res = <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("the login never finished")
			}
			if res.status != http.StatusConflict || res.cookie {
				t.Fatalf("login that started before the revocation: status=%d cookie=%v, want 409 and no session", res.status, res.cookie)
			}
			var live, sub int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM human_sessions WHERE account_id = $1 AND revoked_at IS NULL`, acct).Scan(&live); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM revocations WHERE kind = 'sub' AND value = $1`, acct).Scan(&sub); err != nil {
				t.Fatal(err)
			}
			if live != 0 || sub != 1 {
				t.Fatalf("after the refused login: live sessions=%d, sub revocations=%d; want 0 and 1", live, sub)
			}

			// A login that starts after the revocation is a fresh one.
			password := oldPassword
			if tc.changePassword {
				if res := postLogin(srv, "racer", oldPassword); res.status != http.StatusUnauthorized {
					t.Fatalf("old password after the change: %d, want 401", res.status)
				}
				password = newPassword
			}
			if res := postLogin(srv, "racer", password); res.status != http.StatusOK || !res.cookie {
				t.Fatalf("fresh login after the revocation: status=%d cookie=%v", res.status, res.cookie)
			}
		})
	}
}

// A login that cannot take the account's session fence in time is told to retry (503 with
// Retry-After), not given a 500 (review M2).
func TestLoginBusyFenceIsRetryable(t *testing.T) {
	deps, pool := newTestDeps(t, []string{"http://board.example.com"}, nil)
	svc := deps.Identity.(*identity.Service)
	svc.SetSessionLockTimeout(50 * time.Millisecond)
	ctx := context.Background()
	insertLocalAccount(t, pool, "busy-login", "busy-login-password")
	var acct string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE provider_subject = 'busy-login'`).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	side, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer side.Close()
	tx, err := side.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // the fence is only held, never committed
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('human_sessions:' || $1::text, 0))`, acct); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	form := url.Values{"provider_subject": {"busy-login"}, "password": {"busy-login-password"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("busy login: %d Retry-After=%q, want 503 with Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}
