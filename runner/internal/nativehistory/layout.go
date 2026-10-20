// Package nativehistory describes the native conversation data an engine may
// retain. Authentication, plugins, configuration and grants never live here.
package nativehistory

import "github.com/blerglab/blerg-ai/runner/internal/models"

const Root = "/var/run/blerg-native-history"
const IdentityPath = Root + "/identity"
const RuntimeCodexHome = "/workspace/.blerg-native-runtime/codex"

type Mount struct{ SubPath, Path string }

// Layout is one engine's retained conversation data. Home is the engine's
// own directory under the agent's HOME; every Mount under HOME sits beneath
// it. A nested mountpoint makes the container runtime create its missing
// parents as root, so the pod prepares Home itself as an agent-owned,
// pod-ephemeral directory: the engine's login and config are written there,
// never to the native-history volume.
type Layout struct {
	Engine string
	Home   string
	Mounts []Mount
}

var layouts = map[string]Layout{
	"claude": {Engine: "claude", Home: "/workspace/.claude", Mounts: []Mount{{SubPath: "claude-projects", Path: "/workspace/.claude/projects"}}},
	"codex":  {Engine: "codex", Home: "/workspace/.codex", Mounts: []Mount{{SubPath: "codex-sessions", Path: "/workspace/.codex/sessions"}, {SubPath: "codex-sqlite", Path: Root + "/sqlite"}}},
}

func ForEngine(engine string) (Layout, bool) {
	l, ok := layouts[models.NormalizeEngine(engine)]
	l.Mounts = append([]Mount(nil), l.Mounts...)
	return l, ok
}
