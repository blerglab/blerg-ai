package server

// The five spawn sites, the Job spec, the end paths and resume, for sessions that carry MCP
// grants (mcpstart.go). Every test here runs against a real database.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func (fx *mcpFx) owner() runnerPrincipal {
	return runnerPrincipal{Kind: "human", Sub: mcpAcct, Sid: testSID, Caps: []string{"session.start"}}
}

// mustGrant resolves the fixture's standard selection for the given target, as the scheduler
// would build a grant before calling StartSession.
func (fx *mcpFx) mustGrant(target grantTarget) *ResolvedGrant {
	fx.t.Helper()
	g, apiErr := fx.api.resolveGrant(context.Background(), humanWho(), echoSel(fx, "allow", fx.up.hash("echo")), target)
	if apiErr != nil {
		fx.t.Fatalf("resolveGrant: %v", apiErr)
	}
	return g
}

// siteOutcome is what one spawn path did with a grant.
type siteOutcome struct {
	sessionID string
	status    int    // the HTTP status (or the APIError's) the caller got
	msg       string // the refusal message
	config    *protocol.MCPGatewayConfig
}

func (o siteOutcome) accepted() bool { return o.status == http.StatusAccepted }

type spawnSite struct {
	name   string
	daemon bool // runs on the daemon (Docker sandbox), not in a pod
	// start runs the path with the fixture's standard selection (public routes) or the given
	// pre-resolved grant (StartSession); runtimeOverride, when set, replaces the runtime.
	start func(fx *mcpFx, grant *ResolvedGrant, runtimeOverride string) siteOutcome
}

func (fx *mcpFx) publicOutcome(rec *httptest.ResponseRecorder, daemon bool) siteOutcome {
	fx.t.Helper()
	if rec.Code != http.StatusAccepted {
		return siteOutcome{status: rec.Code, msg: rec.Body.String()}
	}
	id := fx.sessionID(rec)
	return siteOutcome{sessionID: id, status: rec.Code, config: fx.deliveredConfig(id, daemon)}
}

// deliveredConfig reads the grant config the accepted session received: from its Kubernetes
// Secret (cluster) or from the spawn message the daemon got.
func (fx *mcpFx) deliveredConfig(sessionID string, daemon bool) *protocol.MCPGatewayConfig {
	fx.t.Helper()
	if !daemon {
		return fx.secretConfig(sessionID)
	}
	msg := recvSpawn(fx.t, fx.dc)
	if msg.SessionID != sessionID || !msg.Sandbox || msg.Kind != "agent" {
		fx.t.Fatalf("spawn message = %+v", msg)
	}
	return msg.MCPGateway
}

func (fx *mcpFx) startOutcome(resp StartResponse, apiErr *APIError, daemon bool) siteOutcome {
	fx.t.Helper()
	if apiErr != nil {
		return siteOutcome{status: apiErr.Status, msg: apiErr.Message}
	}
	return siteOutcome{sessionID: resp.SessionID, status: http.StatusAccepted, config: fx.deliveredConfig(resp.SessionID, daemon)}
}

