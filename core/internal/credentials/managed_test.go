package credentials_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/credentials"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/contracts/engineaccess"
)

const fixtureSID = "11111111-1111-4111-8111-111111111111"

type managedFixtureAdapter struct {
	calls       atomic.Int32
	started     chan struct{}
	release     chan struct{}
	fail        bool
	pollStarted chan struct{}
	pollRelease chan struct{}
}

func (*managedFixtureAdapter) AccessMode() string         { return "chatgptAuthTokens" }
func (*managedFixtureAdapter) Engine() string             { return "codex" }
func (*managedFixtureAdapter) VerificationOrigin() string { return "https://auth.openai.com" }
func (*managedFixtureAdapter) Kind() string               { return "codex_oauth" }
func (*managedFixtureAdapter) StartDevice(context.Context) (credentials.DeviceGrant, error) {
	return credentials.DeviceGrant{DeviceID: "fixture-device-secret", UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", IntervalSeconds: 1, ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (a *managedFixtureAdapter) PollDevice(context.Context, credentials.DeviceGrant) (credentials.ManagedToken, error) {
	if a.pollStarted != nil {
		close(a.pollStarted)
		<-a.pollRelease
	}
	return fixtureManagedToken(), nil
}
func (a *managedFixtureAdapter) Refresh(context.Context, credentials.ManagedToken) (credentials.ManagedToken, error) {
	a.calls.Add(1)
	if a.started != nil {
		close(a.started)
		<-a.release
	}
	if a.fail {
		return credentials.ManagedToken{}, errors.New("fixture-private-provider-error")
	}
	return fixtureManagedToken(), nil
}
func fixtureManagedToken() credentials.ManagedToken {
	return credentials.ManagedToken{AccessToken: "fixture-private-access", RefreshToken: "fixture-private-refresh", AccountID: "provider-account", ExpiresAt: time.Now().Add(time.Hour)}
}
func setupManaged(t *testing.T, a credentials.Adapter) (*credentials.Service, *credentials.Service, *pgxpool.Pool, string) {
	t.Helper()
	pool := testPool(t)
	backend, err := keybackend.NewLocal(testLocalKey)
	if err != nil {
		t.Fatal(err)
	}
	st := db.NewPgStore(pool)
	account := insertTestAccount(t, pool)
	if _, err := pool.Exec(context.Background(), `INSERT INTO human_sessions(id,account_id,token_hash,chain_id,expires_at) VALUES($1,$2,'managed-browser-fixture',$1,now()+interval '1 hour')`, fixtureSID, account); err != nil {
		t.Fatal(err)
	}
	return credentials.NewServiceWithAdapters(st, backend, a), credentials.NewServiceWithAdapters(st, backend, a), pool, account
}
func connectManaged(t *testing.T, s *credentials.Service, account string) credentials.Access {
	t.Helper()
	ctx := context.Background()
	start, err := s.StartLogin(ctx, account, "codex", fixtureSID)
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.PollLogin(ctx, account, "codex", fixtureSID, start.AttemptID)
	if err != nil || status.State != "connected" {
		t.Fatalf("login failed: %v", err)
	}
	access, err := s.Obtain(ctx, account, "codex", false, "")
	if err != nil {
		t.Fatal(err)
	}
	return access
}
func TestManagedBrokerCrossReplicaRefreshCoalescesFailedGeneration(t *testing.T) {
	a := &managedFixtureAdapter{started: make(chan struct{}), release: make(chan struct{})}
	s, replica, pool, account := setupManaged(t, a)
	old := connectManaged(t, s, account)
	var ciphertext []byte
	if err := pool.QueryRow(context.Background(), `SELECT ciphertext FROM user_credentials WHERE account_id=$1`, account).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("fixture-private")) {
		t.Fatal("managed state persisted without encryption")
	}
	if _, err := s.Fetch(context.Background(), account, "codex"); !errors.Is(err, credentials.ErrNotManaged) {
		t.Fatal("legacy fetch disclosed managed refresh authority")
	}
	completed := make(chan credentials.Access, 1)
	failed := make(chan error, 1)
	go func() {
		v, err := s.Obtain(context.Background(), account, "codex", true, old.Generation)
		if err != nil {
			failed <- err
			return
		}
		completed <- v
	}()
	<-a.started
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := replica.Obtain(context.Background(), account, "codex", true, old.Generation)
			if !errors.Is(err, credentials.ErrRefreshPending) {
				t.Errorf("parallel consumer got %v", err)
			}
		}()
	}
	wg.Wait()
	close(a.release)
	var fresh credentials.Access
	select {
	case fresh = <-completed:
	case err := <-failed:
		t.Fatal(err)
	}
	if fresh.Generation == old.Generation {
		t.Fatal("refresh did not advance generation")
	}
	cached, err := replica.Obtain(context.Background(), account, "codex", true, old.Generation)
	if err != nil || cached.Generation != fresh.Generation || a.calls.Load() != 1 {
		t.Fatalf("refresh did not coalesce: %v, calls %d", err, a.calls.Load())
	}
}
func TestManagedBrokerLateRefreshCannotResurrectDeleteOrOverwriteImport(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "import"}[replace], func(t *testing.T) {
			a := &managedFixtureAdapter{started: make(chan struct{}), release: make(chan struct{})}
			s, replica, _, account := setupManaged(t, a)
			old := connectManaged(t, s, account)
			result := make(chan error, 1)
			go func() {
				_, err := s.Obtain(context.Background(), account, "codex", true, old.Generation)
				result <- err
			}()
			<-a.started
			var err error
			if replace {
				err = replica.Store(context.Background(), account, "codex", []byte("replacement-private-token"))
			} else {
				err = replica.Delete(context.Background(), account, "codex")
			}
			if err != nil {
				t.Fatal(err)
			}
			close(a.release)
			if err = <-result; !errors.Is(err, credentials.ErrReconnectRequired) {
				t.Fatalf("stale refresh accepted: %v", err)
			}
			raw, err := replica.Fetch(context.Background(), account, "codex")
			if replace {
				if err != nil || string(raw) != "replacement-private-token" {
					t.Fatal("stale refresh overwrote explicit import")
				}
			} else if !errors.Is(err, credentials.ErrNotFound) {
				t.Fatal("stale refresh resurrected deleted login")
			}
		})
	}
}
func TestManagedBrokerAmbiguousCrashRequiresReconnectWithoutRetry(t *testing.T) {
	a := &managedFixtureAdapter{}
	s, replica, pool, account := setupManaged(t, a)
	_ = connectManaged(t, s, account)
	if _, err := pool.Exec(context.Background(), `UPDATE user_credentials SET state='refresh_pending',refresh_claim=gen_random_uuid(),claim_deadline=now()-interval '1 second' WHERE account_id=$1`, account); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := replica.Obtain(context.Background(), account, "codex", false, ""); !errors.Is(err, credentials.ErrReconnectRequired) {
			t.Fatalf("ambiguous refresh accepted: %v", err)
		}
	}
	if a.calls.Load() != 0 {
		t.Fatal("possibly consumed refresh credential retried")
	}
	list, err := s.List(context.Background(), account)
	if err != nil || list[0].State != "reconnect_required" {
		t.Fatal("ambiguity fence not durable")
	}
}
func TestManagedLoginBoundToBrowserAndFencedByCancelAndImport(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "import"}[replace], func(t *testing.T) {
			a := &managedFixtureAdapter{pollStarted: make(chan struct{}), pollRelease: make(chan struct{})}
			s, replica, _, account := setupManaged(t, a)
			start, err := s.StartLogin(context.Background(), account, "codex", fixtureSID)
			if err != nil {
				t.Fatal(err)
			}
			otherSID := "22222222-2222-4222-8222-222222222222"
			if _, err = s.PollLogin(context.Background(), account, "codex", otherSID, start.AttemptID); !errors.Is(err, credentials.ErrNotFound) {
				t.Fatal("another browser polled device grant")
			}
			if err = s.CancelLogin(context.Background(), account, "codex", otherSID, start.AttemptID); !errors.Is(err, credentials.ErrNotFound) {
				t.Fatal("another browser canceled device grant")
			}
			result := make(chan error, 1)
			go func() {
				_, err := s.PollLogin(context.Background(), account, "codex", fixtureSID, start.AttemptID)
				result <- err
			}()
			<-a.pollStarted
			if replace {
				err = replica.Store(context.Background(), account, "codex", []byte("explicit-import"))
			} else {
				err = replica.CancelLogin(context.Background(), account, "codex", fixtureSID, start.AttemptID)
			}
			if err != nil {
				t.Fatal(err)
			}
			close(a.pollRelease)
			if err = <-result; !errors.Is(err, credentials.ErrNotFound) {
				t.Fatalf("stale login persisted: %v", err)
			}
			raw, err := replica.Fetch(context.Background(), account, "codex")
			if replace {
				if err != nil || string(raw) != "explicit-import" {
					t.Fatal("import was overwritten")
				}
			} else if !errors.Is(err, credentials.ErrNotFound) {
				t.Fatal("canceled login persisted")
			}
		})
	}
}

