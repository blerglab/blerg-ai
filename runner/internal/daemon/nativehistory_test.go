package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func TestNativeHistoryRetainsOnlyConversationIdentity(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := NativeHistoryConfig{Directory: dir, AccountID: "fixture-account", ClaimUID: "fixture-claim"}
	k, err := openNativeHistory(cfg, "fixture-session", "codex", work)
	if err != nil {
		t.Fatal(err)
	}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Emitter: &collectEmitter{}, Env: []string{"FIXTURE_AUTH=private-value"}})
	d.RuntimeConfig = map[string]any{"fixture_credential": "private-value"}
	if err = k.bind(d, t.TempDir(), func() { t.Error("unexpected persistence fault") }); err != nil {
		t.Fatal(err)
	}
	d.onThreadID("native-thread")
	d.onKnownChildren([]OpenSubagent{{AgentID: "native-child", ChildID: "canonical-child", CumulativeUsage: &agent.Usage{InputTokens: 12}}})
	if err = k.observe(agent.Event{Kind: "user_message", Payload: agent.UserMessagePayload{TurnID: "native-turn"}}); err != nil {
		t.Fatal(err)
	}
	_ = k.root.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "conversation.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-value", "FIXTURE_AUTH", "fixture_credential", "session_token", "mcp_gateway"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("nonconversation state persisted: %s", secret)
		}
	}
	cfg.Resume = true
	k, err = openNativeHistory(cfg, "fixture-session", "codex", work)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.root.Close() }()
	resumed := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Emitter: &collectEmitter{}})
	if err = k.bind(resumed, t.TempDir(), func() { t.Error("unexpected persistence fault") }); err != nil {
		t.Fatal(err)
	}
	if resumed.threadID != "native-thread" || resumed.usageTurnID != "native-turn" || !resumed.childKnown("native-child") || resumed.children["native-child"].lastTotal.InputTokens != 12 {
		t.Fatal("native identity or child baseline was not restored")
	}
	if !strings.Contains(strings.Join(resumed.launchArgs(), " "), "sqlite_home=") {
		t.Fatal("native SQLite path was not fixed at process startup")
	}
}

func TestNativeHistoryRejectsMissingForeignAndSymlinkState(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := NativeHistoryConfig{Directory: dir, AccountID: "fixture-owner", ClaimUID: "fixture-uid", Resume: true}
	if k, err := openNativeHistory(cfg, "fixture-session", "codex", work); err == nil {
		_ = k.root.Close()
		t.Fatal("missing expected identity silently became fresh")
	}
	cfg.Resume = false
	k, err := openNativeHistory(cfg, "fixture-session", "codex", work)
	if err != nil {
		t.Fatal(err)
	}
	if err = k.setNativeID("native-root"); err != nil {
		t.Fatal(err)
	}
	_ = k.root.Close()
	cfg.Resume = true
	for _, mutate := range []func(*NativeHistoryConfig){func(c *NativeHistoryConfig) { c.AccountID = "other-owner" }, func(c *NativeHistoryConfig) { c.ClaimUID = "replaced-uid" }} {
		bad := cfg
		mutate(&bad)
		if k, err := openNativeHistory(bad, "fixture-session", "codex", work); err == nil {
			_ = k.root.Close()
			t.Fatal("foreign retained history accepted")
		}
	}
	if k, err := openNativeHistory(cfg, "another-session", "codex", work); err == nil {
		_ = k.root.Close()
		t.Fatal("another session's identity accepted")
	}
	if k, err := openNativeHistory(cfg, "fixture-session", "claude", work); err == nil {
		_ = k.root.Close()
		t.Fatal("another engine's identity accepted")
	}
	if k, err := openNativeHistory(cfg, "fixture-session", "codex", t.TempDir()); err == nil {
		_ = k.root.Close()
		t.Fatal("different working directory accepted")
	}
	if err = os.Remove(filepath.Join(dir, "conversation.json")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(dir, "conversation.json")); err != nil {
		t.Fatal(err)
	}
	if k, err := openNativeHistory(cfg, "fixture-session", "codex", work); err == nil {
		_ = k.root.Close()
		t.Fatal("symlink identity accepted")
	}
}

