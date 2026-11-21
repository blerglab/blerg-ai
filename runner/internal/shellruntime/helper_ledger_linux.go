//go:build linux

package shellruntime

import (
	"context"
	"os"
	"os/exec"

	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
	"golang.org/x/sys/unix"
)

func helperProcessLedger(ctx context.Context, cmd *exec.Cmd, enabled bool) (func() bool, func(), error) {
	if !enabled {
		return nil, func() {}, nil
	}
	ledger, child, err := processcontainment.NewShellLedger()
	if err != nil {
		_ = bridgeFailure("helper_ledger_create", err)
		return nil, nil, err
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, child)
	cmd.Env = append(cmd.Env, "BLERG_SHELL_LEDGER_FD=3")
	// The inherited descriptor must not remain open in the keeper after Start.
	return func() bool {
		err := ledger.Wait(ctx)
		ledger.Close()
		if err != nil {
			_ = bridgeFailure("helper_tree_unproven", err)
		}
		return err == nil
	}, func() { _ = child.Close() }, nil
}

func shellProcessRecorder() (*os.File, func(int) error) {
	if os.Getenv("BLERG_SHELL_LEDGER_FD") != "3" {
		return nil, nil
	}
	f := os.NewFile(3, "shell-process-ledger")
	unix.CloseOnExec(3)
	return f, func(pidfd int) error { return processcontainment.RecordShellProcess(f, pidfd) }
}
