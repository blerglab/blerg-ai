package server

// The cron start path and lifecycle (cronstart.go), against a real database, a fake Kubernetes API,
// a fake core and a fake MCP upstream. The fixture is the MCP start fixture (mcpstart_fixture_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/cron"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

const cronTok = "c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0c1"

// ---- fakes -------------------------------------------------------------------------------

// fakeCronCore is core's token status and revoke.
type fakeCronCore struct {
	mu        sync.Mutex
	live      map[string]bool // token id -> live; absent = 404 (not live)
	statusErr error
	revokeErr error
	revoked   []string // "<account>:<token>"
	checks    int
	onStatus  func()           // runs inside TokenLive, before it answers (a race the test wants to interleave)
	onRevoke  func()           // runs at the start of RevokeToken, before it answers
	revokeFor map[string]error // per-token revoke errors
}

func (f *fakeCronCore) TokenLive(_ context.Context, _, tokenID string) (bool, error) {
	f.mu.Lock()
	hook := f.onStatus
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if f.statusErr != nil {
		return false, f.statusErr
	}
	return f.live[tokenID], nil
}

func (f *fakeCronCore) RevokeToken(_ context.Context, account, tokenID string) error {
	f.mu.Lock()
	hook := f.onRevoke
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.revokeFor[tokenID]; err != nil {
		return err
	}
	if f.revokeErr != nil {
		return f.revokeErr
	}
	f.revoked = append(f.revoked, account+":"+tokenID)
	f.live[tokenID] = false
	return nil
}

func (f *fakeCronCore) revokedList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

type cronTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *cronTestClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *cronTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// ---- fixture -----------------------------------------------------------------------------

type cronFx struct {
	*mcpFx
	svc   *CronService
	core  *fakeCronCore
	clock *cronTestClock
}

func newCronFx(t *testing.T) *cronFx {
	t.Helper()
	fx := newMCPFx(t)
	core := &fakeCronCore{live: map[string]bool{cronTok: true}}
	svc := NewCronService(fx.api, CronServiceConfig{Core: core, Logf: t.Logf})
	// The owner has a personal Claude credential at core (the cluster path needs one).
	fx.k8s.credentialFetchResponses = map[string][]byte{mcpAcct + ":claude": []byte("sk-ant-oat-owner")}
	return &cronFx{mcpFx: fx, svc: svc, core: core, clock: &cronTestClock{t: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}}
}

// cronMCP is the stored `mcp` of a cron naming the fixture's connection.
func (cf *cronFx) cronMCP() json.RawMessage {
	raw, err := json.Marshal(cf.selection())
	if err != nil {
		cf.t.Fatal(err)
	}
	return raw
}

// newCron inserts a cron for the fixture's owner with its own token, mutated by mut.
func (cf *cronFx) newCron(mut func(*db.Cron)) *db.Cron {
	cf.t.Helper()
	c := db.Cron{
		OwnerAccountID: mcpAcct, Name: "Morning digest", Enabled: true, Schedule: "0 8 * * *", Timezone: "UTC",
		Prompt: "Summarise my inbox", Runtime: "auto", MCP: cf.cronMCP(), TokenID: cronTok,
		TokenExpiresAt: time.Now().Add(30 * 24 * time.Hour), GraceSeconds: 3600, MaxRuntimeSeconds: 1800,
		NextRunAt: time.Now().Add(time.Hour),
	}
	if mut != nil {
		mut(&c)
	}
	out, err := db.InsertCron(context.Background(), cf.pool, c, 20)
	if err != nil {
		cf.t.Fatal(err)
	}
	return out
}

func (cf *cronFx) run(c *db.Cron) *db.CronRun {
	return &db.CronRun{ID: newUUID(), CronID: c.ID, ScheduledFor: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC), Status: db.CronRunClaimed}
}

func (cf *cronFx) session(id string) *db.SessionRow {
	cf.t.Helper()
	row, err := db.GetSession(context.Background(), cf.pool, id)
	if err != nil || row == nil {
		cf.t.Fatalf("GetSession %s: %v (%v)", id, row, err)
	}
	return row
}

// assertNothingWritten is the pre-flight guarantee: no session, idempotency key, grant, Job or
// Secret exists.
func (cf *cronFx) assertNothingWritten(what string) {
	cf.t.Helper()
	for _, table := range []string{"sessions", "runner_idempotency", "session_mcp_grants"} {
		if n := cf.count(table); n != 0 {
			cf.t.Errorf("%s: %d rows in %s, want 0", what, n, table)
		}
	}
	cf.k8s.mu.Lock()
	defer cf.k8s.mu.Unlock()
	if len(cf.k8s.created) != 0 || len(cf.k8s.createdSecrets) != 0 {
		cf.t.Errorf("%s: %d Jobs and %d Secrets created, want none", what, len(cf.k8s.created), len(cf.k8s.createdSecrets))
	}
}

func isCapacity(err error) bool   { return errors.Is(err, cron.ErrCapacity) }
func isCredential(err error) bool { return errors.Is(err, cron.ErrCredential) }

// ---- pure functions ----------------------------------------------------------------------

func TestResolveCronRuntime(t *testing.T) {
	tests := []struct {
		runtime string
		cluster bool
		want    string
		wantErr bool
	}{
		{"auto", true, "cluster", false},
		{"auto", false, "docker", false},
		{"", true, "cluster", false}, // an unset runtime resolves like auto, it never reaches the start path empty
		{"", false, "docker", false},
		{"cluster", true, "cluster", false},
		{"cluster", false, "", true},
		{"docker", true, "docker", false},
		{"docker", false, "docker", false},
		{"daemon", true, "", true}, // the bare host is never allowed
		{"daemon", false, "", true},
		{"host", false, "", true},
	}
	for _, tc := range tests {
		got, err := resolveCronRuntime(tc.runtime, tc.cluster)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("resolveCronRuntime(%q, cluster=%v) = %q, %v; want %q (err %v)", tc.runtime, tc.cluster, got, err, tc.want, tc.wantErr)
		}
		if err == nil && got != "cluster" && got != "docker" {
			t.Errorf("resolveCronRuntime(%q, %v) = %q: must be explicit cluster or docker", tc.runtime, tc.cluster, got)
		}
	}
}

