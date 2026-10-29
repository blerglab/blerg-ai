package server

import (
	"slices"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// HostSelection is shared by placement and discovery. An unknown legacy report
// does not prove an engine missing; an explicit negative report excludes it.
type HostSelection struct {
	Kind       string
	Engine     string
	Runtime    string
	Restricted bool
}

func hostMatchesSelection(dc *DaemonConn, requirements []HostSelection) bool {
	if len(requirements) == 0 {
		return true
	}
	return hostEngineState(dc, requirements[0]) != "unavailable"
}

func hostEngineState(dc *DaemonConn, selection HostSelection) string {
	descriptor, supported := engines.Lookup(selection.Engine)
	if !supported {
		return "unavailable"
	}
	runtime := selection.Runtime
	if runtime == "" {
		runtime = resolveDaemonRuntime("", dc.SandboxAvailable())
	}
	if !descriptor.SupportsRuntime(runtime) || runtime == "cluster" || (runtime == "docker" && !dc.SandboxAvailable()) {
		return "unavailable"
	}
	if selection.Restricted {
		if selection.Kind == "tmux" {
			return "unavailable"
		}
		if !descriptor.Restricted || runtime != "docker" || !dc.CanRestrictTools() {
			return "unavailable"
		}
		// Legacy wire capability is converted here; consumers use the common
		// descriptor rather than reconstructing per-engine confinement policy.
		if descriptor.ID == "codex" && !dc.CanCodexRestricted() {
			return "unavailable"
		}
	}
	reported := dc.AvailableEngines()
	if descriptor.ID == "claude" {
		if selection.Kind == "tmux" && runtime == "daemon" {
			if cli, key := dc.HostClaude(); !cli && (key || reported != nil) {
				return "unavailable"
			}
		}
		if runtime == "docker" {
			if credential := dc.SandboxClaudeCredential(); credential != nil {
				if *credential {
					return "available"
				}
				return "unavailable"
			}
		} else if st, reported := reportedEngineStatus(dc, "claude"); reported && st.Status != protocol.EngineAvailable {
			// A daemon that reports statuses says when its Claude is not logged in:
			// the launch sheet shows why instead of offering a launch that fails.
			return "unavailable"
		} else if cli, key := dc.HostClaude(); cli || key {
			return "available"
		}
	}
	if reported == nil {
		return "unknown"
	}
	if slices.Contains(reported, descriptor.ID) {
		return "available"
	}
	return "unavailable"
}

func engineSteeringSupported(engine, kind string) bool {
	descriptor, ok := engines.Lookup(engine)
	return ok && kind == "agent" && descriptor.Steering
}

// reportedEngineStatus is the daemon's own status for engine, when it reports statuses.
func reportedEngineStatus(dc *DaemonConn, engine string) (protocol.EngineStatus, bool) {
	for _, st := range dc.EngineStatuses() {
		if st.Engine == engine {
			return st, true
		}
	}
	return protocol.EngineStatus{}, false
}
