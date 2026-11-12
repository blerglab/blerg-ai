package hostgit

import (
	"context"
	"os/exec"
	"syscall"
	"unsafe"
)

func platformMemoryWatch(o Options, cmd *exec.Cmd) *memoryWatch {
	if !o.MemoryLimit {
		return nil
	}
	return watchGroupMemoryObserved(cmd, memoryLimitKB(o), darwinGroupRSS, o.Diagnostic)
}

func darwinGroupRSS(ctx context.Context, pgid int) (uint64, error) {
	return nativeGroupRSS(ctx, pgid, darwinProcInfo)
}

// darwinProcInfo uses the same syscall as libproc's proc_listpids/proc_pidinfo.
// No cgo: the desktop installer can cross-build its Mach-O daemon from Linux.
// arg is zero for both operations; all buffers contain numeric data only.
func darwinProcInfo(call, pid, flavor int, buf []byte) (int, error) {
	n, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO, uintptr(call), uintptr(pid),
		uintptr(flavor), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))) //nolint:gosec // G103: synchronous kernel copyout into the nonempty buffer, bounded by its exact length.
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}
