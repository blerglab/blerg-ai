package mcpconn

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sessionDispatchReady(t *testing.T, configure ...func(*sessionFlowEnv)) (*sessionFlowEnv, SessionDispatchRequest) {
	t.Helper()
	e := newSessionFlowEnv(t)
	for _, apply := range configure {
		apply(e)
	}
	_, _, callback := sessionPrepared(t, e)
	c, err := e.svc.CompleteSessionOAuth(context.Background(), callback)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.pool.Exec(context.Background(), `UPDATE mcp_connections SET lifecycle='active',credential_generation=credential_generation+1,suspended_at=NULL,cleanup_after=NULL,binding_lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	return e, SessionDispatchRequest{SessionScope: e.auth.Binding.SessionScope, ConnectionID: c.ID, HumanSessionID: e.auth.Binding.HumanSessionID, LaunchGeneration: e.auth.LaunchGeneration, CredentialGeneration: c.Generation + 1, GrantTokenHash: strings.Repeat("a", 64)}
}
func TestSessionDispatchLiveBindingAndAccessOnly(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	if err := e.svc.CheckSessionDispatch(ctx, in); err != nil {
		t.Fatal("live scoped admission refused", err)
	}
	secret, err := e.svc.FetchSessionSecret(ctx, in)
	if err != nil || !strings.HasPrefix(secret.Value, "Bearer ") || secret.URL != e.auth.Definition.URL || secret.ExpiresAt == nil || !secret.ExpiresAt.After(time.Now()) || secret.PinPolicy != PinPolicyStrict {
		t.Fatal("scoped gateway did not receive a current access credential")
	}
	if _, err = e.svc.FetchSecret(ctx, in.AccountID, in.ConnectionID, Proof{SessionID: in.HumanSessionID}); !errors.Is(err, ErrNotFound) {
		t.Fatal("legacy token path exposed scoped credential")
	}
}
func TestSessionDispatchWarmAdmissionCannotOutliveBinding(t *testing.T) {
	for _, name := range []string{"generation", "suspended", "lease expired", "login revoked", "login expired", "account disabled", "human deadline", "password reset"} {
		t.Run(name, func(t *testing.T) {
			e, in := sessionDispatchReady(t)
			ctx := context.Background()
			if err := e.svc.CheckSessionDispatch(ctx, in); err != nil {
				t.Fatal(err)
			}
			var err error
			switch name {
			case "generation":
				_, err = e.pool.Exec(ctx, `UPDATE mcp_connections SET credential_generation=credential_generation+1 WHERE id=$1`, in.ConnectionID)
			case "suspended":
				_, err = e.svc.SuspendSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration)
			case "lease expired":
				_, err = e.pool.Exec(ctx, `UPDATE mcp_connections SET binding_lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, in.ConnectionID)
			case "login revoked":
				_, err = e.pool.Exec(ctx, `UPDATE human_sessions SET revoked_at=now() WHERE chain_id=$1`, in.HumanSessionID)
			case "login expired":
				_, err = e.pool.Exec(ctx, `UPDATE human_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE chain_id=$1`, in.HumanSessionID)
			case "human deadline":
				_, err = e.pool.Exec(ctx, `UPDATE mcp_connections SET human_deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, in.ConnectionID)
			case "password reset":
				_, err = e.pool.Exec(ctx, `UPDATE accounts SET must_change_password=true WHERE id=$1`, in.AccountID)
			case "account disabled":
				_, err = e.pool.Exec(ctx, `UPDATE accounts SET disabled_at=now() WHERE id=$1`, in.AccountID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = e.svc.CheckSessionDispatch(ctx, in); !errors.Is(err, ErrNotFound) {
				t.Fatal("cached authority bypassed current binding/login", err)
			}
			if _, err = e.svc.FetchSessionSecret(ctx, in); !errors.Is(err, ErrNotFound) {
				t.Fatal("cached authority bypassed current token admission", err)
			}
		})
	}
}
func TestSessionDispatchRejectsForeignAndUnwired(t *testing.T) {
	e, valid := sessionDispatchReady(t)
	ctx := context.Background()
	for _, change := range []func(*SessionDispatchRequest){
		func(in *SessionDispatchRequest) { in.AccountID = "00000000-0000-4000-8000-000000000199" },
		func(in *SessionDispatchRequest) { in.RunnerSessionID = "00000000-0000-4000-8000-000000000199" },
		func(in *SessionDispatchRequest) { in.RunnerInstallationID = "foreign" },
		func(in *SessionDispatchRequest) { in.HumanSessionID = "00000000-0000-4000-8000-000000000199" },
		func(in *SessionDispatchRequest) { in.CredentialGeneration++ },
		func(in *SessionDispatchRequest) { in.LaunchGeneration++ },
		func(in *SessionDispatchRequest) { in.GrantTokenHash = "invalid" },
	} {
		in := valid
		change(&in)
		if err := e.svc.CheckSessionDispatch(ctx, in); !errors.Is(err, ErrNotFound) {
			t.Fatal("foreign dispatch accepted", err)
		}
	}
	e.svc.sessionAuthority = nil
	if err := e.svc.CheckSessionDispatch(ctx, valid); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unwired admission did not fail closed")
	}
	if _, err := e.svc.FetchSessionSecret(ctx, valid); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unwired token fetch did not fail closed")
	}
}

func TestSessionDispatchAuditFailureRefusesSecret(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `CREATE FUNCTION deny_scoped_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit refusal'; END $$; CREATE TRIGGER deny_scoped_audit BEFORE INSERT ON mcp_secret_access_log FOR EACH ROW EXECUTE FUNCTION deny_scoped_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.FetchSessionSecret(ctx, in); err == nil {
		t.Fatal("unaudited scoped credential was returned")
	}
}

