package daemon

import (
	"os/exec"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// agentDriverKind is which driver an agent-kind session runs on.
type agentDriverKind int

const (
	driverNone       agentDriverKind = iota // refused: nothing can run it
	driverCLI                               // a registered CLI engine (codex, hermes, openclaw)
	driverClaudeCode                        // `claude -p` — the user's Claude Code login (or the key it is given)
	driverNative                            // the in-process API loop (needs ANTHROPIC_API_KEY)
)

// agentDriverInputs is everything the driver choice depends on.
type agentDriverInputs struct {
	Engine  string // "" / "claude" = Claude
	Sandbox bool   // runs in the Docker sandbox
	// ClaudeCodeForced is AgentHostConfig.ClaudeCode: the cluster pod's
	// explicit subscription mode.
	ClaudeCodeForced bool
	// PreferClaudeCLI is AgentHostConfig.PreferClaudeCLI: on a desktop
	// daemon a Claude session runs on the user's own `claude` login when the
	// CLI is installed. The cluster pod leaves it off and decides by
	// credential (ClaudeCodeForced).
	PreferClaudeCLI bool
	ClaudeOnPath    bool // `claude` resolves on the daemon's PATH
	ProviderSet     bool // a test provider is injected
	HasAPIKey       bool // ANTHROPIC_API_KEY is configured
}

// noClaudeEngineRefusal is the reason a host Claude agent session cannot run.
const noClaudeEngineRefusal = "no way to run a Claude agent session on this machine — install Claude Code and log in (`claude` on the daemon's PATH), or set ANTHROPIC_API_KEY for the daemon"

// chooseAgentDriver is the single decision of which driver an agent-kind
// session runs on — used by spawn, by the engine preflight and so by
// recovery, so a recovered session comes back on the same driver.
//
//   - a registered CLI engine (codex, hermes, openclaw) runs its CLI;
//   - in the sandbox, Claude always runs Claude Code inside the container
//     (the native loop's tools act on host paths, so "sandboxed" would be a
//     lie);
//   - an injected test provider runs the native loop;
//   - the cluster pod's subscription mode runs Claude Code;
//   - on a desktop daemon, Claude runs the user's `claude` CLI when it is
//     installed — every session uses the engine login, as documented — and
//     otherwise the native loop when an API key is configured;
//   - with neither, the spawn is refused with both fixes named.
func chooseAgentDriver(in agentDriverInputs) (agentDriverKind, string) {
	if _, cli := engineRegistry[in.Engine]; cli {
		return driverCLI, ""
	}
	if in.Sandbox {
		return driverClaudeCode, ""
	}
	if in.ProviderSet {
		return driverNative, ""
	}
	if in.ClaudeCodeForced {
		return driverClaudeCode, ""
	}
	if in.PreferClaudeCLI && in.ClaudeOnPath {
		return driverClaudeCode, ""
	}
	if in.HasAPIKey {
		return driverNative, ""
	}
	return driverNone, noClaudeEngineRefusal
}

// claudeOnPath reports whether `claude` resolves the way the driver will run
// it: exec.Command resolves a bare name against the daemon process's own
// PATH (not the child's env), so that is the PATH looked at here.
var claudeOnPath = func() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// driverFor applies chooseAgentDriver to a spawn on this host.
func (h *AgentHost) driverFor(msg protocol.SpawnSession) (agentDriverKind, string) {
	in := agentDriverInputs{
		Engine:           msg.Engine,
		Sandbox:          msg.Sandbox,
		ClaudeCodeForced: h.cfg.ClaudeCode,
		PreferClaudeCLI:  h.cfg.PreferClaudeCLI,
		ProviderSet:      h.cfg.Provider != nil,
		HasAPIKey:        h.cfg.APIKey != "",
	}
	// Only asked when it can change the answer (LookPath touches the disk).
	if in.PreferClaudeCLI && !in.Sandbox && !in.ProviderSet && !in.ClaudeCodeForced {
		in.ClaudeOnPath = claudeOnPath()
	}
	return chooseAgentDriver(in)
}
