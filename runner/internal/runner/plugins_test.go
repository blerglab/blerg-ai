package runner

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
)

const official = "anthropics/claude-plugins-official"

func ent(p string) pluginspec.Entry { return pluginspec.Entry{Marketplace: official, Plugin: p} }

func newTestInstaller(run pluginRunner) *pluginInstaller {
	allow, _ := pluginspec.ParseAllowlist("")
	return &pluginInstaller{bin: "claude", allow: allow, run: run, cmdLimit: time.Second, total: 5 * time.Second}
}

const listJSON = `[{"name":"claude-plugins-official","source":"github","repo":"anthropics/claude-plugins-official"}]`

func TestInstallCommandsAreExactArgLists(t *testing.T) {
	var calls [][]string
	p := newTestInstaller(func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 2 && args[2] == "list" {
			return listJSON, nil
		}
		return "", nil
	})
	res := p.install(context.Background(), []pluginspec.Entry{ent("frontend-design"), ent("superpowers")})
	if res.Installed != 2 || res.Total != 2 || res.Detail() != "2 of 2 installed" {
		t.Fatalf("res = %+v %q", res, res.Detail())
	}
	want := [][]string{
		{"claude", "plugin", "marketplace", "add", official},
		{"claude", "plugin", "marketplace", "list", "--json"},
		{"claude", "plugin", "install", "frontend-design@claude-plugins-official", "--scope", "user"},
		{"claude", "plugin", "install", "superpowers@claude-plugins-official", "--scope", "user"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v", calls)
	}
	for i := range want {
		if strings.Join(calls[i], "\x00") != strings.Join(want[i], "\x00") {
			t.Errorf("call %d = %v, want %v", i, calls[i], want[i])
		}
	}
}

func TestOneFailureDoesNotStopOthersAndDetailIsFixed(t *testing.T) {
	p := newTestInstaller(func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 1 && args[1] == "install" && strings.HasPrefix(args[2], "frontend-design@") {
			// Raw output that must never reach an event.
			return "fatal: could not read Password for https://x-access-token:SECRET123@github.com", errors.New("exit 1")
		}
		return listJSON, nil
	})
	res := p.install(context.Background(), []pluginspec.Entry{ent("frontend-design"), ent("superpowers")})
	if res.Installed != 1 || res.Total != 2 {
		t.Fatalf("res = %+v", res)
	}
	if got := res.Detail(); got != "1 of 2 installed — frontend-design failed" {
		t.Fatalf("detail = %q", got)
	}
	if strings.Contains(res.Detail(), "SECRET123") {
		t.Fatal("raw output leaked into the detail")
	}
}

func TestMarketplaceAddFailureFailsItsPluginsOnly(t *testing.T) {
	allow, _ := pluginspec.ParseAllowlist("*")
	p := newTestInstaller(func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 3 && args[2] == "add" && args[3] == "bad/market" {
			return "nope", errors.New("exit 1")
		}
		if args[1] == "marketplace" && args[2] == "list" {
			return `[{"name":"claude-plugins-official","source":"github","repo":"anthropics/claude-plugins-official"},{"name":"bad","source":"github","repo":"bad/market"}]`, nil
		}
		return "", nil
	})
	p.allow = allow
	res := p.install(context.Background(), []pluginspec.Entry{{Marketplace: "bad/market", Plugin: "x"}, ent("superpowers")})
	if res.Installed != 1 || len(res.Failed) != 1 || res.Failed[0] != "x" {
		t.Fatalf("res = %+v", res)
	}
}

func TestDisallowedAndMalformedEntriesAreSkippedNeverRun(t *testing.T) {
	var calls []string
	p := newTestInstaller(func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return listJSON, nil
	})
	res := p.install(context.Background(), []pluginspec.Entry{
		{Marketplace: "evil/plugins", Plugin: "steal"},
		{Marketplace: "../../etc", Plugin: "x"},
		{Marketplace: official, Plugin: "a;rm -rf /"},
		{Marketplace: official, Plugin: "--scope"},
		ent("superpowers"),
	})
	for _, c := range calls {
		if strings.Contains(c, "evil") || strings.Contains(c, "..") || strings.Contains(c, ";") || strings.Contains(c, "--scope --") {
			t.Fatalf("a refused entry reached the CLI: %q", c)
		}
	}
	if res.Installed != 1 || len(res.Skipped) != 4 {
		t.Fatalf("res = %+v", res)
	}
	if want := "1 of 5 installed — steal, x, (invalid entry), (invalid entry) skipped (not allowed)"; res.Detail() != want {
		t.Fatalf("detail = %q, want %q", res.Detail(), want)
	}
	if strings.Contains(res.Detail(), "rm -rf") {
		t.Fatalf("invalid entry text reached the detail: %q", res.Detail())
	}
}