func sessionNearExpiry(e *sessionFlowEnv) {
	now := time.Now().Add(59*time.Minute + 45*time.Second)
	e.svc.oauthNow = func() time.Time { return now }
}
func TestSessionDispatchRefreshRotatesAccessOnly(t *testing.T) {
	e, in := sessionDispatchReady(t)
	var ct []byte
	var key *string
	if err := e.pool.QueryRow(context.Background(), `SELECT secret_ciphertext,key_id FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&ct, &key); err != nil {
		t.Fatal(err)
	}
	initial, err := e.svc.openBundle(context.Background(), ct, key)
	if err != nil {
		t.Fatal(err)
	}
	sessionNearExpiry(e)
	secret, err := e.svc.FetchSessionSecret(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	e.provider.Lock()
	spent, reuse := e.provider.RefreshSpent, e.provider.RefreshReuse
	e.provider.Unlock()
	if spent != 1 || reuse != 0 || secret.Value == "Bearer "+initial.AccessToken || !strings.HasPrefix(secret.Value, "Bearer ") {
		t.Fatal("scoped refresh did not rotate exactly once before returning access only")
	}
}
func TestSessionDispatchUncertainRefreshSuspends(t *testing.T) {
	e, in := sessionDispatchReady(t)
	sessionNearExpiry(e)
	e.provider.RefreshStatus = 500
	if _, err := e.svc.FetchSessionSecret(context.Background(), in); !errors.Is(err, ErrNotFound) {
		t.Fatal("uncertain rotation did not require sign-in", err)
	}
	var state, status string
	var generation int64
	if err := e.pool.QueryRow(context.Background(), `SELECT lifecycle,status,credential_generation FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&state, &status, &generation); err != nil {
		t.Fatal(err)
	}
	if state != "suspended" || status != "needs_auth" || generation != in.CredentialGeneration+1 {
		t.Fatal("uncertain refresh was not durably fenced")
	}
}
func TestSessionDispatchExpiredRefreshLeaseCannotSpendAgain(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET session_refresh_lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, in.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CheckSessionDispatch(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatal("abandoned refresh lease still admitted dispatch", err)
	}
	if _, err := e.svc.FetchSessionSecret(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatal("abandoned refresh lease retried a possible rotation", err)
	}
	e.provider.Lock()
	spent := e.provider.RefreshSpent
	e.provider.Unlock()
	if spent != 0 {
		t.Fatal("abandoned lease spent a token again")
	}
}
func TestSessionDispatchLateRefreshCannotResurrect(t *testing.T) {
	e, in := sessionDispatchReady(t)
	sessionNearExpiry(e)
	ctx := context.Background()
	hookError := make(chan error, 1)
	e.provider.AfterRefresh = func() {
		_, err := e.svc.SuspendSessionCredential(ctx, in.SessionScope, in.ConnectionID, in.CredentialGeneration)
		hookError <- err
	}
	if _, err := e.svc.FetchSessionSecret(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatal("late refresh crossed suspension", err)
	}
	if err := <-hookError; err != nil {
		t.Fatal(err)
	}
	var state, status string
	if err := e.pool.QueryRow(ctx, `SELECT lifecycle,status FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&state, &status); err != nil {
		t.Fatal(err)
	}
	e.provider.Lock()
	revoked := len(e.provider.Revoked)
	e.provider.Unlock()
	if state != "suspended" || status != "needs_auth" || revoked != 2 {
		t.Fatal("late issued tokens were retained or not revoked")
	}
}

type sessionReadTracer struct{ reads atomic.Int64 }

func (t *sessionReadTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(strings.TrimSpace(data.SQL), "SELECT") && strings.Contains(data.SQL, "mcp_connections") {
		t.reads.Add(1)
	}
	return ctx
}
func (*sessionReadTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestSessionDispatchWarmCostAndAuthorityTTL(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	now := time.Now()
	trace := &sessionReadTracer{}
	cfg := e.pool.Config()
	cfg.ConnConfig.Tracer = trace
	observed, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observed.Close)
	svc := NewService(db.NewPgStore(observed), e.svc.backend, e.svc.policy, WithSessionOAuthAuthority(e.svc.sessionAuthority), WithOAuthClock(func() time.Time { return now }))
	if err = svc.CheckSessionDispatch(ctx, in); err != nil {
		t.Fatal(err)
	}
	if trace.reads.Load() != 2 || e.dispatchCalls.Load() != 1 {
		t.Fatal("cold authority check must re-read Core after RPC")
	}
	trace.reads.Store(0)
	times := make([]time.Duration, 100)
	for i := range times {
		started := time.Now()
		if err = svc.CheckSessionDispatch(ctx, in); err != nil {
			t.Fatal(err)
		}
		times[i] = time.Since(started)
	}
	if trace.reads.Load() != 100 || e.dispatchCalls.Load() != 1 {
		t.Fatal("warm dispatch must use one Core read and no additional Runner authority round trip")
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	t.Logf("warm admission: 100 requests, exactly 100 indexed-key reads, zero extra authority RPCs; p50=%s p95=%s", times[50], times[95])
	now = now.Add(2 * time.Second)
	e.revoked.Store(true)
	if err = svc.CheckSessionDispatch(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired authority cache retained approval", err)
	}
}
func TestSessionDispatchLogoutDuringColdAuthorityWins(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	hookError := make(chan error, 1)
	e.dispatchHook = func() {
		_, err := e.pool.Exec(ctx, `UPDATE human_sessions SET revoked_at=clock_timestamp() WHERE chain_id=$1`, in.HumanSessionID)
		hookError <- err
	}
	if err := e.svc.CheckSessionDispatch(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatal("logout during cold authority RPC did not win", err)
	}
	if err := <-hookError; err != nil {
		t.Fatal(err)
	}
}

func TestSessionDispatchTwoCoreInstancesSpendRefreshOnce(t *testing.T) {
	e, in := sessionDispatchReady(t)
	sessionNearExpiry(e)
	ctx := context.Background()
	peer := NewService(db.NewPgStore(e.pool), e.svc.backend, e.svc.policy, WithSessionOAuthAuthority(e.svc.sessionAuthority), WithOAuthClock(e.svc.oauthTime))
	peer.httpc = e.svc.httpc
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	e.provider.AfterRefresh = func() { close(entered); <-release }
	type answer struct {
		secret SecretResult
		err    error
	}
	first := make(chan answer, 1)
	go func() { secret, err := e.svc.FetchSessionSecret(ctx, in); first <- answer{secret, err} }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not reach refresh provider")
	}
	if _, err := peer.FetchSessionSecret(ctx, in); !errors.Is(err, ErrSessionRefreshBusy) {
		t.Fatal("second Core did not respect durable refresh lease", err)
	}
	if err := peer.CheckSessionDispatch(ctx, in); !errors.Is(err, ErrSessionRefreshBusy) {
		t.Fatal("warm credential could dispatch through in-progress rotation", err)
	}
	unblock()
	done := <-first
	if done.err != nil {
		t.Fatal(done.err)
	}
	second, err := peer.FetchSessionSecret(ctx, in)
	if err != nil || second.Value != done.secret.Value {
		t.Fatal("peer did not read the persisted rotated access token", err)
	}
	e.provider.Lock()
	spent, reuse := e.provider.RefreshSpent, e.provider.RefreshReuse
	e.provider.Unlock()
	if spent != 1 || reuse != 0 {
		t.Fatal("multiple Core instances spent a rotating refresh token twice")
	}
}
func TestSessionDispatchCanceledCallerKeepsRotatedBundle(t *testing.T) {
	e, in := sessionDispatchReady(t)
	sessionNearExpiry(e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	e.provider.AfterRefresh = func() { close(entered); <-release }
	result := make(chan error, 1)
	go func() { _, err := e.svc.FetchSessionSecret(ctx, in); result <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh owner did not reach provider")
	}
	e.svc.flightMu.Lock()
	var finished <-chan struct{}
	for key, f := range e.svc.flights {
		if strings.HasPrefix(key, "scoped/") {
			finished = f.done
		}
	}
	e.svc.flightMu.Unlock()
	if finished == nil {
		t.Fatal("refresh was not singleflight owned")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("canceled caller stayed attached", err)
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("detached refresh did not finish")
	}
	if _, err := e.svc.FetchSessionSecret(context.Background(), in); err != nil {
		t.Fatal("rotated token lost with canceled caller", err)
	}
	e.provider.Lock()
	spent, reuse, revoked := e.provider.RefreshSpent, e.provider.RefreshReuse, len(e.provider.Revoked)
	e.provider.Unlock()
	if spent != 1 || reuse != 0 || revoked != 0 {
		t.Fatal("caller cancellation lost or retried a completed rotation")
	}
}
func TestSessionDispatchConfidentialClientStaysInCore(t *testing.T) {
	e, in := sessionDispatchReady(t, func(e *sessionFlowEnv) { e.provider.Confidential = true })
	sessionNearExpiry(e)
	secret, err := e.svc.FetchSessionSecret(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "client-secret") || strings.Contains(string(encoded), "rt-") {
		t.Fatal("gateway received refresh/client credentials")
	}
	e.provider.Lock()
	spent := e.provider.RefreshSpent
	e.provider.Unlock()
	if spent != 1 {
		t.Fatal("confidential refresh authentication failed")
	}
}

func TestSessionDispatchRefreshCannotExpandScopesOrResurrectLostFence(t *testing.T) {
	for _, name := range []string{"scope expansion", "lost fence", "expired refresh lease", "expired login", "closing"} {
		t.Run(name, func(t *testing.T) {
			e, in := sessionDispatchReady(t)
			sessionNearExpiry(e)
			ctx := context.Background()
			if name == "scope expansion" {
				e.provider.RefreshRaw = `{"access_token":"synthetic-expanded","refresh_token":"synthetic-rotated","token_type":"Bearer","expires_in":3600,"scope":"notes.read forbidden.admin"}`
			} else {
				e.provider.AfterRefresh = func() {
					var query string
					switch name {
					case "lost fence":
						query = `UPDATE mcp_connections SET refresh_fence=refresh_fence+1 WHERE id=$1`
					case "expired refresh lease":
						query = `UPDATE mcp_connections SET session_refresh_lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`
					case "expired login":
						_, err := e.pool.Exec(ctx, `UPDATE human_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE chain_id=$1`, in.HumanSessionID)
						if err != nil {
							t.Error(err)
						}
						return
					case "closing":
						query = `UPDATE mcp_connections SET lifecycle='closing',credential_generation=credential_generation+1,binding_lease_until=NULL,session_refresh_lease_until=NULL,cleanup_after=now() WHERE id=$1`
					}
					if _, err := e.pool.Exec(ctx, query, in.ConnectionID); err != nil {
						t.Error(err)
					}
				}
			}
			if _, err := e.svc.FetchSessionSecret(ctx, in); err == nil {
				t.Fatal("invalid or superseded refresh returned a credential")
			}
			var lifecycle string
			if err := e.pool.QueryRow(ctx, `SELECT lifecycle FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&lifecycle); err != nil {
				t.Fatal(err)
			}
			if lifecycle == "active" {
				t.Fatal("uncertain refresh remained active")
			}
			if name == "closing" && lifecycle != "closing" {
				t.Fatal("late refresh changed a closing row")
			}
			if _, _, revoked, _ := e.provider.Stats(); revoked != 2 {
				t.Fatalf("lost rotated tokens not revoked: %d", revoked)
			}
		})
	}
}