func spawnSites() []spawnSite {
	pub := func(base map[string]any, daemon bool) func(*mcpFx, *ResolvedGrant, string) siteOutcome {
		return func(fx *mcpFx, _ *ResolvedGrant, runtimeOverride string) siteOutcome {
			b := map[string]any{"kind": "agent", "mcp": fx.selection()}
			for k, v := range base {
				b[k] = v
			}
			if daemon {
				b["daemon_id"] = fx.dc.ID
			}
			if runtimeOverride != "" {
				b["runtime"] = runtimeOverride
			}
			return fx.publicOutcome(fx.do(fx.api.HandlePostSessions, fx.human(), b), daemon)
		}
	}
	inProc := func(req RunnerStartRequest, daemon bool) func(*mcpFx, *ResolvedGrant, string) siteOutcome {
		return func(fx *mcpFx, grant *ResolvedGrant, runtimeOverride string) siteOutcome {
			r := req
			r.Grant = grant
			if runtimeOverride != "" {
				r.Runtime = runtimeOverride
			}
			resp, apiErr := fx.api.StartSession(context.Background(), fx.owner(), r, "")
			return fx.startOutcome(resp, apiErr, daemon)
		}
	}
	return []spawnSite{
		{"POST /api/sessions cluster, repo", false, pub(map[string]any{"repo": "acme/widget", "runtime": "cluster"}, false)},
		{"POST /api/sessions cluster, no repo", false, pub(map[string]any{"no_repo": true, "runtime": "cluster"}, false)},
		{"POST /api/sessions daemon", true, pub(map[string]any{"repo": "app", "runtime": "docker"}, true)},
		{"StartSession cluster", false, inProc(RunnerStartRequest{Repo: "acme/widget", Prompt: "p", Runtime: "cluster"}, false)},
		{"StartSession daemon", true, inProc(RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker"}, true)},
	}
}

func (s spawnSite) target(fx *mcpFx) grantTarget {
	if s.daemon {
		return fx.dockerTarget()
	}
	return clusterTarget()
}

// noSideEffects fails when a refused start left anything behind.
func (fx *mcpFx) noSideEffects(what string) {
	fx.t.Helper()
	if n := fx.count("sessions"); n != 0 {
		fx.t.Errorf("%s: %d session rows left behind", what, n)
	}
	if n := fx.count("session_mcp_grants"); n != 0 {
		fx.t.Errorf("%s: %d grants left behind", what, n)
	}
	fx.k8s.mu.Lock()
	jobs, secrets := len(fx.k8s.created), len(fx.k8s.createdSecrets)
	fx.k8s.mu.Unlock()
	if jobs != 0 || secrets != 0 {
		fx.t.Errorf("%s: %d Jobs and %d Secrets created", what, jobs, secrets)
	}
	select {
	case m := <-fx.dc.send:
		fx.t.Errorf("%s: a message reached the daemon: %s", what, m)
	default:
	}
}

// THE acceptance test: every spawn path, given a grant, either attaches it (grant rows, a
// private session owned by the caller, the token delivered in the ONLY place that runtime
// gets one) or refuses with a specific message and leaves nothing behind. None drops it.
func TestEverySpawnSiteAttachesOrRefusesTheGrant(t *testing.T) {
	for _, site := range spawnSites() {
		t.Run(site.name+"/attaches", func(t *testing.T) {
			fx := newMCPFx(t)
			out := site.start(fx, fx.mustGrant(site.target(fx)), "")
			if !out.accepted() {
				t.Fatalf("refused: %d %s", out.status, out.msg)
			}
			fx.assertGranted(out.sessionID, out.config)
		})

		refusals := []struct {
			name   string
			mutate func(fx *mcpFx)
			only   func(s spawnSite) bool
			want   string
			status int
		}{
			{name: "gateway not running", want: msgMCPNoGateway, status: 503, mutate: func(fx *mcpFx) {
				cfg := *fx.hub.mcpStart.get()
				cfg.Hasher = nil
				fx.api.setMCPStart(&cfg)
			}},
			{name: "no gateway address for sessions", want: msgMCPNoGatewayURL, status: 503, mutate: func(fx *mcpFx) {
				cfg := *fx.hub.mcpStart.get()
				cfg.GatewayURL = ""
				fx.api.setMCPStart(&cfg)
			}},
			{name: "daemon without mcp_gateway", want: msgMCPDaemonOld, status: 422,
				only:   func(s spawnSite) bool { return s.daemon },
				mutate: func(fx *mcpFx) { fx.dc.SetMCPGateway(false) }},
		}
		for _, r := range refusals {
			if r.only != nil && !r.only(site) {
				continue
			}
			t.Run(site.name+"/refuses: "+r.name, func(t *testing.T) {
				fx := newMCPFx(t)
				grant := fx.mustGrant(site.target(fx)) // resolved while everything works, like the scheduler's
				r.mutate(fx)
				out := site.start(fx, grant, "")
				if out.accepted() || out.status != r.status || !strings.Contains(out.msg, r.want) {
					t.Fatalf("outcome = %d %q, want %d containing %q", out.status, out.msg, r.status, r.want)
				}
				fx.noSideEffects(site.name)
			})
		}

		if site.daemon {
			// Never on the bare host, however the daemon path is reached.
			t.Run(site.name+"/refuses the bare host", func(t *testing.T) {
				fx := newMCPFx(t)
				grant := fx.mustGrant(site.target(fx))
				out := site.start(fx, grant, "daemon")
				if out.accepted() || out.status != http.StatusUnprocessableEntity || !strings.Contains(out.msg, msgMCPRuntime) {
					t.Fatalf("outcome = %d %q, want 422 %q", out.status, out.msg, msgMCPRuntime)
				}
				fx.noSideEffects(site.name)
			})
		}
	}
}

// On the public route every non-cluster, non-docker runtime is the bare host.
func TestPostSessionsMCPRuntimeRefusals(t *testing.T) {
	for _, rt := range []string{"", "daemon"} {
		fx := newMCPFx(t)
		rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
			"daemon_id": fx.dc.ID, "repo": "app", "runtime": rt, "kind": "agent", "mcp": fx.selection(),
		})
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), msgMCPRuntime) {
			t.Errorf("runtime %q: %d %s, want 422 %q", rt, rec.Code, rec.Body.String(), msgMCPRuntime)
		}
		fx.noSideEffects("runtime " + rt)
	}
	// engine and kind, on a real route
	fx := newMCPFx(t)
	for _, tc := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": "agent", "engine": "codex"}, msgMCPEngine},
		{map[string]any{"daemon_id": fx.dc.ID, "repo": "app", "runtime": "docker", "kind": ""}, msgMCPKind},
		{map[string]any{"daemon_id": fx.dc.ID, "repo": "app", "runtime": "docker", "kind": "tmux"}, msgMCPKind},
	} {
		tc.body["mcp"] = fx.selection()
		rec := fx.do(fx.api.HandlePostSessions, fx.human(), tc.body)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(errorText(t, rec.Body.Bytes()), tc.want) {
			t.Errorf("%v: %d %s, want 422 %q", tc.body, rec.Code, rec.Body.String(), tc.want)
		}
	}
	fx.noSideEffects("engine and kind")
}

