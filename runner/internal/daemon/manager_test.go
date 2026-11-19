package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// recordingSender captures all Send calls for inspection.
type recordingSender struct {
	mu   sync.Mutex
	msgs []any
	ch   chan any
}

func newRecordingSender(buf int) *recordingSender {
	return &recordingSender{ch: make(chan any, buf)}
}

func (r *recordingSender) Send(msg any) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
	select {
	case r.ch <- msg:
	default:
	}
	return nil
}

// messages returns every captured Send call, JSON-marshaled, for substring
// assertions in tests.
func (r *recordingSender) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.msgs))
	for _, m := range r.msgs {
		b, _ := json.Marshal(m)
		out = append(out, string(b))
	}
	return out
}

// slicesEqual reports whether two string slices have equal contents.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The daemon refuses a spawn whose model or effort is invalid, before any
// workspace work, rather than trusting the server's 422.
func TestSpawnModelProblem(t *testing.T) {
	ok := []protocol.SpawnSession{
		{},
		{Model: "sonnet", Effort: "max"},
		{Model: "claude-haiku-4-5-20251001"},
		{Engine: "hermes", Model: "openrouter/anthropic/claude-sonnet-4"},
		{Engine: "hermes", Model: "qwen3-30b", Effort: "minimal"},
		{Engine: "codex", Model: "gpt-5.6-terra", Effort: "ultra"},
	}
	for _, m := range ok {
		if p := spawnModelProblem(m); p != "" {
			t.Errorf("%+v refused: %s", m, p)
		}
	}
	bad := []protocol.SpawnSession{
		{Engine: "future-engine", Kind: "agent"},
		{Engine: "future-engine", Kind: "tmux"},
		{Model: "sonnet --x"},
		{Model: "-p"},
		{Effort: "ultra"},
		{Engine: "codex", Model: "gpt 5"},
		{Engine: "codex", Effort: "$(id)"},
		{Engine: "codex", Effort: "auto"},
		{Engine: "hermes", Effort: "auto"},
		// Engines that registered no effort levels take none.
		{Engine: "openclaw", Effort: "high"},
	}
	for _, m := range bad {
		if p := spawnModelProblem(m); p == "" {
			t.Errorf("%+v accepted", m)
		}
	}
}

// Codex/Hermes/OpenClaw's terminal-command flag mapping is now covered by
// engines_test.go's TestBuildTerminalCommand (one generic builder driven by
// EngineSpec data instead of a per-engine xCommand function).

// waitManagerLifecycle joins the asynchronous kill worker without blocking the
// WebSocket pump that its authority RPCs need.
func waitManagerLifecycle(t *testing.T, m *Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		active := m.lifecycleActive[id]
		m.mu.Unlock()
		if !active {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("kill worker did not join")
		}
		time.Sleep(time.Millisecond)
	}
}

// clearBlergRunnerVars unsets the given env keys for the duration of the test,
// restoring them (or leaving them unset) via t.Cleanup. This ensures
// "omitted when config empty" assertions hold regardless of host env.
func clearBlergRunnerVars(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		k := k
		if v, had := os.LookupEnv(k); had {
			t.Setenv(k, v) // registers cleanup; then immediately unset below
			os.Unsetenv(k)
		} else {
			t.Cleanup(func() { os.Unsetenv(k) })
		}
	}
}

