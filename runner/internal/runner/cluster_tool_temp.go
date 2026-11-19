package runner

import (
	"os"
	"os/exec"
	"strings"
)

// Only untrusted agent/tool children receive bulk scratch. The supervisor's
// os.TempDir stays on /tmp, the pod-lifetime emptyDir, for private launch files.
// tool-tmp shares the bounded 4 GiB workspace-home emptyDir, never the PVC.
func clusterToolEnvironment(cmd *exec.Cmd) {
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "TMPDIR=") {
			out = append(out, entry)
		}
	}
	out = append(out, "TMPDIR=/workspace/tool-tmp")
	cmd.Env = out
}