// A source-level guard behind the behavioural test above: every place that builds a spawn
// message or a Job spec is known, and the grant-aware ones call the shared functions. A new
// spawn path that does not decide about grants fails here.
func TestSpawnSitesAreEnumerated(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	type fnInfo struct {
		calls    map[string]int // selector-call name -> count
		literals map[string]bool
	}
	funcs := map[string]*fnInfo{}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", n), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			info := &fnInfo{calls: map[string]int{}, literals: map[string]bool{}}
			ast.Inspect(fd.Body, func(node ast.Node) bool {
				switch x := node.(type) {
				case *ast.CallExpr:
					if s, ok := x.Fun.(*ast.SelectorExpr); ok {
						info.calls[s.Sel.Name]++
					} else if id, ok := x.Fun.(*ast.Ident); ok {
						info.calls[id.Name]++
					}
				case *ast.CompositeLit:
					switch ty := x.Type.(type) {
					case *ast.SelectorExpr:
						if ty.Sel.Name == "SpawnSession" {
							info.literals["SpawnSession"] = true
						}
					case *ast.Ident:
						if ty.Name == "SessionJobSpec" {
							info.literals["SessionJobSpec"] = true
						}
					}
				}
				return true
			})
			funcs[fd.Name.Name] = info
		}
	}

	// Grant-aware paths: they resolve or check a grant, attach it, or (resume) re-issue it.
	aware := map[string]struct{ decide, attach string }{
		"HandlePostSessions":        {"resolveGrant", "attachGrant"},
		"startClusterNoRepo":        {"resolveGrant", "attachGrant"},
		"startRunnerSession":        {"checkGrant", "attachGrant"},
		"startBoardSessionOnDaemon": {"checkGrant", "attachGrant"},
		"resumeClusterSession":      {"grantResumeProblem", "reissueGrants"},
	}
	// Paths that never carry a grant, by construction: nothing they decode names one.
	grantless := map[string]bool{"HandlePostAssist": true, "forwardBrowserSpawn": true, "CreateSessionJob": true}

	var found []string
	for name, info := range funcs {
		if !info.literals["SpawnSession"] && !info.literals["SessionJobSpec"] {
			continue
		}
		found = append(found, name)
		if a, ok := aware[name]; ok {
			if info.calls[a.decide] == 0 || info.calls[a.attach] == 0 {
				t.Errorf("%s builds a spawn but does not call %s and %s", name, a.decide, a.attach)
			}
			continue
		}
		if !grantless[name] {
			t.Errorf("%s builds a SpawnSession or SessionJobSpec but is not a known spawn site: decide whether it carries a MCP grant and register it here", name)
		}
	}
	sort.Strings(found)
	for name := range aware {
		if funcs[name] == nil || (!funcs[name].literals["SpawnSession"] && !funcs[name].literals["SessionJobSpec"]) {
			t.Errorf("known spawn site %s no longer builds a spawn (found: %v)", name, found)
		}
	}
	// The public route has two grant-carrying branches (cluster and daemon); StartSession's
	// paths have one each.
	if c := funcs["HandlePostSessions"]; c.calls["attachGrant"] < 2 || c.calls["resolveGrant"] < 2 {
		t.Errorf("HandlePostSessions must resolve and attach on both the cluster and the daemon branch: %v", c.calls)
	}
	// attachGrant is called only from the spawn sites.
	for name, info := range funcs {
		if info.calls["attachGrant"] > 0 {
			if _, ok := aware[name]; !ok {
				t.Errorf("attachGrant called from %s, which is not a registered spawn site", name)
			}
		}
	}
}

