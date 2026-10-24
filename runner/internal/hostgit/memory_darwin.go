package hostgit

import (
	"context"
	"os/exec"
)

func platformMemoryWatch(o Options, cmd *exec.Cmd) *memoryWatch {
	if !o.MemoryLimit {
		return nil
	}
	return watchGroupMemory(cmd, memoryLimitKB(o), darwinGroupRSS)
}

func darwinGroupRSS(ctx context.Context, pgid int) (uint64, error) {
	return readGroupRSS(ctx, pgid, darwinRSSCommand)
}
