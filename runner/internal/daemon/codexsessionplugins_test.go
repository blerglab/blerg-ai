package daemon

import (
	"context"
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestCodexSessionPluginsRestartBetweenTurnsAndResume(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_HOLD_FIRST", "1")
	log := filepath.Join(bin, "calls.log")
	t.Setenv("FAKE_CODEX_LOG", log)
	allow, _ := pluginspec.ParseAllowlist("example-org/plugins")
	w := newPluginWorkshop(filepath.Join(t.TempDir(), "plugins", "claude"), t.TempDir(), allow)
	source := filepath.Join(w.home, ".codex")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", source)
	w.run = func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 2 && args[1] == "marketplace" && args[2] == "list" {
			return `{"marketplaces":[{"name":"market","marketplaceSource":{"sourceType":"git","source":"example-org/plugins"}}]}`, nil
		}
		if len(args) > 1 && args[1] == "add" {
			path := filepath.Join(filepath.Dir(w.dir), "codex/workshop/plugins/cache/market/selected/1.0.0")
			if err := os.MkdirAll(path, 0700); err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(path, "plugin.json"), []byte(`{"name":"selected"}`), 0600); err != nil {
				return "", err
			}
			return `{"pluginId":"selected@market","name":"selected","marketplaceName":"market","installedPath":"` + path + `"}`, nil
		}
		return "", nil
	}
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	sender := &agentTestSender{}
	h := NewAgentHost(sender, AgentHostConfig{Plugins: w, RecordsDir: t.TempDir()})
	h.sessions["session"] = &agentSession{engine: "codex", loop: d, record: newRecordKeeper(h.cfg.RecordsDir, nil, AgentRecord{SessionID: "session", Repo: "repo", WorkDir: d.workDir})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startTestCodexDriver(ctx, t, d)
	defer stop()
	d.Enqueue("first", "chat")
	waitUntil(t, "first accepted", func() bool { return countKind(em, "user_message") == 1 })
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "add", SessionID: "session", Plugins: []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "selected"}}})
	pluginResultFor(t, sender, "add", "queued")
	raw, _ := os.ReadFile(log)
	if strings.Count(string(raw), "codex app-server") != 1 {
		t.Fatal("restarted during active turn")
	}
	d.Enqueue("finish first", "chat")
	waitUntil(t, "plugin restart", func() bool { raw, _ := os.ReadFile(log); return strings.Contains(string(raw), "thread/resume") })
	waitCodexPluginResult(t, sender, "add", "applied")
	d.Enqueue("second", "chat")
	waitUntil(t, "second accepted", func() bool { return countKind(em, "user_message") == 3 })
	raw, _ = os.ReadFile(log)
	if !strings.Contains(string(raw), `"threadId": "cx-thread-1"`) || !strings.Contains(string(raw), `selected@market`) {
		t.Fatal("restart lost conversation or plugins")
	}
	if strings.Count(string(raw), "thread/start ") != 1 {
		t.Fatal("started a new conversation")
	}
	// Revocation must still let the owner remove the installed plugin.
	if err := w.policy.Apply(pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{}), time.Second); err != nil {
		t.Fatal(err)
	}
	// Removal also waits through the active replacement turn, and must not count
	// planned restarts toward the native crash retry limit.
	for i := 0; i < 4; i++ {
		request := []string{"remove-a", "remove-b", "remove-c", "remove-d"}[i]
		h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: request, SessionID: "session", Remove: []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "selected"}}})
		d.Enqueue("finish current turn", "chat")
		waitUntil(t, "removal restart", func() bool { raw, _ := os.ReadFile(log); return strings.Count(string(raw), "thread/resume ") >= i+2 })
		waitCodexPluginResult(t, sender, request, "applied")
		d.Enqueue("next turn", "chat")
		want := 5 + 2*i
		waitUntil(t, "next accepted after removal", func() bool { return countKind(em, "user_message") == want })
	}
	records := listAgentRecords(h.cfg.RecordsDir)
	if len(records) != 1 || len(records[0].Plugins) != 0 || records[0].PluginRequests["remove-d"].State != "applied" {
		t.Fatal("removal was not persisted")
	}
	d.mu.Lock()
	remaining, _ := d.RuntimeConfig["plugins"].(map[string]any)
	home := d.CodexHome
	d.mu.Unlock()
	if len(remaining) != 0 {
		t.Fatal("removal retained plugin config")
	}
	if _, err := os.Stat(filepath.Join(home, "plugins/cache/market/selected")); !os.IsNotExist(err) {
		t.Fatal("removed plugin still present")
	}

}