// ---- the Job spec ------------------------------------------------------------------------

func TestGrantJobSpecHoldsNoTokenOnlyASecretRef(t *testing.T) {
	fx := newMCPFx(t)
	jm := fx.hub.JobManager()
	jm.PluginAllow, _ = pluginspec.ParseAllowlist(officialSource)
	jm.PluginAllowRaw = officialSource
	fx.k8s.pluginLists = map[string][]pluginspec.Entry{mcpAcct: {pe("some-plugin")}}

	// Control: an ordinary session on the same install does get the always-on plugins.
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": "agent"})
	fx.sessionID(rec)
	if len(fx.k8s.pluginRequests) != 1 {
		t.Fatalf("control session fetched plugins %d times, want 1", len(fx.k8s.pluginRequests))
	}
	if _, ok := jobEnv(t, fx.k8s.created[0])["BLERG_RUNNER_PLUGINS"]; !ok {
		t.Fatal("control session has no BLERG_RUNNER_PLUGINS env")
	}

	// The grant session.
	rec = fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"repo": "acme/widget", "runtime": "cluster", "kind": "agent", "mcp": fx.selection(),
	})
	sid := fx.sessionID(rec)
	cfg := fx.secretConfig(sid)
	if len(fx.k8s.pluginRequests) != 1 {
		t.Errorf("the grant session fetched plugins (%d requests): they must be off", len(fx.k8s.pluginRequests))
	}
	if len(fx.k8s.created) != 2 {
		t.Fatalf("%d Jobs created", len(fx.k8s.created))
	}
	rawJob, err := json.Marshal(fx.k8s.created[1])
	if err != nil {
		t.Fatal(err)
	}
	job := string(rawJob)
	if strings.Contains(job, "gw_") {
		t.Error("a gateway token prefix appears in the marshalled Job")
	}
	for _, s := range cfg.Servers {
		if s.Token == "" || strings.Contains(job, s.Token) {
			t.Errorf("token of %q appears in the Job spec", s.Name)
		}
	}
	if strings.Contains(job, cfg.BaseURL) {
		t.Error("the gateway address travels in the Job spec: it belongs to the Secret with the tokens")
	}

	env := jobEnv(t, fx.k8s.created[1])
	entry, ok := env["BLERG_RUNNER_MCP_CONFIG"]
	if !ok {
		t.Fatal("no BLERG_RUNNER_MCP_CONFIG env in the Job")
	}
	if _, literal := entry["value"]; literal {
		t.Error("BLERG_RUNNER_MCP_CONFIG carries a literal value")
	}
	ref, _ := entry["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
	if ref["name"] != sessionSecretName(sid) || ref["key"] != "BLERG_RUNNER_MCP_CONFIG" || ref["optional"] != false {
		t.Errorf("secretKeyRef = %v", ref)
	}
	for name := range env {
		if strings.Contains(name, "PLUGIN") || strings.Contains(name, "PREVIEW") {
			t.Errorf("a grant session pod gets env %s", name)
		}
	}
	// The Secret is where the config (and the tokens) live.
	if len(cfg.Servers) != 1 || !strings.HasPrefix(cfg.Servers[0].Token, "gw_") || cfg.BaseURL != mcpGatewayURL {
		t.Errorf("Secret config = %+v", cfg)
	}
}

