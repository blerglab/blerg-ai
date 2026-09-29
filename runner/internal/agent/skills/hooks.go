package skills

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type hooksFile struct {
	Hooks struct {
		SessionStart []struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"SessionStart"`
	} `json:"hooks"`
}

// hookEnv is the environment a SessionStart hook runs with: the host env
// plus CLAUDE_PLUGIN_ROOT, minus the runner daemon master token. Hooks are
// operator-installed plugin code, not model-driven, but when the host is the
// blerg-runner daemon its env holds full runner admin and no child process
// of a session start should ever see it (desktop-safety C1).
func hookEnv(vdir string) []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+1)
	for _, e := range base {
		if !strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") {
			env = append(env, e)
		}
	}
	return append(env, "CLAUDE_PLUGIN_ROOT="+vdir)
}

// SessionStartContext executes SessionStart hooks of enabled plugins and
// returns their concatenated additionalContext / stdout for system-prompt
// assembly. This is what loads superpowers' skill-discipline preamble —
// without it the skills list is inert. Errors skip the hook (never fatal).
func SessionStartContext(projectDir, homeDir string) string {
	var out strings.Builder
	for _, plugin := range enabledPlugins(projectDir) {
		vdir := pluginVersionDir(homeDir, plugin)
		if vdir == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(vdir, "hooks/hooks.json")) //nolint:gosec // path is hooks/hooks.json inside an installed plugin version dir under the user's own ~/.claude/plugins
		if err != nil {
			continue
		}
		var hf hooksFile
		if err := json.Unmarshal(data, &hf); err != nil {
			continue
		}
		for _, group := range hf.Hooks.SessionStart {
			for _, h := range group.Hooks {
				if h.Type != "command" {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				cmd := exec.CommandContext(ctx, "bash", "-c", h.Command) //nolint:gosec // runs the SessionStart hook a user-enabled plugin declares (Claude Code hook semantics); 10s timeout, minimal hookEnv, no request input
				cmd.Dir = projectDir
				cmd.Env = hookEnv(vdir)
				stdout, err := cmd.Output()
				cancel()
				if err != nil {
					continue
				}
				text := string(stdout)
				var parsed struct {
					HookSpecificOutput struct {
						AdditionalContext string `json:"additionalContext"`
					} `json:"hookSpecificOutput"`
				}
				if json.Unmarshal(stdout, &parsed) == nil && parsed.HookSpecificOutput.AdditionalContext != "" {
					text = parsed.HookSpecificOutput.AdditionalContext
				}
				out.WriteString(text)
				out.WriteString("\n")
			}
		}
	}
	return out.String()
}
