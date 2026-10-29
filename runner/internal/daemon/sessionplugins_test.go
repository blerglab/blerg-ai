package daemon

import (
	"context"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionPluginsWaitForTurnAndKeepResume(t *testing.T) {
	d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	d.ccSessionID = "original-conversation"
	d.steer.running = true
	called := false
	if !d.queuePluginUpdate(func(context.Context) { called = true; d.pluginDirs = []string{"verified-snapshot"} }) {
		t.Fatal("update refused")
	}
	if d.applyPendingPlugins(context.Background()) || called {
		t.Fatal("applied during turn")
	}
	d.steer.running = false
	if !d.applyPendingPlugins(context.Background()) || !called {
		t.Fatal("not applied between turns")
	}
	if d.ccSessionID != "original-conversation" {
		t.Fatal("conversation changed")
	}
	if d.applyPendingPlugins(context.Background()) {
		t.Fatal("applied twice")
	}
}

func TestSessionPluginsNewSnapshotPreservesOldOnFailure(t *testing.T) {
	w, f := newTestWorkshop(t)
	old, res := w.prepare(context.Background(), "session", []pluginspec.Entry{wsEntry("existing")})
	if res.Installed != 1 {
		t.Fatal("fixture install")
	}
	f.failInstall = map[string]bool{"new": true}
	h := NewAgentHost(&agentTestSender{}, AgentHostConfig{Plugins: w})
	d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	d.pluginDirs = old
	sess := &agentSession{loop: d, workspaceCtx: context.Background(), pluginSpecs: []pluginspec.Entry{wsEntry("existing")}}
	h.sessions["session"] = sess
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "request", SessionID: "session", Plugins: []pluginspec.Entry{wsEntry("existing"), wsEntry("new")}})
	if !d.applyPendingPlugins(context.Background()) {
		t.Fatal("update missing")
	}
	if len(d.pluginDirs) != 1 || d.pluginDirs[0] != old[0] {
		t.Fatal("failed update changed live plugins")
	}
	if _, err := os.Stat(filepath.Join(old[0], ".claude-plugin", "plugin.json")); err != nil {
		t.Fatal("old snapshot removed")
	}
}

func pluginResultFor(t *testing.T, s *agentTestSender, id, state string) protocol.SessionPluginsResult {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.msgs) - 1; i >= 0; i-- {
		if v, ok := s.msgs[i].(protocol.SessionPluginsResult); ok && v.RequestID == id && v.State == state {
			return v
		}
	}
	t.Fatalf("missing plugin result %s %s", id, state)
	return protocol.SessionPluginsResult{}
}

func TestSessionPluginsBackgroundWorkBlocksInstall(t *testing.T) {
	for _, mode := range []string{"background", "child", "starting-background", "awaiting"} {
		t.Run(mode, func(t *testing.T) {
			d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
			called := false
			d.queuePluginUpdate(func(context.Context) { called = true })
			switch mode {
			case "background":
				d.bgTasks = map[string]*ccBgTask{"b": {}}
			case "child":
				d.subagents = map[string]*ccSubagent{"c": {}}
			case "starting-background":
				d.bgCalls = map[string]ccBgCall{"b": {}}
			case "awaiting":
				d.steer.awaiting = []awaitMsg{{}}
			}
			if d.applyPendingPlugins(context.Background()) || called {
				t.Fatal("owned work interrupted")
			}
			d.bgTasks = nil
			d.subagents = nil
			d.bgCalls = nil
			d.steer.awaiting = nil
			if !d.applyPendingPlugins(context.Background()) || !called {
				t.Fatal("not applied after work settled")
			}
		})
	}
}