func TestCreateSessionJobRefusesAnMCPEnvKeyFromTheCaller(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "abc", Repo: "proj",
		ExtraEnv:   map[string]string{"BLERG_RUNNER_MCP_CONFIG": `{"base_url":"http://x","servers":[]}`},
		MCPGateway: &protocol.MCPGatewayConfig{BaseURL: "http://gw", Servers: []protocol.MCPGatewayServer{{Name: "a", Token: "gw_x"}}},
	})
	if err == nil || len(f.created) != 0 {
		t.Fatalf("err = %v, created = %d: the caller's env must not shadow the grant", err, len(f.created))
	}
}

// ---- idempotency ---------------------------------------------------------------------------

// A retry of a start under the same key answers with the first session and makes no second grant.
func TestStartSessionWithGrantIsIdempotent(t *testing.T) {
	fx := newMCPFx(t)
	req := RunnerStartRequest{Repo: "acme/widget", Prompt: "p", Runtime: "cluster", Grant: fx.mustGrant(clusterTarget())}
	first, apiErr := fx.api.StartSession(context.Background(), fx.owner(), req, "cron:1:run-1")
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	again, apiErr := fx.api.StartSession(context.Background(), fx.owner(), req, "cron:1:run-1")
	if apiErr != nil || !again.Replayed || again.SessionID != first.SessionID {
		t.Fatalf("retry = %+v, %v", again, apiErr)
	}
	if fx.count("session_mcp_grants") != 1 || len(fx.k8s.created) != 1 {
		t.Errorf("grants %d Jobs %d after a retry", fx.count("session_mcp_grants"), len(fx.k8s.created))
	}
}

// ---- session end: grants die with the session ---------------------------------------------