func TestCronStartProblem(t *testing.T) {
	ok := RunnerStartRequest{CronID: "c", Runtime: "cluster", Engine: "claude", NoRepo: true, AutoStop: true, NoOperatorFallback: true}
	if msg := cronStartProblem(ok); msg != "" {
		t.Fatalf("the well formed cron request is refused: %s", msg)
	}
	for name, mut := range map[string]func(*RunnerStartRequest){
		"bare host":         func(r *RunnerStartRequest) { r.Runtime = "daemon" },
		"empty runtime":     func(r *RunnerStartRequest) { r.Runtime = "" },
		"other engine":      func(r *RunnerStartRequest) { r.Engine = "codex" },
		"default engine":    func(r *RunnerStartRequest) { r.Engine = "" },
		"with a repo":       func(r *RunnerStartRequest) { r.NoRepo = false },
		"interactive":       func(r *RunnerStartRequest) { r.AutoStop = false },
		"operator fallback": func(r *RunnerStartRequest) { r.NoOperatorFallback = false },
	} {
		r := ok
		mut(&r)
		if cronStartProblem(r) == "" {
			t.Errorf("%s: a cron start must be refused", name)
		}
	}
	if cronStartProblem(RunnerStartRequest{Runtime: "daemon"}) != "" {
		t.Error("a start that is not a cron's is not judged by the cron rules")
	}
}

