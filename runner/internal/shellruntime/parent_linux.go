//go:build linux

package shellruntime

import (
	"os/exec"

	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
)

// Linux parent-death signals bind to the creating THREAD, not just its process.
// Keep that thread alive until Wait finishes so idle Go thread retirement cannot
// kill a healthy helper. Daemon death kills its owned helper/client immediately.
func startOwnedCommand(cmd *exec.Cmd, done <-chan struct{}) error {
	return shellkeeper.StartOwned(cmd, done)
}
