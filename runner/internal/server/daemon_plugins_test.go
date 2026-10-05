package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func planIDs(plan []protocol.StartStage) string {
	var out []string
	for _, s := range plan {
		out = append(out, s.ID)
	}
	return strings.Join(out, ",")
}

// The plugin stage sits between the daemon and sandbox stages: the daemon
// installs before the container starts (the cache is mounted at docker run).
func TestDaemonStartPlanPutsPluginsBeforeTheSandbox(t *testing.T) {
	if got := planIDs(daemonStartPlan("laptop", true, nil)); got != "queued,daemon,sandbox,engine,ready" {
		t.Fatalf("plan without plugins = %s", got)
	}
	st := pluginStage([]pluginspec.Entry{pe("superpowers")}, "")
	if got := planIDs(daemonStartPlan("laptop", true, st)); got != "queued,daemon,plugins,sandbox,engine,ready" {
		t.Fatalf("sandbox plan = %s", got)
	}
	if got := planIDs(daemonStartPlan("laptop", false, st)); got != "queued,daemon,plugins,engine,ready" {
		t.Fatalf("host plan = %s", got)
	}
}

func pluginAPI(t *testing.T, list []pluginspec.Entry, err error) (*API, *DaemonConn, *int) {
	t.Helper()
	api, dc := runtimeTestAPI(t)
	api.SetCoreCredentials("http://core.test", "internal-key")
	calls := 0
	api.fetchPlugins = func(_ context.Context, accountID string, proof coreProof) ([]pluginspec.Entry, error) {
		calls++
		if accountID != "user-1" || proof.SessionID != testSID || proof.TokenID != "" {
			t.Errorf("core asked for account %q proof %+v", accountID, proof)
		}
		return list, err
	}
	return api, dc, &calls
}

