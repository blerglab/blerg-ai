package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livemcp"
	"github.com/blerglab/blerg-ai/core/internal/api"
	coreidentity "github.com/blerglab/blerg-ai/core/internal/identity"
	corelive "github.com/blerglab/blerg-ai/core/internal/livedata"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLiveDataOwnerSettings(t *testing.T) {
	e := newMCPEnv(t)
	token, err := e.idSvc.MintHumanAccessTokenForSession(context.Background(), e.acctA, "blerg-core", e.sidA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("GET", e.srv.URL+"/api/settings/live-data", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		t.Fatalf("live polling Settings missing: %d", response.StatusCode)
	}
}

// newLiveApprovalEnv uses the real Core router, signing keys, human sessions and
// database. The external authority epoch is a trusted fixture, never body data.
func newLiveApprovalEnv(t *testing.T) *mcpEnv {
	e := newMCPEnv(t)
	e.srv.Close()
	deps := e.deps
	deps.LiveDataAuthorityEpoch = func() int64 { return 1 }
	deps.LiveDataNow = func() time.Time { return time.Now().Add(-2 * time.Second) }
	deps.LiveDataRunnerIdentity = func(*http.Request) (ld.RunnerIdentity, error) {
		return ld.RunnerIdentity{Installation: "00000000-0000-4000-8000-000000000001", Runner: "00000000-0000-4000-8000-000000000002"}, nil
	}
	listing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		config := liveConfig(e, "00000000-0000-4000-8000-000000000001", 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"definition": config.ToolDefinition, "read_only": true})
	}))
	t.Cleanup(listing.Close)
	deps.LiveDataRunnerURL = listing.URL
	e.deps = deps
	e.srv = httptest.NewServer(failurelog.Default.HTTP("core", api.NewRouter(deps)))
	t.Cleanup(e.srv.Close)
	return e
}
func liveConfig(e *mcpEnv, binding string, revision int64) ld.Revision {
	r := ld.Revision{Card: &ld.CardSpec{Kind: "stat"}, Scope: ld.Scope{Installation: "00000000-0000-4000-8000-000000000001", Runner: "00000000-0000-4000-8000-000000000002", Owner: e.acctA, Binding: binding, Connection: e.connA, Tool: "quotes", Hash: strings.Repeat("a", 64), Revision: revision, AuthorityEpoch: 999}, Arguments: json.RawMessage(`{"symbol":"NVDA"}`), Mapping: livemcp.Mapping{RowsPath: "/quotes", Fields: []livemcp.Field{{Name: "time", Path: "/time", Type: "timestamp", Format: "rfc3339", Timezone: "UTC"}, {Name: "value", Path: "/close", Type: "number"}}, Keys: []string{"time"}}, IntervalSeconds: 60, ExpiresAt: time.Now().UTC().Add(24 * time.Hour), OwnerOnly: true}
	r.ToolDefinition = json.RawMessage(`{"description":"Market quotes","inputSchema":{"additionalProperties":false,"properties":{"limit":{"type":"integer"},"symbol":{"type":"string"}},"required":["symbol"],"type":"object"},"name":"quotes"}`)
	sum := sha256.Sum256(r.ToolDefinition)
	r.Scope.Hash = hex.EncodeToString(sum[:])
	return r
}
func liveToken(t *testing.T, e *mcpEnv, owner, sid, audience string) string {
	t.Helper()
	token, err := e.idSvc.MintHumanAccessTokenForSession(context.Background(), owner, audience, sid)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func liveCall(t *testing.T, e *mcpEnv, method, path, token string, body any) (int, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := e.srv.Client()
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data
}
func proposeLive(t *testing.T, e *mcpEnv, config ld.Revision) ld.Proposal {
	t.Helper()
	code, raw := liveCall(t, e, "POST", "/internal/live-data/proposals", testInternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "configuration": config, "ask_id": liveAskID(config)})
	if code != 200 {
		t.Fatalf("proposal status %d", code)
	}
	var p ld.Proposal
	if json.Unmarshal(raw, &p) != nil {
		t.Fatal("invalid proposal")
	}
	return p
}