type pluginInventoryWriter struct {
	app   *codexApp
	reply codexRPC
}

func (w *pluginInventoryWriter) Close() error { return nil }
func (w *pluginInventoryWriter) Write(raw []byte) (int, error) {
	var req struct{ ID int }
	if err := json.Unmarshal(raw, &req); err != nil {
		return 0, err
	}
	w.app.mu.Lock()
	ch := w.app.pending[req.ID]
	w.app.mu.Unlock()
	ch <- w.reply
	return len(raw), nil
}
func TestCodexSessionPluginsIdleGateKeepsOwnedWork(t *testing.T) {
	for _, mode := range []string{"active", "recovery", "background", "child", "question", "dynamic", "backlog", "pending-child", "native-terminal", "unknown-terminals", "notice", "idle"} {
		t.Run(mode, func(t *testing.T) {
			d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
			d.threadID = "fixture-thread"
			a := &codexApp{d: d, ctx: context.Background(), pending: map[int]chan codexRPC{}, done: make(chan struct{})}
			w := &pluginInventoryWriter{app: a, reply: codexRPC{Result: json.RawMessage(`{"thread":{"id":"fixture-thread","status":{"type":"idle"},"turns":[]}}`)}}
			a.in = w
			applied := false
			d.queuePluginUpdate(func(context.Context) { applied = true })
			switch mode {
			case "recovery":
				d.recoveryInputReady = func() bool { return false }
			case "active":
				d.activeTurn = true
			case "background":
				d.bgTasks = map[string]agent.BackgroundTaskStartedPayload{"b": {TaskID: "b"}}
			case "child":
				d.children = map[string]*codexChild{"child": {}}
			case "question":
				a.pendingQuestions = map[string]bool{"q": true}
			case "dynamic":
				a.dynamicCalls = map[string]codexDynamicCall{"d": {}}
			case "notice":
				a.backlog = []codexRPC{{Method: "deprecationNotice"}}
			case "backlog":
				a.backlog = []codexRPC{{Method: "turn/started"}}
			case "pending-child":
				a.pendingChildren = map[string]*codexPendingChild{"c": {}}
			case "native-terminal":
				w.reply.Result = json.RawMessage(`{"thread":{"id":"fixture-thread","status":{"type":"idle"},"turns":[{"status":"completed","items":[{"type":"commandExecution","status":"completed","processId":"b","exitCode":null}]}]}}`)
			case "unknown-terminals":
				w.reply.Result = json.RawMessage(`{}`)
			}
			if got := a.restartForPlugins(); got != (mode == "idle" || mode == "notice") {
				t.Fatalf("restart=%v", got)
			}
			if applied {
				t.Fatal("applied before old process stopped")
			}
			if (mode == "idle" || mode == "notice") && (!d.applySessionPlugins(context.Background()) || !applied) {
				t.Fatal("idle update lost")
			}
		})
	}
}

func TestCodexSessionPluginsWakeAnIdleDriver(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startTestCodexDriver(ctx, t, d)
	defer stop()
	d.Enqueue("first", "chat")
	waitUntil(t, "first idle", func() bool { return countKind(em, "turn_done") == 1 })
	applied := make(chan struct{})
	if !d.queuePluginUpdate(func(context.Context) { close(applied) }) {
		t.Fatal("idle plugin update refused")
	}
	select {
	case <-applied:
	case <-time.After(10 * time.Second):
		t.Fatal("idle driver did not wake for plugin update")
	}
}

