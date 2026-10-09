package runner

// Always-on plugins in the cluster pod: installed into the pod's Claude Code before the engine
// starts, from the list the server put in BLERG_RUNNER_PLUGINS (non-secret configuration,
// fetched from blerg-core for the account that started the session). The installer itself is
// shared with the workstation daemon: see pluginsinstall.

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/pluginsinstall"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// PluginResult is the outcome of an install run (pluginsinstall.Result).
type PluginResult = pluginsinstall.Result

// ParsePluginsEnv decodes BLERG_RUNNER_PLUGINS; see pluginsinstall.ParsePluginsEnv.
func ParsePluginsEnv(raw string) ([]pluginspec.Entry, error) {
	return pluginsinstall.ParsePluginsEnv(raw)
}

// InstallPlugins installs entries into the Claude Code under cfg.Home. The allow-list is
// BLERG_RUNNER_PLUGIN_MARKETPLACES as the server passed it into the pod (default: the
// official marketplace only).
func InstallPlugins(ctx context.Context, cfg Config, entries []pluginspec.Entry) PluginResult {
	allow, _ := pluginspec.ParseAllowlist(os.Getenv("BLERG_RUNNER_PLUGIN_MARKETPLACES"))
	return pluginsinstall.Install(ctx, pluginsinstall.Options{
		Bin:       "claude",
		Env:       pluginsinstall.ChildEnv(cfg.Home),
		Allow:     allow,
		Secrets:   []string{cfg.GitToken, cfg.APIKey, cfg.DaemonToken, os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")},
		LogPrefix: "runner: plugins",
	}, entries)
}

// installPluginsFunc is InstallPlugins, swappable in tests.
type installPluginsFunc func(context.Context, Config, []pluginspec.Entry) PluginResult

func installPlugins(ctx context.Context, cfg Config, entries []pluginspec.Entry) PluginResult {
	return InstallPlugins(ctx, cfg, entries)
}

// reportWorkspaceReady closes the clone stage and opens the engine stage, with the plugin
// install between them when this session has always-on plugins (Claude sessions only).
func reportWorkspaceReady(ctx context.Context, cfg Config, stages *stageReporter, install installPluginsFunc) {
	if cfg.PluginsErr != nil {
		log.Printf("runner: plugins: ignoring BLERG_RUNNER_PLUGINS: %v", cfg.PluginsErr)
	}
	engineActive := active(protocol.StageEngine, "Starting the engine")
	// A restricted session runs unattended on untrusted text with a narrowed
	// tool list: no plugin (code, hooks, MCP servers of its own) is installed
	// into it, whatever the operator's always-on list says.
	if len(cfg.Plugins) == 0 || cfg.skipsUserConfig() || (cfg.Engine != "" && cfg.Engine != pluginspec.EngineClaude) {
		stages.report(done(protocol.StageClone), engineActive)
		return
	}
	start := active(protocol.StagePlugins, fmt.Sprintf("Installing %d plugin(s)", len(cfg.Plugins)))
	start.Label = "Installing plugins" // the server's plan has it; the label keeps a late-adopted plan readable
	stages.report(done(protocol.StageClone), start)
	res := install(ctx, cfg, cfg.Plugins)
	log.Printf("runner: plugins: %s", res.Detail())
	state := protocol.StageStateDone
	if len(res.Failed)+len(res.Skipped) > 0 {
		state = protocol.StageStateWarning // finished, but not everything asked for was installed
	}
	stages.report(protocol.StartStage{ID: protocol.StagePlugins, Label: "Installing plugins", State: state, Detail: res.Detail()}, engineActive)
}
