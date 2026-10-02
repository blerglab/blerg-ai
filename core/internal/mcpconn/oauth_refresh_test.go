package mcpconn_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn/mcptest"
)

// refreshOutcome classes of the provider's answer to a refresh (MAJOR 13): only a 400/401 with
// invalid_grant, invalid_client or unauthorized_client means the grant or client is refused.
func TestOAuthRefreshResponseClasses(t *testing.T) {
	type tc struct {
		name     string
		status   int
		code     string
		raw      string // a 200 body instead of a status
		rejected bool
	}
	cases := []tc{
		{"400 invalid_grant", 400, "invalid_grant", "", true},
		{"401 invalid_client", 401, "invalid_client", "", true},
		{"400 unauthorized_client", 400, "unauthorized_client", "", true},
		{"400 invalid_request", 400, "invalid_request", "", false},
		{"400 invalid_scope", 400, "invalid_scope", "", false},
		{"401 without a code", 401, "zz-not-a-code", "", false},
		{"403", 403, "access_denied", "", false},
		{"404", 404, "not_found", "", false},
		{"408", 408, "request_timeout", "", false},
		{"429", 429, "slow_down", "", false},
		{"500", 500, "", "", false},
		{"502", 502, "", "", false},
		{"503", 503, "", "", false},
		{"200 not json", 0, "", "<html>maintenance</html>", false},
		{"200 without an access token", 0, "", `{"token_type":"Bearer","expires_in":3600}`, false},
		{"200 non-bearer", 0, "", `{"access_token":"x","token_type":"mac","expires_in":3600}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newOAuthEnv(t)
			ctx := context.Background()
			e.as.SetAccessTTL(30) // valid for 30 s: inside the refresh window, still usable
			conn := e.connect(t, "notes")
			e.as.Lock()
			e.as.RefreshStatus, e.as.RefreshError, e.as.RefreshRaw = c.status, c.code, c.raw
			e.as.Unlock()

			res, err := e.fetch(t, conn.ID)
			got, _ := e.svc.Get(ctx, e.acct, conn.ID)
			if c.rejected {
				if !errors.Is(err, mcpconn.ErrNotFound) || got.Status != "needs_auth" {
					t.Fatalf("rejected: err=%v status=%q, want ErrNotFound and needs_auth", err, got.Status)
				}
				return
			}
			// Transient: the still-valid token comes back and the connection is untouched.
			if err != nil || !strings.HasPrefix(res.Value, "Bearer at-") {
				t.Fatalf("transient: fetch = %+v %v, want the still-valid access token", res, err)
			}
			if got.Status != "ok" {
				t.Fatalf("transient: status = %q, want ok", got.Status)
			}
			// The refresh token was kept: once the provider recovers, the ORIGINAL one is spent
			// and works (a lost token would be a reuse attempt answered with invalid_grant).
			e.as.SetAccessTTL(3600)
			e.as.Lock()
			e.as.RefreshStatus, e.as.RefreshError, e.as.RefreshRaw = 0, "", ""
			e.as.Unlock()
			res2, err := e.fetch(t, conn.ID)
			if err != nil || res2.Value == res.Value {
				t.Fatalf("after recovery: fetch = %+v %v, want a fresh token", res2, err)
			}
			if spent, reuse, _, _ := e.as.Stats(); spent != 1 || reuse != 0 {
				t.Fatalf("spent=%d reuse=%d, want 1 and 0", spent, reuse)
			}
		})
	}
}

// With the access token already expired, a transient failure is ErrUnavailable (503 +
// Retry-After), never needs_auth.
func TestOAuthExpiredTokenTransientClassesAreUnavailable(t *testing.T) {
	for _, st := range []int{429, 408, 403} {
		t.Run(fmt.Sprint(st), func(t *testing.T) {
			e := newOAuthEnv(t)
			e.as.SetAccessTTL(1)
			conn := e.connect(t, "notes")
			time.Sleep(1200 * time.Millisecond)
			e.as.Lock()
			e.as.RefreshStatus, e.as.RefreshError = st, "access_denied"
			e.as.Unlock()
			if _, err := e.fetch(t, conn.ID); !errors.Is(err, mcpconn.ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if got, _ := e.svc.Get(context.Background(), e.acct, conn.ID); got.Status != "ok" {
				t.Fatalf("status = %q, want ok", got.Status)
			}
		})
	}
}

// smallPoolSvc is a Service on its OWN pool of exactly one connection, in the same schema, so a
// held connection is observable as a stall of every other query on that pool.
func smallPoolSvc(t *testing.T) (*mcpconn.Service, *pgxpool.Pool) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = testSchema
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	backend, err := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return mcpconn.NewService(db.NewPgStore(pool), backend, loopbackPolicy()), pool
}

// waitFor polls cond up to two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func refreshSeen(e *oauthEnv) bool {
	e.as.Lock()
	defer e.as.Unlock()
	for _, f := range e.as.TokenForms {
		if f.Get("grant_type") == "refresh_token" {
			return true
		}
	}
	return false
}

// MAJOR 14: a slow provider call must not hold a pooled connection. With a pool of ONE, a
// login-like query made while the refresh is in flight still succeeds promptly.
func TestOAuthSlowRefreshDoesNotHoldADatabaseConnection(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.SetAccessTTL(30)
	conn := e.connect(t, "notes")
	e.as.SetAccessTTL(3600)
	e.as.Lock()
	e.as.RefreshDelay = 800 * time.Millisecond
	e.as.Unlock()
	small, pool := smallPoolSvc(t)

	done := make(chan error, 1)
	go func() {
		_, err := small.FetchSecret(context.Background(), e.acct, conn.ID, mcpconn.Proof{SessionID: sessA})
		done <- err
	}()
	waitFor(t, "the refresh to reach the provider", func() bool { return refreshSeen(e) })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var one int
	if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("a query on the one-connection pool stalled behind the provider call: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("refresh: %v", err)
	}
}

func TestOAuthSlowRevocationOnDeleteDoesNotHoldADatabaseConnection(t *testing.T) {
	e := newOAuthEnv(t)
	conn := e.connect(t, "notes")
	e.as.Lock()
	e.as.RevokeDelay = 800 * time.Millisecond
	e.as.Unlock()
	small, pool := smallPoolSvc(t)

	done := make(chan error, 1)
	go func() { done <- small.Delete(context.Background(), e.acct, conn.ID) }()
	waitFor(t, "the revocation to reach the provider", func() bool {
		e.as.Lock()
		defer e.as.Unlock()
		return e.as.Hits["/as/revoke"] > 0
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var one int
	if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("a query on the one-connection pool stalled behind the revocation call: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// The rotated refresh token must never be lost: the caller hanging up mid-refresh does not stop
// the new token bundle from being stored.
func TestOAuthRotatedTokenIsStoredEvenIfTheCallerHangsUp(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.SetAccessTTL(30)
	conn := e.connect(t, "notes")
	e.as.SetAccessTTL(3600)
	e.as.Lock()
	e.as.RefreshDelay = 400 * time.Millisecond
	e.as.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		waitFor(t, "the refresh to reach the provider", func() bool { return refreshSeen(e) })
		cancel()
	}()
	if _, err := e.svc.FetchSecret(ctx, e.acct, conn.ID, mcpconn.Proof{SessionID: sessA}); err == nil {
		t.Fatal("the caller that hung up still got an answer")
	}
	time.Sleep(700 * time.Millisecond) // the detached refresh finishes and stores its result
	e.as.Lock()
	e.as.RefreshDelay = 0
	e.as.Unlock()
	res, err := e.fetch(t, conn.ID)
	if err != nil || !strings.HasPrefix(res.Value, "Bearer at-") {
		t.Fatalf("fetch after the hang-up = %+v %v", res, err)
	}
	if spent, reuse, _, _ := e.as.Stats(); spent != 1 || reuse != 0 {
		t.Fatalf("spent=%d reuse=%d, want the rotation stored exactly once (1, 0)", spent, reuse)
	}
}

// A connection deleted while its refresh is in flight: the tokens the provider just issued are
// taken back, and the caller gets the uniform not-found.
func TestOAuthDeleteDuringRefreshRevokesTheNewTokens(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.SetAccessTTL(30)
	conn := e.connect(t, "notes")
	e.as.SetAccessTTL(3600)
	e.as.Lock()
	e.as.RefreshDelay = 400 * time.Millisecond
	e.as.Unlock()

	done := make(chan error, 1)
	go func() { _, err := e.fetch(t, conn.ID); done <- err }()
	waitFor(t, "the refresh to reach the provider", func() bool { return refreshSeen(e) })
	if err := e.svc.Delete(context.Background(), e.acct, conn.ID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatalf("fetch = %v, want ErrNotFound", err)
	}
	// Delete revoked the old pair (2 calls); the refresh that lost the race revoked the new pair.
	if _, _, revokes, _ := e.as.Stats(); revokes != 4 {
		t.Fatalf("%d revocations reached the provider, want 4 (old pair + the new pair)", revokes)
	}
}

// failingEncrypt wraps a backend and fails Encrypt on demand.
type failingEncrypt struct {
	keybackend.Backend
	fail atomic.Bool
}

func (f *failingEncrypt) Encrypt(ctx context.Context, p []byte) ([]byte, string, error) {
	if f.fail.Load() {
		return nil, "", errors.New("key backend down")
	}
	return f.Backend.Encrypt(ctx, p)
}

// MINOR 35 (i): failing to store a rotated token is logged, without any secret, and the freshly
// issued access token is still handed out.
func TestOAuthLogsWhenTheRotatedTokenCannotBeStored(t *testing.T) {
	pool := testPool(t)
	local, err := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	be := &failingEncrypt{Backend: local}
	as := mcptest.New()
	t.Cleanup(as.Close)
	svc := mcpconn.NewService(db.NewPgStore(pool), be, loopbackPolicy())
	e := &oauthEnv{svc: svc, pool: pool, as: as, acct: newAccount(t, pool)}
	as.SetAccessTTL(30)
	conn := e.connect(t, "notes")
	as.SetAccessTTL(3600)

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	be.fail.Store(true)
	res, err := e.fetch(t, conn.ID)
	if err != nil || !strings.HasPrefix(res.Value, "Bearer at-") {
		t.Fatalf("fetch = %+v %v, want the new access token", res, err)
	}
	out := logs.String()
	if !strings.Contains(out, "rotated") || !strings.Contains(out, conn.ID) {
		t.Fatalf("no log line about the lost rotation: %q", out)
	}
	for _, secret := range []string{"at-", "rt-", "client-secret"} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains %q: %s", secret, out)
		}
	}
}

// MINOR 35 (c): expires_in may be a numeric string.
func TestOAuthExpiresInAsString(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.ExpiresInString = true
	e.as.SetAccessTTL(120)
	conn := e.connect(t, "notes")
	res, err := e.fetch(t, conn.ID)
	if err != nil || res.ExpiresAt == nil {
		t.Fatalf("fetch = %+v %v", res, err)
	}
	if d := time.Until(*res.ExpiresAt); d < 100*time.Second || d > 125*time.Second {
		t.Fatalf("expires in %v, want about 120 s (the string was ignored and the default applied)", d)
	}
}