// TestSessionEnv verifies that sessionEnv returns os.Environ plus the expected
// Blerg Runner vars, including the per-session BLERG_RUNNER_SESSION_ID and the optional
// BLERG_RUNNER_SERVER_HTTP (omitted when the config value is empty).
func TestSessionEnv(t *testing.T) {
	// Scrub host BLERG_RUNNER_* so assertions on omitted vars are hermetic.
	clearBlergRunnerVars(t,
		"BLERG_RUNNER_PREVIEW_URL", "BLERG_RUNNER_DAEMON_TOKEN", "BLERG_RUNNER_SERVER_HTTP",
		"BLERG_RUNNER_SESSION_ID", "BLERG_RUNNER_BOARD_ID", "BLERG_RUNNER_BOARD_TOKEN",
	)

	t.Run("all config vars set", func(t *testing.T) {
		sender := newRecordingSender(4)
		mgr := newManagerWithSender(sender, ManagerConfig{
			DaemonToken: "tok-abc",
			ServerHTTP:  "http://runner.example.test",
		})
		env := mgr.sessionEnv("sess-x", false, "", "", "", nil)

		// BLERG_RUNNER_DAEMON_TOKEN is deliberately NOT in this map: the master
		// token never reaches a session env (TestSessionEnvNeverCarriesDaemonToken).
		want := map[string]string{
			"BLERG_RUNNER_SESSION_ID":  "sess-x",
			"BLERG_RUNNER_SERVER_HTTP": "http://runner.example.test",
		}
		for k, v := range want {
			needle := k + "=" + v
			found := false
			for _, e := range env {
				if e == needle {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("sessionEnv missing %q; keys = %v", envKey(needle), envKeys(env))
			}
		}
	})

	// An older env file may still set BLERG_RUNNER_PREVIEW_URL for the daemon: it
	// is not passed on to a session.
	t.Run("inherited preview URL is not passed on", func(t *testing.T) {
		t.Setenv("BLERG_RUNNER_PREVIEW_URL", "http://runner.example.test/api/preview")
		mgr := newManagerWithSender(newRecordingSender(4), ManagerConfig{ServerHTTP: "http://runner.example.test"})
		for _, e := range mgr.sessionEnv("sess-x", false, "", "", "", nil) {
			if strings.HasPrefix(e, "BLERG_RUNNER_PREVIEW_URL=") {
				t.Errorf("sessionEnv carries %q", envKey(e))
			}
		}
	})

	t.Run("ServerHTTP empty is omitted", func(t *testing.T) {
		sender := newRecordingSender(4)
		mgr := newManagerWithSender(sender, ManagerConfig{
			DaemonToken: "tok-abc",
			ServerHTTP:  "",
		})
		env := mgr.sessionEnv("sess-y", false, "", "", "", nil)

		for _, e := range env {
			if strings.HasPrefix(e, "BLERG_RUNNER_SERVER_HTTP=") {
				t.Errorf("BLERG_RUNNER_SERVER_HTTP must be omitted when ServerHTTP is empty; found key %q", envKey(e))
			}
		}

		// SESSION_ID must still be present.
		found := false
		for _, e := range env {
			if e == "BLERG_RUNNER_SESSION_ID=sess-y" {
				found = true
				break
			}
		}
		if !found {
			t.Error("BLERG_RUNNER_SESSION_ID=sess-y missing from env")
		}
	})
}

// TestSessionEnvAssist verifies Assist-session env injection:
//   - board vars are present when assist=true
//   - BLERG_RUNNER_DAEMON_TOKEN is absent even when an ambient value is in os.Environ
//   - a non-assist env does NOT carry BLERG_RUNNER_DAEMON_TOKEN either (it
//     used to; the per-session token replaced it)
func TestSessionEnvAssist(t *testing.T) {
	// Simulate the daemon process having BLERG_RUNNER_DAEMON_TOKEN in its own env.
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "ambient-daemon-tok")

	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{
		DaemonToken: "config-daemon-tok",
		ServerHTTP:  "http://runner.example.test",
	})

	// --- Assist env ---
	assistEnv := mgr.sessionEnv("sess-assist", true, "board-123", "btok-xyz", "", nil)

	// Board vars must be present.
	for _, needle := range []string{"BLERG_RUNNER_BOARD_ID=board-123", "BLERG_RUNNER_BOARD_TOKEN=btok-xyz"} {
		found := false
		for _, e := range assistEnv {
			if e == needle {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("assist env missing %q; keys = %v", envKey(needle), envKeys(assistEnv))
		}
	}

	// BLERG_RUNNER_DAEMON_TOKEN must NOT appear at all (strip from os.Environ AND skip config append).
	for _, e := range assistEnv {
		if strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") {
			t.Errorf("assist env must not contain BLERG_RUNNER_DAEMON_TOKEN; found key %q", envKey(e))
		}
	}

	// SESSION_ID must still be injected.
	found := false
	for _, e := range assistEnv {
		if e == "BLERG_RUNNER_SESSION_ID=sess-assist" {
			found = true
			break
		}
	}
	if !found {
		t.Error("BLERG_RUNNER_SESSION_ID=sess-assist missing from assist env")
	}

	// --- Non-assist env must not carry the daemon token either ---
	normalEnv := mgr.sessionEnv("sess-normal", false, "", "", "", nil)
	for _, e := range normalEnv {
		if strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") {
			t.Errorf("non-assist env must not contain BLERG_RUNNER_DAEMON_TOKEN; found key %q", envKey(e))
		}
	}
}

