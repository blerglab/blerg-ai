package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const officialSource = "anthropics/claude-plugins-official"

func pe(p string) pluginspec.Entry { return pluginspec.Entry{Marketplace: officialSource, Plugin: p} }

// pluginHandler is the fake core's POST /internal/plugins/list.
func (f *fakeK8s) pluginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Internal-Key") != testInternalKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body internalPluginsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if (body.TokenID != "") == (body.SessionID != "") { // core: exactly one named proof
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.pluginRequests = append(f.pluginRequests, body)
	list, ok := f.pluginLists[body.AccountID]
	status := f.pluginStatus
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("boom"))
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(internalPluginsResponse{Plugins: list})
}

func newPluginJM(t *testing.T, f *fakeK8s, allow string) *JobManager {
	t.Helper()
	jm := newTestJobManager(t, f)
	jm.PluginAllow, _ = pluginspec.ParseAllowlist(allow)
	jm.PluginAllowRaw = allow
	return jm
}

func envValue(t *testing.T, f *fakeK8s, name string) (string, bool) {
	t.Helper()
	if len(f.created) == 0 {
		t.Fatal("no Job created")
	}
	e, ok := jobEnv(t, f.created[len(f.created)-1])[name]
	if !ok {
		return "", false
	}
	v, _ := e["value"].(string)
	return v, true
}

func TestStartPlanIncludesPluginsStageOnlyWithPlugins(t *testing.T) {
	ids := func(plan []protocol.StartStage) string {
		var out []string
		for _, s := range plan {
			out = append(out, s.ID)
		}
		return strings.Join(out, ",")
	}
	without := clusterStartPlan("org/repo", nil)
	if strings.Contains(ids(without), "plugins") {
		t.Fatalf("plan without plugins has the stage: %s", ids(without))
	}
	st := pluginStage([]pluginspec.Entry{pe("frontend-design"), pe("superpowers")}, "")
	with := clusterStartPlan("org/repo", st)
	if got := ids(with); got != "queued,schedule,image,connect,clone,plugins,engine,ready" {
		t.Fatalf("plan order = %s", got)
	}
	for _, s := range with {
		if s.ID == protocol.StagePlugins && (s.Label != "Installing plugins" || s.Detail != "frontend-design, superpowers" || s.State != protocol.StageStatePending) {
			t.Fatalf("plugins stage = %+v", s)
		}
	}
	if pluginStage(nil, "") != nil {
		t.Fatal("no plugins and no note must give no stage")
	}
	if s := pluginStage(nil, errPluginsUnavailable.Error()); s == nil || s.State != protocol.StageStateWarning || !strings.Contains(s.Detail, "your plugin list couldn't be loaded") {
		t.Fatalf("unavailable stage = %+v", s)
	}
}

func TestTrackerKeepsALatePluginsStageBeforeReady(t *testing.T) {
	var tr startTracker
	tr.begin("s", clusterStartPlan("org/repo", nil))
	tr.merge("s", []protocol.StartStage{{ID: protocol.StagePlugins, State: protocol.StageStateActive, Detail: "x"}}, true)
	a := tr.get("s")
	n := len(a.stages)
	if a.stages[n-1].ID != protocol.StageReady || a.stages[n-2].ID != protocol.StageEngine || a.stages[n-3].ID != protocol.StagePlugins || a.stages[n-4].ID != protocol.StageClone {
		t.Fatalf("stages = %+v", a.stages)
	}
}

// A warning stage is over but not green: later progress must not fold it to done, and it is not
// the "current" stage a failure would be pinned on.
func TestWarningStageSurvivesLaterProgress(t *testing.T) {
	var tr startTracker
	warn := stage(protocol.StagePlugins, "Installing plugins", protocol.StageStateWarning, "plugins skipped: your plugin list couldn't be loaded")
	tr.begin("s", clusterStartPlan("org/repo", &warn))
	tr.merge("s", []protocol.StartStage{{ID: protocol.StageEngine, State: protocol.StageStateActive}}, true)
	for _, s := range tr.get("s").stages {
		if s.ID == protocol.StagePlugins && s.State != protocol.StageStateWarning {
			t.Fatalf("plugins stage = %+v, want it to stay a warning", s)
		}
	}
	if cur, _ := tr.current("s"); cur.ID == protocol.StagePlugins {
		t.Fatal("a warning must not be the current stage")
	}
}