func waitCodexPluginResult(t *testing.T, sender *agentTestSender, request, state string) {
	t.Helper()
	waitUntil(t, "plugin result "+request+" "+state, func() bool {
		sender.mu.Lock()
		defer sender.mu.Unlock()
		for _, msg := range sender.msgs {
			if result, ok := msg.(protocol.SessionPluginsResult); ok && result.RequestID == request && result.State == state {
				return true
			}
		}
		return false
	})
}

func codexPluginTestHost(t *testing.T) (*AgentHost, *codexDriver, *agentTestSender, *collectEmitter) {
	t.Helper()
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	allow, _ := pluginspec.ParseAllowlist("example-org/plugins")
	w := newPluginWorkshop(filepath.Join(t.TempDir(), "plugins", "claude"), t.TempDir(), allow)
	t.Setenv("CODEX_HOME", t.TempDir())
	w.run = func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 2 && args[1] == "marketplace" && args[2] == "list" {
			return `{"marketplaces":[{"name":"market","marketplaceSource":{"sourceType":"git","source":"example-org/plugins"}}]}`, nil
		}
		if len(args) > 1 && args[1] == "add" {
			name := strings.Split(args[2], "@")[0]
			path := filepath.Join(filepath.Dir(w.dir), "codex/workshop/plugins/cache/market", name, "1.0.0")
			if err := os.MkdirAll(path, 0700); err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(path, "plugin.json"), []byte(`{"name":"`+name+`"}`), 0600); err != nil {
				return "", err
			}
			return `{"pluginId":"` + name + `@market","name":"` + name + `","marketplaceName":"market","installedPath":"` + path + `"}`, nil
		}
		return "", nil
	}
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	sender := &agentTestSender{}
	h := NewAgentHost(sender, AgentHostConfig{Plugins: w, RecordsDir: t.TempDir()})
	h.sessions["session"] = &agentSession{engine: "codex", loop: d, record: newRecordKeeper(h.cfg.RecordsDir, nil, AgentRecord{SessionID: "session", Repo: "repo", WorkDir: d.workDir})}
	return h, d, sender, em
}

func TestCodexSessionPluginsRollBackReplacementLoadFailure(t *testing.T) {
	for _, boundary := range []string{"initialize", "thread/resume"} {
		t.Run(boundary, func(t *testing.T) {
			t.Setenv("FAKE_CODEX_REJECT_PLUGIN", boundary)
			gate := filepath.Join(t.TempDir(), "release")
			t.Setenv("FAKE_CODEX_PLUGIN_GATE", gate)
			log := filepath.Join(t.TempDir(), "calls")
			t.Setenv("FAKE_CODEX_LOG", log)
			h, d, sender, em := codexPluginTestHost(t)
			oldHome := t.TempDir()
			d.CodexHome = oldHome
			d.RuntimeConfig = map[string]any{"fixture_setting": "preserved", "plugins": map[string]any{"previous@market": map[string]any{"enabled": true}}}
			previous := []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "previous"}}
			h.sessions["session"].pluginSpecs = previous
			h.sessions["session"].record.rec.Plugins = previous
			stop := startTestCodexDriver(context.Background(), t, d)
			defer stop()
			d.Enqueue("before", "chat")
			waitUntil(t, "first turn", func() bool { return countKind(em, "turn_done") == 1 })
			h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "bad", SessionID: "session", Plugins: []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "selected"}}})
			waitUntil(t, "replacement waiting", func() bool { _, err := os.Stat(gate + ".waiting"); return err == nil })
			records := listAgentRecords(h.cfg.RecordsDir)
			if len(records) != 1 || (len(records[0].Plugins) != 1 || records[0].Plugins[0].Plugin != "previous") || records[0].PluginRequests["bad"].State == "applied" {
				t.Error("selection reported applied before replacement loaded")
			}
			d.Enqueue("after", "chat")
			if err := os.WriteFile(gate, nil, 0600); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, "rollback or stopped runtime", func() bool {
				sender.mu.Lock()
				failed := false
				for _, msg := range sender.msgs {
					if r, ok := msg.(protocol.SessionPluginsResult); ok && r.RequestID == "bad" && r.State == "failed" {
						failed = true
					}
				}
				sender.mu.Unlock()
				d.mu.Lock()
				stopped := d.stopped
				d.mu.Unlock()
				return failed || stopped
			})
			result := pluginResultFor(t, sender, "bad", "failed")
			if !strings.Contains(result.Error, "previous plugins") {
				t.Fatalf("unclear owner message: %q", result.Error)
			}
			waitUntil(t, "queued turn after rollback", func() bool { return countKind(em, "turn_done") == 2 })
			d.mu.Lock()
			if d.stopped || d.CodexHome != oldHome || d.RuntimeConfig["fixture_setting"] != "preserved" || len(d.RuntimeConfig["plugins"].(map[string]any)) != 1 || d.RuntimeConfig["plugins"].(map[string]any)["previous@market"] == nil || d.threadID != "cx-thread-1" {
				t.Error("previous engine configuration was not restored")
			}
			d.mu.Unlock()
			records = listAgentRecords(h.cfg.RecordsDir)
			if len(records) != 1 || (len(records[0].Plugins) != 1 || records[0].Plugins[0].Plugin != "previous") || records[0].PluginRequests["bad"].State != "failed" {
				t.Fatal("rollback was not durable")
			}
			h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "bad", SessionID: "session"})
			pluginResultFor(t, sender, "bad", "failed")
			raw, _ := os.ReadFile(log)
			if strings.Count(string(raw), "thread/start ") != 1 || !strings.Contains(string(raw), `"threadId": "cx-thread-1"`) {
				t.Fatal("rollback lost the conversation")
			}
		})
	}
}