// TestSessionEnvNeverCarriesDaemonToken is the desktop-safety C1 guarantee:
// whatever kind of session is spawned (assist or not, with or without extra
// env), the master daemon token — which is full runner admin — is never in
// the child's environment, neither from config nor inherited from the
// daemon's own os.Environ. The per-session token and caller-supplied extra
// env are what the session gets instead.
func TestSessionEnvNeverCarriesDaemonToken(t *testing.T) {
	clearBlergRunnerVars(t, "BLERG_RUNNER_SESSION_TOKEN")
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "ambient-daemon-tok")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-inherited")
	mgr := newManagerWithSender(newRecordingSender(2), ManagerConfig{DaemonToken: "config-daemon-tok", ServerHTTP: "http://srv"})
	for _, assist := range []bool{false, true} {
		env := mgr.sessionEnv("s1", assist, "", "", "sess-tok-123", map[string]string{"BLERG_BOARD_URL": "http://localhost:8082"})
		for _, e := range env {
			if strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") {
				t.Fatalf("assist=%v: master token leaked via %s", assist, envKey(e))
			}
		}
		if !hasEntry(env, "BLERG_RUNNER_SESSION_TOKEN=sess-tok-123") || !hasEntry(env, "BLERG_BOARD_URL=http://localhost:8082") {
			t.Fatalf("assist=%v: session token / extra env missing; keys: %v", assist, envKeys(env))
		}
		// The engine's own credential is inherited: the engine cannot run
		// without it (ruling R1).
		if !hasEntry(env, "ANTHROPIC_API_KEY=sk-ant-inherited") {
			t.Fatalf("assist=%v: engine credential not inherited; keys: %v", assist, envKeys(env))
		}
		if !hasEntry(env, "BLERG_RUNNER_SESSION_ID=s1") {
			t.Fatalf("assist=%v: session id missing; keys: %v", assist, envKeys(env))
		}
	}
	// No token minted (older recovery record, or DB-less server): no
	// BLERG_RUNNER_SESSION_TOKEN entry at all rather than an empty one.
	for _, e := range mgr.sessionEnv("s2", false, "", "", "", nil) {
		if strings.HasPrefix(e, "BLERG_RUNNER_SESSION_TOKEN=") {
			t.Fatalf("empty session token must not be injected: %s", envKey(e))
		}
	}

	// sanitizedEnviron is the base every session process (tmux, sandbox, and
	// every agent-kind driver) starts from.
	s := sanitizedEnviron("X=1")
	if !hasEntry(s, "X=1") {
		t.Fatalf("sanitizedEnviron dropped extra; keys: %v", envKeys(s))
	}
	if !hasEntry(s, "ANTHROPIC_API_KEY=sk-ant-inherited") {
		t.Fatalf("sanitizedEnviron dropped the engine credential; keys: %v", envKeys(s))
	}
	for _, e := range s {
		if strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") {
			t.Fatalf("sanitizedEnviron leaked the master token via %s", envKey(e))
		}
	}

	// extraEnvPairs is what the agent-kind host feeds sanitizedEnviron:
	// deterministic order, token first, no entry for an empty token.
	pairs := extraEnvPairs("tok", map[string]string{"B": "2", "A": "1"})
	if len(pairs) != 3 || pairs[0] != "BLERG_RUNNER_SESSION_TOKEN=tok" || pairs[1] != "A=1" || pairs[2] != "B=2" {
		t.Fatalf("extraEnvPairs keys = %v", envKeys(pairs))
	}
	if got := extraEnvPairs("", nil); len(got) != 0 {
		t.Fatalf("extraEnvPairs(\"\", nil) keys = %v, want empty", envKeys(got))
	}

	// ExtraEnv is caller-supplied: it can't smuggle the master token back in,
	// override the engine credential or PATH-style daemon-owned vars, or
	// inject malformed keys. Only the clean key survives.
	hostile := map[string]string{
		"BLERG_RUNNER_DAEMON_TOKEN":  "smuggled",
		"BLERG_RUNNER_SESSION_TOKEN": "forged",
		"BLERG_RUNNER_ANYTHING":      "daemon-owned",
		"ANTHROPIC_API_KEY":          "stolen",
		"CLAUDE_CODE_OAUTH_TOKEN":    "stolen",
		"OPENAI_API_KEY":             "stolen",
		"":                           "empty-key",
		"HAS=EQUALS":                 "x",
		"HAS\x00NUL":                 "x",
		"HAS\nNEWLINE":               "x",
		"BLERG_BOARD_URL":            "http://board",
	}
	got := extraEnvPairs("real-tok", hostile)
	if len(got) != 2 || got[0] != "BLERG_RUNNER_SESSION_TOKEN=real-tok" || got[1] != "BLERG_BOARD_URL=http://board" {
		t.Fatalf("extraEnvPairs(hostile) keys = %q", envKeys(got))
	}
	full := mgr.sessionEnv("s3", false, "", "", "real-tok", hostile)
	for _, e := range full {
		if strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") || e == "ANTHROPIC_API_KEY=stolen" || e == "BLERG_RUNNER_SESSION_TOKEN=forged" {
			t.Fatalf("hostile extra env reached the session via %s", envKey(e))
		}
	}
	if !hasEntry(full, "ANTHROPIC_API_KEY=sk-ant-inherited") {
		t.Fatalf("engine credential clobbered; keys: %v", envKeys(full))
	}
}

