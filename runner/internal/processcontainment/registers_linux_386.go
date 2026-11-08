package processcontainment

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// creators are the syscalls that can start a task. clone3 fails in InstallFilter's filter.
var creators = []uint32{unix.SYS_CLONE, unix.SYS_FORK, unix.SYS_VFORK}

// stoppedSyscall is the syscall tid is stopped at the entry of.
func stoppedSyscall(tid int) (uint32, error) {
	var regs unix.PtraceRegs
	if err := unix.PtraceGetRegs(tid, &regs); err != nil {
		return 0, err
	}
	return uint32(regs.Orig_eax), nil //nolint:gosec // syscall numbers fit, and a filter compares only these bits
}

// creatingThread reports whether tid is stopped at the entry of a clone that makes a thread. The task
// is held, so these are the flags the kernel will read.
func creatingThread(tid int) (bool, error) {
	var regs unix.PtraceRegs
	if err := unix.PtraceGetRegs(tid, &regs); err != nil {
		return false, err
	}
	return regs.Orig_eax == unix.SYS_CLONE && regs.Ebx&unix.CLONE_THREAD != 0, nil
}

// failSyscall makes the syscall tid is stopped at the entry of return errno, without running it.
func failSyscall(tid int, errno syscall.Errno) error {
	var regs unix.PtraceRegs
	if err := unix.PtraceGetRegs(tid, &regs); err != nil {
		return err
	}
	regs.Orig_eax = -1
	regs.Eax = -int32(errno)
	return unix.PtraceSetRegs(tid, &regs)
}
