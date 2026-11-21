package mcpgw

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

type scopedCoreFake struct {
	admit                   func(context.Context, SessionDispatchRequest) error
	token                   Credential
	legacyCalls, admissions atomic.Int64
	mu                      sync.Mutex
}

func (f *scopedCoreFake) Token(context.Context, Proof, string) (Credential, error) {
	f.legacyCalls.Add(1)
	return Credential{}, errSessionCore
}
func (f *scopedCoreFake) SessionToken(context.Context, SessionDispatchRequest) (Credential, error) {
	return f.token, nil
}
func (f *scopedCoreFake) SessionAdmit(ctx context.Context, in SessionDispatchRequest) error {
	f.admissions.Add(1)
	f.mu.Lock()
	hook := f.admit
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, in)
	}
	return nil
}

func (f *scopedCoreFake) setAdmit(hook func(context.Context, SessionDispatchRequest) error) {
	f.mu.Lock()
	f.admit = hook
	f.mu.Unlock()
}

func TestSessionGatewayChecksCoreAndCurrentRuntimeBeforeEveryDispatch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	in := dispatchFixture()
	var daemon string
	if err := pool.QueryRow(ctx, `INSERT INTO daemons(name,mode,repos_root) VALUES('synthetic','host','/workspace') RETURNING id::text`).Scan(&daemon); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(id,daemon_id,status,project_path,repo,spawning_account_id,mcp_launch_generation) VALUES($1,$2,'running','/workspace','synthetic',$3,1)`, in.RunnerSessionID, daemon, in.AccountID); err != nil {
		t.Fatal(err)
	}
	rows, _, err := buildGrants(in.RunnerSessionID, in.AccountID, Proof{SessionID: in.HumanSessionID}, []GrantSpec{{ConnectionID: in.ConnectionID, Name: "synthetic", URL: "https://resource.example/mcp", SessionScope: &in.MCPSessionScope}})
	if err != nil {
		t.Fatal(err)
	}
	in.MCPSessionScope = *rows[0].SessionScope
	if err := db.InsertMCPGrants(ctx, pool, rows); err != nil {
		t.Fatal(err)
	}
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { upstreamCalls.Add(1); w.WriteHeader(202) }))
	defer upstream.Close()
	core := &scopedCoreFake{}
	g := &Gateway{cfg: Config{DB: pool, Core: core}, client: upstream.Client()}
	st := &grantState{g: g}
	cred := Credential{URL: upstream.URL, session: &in}
	post := func() error { _, _, _, err := st.post(ctx, cred, []byte(`{}`), "", "", ""); return err }
	if err := post(); err != nil {
		t.Fatal(err)
	}
	if core.admissions.Load() != 1 || upstreamCalls.Load() != 1 {
		t.Fatalf("missing admission before dispatch: checks=%d sends=%d", core.admissions.Load(), upstreamCalls.Load())
	}
	core.setAdmit(func(context.Context, SessionDispatchRequest) error { return ErrConnectionGone })
	if err := post(); err == nil || upstreamCalls.Load() != 1 {
		t.Fatal("warm credential survived Core revocation")
	}
	core.setAdmit(func(context.Context, SessionDispatchRequest) error {
		_, err := pool.Exec(ctx, `UPDATE sessions SET mcp_launch_generation=2 WHERE id=$1`, in.RunnerSessionID)
		return err
	})
	if err := post(); err == nil || upstreamCalls.Load() != 1 {
		t.Fatal("generation change during Core admission reached upstream")
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET mcp_launch_generation=1 WHERE id=$1`, in.RunnerSessionID); err != nil {
		t.Fatal(err)
	}
	core.setAdmit(nil)
	if _, err := pool.Exec(ctx, `DELETE FROM session_mcp_grants WHERE session_id=$1`, in.RunnerSessionID); err != nil {
		t.Fatal(err)
	}
	if err := post(); err == nil || upstreamCalls.Load() != 1 {
		t.Fatal("old proposal or credential survived grant revocation")
	}
	if core.legacyCalls.Load() != 0 {
		t.Fatal("scoped dispatch used account fallback")
	}
}

