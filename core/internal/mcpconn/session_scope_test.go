package mcpconn_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/contracts/netguard"

	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

// A real encrypted account OAuth connection becomes a scoped fixture only in the
// test database. Production account routes must never perform this conversion.
func TestAccountQueriesExcludeSessionCredentials(t *testing.T) {
	e := newOAuthEnv(t)
	c := e.connect(t, "session-only")
	ctx := context.Background()
	_, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET runner_installation_id='test-runner',runner_session_id=$2,bound_sid=$3,credential_generation=1,lifecycle='suspended',human_deadline=now()+interval '1 day',suspended_at=now(),cleanup_after=now()+interval '168 hours',refresh_fence=0 WHERE id=$1`, c.ID, sessA, liveSid)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("list", func(t *testing.T) {
		rows, err := e.svc.List(ctx, e.acct)
		if err != nil || len(rows) != 0 {
			t.Fatalf("scoped row visible: %d rows, %v", len(rows), err)
		}
	})
	t.Run("get", func(t *testing.T) {
		_, err := e.svc.Get(ctx, e.acct, c.ID)
		if !errors.Is(err, mcpconn.ErrNotFound) {
			t.Fatalf("get: %v", err)
		}
	})
	t.Run("patch", func(t *testing.T) {
		policy := mcpconn.PinPolicyAutoAccept
		_, err := e.svc.Patch(ctx, e.acct, c.ID, mcpconn.PatchInput{PinPolicy: &policy})
		if !errors.Is(err, mcpconn.ErrNotFound) {
			t.Fatalf("patch: %v", err)
		}
	})
	t.Run("events", func(t *testing.T) {
		_, err := e.svc.ListConnectionEvents(ctx, e.acct, c.ID, 50)
		if !errors.Is(err, mcpconn.ErrNotFound) {
			t.Fatalf("events: %v", err)
		}
	})
	t.Run("secret", func(t *testing.T) {
		_, err := e.svc.FetchSecret(ctx, e.acct, c.ID, mcpconn.Proof{SessionID: liveSid})
		if !errors.Is(err, mcpconn.ErrNotFound) {
			t.Fatalf("secret: %v", err)
		}
	})
	t.Run("reconnect", func(t *testing.T) {
		in := e.start("ignored")
		in.ReconnectID = c.ID
		_, err := e.svc.StartOAuth(ctx, in)
		if !errors.Is(err, mcpconn.ErrNotFound) {
			t.Fatalf("reconnect: %v", err)
		}
	})
	t.Run("delete", func(t *testing.T) {
		err := e.svc.Delete(ctx, e.acct, c.ID)
		if !errors.Is(err, mcpconn.ErrNotFound) {
			t.Fatalf("delete: %v", err)
		}
	})
}

func TestSessionStorageEncryptionIsolationAndLifecycle(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	binding := mcpconn.SessionBinding{SessionScope: mcpconn.SessionScope{AccountID: e.acct, RunnerInstallationID: "test-runner", RunnerSessionID: sessA}, HumanSessionID: liveSid, HumanDeadline: time.Now().Add(time.Hour)}
	in := mcpconn.SessionOAuthCredential{Name: "scoped", URL: e.as.MCPURL(), Issuer: e.as.Issuer(), TokenEndpoint: e.as.Issuer() + "/token", RevocationEndpoint: e.as.Issuer() + "/revoke", ClientID: "test-client", AccessToken: "session_access_canary", RefreshToken: "session_refresh_canary", AuthMethod: "client_secret_basic", ClientSecret: "session_client_secret_canary", ExpiresAt: time.Now().Add(time.Hour)}
	c, err := e.svc.StoreSessionOAuth(ctx, binding, in)
	if err != nil {
		t.Fatal(err)
	}
	if c.Lifecycle != "suspended" || c.Generation != 1 || c.CleanupAfter == nil || c.SuspendedAt == nil || c.CleanupAfter.Sub(*c.SuspendedAt) != 7*24*time.Hour {
		t.Fatal("new credential must be suspended with bounded retention")
	}
	var ct, meta []byte
	var kid string
	if err := e.pool.QueryRow(ctx, `SELECT secret_ciphertext,key_id,oauth_meta FROM mcp_connections WHERE id=$1`, c.ID).Scan(&ct, &kid, &meta); err != nil {
		t.Fatal(err)
	}
	inputJSON, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{in.AccessToken, in.RefreshToken, in.ClientSecret} {
		if bytes.Contains(inputJSON, []byte(canary)) {
			t.Fatal("input JSON exposed secret material")
		}
		if bytes.Contains(ct, []byte(canary)) || bytes.Contains(meta, []byte(canary)) {
			t.Fatal("plaintext token in database")
		}
	}
	backend, err := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := backend.Decrypt(ctx, ct, kid)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plain, []byte(in.RefreshToken)) || !bytes.Contains(plain, []byte(in.ClientSecret)) {
		t.Fatal("encrypted bundle did not preserve token material")
	}
	for _, scope := range []mcpconn.SessionScope{{AccountID: newAccount(t, e.pool), RunnerInstallationID: "test-runner", RunnerSessionID: sessA}, {AccountID: e.acct, RunnerInstallationID: "other", RunnerSessionID: sessA}, {AccountID: e.acct, RunnerInstallationID: "test-runner", RunnerSessionID: liveSid}} {
		if _, err := e.svc.GetSessionCredential(ctx, scope, c.ID); !errors.Is(err, mcpconn.ErrNotFound) {
			t.Fatal("foreign scope read")
		}
		if _, err := e.svc.SuspendSessionCredential(ctx, scope, c.ID, c.Generation); !errors.Is(err, mcpconn.ErrNotFound) {
			t.Fatal("foreign scope suspend")
		}
		if err := e.svc.CloseSessionCredential(ctx, scope, c.ID, c.Generation); !errors.Is(err, mcpconn.ErrNotFound) {
			t.Fatal("foreign scope close")
		}
	}
	// There is no activation API in increment 1. Simulate a future human attachment
	// only in the fixture to prove suspension fences an active generation.
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET lifecycle='active',suspended_at=NULL,cleanup_after=NULL,binding_lease_until=now()+interval '1 minute' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	c, err = e.svc.SuspendSessionCredential(ctx, binding.SessionScope, c.ID, c.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if c.Generation != 2 || c.Lifecycle != "suspended" {
		t.Fatal("suspension did not fence active credential")
	}
	again, err := e.svc.SuspendSessionCredential(ctx, binding.SessionScope, c.ID, c.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if again.Generation != c.Generation || !again.CleanupAfter.Equal(*c.CleanupAfter) {
		t.Fatal("repeated suspension extends retention or generation")
	}
	if err := e.svc.CloseSessionCredential(ctx, binding.SessionScope, c.ID, 1); !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatal("stale generation close")
	}
	if err := e.svc.CloseSessionCredential(ctx, binding.SessionScope, c.ID, c.Generation); err != nil {
		t.Fatal(err)
	}
	dead, err := e.svc.GetSessionCredential(ctx, binding.SessionScope, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var erased bool
	if err := e.pool.QueryRow(ctx, `SELECT secret_ciphertext IS NULL AND key_id IS NULL FROM mcp_connections WHERE id=$1`, c.ID).Scan(&erased); err != nil {
		t.Fatal(err)
	}
	if !erased || dead.Lifecycle != "deleted" || dead.Generation != 3 {
		t.Fatal("close did not erase and fence")
	}
	if err := e.svc.CloseSessionCredential(ctx, binding.SessionScope, c.ID, dead.Generation); err != nil {
		t.Fatal("close must be idempotent")
	}
}

func TestSessionScopeConstraintRejectsPartialRows(t *testing.T) {
	e := newOAuthEnv(t)
	c := e.connect(t, "scoped")
	ctx := context.Background()
	// Existing account rows cannot acquire only one piece of a session identity.
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET runner_session_id=$2 WHERE id=$1`, c.ID, sessA); err == nil {
		t.Fatal("partial account-to-session binding accepted")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET runner_installation_id='test-runner',runner_session_id=$2,bound_sid=$3,credential_generation=1,lifecycle='suspended',human_deadline=now()+interval '1 day',suspended_at=now(),cleanup_after=now()+interval '168 hours',refresh_fence=0 WHERE id=$1`, c.ID, sessA, liveSid); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"runner_installation_id", "runner_session_id", "bound_sid", "credential_generation", "lifecycle", "human_deadline", "refresh_fence", "suspended_at", "cleanup_after", "secret_ciphertext", "key_id"} {
		t.Run(column, func(t *testing.T) {
			_, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET `+column+`=NULL WHERE id=$1`, c.ID)
			var pe *pgconn.PgError
			if !errors.As(err, &pe) || pe.Code != "23514" {
				t.Fatalf("missing %s should violate scope check: %v", column, err)
			}
		})
	}
}

