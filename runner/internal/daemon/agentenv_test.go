package daemon

// Agent-kind sessions must start from the SAME environment every other spawn
// kind gets. They used to build their own (sanitizedEnviron + the token), so a
// board-driven session held a valid per-session messaging token with no
// BLERG_RUNNER_SESSION_ID and no BLERG_RUNNER_SERVER_HTTP to spend it with —
// `blerg-runner` printed "not running under Blerg Runner" and exited 0.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/server"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeEngineDumpingEnv writes a `codex` stub that records its own environment
// (the env the driver actually handed the subprocess) and optionally runs an
// extra command first, then emits a minimal valid codex event stream.
func fakeEngineDumpingEnv(t *testing.T, envFile, pre string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/bash\nenv > " + envFile + "\n" + pre + `
cat <<'EOF'
{"type":"thread.started","thread_id":"cx-1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"ok"}}
{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1}}
EOF
`
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readEnvDump(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("engine never ran (no env dump): %v", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

// An agent-kind session's subprocess env must carry the session identity, the
// server URL and the per-session token (so blerg-runner is usable), plus the
// board linkage — and must never carry the daemon master token.
func TestAgentSessionEnvCarriesSessionIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "master-token-must-not-leak")

	envFile := filepath.Join(t.TempDir(), "env.txt")
	binDir := fakeEngineDumpingEnv(t, envFile, "")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{
		ReposRoot: reposRoot, HomeDir: t.TempDir(), ServerHTTP: "http://127.0.0.1:9",
	})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-env", Repo: "proj", Kind: "agent",
		Engine: "codex", InitialPrompt: "hello", SessionToken: "session-token-abc",
		ExtraEnv: map[string]string{
			"BLERG_BOARD_BOARD": "board-1",
			"BLERG_BOARD_TOKEN": "board-token",
			"BLERG_BOARD_URL":   "http://board.local",
			// Task 3 guarantee: a caller may not smuggle daemon-owned vars in.
			"BLERG_RUNNER_DAEMON_TOKEN": "smuggled",
		},
	})
	sender.waitForKind(t, "turn_done")

	env := readEnvDump(t, envFile)
	for k, want := range map[string]string{
		"BLERG_RUNNER_SESSION_ID":    "s-env",
		"BLERG_RUNNER_SESSION_TOKEN": "session-token-abc",
		"BLERG_RUNNER_SERVER_HTTP":   "http://127.0.0.1:9",
		"BLERG_BOARD_BOARD":          "board-1",
		"BLERG_BOARD_TOKEN":          "board-token",
		"BLERG_BOARD_URL":            "http://board.local",
	} {
		if env[k] != want {
			t.Errorf("env[%s] = %q, want %q", k, env[k], want)
		}
	}
	if _, ok := env["BLERG_RUNNER_DAEMON_TOKEN"]; ok {
		t.Error("the daemon master token reached an agent-kind session")
	}
	if !strings.HasPrefix(env["PATH"], filepath.Join(home, ".local", "bin")+string(os.PathListSeparator)) {
		t.Errorf("PATH does not start with ~/.local/bin (blerg-runner would not resolve): %q", env["PATH"])
	}
}

// End to end: the env an agent-kind session actually gets must let the real
// `blerg-runner` CLI post a message for that session against the real
// messaging endpoint — the thing the per-session token exists for.
// Requires TEST_DATABASE_URL; skips otherwise.
func TestAgentSessionEnvLetsBlergRunnerPostAMessage(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration test")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	const schema = "test_blerg_runner_agentenv"
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	bootstrap, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	for _, q := range []string{
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema),
		fmt.Sprintf("CREATE SCHEMA %s", schema),
	} {
		if _, err := bootstrap.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	bootstrap.Close()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect (schema): %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if p, err := pgxpool.New(context.Background(), dsn); err == nil {
			p.Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
			p.Close()
		}
	})
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	const daemonID = "0e0e0e0e-0e0e-4e0e-8e0e-0e0e0e0e0e0e"
	const sessionID = "1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "laptop", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/proj", "proj", "t", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	token, err := db.MintSessionToken(ctx, pool, sessionID, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatalf("MintSessionToken: %v", err)
	}

	api := server.NewAPI(server.NewHub(), pool, "daemon-master-token-1234567890", nil, "")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/messages", api.HandlePostMessages)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// The CLI is resolved the way a real session resolves it: through the
	// ~/.local/bin entry sessionEnv prepends to PATH.
	home := t.TempDir()
	t.Setenv("HOME", home)
	binHome := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binHome, 0o755); err != nil {
		t.Fatal(err)
	}
	_, thisFile, _, _ := runtime.Caller(0)
	cli := filepath.Join(filepath.Dir(thisFile), "..", "..", ".claude", "skills", "session-messaging", "blerg-runner")
	if _, err := os.Stat(cli); err != nil {
		t.Fatalf("blerg-runner CLI not found at %s: %v", cli, err)
	}
	abs, err := filepath.Abs(cli)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(abs, filepath.Join(binHome, "blerg-runner")); err != nil {
		t.Fatal(err)
	}

	envFile := filepath.Join(t.TempDir(), "env.txt")
	binDir := fakeEngineDumpingEnv(t, envFile, `blerg-runner update "from inside the session" >&2`)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{
		ReposRoot: reposRoot, HomeDir: t.TempDir(), ServerHTTP: srv.URL,
	})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: sessionID, Repo: "proj", Kind: "agent",
		Engine: "codex", InitialPrompt: "hello", SessionToken: token,
	})
	sender.waitForKind(t, "turn_done")

	msgs, err := db.ListMessagesBySession(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("ListMessagesBySession: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "from inside the session" {
		t.Fatalf("blerg-runner did not post for this session: %+v", msgs)
	}
}