// Environment assertion diagnostics must never include values: a fixture may
// accidentally inherit a real credential when run inside an agent session.
func envKey(entry string) string {
	key, _, _ := strings.Cut(entry, "=")
	return key
}

func envKeys(env []string) []string {
	keys := make([]string, len(env))
	for i, entry := range env {
		keys[i] = envKey(entry)
	}
	return keys
}

func hasEntry(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// TestMergeSessionIDsThreeGroups verifies that mergeSessionIDs deduplicates
// across three overlapping slices.
func TestMergeSessionIDsThreeGroups(t *testing.T) {
	a := []string{"x", "y"}
	b := []string{"y", "z"}
	c := []string{"z", "w", "x"}

	got := mergeSessionIDs(a, b, c)
	sort.Strings(got)
	want := []string{"w", "x", "y", "z"}
	if !slicesEqual(got, want) {
		t.Errorf("mergeSessionIDs = %v, want %v", got, want)
	}
}

func TestSpawnRejectsRepoOutsideReposRoot(t *testing.T) {
	root := t.TempDir()
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root, Command: []string{"unused"}})
	for _, repo := range []string{"../..", "..", "x/../../y", "/tmp"} {
		raw, _ := json.Marshal(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-" + repo, Repo: repo})
		mgr.handleSpawnSession(raw)
	}
	msgs := sender.messages()
	if len(msgs) != 4 {
		t.Fatalf("want 4 error messages, got %d: %v", len(msgs), msgs)
	}
	for _, m := range msgs {
		if !strings.Contains(m, `"status":"error"`) || !strings.Contains(m, "invalid folder path") {
			t.Errorf("unexpected message %s", m)
		}
	}
}

func TestResolveProjectPath(t *testing.T) {
	cases := map[string]bool{"app": true, "app/../app": true, "../app": false, "..": false, "/abs": false, "a/b": true}
	for repo, ok := range cases {
		_, err := resolveProjectPath("/home/dev/repos", repo)
		if (err == nil) != ok {
			t.Errorf("resolveProjectPath(%q) err=%v, want ok=%v", repo, err, ok)
		}
	}
}

// TestResolveProjectPathSymlinkEscape verifies that a symlink inside
// reposRoot pointing outside it is rejected even though the Clean-based
// containment check on the un-resolved path passes.
func TestResolveProjectPathSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := resolveProjectPath(root, "escape"); err == nil {
		t.Error("resolveProjectPath(escape) should reject a symlink pointing outside reposRoot")
	}

	// A real subdirectory must still resolve.
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := resolveProjectPath(root, "real"); err != nil {
		t.Errorf("resolveProjectPath(real) should succeed, got %v", err)
	}

	// A non-existent repo (new_repo path) keeps the Clean-based check only.
	if _, err := resolveProjectPath(root, "new_repo"); err != nil {
		t.Errorf("resolveProjectPath(new_repo) should succeed for a not-yet-created repo, got %v", err)
	}
}

// TestSpawnReportsMissingEngine verifies that when the engine binary is not on
// the daemon service's PATH, the spawn fails fast with a named, actionable
// reason (trial blocker 2) instead of a bare tmux/docker failure — and nothing
// gets spawned.
func TestSpawnReportsMissingEngine(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing on PATH
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root})
	mgr.agents = NewAgentHost(sender, AgentHostConfig{ClaudeCode: true}) // no Command override: real engine resolution
	raw, _ := json.Marshal(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-noeng", Repo: "app"})
	mgr.handleSpawnSession(raw)
	msgs := sender.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], `"status":"error"`) || !strings.Contains(msgs[0], "claude not found on PATH") {
		t.Fatalf("want one error naming the missing binary, got %v", msgs)
	}
}

// TestSpawnReportsMissingSandboxImage verifies that a sandboxed spawn fails
// fast when the sandbox image isn't present, via the sandboxImagePresent seam.
func TestSpawnReportsMissingSandboxImage(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root})
	mgr.agents = NewAgentHost(sender, AgentHostConfig{ClaudeCode: true})
	mgr.sandboxImagePresent = func() bool { return false }
	raw, _ := json.Marshal(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-noimg", Repo: "app", Sandbox: true})
	mgr.handleSpawnSession(raw)
	msgs := sender.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "sandbox image "+sandboxImage()+" missing") {
		t.Fatalf("want one error naming the missing image, got %v", msgs)
	}
}