func TestCodexSessionPluginsQueuedChangesConfirmIndividually(t *testing.T) {
	t.Setenv("FAKE_CODEX_REJECT_PLUGIN", "thread/resume")
	gate := filepath.Join(t.TempDir(), "release")
	t.Setenv("FAKE_CODEX_PLUGIN_GATE", gate)
	h, d, sender, em := codexPluginTestHost(t)
	stop := startTestCodexDriver(context.Background(), t, d)
	defer stop()
	d.Enqueue("before", "chat")
	waitUntil(t, "first turn", func() bool { return countKind(em, "turn_done") == 1 })
	entry := []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "selected"}}
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "bad", SessionID: "session", Plugins: entry})
	waitUntil(t, "replacement waiting", func() bool { _, err := os.Stat(gate + ".waiting"); return err == nil })
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "next", SessionID: "session", Remove: entry})
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "follow-up replacement", func() bool {
		sender.mu.Lock()
		defer sender.mu.Unlock()
		for _, msg := range sender.msgs {
			if r, ok := msg.(protocol.SessionPluginsResult); ok && r.RequestID == "next" && r.State == "applied" {
				return true
			}
		}
		return false
	})
	pluginResultFor(t, sender, "bad", "failed")
	d.Enqueue("after", "chat")
	waitUntil(t, "turn after follow-up", func() bool { return countKind(em, "turn_done") == 2 })
	records := listAgentRecords(h.cfg.RecordsDir)
	if len(records) != 1 || len(records[0].Plugins) != 0 || records[0].PluginRequests["bad"].State != "failed" || records[0].PluginRequests["next"].State != "applied" {
		t.Fatal("queued change inherited a failed selection")
	}
}

