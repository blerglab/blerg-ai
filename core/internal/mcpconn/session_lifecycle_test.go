package mcpconn

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSessionBindingRenewalNeverResurrects(t *testing.T) {
	for _, reason := range []string{"expired lease", "expired login", "revoked login", "disabled account", "stale generation", "foreign session"} {
		t.Run(reason, func(t *testing.T) {
			e, in := sessionDispatchReady(t)
			ctx := context.Background()
			var err error
			switch reason {
			case "expired lease":
				_, err = e.pool.Exec(ctx, `UPDATE mcp_connections SET binding_lease_until=now()-interval '1 second' WHERE id=$1`, in.ConnectionID)
			case "expired login":
				_, err = e.pool.Exec(ctx, `UPDATE human_sessions SET expires_at=now()-interval '1 second' WHERE chain_id=$1`, in.HumanSessionID)
			case "revoked login":
				_, err = e.pool.Exec(ctx, `UPDATE human_sessions SET revoked_at=now() WHERE chain_id=$1`, in.HumanSessionID)
			case "disabled account":
				_, err = e.pool.Exec(ctx, `UPDATE accounts SET disabled_at=now() WHERE id=$1`, in.AccountID)
			case "stale generation":
				in.CredentialGeneration++
			case "foreign session":
				in.RunnerSessionID = "00000000-0000-4000-8000-000000000999"
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = e.svc.RenewSessionBinding(ctx, in); !errors.Is(err, ErrNotFound) {
				t.Fatal("dead authority renewed", err)
			}
		})
	}
}

func TestIdleExpiredLoginStartsSuspensionClock(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	e.svc.oauthNow = func() time.Time { return now }
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET human_deadline=$2 WHERE id=$1`, in.ConnectionID, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RenewSessionBinding(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Error("expired human deadline renewed", err)
	}
	if err := e.svc.SweepSessionCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := e.svc.GetSessionCredential(ctx, in.SessionScope, in.ConnectionID)
	if err != nil || c.Lifecycle != "suspended" || c.Generation != in.CredentialGeneration+1 || c.SuspendedAt == nil || !c.SuspendedAt.Equal(now) || c.CleanupAfter == nil || !c.CleanupAfter.Equal(now.Add(168*time.Hour)) {
		t.Fatal("idle expiry did not start bounded suspension", c, err)
	}
}

func TestBindingLeaseExpirySuspendsNeverErases(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	e.svc.oauthNow = func() time.Time { return now }
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET binding_lease_until=$2 WHERE id=$1`, in.ConnectionID, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.svc.SweepSessionCredentials(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var lifecycle string
	var retained bool
	var cutoff time.Time
	if err := e.pool.QueryRow(ctx, `SELECT lifecycle,secret_ciphertext IS NOT NULL,cleanup_after FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&lifecycle, &retained, &cutoff); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "suspended" || !retained || !cutoff.Equal(now.Add(168*time.Hour)) {
		t.Fatal("Runner outage erased or extended retention")
	}
}

func TestSuspendedCredentialRetentionCutoff(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	e.svc.oauthNow = func() time.Time { return now }
	c, err := e.svc.SuspendSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration)
	if err != nil {
		t.Fatal(err)
	}
	now = c.CleanupAfter.Add(-time.Microsecond)
	if err = e.svc.SweepSessionCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	c, err = e.svc.GetSessionCredential(ctx, in.SessionScope, in.ConnectionID)
	if err != nil || c.Lifecycle != "suspended" {
		t.Fatal("erased before retention", err)
	}
	now = now.Add(time.Microsecond)
	if err = e.svc.SweepSessionCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	c, err = e.svc.GetSessionCredential(ctx, in.SessionScope, in.ConnectionID)
	if err != nil || c.Lifecycle != "deleted" {
		t.Fatal("did not erase at cutoff", c, err)
	}
	_, _, revokes, _ := e.provider.Stats()
	if revokes == 0 {
		t.Fatal("cleanup did not attempt provider revocation")
	}
}

func TestSessionRenewalChecksLiveHumanAndKeepsAbsoluteDeadline(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	e.svc.oauthNow = func() time.Time { return now }
	deadline := now.Add(30 * time.Second)
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET human_deadline=$2,binding_lease_until=$3 WHERE id=$1`, in.ConnectionID, deadline, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RenewSessionBinding(ctx, in); err != nil {
		t.Fatal(err)
	}
	var lease, absolute time.Time
	if err := e.pool.QueryRow(ctx, `SELECT binding_lease_until,human_deadline FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&lease, &absolute); err != nil {
		t.Fatal(err)
	}
	if !lease.Equal(deadline) || !absolute.Equal(deadline) {
		t.Fatal("renewal extended login deadline")
	}
	if _, err := e.svc.SuspendSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RenewSessionBinding(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatal("renewal reattached suspended credential")
	}
}

func TestSessionSweepRetriesClosingAfterEraseFailure(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `CREATE FUNCTION fail_sweep_erase() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.lifecycle='deleted' THEN RAISE EXCEPTION 'synthetic erase failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_sweep_erase BEFORE UPDATE ON mcp_connections FOR EACH ROW EXECUTE FUNCTION fail_sweep_erase()`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CloseSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration); err == nil {
		t.Fatal("fixture did not fail erasure")
	}
	c, err := e.svc.GetSessionCredential(ctx, in.SessionScope, in.ConnectionID)
	if err != nil || c.Lifecycle != "closing" || c.CleanupAfter == nil {
		t.Fatal("failed erasure lost retry deadline", c, err)
	}
	if _, err = e.pool.Exec(ctx, `DROP TRIGGER fail_sweep_erase ON mcp_connections`); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.SweepSessionCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	c, err = e.svc.GetSessionCredential(ctx, in.SessionScope, in.ConnectionID)
	if err != nil || c.Lifecycle != "deleted" {
		t.Fatal("closing was not retried", c, err)
	}
}

func TestRetentionRevokesBeforeEraseEvenWhenProviderFails(t *testing.T) {
	for _, mode := range []string{"rejected", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			e, in := sessionDispatchReady(t)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Microsecond)
			e.svc.oauthNow = func() time.Time { return now }
			c, err := e.svc.SuspendSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration)
			if err != nil {
				t.Fatal(err)
			}
			now = *c.CleanupAfter
			observed := 0
			original := e.svc.httpc.Transport
			e.svc.httpc.Transport = reviewAuthorityTransport(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/revoke") {
					var lifecycle string
					var sealed bool
					if err := e.pool.QueryRow(ctx, `SELECT lifecycle,secret_ciphertext IS NOT NULL FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&lifecycle, &sealed); err != nil {
						t.Error(err)
					}
					if lifecycle != "closing" || !sealed {
						t.Error("provider revocation followed erasure")
					}
					observed++
					if mode == "timeout" {
						return nil, context.DeadlineExceeded
					}
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("synthetic-provider-body")), Header: http.Header{}}, nil
				}
				return original.RoundTrip(r)
			})
			if err = e.svc.SweepSessionCredentials(ctx); err != nil {
				t.Fatal(err)
			}
			if observed != 2 {
				t.Fatal("revocation was skipped", observed)
			}
			c, err = e.svc.GetSessionCredential(ctx, in.SessionScope, in.ConnectionID)
			if err != nil || c.Lifecycle != "deleted" {
				t.Fatal("provider failure prevented erasure", c, err)
			}
		})
	}
}

func TestIdleAbandonedRefreshRequiresNewProviderSignIn(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	e.svc.oauthNow = func() time.Time { return now }
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET session_refresh_lease_until=$2 WHERE id=$1`, in.ConnectionID, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RenewSessionBinding(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatal("idle uncertain refresh retained live authority", err)
	}
	if err := e.svc.SweepSessionCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := e.svc.GetSessionCredential(ctx, in.SessionScope, in.ConnectionID)
	if err != nil || c.Lifecycle != "suspended" || c.Generation != in.CredentialGeneration+1 {
		t.Fatal("abandoned refresh did not suspend", c, err)
	}
	var status string
	var retained bool
	if err = e.pool.QueryRow(ctx, `SELECT status,secret_ciphertext IS NOT NULL FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&status, &retained); err != nil || status != "needs_auth" || !retained {
		t.Fatal("uncertain refresh was erased or made reusable", status, retained, err)
	}
}

func TestClosingErasesAfterCallerCancellation(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := e.svc.httpc.Transport
	e.svc.httpc.Transport = reviewAuthorityTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/revoke") {
			cancel()
		}
		return original.RoundTrip(r)
	})
	if err := e.svc.CloseSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration); err != nil {
		t.Fatal("committed closing lost erasure after caller cancellation", err)
	}
	c, err := e.svc.GetSessionCredential(context.Background(), in.SessionScope, in.ConnectionID)
	if err != nil || c.Lifecycle != "deleted" {
		t.Fatal("committed closing retained sealed credential after caller left", c, err)
	}
}

func TestLostSuspendAcknowledgmentCanClosePromptly(t *testing.T) {
	for _, mode := range []string{"lost ack", "sweep first"} {
		t.Run(mode, func(t *testing.T) {
			e, in := sessionDispatchReady(t)
			ctx := context.Background()
			if mode == "lost ack" {
				if _, err := e.svc.SuspendSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET binding_lease_until=now()-interval '1 second' WHERE id=$1`, in.ConnectionID); err != nil {
					t.Fatal(err)
				}
				if err := e.svc.SweepSessionCredentials(ctx); err != nil {
					t.Fatal(err)
				}
			}
			c, err := e.svc.SuspendSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration)
			if err != nil || c.Generation != in.CredentialGeneration+1 {
				t.Fatal("lost acknowledgment cannot recover exact suspended fence", err)
			}
			again, err := e.svc.SuspendSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration)
			if err != nil || again.Generation != c.Generation || !again.CleanupAfter.Equal(*c.CleanupAfter) {
				t.Fatal("duplicate delivery changed fence or retention", err)
			}
			if err = e.svc.CloseSessionCredential(ctx, in.SessionScope, in.ConnectionID, c.Generation); err != nil {
				t.Fatal(err)
			}
			var erased bool
			if err = e.pool.QueryRow(ctx, `SELECT lifecycle='deleted' AND secret_ciphertext IS NULL FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&erased); err != nil || !erased {
				t.Fatal("terminal close waited for retention", err)
			}
		})
	}
}

func TestIdempotentSuspendRefusesNewerAttachment(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET credential_generation=credential_generation+1 WHERE id=$1`, in.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SuspendSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration); !errors.Is(err, ErrNotFound) {
		t.Fatal("old scope suspended a newer active attachment", err)
	}
	if err := e.svc.CloseSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration); !errors.Is(err, ErrNotFound) {
		t.Fatal("old scope closed a newer active attachment", err)
	}
}

