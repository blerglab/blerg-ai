package runner

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
)

const official = "anthropics/claude-plugins-official"

func ent(p string) pluginspec.Entry { return pluginspec.Entry{Marketplace: official, Plugin: p} }

const listJSON = `[{"name":"claude-plugins-official","source":"github","repo":"anthropics/claude-plugins-official"}]`

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