func TestLiveDataApprovalGuards(t *testing.T) {
	e := newLiveApprovalEnv(t)
	ctx := context.Background()
	owner := liveToken(t, e, e.acctA, e.sidA, "blerg-runner")
	core := liveToken(t, e, e.acctA, e.sidA, "blerg-core")
	other := liveToken(t, e, e.acctB, e.sidB, "blerg-runner")
	binding := "00000000-0000-4000-8000-000000000514"
	p := proposeLive(t, e, liveConfig(e, binding, 1))
	if p.Configuration.Scope.AuthorityEpoch != 1 {
		t.Fatal("caller chose authority epoch")
	}
	decision := ld.Decision{AccountID: e.acctA, AccessToken: owner, BindingID: binding, Revision: 1, Digest: p.Digest, AskID: p.AskID, Approve: true}
	for name, token := range map[string]string{"service key only": "", "wrong person": other, "forged": "forged-person-credential-canary", "wrong audience": core, "agent": e.agentA} {
		t.Run(name, func(t *testing.T) {
			bad := decision
			bad.AccessToken = token
			code, raw := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, bad)
			if code != 404 || bytes.Contains(raw, []byte("credential-canary")) {
				t.Fatalf("credential refusal %d", code)
			}
		})
	}
	policy := ld.PolicyWrite{AccountID: e.acctA, AccessToken: owner, BindingID: binding, Revision: 1, Digest: p.Digest, Mode: "allow"}
	badPolicy := policy
	badPolicy.AccessToken = ""
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/policy", testInternalKey, badPolicy); code != 403 {
		t.Fatal("service key raised policy", code)
	}
	if code, _ := livePolicy(t, e, policy); code != 200 {
		t.Fatal("owner policy write", code)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 409 {
		t.Fatal("unsafe host approved without consent", code)
	}
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", core, map[string]any{"unsafe_host_opt_in": true}); code != 400 {
		t.Fatal("warning acknowledgement bypass", code)
	}
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", core, map[string]any{"unsafe_host_opt_in": true, "acknowledgement": ld.UnsafeHostWarning}); code != 200 {
		t.Fatal("owner consent", code)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 200 {
		t.Fatal("owner approval", code)
	}
	var approved time.Time
	if err := e.pool.QueryRow(ctx, `SELECT approved_at FROM live_data_grants WHERE binding_id=$1`, binding).Scan(&approved); err != nil {
		t.Fatal(err)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 200 {
		t.Fatal("exact receipt replay", code)
	}
	var after time.Time
	var grants int
	if err := e.pool.QueryRow(ctx, `SELECT count(*),min(approved_at) FROM live_data_grants WHERE binding_id=$1`, binding).Scan(&grants, &after); err != nil || grants != 1 || !after.Equal(approved) {
		t.Fatal("replay renewed authority")
	}
	conflict := decision
	conflict.AskID = "00000000-0000-4000-8000-000000000602"
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, conflict); code != 409 {
		t.Fatal("conflicting replay", code)
	}
	// Disabling consent revokes the durable grant; re-enabling cannot resurrect it.
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", core, map[string]any{"unsafe_host_opt_in": false}); code != 200 {
		t.Fatal("opt out", code)
	}
	var revoked bool
	if err := e.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM live_data_grants WHERE binding_id=$1`, binding).Scan(&revoked); err != nil || !revoked {
		t.Fatal("opt out failed to revoke")
	}
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", core, map[string]any{"unsafe_host_opt_in": true, "acknowledgement": ld.UnsafeHostWarning}); code != 200 {
		t.Fatal("re-enable consent", code)
	}
	if err := e.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM live_data_grants WHERE binding_id=$1`, binding).Scan(&revoked); err != nil || !revoked {
		t.Fatal("consent resurrected grant", err)
	}
	_ = proposeLive(t, e, liveConfig(e, binding, 2))
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 409 {
		t.Fatal("stale revision approved", code)
	}
	p2 := proposeLive(t, e, liveConfig(e, "00000000-0000-4000-8000-000000000515", 1))
	lower := ld.PolicyWrite{AccountID: e.acctA, BindingID: p2.Configuration.Scope.Binding, Revision: 1, Digest: p2.Digest, Mode: "off"}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/policy", testInternalKey, lower); code != 200 {
		t.Fatal("lenient lowering refused", code)
	}
}