func TestNativeClaudeResumeDoesNotFallBackToNewConversation(t *testing.T) {
	env, logPath := installFakeClaude(t)
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, env)
	d.steer.legacy = false
	d.ccSessionID = "bad-native-session"
	d.requireNativeResume = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	d.Enqueue("current request", "chat")
	waitUntil(t, "native resume refusal", func() bool { return em.count("error:") > 0 })
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("driver did not stop")
	}
	started := starts(logLines(t, logPath))
	if len(started) != 1 || !strings.Contains(started[0], "--resume bad-native-session") {
		t.Fatalf("native failure started a new conversation: %v", started)
	}
}

func TestNativeToolSchemaDigestIsCanonicalAndContainsOnlyDefinitions(t *testing.T) {
	left := []map[string]any{{"name": "fixture", "inputSchema": json.RawMessage(`{"properties":{"value":{"type":"string"}},"type":"object"}`)}}
	right := []map[string]any{{"inputSchema": json.RawMessage(`{ "type": "object", "properties": { "value": {"type": "string"} } }`), "name": "fixture"}}
	first, err := nativeToolSchemaHash(left)
	if err != nil {
		t.Fatal(err)
	}
	same, err := nativeToolSchemaHash(right)
	if err != nil {
		t.Fatal(err)
	}
	if first != same || len(first) != 64 {
		t.Fatal("equivalent definitions produced different digest")
	}
	right[0]["name"] = "new-fixture"
	changed, err := nativeToolSchemaHash(right)
	if err != nil || first == changed {
		t.Fatal("changed schema was not identified")
	}
	for _, marker := range []string{"inputSchema", "properties", "fixture"} {
		if strings.Contains(first, marker) {
			t.Fatal("digest retained tool definition content")
		}
	}
}

func TestNativeSchemaNoticeOnlyWhenDefinitionsChanged(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := NativeHistoryConfig{Directory: dir, AccountID: "fixture-owner", ClaimUID: "fixture-uid"}
	specs := []map[string]any{{"name": "fixture-tool", "inputSchema": map[string]any{"type": "object"}}}
	makeDriver := func(definitions []map[string]any) *codexDriver {
		t.Helper()
		rHome, err := os.MkdirTemp("", "native-fixture-home-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(rHome) })
		d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Emitter: &collectEmitter{}})
		d.RestrictedRuntime = &codexRestrictedRuntime{Home: rHome, specs: definitions}
		return d
	}
	k, err := openNativeHistory(cfg, "fixture-session", "codex", work)
	if err != nil {
		t.Fatal(err)
	}
	first := makeDriver(specs)
	if err = k.bind(first, t.TempDir(), func() { t.Error("persistence fault") }); err != nil {
		t.Fatal(err)
	}
	defer first.RestrictedRuntime.Close()
	if err = k.setNativeID("native-thread"); err != nil {
		t.Fatal(err)
	}
	_ = k.root.Close()
	cfg.Resume = true
	for _, changed := range []bool{false, true} {
		k, err = openNativeHistory(cfg, "fixture-session", "codex", work)
		if err != nil {
			t.Fatal(err)
		}
		definitions := specs
		if changed {
			definitions = []map[string]any{{"name": "different-tool"}}
		}
		d := makeDriver(definitions)
		if err = k.bind(d, t.TempDir(), func() { t.Error("persistence fault") }); err != nil {
			t.Fatal(err)
		}
		note := d.RestrictedRuntime.capabilities("fixture-model").Note
		if strings.Contains(note, "retains earlier tool definitions") != changed {
			t.Fatal("schema-change notice is inaccurate")
		}
		d.RestrictedRuntime.Close()
		_ = k.root.Close()
	}
}