func TestManagedDeviceAttemptResumeThrottleAndCrashFence(t *testing.T) {
	a := &managedFixtureAdapter{}
	s, replica, pool, account := setupManaged(t, a)
	ctx := context.Background()
	start, err := s.StartLogin(ctx, account, "codex", fixtureSID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := replica.StartLogin(ctx, account, "codex", fixtureSID)
	if err != nil || resumed.AttemptID != start.AttemptID {
		t.Fatal("same browser did not resume its durable attempt")
	}
	if _, err = replica.StartLogin(ctx, account, "codex", "22222222-2222-4222-8222-222222222222"); !errors.Is(err, credentials.ErrRefreshPending) {
		t.Fatal("another browser replaced an active login attempt")
	}
	if _, err = pool.Exec(ctx, `UPDATE credential_login_attempts SET state='polling',claim=gen_random_uuid(),claim_deadline=now()-interval '1 second' WHERE attempt_id=$1`, start.AttemptID); err != nil {
		t.Fatal(err)
	}
	status, err := replica.PollLogin(ctx, account, "codex", fixtureSID, start.AttemptID)
	if err != nil || status.State != "reconnect_required" {
		t.Fatal("ambiguous device exchange was retried")
	}
	if _, err = s.Fetch(ctx, account, "codex"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatal("ambiguous login persisted a credential")
	}
}

func TestManagedRefreshProviderFailureIsDurablyRedacted(t *testing.T) {
	a := &managedFixtureAdapter{fail: true}
	s, replica, pool, account := setupManaged(t, a)
	old := connectManaged(t, s, account)
	_, err := s.Obtain(context.Background(), account, "codex", true, old.Generation)
	if !errors.Is(err, credentials.ErrReconnectRequired) || strings.Contains(err.Error(), "private-provider") {
		t.Fatal("provider failure was not redacted")
	}
	if _, err = replica.Obtain(context.Background(), account, "codex", true, old.Generation); !errors.Is(err, credentials.ErrReconnectRequired) && err != nil || a.calls.Load() != 1 {
		t.Fatal("failed provider exchange was retried")
	}
	var actions string
	if err = pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(action,','),'') FROM credential_lifecycle_events WHERE account_id=$1`, account).Scan(&actions); err != nil || strings.Contains(actions, "private") {
		t.Fatal("audit contained provider data")
	}
}

func TestManagedAuditExpiredStartLeaseCanReconnect(t *testing.T) {
	s, replica, pool, account := setupManaged(t, &managedFixtureAdapter{})
	start, err := s.StartLogin(context.Background(), account, "codex", fixtureSID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(context.Background(), `UPDATE credential_login_attempts SET state='starting',ciphertext=NULL,key_id=NULL,user_code='',verification_url='',claim_deadline=now()-interval '1 second' WHERE attempt_id=$1`, start.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := replica.StartLogin(context.Background(), account, "codex", fixtureSID)
	if err != nil || next.AttemptID == start.AttemptID || next.UserCode == "" || next.VerificationURL == "" {
		t.Fatal("an expired startup lease must allow a fresh, usable device ceremony")
	}
}

func TestManagedAuditRevokedBrowserCannotReplaceCredential(t *testing.T) {
	a := &managedFixtureAdapter{pollStarted: make(chan struct{}), pollRelease: make(chan struct{})}
	s, replica, pool, account := setupManaged(t, a)
	ctx := context.Background()
	if err := s.Store(ctx, account, "codex", []byte("original-import-fixture")); err != nil {
		t.Fatal(err)
	}
	start, err := s.StartLogin(ctx, account, "codex", fixtureSID)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, e := s.PollLogin(ctx, account, "codex", fixtureSID, start.AttemptID); result <- e }()
	<-a.pollStarted
	if _, err = pool.Exec(ctx, `UPDATE human_sessions SET revoked_at=now() WHERE id=$1`, fixtureSID); err != nil {
		t.Fatal(err)
	}
	close(a.pollRelease)
	if err = <-result; !errors.Is(err, credentials.ErrNotFound) {
		t.Error("a revoked initiating browser must not commit a managed credential")
	}
	raw, err := replica.Fetch(ctx, account, "codex")
	if err != nil || string(raw) != "original-import-fixture" {
		t.Fatal("login committed after revocation and replaced the credential")
	}
}

func TestManagedAuditDeadBrowserAttemptDoesNotBlockNewLogin(t *testing.T) {
	s, _, pool, account := setupManaged(t, &managedFixtureAdapter{})
	ctx := context.Background()
	start, err := s.StartLogin(ctx, account, "codex", fixtureSID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE human_sessions SET revoked_at=now() WHERE id=$1`, fixtureSID); err != nil {
		t.Fatal(err)
	}
	const nextSID = "33333333-3333-4333-8333-333333333333"
	if _, err = pool.Exec(ctx, `INSERT INTO human_sessions(id,account_id,token_hash,chain_id,expires_at) VALUES($1,$2,'next-browser-fixture',$1,now()+interval '1 hour')`, nextSID, account); err != nil {
		t.Fatal(err)
	}
	next, err := s.StartLogin(ctx, account, "codex", nextSID)
	if err != nil || next.AttemptID == start.AttemptID || next.UserCode == "" {
		t.Fatal("a dead browser attempt blocked a fresh login from a live browser")
	}
	if _, err = s.PollLogin(ctx, account, "codex", fixtureSID, start.AttemptID); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatal("the dead browser retained its replaced attempt")
	}
}