func TestLiveDataSecretArgumentsNeverPersist(t *testing.T) {
	e := newLiveApprovalEnv(t)
	config := liveConfig(e, "00000000-0000-4000-8000-000000000516", 1)
	const canary = "credential-canary-never-store"
	for _, args := range []string{`{"nested":{"aUtH_orization":"Bearer ` + canary + `"}}`, `{"url":"https://market.example/quotes?api_key=` + canary + `"}`, `{"symbol":"Bearer ` + canary + `"}`, `{"symbol":"xoxb-` + canary + `"}`, `{"symbol":"AKIA` + canary + `"}`, `{"symbol":"glpat-` + canary + `"}`, `{"symbol":"token ` + canary + `"}`} {
		config.Arguments = json.RawMessage(args)
		code, body := liveCall(t, e, "POST", "/internal/live-data/proposals", testInternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "configuration": config, "ask_id": liveAskID(config)})
		if code != 400 || bytes.Contains(body, []byte(canary)) {
			t.Fatal("secret argument not safely refused", code)
		}
	}
	var count int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM live_data_proposals`).Scan(&count); err != nil || count != 0 {
		t.Fatal("secret proposal persisted")
	}
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM live_data_approval_audit WHERE row_to_json(live_data_approval_audit)::text LIKE $1`, "%"+canary+"%").Scan(&count); err != nil || count != 0 {
		t.Fatal("canary in audit")
	}
}

func TestLiveDataSafeRuntimeStillRequiresPolicy(t *testing.T) {
	e := newLiveApprovalEnv(t)
	e.srv.Close()
	deps := e.deps
	deps.LiveDataSafeRuntime = true
	e.srv = httptest.NewServer(failurelog.Default.HTTP("core", api.NewRouter(deps)))
	t.Cleanup(e.srv.Close)
	owner := liveToken(t, e, e.acctA, e.sidA, "blerg-runner")
	p := proposeLive(t, e, liveConfig(e, "00000000-0000-4000-8000-000000000517", 1))
	d := ld.Decision{AccountID: e.acctA, AccessToken: owner, BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, AskID: p.AskID, Approve: true}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, d); code != 409 {
		t.Fatal("binding approval promoted policy", code)
	}
	policy := ld.PolicyWrite{AccountID: e.acctA, AccessToken: owner, BindingID: d.BindingID, Revision: 1, Digest: p.Digest, Mode: "allow"}
	bad := policy
	bad.Digest = strings.Repeat("f", 64)
	if code, _ := livePolicy(t, e, bad); code != 409 {
		t.Fatal("policy digest not bound", code)
	}
	if code, _ := livePolicy(t, e, policy); code != 200 {
		t.Fatal("owner allow", code)
	}
	forged := d
	forged.Digest = strings.Repeat("f", 64)
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, forged); code != 409 {
		t.Fatal("approval digest not bound", code)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, d); code != 200 {
		t.Fatal("safe cluster required desktop opt-in", code)
	}
	var sid string
	if err := e.pool.QueryRow(t.Context(), `SELECT approved_sid FROM live_data_grants WHERE binding_id=$1`, d.BindingID).Scan(&sid); err != nil || sid != e.sidA {
		t.Fatal("Core failed to record the verified login", err)
	}
	// Losing allow irreversibly revokes, even if policy later returns to allow.
	policy.Mode = "off"
	policy.AccessToken = ""
	if code, _ := livePolicy(t, e, policy); code != 200 {
		t.Fatal("lenient lower", code)
	}
	policy.Mode = "allow"
	policy.AccessToken = owner
	if code, _ := livePolicy(t, e, policy); code != 200 {
		t.Fatal("raise again", code)
	}
	var revoked bool
	if err := e.pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM live_data_grants WHERE binding_id=$1`, d.BindingID).Scan(&revoked); err != nil || !revoked {
		t.Fatal("policy resurrected grant", err)
	}
}

func TestLiveDataApprovalRequiresLiveOwner(t *testing.T) {
	for name, sql := range map[string]string{
		"revoked login":           `UPDATE human_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`,
		"expired login":           `UPDATE human_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`,
		"disabled owner":          `UPDATE accounts SET disabled_at=clock_timestamp() WHERE id=$1`,
		"password reset required": `UPDATE accounts SET must_change_password=true WHERE id=$1`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newLiveApprovalEnv(t)
			owner := liveToken(t, e, e.acctA, e.sidA, "blerg-runner")
			p := proposeLive(t, e, liveConfig(e, "00000000-0000-4000-8000-000000000518", 1))
			if _, err := e.pool.Exec(t.Context(), sql, e.acctA); err != nil {
				t.Fatal(err)
			}
			decision := ld.Decision{AccountID: e.acctA, AccessToken: owner, BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, AskID: p.AskID, Approve: true}
			if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 404 {
				t.Fatal("dead owner approved", code)
			}
			policy := ld.PolicyWrite{AccountID: e.acctA, AccessToken: owner, BindingID: decision.BindingID, Revision: 1, Digest: p.Digest, Mode: "allow"}
			if code, _ := liveCall(t, e, "POST", "/internal/live-data/policy", testInternalKey, policy); code != 403 {
				t.Fatal("dead owner raised policy", code)
			}
		})
	}
}

func TestLiveDataApprovalSessionFence(t *testing.T) {
	e := newLiveApprovalEnv(t)
	e.idSvc.SetSessionLockTimeout(10 * time.Millisecond)
	first, err := e.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(t.Context()) }()
	if err = e.idSvc.LockLiveHumanApprovalTx(t.Context(), first, e.acctA, e.sidA); err != nil {
		t.Fatal(err)
	}
	second, err := e.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = e.idSvc.LockLiveHumanApprovalTx(t.Context(), second, e.acctA, e.sidA)
	_ = second.Rollback(t.Context())
	if !errors.Is(err, coreidentity.ErrSessionBusy) {
		t.Fatal("approval bypassed logout/password-reset fence", err)
	}
	if _, err = first.Exec(t.Context(), `UPDATE human_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, e.acctA); err != nil {
		t.Fatal(err)
	}
	if err = first.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := e.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = after.Rollback(t.Context()) }()
	if err = e.idSvc.LockLiveHumanApprovalTx(t.Context(), after, e.acctA, e.sidA); !errors.Is(err, coreidentity.ErrSessionRevoked) {
		t.Fatal("approval trusted pre-fence login", err)
	}
}