// A request body can never carry the in-process fields.
func TestCronFieldsAreNotDecodableFromABody(t *testing.T) {
	var req RunnerStartRequest
	body := `{"cron_id":"x","CronID":"x","daemon_id":"d","DaemonID":"d","no_operator_fallback":true,"NoOperatorFallback":true,"grant":{"AccountID":"a"},"repo":"r"}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if req.CronID != "" || req.DaemonID != "" || req.NoOperatorFallback || req.Grant != nil || req.Repo != "r" {
		t.Errorf("decoded %+v", req)
	}
	// And a request without them hashes exactly as before.
	a, _ := startRequestHash(RunnerStartRequest{Repo: "r", Prompt: "p"})
	b, _ := startRequestHash(RunnerStartRequest{Repo: "r", Prompt: "p", CronID: "c", DaemonID: "d", NoOperatorFallback: true})
	if a != b {
		t.Error("the in-process fields changed the idempotency hash")
	}
}

func TestCronSessionTitleUsesTheCronsTimezone(t *testing.T) {
	c := &db.Cron{Name: "Digest", Timezone: "America/Los_Angeles"}
	run := &db.CronRun{ScheduledFor: time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)}
	if got, want := cronSessionTitle(c, run), "Digest 2026-09-01 08:00 PDT"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
	c.Timezone = "Not/AZone"
	if got := cronSessionTitle(c, run); got != "Digest 2026-09-01 15:00 UTC" {
		t.Errorf("bad zone title = %q", got)
	}
}

// ---- core over HTTP ------------------------------------------------------------------------

func TestHTTPCronCore(t *testing.T) {
	type seen struct{ path, key, body string }
	var last seen
	status, live := http.StatusOK, true
	bare404 := false // a router's or proxy's 404, without core's own body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		last = seen{r.URL.Path, r.Header.Get("X-Internal-Key"), string(b)}
		if status == http.StatusNotFound && !bare404 {
			http.Error(w, "not found", status)
			return
		}
		w.WriteHeader(status)
		if status == http.StatusOK && r.URL.Path == "/internal/tokens/status" {
			_ = json.NewEncoder(w).Encode(map[string]any{"live": live, "account_id": "a1"})
		}
	}))
	defer srv.Close()
	c := &HTTPCronCore{BaseURL: srv.URL + "/", InternalKey: "k-secret", HTTP: srv.Client()}
	ctx := context.Background()

	if ok, err := c.TokenLive(ctx, "a1", "t1"); err != nil || !ok {
		t.Fatalf("live = %v, %v", ok, err)
	}
	if last.path != "/internal/tokens/status" || last.key != "k-secret" || !strings.Contains(last.body, `"account_id":"a1"`) || !strings.Contains(last.body, `"token_id":"t1"`) {
		t.Errorf("status request = %+v", last)
	}
	live = false
	if ok, err := c.TokenLive(ctx, "a1", "t1"); err != nil || ok {
		t.Errorf("a dead token: live = %v, %v", ok, err)
	}
	// Core says 200 {"live": false} for an unknown or another account's token. A 404 is not core
	// (a proxy, a rolled-back deploy): an error, which holds the run, never "revoked".
	status = http.StatusNotFound
	if ok, err := c.TokenLive(ctx, "a1", "t1"); err == nil || ok {
		t.Errorf("a 404 must be an error, not a not-live verdict: %v, %v", ok, err)
	}
	status = http.StatusInternalServerError
	if _, err := c.TokenLive(ctx, "a1", "t1"); err == nil {
		t.Error("a 500 is an error (core could not say), not 'not live'")
	}

	status = http.StatusOK
	if err := c.RevokeToken(ctx, "a1", "t1"); err != nil {
		t.Fatal(err)
	}
	if last.path != "/internal/tokens/revoke" || last.key != "k-secret" ||
		strings.Contains(last.body, "session_id") || !strings.Contains(last.body, `"token_id":"t1"`) {
		t.Errorf("revoke request = %+v (must carry the internal key and no session proof)", last)
	}
	status = http.StatusNotFound
	if err := c.RevokeToken(ctx, "a1", "t1"); err != nil {
		t.Errorf("revoking a token that is gone is not an error: %v", err)
	}
	bare404 = true // MINOR 26: a 404 that is not core's own acknowledges nothing
	if err := c.RevokeToken(ctx, "a1", "t1"); err == nil {
		t.Error("a router's 404 was taken as an acknowledged revocation")
	}
	bare404 = false
	status = http.StatusServiceUnavailable
	if err := c.RevokeToken(ctx, "a1", "t1"); err == nil {
		t.Error("a 503 on revoke must be reported")
	}
	srv.Close()
	if _, err := c.TokenLive(ctx, "a1", "t1"); err == nil {
		t.Error("an unreachable core is an error")
	}
	if _, err := (&HTTPCronCore{}).TokenLive(ctx, "a", "t"); err == nil {
		t.Error("an unconfigured core is an error")
	}
}

// ---- the start path ------------------------------------------------------------------------

func TestCronStartCluster(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	c := cf.newCron(func(c *db.Cron) { c.Model, c.Effort = ptr("claude-sonnet-5"), ptr("high") })
	run := cf.run(c)

	sid, err := cf.svc.Start(ctx, c, run)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	row := cf.session(sid)
	if row.Runtime == nil || *row.Runtime != "cluster" {
		t.Errorf("runtime = %v, want cluster (auto with a cluster configured)", row.Runtime)
	}
	if !row.Private || row.CronID == nil || *row.CronID != c.ID || !row.AutoStop {
		t.Errorf("row: private=%v cron=%v auto_stop=%v", row.Private, row.CronID, row.AutoStop)
	}
	if row.SpawningAccountID == nil || *row.SpawningAccountID != mcpAcct || row.TokenID == nil || *row.TokenID != cronTok {
		t.Errorf("attribution: account=%v token=%v", row.SpawningAccountID, row.TokenID)
	}
	if row.Title == nil || *row.Title != "Morning digest 2026-09-01 08:00 UTC" || row.Kind != "agent" {
		t.Errorf("title=%v kind=%s", row.Title, row.Kind)
	}
	if row.Model == nil || *row.Model != "claude-sonnet-5" || row.Effort == nil || *row.Effort != "high" {
		t.Errorf("model/effort = %v / %v", row.Model, row.Effort)
	}
	if row.Engine == nil || *row.Engine != "claude" {
		t.Errorf("engine = %v", row.Engine)
	}

	// The grant: the cron token is the proof, the connection is the owner's.
	gs := cf.grants(sid)
	if len(gs) != 1 {
		t.Fatalf("grants = %d, want 1", len(gs))
	}
	g := gs[0]
	if g.ProofKind != "token_id" || g.ProofValue != cronTok || g.AccountID != mcpAcct || g.ConnectionID != cf.connID {
		t.Errorf("grant = %+v", g)
	}
	cf.list.mu.Lock()
	for _, p := range cf.list.proofs {
		if p.TokenID != cronTok || p.SessionID != "" || p.AccountID != mcpAcct {
			t.Errorf("a connection list was asked with proof %+v, want the cron token only", p)
		}
	}
	cf.list.mu.Unlock()
	cfg := cf.secretConfig(sid)
	if len(cfg.Servers) != 1 || !cf.tokenBelongsTo(cfg.Servers[0].Token, sid) {
		t.Errorf("delivered config = %+v", cfg)
	}

	// The credential came from the owner, with the cron token as proof; never the operator's.
	if got := cf.k8s.credentialFetchTokenIDs; len(got) == 0 || got[0] != cronTok {
		t.Errorf("credential proofs = %v", got)
	}
	cf.k8s.mu.Lock()
	jobs := len(cf.k8s.created)
	cf.k8s.mu.Unlock()
	if jobs != 1 {
		t.Errorf("jobs = %d, want 1", jobs)
	}

	// Another account cannot see it, the owner can.
	if canSeeAccount("someone-else", row) || !canSeeAccount(mcpAcct, row) {
		t.Error("a cron session must be visible to its owner only")
	}

	// The idempotency key is the run's, in the cron's own (token independent) scope.
	key, err := db.GetIdempotencyKey(ctx, cf.pool, "cron:"+c.ID, run.IdempotencyKey())
	if err != nil || key == nil || key.SessionID != sid {
		t.Errorf("idempotency row = %+v, %v", key, err)
	}
	// Repeating the call, even after the cron was edited (a different request hash), replays.
	if _, err := db.UpdateCron(ctx, cf.pool, mcpAcct, c.ID, func(x *db.Cron) error { x.Prompt = "A different prompt"; return nil }); err != nil {
		t.Fatal(err)
	}
	edited, _ := db.GetCron(ctx, cf.pool, c.ID)
	again, err := cf.svc.Start(ctx, edited, run)
	if err != nil || again != sid {
		t.Errorf("repeat = %q, %v; want the same session %s", again, err, sid)
	}
	if cf.count("sessions") != 1 {
		t.Errorf("a repeat call made a second session: %d", cf.count("sessions"))
	}
	cf.k8s.mu.Lock()
	jobs = len(cf.k8s.created)
	cf.k8s.mu.Unlock()
	if jobs != 1 {
		t.Errorf("a repeat call made a second Job: %d", jobs)
	}
}

func TestCronStartDockerSandbox(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	c := cf.newCron(func(c *db.Cron) { c.Runtime = "docker" })
	sid, err := cf.svc.Start(ctx, c, cf.run(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	msg := recvSpawn(t, cf.dc)
	if msg.SessionID != sid || !msg.Sandbox || msg.Kind != "agent" || msg.Engine != "claude" || !msg.NoRepo {
		t.Fatalf("spawn message = %+v", msg)
	}
	if msg.MCPGateway == nil || len(msg.MCPGateway.Servers) != 1 || !cf.tokenBelongsTo(msg.MCPGateway.Servers[0].Token, sid) {
		t.Errorf("gateway config = %+v", msg.MCPGateway)
	}
	if msg.InitialPrompt != "Summarise my inbox" {
		t.Errorf("prompt = %q", msg.InitialPrompt)
	}
	row := cf.session(sid)
	if row.Runtime == nil || *row.Runtime != "docker" || !row.Private || row.CronID == nil || *row.CronID != c.ID || !row.AutoStop {
		t.Errorf("row = runtime %v private %v cron %v auto_stop %v", row.Runtime, row.Private, row.CronID, row.AutoStop)
	}
	if g := cf.grants(sid); len(g) != 1 || g[0].ProofKind != "token_id" || g[0].ProofValue != cronTok {
		t.Errorf("grants = %+v", g)
	}
	cf.k8s.mu.Lock()
	jobs := len(cf.k8s.created)
	cf.k8s.mu.Unlock()
	if jobs != 0 {
		t.Errorf("a Docker cron created %d cluster Jobs", jobs)
	}
}

// auto with no cluster configured resolves to Docker, never to the bare host.
func TestCronStartAutoWithoutClusterIsDocker(t *testing.T) {
	cf := newCronFx(t)
	cf.hub.SetJobManager(nil)
	c := cf.newCron(nil)
	sid, err := cf.svc.Start(context.Background(), c, cf.run(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	msg := recvSpawn(t, cf.dc)
	if !msg.Sandbox || msg.SessionID != sid {
		t.Fatalf("spawn = %+v: an auto cron with no cluster must run in the sandbox", msg)
	}
	if rt := cf.session(sid).Runtime; rt == nil || *rt != "docker" {
		t.Errorf("runtime = %v, want docker", rt)
	}
}

func TestCronStartWithoutMCPStillPrivateAndMarked(t *testing.T) {
	cf := newCronFx(t)
	c := cf.newCron(func(c *db.Cron) { c.MCP = json.RawMessage("[]"); c.Runtime = "docker" })
	sid, err := cf.svc.Start(context.Background(), c, cf.run(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	msg := recvSpawn(t, cf.dc)
	if msg.MCPGateway != nil {
		t.Error("a cron with no connections must not get a gateway")
	}
	row := cf.session(sid)
	if !row.Private || row.CronID == nil {
		t.Errorf("a cron session is private and marked even with no grant: private=%v cron=%v", row.Private, row.CronID)
	}
}

func TestCronStartPinnedDaemon(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	other := "0a0a0a0a-0a0a-4a0a-8a0a-0a0a0a0a0a0f" // sorts before the fixture daemon (0d0d...), so a pin is what selects the fixture one
	if err := db.UpsertDaemon(ctx, cf.pool, other, "desk", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	dc2 := &DaemonConn{ID: other, Name: "desk", ReposRoot: "/repos", send: make(chan []byte, 8)}
	dc2.SetMCPGateway(true)
	dc2.SetRestrictTools(true)
	dc2.SetSandboxAvailable(true)
	cf.hub.Register(dc2)

	// Pinned to the fixture's (higher id) daemon: it, and only it, gets the spawn.
	c := cf.newCron(func(c *db.Cron) { c.Runtime = "docker"; c.DaemonID = &cf.dc.ID })
	sid, err := cf.svc.Start(ctx, c, cf.run(c))
	if err != nil {
		t.Fatal(err)
	}
	if msg := recvSpawn(t, cf.dc); msg.SessionID != sid {
		t.Errorf("spawn = %+v", msg)
	}
	if len(dc2.send) != 0 {
		t.Error("a pinned cron was sent to another daemon")
	}
	// Unpinned: the lowest id that can run the sandbox.
	c2 := cf.newCron(func(c *db.Cron) { c.Runtime = "docker"; c.TokenID = cronTok })
	cf.core.live[cronTok] = true
	sid2, err := cf.svc.Start(ctx, c2, cf.run(c2))
	if err != nil {
		t.Fatal(err)
	}
	if msg := recvSpawn(t, dc2); msg.SessionID != sid2 {
		t.Errorf("unpinned spawn = %+v", msg)
	}

	// Pinned to a daemon that is not connected: held, nothing written.
	before := cf.count("sessions")
	ghost := "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f"
	c3 := cf.newCron(func(c *db.Cron) { c.Runtime = "docker"; c.DaemonID = &ghost })
	if _, err := cf.svc.Start(ctx, c3, cf.run(c3)); !isCapacity(err) {
		t.Errorf("a missing pinned daemon: err = %v, want capacity", err)
	}
	// Pinned to a daemon without the sandbox image: held too (it would only fail at the daemon).
	dc2.SetSandboxAvailable(false)
	c4 := cf.newCron(func(c *db.Cron) { c.Runtime = "docker"; c.DaemonID = &other })
	if _, err := cf.svc.Start(ctx, c4, cf.run(c4)); !isCapacity(err) {
		t.Errorf("a pinned daemon with no sandbox image: err = %v, want capacity", err)
	}
	if got := cf.count("sessions"); got != before {
		t.Errorf("held starts created sessions: %d -> %d", before, got)
	}
}

// ---- capacity: nothing is written before it is known ----------------------------------------

func TestCronCapacityPreflightWritesNothing(t *testing.T) {
	ctx := context.Background()

	t.Run("cluster cap", func(t *testing.T) {
		cf := newCronFx(t)
		cf.k8s.active = 2 // the fixture's MaxSessions
		c := cf.newCron(nil)
		for i := 0; i < 5; i++ {
			if _, err := cf.svc.Start(ctx, c, cf.run(c)); !isCapacity(err) {
				t.Fatalf("attempt %d: err = %v, want capacity", i, err)
			}
		}
		cf.assertNothingWritten("cluster at its cap")
	})

	t.Run("no daemon", func(t *testing.T) {
		cf := newCronFx(t)
		cf.hub.UnregisterIfCurrent(cf.dc)
		c := cf.newCron(func(c *db.Cron) { c.Runtime = "docker" })
		if _, err := cf.svc.Start(ctx, c, cf.run(c)); !isCapacity(err) {
			t.Fatalf("err = %v, want capacity", err)
		}
		cf.assertNothingWritten("no daemon connected")
	})

	t.Run("daemon without the sandbox image", func(t *testing.T) {
		cf := newCronFx(t)
		cf.dc.SetSandboxAvailable(false)
		c := cf.newCron(func(c *db.Cron) { c.Runtime = "docker" })
		if _, err := cf.svc.Start(ctx, c, cf.run(c)); !isCapacity(err) {
			t.Fatalf("err = %v, want capacity", err)
		}
		cf.assertNothingWritten("no sandbox image")
	})

	t.Run("daemon too old for the gateway", func(t *testing.T) {
		cf := newCronFx(t)
		cf.dc.SetMCPGateway(false)
		c := cf.newCron(func(c *db.Cron) { c.Runtime = "docker" })
		if _, err := cf.svc.Start(ctx, c, cf.run(c)); !isCapacity(err) {
			t.Fatalf("err = %v, want capacity (held until the daemon is updated)", err)
		}
		cf.assertNothingWritten("daemon without mcp_gateway")
	})

	t.Run("core unreachable holds", func(t *testing.T) {
		cf := newCronFx(t)
		cf.core.statusErr = errors.New("core unreachable")
		c := cf.newCron(nil)
		if _, err := cf.svc.Start(ctx, c, cf.run(c)); !isCapacity(err) {
			t.Fatalf("err = %v, want capacity (hold, do not fail or pause on a core blip)", err)
		}
		cf.assertNothingWritten("core down")
		if got, _ := db.GetCron(ctx, cf.pool, c.ID); got.PausedReason != nil {
			t.Errorf("a core outage paused the cron: %q", *got.PausedReason)
		}
	})
}

// The scheduler over many ticks with no capacity: one held run, zero sessions, keys, grants and
// Jobs, then it starts the moment capacity returns (spec 7.3).
func TestCronHeldRunRetriedEveryTickCreatesNothing(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	cf.k8s.active = 2
	sch, err := cron.New(cron.Config{
		Pool: cf.pool, Starter: cf.svc, Sessions: cf.svc, Clock: cf.clock, OnPaused: cf.svc.OnPaused, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sch.Release(context.Background()) }) // hands the lock connection back before the pool closes
	c, err := sch.CreateCron(ctx, cron.NewCron{
		Owner: mcpAcct, Name: "Held", Enabled: true, Schedule: "*/15 * * * *", Timezone: "UTC", Prompt: "p",
		MCP: cf.cronMCP(), TokenID: cronTok, TokenExpiresAt: cf.clock.Now().Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	cf.clock.Advance(16 * time.Minute) // past the first slot
	for i := 0; i < 12; i++ {
		if err := sch.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		cf.clock.Advance(30 * time.Second)
	}
	cf.assertNothingWritten("twelve ticks at the cap")
	runs, err := db.ListCronRuns(ctx, cf.pool, c.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].Status != db.CronRunHeld {
		t.Fatalf("runs = %+v, %v; want exactly one held run", runs, err)
	}
	if runs[0].Reason == nil || !strings.Contains(*runs[0].Reason, "cluster session cap reached") {
		t.Errorf("held reason = %v", runs[0].Reason)
	}

	cf.k8s.active = 0 // capacity returns
	if err := sch.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ = db.ListCronRuns(ctx, cf.pool, c.ID, 10)
	if len(runs) != 1 || runs[0].Status != db.CronRunStarted || runs[0].SessionID == nil {
		t.Fatalf("after capacity returned: runs = %+v", runs)
	}
	if n := cf.count("sessions"); n != 1 {
		t.Errorf("sessions = %d, want exactly 1", n)
	}
	if n := cf.count("runner_idempotency"); n != 1 {
		t.Errorf("idempotency rows = %d, want 1", n)
	}
	if row := cf.session(*runs[0].SessionID); row.CronID == nil || *row.CronID != c.ID {
		t.Errorf("session cron id = %v", row.CronID)
	}
}

// The account's concurrent limit holds the run; it starts when one of the sessions ends.
func TestCronConcurrentLimitHolds(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	other := cf.newCron(func(c *db.Cron) { c.Name = "Other"; c.TokenID = "c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0c2" })
	for i := 0; i < 2; i++ {
		id := newUUID()
		cf.insertCronSession(id, other.ID, mcpAcct, "running")
	}
	if n := cf.svc.ConcurrentActive(ctx, mcpAcct); n != 2 {
		t.Fatalf("ConcurrentActive = %d, want 2", n)
	}
	if n := cf.svc.ConcurrentActive(ctx, "someone-else"); n != 0 {
		t.Errorf("another account has %d", n)
	}

	sch, err := cron.New(cron.Config{Pool: cf.pool, Starter: cf.svc, Sessions: cf.svc, Clock: cf.clock, OnPaused: cf.svc.OnPaused, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sch.Release(context.Background()) }) // hands the lock connection back before the pool closes
	c, err := sch.CreateCron(ctx, cron.NewCron{
		Owner: mcpAcct, Name: "Third", Enabled: true, Schedule: "*/15 * * * *", Timezone: "UTC", Prompt: "p",
		MCP: cf.cronMCP(), TokenID: cronTok, TokenExpiresAt: cf.clock.Now().Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	cf.clock.Advance(16 * time.Minute)
	if err := sch.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ := db.ListCronRuns(ctx, cf.pool, c.ID, 5)
	if len(runs) != 1 || runs[0].Status != db.CronRunHeld || runs[0].Reason == nil || !strings.Contains(*runs[0].Reason, "concurrent") {
		t.Fatalf("runs = %+v, want one held run naming the concurrent limit", runs)
	}
	if n := cf.count("sessions"); n != 2 {
		t.Errorf("a held run created a session: %d sessions", n)
	}
	// One of the two ends: the held run starts on the next tick.
	if _, err := cf.pool.Exec(ctx, `UPDATE sessions SET status = 'ended' WHERE id = (SELECT id FROM sessions LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	cf.clock.Advance(30 * time.Second)
	if err := sch.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ = db.ListCronRuns(ctx, cf.pool, c.ID, 5)
	if len(runs) != 1 || runs[0].Status != db.CronRunStarted {
		t.Fatalf("after a slot freed: runs = %+v", runs)
	}
}

// insertCronSession adds a session row of a cron directly.
func (cf *cronFx) insertCronSession(id, cronID, account, status string) {
	cf.t.Helper()
	ctx := context.Background()
	if err := db.InsertSession(ctx, cf.pool, id, cf.dc.ID, status, "/repos/.scratch", "", "t", ""); err != nil {
		cf.t.Fatal(err)
	}
	if err := db.SetSessionSpawningAccount(ctx, cf.pool, id, account); err != nil {
		cf.t.Fatal(err)
	}
	if err := db.SetSessionCronID(ctx, cf.pool, id, cronID); err != nil {
		cf.t.Fatal(err)
	}
}

// ---- liveness gate ---------------------------------------------------------------------------

func TestCronLivenessGatePausesARevokedCron(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	c := cf.newCron(nil)
	// A session the cron already has running, which the pause must stop.
	running := newUUID()
	cf.insertCronSession(running, c.ID, mcpAcct, "running")

	delete(cf.core.live, cronTok) // core answers 404 for it: revoked, or never the owner's
	_, err := cf.svc.Start(ctx, c, cf.run(c))
	if err == nil || isCapacity(err) || isCredential(err) {
		t.Fatalf("err = %v, want a plain failure (not capacity, not credential)", err)
	}
	if !strings.Contains(err.Error(), "access revoked") {
		t.Errorf("err = %v", err)
	}
	got, _ := db.GetCron(ctx, cf.pool, c.ID)
	if got.PausedReason == nil || *got.PausedReason != "access revoked" {
		t.Fatalf("paused reason = %v, want %q", got.PausedReason, "access revoked")
	}
	if list := cf.core.revokedList(); len(list) != 1 || list[0] != mcpAcct+":"+cronTok {
		t.Errorf("revoked = %v, want the cron token revoked on pause", list)
	}
	if row := cf.session(running); row.Status != "ended" {
		t.Errorf("the running session is %q, want ended", row.Status)
	}
	// Nothing new was started.
	if n := cf.count("session_mcp_grants"); n != 0 {
		t.Errorf("grants = %d", n)
	}
	cf.k8s.mu.Lock()
	jobs := len(cf.k8s.created)
	cf.k8s.mu.Unlock()
	if jobs != 0 {
		t.Errorf("jobs = %d", jobs)
	}
}

// A dead token found by the connection list (not the status call) pauses the cron the same way.
func TestCronDeadProofFromTheConnectionListPauses(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	cf.list.err = ErrMCPProofInvalid
	c := cf.newCron(nil)
	if _, err := cf.svc.Start(ctx, c, cf.run(c)); err == nil || isCapacity(err) || isCredential(err) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := db.GetCron(ctx, cf.pool, c.ID); got.PausedReason == nil || *got.PausedReason != "access revoked" {
		t.Errorf("paused reason = %v", got.PausedReason)
	}
	cf.assertNothingWritten("dead proof")
}

// ---- credentials -----------------------------------------------------------------------------

func TestCronStartNeverUsesTheOperatorCredential(t *testing.T) {
	ctx := context.Background()
	t.Run("core 404", func(t *testing.T) {
		cf := newCronFx(t)
		cf.k8s.credentialFetchResponses = nil // no personal credential: core answers 404
		c := cf.newCron(nil)
		run := cf.run(c)
		_, err := cf.svc.Start(ctx, c, run)
		if !isCredential(err) {
			t.Fatalf("err = %v, want a credential error", err)
		}
		cf.k8s.mu.Lock()
		jobs, secrets := len(cf.k8s.created), len(cf.k8s.createdSecrets)
		cf.k8s.mu.Unlock()
		if jobs != 0 || secrets != 0 {
			t.Errorf("%d Jobs and %d Secrets created", jobs, secrets)
		}
		if k, _ := db.GetIdempotencyKey(ctx, cf.pool, "cron:"+c.ID, run.IdempotencyKey()); k != nil {
			t.Error("the idempotency key of a failed start was left behind")
		}
		if n := cf.count("session_mcp_grants"); n != 0 {
			t.Errorf("grants left behind: %d", n)
		}
	})
	t.Run("core 500", func(t *testing.T) {
		cf := newCronFx(t)
		url, _ := coreCreds(t, http.StatusInternalServerError, "")
		jm := cf.hub.JobManager()
		jm.CoreURL, jm.CredentialClient = url, http.DefaultClient
		c := cf.newCron(nil)
		if _, err := cf.svc.Start(ctx, c, cf.run(c)); !isCredential(err) {
			t.Fatalf("err = %v, want a credential error", err)
		}
		if len(cf.k8s.created) != 0 {
			t.Error("a Job was created")
		}
	})
}

func TestCronStartMissingConnectionFailsVisibly(t *testing.T) {
	ctx := context.Background()
	t.Run("deleted", func(t *testing.T) {
		cf := newCronFx(t)
		c := cf.newCron(nil)
		cf.list.conns = nil // the connection was deleted in Settings
		_, err := cf.svc.Start(ctx, c, cf.run(c))
		if !isCredential(err) || !strings.Contains(err.Error(), cf.connID) || !strings.Contains(err.Error(), "no longer exists") {
			t.Fatalf("err = %v, want a credential error naming the connection", err)
		}
		cf.assertNothingWritten("missing connection")
	})
	t.Run("needs sign-in", func(t *testing.T) {
		cf := newCronFx(t)
		c := cf.newCron(nil)
		cf.list.conns[0].Status = "needs_auth"
		_, err := cf.svc.Start(ctx, c, cf.run(c))
		if !isCredential(err) || !strings.Contains(err.Error(), "needs_auth") {
			t.Fatalf("err = %v, want a credential error", err)
		}
		cf.assertNothingWritten("connection needing sign-in")
	})
	t.Run("someone else's", func(t *testing.T) {
		cf := newCronFx(t)
		c := cf.newCron(func(c *db.Cron) {
			c.MCP = json.RawMessage(`[{"connection":"` + newUUID() + `","tools":{"echo":{"mode":"allow","hash":"x"}}}]`)
		})
		if _, err := cf.svc.Start(ctx, c, cf.run(c)); !isCredential(err) {
			t.Fatalf("err = %v, want a credential error", err)
		}
		cf.assertNothingWritten("unknown connection")
	})
	t.Run("a tool that changed is an ordinary failure", func(t *testing.T) {
		cf := newCronFx(t)
		c := cf.newCron(nil)
		cf.up.setDescription("echo", "now does something else")
		_, err := cf.svc.Start(ctx, c, cf.run(c))
		if err == nil || isCredential(err) || isCapacity(err) || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("err = %v, want a plain failure that says the tool changed", err)
		}
		cf.assertNothingWritten("changed tool")
	})
}