func TestCreateSessionJobCarriesPluginsForHumanSession(t *testing.T) {
	f := &fakeK8s{pluginLists: map[string][]pluginspec.Entry{"acct-1": {pe("frontend-design"), pe("superpowers")}}}
	jm := newPluginJM(t, f, "")
	spec := SessionJobSpec{SessionID: "s1", Repo: "r", SpawningAccountID: "acct-1", AuthSessionID: testSID}
	if err := jm.CreateSessionJob(spec); err != nil {
		t.Fatal(err)
	}
	raw, ok := envValue(t, f, "BLERG_RUNNER_PLUGINS")
	if !ok {
		t.Fatal("BLERG_RUNNER_PLUGINS not in the pod env")
	}
	var got []pluginspec.Entry
	if err := json.Unmarshal([]byte(raw), &got); err != nil || len(got) != 2 || got[0].Plugin != "frontend-design" {
		t.Fatalf("env = %q (%v)", raw, err)
	}
	if v, _ := envValue(t, f, "BLERG_RUNNER_PLUGIN_MARKETPLACES"); v != "" {
		t.Fatalf("empty operator allow-list must not be sent as an env value, got %q", v)
	}
	// Non-secret configuration: a literal value, never a secretKeyRef.
	if _, isRef := jobEnv(t, f.created[0])["BLERG_RUNNER_PLUGINS"]["valueFrom"]; isRef {
		t.Fatal("plugin list must be plain env, not secret-backed")
	}
	if len(f.pluginRequests) != 1 || f.pluginRequests[0].SessionID != testSID || f.pluginRequests[0].TokenID != "" || f.pluginRequests[0].Engine != "claude" {
		t.Fatalf("core request = %+v", f.pluginRequests)
	}
}

func TestCreateSessionJobPluginRequestForTokenSessionCarriesTokenNotSession(t *testing.T) {
	f := &fakeK8s{pluginLists: map[string][]pluginspec.Entry{"acct-1": {pe("superpowers")}}}
	jm := newPluginJM(t, f, "")
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s1", Repo: "r", SpawningAccountID: "acct-1", TokenID: "tok-9"}); err != nil {
		t.Fatal(err)
	}
	r := f.pluginRequests[0]
	if r.TokenID != "tok-9" || r.SessionID != "" {
		t.Fatalf("request = %+v", r)
	}
}

func TestCreateSessionJobPluginsFailSoft(t *testing.T) {
	for name, f := range map[string]*fakeK8s{
		"core 500":     {pluginStatus: http.StatusInternalServerError},
		"core 401":     {pluginStatus: http.StatusUnauthorized},
		"account gone": {},
		"empty list":   {pluginLists: map[string][]pluginspec.Entry{"acct-1": {}}},
	} {
		jm := newPluginJM(t, f, "")
		spec := SessionJobSpec{SessionID: "s1", Repo: "r", SpawningAccountID: "acct-1", AuthSessionID: testSID}
		if err := jm.CreateSessionJob(spec); err != nil {
			t.Fatalf("%s: the session must still start: %v", name, err)
		}
		if _, ok := envValue(t, f, "BLERG_RUNNER_PLUGINS"); ok {
			t.Errorf("%s: pod got plugins", name)
		}
		// Every failure to load (a 404 for a dead session included) is visible; an empty list is not a failure.
		probe := SessionJobSpec{SessionID: "p", Repo: "r", SpawningAccountID: "acct-1", AuthSessionID: testSID}
		jm.ResolvePlugins(context.Background(), &probe)
		if wantNote := name != "empty list"; (probe.PluginsNote != "") != wantNote {
			t.Errorf("%s: note = %q", name, probe.PluginsNote)
		}
	}

	// The panel says so when core failed (and only then).
	f := &fakeK8s{pluginStatus: http.StatusInternalServerError}
	jm := newPluginJM(t, f, "")
	spec := SessionJobSpec{SessionID: "s1", Repo: "r", SpawningAccountID: "acct-1", AuthSessionID: testSID}
	jm.ResolvePlugins(context.Background(), &spec)
	st := pluginStage(spec.Plugins, spec.PluginsNote)
	if st == nil || !strings.Contains(st.Detail, "your plugin list couldn't be loaded") {
		t.Fatalf("stage = %+v note=%q", st, spec.PluginsNote)
	}
	if strings.Contains(st.Detail, "boom") {
		t.Fatal("core's response body reached the start panel")
	}

	// Core unreachable altogether.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	jm.CoreURL = dead.URL
	spec = SessionJobSpec{SessionID: "s2", Repo: "r", SpawningAccountID: "acct-1", AuthSessionID: testSID}
	jm.ResolvePlugins(context.Background(), &spec)
	if spec.PluginsNote == "" || len(spec.Plugins) != 0 {
		t.Fatalf("unreachable core: %+v", spec)
	}
}

func TestPluginsOnlyForClaudeSessionsWithAnAccount(t *testing.T) {
	f := &fakeK8s{pluginLists: map[string][]pluginspec.Entry{"acct-1": {pe("superpowers")}}}
	jm := newPluginJM(t, f, "")
	for name, spec := range map[string]SessionJobSpec{
		"no account":   {SessionID: "a", Repo: "r"},
		"codex":        {SessionID: "b", Repo: "r", SpawningAccountID: "acct-1", AuthSessionID: testSID, Engine: "codex"},
		"hermes":       {SessionID: "c", Repo: "r", SpawningAccountID: "acct-1", AuthSessionID: testSID, Engine: "hermes"},
		"claude blank": {SessionID: "d", Repo: "r", SpawningAccountID: "acct-1", AuthSessionID: testSID, Engine: ""},
	} {
		before := len(f.pluginRequests)
		if err := jm.CreateSessionJob(spec); err != nil {
			t.Fatal(err)
		}
		_, has := envValue(t, f, "BLERG_RUNNER_PLUGINS")
		if want := name == "claude blank"; has != want {
			t.Errorf("%s: has plugins = %v, want %v", name, has, want)
		}
		if asked := len(f.pluginRequests) > before; asked != (name == "claude blank") {
			t.Errorf("%s: asked core = %v", name, asked)
		}
	}
}