func TestSessionGatewayScopedCredentialCannotFallBackOrChangeURL(t *testing.T) {
	in := dispatchFixture()
	gr := &db.MCPGrant{SessionID: in.RunnerSessionID, AccountID: in.AccountID, ConnectionID: in.ConnectionID, ProofKind: "session_id", ProofValue: in.HumanSessionID, SessionScope: &in.MCPSessionScope, URLSnapshot: "https://resource.example/mcp"}
	gr.TokenHash = hashToken("synthetic-grant")
	gr.SessionScope.GrantTokenHash = fmtHash(gr.TokenHash)
	core := &scopedCoreFake{token: Credential{URL: "https://resource.example/other", HeaderName: "Authorization", Value: "Bearer synthetic", PinPolicy: PinPolicyStrict}}
	g := &Gateway{cfg: Config{Core: core, Now: time.Now}}
	st := &grantState{g: g}
	if _, err := st.credential(context.Background(), gr); err == nil {
		t.Fatal("scoped credential changed its frozen resource URL")
	}
	if core.legacyCalls.Load() != 0 {
		t.Fatal("scoped credential used account fallback")
	}
}
func fmtHash(b []byte) string { return strings.ToLower(hex.EncodeToString(b)) }

func TestSessionProposalSinkFreezesOriginalGrant(t *testing.T) {
	pool := testPool(t)
	in := dispatchFixture()
	sink := NewProposalSink(pool, "", 50)
	p, _, err := sink.ProposeRow(context.Background(), ProposalRequest{SessionScope: &in.MCPSessionScope, AccountID: in.AccountID, SessionID: in.RunnerSessionID, ConnectionID: in.ConnectionID, ConnectionName: "synthetic", URLSnapshot: "https://resource.example/mcp", Tool: "send", ToolHash: "synthetic-hash", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if p.SessionScope == nil || *p.SessionScope != in.MCPSessionScope {
		t.Fatal("proposal lost the original grant scope")
	}
}

func newScopedGatewayHarness(t *testing.T) (*harness, grantHandle, SessionDispatchRequest, *scopedCoreFake) {
	t.Helper()
	h := newHarness(t, withSink(50, testRunnerURL))
	in := dispatchFixture()
	ctx := context.Background()
	var daemon string
	if err := h.pool.QueryRow(ctx, `INSERT INTO daemons(name,mode,repos_root) VALUES('synthetic','host','/workspace') RETURNING id::text`).Scan(&daemon); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO sessions(id,daemon_id,status,project_path,repo,spawning_account_id,mcp_launch_generation) VALUES($1,$2,'running','/workspace','synthetic',$3,1)`, in.RunnerSessionID, daemon, in.AccountID); err != nil {
		t.Fatal(err)
	}
	pins := map[string]ToolGrant{}
	for _, name := range []string{"echo", "send"} {
		pins[name] = ToolGrant{Mode: ModeAllow, Hash: h.up.hash(name)}
	}
	tokens, err := CreateGrants(ctx, h.pool, in.RunnerSessionID, in.AccountID, Proof{SessionID: in.HumanSessionID}, []GrantSpec{{ConnectionID: in.ConnectionID, Name: "synthetic", URL: h.up.url(), Tools: pins, SessionScope: &in.MCPSessionScope}})
	if err != nil {
		t.Fatal(err)
	}
	in.GrantTokenHash = fmtHash(hashToken(tokens[in.ConnectionID]))
	core := &scopedCoreFake{token: Credential{URL: h.up.url(), HeaderName: "Authorization", Value: "Bearer synthetic-access", PinPolicy: PinPolicyStrict}}
	h.gw.cfg.Core = core
	return h, grantHandle{sessionID: in.RunnerSessionID, connectionID: in.ConnectionID, name: "synthetic", token: tokens[in.ConnectionID]}, in, core
}

func TestSessionGatewayPublicFlowChecksInitializePagingAndWarmCalls(t *testing.T) {
	h, gh, in, core := newScopedGatewayHarness(t)
	h.up.pageSize = 1
	if names := toolNames(h.call(gh, "tools/list", map[string]any{})); strings.Join(names, ",") != "echo,send" {
		t.Fatalf("listing=%v", names)
	}
	// initialize + initialized notification + all three listing pages.
	if core.admissions.Load() != 5 {
		t.Fatalf("missing init/page admission: %d", core.admissions.Load())
	}
	before := core.admissions.Load()
	if r := h.callTool(gh, "echo", map[string]any{"msg": "synthetic"}); r.result() == nil || r.result()["isError"] == true {
		t.Fatalf("call failed: %s", r.raw)
	}
	if core.admissions.Load() <= before {
		t.Fatal("warm call bypassed live admission")
	}
	_, _, sent := h.up.snapshot()
	core.setAdmit(func(context.Context, SessionDispatchRequest) error { return ErrConnectionGone })
	h.callTool(gh, "echo", map[string]any{})
	if _, _, got := h.up.snapshot(); got != sent {
		t.Fatal("revoked warm credential sent tool call")
	}
	core.setAdmit(nil)
	if _, err := h.pool.Exec(context.Background(), `UPDATE sessions SET mcp_launch_generation=mcp_launch_generation+1 WHERE id=$1`, in.RunnerSessionID); err != nil {
		t.Fatal(err)
	}
	h.callTool(gh, "echo", map[string]any{})
	if _, _, got := h.up.snapshot(); got != sent {
		t.Fatal("changed runtime sent tool call")
	}
	if core.legacyCalls.Load() != 0 {
		t.Fatal("account fallback used")
	}
}

func TestSessionApprovedProposalKeepsOriginalScopeAndAddsApprover(t *testing.T) {
	h, _, in, core := newScopedGatewayHarness(t)
	approver := "00000000-0000-4000-8000-000000000155"
	core.setAdmit(func(ctx context.Context, got SessionDispatchRequest) error {
		if got.MCPSessionScope != in.MCPSessionScope || got.ActorSessionID != approver {
			t.Error("approval replaced original scope or lost approver")
		}
		return nil
	})
	call := ApprovedCall{SessionScope: &in.MCPSessionScope, Proof: Proof{AccountID: in.AccountID, SessionID: approver}, ConnectionID: in.ConnectionID, URLSnapshot: h.up.url(), Tool: "send", ToolHash: h.up.hash("send"), Arguments: json.RawMessage(`{}`)}
	if _, err := h.gw.ExecuteApproved(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	_, _, sent := h.up.snapshot()
	// Replacing the grant must never let the old proposal borrow its new authority.
	if _, err := h.pool.Exec(context.Background(), `UPDATE session_mcp_grants SET token_hash=decode(repeat('b',64),'hex'),session_scope=jsonb_set(session_scope,'{grant_token_hash}',to_jsonb(repeat('b',64))) WHERE session_id=$1`, in.RunnerSessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.gw.ExecuteApproved(context.Background(), call); err == nil {
		t.Fatal("old proposal borrowed reissued grant")
	}
	if _, _, got := h.up.snapshot(); got != sent {
		t.Fatal("stale approval reached upstream")
	}
}

func TestSessionGatewayWarmDispatchCost(t *testing.T) {
	h, gh, _, core := newScopedGatewayHarness(t)
	if r := h.callTool(gh, "echo", map[string]any{}); r.result() == nil || r.result()["isError"] == true {
		t.Fatalf("warmup failed: %s", r.raw)
	}
	before := core.admissions.Load()
	times := make([]time.Duration, 0, 100)
	for range 100 {
		start := time.Now()
		r := h.callTool(gh, "echo", map[string]any{})
		times = append(times, time.Since(start))
		if r.result() == nil || r.result()["isError"] == true {
			t.Fatalf("warm call failed: %s", r.raw)
		}
	}
	if core.admissions.Load()-before != 100 {
		t.Fatalf("warm calls skipped dispatch admission: %d", core.admissions.Load()-before)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	t.Logf("100 warm gateway HTTP tool calls, fake Core admission + real Runner database + HTTP upstream: p50=%s p95=%s", times[49], times[94])
}

func TestSessionScopedReissueChangesAuthorityButKeepsPinsAndBudget(t *testing.T) {
	h, gh, in, _ := newScopedGatewayHarness(t)
	// Isolate grant-replacement mechanics. Actual runtime transitions detach first;
	// lifecycle tests prove retained pins/budget, and 4b must restore them atomically.
	if _, err := h.pool.Exec(t.Context(), `ALTER TABLE sessions DISABLE TRIGGER mcp_binding_runtime_update`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, err := db.ConsumeMCPCall(ctx, h.pool, gh.sessionID, gh.connectionID); err != nil || !ok {
		t.Fatal(err)
	}
	old, err := db.MCPGrantByTokenHash(ctx, h.pool, hashToken(gh.token))
	if err != nil {
		t.Fatal(err)
	}
	next := in.MCPSessionScope
	next.LaunchGeneration++
	next.CredentialGeneration++
	tokens, err := ReissueGrants(ctx, h.pool, gh.sessionID, in.AccountID, Proof{SessionID: in.HumanSessionID}, []GrantSpec{{ConnectionID: gh.connectionID, Name: gh.name, URL: h.up.url(), SessionScope: &next, Tools: map[string]ToolGrant{}}})
	if err != nil {
		t.Fatal("scoped reissue failed", err)
	}
	updated, err := db.MCPGrantByTokenHash(ctx, h.pool, hashToken(tokens[gh.connectionID]))
	if err != nil {
		t.Fatal(err)
	}
	if updated.SessionScope == nil || updated.SessionScope.LaunchGeneration != next.LaunchGeneration || updated.SessionScope.CredentialGeneration != next.CredentialGeneration || updated.SessionScope.GrantTokenHash == in.GrantTokenHash {
		t.Fatal("reissue restored stale scope authority")
	}
	if updated.CallsUsed != old.CallsUsed || len(updated.Tools) != len(old.Tools) {
		t.Fatal("reissue discarded spent budget or authorized pins")
	}
	if live, err := db.MCPScopedGrantLive(ctx, h.pool, in.MCPSessionScope, gh.connectionID); err != nil || live {
		t.Fatal("previous issuance remained live", err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE sessions SET mcp_launch_generation=$2 WHERE id=$1`, gh.sessionID, next.LaunchGeneration); err != nil {
		t.Fatal(err)
	}
	if live, err := db.MCPScopedGrantLive(ctx, h.pool, *updated.SessionScope, gh.connectionID); err != nil || !live {
		t.Fatal("fresh reissue did not bind current runtime", err)
	}
}

type accountOnlyCoreFake struct{ calls atomic.Int64 }

func (f *accountOnlyCoreFake) Token(context.Context, Proof, string) (Credential, error) {
	f.calls.Add(1)
	return Credential{URL: "https://resource.example/mcp", HeaderName: "Authorization", Value: "Bearer synthetic-account-access", PinPolicy: PinPolicyStrict}, nil
}
func TestSessionGatewayUnwiredCoreNeverUsesAccountFallback(t *testing.T) {
	in := dispatchFixture()
	raw := "synthetic-grant"
	in.GrantTokenHash = fmtHash(hashToken(raw))
	gr := &db.MCPGrant{SessionScope: &in.MCPSessionScope, AccountID: in.AccountID, SessionID: in.RunnerSessionID, ConnectionID: in.ConnectionID, ProofKind: "session_id", ProofValue: in.HumanSessionID, TokenHash: hashToken(raw), URLSnapshot: "https://resource.example/mcp"}
	core := &accountOnlyCoreFake{}
	st := &grantState{g: &Gateway{cfg: Config{Core: core, Now: time.Now}}}
	if _, err := st.credential(context.Background(), gr); err == nil || core.calls.Load() != 0 {
		t.Fatal("unwired scoped client used account credentials")
	}
}

func TestSessionGatewayGoneAdmissionPurgesCachedCredential(t *testing.T) {
	for _, gone := range []bool{true, false} {
		in := dispatchFixture()
		core := &scopedCoreFake{}
		if gone {
			core.setAdmit(func(context.Context, SessionDispatchRequest) error { return ErrConnectionGone })
		} else {
			core.setAdmit(func(context.Context, SessionDispatchRequest) error { return errSessionCore })
		}
		sends := 0
		g := &Gateway{cfg: Config{Core: core}, client: &http.Client{Transport: scopedTransportFunc(func(*http.Request) (*http.Response, error) {
			sends++
			return nil, errors.New("unexpected upstream dispatch")
		})}}
		cred := Credential{URL: "https://resource.example/mcp", session: &in}
		st := &grantState{g: g, cred: &cred, credUntil: time.Now().Add(time.Minute)}
		if _, _, _, err := st.post(context.Background(), cred, []byte(`{}`), "", "", ""); err == nil {
			t.Fatal("admission refusal ignored")
		}
		if sends != 0 {
			t.Fatal("refusal reached upstream")
		}
		if gone && st.cred != nil {
			t.Fatal("revoked credential remains cached")
		}
		if !gone && st.cred == nil {
			t.Fatal("transient admission unnecessarily purged credential")
		}
	}
}