func TestManagedAuditLoginCommitUsesLiveBrowserChain(t *testing.T) {
	for _, state := range []string{"disabled", "expired", "rotated"} {
		t.Run(state, func(t *testing.T) {
			a := &managedFixtureAdapter{pollStarted: make(chan struct{}), pollRelease: make(chan struct{})}
			s, _, pool, account := setupManaged(t, a)
			ctx := context.Background()
			if err := s.Store(ctx, account, "codex", []byte("original-import-fixture")); err != nil {
				t.Fatal(err)
			}
			start, err := s.StartLogin(ctx, account, "codex", fixtureSID)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { _, e := s.PollLogin(ctx, account, "codex", fixtureSID, start.AttemptID); result <- e }()
			<-a.pollStarted
			switch state {
			case "disabled":
				_, err = pool.Exec(ctx, `UPDATE accounts SET disabled_at=now() WHERE id=$1`, account)
			case "expired":
				_, err = pool.Exec(ctx, `UPDATE human_sessions SET expires_at=now()-interval '1 second' WHERE id=$1`, fixtureSID)
			case "rotated":
				_, err = pool.Exec(ctx, `UPDATE human_sessions SET revoked_at=now() WHERE id=$1`, fixtureSID)
				if err == nil {
					_, err = pool.Exec(ctx, `INSERT INTO human_sessions(account_id,token_hash,chain_id,expires_at) VALUES($1,'rotated-browser-fixture',$2,now()+interval '1 hour')`, account, fixtureSID)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			close(a.pollRelease)
			err = <-result
			if state == "rotated" {
				if err != nil {
					t.Fatal("legitimate browser rotation broke its pending ceremony")
				}
				if _, err = s.Obtain(ctx, account, "codex", false, ""); err != nil {
					t.Fatal("rotated browser did not persist the connection")
				}
			} else {
				if !errors.Is(err, credentials.ErrNotFound) {
					t.Fatal("inactive principal committed a connection")
				}
				raw, err := s.Fetch(ctx, account, "codex")
				if err != nil || string(raw) != "original-import-fixture" {
					t.Fatal("inactive principal overwrote the prior credential")
				}
			}
		})
	}
}

// A second provider must select its own consumer mode on both cache and refresh
// paths, while the existing Codex access wire contract remains compatible.
type alternateManagedAdapter struct{ *managedFixtureAdapter }

func (*alternateManagedAdapter) Engine() string     { return "fixture_engine" }
func (*alternateManagedAdapter) Kind() string       { return "fixture_oauth" }
func (*alternateManagedAdapter) AccessMode() string { return "fixtureAccessTokens" }

func TestManagedAccessModeBelongsToSelectedAdapter(t *testing.T) {
	t.Cleanup(engineaccess.Register(engineaccess.TokenCodec("fixture_engine", "fixtureAccessTokens")))
	for _, engine := range []string{"codex", "fixture_engine"} {
		t.Run(engine, func(t *testing.T) {
			base := &managedFixtureAdapter{}
			var adapter credentials.Adapter = base
			wantMode := "chatgptAuthTokens"
			accountField := "chatgpt_account_id"
			if engine != "codex" {
				adapter = &alternateManagedAdapter{base}
				wantMode = "fixtureAccessTokens"
				accountField = "account_id"
			}
			s, _, _, account := setupManaged(t, adapter)
			ctx := context.Background()
			attempt, err := s.StartLogin(ctx, account, engine, fixtureSID)
			if err != nil {
				t.Fatal(err)
			}
			status, err := s.PollLogin(ctx, account, engine, fixtureSID, attempt.AttemptID)
			if err != nil || status.State != "connected" {
				t.Fatalf("login: %v", err)
			}
			cached, err := s.Obtain(ctx, account, engine, false, "")
			if err != nil {
				t.Fatal(err)
			}
			if base.calls.Load() != 0 {
				t.Fatal("cache unnecessarily refreshed")
			}
			fresh, err := s.Obtain(ctx, account, engine, true, cached.Generation)
			if err != nil {
				t.Fatal(err)
			}
			if base.calls.Load() != 1 || fresh.Generation == cached.Generation {
				t.Fatal("forced refresh did not rotate generation exactly once")
			}
			for _, access := range []credentials.Access{cached, fresh} {
				raw, err := json.Marshal(access)
				if err != nil {
					t.Fatal(err)
				}
				var wire map[string]json.RawMessage
				if err = json.Unmarshal(raw, &wire); err != nil {
					t.Fatal(err)
				}
				if access.Mode != wantMode {
					t.Errorf("mode=%q, want selected adapter mode %q", access.Mode, wantMode)
				}
				if access.AccountID != "provider-account" || string(wire[accountField]) != `"provider-account"` {
					t.Fatal("compatible provider account field lost")
				}
				if len(wire) != 5 {
					t.Errorf("unexpected access wire fields: %d", len(wire))
				}
				for _, key := range []string{"mode", "access_token", accountField, "generation", "expires_at"} {
					if _, ok := wire[key]; !ok {
						t.Errorf("missing wire field %s", key)
					}
				}
				if bytes.Contains(raw, []byte("refresh_token")) || bytes.Contains(raw, []byte("fixture-private-refresh")) {
					t.Fatal("access serialization leaked refresh authority")
				}
			}
		})
	}
}

func TestUnregisteredManagedModeFailsBeforeProvider(t *testing.T) {
	base := &managedFixtureAdapter{}
	s := credentials.NewServiceWithAdapters(nil, nil, &alternateManagedAdapter{base})
	if len(s.ManagedCapabilities()) != 0 {
		t.Fatal("unregistered protocol advertised login")
	}
	_, err := s.StartLogin(t.Context(), "fixture-account", "fixture_engine", fixtureSID)
	if !errors.Is(err, credentials.ErrNotManaged) {
		t.Fatal("unregistered consumer mode reached provider or persistence")
	}
}

// A forced refresh is spent at most once a minute per account: a session (or
// an agent with the pod's credentials) asking again gets the cached access.
func TestManagedForcedRefreshIsThrottledPerAccount(t *testing.T) {
	a := &managedFixtureAdapter{}
	s, replica, pool, account := setupManaged(t, a)
	old := connectManaged(t, s, account)
	fresh, err := s.Obtain(context.Background(), account, "codex", true, old.Generation)
	if err != nil || fresh.Generation == old.Generation || a.calls.Load() != 1 {
		t.Fatalf("first forced refresh: %v, calls %d", err, a.calls.Load())
	}
	for i := 0; i < 5; i++ {
		cached, err := replica.Obtain(context.Background(), account, "codex", true, fresh.Generation)
		if err != nil || cached.Generation != fresh.Generation || cached.AccessToken == "" {
			t.Fatalf("throttled forced refresh did not return the cached access: %v", err)
		}
	}
	if a.calls.Load() != 1 {
		t.Fatalf("forced refreshes were not throttled: %d provider calls", a.calls.Load())
	}
	if _, err = pool.Exec(context.Background(), `UPDATE credential_lifecycle_events SET created_at=created_at-interval '2 minutes' WHERE account_id=$1`, account); err != nil {
		t.Fatal(err)
	}
	later, err := replica.Obtain(context.Background(), account, "codex", true, fresh.Generation)
	if err != nil || later.Generation == fresh.Generation || a.calls.Load() != 2 {
		t.Fatalf("forced refresh after the window: %v, calls %d", err, a.calls.Load())
	}
}

// A failed refresh refuses the caller that forced it, but every other session
// of the account keeps the still-valid access until it expires. The possibly
// consumed refresh token is never presented again.
func TestManagedFailedRefreshKeepsAccessUntilExpiry(t *testing.T) {
	a := &managedFixtureAdapter{fail: true}
	s, replica, pool, account := setupManaged(t, a)
	old := connectManaged(t, s, account)
	if _, err := s.Obtain(context.Background(), account, "codex", true, old.Generation); !errors.Is(err, credentials.ErrReconnectRequired) {
		t.Fatalf("forcing caller: %v", err)
	}
	cached, err := replica.Obtain(context.Background(), account, "codex", false, "")
	if err != nil || cached.Generation != old.Generation || cached.AccessToken != old.AccessToken {
		t.Fatalf("other sessions lost the still-valid access: %v", err)
	}
	list, err := s.List(context.Background(), account)
	if err != nil || list[0].State != "usable" {
		t.Fatal("one failed refresh forced every session to reconnect")
	}
	if _, err = pool.Exec(context.Background(), `UPDATE credential_lifecycle_events SET created_at=created_at-interval '2 minutes' WHERE account_id=$1`, account); err != nil {
		t.Fatal(err)
	}
	if _, err = replica.Obtain(context.Background(), account, "codex", true, old.Generation); !errors.Is(err, credentials.ErrReconnectRequired) {
		t.Fatalf("spent refresh authority: %v", err)
	}
	if a.calls.Load() != 1 {
		t.Fatalf("possibly consumed refresh token presented again: %d calls", a.calls.Load())
	}
	if _, err = pool.Exec(context.Background(), `UPDATE user_credentials SET expires_at=now()+interval '10 seconds' WHERE account_id=$1`, account); err != nil {
		t.Fatal(err)
	}
	if _, err = replica.Obtain(context.Background(), account, "codex", false, ""); !errors.Is(err, credentials.ErrReconnectRequired) || a.calls.Load() != 1 {
		t.Fatalf("expiring access without refresh authority: %v, calls %d", err, a.calls.Load())
	}
	list, err = s.List(context.Background(), account)
	if err != nil || list[0].State != "reconnect_required" {
		t.Fatal("expired access without refresh authority did not require reconnect")
	}
}