// ---- lifecycle -------------------------------------------------------------------------------

func TestStopCronSessionsAndRevokeHelpers(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	a := cf.newCron(func(c *db.Cron) { c.Name = "A" })
	b := cf.newCron(func(c *db.Cron) { c.Name = "B"; c.TokenID = "c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0c3" })

	// A real start, so the stop has a Job, a grant and a board token to clean up.
	sidA, err := cf.svc.Start(ctx, a, cf.run(a))
	if err != nil {
		t.Fatal(err)
	}
	sidB := newUUID()
	cf.insertCronSession(sidB, b.ID, mcpAcct, "running")
	ended := newUUID()
	cf.insertCronSession(ended, a.ID, mcpAcct, "ended")

	n, err := cf.svc.StopCronSessions(ctx, a.ID)
	if err != nil || n != 1 {
		t.Fatalf("StopCronSessions = %d, %v; want 1 (only the running one of this cron)", n, err)
	}
	if row := cf.session(sidA); row.Status != "ended" || row.EndReason == nil || *row.EndReason != db.EndReasonStoppedByAgent {
		t.Errorf("stopped session: status %q reason %v", row.Status, row.EndReason)
	}
	if got := cf.grants(sidA); len(got) != 0 {
		t.Errorf("the stop left %d grants (their tokens must die)", len(got))
	}
	cf.k8s.mu.Lock()
	deleted := append([]string(nil), cf.k8s.deleted...)
	cf.k8s.mu.Unlock()
	if len(deleted) == 0 {
		t.Error("the Job was not deleted")
	}
	if row := cf.session(sidB); row.Status != "running" {
		t.Errorf("another cron's session was stopped: %q", row.Status)
	}

	if err := cf.svc.RevokeCronToken(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got := cf.core.revokedList(); len(got) != 1 || got[0] != mcpAcct+":"+cronTok {
		t.Errorf("revoked = %v", got)
	}
	cf.core.revokeErr = errors.New("core down")
	if err := cf.svc.RevokeCronToken(ctx, a); err == nil {
		t.Error("a failed revoke must be reported to the caller")
	}

	// OnPaused does both, and keeps going when the revoke fails.
	cf.svc.OnPaused(ctx, b, "paused after 3 failed runs in a row")
	if row := cf.session(sidB); row.Status != "ended" {
		t.Errorf("OnPaused left the session %q", row.Status)
	}
}

func TestWatchdogStopsAnOverlongRun(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	c := cf.newCron(func(c *db.Cron) { c.MaxRuntimeSeconds = 120 })

	over, err := cf.svc.Start(ctx, c, cf.run(c))
	if err != nil {
		t.Fatal(err)
	}
	fresh := newUUID()
	cf.insertCronSession(fresh, c.ID, mcpAcct, "running")
	orphan := newUUID()
	cf.insertCronSession(orphan, "d0d0d0d0-d0d0-4d0d-8d0d-d0d0d0d0d0d0", mcpAcct, "running") // its cron does not exist
	if _, err := cf.pool.Exec(ctx, `UPDATE sessions SET started_at = now() - interval '3 minutes' WHERE id = $1`, over); err != nil {
		t.Fatal(err)
	}

	if n := cf.svc.WatchdogOnce(ctx); n != 2 {
		t.Fatalf("watchdog stopped %d, want 2 (the overlong run and the orphan)", n)
	}
	if row := cf.session(over); row.Status != "ended" {
		t.Errorf("overlong session = %q, want ended", row.Status)
	}
	if got := cf.grants(over); len(got) != 0 {
		t.Errorf("overlong session kept %d grants", len(got))
	}
	cf.k8s.mu.Lock()
	deleted := len(cf.k8s.deleted)
	cf.k8s.mu.Unlock()
	if deleted == 0 {
		t.Error("the overlong session's Job was not deleted")
	}
	if row := cf.session(orphan); row.Status != "ended" {
		t.Errorf("orphan = %q", row.Status)
	}
	if row := cf.session(fresh); row.Status != "running" {
		t.Errorf("a run inside its limit was stopped: %q", row.Status)
	}
	if n := cf.svc.WatchdogOnce(ctx); n != 0 {
		t.Errorf("second pass stopped %d", n)
	}
}

func TestSessionActiveAndInfoBadge(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	c := cf.newCron(nil)
	live, dead := newUUID(), newUUID()
	cf.insertCronSession(live, c.ID, mcpAcct, "running")
	cf.insertCronSession(dead, c.ID, mcpAcct, "ended")
	if !cf.svc.SessionActive(ctx, live) || cf.svc.SessionActive(ctx, dead) || cf.svc.SessionActive(ctx, newUUID()) {
		t.Error("SessionActive: running true, ended false, unknown false")
	}

	row := cf.session(live)
	raw, _ := json.Marshal(sessionRowToInfo(*row, mcpAcct))
	if !strings.Contains(string(raw), `"cron_id":"`+c.ID+`"`) {
		t.Errorf("SessionInfo JSON lacks the cron id: %s", raw)
	}
	plain := newUUID()
	if err := db.InsertSession(ctx, cf.pool, plain, cf.dc.ID, "running", "/r", "r", "t", ""); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(sessionRowToInfo(*cf.session(plain), mcpAcct))
	if strings.Contains(string(raw), "cron_id") {
		t.Errorf("a session no cron started must not carry a cron id: %s", raw)
	}
}

// ---- sweeper ---------------------------------------------------------------------------------

func TestSweeper(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	c := cf.newCron(nil)
	specs := []mcpgw.GrantSpec{{ConnectionID: cf.connID, Name: mcpConn, URL: cf.up.url(),
		Tools: map[string]mcpgw.ToolGrant{"echo": {Mode: mcpgw.ModeAllow, Hash: cf.up.hash("echo")}}}}
	proof := mcpgw.Proof{AccountID: mcpAcct, TokenID: cronTok}

	liveSession := newUUID()
	cf.insertCronSession(liveSession, c.ID, mcpAcct, "running")
	if _, err := mcpgw.CreateGrants(ctx, cf.pool, liveSession, mcpAcct, proof, specs); err != nil {
		t.Fatal(err)
	}
	endedSession := newUUID()
	cf.insertCronSession(endedSession, c.ID, mcpAcct, "ended")
	if _, err := mcpgw.CreateGrants(ctx, cf.pool, endedSession, mcpAcct, proof, specs); err != nil {
		t.Fatal(err)
	}

	// Grants younger than the resume guard are never swept (MINOR 28): age them first.
	if _, err := cf.pool.Exec(ctx, `UPDATE session_mcp_grants SET created_at = now() - interval '11 minutes'`); err != nil {
		t.Fatal(err)
	}
	// Everything in order but one ended session's grant.
	rep := cf.svc.SweepOnce(ctx)
	if rep.OrphanGrants != 1 || rep.StaleGrants != 0 || len(rep.FlaggedCrons) != 0 {
		t.Fatalf("first sweep = %+v", rep)
	}
	if g := cf.grants(liveSession); len(g) != 1 {
		t.Fatalf("the live session's grant was removed")
	}
	if len(cf.grants(endedSession)) != 0 {
		t.Error("the ended session's grant survived")
	}

	// Core cannot be asked: nothing further is removed or flagged.
	cf.list.err = errors.New("core down")
	rep = cf.svc.SweepOnce(ctx)
	if !rep.CoreUnreachable || rep.StaleGrants != 0 || len(cf.grants(liveSession)) != 1 {
		t.Errorf("sweep with core down = %+v", rep)
	}
	cf.list.err = nil

	// The connection is deleted: the live grant goes, the cron is flagged but kept as written.
	cf.list.conns = nil
	rep = cf.svc.SweepOnce(ctx)
	if rep.StaleGrants != 1 || len(cf.grants(liveSession)) != 0 {
		t.Errorf("stale grant not removed: %+v", rep)
	}
	if got := rep.FlaggedCrons[c.ID]; len(got) != 1 || !strings.Contains(got[0], cf.connID) {
		t.Errorf("flagged = %+v", rep.FlaggedCrons)
	}
	if got := cf.svc.MissingConnections(c.ID); len(got) != 1 {
		t.Errorf("MissingConnections = %v", got)
	}
	kept, err := db.GetCron(ctx, cf.pool, c.ID)
	if err != nil || string(kept.MCP) != string(c.MCP) && !jsonEqual(kept.MCP, c.MCP) {
		t.Errorf("the sweeper must never edit or delete a cron: %v %s", err, kept.MCP)
	}
	// The next fire fails visibly with the reason.
	if _, err := cf.svc.Start(ctx, kept, cf.run(kept)); !isCredential(err) || !strings.Contains(err.Error(), cf.connID) {
		t.Errorf("the next fire: err = %v", err)
	}

	// The connection comes back: no longer flagged.
	cf.list.conns = []MCPConnection{{ID: cf.connID, Name: mcpConn, URL: cf.up.url(), AuthKind: "static", Status: "ok"}}
	if rep = cf.svc.SweepOnce(ctx); len(rep.FlaggedCrons) != 0 || len(cf.svc.MissingConnections(c.ID)) != 0 {
		t.Errorf("sweep after the connection returned = %+v", rep)
	}

	// A grant whose proof core no longer honours is removed too.
	if _, err := mcpgw.CreateGrants(ctx, cf.pool, liveSession, mcpAcct, proof, specs); err != nil {
		t.Fatal(err)
	}
	cf.list.err = ErrMCPProofInvalid
	rep = cf.svc.SweepOnce(ctx)
	if rep.StaleGrants != 1 || len(cf.grants(liveSession)) != 0 {
		t.Errorf("a dead proof's grant was kept: %+v", rep)
	}
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}

func ptr[T any](v T) *T { return &v }