func TestSessionPluginsAdditiveConcurrentAndDurableReplay(t *testing.T) {
	w, _ := newTestWorkshop(t)
	sender := &agentTestSender{}
	h := NewAgentHost(sender, AgentHostConfig{Plugins: w, RecordsDir: t.TempDir()})
	d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	dirs, res := w.prepare(context.Background(), "session", []pluginspec.Entry{wsEntry("existing")})
	if res.Installed != 1 {
		t.Fatal("fixture")
	}
	d.pluginDirs = dirs
	sess := &agentSession{loop: d, pluginSpecs: []pluginspec.Entry{wsEntry("existing")}, record: newRecordKeeper(h.cfg.RecordsDir, nil, AgentRecord{SessionID: "session", Repo: "repo", WorkDir: d.workDir, Plugins: []pluginspec.Entry{wsEntry("existing")}})}
	h.sessions["session"] = sess
	for _, name := range []string{"first", "second"} {
		h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: name, SessionID: "session", Plugins: []pluginspec.Entry{wsEntry(name)}})
	}
	for range 2 {
		if !d.applyPendingPlugins(context.Background()) {
			t.Fatal("queued addition missing")
		}
	}
	got := pluginResultFor(t, sender, "second", "applied")
	if len(got.Plugins) != 3 || len(d.pluginDirs) != 3 {
		t.Fatal("an approved addition dropped another plugin")
	}
	records := listAgentRecords(h.cfg.RecordsDir)
	if len(records) != 1 || len(records[0].Plugins) != 3 || records[0].PluginRequests["second"].State != "applied" {
		t.Fatal("applied state not durable")
	}
	recovered := h.keepRecord(protocol.SpawnSession{SessionID: "session", Repo: "repo", Plugins: records[0].Plugins}, d.workDir, "", &adoptRequest{rec: records[0]})
	if recovered.rec.PluginRequests["second"].State != "applied" {
		t.Fatal("recovery dropped request replay state")
	}
	// Rehosted driver replays a result without installation, restart or dir mutation.
	d2 := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	h.sessions["session"] = &agentSession{loop: d2, pluginRequests: records[0].PluginRequests, pluginSpecs: records[0].Plugins}
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "second", SessionID: "session", Plugins: []pluginspec.Entry{wsEntry("malicious-replay")}})
	if d2.applyPendingPlugins(context.Background()) {
		t.Fatal("replayed request reinstalled code")
	}
	pluginResultFor(t, sender, "second", "applied")
}

func TestSessionPluginsFailsClosedBeforeInstalling(t *testing.T) {
	for _, mode := range []string{"restricted", "docker", "cluster", "mismatched-driver", "unlisted", "invalid", "duplicate", "too-many", "no-workshop", "missing"} {
		t.Run(mode, func(t *testing.T) {
			w, f := newTestWorkshop(t)
			sender := &agentTestSender{}
			h := NewAgentHost(sender, AgentHostConfig{Plugins: w})
			d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
			sess := &agentSession{loop: d}
			h.sessions["session"] = sess
			entries := []pluginspec.Entry{wsEntry("safe")}
			switch mode {
			case "restricted":
				d.restrict = true
			case "docker":
				sess.sandboxed = true
			case "cluster":
				h.cfg.Plugins = nil
			case "mismatched-driver":
				sess.loop = newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
				sess.loop.(*codexDriver).RestrictedRuntime = &codexRestrictedRuntime{Restricted: true}
			case "unlisted":
				entries[0].Marketplace = "untrusted/repo"
			case "invalid":
				entries[0].Plugin = "--config=evil"
			case "duplicate":
				entries = append(entries, entries[0])
			case "too-many":
				for range pluginspec.MaxEntries {
					entries = append(entries, wsEntry("safe"))
				}
			case "no-workshop":
				h.cfg.Plugins = nil
			case "missing":
				delete(h.sessions, "session")
			}
			h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "request", SessionID: "session", Plugins: entries})
			if d.applyPendingPlugins(context.Background()) {
				t.Fatal("unsafe request scheduled")
			}
			f.mu.Lock()
			calls := len(f.args)
			f.mu.Unlock()
			if calls != 0 {
				t.Fatal("unsafe request ran installer")
			}
			sender.mu.Lock()
			last := sender.msgs[len(sender.msgs)-1].(protocol.SessionPluginsResult)
			sender.mu.Unlock()
			if last.State != "failed" && last.State != "unsupported" {
				t.Fatal("unsafe request not refused")
			}
		})
	}
}