func TestLiveDataRefusalAuditRequiresAuthentication(t *testing.T) {
	e := newLiveApprovalEnv(t)
	count := func() int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM live_data_approval_audit`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, path := range []string{"/internal/live-data/proposals", "/internal/live-data/proposal", "/internal/live-data/approve", "/internal/live-data/policy", "/internal/live-data/receipt", "/internal/live-data/confirm", "/internal/live-data/cancel", "/api/settings/live-data", "/api/settings/live-data/policy"} {
		methods := []string{"POST"}
		if strings.HasPrefix(path, "/api/") {
			methods = []string{"PUT"}
			if path == "/api/settings/live-data" {
				methods = append(methods, "GET")
			}
		}
		for _, method := range methods {
			for _, token := range []string{"", "forged-credential-canary"} {
				t.Run(method+" "+path+" "+map[bool]string{true: "missing", false: "forged"}[token == ""], func(t *testing.T) {
					before := count()
					for range 3 {
						body := map[string]any{"unsafe_host_opt_in": true}
						if strings.HasSuffix(path, "/policy") {
							body = map[string]any{"mode": "allow"}
						}
						code, _ := liveCall(t, e, method, path, token, body)
						if code < 400 {
							t.Fatal("unauthenticated request accepted", code)
						}
					}
					if after := count(); after != before {
						t.Fatalf("unauthenticated refusals grew audit: %d -> %d", before, after)
					}
				})
			}
		}
	}
	// Successful component authentication still audits a refused decision.
	before := count()
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, ld.Decision{AskID: "00000000-0000-4000-8000-000000000620"}); code != 404 {
		t.Fatal("component refusal", code)
	}
	if count() != before+1 {
		t.Fatal("authenticated component refusal lost audit")
	}
	// A verified person still gets a durable refusal when omitting the warning.
	token := liveToken(t, e, e.acctA, e.sidA, "blerg-core")
	before = count()
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", token, map[string]any{"unsafe_host_opt_in": true}); code != 400 {
		t.Fatal("person refusal", code)
	}
	if count() != before+1 {
		t.Fatal("authenticated person refusal lost audit")
	}
}

func liveAskID(config ld.Revision) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", config.Scope.Binding, config.Scope.Revision)))
	h := hex.EncodeToString(sum[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
func livePolicy(t *testing.T, e *mcpEnv, policy ld.PolicyWrite) (int, []byte) {
	if policy.Mode != "allow" {
		return liveCall(t, e, "POST", "/internal/live-data/policy", testInternalKey, policy)
	}
	return liveCall(t, e, "PUT", "/api/settings/live-data/policy", liveToken(t, e, e.acctA, e.sidA, "blerg-core"), policy)
}

func TestLiveDataRegisteredAskAndRunner(t *testing.T) {
	e := newLiveApprovalEnv(t)
	config := liveConfig(e, "00000000-0000-4000-8000-000000000721", 1)
	config.Scope.Installation = "00000000-0000-4000-8000-000000000999"
	config.Scope.Runner = config.Scope.Installation
	p := proposeLive(t, e, config)
	if p.Configuration.Scope.Runner == config.Scope.Runner || p.Configuration.Scope.Installation == config.Scope.Installation {
		t.Fatal("request selected Runner identity")
	}
	if code, _ := livePolicy(t, e, ld.PolicyWrite{AccountID: e.acctA, BindingID: config.Scope.Binding, Revision: 1, Digest: p.Digest, Mode: "allow"}); code != 200 {
		t.Fatal("policy setup", code)
	}
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", liveToken(t, e, e.acctA, e.sidA, "blerg-core"), map[string]any{"unsafe_host_opt_in": true, "acknowledgement": ld.UnsafeHostWarning}); code != 200 {
		t.Fatal("consent setup", code)
	}
	d := ld.Decision{AccountID: e.acctA, AccessToken: liveToken(t, e, e.acctA, e.sidA, "blerg-runner"), BindingID: config.Scope.Binding, Revision: 1, Digest: p.Digest, AskID: "00000000-0000-4000-8000-000000000722", Approve: true}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, d); code != 409 {
		t.Fatal("unregistered ask approved", code)
	}
	policy := ld.PolicyWrite{AccountID: e.acctA, AccessToken: d.AccessToken, BindingID: d.BindingID, Revision: 1, Digest: p.Digest, Mode: "allow"}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/policy", testInternalKey, policy); code != 403 {
		t.Fatal("internal policy promoted", code)
	}
	e.srv.Close()
	deps := e.deps
	deps.LiveDataRunnerIdentity = nil
	e.srv = httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(e.srv.Close)
	config.Scope.Binding = "00000000-0000-4000-8000-000000000723"
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/proposals", testInternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "configuration": config, "ask_id": liveAskID(config)}); code != 403 {
		t.Fatal("shared key impersonated Runner", code)
	}
}
func TestLiveDataApprovalNeedsPostProposalToken(t *testing.T) {
	e := newLiveApprovalEnv(t)
	token := liveToken(t, e, e.acctA, e.sidA, "blerg-runner")
	e.srv.Close()
	deps := e.deps
	deps.LiveDataNow = func() time.Time { return time.Now().Add(2 * time.Second) }
	e.srv = httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(e.srv.Close)
	p := proposeLive(t, e, liveConfig(e, "00000000-0000-4000-8000-000000000724", 1))
	d := ld.Decision{AccountID: e.acctA, AccessToken: token, BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, AskID: p.AskID, Approve: true}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, d); code != 401 {
		t.Fatal("captured pre-proposal token accepted", code)
	}
}
func TestLiveDataRequiresFreshReadOnlyListing(t *testing.T) {
	e := newLiveApprovalEnv(t)
	readOnly := false
	calls := 0
	invalidate := false
	listing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		tx, err := e.pool.Begin(r.Context())
		if err != nil {
			t.Error(err)
			w.WriteHeader(503)
			return
		}
		var available bool
		if err = tx.QueryRow(r.Context(), `SELECT pg_try_advisory_xact_lock(hashtextextended('human_sessions:' || $1::text,0))`, e.acctA).Scan(&available); err != nil || !available {
			t.Error("listing holds human session fence", err)
		}
		if _, err = tx.Exec(r.Context(), `SELECT 1 FROM live_data_proposals FOR UPDATE NOWAIT`); err != nil {
			t.Error("listing holds proposal lock", err)
		}
		_ = tx.Rollback(r.Context())
		if invalidate {
			if _, err = e.pool.Exec(r.Context(), `UPDATE live_data_proposal_heads SET revision=revision+1`); err != nil {
				t.Error(err)
			}
		}

		config := liveConfig(e, "00000000-0000-4000-8000-000000000001", 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"definition": config.ToolDefinition, "read_only": readOnly})
	}))
	defer listing.Close()
	e.srv.Close()
	deps := e.deps
	deps.LiveDataRunnerURL = listing.URL
	e.srv = httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(e.srv.Close)
	config := liveConfig(e, "00000000-0000-4000-8000-000000000725", 1)
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/proposals", testInternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "configuration": config, "ask_id": liveAskID(config)}); code != 409 {
		t.Fatal("non-read-only proposal persisted", code)
	}
	readOnly = true
	p := proposeLive(t, e, config)
	if code, _ := livePolicy(t, e, ld.PolicyWrite{AccountID: e.acctA, BindingID: config.Scope.Binding, Revision: 1, Digest: p.Digest, Mode: "allow"}); code != 200 {
		t.Fatal("policy", code)
	}
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", liveToken(t, e, e.acctA, e.sidA, "blerg-core"), map[string]any{"unsafe_host_opt_in": true, "acknowledgement": ld.UnsafeHostWarning}); code != 200 {
		t.Fatal("consent", code)
	}
	readOnly = false
	d := ld.Decision{AccountID: e.acctA, AccessToken: liveToken(t, e, e.acctA, e.sidA, "blerg-runner"), BindingID: config.Scope.Binding, Revision: 1, Digest: p.Digest, AskID: p.AskID, Approve: true}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, d); code != 409 {
		t.Fatal("cached read-only claim approved", code)
	}
	if calls != 3 {
		t.Fatal("listing was cached", calls)
	}
	readOnly = true
	invalidate = true
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, d); code != 409 {
		t.Fatal("revision changed during listing approved", code)
	}
	var grants int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM live_data_grants`).Scan(&grants); err != nil || grants != 0 {
		t.Fatal("stale preflight created grant", grants, err)
	}

}
func TestLiveDataDecisionReconciliation(t *testing.T) {
	e := newLiveApprovalEnv(t)
	owner := liveToken(t, e, e.acctA, e.sidA, "blerg-runner")
	core := liveToken(t, e, e.acctA, e.sidA, "blerg-core")
	p := proposeLive(t, e, liveConfig(e, "00000000-0000-4000-8000-000000000726", 1))
	d := ld.Decision{AccountID: e.acctA, AccessToken: owner, BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, AskID: p.AskID, Approve: true}
	if code, _ := livePolicy(t, e, ld.PolicyWrite{AccountID: e.acctA, BindingID: d.BindingID, Revision: 1, Digest: p.Digest, Mode: "allow"}); code != 200 {
		t.Fatal("policy", code)
	}
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", core, map[string]any{"unsafe_host_opt_in": true, "acknowledgement": ld.UnsafeHostWarning}); code != 200 {
		t.Fatal("consent", code)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, d); code != 200 {
		t.Fatal("approval", code)
	}
	var committed, revoked bool
	state := func() {
		t.Helper()
		if err := e.pool.QueryRow(t.Context(), `SELECT runner_committed,revoked_at IS NOT NULL FROM live_data_grants WHERE binding_id=$1`, d.BindingID).Scan(&committed, &revoked); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (corelive.Postgres{Pool: e.pool}).Current(t.Context(), d.BindingID, 1); err == nil {
		t.Fatal("unconfirmed approval became dispatch authority")
	}
	state()
	if committed || revoked {
		t.Fatal("Core committed approval activated prematurely")
	}
	wrong := d
	wrong.Digest = strings.Repeat("f", 64)
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/receipt", testInternalKey, wrong); code != 404 {
		t.Fatal("receipt substituted another subject", code)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/receipt", testInternalKey, d); code != 200 {
		t.Fatal("lost reply reconciliation", code)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/confirm", testInternalKey, d); code != 200 {
		t.Fatal("confirmation", code)
	}
	if _, err := (corelive.Postgres{Pool: e.pool}).Current(t.Context(), d.BindingID, 1); err != nil {
		t.Fatal("confirmed grant unavailable", err)
	}
	state()
	if !committed || revoked {
		t.Fatal("confirmation failed")
	}
	// Pending revision keeps the approved old revision alive.
	_ = proposeLive(t, e, liveConfig(e, d.BindingID, 2))
	state()
	if revoked {
		t.Fatal("pending edit revoked approved revision")
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/cancel", testInternalKey, d); code != 200 {
		t.Fatal("cancel after approval", code)
	}
	state()
	if !revoked {
		t.Fatal("cancel failed to revoke")
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/confirm", testInternalKey, d); code != 409 {
		t.Fatal("confirmation resurrected canceled revision", code)
	}

	next := proposeLive(t, e, liveConfig(e, "00000000-0000-4000-8000-000000000729", 1))
	d.BindingID = next.Configuration.Scope.Binding
	d.Digest = next.Digest
	d.AskID = next.AskID
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, d); code != 200 {
		t.Fatal("second approval", code)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/confirm", testInternalKey, d); code != 200 {
		t.Fatal("second confirmation", code)
	}
	d.Approve = false
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, d); code != 200 {
		t.Fatal("owner decline after approval", code)
	}
	state()
	if !revoked {
		t.Fatal("owner decline left grant active")
	}
}

