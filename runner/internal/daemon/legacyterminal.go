package daemon

import (
	"context"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// LegacyTerminalWarning probes surviving terminals once for one daemon instance.
type LegacyTerminalWarning struct{ once sync.Once }

// Warn reports surviving terminals without resuming or stopping them.
func (w *LegacyTerminalWarning) Warn() {
	w.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "tmux", "list-sessions", "-F", "#S").Output()
		if err != nil {
			return
		} // tmux or its server may no longer be installed/running
		for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.HasPrefix(name, "blerg-") {
				log.Printf("upgrade: legacy Terminal %q is still running; it will not be resumed or stopped automatically; see the changelog for manual cleanup", name)
			}
		}
	})
}