func TestCodexSessionPluginsRecoveryReplaysUnconfirmedRequest(t *testing.T) {
	h, _, sender, _ := codexPluginTestHost(t)
	root := t.TempDir()
	h.cfg.ReposRoot, h.cfg.HomeDir = root, t.TempDir()
	// Start a fresh host from the record left while a candidate was initializing.
	delete(h.sessions, "session")
	rec := AgentRecord{SessionID: "session", Engine: "codex", WorkDir: root, CodexThreadID: "cx-thread-1", PluginRequests: map[string]protocol.SessionPluginsResult{
		"unconfirmed": {RequestID: "unconfirmed", SessionID: "session", State: "queued"},
		"terminal":    {RequestID: "terminal", SessionID: "session", State: "failed"},
	}}
	if err := writeAgentRecord(h.cfg.RecordsDir, rec); err != nil {
		t.Fatal(err)
	}
	original := codexTranscriptExists
	codexTranscriptExists = func(string) bool { return true }
	t.Cleanup(func() { codexTranscriptExists = original })
	defer func() { h.Freeze(); h.Kill("session") }()
	if got := h.AdoptRecorded(); got != 1 {
		t.Fatalf("adopted %d", got)
	}
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "unconfirmed", SessionID: "session", Plugins: []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "selected"}}})
	waitCodexPluginResult(t, sender, "unconfirmed", "applied")
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "terminal", SessionID: "session", Plugins: []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "unreviewed"}}})
	pluginResultFor(t, sender, "terminal", "failed")
	records := listAgentRecords(h.cfg.RecordsDir)
	if len(records) != 1 || len(records[0].Plugins) != 1 || records[0].Plugins[0].Plugin != "selected" {
		t.Fatal("recovered unconfirmed change did not apply")
	}
}

func TestCodexRemovalRetainsOtherRevokedPluginsWithoutInstall(t *testing.T) {
	w, _ := newTestWorkshop(t)
	source, cache := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", source)
	entries := []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "first"}, {Marketplace: "example-org/plugins", Plugin: "second"}}
	paths := map[string]string{}
	for _, entry := range entries {
		path := filepath.Join(cache, "plugins/cache/market", entry.Plugin, "1.0.0")
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "plugin.json"), []byte(`{"name":"`+entry.Plugin+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
		paths[entry.Plugin+"@market"] = path
	}
	oldHome := filepath.Join(filepath.Dir(w.dir), "codex/sessions/original")
	home, config, err := snapshotCodexPluginHome(oldHome, source, cache, entries, paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.policy.Apply(pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{}), time.Second); err != nil {
		t.Fatal(err)
	}
	w.run = func(context.Context, string, ...string) (string, error) {
		t.Error("removal contacted installer")
		return "", nil
	}
	d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	d.CodexHome, d.RuntimeConfig = home, config
	sender := &agentTestSender{}
	h := NewAgentHost(sender, AgentHostConfig{Plugins: w})
	sess := &agentSession{engine: "codex", loop: d, pluginSpecs: entries}
	h.sessions["session"] = sess
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "remove", SessionID: "session", Remove: entries[:1]})
	pluginResultFor(t, sender, "remove", "queued")
	// The native loop has reaped its old process before applying the selection.
	d.restartPlugins, d.pendingPlugins = d.pendingPlugins, nil
	if !d.applySessionPlugins(t.Context()) || d.pluginAttempt == nil {
		t.Fatal("removal failed while another revoked plugin remains")
	}
	if !d.confirmSessionPlugins() {
		t.Fatal("replacement selection could not be confirmed")
	}
	result := pluginResultFor(t, sender, "remove", "applied")
	if len(result.Plugins) != 1 || result.Plugins[0] != entries[1] {
		t.Fatal("wrong retained selection", result)
	}
	if _, err := os.Stat(filepath.Join(d.CodexHome, "plugins/cache/market/first")); !os.IsNotExist(err) {
		t.Fatal("removed plugin survived", err)
	}
	if _, err := os.Stat(filepath.Join(d.CodexHome, "plugins/cache/market/second/1.0.0/plugin.json")); err != nil {
		t.Fatal("retained plugin lost", err)
	}
	if _, err := os.Stat(filepath.Join(oldHome, "plugins/cache/market/first/1.0.0/plugin.json")); err != nil {
		t.Fatal("old snapshot changed before confirmation", err)
	}
	_, _, res := w.prepareCodexRemoval(t.Context(), "not-retained", entries, d.CodexHome, source)
	if res.Installed != 0 || len(res.Failed) == 0 {
		t.Fatal("removal copied a plugin absent from the current snapshot", res)
	}

	// This path must never authorize a new installation from the revoked source.
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "add", SessionID: "session", Plugins: entries[:1]})
	pluginResultFor(t, sender, "add", "failed")
}