func TestDisallowedMarketplaceIsSkippedWithNote(t *testing.T) {
	f := &fakeK8s{pluginLists: map[string][]pluginspec.Entry{"acct-1": {
		pe("superpowers"),
		{Marketplace: "evil/plugins", Plugin: "steal"},
		{Marketplace: officialSource, Plugin: "Bad Name"},
	}}}
	jm := newPluginJM(t, f, "")
	spec := SessionJobSpec{SessionID: "s1", Repo: "r", SpawningAccountID: "acct-1", AuthSessionID: testSID}
	if err := jm.CreateSessionJob(spec); err != nil {
		t.Fatal(err)
	}
	raw, _ := envValue(t, f, "BLERG_RUNNER_PLUGINS")
	if strings.Contains(raw, "steal") || strings.Contains(raw, "evil") || strings.Contains(raw, "Bad Name") || !strings.Contains(raw, "superpowers") {
		t.Fatalf("env = %s", raw)
	}
	s2 := SessionJobSpec{SessionID: "s2", Repo: "r", SpawningAccountID: "acct-1", AuthSessionID: testSID}
	jm.ResolvePlugins(context.Background(), &s2)
	if !strings.Contains(s2.PluginsNote, "2 skipped") {
		t.Fatalf("note = %q", s2.PluginsNote)
	}
	st := pluginStage(s2.Plugins, s2.PluginsNote)
	if !strings.Contains(st.Detail, "superpowers") || !strings.Contains(st.Detail, "marketplace not allowed") {
		t.Fatalf("stage = %+v", st)
	}

	// With a wider allow-list the same entry is fine, and the allow-list rides into the pod for its re-check.
	f2 := &fakeK8s{pluginLists: map[string][]pluginspec.Entry{"acct-1": {{Marketplace: "evil/plugins", Plugin: "steal"}}}}
	jm2 := newPluginJM(t, f2, "evil/plugins")
	if err := jm2.CreateSessionJob(spec); err != nil {
		t.Fatal(err)
	}
	if v, _ := envValue(t, f2, "BLERG_RUNNER_PLUGIN_MARKETPLACES"); v != "evil/plugins" {
		t.Fatalf("allow-list env = %q", v)
	}
}

func TestResumeCarriesPluginsThroughCreateSessionJob(t *testing.T) {
	f := &fakeK8s{pluginLists: map[string][]pluginspec.Entry{"acct-1": {pe("superpowers")}}}
	jm := newPluginJM(t, f, "")
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s1", Repo: "r", SpawningAccountID: "acct-1", AuthSessionID: testSID, Resume: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := envValue(t, f, "BLERG_RUNNER_PLUGINS"); !ok {
		t.Fatal("resumed pod has no plugins")
	}
	if v, _ := envValue(t, f, "BLERG_RUNNER_RESUME"); v != "1" {
		t.Fatal("not a resume")
	}
}

// resumeClusterSession, end to end through the sessions row: the launcher's resume carries the
// list, and a resume by anyone else (no spawning account handed on) does not.
func TestResumeClusterSessionCarriesPlugins(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration test")
	}
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	daemonID := "00000000-0000-0000-0000-0000000000a1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		requester string
		want      bool
	}{{"acct-1", true}, {"someone-else", false}} {
		sessionID := "00000000-0000-0000-0000-0000000000b" + string(rune('1'+i))
		if err := db.InsertSession(ctx, pool, sessionID, daemonID, "disconnected", "/workspace/proj", "proj", "T", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.SetSessionSpawningAccount(ctx, pool, sessionID, "acct-1"); err != nil {
			t.Fatal(err)
		}
		if err := db.SetSessionEngine(ctx, pool, sessionID, "claude"); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "UPDATE sessions SET status = 'disconnected' WHERE id = $1", sessionID); err != nil {
			t.Fatal(err)
		}
		f := &fakeK8s{pluginLists: map[string][]pluginspec.Entry{"acct-1": {pe("superpowers")}}}
		jm := newPluginJM(t, f, "")
		hub := NewHub()
		hub.SetJobManager(jm)
		resumeClusterSession(ctx, hub, pool, sessionID, "continue", tc.requester, testSID)
		if len(f.created) != 1 {
			t.Fatalf("%s: created %d Jobs", tc.requester, len(f.created))
		}
		if _, has := envValue(t, f, "BLERG_RUNNER_PLUGINS"); has != tc.want {
			t.Errorf("%s: has plugins = %v, want %v", tc.requester, has, tc.want)
		}
	}
}