// Every real end path deletes the session's grants, so its tokens stop resolving.
func TestGrantsDieOnEveryEndPath(t *testing.T) {
	ctx := context.Background()
	setStatus := func(fx *mcpFx, sid, status string) {
		fx.t.Helper()
		if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status = $2 WHERE id = $1`, sid, status); err != nil {
			fx.t.Fatal(err)
		}
	}
	paths := []struct {
		name   string
		daemon bool
		end    func(fx *mcpFx, sid string)
	}{
		{"daemon session_ended (HandleSessionEnded)", true, func(fx *mcpFx, sid string) {
			HandleSessionEnded(ctx, fx.hub, fx.pool, protocol.SessionEnded{Type: "session_ended", SessionID: sid, ExitCode: 0})
		}},
		{"daemon reports stopped (HandleSessionStateChanged)", true, func(fx *mcpFx, sid string) {
			setStatus(fx, sid, "running")
			if err := HandleSessionStateChanged(ctx, fx.hub, fx.pool, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: sid, Status: "stopped"}); err != nil {
				fx.t.Fatal(err)
			}
		}},
		{"runner stop of a daemon session (Stop)", true, func(fx *mcpFx, sid string) {
			if _, apiErr := fx.api.Stop(ctx, fx.owner(), sid); apiErr != nil {
				fx.t.Fatal(apiErr)
			}
		}},
		{"runner stop of a cluster session (Stop)", false, func(fx *mcpFx, sid string) {
			if _, apiErr := fx.api.Stop(ctx, fx.owner(), sid); apiErr != nil {
				fx.t.Fatal(apiErr)
			}
		}},
		{"cluster Job finished (reconciler)", false, func(fx *mcpFx, sid string) {
			fx.k8s.setJobStatus(fx.hub.JobManager().jobName(sid), map[string]any{"succeeded": 1})
			fx.api.reconcileClusterJobsOnce(ctx)
		}},
		{"cluster Job failed (reconciler)", false, func(fx *mcpFx, sid string) {
			fx.k8s.setJobStatus(fx.hub.JobManager().jobName(sid), map[string]any{"failed": 1})
			fx.api.reconcileClusterJobsOnce(ctx)
		}},
		{"lost daemon finalised (reconciler)", true, func(fx *mcpFx, sid string) {
			if err := db.SetSessionDaemonLost(ctx, fx.pool, sid); err != nil {
				fx.t.Fatal(err)
			}
			if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status = 'error', daemon_lost_at = now() - interval '2 hours' WHERE id = $1`, sid); err != nil {
				fx.t.Fatal(err)
			}
			fx.api.finaliseLostDaemonSessions(ctx)
		}},
		{"delete of a disconnected cluster session (DELETE /api/sessions/{id})", false, func(fx *mcpFx, sid string) {
			setStatus(fx, sid, "disconnected")
			req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sid, nil)
			req.SetPathValue("id", sid)
			req.Header.Set("Authorization", "Bearer "+fx.human())
			rec := httptest.NewRecorder()
			fx.api.HandleDeleteSession(rec, req)
			if rec.Code != http.StatusNoContent {
				fx.t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
			}
		}},
		{"spawn aborted (abortSpawnSessionToken)", true, func(fx *mcpFx, sid string) {
			abortSpawnSessionToken(ctx, fx.pool, sid)
		}},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			fx := newMCPFx(t)
			var out siteOutcome
			if p.daemon {
				out = spawnSites()[2].start(fx, nil, "")
			} else {
				out = spawnSites()[0].start(fx, nil, "")
			}
			if !out.accepted() {
				t.Fatalf("start refused: %d %s", out.status, out.msg)
			}
			token := out.config.Servers[0].Token
			if !fx.tokenBelongsTo(token, out.sessionID) || len(fx.grants(out.sessionID)) != 1 {
				t.Fatal("the grant is not live before the end")
			}
			p.end(fx, out.sessionID)
			if fx.tokenBelongsTo(token, out.sessionID) || len(fx.grants(out.sessionID)) != 0 {
				t.Errorf("the grant survived: %v", fx.grants(out.sessionID))
			}
		})
	}
}

// A start that fails after the grant was made undoes the grant with the rest.
func TestFailedStartsLeaveNoGrant(t *testing.T) {
	t.Run("cluster Job create fails", func(t *testing.T) {
		fx := newMCPFx(t)
		fx.k8s.failNext = true
		rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
			"repo": "acme/widget", "runtime": "cluster", "kind": "agent", "mcp": fx.selection(),
		})
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status %d, want 503: %s", rec.Code, rec.Body.String())
		}
		if n := fx.count("session_mcp_grants"); n != 0 {
			t.Errorf("%d grants left after a failed Job create", n)
		}
	})
	t.Run("cluster Job create fails on StartSession", func(t *testing.T) {
		fx := newMCPFx(t)
		fx.k8s.failNext = true
		_, apiErr := fx.api.StartSession(context.Background(), fx.owner(),
			RunnerStartRequest{Repo: "acme/widget", Prompt: "p", Runtime: "cluster", Grant: fx.mustGrant(clusterTarget())}, "")
		if apiErr == nil {
			t.Fatal("expected a failure")
		}
		if n := fx.count("session_mcp_grants"); n != 0 {
			t.Errorf("%d grants left after a failed Job create", n)
		}
	})
	t.Run("daemon send buffer full", func(t *testing.T) {
		fx := newMCPFx(t)
		for len(fx.dc.send) < cap(fx.dc.send) {
			fx.dc.send <- []byte("{}")
		}
		rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
			"daemon_id": fx.dc.ID, "repo": "app", "runtime": "docker", "kind": "agent", "mcp": fx.selection(),
		})
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status %d, want 503: %s", rec.Code, rec.Body.String())
		}
		if fx.count("session_mcp_grants") != 0 || fx.count("sessions") != 0 {
			t.Error("a start that never reached the daemon left a grant or a row")
		}
	})
	t.Run("daemon send buffer full on StartSession", func(t *testing.T) {
		fx := newMCPFx(t)
		grant := fx.mustGrant(fx.dockerTarget())
		for len(fx.dc.send) < cap(fx.dc.send) {
			fx.dc.send <- []byte("{}")
		}
		if _, apiErr := fx.api.StartSession(context.Background(), fx.owner(),
			RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker", Grant: grant}, ""); apiErr == nil {
			t.Fatal("expected a failure")
		}
		if fx.count("session_mcp_grants") != 0 || fx.count("sessions") != 0 {
			t.Error("a start that never reached the daemon left a grant or a row")
		}
	})
}