func TestPerCommandTimeoutAndTotalBudget(t *testing.T) {
	p := newTestInstaller(func(ctx context.Context, _ string, args ...string) (string, error) {
		if args[1] == "marketplace" && args[2] == "list" {
			return listJSON, nil
		}
		if args[1] == "install" {
			<-ctx.Done() // hangs until the per-command timeout fires
			return "", ctx.Err()
		}
		return "", nil
	})
	p.cmdLimit = 50 * time.Millisecond
	start := time.Now()
	res := p.install(context.Background(), []pluginspec.Entry{ent("a"), ent("b")})
	if res.Installed != 0 || len(res.Failed) != 2 {
		t.Fatalf("res = %+v", res)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("per-command timeout was not applied")
	}

	// The overall budget ends the run: once spent, remaining commands see a dead context.
	p2 := newTestInstaller(func(ctx context.Context, _ string, args ...string) (string, error) {
		if args[1] == "marketplace" && args[2] == "list" {
			return listJSON, nil
		}
		if args[1] == "install" {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "", nil
	})
	p2.cmdLimit = 10 * time.Second
	p2.total = 80 * time.Millisecond
	start = time.Now()
	res = p2.install(context.Background(), []pluginspec.Entry{ent("a"), ent("b"), ent("c")})
	if res.Installed != 0 || time.Since(start) > 3*time.Second {
		t.Fatalf("total budget not enforced: %+v after %s", res, time.Since(start))
	}
}

func TestMarketplaceNameFromListMustBeSafe(t *testing.T) {
	var installArg string
	p := newTestInstaller(func(_ context.Context, _ string, args ...string) (string, error) {
		if args[1] == "marketplace" && args[2] == "list" {
			return `[{"name":"--evil flag","source":"github","repo":"anthropics/claude-plugins-official"}]`, nil
		}
		if args[1] == "install" {
			installArg = args[2]
		}
		return "", nil
	})
	res := p.install(context.Background(), []pluginspec.Entry{ent("superpowers")})
	if installArg != "" || res.Installed != 0 {
		t.Fatalf("an unsafe marketplace name reached install: %q %+v", installArg, res)
	}
}

func TestParsePluginsEnv(t *testing.T) {
	if e, err := ParsePluginsEnv(""); e != nil || err != nil {
		t.Fatal("empty is no plugins")
	}
	e, err := ParsePluginsEnv(`[{"marketplace":"a/b","plugin":"c"}]`)
	if err != nil || len(e) != 1 || e[0].Plugin != "c" {
		t.Fatalf("%v %v", e, err)
	}
	for _, bad := range []string{`{`, `{"a":1}`, strings.Repeat("x", maxPluginsEnvBytes+1)} {
		if e, err := ParsePluginsEnv(bad); e != nil || err == nil {
			t.Errorf("%.20q accepted", bad)
		}
	}
	many := "[" + strings.Repeat(`{"marketplace":"a/b","plugin":"c"},`, 20) + `{"marketplace":"a/b","plugin":"c"}]`
	if _, err := ParsePluginsEnv(many); err == nil {
		t.Error("over the cap accepted")
	}
}

// A fake `claude` on PATH proves the real exec path end to end: argument lists, HOME (the
// directory the engine's own turns use), no secrets in the child environment, and files landing
// under $HOME/.claude.
func TestInstallPluginsWithFakeClaudeOnPath(t *testing.T) {
	bin := t.TempDir()
	home := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls.log")
	script := `#!/bin/sh
{ echo "ARGS: $*"; echo "HOME=$HOME"; echo "TOKEN=${CLAUDE_CODE_OAUTH_TOKEN:-unset} GIT=${BLERG_RUNNER_GIT_TOKEN:-unset}"; } >> "` + log + `"
mkdir -p "$HOME/.claude/plugins"
case "$*" in
  "plugin marketplace list --json") echo '` + listJSON + `' ;;
  "plugin install frontend-design@"*) echo "boom $CLAUDE_CODE_OAUTH_TOKEN"; exit 1 ;;
  "plugin install "*) echo installed > "$HOME/.claude/plugins/installed-$3" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-secret-value")
	t.Setenv("BLERG_RUNNER_GIT_TOKEN", "git-secret-value")
	t.Setenv("BLERG_RUNNER_PLUGIN_MARKETPLACES", "")

	res := InstallPlugins(context.Background(), Config{Home: home}, []pluginspec.Entry{ent("frontend-design"), ent("superpowers")})
	if res.Detail() != "1 of 2 installed — frontend-design failed" {
		t.Fatalf("detail = %q", res.Detail())
	}
	raw, _ := os.ReadFile(log)
	got := string(raw)
	for _, want := range []string{
		"ARGS: plugin marketplace add " + official,
		"ARGS: plugin marketplace list --json",
		"ARGS: plugin install superpowers@claude-plugins-official --scope user",
		"HOME=" + home,
		"TOKEN=unset GIT=unset",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fake claude log missing %q:\n%s", want, got)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "plugins")); err != nil {
		t.Errorf("nothing was written under HOME/.claude: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "plugins", "installed-superpowers@claude-plugins-official")); err != nil {
		t.Errorf("superpowers was not actually installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "plugins", "installed-frontend-design@claude-plugins-official")); err == nil {
		t.Error("frontend-design failed but left an installed marker")
	}
}

// The children get an allow-listed environment and an empty scratch directory: a fake claude
// dumps both, and none of the secret-shaped variables (or the workspace) may appear.
func TestInstallChildrenGetAllowlistedEnvAndScratchCwd(t *testing.T) {
	bin, home, work := t.TempDir(), t.TempDir(), t.TempDir()
	dump := filepath.Join(t.TempDir(), "dump.txt")
	script := "#!/bin/sh\n{ echo \"CWD=$(pwd)\"; env; } >> \"" + dump + "\"\ncase \"$*\" in \"plugin marketplace list --json\") echo '" + listJSON + "' ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Chdir(work)
	secrets := map[string]string{
		"BLERG_RUNNER_DAEMON_TOKEN": "s1", "BLERG_RUNNER_GIT_TOKEN": "s2", "ANTHROPIC_API_KEY": "s3",
		"CLAUDE_CODE_OAUTH_TOKEN": "s4", "BLERG_BOARD_TOKEN": "s5", "BLERG_RUNNER_SESSION_TOKEN": "s6",
		"BLERG_RUNNER_INITIAL_PROMPT": "s7", "OPENAI_API_KEY": "s8", "GH_TOKEN": "s9", "GITHUB_TOKEN": "s10",
		"CODEX_AUTH_JSON": "s11", "HERMES_ENV_CONTENTS": "s12",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	t.Setenv("LC_ALL", "C")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	t.Setenv("BLERG_RUNNER_PLUGIN_MARKETPLACES", "")
	res := InstallPlugins(context.Background(), Config{Home: home}, []pluginspec.Entry{ent("superpowers")})
	if res.Installed != 1 {
		t.Fatalf("res = %+v", res)
	}
	raw, _ := os.ReadFile(dump)
	got := string(raw)
	for k := range secrets {
		if strings.Contains(got, k+"=") {
			t.Errorf("child env contains %s", k)
		}
	}
	for _, want := range []string{"HOME=" + home, "LC_ALL=C", "HTTPS_PROXY=http://proxy.invalid:3128", "PATH="} {
		if !strings.Contains(got, want) {
			t.Errorf("child env lacks %s", want)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "CWD=") {
			cwd := strings.TrimPrefix(line, "CWD=")
			if cwd == work || strings.HasPrefix(cwd, work) || !strings.Contains(cwd, "blerg-plugins-") {
				t.Errorf("child cwd = %q, want a scratch dir outside %q", cwd, work)
			}
			if _, err := os.Stat(cwd); err == nil {
				t.Errorf("scratch dir %q was not removed", cwd)
			}
		}
	}
}

// A timeout must kill the whole process group, not just claude: a grandchild that would write a
// marker after the timeout must be dead. Uses a real process tree.
func TestTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	bin, home := t.TempDir(), t.TempDir()
	marker := filepath.Join(t.TempDir(), "grandchild-survived")
	script := "#!/bin/sh\n(sleep 1; echo alive > \"" + marker + "\") &\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	allow, _ := pluginspec.ParseAllowlist("")
	p := &pluginInstaller{bin: "claude", env: pluginChildEnv(home), allow: allow, cmdLimit: 200 * time.Millisecond, total: time.Minute}
	p.run = p.execRunner
	start := time.Now()
	_, err := p.step(context.Background(), "test", "plugin", "install", "x@y")
	if err == nil {
		t.Fatal("the hung command should have failed")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("timeout took %s", time.Since(start))
	}
	time.Sleep(1800 * time.Millisecond) // well past when the grandchild would have written
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the grandchild outlived the timeout")
	}
}

func stageIDsAndStates(t *testing.T, fs *fakeSender) []string {
	t.Helper()
	var out []string
	for _, m := range fs.sent {
		ev := m.(protocol.AgentEvent)
		var p protocol.StartStagePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		for _, s := range p.Stages {
			out = append(out, s.ID+":"+s.State+":"+s.Detail)
		}
	}
	return out
}

func TestReportWorkspaceReadyEmitsPluginsStageBetweenCloneAndEngine(t *testing.T) {
	fs := &fakeSender{}
	r := &stageReporter{s: fs, sessionID: "sid"}
	cfg := Config{Plugins: []pluginspec.Entry{ent("a"), ent("b")}}
	reportWorkspaceReady(context.Background(), cfg, r, func(context.Context, Config, []pluginspec.Entry) PluginResult {
		return PluginResult{Total: 2, Installed: 1, Failed: []string{"b"}}
	})
	got := strings.Join(stageIDsAndStates(t, fs), " | ")
	want := "clone:done: | plugins:active:Installing 2 plugin(s) | plugins:warning:1 of 2 installed — b failed | engine:active:Starting the engine"
	if got != want {
		t.Fatalf("stages =\n%s\nwant\n%s", got, want)
	}
}

func TestReportWorkspaceReadyWithoutPluginsOrForOtherEngines(t *testing.T) {
	for _, cfg := range []Config{{}, {Engine: "codex", Plugins: []pluginspec.Entry{ent("a")}}} {
		fs := &fakeSender{}
		r := &stageReporter{s: fs, sessionID: "sid"}
		reportWorkspaceReady(context.Background(), cfg, r, func(context.Context, Config, []pluginspec.Entry) PluginResult {
			t.Fatal("install must not run")
			return PluginResult{}
		})
		got := strings.Join(stageIDsAndStates(t, fs), " | ")
		if strings.Contains(got, "plugins") || !strings.Contains(got, "engine:active") {
			t.Fatalf("stages = %s", got)
		}
	}
}

// Opt-in: BLERG_TEST_REAL_CLAUDE=1 runs the real claude CLI (network, GitHub) against a
// temporary HOME, the way the pod does. Not part of the default run.
func TestInstallPluginsWithRealClaude(t *testing.T) {
	if os.Getenv("BLERG_TEST_REAL_CLAUDE") != "1" {
		t.Skip("BLERG_TEST_REAL_CLAUDE not set")
	}
	home := t.TempDir()
	t.Setenv("BLERG_RUNNER_PLUGIN_MARKETPLACES", "")
	res := InstallPlugins(context.Background(), Config{Home: home}, []pluginspec.Entry{ent("frontend-design"), ent("superpowers")})
	if res.Detail() != "2 of 2 installed" {
		t.Fatalf("detail = %q", res.Detail())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err != nil {
		t.Fatalf("user settings not written under the pod HOME: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	for _, want := range []string{"superpowers@claude-plugins-official", "frontend-design@claude-plugins-official"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("settings.json lacks %s: %s", want, raw)
		}
	}
}