func TestLiveDataConsentMustBeRenewedForActivationEpoch(t *testing.T) {
	e := newLiveApprovalEnv(t)
	core := liveToken(t, e, e.acctA, e.sidA, "blerg-core")
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", core, map[string]any{"unsafe_host_opt_in": true, "acknowledgement": ld.UnsafeHostWarning}); code != 200 {
		t.Fatal("initial consent", code)
	}
	e.srv.Close()
	deps := e.deps
	deps.LiveDataAuthorityEpoch = func() int64 { return 2 }
	e.srv = httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(e.srv.Close)
	code, raw := liveCall(t, e, "GET", "/api/settings/live-data", core, nil)
	var view struct {
		OptIn bool `json:"unsafe_host_opt_in"`
	}
	_ = json.Unmarshal(raw, &view)
	if code != 200 || view.OptIn {
		t.Fatal("activation reused old machine consent", code)
	}
	p := proposeLive(t, e, liveConfig(e, "00000000-0000-4000-8000-000000000727", 1))
	if code, _ := livePolicy(t, e, ld.PolicyWrite{AccountID: e.acctA, BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, Mode: "allow"}); code != 200 {
		t.Fatal("policy", code)
	}
	d := ld.Decision{AccountID: e.acctA, AccessToken: liveToken(t, e, e.acctA, e.sidA, "blerg-runner"), BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, AskID: p.AskID, Approve: true}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, d); code != 409 {
		t.Fatal("old consent approved activation", code)
	}
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", core, map[string]any{"unsafe_host_opt_in": true, "acknowledgement": ld.UnsafeHostWarning}); code != 200 {
		t.Fatal("new consent", code)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, d); code != 200 {
		t.Fatal("fresh consent refused", code)
	}
}