// ---- resume ---------------------------------------------------------------------------------

func TestResumeReissuesGrantsAndRefusesOthers(t *testing.T) {
	ctx := context.Background()
	fx := newMCPFx(t)
	out := spawnSites()[0].start(fx, nil, "")
	if !out.accepted() {
		t.Fatalf("start: %d %s", out.status, out.msg)
	}
	sid, oldToken := out.sessionID, out.config.Servers[0].Token
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	oldGrant := fx.grants(sid)[0]

	// Refused: another account, the launcher without a login session, no account at all.
	for name, tc := range map[string]struct{ account, sid string }{
		"another account":         {"someone-else", "sid-9"},
		"launcher, no login":      {mcpAcct, ""},
		"no account (runner key)": {"", "sid-9"},
	} {
		resumeClusterSession(ctx, fx.hub, fx.pool, sid, "carry on", tc.account, tc.sid)
		if len(fx.k8s.created) != 1 {
			t.Errorf("%s: a Job was created for a refused resume", name)
		}
		if g := fx.grants(sid); len(g) != 1 || string(g[0].TokenHash) != string(oldGrant.TokenHash) {
			t.Errorf("%s: the grant changed on a refused resume", name)
		}
	}

	// Through SendMessage, where the caller can be told: an agent token acting for the launcher
	// has no login session, and another account cannot even see the private session.
	agent := runnerPrincipal{Kind: "agent", Sub: "tok-1", OnBehalfOf: mcpAcct, Caps: []string{"session.start"}}
	if _, apiErr := fx.api.SendMessage(ctx, agent, sid, "hello", ""); apiErr == nil || apiErr.Status != http.StatusUnauthorized {
		t.Errorf("agent token resume = %v, want 401", apiErr)
	}
	stranger := runnerPrincipal{Kind: "human", Sub: "someone-else", Sid: "sid-9", Caps: []string{"session.start"}}
	if _, apiErr := fx.api.SendMessage(ctx, stranger, sid, "hello", ""); apiErr == nil || apiErr.Status != http.StatusNotFound {
		t.Errorf("another account's resume = %v, want 404", apiErr)
	}
	if _, apiErr := fx.api.SendMessage(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind}, sid, "hello", ""); apiErr == nil {
		t.Error("the runner key resumed a private grant session")
	}
	if len(fx.k8s.created) != 1 {
		t.Fatal("a refused resume created a Job")
	}

	// The launcher, signed in with a new login session: new tokens, old ones dead.
	body, apiErr := fx.api.SendMessage(ctx, runnerPrincipal{Kind: "human", Sub: mcpAcct, Sid: "sid-2", Caps: []string{"session.start"}}, sid, "carry on", "")
	if apiErr != nil || body["resumed"] != true {
		t.Fatalf("resume = %v, %v", body, apiErr)
	}
	if len(fx.k8s.created) != 2 {
		t.Fatalf("%d Jobs, want 2", len(fx.k8s.created))
	}
	fx.k8s.mu.Lock()
	var configs []protocol.MCPGatewayConfig
	for _, s := range fx.k8s.createdSecrets {
		sd, _ := s["stringData"].(map[string]any)
		if raw, ok := sd["BLERG_RUNNER_MCP_CONFIG"].(string); ok {
			var c protocol.MCPGatewayConfig
			if err := json.Unmarshal([]byte(raw), &c); err != nil {
				t.Fatal(err)
			}
			configs = append(configs, c)
		}
	}
	fx.k8s.mu.Unlock()
	if len(configs) != 2 {
		t.Fatalf("%d Secrets hold a gateway config, want 2", len(configs))
	}
	newToken := configs[1].Servers[0].Token
	if newToken == "" || newToken == oldToken {
		t.Fatalf("the resumed pod got token %q (old %q): it must be new", newToken, oldToken)
	}
	if fx.tokenBelongsTo(oldToken, sid) {
		t.Error("the old token still works after the resume")
	}
	if !fx.tokenBelongsTo(newToken, sid) {
		t.Error("the new token is not live")
	}
	g := fx.grants(sid)
	if len(g) != 1 || g[0].ProofKind != "session_id" || g[0].ProofValue != "sid-2" || g[0].AccountID != mcpAcct ||
		g[0].CallBudget != oldGrant.CallBudget || g[0].URLSnapshot != oldGrant.URLSnapshot || g[0].Tools["echo"] != oldGrant.Tools["echo"] {
		t.Errorf("re-issued grant = %+v, old = %+v", g, oldGrant)
	}
	if row, _ := db.GetSession(ctx, fx.pool, sid); row == nil || !row.Private {
		t.Error("the session is no longer private")
	}
	if _, ok := jobEnv(t, fx.k8s.created[1])["BLERG_RUNNER_RESUME"]; !ok {
		t.Error("the second Job is not a resume")
	}
	rawJob, _ := json.Marshal(fx.k8s.created[1])
	if strings.Contains(string(rawJob), "gw_") {
		t.Error("a token is in the resumed Job spec")
	}
}

