package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/livemcp"
	"github.com/blerglab/blerg-ai/core/internal/livedata"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
)

func TestLiveDataIssuerBootstrapFencesRestore(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	ctx := t.Context()
	root := t.TempDir()
	if e := livetls.Provision(root); e != nil {
		t.Fatal(e)
	}
	identity, e := livetls.Load(filepath.Join(root, "core"), "core")
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("BLERG_LIVE_DATA_AUTHORITY_BACKEND", "")
	t.Setenv("BLERG_LIVE_DATA_AUTHORITY_DIR", t.TempDir())
	t.Setenv("BLERG_LIVE_DATA_DESKTOP", "true")
	var deps api.Deps
	first, e := configureLiveDataIssuer(ctx, &deps, identity, pool)
	if e != nil || first == nil || deps.LiveDataAuthorityEpoch() != 1 || !first.Desktop {
		t.Fatal("issuer bootstrap failed", e)
	}
	if liveDataIssuerEnabled {
		t.Fatal("polling enabled during issuer rollout")
	}
	// A restored grant is deliberately staged under the old authority epoch.
	owner := "00000000-0000-4000-8000-000000000731"
	connection := "00000000-0000-4000-8000-000000000732"
	binding := "00000000-0000-4000-8000-000000000733"
	if _, e = pool.Exec(ctx, `INSERT INTO accounts(id,provider,provider_subject,email,role) VALUES($1,'local','issuer-boot-fixture','issuer@example.test','member')`, owner); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO mcp_connections(id,account_id,name,url,auth_kind) VALUES($1,$2,'market','https://market.example/mcp','none')`, connection, owner); e != nil {
		t.Fatal(e)
	}
	definition := json.RawMessage(`{"description":"Fixture quotes","inputSchema":{"additionalProperties":false,"properties":{},"type":"object"},"name":"quotes"}`)
	sum := sha256.Sum256(definition)
	revision := ld.Revision{Scope: ld.Scope{Installation: identity.Installation, Runner: identity.Runner, Owner: owner, Connection: connection, Binding: binding, Tool: "quotes", Hash: hex.EncodeToString(sum[:]), Revision: 1, AuthorityEpoch: 1, OwnerEpoch: 1, ConnectionGeneration: 1}, ToolDefinition: definition, Arguments: json.RawMessage(`{}`), Mapping: livemcp.Mapping{RowsPath: "/rows", Fields: []livemcp.Field{{Name: "value", Path: "/value", Type: "number"}}, Keys: []string{"value"}}, IntervalSeconds: 60, ExpiresAt: time.Now().Add(24 * time.Hour), OwnerOnly: true}
	frozen, configuration, e := revision.Canonical(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	scopeJSON, e := json.Marshal(frozen.Scope)
	if e != nil {
		t.Fatal(e)
	}
	insert := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO live_data_grants(binding_id,revision,owner_id,connection_id,scope,arguments,approved_sid,approved_at,expires_at,approved_configuration,runner_committed) VALUES($1,1,$2,$3,$4,'{}','fixture',clock_timestamp(),$5,$6,true)`, binding, owner, connection, scopeJSON, frozen.ExpiresAt, configuration); err != nil {
			t.Fatal(err)
		}
	}
	insert()
	restarted, e := configureLiveDataIssuer(ctx, &deps, identity, pool)
	if e != nil || restarted == nil || deps.LiveDataAuthorityEpoch() != 1 {
		t.Fatal("ordinary restart changed epoch", e)
	}
	var intact bool
	if e = pool.QueryRow(ctx, `SELECT revoked_at IS NULL FROM live_data_grants WHERE binding_id=$1`, binding).Scan(&intact); e != nil || intact {
		t.Fatal("ordinary restart retained an approval without a restore fence", e)
	}
	var noTLS api.Deps
	if issuer, e := configureLiveDataIssuer(ctx, &noTLS, nil, pool); e != nil || issuer != nil || noTLS.LiveDataAuthorityEpoch != nil {
		t.Fatal("optional TLS loss stranded revocation durability or enabled approvals", e)
	}
	// A database-only restore must not resurrect a checkpointed revocation.
	if _, e = pool.Exec(ctx, `DELETE FROM live_data_grants WHERE binding_id=$1`, binding); e != nil {
		t.Fatal(e)
	}
	state, stateErr := livedata.StartAuthority(os.Getenv("BLERG_LIVE_DATA_AUTHORITY_DIR"), false)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if e = (livedata.RevocationCheckpoint{Pool: pool, Authority: state, Epoch: state.Epoch()}).Sync(ctx); e != nil {
		t.Fatal("complete checkpoint before snapshot restore", e)
	}
	// Restore the old singleton too, as pg_restore would. Sequence reservations
	// are allowed to remain higher; only the external persisted mark is trusted.
	if _, e = pool.Exec(ctx, `UPDATE live_data_revocation_checkpoint SET high_water=0,authority_epoch=1 WHERE singleton`); e != nil {
		t.Fatal(e)
	}
	insert()
	var restored api.Deps
	if _, e = configureLiveDataIssuer(ctx, &restored, identity, pool); e == nil || restored.LiveDataAuthorityEpoch != nil {
		t.Fatal("database-only restore resurrected a revoked approval")
	}
	if _, e = livedata.FenceAuthority(os.Getenv("BLERG_LIVE_DATA_AUTHORITY_DIR")); e != nil {
		t.Fatal(e)
	}
	second, e := configureLiveDataIssuer(ctx, &deps, identity, pool)
	if e != nil || second == nil || deps.LiveDataAuthorityEpoch() != 2 {
		t.Fatal("external epoch not advanced", e)
	}
	var revoked bool
	if e = pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM live_data_grants WHERE binding_id=$1`, binding).Scan(&revoked); e != nil || !revoked {
		t.Fatal("restored grant not revoked", e)
	}
	if _, e = first.PublicKeys(ctx); e == nil {
		t.Fatal("old Core kept issuing trust")
	}
	// Losing even the entire external state must not silently reinitialize
	// while the database contains live-data authority history.
	t.Setenv("BLERG_LIVE_DATA_AUTHORITY_DIR", t.TempDir())
	var lost api.Deps
	if _, e = configureLiveDataIssuer(ctx, &lost, identity, pool); e == nil || lost.LiveDataAuthorityEpoch != nil {
		t.Fatal("lost independent state reset despite authority history")
	}
	// The production public router has no issuer registration.
	public := api.NewRouter(deps)
	rec := httptest.NewRecorder()
	public.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ld.IssuerPath+"mint", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatal("public mux exposed issuer")
	}
}

func TestLiveDataExplicitFenceCommand(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BLERG_LIVE_DATA_AUTHORITY_BACKEND", "")
	t.Setenv("BLERG_LIVE_DATA_AUTHORITY_DIR", root)
	if runLiveDataFence([]string{"--reason", "restore"}) == nil {
		t.Fatal("fence initialized missing authority")
	}
	// Missing-file recovery intentionally refuses even after the failed fence.
	root = t.TempDir()
	t.Setenv("BLERG_LIVE_DATA_AUTHORITY_DIR", root)
	first, e := livedata.StartAuthority(root, true)
	if e != nil {
		t.Fatal(e)
	}
	if runLiveDataFence(nil) == nil || runLiveDataFence([]string{"--reason", "restart"}) == nil {
		t.Fatal("implicit fence accepted")
	}
	if first.Epoch() != 1 {
		t.Fatal("invalid command mutated authority")
	}
	if e = runLiveDataFence([]string{"--reason", "restore"}); e != nil {
		t.Fatal(e)
	}
	next, e := livedata.StartAuthority(root, false)
	if e != nil || next.Epoch() != 2 || first.Epoch() != 0 {
		t.Fatal("explicit fence did not persist", e)
	}
}

func TestLiveDataIssuerPostureUsesDedicatedRunner(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	root := t.TempDir()
	if e := livetls.Provision(root); e != nil {
		t.Fatal(e)
	}
	identity, e := livetls.Load(filepath.Join(root, "core"), "core")
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("BLERG_LIVE_DATA_AUTHORITY_BACKEND", "")
	t.Setenv("BLERG_LIVE_DATA_AUTHORITY_DIR", t.TempDir())
	t.Setenv("BLERG_LIVE_DATA_DESKTOP", "false")
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/live-data/posture" || r.Header.Get("X-Internal-Key") != "fixture-posture-key" {
			t.Error("posture request did not use its dedicated Core service path")
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"safe": true, "cause": "isolated_cluster", "checked_at": time.Now().UTC()})
	}))
	defer runner.Close()
	deps := api.Deps{InternalKey: "fixture-posture-key", LiveDataRunnerURL: runner.URL, LiveDataRunnerClient: runner.Client()}
	issuer, e := configureLiveDataIssuer(t.Context(), &deps, identity, pool)
	if e != nil || issuer == nil {
		t.Fatal(e)
	}
	if safe, e := issuer.Posture(t.Context()); e != nil || !safe {
		t.Fatal("issuer ignored the verified Runner posture", safe, e)
	}
}