func TestLiveDataListingFreshnessBound(t *testing.T) {
	e := newLiveApprovalEnv(t)
	p := proposeLive(t, e, liveConfig(e, "00000000-0000-4000-8000-000000000730", 1))
	if code, _ := livePolicy(t, e, ld.PolicyWrite{AccountID: e.acctA, BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, Mode: "allow"}); code != 200 {
		t.Fatal("policy", code)
	}
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", liveToken(t, e, e.acctA, e.sidA, "blerg-core"), map[string]any{"unsafe_host_opt_in": true, "acknowledgement": ld.UnsafeHostWarning}); code != 200 {
		t.Fatal("consent", code)
	}
	e.srv.Close()
	deps := e.deps
	base := time.Now()
	clockReads := 0
	deps.LiveDataNow = func() time.Time { clockReads++; return base.Add(time.Duration(clockReads-1) * 11 * time.Second) }
	e.srv = httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(e.srv.Close)
	d := ld.Decision{AccountID: e.acctA, AccessToken: liveToken(t, e, e.acctA, e.sidA, "blerg-runner"), BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, AskID: p.AskID, Approve: true}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, d); code != 409 {
		t.Fatal("stale listing approved", code)
	}
	var grants int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM live_data_grants`).Scan(&grants); err != nil || grants != 0 {
		t.Fatal("stale listing created grant", grants, err)
	}
}

func TestLiveDataDesktopRequiresSettingsConsent(t *testing.T) {
	e := newLiveApprovalEnv(t)
	e.srv.Close()
	deps := e.deps
	deps.LiveDataSafeRuntime = true
	deps.LiveDataDesktop = true
	e.srv = httptest.NewServer(failurelog.Default.HTTP("core", api.NewRouter(deps)))
	t.Cleanup(e.srv.Close)
	p := proposeLive(t, e, liveConfig(e, "00000000-0000-4000-8000-000000000719", 1))
	if code, _ := livePolicy(t, e, ld.PolicyWrite{AccountID: e.acctA, BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, Mode: "allow"}); code != 200 {
		t.Fatal("policy", code)
	}
	owner := liveToken(t, e, e.acctA, e.sidA, "blerg-runner")
	decision := ld.Decision{AccountID: e.acctA, AccessToken: owner, BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, AskID: p.AskID, Approve: true}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 409 {
		t.Fatal("desktop bypassed Settings consent with safe runtime", code)
	}
	core := liveToken(t, e, e.acctA, e.sidA, "blerg-core")
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", core, map[string]any{"unsafe_host_opt_in": true, "acknowledgement": ld.UnsafeHostWarning}); code != 200 {
		t.Fatal("consent", code)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 200 {
		t.Fatal("explicit Settings consent refused", code)
	}
}

func TestLiveDataPostureBeforeLocksAndOwnerConsent(t *testing.T) {
	e := newLiveApprovalEnv(t)
	safe := true
	calls := 0
	deps := e.deps
	deps.LiveDataPosture = func(ctx context.Context) (bool, error) {
		calls++
		tx, err := e.pool.Begin(ctx)
		if err != nil {
			return false, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var unlocked bool
		if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('human_sessions:' || $1::text,0))`, e.acctA).Scan(&unlocked); err != nil || !unlocked {
			t.Error("posture held account session lock", err)
			return false, errors.New("locked")
		}
		if _, err = tx.Exec(ctx, `SELECT 1 FROM live_data_proposals FOR UPDATE NOWAIT`); err != nil {
			t.Error("posture held proposal lock", err)
			return false, err
		}
		return safe, nil
	}
	e.deps = deps
	e.srv.Close()
	e.srv = httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(e.srv.Close)
	p := proposeLive(t, e, liveConfig(e, "00000000-0000-4000-8000-000000000734", 1))
	if code, _ := livePolicy(t, e, ld.PolicyWrite{AccountID: e.acctA, BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, Mode: "allow"}); code != 200 {
		t.Fatal(code)
	}
	decision := ld.Decision{AccountID: e.acctA, AccessToken: liveToken(t, e, e.acctA, e.sidA, "blerg-runner"), BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, AskID: p.AskID, Approve: true}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 200 || calls != 1 {
		t.Fatal("verified safe cluster required unsafe-host consent", code, calls)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 200 || calls != 1 {
		t.Fatal("exact receipt replay performed another posture call", code, calls)
	}
	safe = false
	p = proposeLive(t, e, liveConfig(e, decision.BindingID, 2))
	decision.Revision = 2
	decision.Digest = p.Digest
	decision.AskID = p.AskID
	decision.AccessToken = liveToken(t, e, e.acctA, e.sidA, "blerg-runner")
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 409 {
		t.Fatal("host runtime approved without Settings consent", code)
	}
	core := liveToken(t, e, e.acctA, e.sidA, "blerg-core")
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", core, map[string]any{"unsafe_host_opt_in": true, "acknowledgement": ld.UnsafeHostWarning}); code != 200 {
		t.Fatal(code)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 200 {
		t.Fatal("explicit machine consent did not admit unsafe revision", code)
	}
}
func TestLiveDataPostureFreshnessAndUnavailability(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			e := newLiveApprovalEnv(t)
			p := proposeLive(t, e, liveConfig(e, "00000000-0000-4000-8000-000000000735", 1))
			if code, _ := livePolicy(t, e, ld.PolicyWrite{AccountID: e.acctA, BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, Mode: "allow"}); code != 200 {
				t.Fatal(code)
			}
			deps := e.deps
			base := time.Now()
			reads := 0
			deps.LiveDataNow = func() time.Time {
				reads++
				if reads >= 3 {
					return base.Add(6 * time.Second)
				}
				return base
			}
			deps.LiveDataPosture = func(context.Context) (bool, error) {
				if unavailable {
					return false, errors.New("fixture unavailable")
				}
				return true, nil
			}
			e.srv.Close()
			e.srv = httptest.NewServer(api.NewRouter(deps))
			t.Cleanup(e.srv.Close)
			decision := ld.Decision{AccountID: e.acctA, AccessToken: liveToken(t, e, e.acctA, e.sidA, "blerg-runner"), BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, AskID: p.AskID, Approve: true}
			want := 409
			if unavailable {
				want = 503
			}
			if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != want {
				t.Fatal("stale or unavailable posture approved", code, want)
			}
			var n int
			if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM live_data_grants`).Scan(&n); err != nil || n != 0 {
				t.Fatal("failed posture created authority", n, err)
			}
		})
	}
}

func TestLiveDataUnsupportedCardRefusedBeforeProposal(t *testing.T) {
	e := newLiveApprovalEnv(t)
	for _, card := range []any{nil, map[string]any{"kind": "chart", "spec": map[string]any{"mark": "line"}}, map[string]any{"kind": "stat", "sparkline": true}} {
		config := liveConfig(e, "00000000-0000-4000-8000-000000000001", 1)
		raw, _ := json.Marshal(config)
		var setup map[string]any
		if err := json.Unmarshal(raw, &setup); err != nil {
			t.Fatal(err)
		}
		setup["card"] = card
		code, body := liveCall(t, e, "POST", "/internal/live-data/proposals", testInternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "configuration": setup, "ask_id": liveAskID(config)})
		if code != 400 || !bytes.Contains(body, []byte("stat and gauge")) {
			t.Fatal("unsupported setup did not give actionable refusal", code, string(body))
		}
		var count int
		if err := e.deps.Store.Pool().QueryRow(t.Context(), `SELECT (SELECT count(*) FROM live_data_proposals)+(SELECT count(*) FROM live_data_grants)`).Scan(&count); err != nil || count != 0 {
			t.Fatal("unsupported setup persisted binding or proposal", count, err)
		}
	}
}