func TestRenewalUsesDatabaseClock(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	// A slow Core clock must not resurrect a lease that admission already refuses.
	e.svc.oauthNow = func() time.Time { return time.Now().Add(-time.Hour) }
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET binding_lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, in.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RenewSessionBinding(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatal("application clock renewed a database-expired lease", err)
	}
}

func cloneDueSessionCredential(t *testing.T, e *sessionFlowEnv, id string) string {
	t.Helper()
	var cloned string
	err := e.pool.QueryRow(context.Background(), `INSERT INTO mcp_connections SELECT
 (jsonb_populate_record(NULL::mcp_connections,to_jsonb(c)||jsonb_build_object('id',gen_random_uuid(),'runner_session_id',gen_random_uuid(),
 'lifecycle','suspended','binding_lease_until',NULL,'suspended_at',now()-interval '8 days','cleanup_after',now()-interval '1 day'))).*
 FROM mcp_connections c WHERE id=$1 RETURNING id::text`, id).Scan(&cloned)
	if err != nil {
		t.Fatal(err)
	}
	return cloned
}
func TestSessionCleanupBatchIsBounded(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	for range 3 {
		cloneDueSessionCredential(t, e, in.ConnectionID)
	}
	if err := e.svc.SweepSessionCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	var closed int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_connections WHERE lifecycle='deleted'`).Scan(&closed); err != nil || closed != 2 {
		t.Fatal("cleanup pass exceeded two-row close budget", closed, err)
	}
}

func TestSessionSuspensionDoesNotWaitForProviderCleanup(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	cloneDueSessionCredential(t, e, in.ConnectionID)
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET binding_lease_until=now()-interval '1 second' WHERE id=$1`, in.ConnectionID); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{}, 2)
	defer close(release)
	original := e.svc.httpc.Transport
	e.svc.httpc.Transport = reviewAuthorityTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/revoke") {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return original.RoundTrip(r)
	})
	done := make(chan error, 1)
	go func() { done <- e.svc.closeExpiredSessionCredentials(ctx) }()
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-bounded.Done():
		t.Fatal("cleanup did not reach controlled provider")
	}
	if err := e.svc.suspendExpiredSessionBindings(bounded); err != nil {
		t.Fatal(err)
	}
	c, err := e.svc.GetSessionCredential(ctx, in.SessionScope, in.ConnectionID)
	if err != nil || c.Lifecycle != "suspended" {
		t.Fatal("slow provider blocked suspension", err)
	}
	// Release both token revocations and await completion before fixture cleanup.
	release <- struct{}{}
	release <- struct{}{}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-bounded.Done():
		t.Fatal("cleanup did not finish")
	}
}

func TestConcurrentSuspendAcknowledgmentsKeepExactFence(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	type result struct {
		c   SessionCredential
		err error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			c, err := e.svc.SuspendSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration)
			results <- result{c, err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.c.Generation != in.CredentialGeneration+1 || second.c.Generation != first.c.Generation || !first.c.CleanupAfter.Equal(*second.c.CleanupAfter) {
		t.Fatal("duplicate replicas lost the exact suspension acknowledgment", first.err, second.err)
	}
}