// A resume that cannot re-issue (the gateway went away) starts nothing and keeps the old grants.
func TestResumeWithoutGatewayStartsNothing(t *testing.T) {
	ctx := context.Background()
	fx := newMCPFx(t)
	out := spawnSites()[0].start(fx, nil, "")
	if !out.accepted() {
		t.Fatal(out.msg)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, out.sessionID); err != nil {
		t.Fatal(err)
	}
	before := fx.grants(out.sessionID)[0]
	cfg := *fx.hub.mcpStart.get()
	cfg.Hasher = nil
	fx.api.setMCPStart(&cfg)
	resumeClusterSession(ctx, fx.hub, fx.pool, out.sessionID, "carry on", mcpAcct, "sid-2")
	if len(fx.k8s.created) != 1 {
		t.Error("a Job was created for a resume that could not be granted")
	}
	if g := fx.grants(out.sessionID); len(g) != 1 || string(g[0].TokenHash) != string(before.TokenHash) {
		t.Error("the grants changed although the re-issue failed")
	}
}

// A session with no grants resumes exactly as before (no gateway config, plugins as usual).
func TestResumeWithoutGrantsIsUnchanged(t *testing.T) {
	ctx := context.Background()
	fx := newMCPFx(t)
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": "agent"})
	sid := fx.sessionID(rec)
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	resumeClusterSession(ctx, fx.hub, fx.pool, sid, "carry on", "someone-else", "sid-9") // resumes on the operator credentials, as ever
	if len(fx.k8s.created) != 2 {
		t.Fatalf("%d Jobs, want 2", len(fx.k8s.created))
	}
	if _, ok := jobEnv(t, fx.k8s.created[1])["BLERG_RUNNER_MCP_CONFIG"]; ok {
		t.Error("a session without grants got a gateway config")
	}
}

// ---- the daemon hello ------------------------------------------------------------------------

func TestDaemonConnRecordsTheMCPGatewayCapability(t *testing.T) {
	dc := &DaemonConn{ID: "d"}
	if dc.CanMCPGateway() {
		t.Error("a daemon that has not said so must not be assumed to deliver grants")
	}
	dc.SetMCPGateway(true)
	if !dc.CanMCPGateway() {
		t.Error("capability not recorded")
	}
	dc.SetMCPGateway(false)
	if dc.CanMCPGateway() {
		t.Error("capability not cleared")
	}
}
