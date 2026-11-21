package processcontainment

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// creators are the syscalls that can start a task. arm64 has no fork or vfork, and clone3 fails in
// InstallFilter's filter.
var creators = []uint32{unix.SYS_CLONE}

// stoppedSyscall is the syscall tid is stopped at the entry of.
func stoppedSyscall(tid int) (uint32, error) {
	var regs unix.PtraceRegs
	if err := unix.PtraceGetRegs(tid, &regs); err != nil {
		return 0, err
	}
	return uint32(regs.Regs[8]), nil //nolint:gosec // syscall numbers fit, and a filter compares only these bits
}

// creatingThread reports whether tid is stopped at the entry of a clone that makes a thread. The task
// is held, so these are the flags the kernel will read.
func creatingThread(tid int) (bool, error) {
	var regs unix.PtraceRegs
	if err := unix.PtraceGetRegs(tid, &regs); err != nil {
		return false, err
	}
	return regs.Regs[8] == unix.SYS_CLONE && regs.Regs[0]&unix.CLONE_THREAD != 0, nil
}

// failSyscall makes the syscall tid is stopped at the entry of return errno, without running it. The
// number lives in its own register set here: x8 is only what the task passed.
func failSyscall(tid int, errno syscall.Errno) error {
	var regs unix.PtraceRegs
	if err := unix.PtraceGetRegs(tid, &regs); err != nil {
		return err
	}
	regs.Regs[0] = -uint64(errno)
	if err := unix.PtraceSetRegs(tid, &regs); err != nil {
		return err
	}
	skip := int32(-1)
	vector := unix.Iovec{Base: (*byte)(unsafe.Pointer(&skip)), Len: uint64(unsafe.Sizeof(skip))} //nolint:gosec // a live int32 for the length of the call
	if _, _, e := unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_SETREGSET, uintptr(tid), unix.NT_ARM_SYSTEM_CALL, uintptr(unsafe.Pointer(&vector)), 0, 0); e != 0 {
		return e
	}
	return nil
}