func TestSessionNamespaceAndSharedQuota(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	b := mcpconn.SessionBinding{SessionScope: mcpconn.SessionScope{AccountID: e.acct, RunnerInstallationID: "runner", RunnerSessionID: sessA}, HumanSessionID: liveSid, HumanDeadline: time.Now().Add(time.Hour)}
	in := mcpconn.SessionOAuthCredential{Name: "same", URL: e.as.MCPURL(), Issuer: e.as.Issuer(), TokenEndpoint: e.as.Issuer() + "/token", ClientID: "test-client", AccessToken: "test-access", ExpiresAt: time.Now().Add(time.Hour)}
	c, err := e.svc.StoreSessionOAuth(ctx, b, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StoreSessionOAuth(ctx, b, in); !errors.Is(err, mcpconn.ErrNameTaken) {
		t.Fatal("same session accepted duplicate name")
	}
	// A session name is not account inventory or an account namespace collision.
	if _, err := e.svc.StartOAuth(ctx, e.start("same")); err != nil {
		t.Fatal("OAuth preflight leaked scoped namespace", err)
	}
	if _, err := e.svc.Create(ctx, e.acct, static("same", "test-static")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartOAuth(ctx, e.start("scoped-only")); err != nil {
		t.Fatal(err)
	}
	for i := 2; i < mcpconn.MaxPerAccount; i++ {
		if _, err := e.svc.Create(ctx, e.acct, static(fmt.Sprintf("account-%d", i), "test-static")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.Create(ctx, e.acct, static("over-limit", "test-static")); !errors.Is(err, mcpconn.ErrLimit) {
		t.Fatal("account create ignored session quota")
	}
	if _, err := e.svc.StartOAuth(ctx, e.start("over-limit")); !errors.Is(err, mcpconn.ErrLimit) {
		t.Fatal("OAuth preflight ignored session quota")
	}
	in.Name = "over-limit"
	if _, err := e.svc.StoreSessionOAuth(ctx, b, in); !errors.Is(err, mcpconn.ErrLimit) {
		t.Fatal("session create ignored shared quota")
	}
	if err := e.svc.CloseSessionCredential(ctx, b.SessionScope, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(ctx, e.acct, static("after-erasure", "test-static")); err != nil {
		t.Fatal("deleted tombstone consumed quota", err)
	}
}

func TestSessionCloseFailureAndRecovery(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	b := mcpconn.SessionBinding{SessionScope: mcpconn.SessionScope{AccountID: e.acct, RunnerInstallationID: "runner", RunnerSessionID: sessA}, HumanSessionID: liveSid, HumanDeadline: time.Now().Add(time.Hour)}
	type revocationObservation struct {
		closing bool
		hint    string
	}
	observations := make(chan revocationObservation, 2)
	revoker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var closing bool
		err := e.pool.QueryRow(r.Context(), `SELECT lifecycle='closing' AND secret_ciphertext IS NOT NULL FROM mcp_connections WHERE name='close'`).Scan(&closing)
		parseErr := r.ParseForm()
		observations <- revocationObservation{closing: err == nil && parseErr == nil && closing, hint: r.PostForm.Get("token_type_hint")}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(revoker.Close)
	in := mcpconn.SessionOAuthCredential{Name: "close", URL: e.as.MCPURL(), Issuer: e.as.Issuer(), TokenEndpoint: e.as.Issuer() + "/token", RevocationEndpoint: revoker.URL, ClientID: "test-client", AccessToken: "close-access", RefreshToken: "close-refresh", ExpiresAt: time.Now().Add(time.Hour)}
	c, err := e.svc.StoreSessionOAuth(ctx, b, in)
	if err != nil {
		t.Fatal(err)
	}
	// A crashed closer left a durable closing record. Recovery uses the current
	// generation; a failing provider must not keep local tokens alive.
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET lifecycle='closing',credential_generation=2 WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CloseSessionCredential(ctx, b.SessionScope, c.ID, 1); err != nil {
		t.Fatal("losing close of an already closing row should be success", err)
	}
	if _, err := e.svc.SuspendSessionCredential(ctx, b.SessionScope, c.ID, 2); !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatal("closing row resurrected")
	}
	if err := e.svc.CloseSessionCredential(ctx, b.SessionScope, c.ID, 2); err != nil {
		t.Fatal(err)
	}
	dead, err := e.svc.GetSessionCredential(ctx, b.SessionScope, c.ID)
	if err != nil || dead.Lifecycle != "deleted" {
		t.Fatal("closing recovery failed")
	}
	first, second := <-observations, <-observations
	if !first.closing || !second.closing || first.hint != "refresh_token" || second.hint != "access_token" {
		t.Fatal("revocation did not precede local erasure in a durable closing state")
	}

}

func TestSessionStoreRejectsInvalidBinding(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	valid := mcpconn.SessionBinding{SessionScope: mcpconn.SessionScope{AccountID: e.acct, RunnerInstallationID: "runner", RunnerSessionID: sessA}, HumanSessionID: liveSid, HumanDeadline: time.Now().Add(time.Hour)}
	in := mcpconn.SessionOAuthCredential{Name: "scoped", URL: e.as.MCPURL(), Issuer: e.as.Issuer(), TokenEndpoint: e.as.Issuer() + "/token", ClientID: "client", AccessToken: "test-access", ExpiresAt: time.Now().Add(time.Hour)}
	for _, change := range []func(*mcpconn.SessionBinding){func(b *mcpconn.SessionBinding) { b.AccountID = "" }, func(b *mcpconn.SessionBinding) { b.RunnerInstallationID = "" }, func(b *mcpconn.SessionBinding) { b.RunnerSessionID = "" }, func(b *mcpconn.SessionBinding) { b.HumanSessionID = "" }, func(b *mcpconn.SessionBinding) { b.HumanDeadline = time.Time{} }} {
		b := valid
		change(&b)
		_, err := e.svc.StoreSessionOAuth(ctx, b, in)
		var ve *mcpconn.ValidationError
		if !errors.As(err, &ve) {
			t.Fatal("incomplete binding accepted")
		}
	}
	for _, change := range []func(*mcpconn.SessionOAuthCredential){
		func(c *mcpconn.SessionOAuthCredential) { c.AccessToken = "" },
		func(c *mcpconn.SessionOAuthCredential) { c.RefreshToken = "bad\nrefresh" },
		func(c *mcpconn.SessionOAuthCredential) { c.ClientSecret = "public-client-secret" },
		func(c *mcpconn.SessionOAuthCredential) { c.AuthMethod = "unknown" },
		func(c *mcpconn.SessionOAuthCredential) { c.AuthMethod = "client_secret_basic" },
		func(c *mcpconn.SessionOAuthCredential) {
			c.AuthMethod = "client_secret_post"
			c.ClientSecret = "expired-client-secret"
			c.ClientSecretExpiresAt = 1
		},
	} {
		c := in
		change(&c)
		_, err := e.svc.StoreSessionOAuth(ctx, valid, c)
		var ve *mcpconn.ValidationError
		if !errors.As(err, &ve) {
			t.Fatal("invalid OAuth credential accepted")
		}
	}
	rows, err := e.svc.List(ctx, e.acct)
	if err != nil || len(rows) != 0 {
		t.Fatal("invalid bindings stored credentials")
	}

}

func TestSessionFailureLogsClassifiedAndBounded(t *testing.T) {
	var logs bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { failurelog.Default = old })
	svc := mcpconn.NewService(nil, nil, netguard.Policy{})
	for range 3 {
		_, err := svc.GetSessionCredential(context.Background(), mcpconn.SessionScope{}, "private-input-canary")
		if !errors.Is(err, mcpconn.ErrNotFound) {
			t.Fatal("malformed scope not refused")
		}
	}
	if bytes.Count(logs.Bytes(), []byte(`"cause":"not_found"`)) != 1 || !bytes.Contains(logs.Bytes(), []byte(`"suppressed":0`)) {
		t.Fatal("session failures must use classified, bounded shared diagnostics")
	}
	if bytes.Contains(logs.Bytes(), []byte("private-input-canary")) {
		t.Fatal("session diagnostics echoed private input")
	}
}

func TestSessionClosingFailureHasRetryDeadline(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	b := mcpconn.SessionBinding{SessionScope: mcpconn.SessionScope{AccountID: e.acct, RunnerInstallationID: "runner", RunnerSessionID: sessA}, HumanSessionID: liveSid, HumanDeadline: time.Now().Add(time.Hour)}
	c, err := e.svc.StoreSessionOAuth(ctx, b, mcpconn.SessionOAuthCredential{Name: "retry-close", URL: e.as.MCPURL(), Issuer: e.as.Issuer(), TokenEndpoint: e.as.Issuer() + "/token", RevocationEndpoint: e.as.Issuer() + "/revoke", ClientID: "client", AccessToken: "synthetic-access", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.pool.Exec(ctx, `UPDATE mcp_connections SET lifecycle='active',suspended_at=NULL,cleanup_after=NULL,binding_lease_until=now()+interval '1 minute' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.pool.Exec(ctx, `CREATE FUNCTION fail_session_erase() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.lifecycle='deleted' THEN RAISE EXCEPTION 'synthetic erase failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_erase BEFORE UPDATE ON mcp_connections FOR EACH ROW EXECUTE FUNCTION fail_session_erase()`); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.CloseSessionCredential(ctx, b.SessionScope, c.ID, c.Generation); err == nil {
		t.Fatal("fixture did not fail erase")
	}
	e.as.Lock()
	revoked := len(e.as.Revoked)
	e.as.Unlock()
	if revoked == 0 {
		t.Fatal("erase failure fixture did not attempt provider revocation first")
	}
	closing, err := e.svc.GetSessionCredential(ctx, b.SessionScope, c.ID)
	if err != nil || closing.Lifecycle != "closing" || closing.CleanupAfter == nil || closing.CleanupAfter.After(time.Now()) {
		t.Fatal("closing failure has no immediately due cleanup deadline")
	}
	if _, err = e.pool.Exec(ctx, `DROP TRIGGER fail_erase ON mcp_connections`); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.CloseSessionCredential(ctx, b.SessionScope, c.ID, closing.Generation); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.CloseSessionCredential(ctx, b.SessionScope, c.ID, c.Generation); err != nil {
		t.Fatal("losing concurrent close must succeed for terminal scoped row", err)
	}
}

func TestSessionRetentionIsFixedAcrossDSTAndInactiveLeaseRefused(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	c := e.connect(t, "dst")
	// Explicit New York time zone crosses the 2026 spring-forward boundary.
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET LOCAL TIME ZONE 'America/New_York'`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `UPDATE mcp_connections SET runner_installation_id='runner',runner_session_id=$2,bound_sid=$3,credential_generation=1,lifecycle='suspended',human_deadline=now()+interval '1 day',suspended_at='2026-03-07 12:00:00-05'::timestamptz,cleanup_after='2026-03-07 12:00:00-05'::timestamptz+interval '168 hours',refresh_fence=0 WHERE id=$1`, c.ID, sessA, liveSid)
	if err != nil {
		t.Fatal("fixed 168-hour retention rejected across DST", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, lifecycle := range []string{"suspended", "closing", "deleted"} {
		if lifecycle == "closing" {
			if _, err = e.pool.Exec(ctx, `UPDATE mcp_connections SET lifecycle='closing',cleanup_after=now() WHERE id=$1`, c.ID); err != nil {
				t.Fatal(err)
			}
		}
		if lifecycle == "deleted" {
			if _, err = e.pool.Exec(ctx, `UPDATE mcp_connections SET lifecycle='deleted',cleanup_after=NULL,secret_ciphertext=NULL,key_id=NULL WHERE id=$1`, c.ID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = e.pool.Exec(ctx, `UPDATE mcp_connections SET binding_lease_until=now()+interval '1 minute' WHERE id=$1`, c.ID); err == nil {
			t.Fatalf("%s binding retained an active lease", lifecycle)
		}
	}

}

func TestSessionDeletedTombstonesBounded(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	b := mcpconn.SessionBinding{SessionScope: mcpconn.SessionScope{AccountID: e.acct, RunnerInstallationID: "runner", RunnerSessionID: sessA}, HumanSessionID: liveSid, HumanDeadline: time.Now().Add(time.Hour)}
	for range 23 {
		c, err := e.svc.StoreSessionOAuth(ctx, b, mcpconn.SessionOAuthCredential{Name: "same", URL: e.as.MCPURL(), Issuer: e.as.Issuer(), TokenEndpoint: e.as.Issuer() + "/token", ClientID: "client", AccessToken: "synthetic-access", ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if err = e.svc.CloseSessionCredential(ctx, b.SessionScope, c.ID, c.Generation); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_connections WHERE account_id=$1 AND runner_session_id=$2 AND lifecycle='deleted'`, e.acct, sessA).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 20 {
		t.Fatalf("deleted tombstones unbounded: %d", n)
	}
}
