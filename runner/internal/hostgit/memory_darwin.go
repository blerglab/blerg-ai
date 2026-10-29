package hostgit

import (
	"context"
	"os/exec"
)

func platformMemoryWatch(o Options, cmd *exec.Cmd) *memoryWatch {
	if !o.MemoryLimit {
		return nil
	}
	return watchGroupMemoryObserved(cmd, memoryLimitKB(o), darwinGroupRSS, o.Diagnostic)
}

func darwinGroupRSS(ctx context.Context, pgid int) (uint64, error) {
	return readGroupRSS(ctx, pgid, darwinRSSCommand)
}