func spawnPlugins(t *testing.T, api *API, dc *DaemonConn, body map[string]any) (protocol.SpawnSession, []protocol.StartStage) {
	t.Helper()
	rec := postSpawn(t, api, body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	msg := recvNamedSpawn(t, dc)
	att := api.hub.starts.get(msg.SessionID)
	if att == nil {
		return msg, nil
	}
	return msg, att.stages
}

// The list travels only to a daemon that said it loads plugins, only for a
// Claude agent-kind session, and the start plan says so.
func TestPostSessionsDaemonCarriesPluginsOnlyWhereTheyLoad(t *testing.T) {
	list := []pluginspec.Entry{pe("superpowers"), pe("frontend-design")}
	agentBody := map[string]any{"daemon_id": "d1", "repo": "my-app", "kind": "agent", "runtime": "docker"}

	// An older daemon (no hello capability): nothing sent, no stage, core never asked.
	api, dc, calls := pluginAPI(t, list, nil)
	msg, stages := spawnPlugins(t, api, dc, agentBody)
	if len(msg.Plugins) != 0 || strings.Contains(planIDs(stages), "plugins") || *calls != 0 {
		t.Fatalf("old daemon: plugins=%v plan=%s core calls=%d", msg.Plugins, planIDs(stages), *calls)
	}

	api, dc, calls = pluginAPI(t, list, nil)
	dc.SetPlugins(true)
	msg, stages = spawnPlugins(t, api, dc, agentBody)
	if len(msg.Plugins) != 2 || msg.Plugins[0].Plugin != "superpowers" || *calls != 1 {
		t.Fatalf("capable daemon: plugins=%v core calls=%d", msg.Plugins, *calls)
	}
	if got := planIDs(stages); got != "queued,daemon,plugins,sandbox,engine,ready" {
		t.Fatalf("plan = %s", got)
	}

	// Terminal kind and non-Claude engines get none.
	for name, body := range map[string]map[string]any{
		"terminal": {"daemon_id": "d1", "repo": "my-app", "runtime": "docker"},
		"codex":    {"daemon_id": "d1", "repo": "my-app", "kind": "agent", "runtime": "docker", "engine": "codex"},
	} {
		api, dc, calls = pluginAPI(t, list, nil)
		dc.SetPlugins(true)
		if msg, _ := spawnPlugins(t, api, dc, body); len(msg.Plugins) != 0 || *calls != 0 {
			t.Errorf("%s session got plugins %v (core calls %d)", name, msg.Plugins, *calls)
		}
	}
}

// Core failing never fails the start: the session starts without plugins and
// the panel shows a warning stage saying why.
func TestPostSessionsDaemonPluginsFailSoft(t *testing.T) {
	api, dc, _ := pluginAPI(t, nil, errors.New("core down"))
	dc.SetPlugins(true)
	msg, stages := spawnPlugins(t, api, dc, map[string]any{"daemon_id": "d1", "repo": "my-app", "kind": "agent", "runtime": "daemon"})
	if len(msg.Plugins) != 0 {
		t.Fatalf("plugins sent despite the failure: %v", msg.Plugins)
	}
	var warn *protocol.StartStage
	for i := range stages {
		if stages[i].ID == protocol.StagePlugins {
			warn = &stages[i]
		}
	}
	if warn == nil || warn.State != protocol.StageStateWarning || !strings.Contains(warn.Detail, "couldn't be loaded") {
		t.Fatalf("plugin stage = %+v (plan %s)", warn, planIDs(stages))
	}
}

// Entries outside this server's allow-list are dropped here as well as on the daemon.
func TestPostSessionsDaemonPluginsHonourTheAllowList(t *testing.T) {
	api, dc, _ := pluginAPI(t, []pluginspec.Entry{pe("superpowers"), {Marketplace: "evil/market", Plugin: "x"}}, nil)
	dc.SetPlugins(true)
	msg, stages := spawnPlugins(t, api, dc, map[string]any{"daemon_id": "d1", "repo": "my-app", "kind": "agent", "runtime": "daemon"})
	if len(msg.Plugins) != 1 || msg.Plugins[0].Plugin != "superpowers" {
		t.Fatalf("plugins = %v", msg.Plugins)
	}
	for _, s := range stages {
		if s.ID == protocol.StagePlugins && !strings.Contains(s.Detail, "1 skipped") {
			t.Fatalf("stage detail = %q", s.Detail)
		}
	}
}

func TestDaemonConnRecordsThePluginsCapability(t *testing.T) {
	dc := &DaemonConn{ID: "d"}
	if dc.CanPlugins() {
		t.Error("a daemon that has not said so must not be assumed to load plugins")
	}
	dc.SetPlugins(true)
	if !dc.CanPlugins() {
		t.Error("capability not recorded")
	}
}

// A board-started (v1) session on a daemon carries the list too, and a cron's does not.
func TestRunnerStartOnDaemonCarriesPlugins(t *testing.T) {
	ctx := context.Background()
	fx := newMCPFx(t) // skips without TEST_DATABASE_URL
	fx.dc.SetPlugins(true)
	fx.dc.SetCheckedOutRepos([]string{"app"})
	fx.api.SetCoreCredentials("http://core.test", "internal-key")
	fx.api.fetchPlugins = func(context.Context, string, coreProof) ([]pluginspec.Entry, error) {
		return []pluginspec.Entry{pe("superpowers")}, nil
	}
	if _, apiErr := fx.api.StartSession(ctx, fx.owner(), RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker"}, ""); apiErr != nil {
		t.Fatalf("start: %+v", apiErr)
	}
	msg := recvSpawn(t, fx.dc)
	if len(msg.Plugins) != 1 || msg.Plugins[0].Plugin != "superpowers" {
		t.Fatalf("v1 daemon spawn plugins = %v", msg.Plugins)
	}
	if att := fx.api.hub.starts.get(msg.SessionID); att == nil || !strings.Contains(planIDs(att.stages), "plugins") {
		t.Fatal("v1 daemon start plan lacks the plugins stage")
	}
	// A cron start is restricted: no plugins, whatever the account's list.
	if _, apiErr := fx.api.StartSession(ctx, fx.owner(),
		RunnerStartRequest{Prompt: "p", Runtime: "docker", Engine: "claude", NoRepo: true, AutoStop: true, CronID: newUUID(), NoOperatorFallback: true}, ""); apiErr != nil {
		t.Fatalf("cron start: %+v", apiErr)
	}
	if msg := recvSpawn(t, fx.dc); len(msg.Plugins) != 0 || !msg.RestrictTools {
		t.Fatalf("cron spawn = plugins %v restrict %v", msg.Plugins, msg.RestrictTools)
	}
}