func TestSessionPluginsGrantedHostPreservesMCPPolicy(t *testing.T) {
	w, _ := newTestWorkshop(t)
	sender := &agentTestSender{}
	h := NewAgentHost(sender, AgentHostConfig{Plugins: w})
	cfg := &mcpConfigFile{}
	d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	d.mcpFile = cfg
	h.sessions["session"] = &agentSession{loop: d}
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "request", SessionID: "session", Plugins: []pluginspec.Entry{wsEntry("safe")}})
	if !d.applyPendingPlugins(context.Background()) {
		t.Fatal("granted unrestricted host rejected")
	}
	pluginResultFor(t, sender, "request", "applied")
	if d.mcpFile != cfg || d.restrict {
		t.Fatal("MCP policy changed")
	}
}

func TestSessionPluginsSteeringResumesWithNewSnapshot(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			env, logPath := installFakeClaude(t)
			em := &collectEmitter{}
			d := newClaudeCodeDriver(t.TempDir(), "", "", em, env)
			d.steer.legacy = legacy
			ctx, cancel := context.WithCancel(context.Background())
			doneRun := make(chan struct{})
			go func() { defer close(doneRun); d.Run(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-doneRun:
				case <-time.After(10 * time.Second):
					t.Error("driver did not stop")
				}
			})
			d.Enqueue("first", "chat")
			waitIdle(t, em, 1)
			d.mu.Lock()
			native := d.ccSessionID
			d.mu.Unlock()
			done := make(chan struct{})
			if !d.queuePluginUpdate(func(context.Context) { d.pluginDirs = []string{"/verified/session/plugin"}; close(done) }) {
				t.Fatal("queue refused")
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("update stuck")
			}
			d.Enqueue("second", "chat")
			waitIdle(t, em, 2)
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			starts := 0
			for _, line := range strings.Split(string(log), "\n") {
				if strings.HasPrefix(line, "start ") {
					starts++
					if starts == 2 && (!strings.Contains(line, "--resume "+native) || !strings.Contains(line, "--plugin-dir=/verified/session/plugin")) {
						t.Fatal("second process lacked original conversation or plugin")
					}
				}
			}
			if starts != 2 {
				t.Fatalf("process starts=%d want2", starts)
			}
		})
	}
}

func TestSessionPluginsPersistenceFailureLeavesLiveConfig(t *testing.T) {
	w, _ := newTestWorkshop(t)
	sender := &agentTestSender{}
	h := NewAgentHost(sender, AgentHostConfig{Plugins: w})
	d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	d.pluginDirs = []string{"previous-snapshot"}
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	sess := &agentSession{loop: d, record: &recordKeeper{dir: blocker, rec: AgentRecord{SessionID: "session"}}}
	h.sessions["session"] = sess
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "request", SessionID: "session", Plugins: []pluginspec.Entry{wsEntry("safe")}})
	if !d.applyPendingPlugins(context.Background()) {
		t.Fatal("request missing")
	}
	pluginResultFor(t, sender, "request", "failed")
	if len(d.pluginDirs) != 1 || d.pluginDirs[0] != "previous-snapshot" || len(sess.record.rec.Plugins) != 0 {
		t.Fatal("failed persistence changed selected plugins")
	}
}

func TestSessionPluginsCanceledIdleGateDoesNotApply(t *testing.T) {
	d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	called := false
	d.queuePluginUpdate(func(context.Context) { called = true })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d.applyPendingPlugins(ctx) || called {
		t.Fatal("canceled session installed code")
	}
}