func TestSessionDispatchApproverMustStayLiveAndAuditKeepsFrozenProof(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	var actor string
	if err := e.pool.QueryRow(ctx, `INSERT INTO human_sessions(account_id,token_hash,chain_id) VALUES($1,'synthetic-approver',gen_random_uuid()) RETURNING id::text`, in.AccountID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE human_sessions SET chain_id=id WHERE id=$1`, actor); err != nil {
		t.Fatal(err)
	}
	in.ActorSessionID = actor
	if _, err := e.svc.FetchSessionSecret(ctx, in); err != nil {
		t.Fatal(err)
	}
	var original, auditedActor, installation, session, hash string
	var launch, generation int64
	if err := e.pool.QueryRow(ctx, `SELECT fetched_by_session_id::text,scoped_actor_sid::text,runner_installation_id,runner_session_id::text,runner_launch_generation,credential_generation,encode(gateway_grant_hash,'hex') FROM mcp_secret_access_log WHERE connection_id=$1`, in.ConnectionID).Scan(&original, &auditedActor, &installation, &session, &launch, &generation, &hash); err != nil {
		t.Fatal(err)
	}
	if original != in.HumanSessionID || auditedActor != actor || installation != in.RunnerInstallationID || session != in.RunnerSessionID || launch != in.LaunchGeneration || generation != in.CredentialGeneration || hash != in.GrantTokenHash {
		t.Fatal("audit lost frozen dispatch scope")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE human_sessions SET revoked_at=now() WHERE chain_id=$1`, actor); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CheckSessionDispatch(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatal("warm admission survived approver logout")
	}
	in.ActorSessionID = in.HumanSessionID
	if err := e.svc.CheckSessionDispatch(ctx, in); err != nil {
		t.Fatal("original human binding incorrectly suspended", err)
	}
	in.ActorSessionID = "00000000-0000-4000-8000-000000000199"
	if err := e.svc.CheckSessionDispatch(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown approver admitted")
	}
}

func TestSessionGatewayRejectsChangedGenerationAfterCallback(t *testing.T) {
	e, in := sessionDispatchReady(t)
	// A callback's external authority result does not survive a later runtime change.
	in.LaunchGeneration++
	if err := e.svc.CheckSessionDispatch(context.Background(), in); !errors.Is(err, ErrNotFound) {
		t.Fatal("callback authorization accepted a changed runtime generation")
	}
}

func TestSessionAuthorityInternalHostContract(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "blerg-runner", "runner.synthetic.svc", "runner.synthetic.svc.cluster.local"} {
		if !sessionAuthorityPlaintextHost(host, true) {
			t.Errorf("internal host refused: %s", host)
		}
	}
	for _, host := range []string{"public.example", "other-runner", "blerg-runner.attacker.example", "runner.svc", "runner.synthetic.svc.attacker.example"} {
		if sessionAuthorityPlaintextHost(host, true) {
			t.Errorf("public or ambiguous plaintext host allowed: %s", host)
		}
	}
}
