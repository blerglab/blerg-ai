// Package nativehistory describes the native conversation data an engine may
// retain. Authentication, plugins, configuration and grants never live here.
package nativehistory

import "github.com/blerglab/blerg-ai/runner/internal/models"

const Root = "/var/run/blerg-native-history"
const IdentityPath = Root + "/identity"
const RuntimeCodexHome = "/workspace/.blerg-native-runtime/codex"

type Mount struct{ SubPath, Path string }
type Layout struct {
	Engine string
	Mounts []Mount
}

var layouts = map[string]Layout{
	"claude": {Engine: "claude", Mounts: []Mount{{SubPath: "claude-projects", Path: "/workspace/.claude/projects"}}},
	"codex":  {Engine: "codex", Mounts: []Mount{{SubPath: "codex-sessions", Path: "/workspace/.codex/sessions"}, {SubPath: "codex-sqlite", Path: Root + "/sqlite"}}},
}

func ForEngine(engine string) (Layout, bool) {
	l, ok := layouts[models.NormalizeEngine(engine)]
	l.Mounts = append([]Mount(nil), l.Mounts...)
	return l, ok
}
