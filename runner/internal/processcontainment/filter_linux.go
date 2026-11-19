//go:build linux

package processcontainment

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Tracing alone is insufficient: CLONE_UNTRACED can opt children out of ptrace.
// This inherited, no-new-privileges filter blocks that escape. clone3's flags
// live behind a pointer, so fail it with ENOSYS for libc's checked clone fallback.
func InstallFilter() error {
	var arch uint32
	switch runtime.GOARCH {
	case "amd64":
		arch = unix.AUDIT_ARCH_X86_64
	case "arm64":
		arch = unix.AUDIT_ARCH_AARCH64
	case "386":
		arch = unix.AUDIT_ARCH_I386
	default:
		return fmt.Errorf("%w: unsupported seccomp architecture %s", ErrUnproven, runtime.GOARCH)
	}
	const filterCount = 13
	filters := [filterCount]unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: arch, Jt: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x40000000, Jf: 1}, // no x32 ABI bypass
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_CLONE3, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_CLONE, Jf: 3},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
		{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: unix.CLONE_UNTRACED, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("%w: set no-new-privileges: %w", ErrUnproven, err)
	}
	program := unix.SockFprog{Len: filterCount, Filter: &filters[0]}
	//nolint:gosec // fixed, bounded kernel sock_fprog and live backing filter array
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil {
		return fmt.Errorf("%w: install seccomp filter: %w", ErrUnproven, err)
	}
	runtime.KeepAlive(filters)
	return nil
}

// installExecFilter gives the tracer a stop at every exec, before de_thread can free a TID, and at
// every syscall that can start a task, before it exists (the process limit). The architecture was
// checked by InstallFilter's filter, which the shim inherited. Inherited in turn by the whole tree, and
// it needs TRACESECCOMP: such a syscall by a task nobody seized fails with ENOSYS.
//
// The kernel takes the strictest answer of all a task's filters, and a filter of the tree's own that
// answers USER_NOTIF outranks TRACE: a listener replying CONTINUE would carry an exec past the stop.
// So seccomp(2) may not create a listener here, which also makes nested runtimes that need one fail
// with EPERM (see the design). Only seccomp(2) takes flags, in a 32-bit argument.
func installExecFilter() error {
	filters := []unix.SockFilter{{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0}}
	for _, syscall := range append([]uint32{unix.SYS_EXECVE, unix.SYS_EXECVEAT}, creators...) {
		filters = append(filters,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: syscall, Jf: 1},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_TRACE})
	}
	filters = append(filters,
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_SECCOMP, Jf: 3},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 24}, // low half of args[1], little-endian on every supported arch
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: unix.SECCOMP_FILTER_FLAG_NEW_LISTENER, Jf: 1},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]} //nolint:gosec // a handful of instructions
	//nolint:gosec // fixed, bounded kernel sock_fprog and live backing filter slice
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil {
		return fmt.Errorf("%w: install exec filter: %w", ErrUnproven, err)
	}
	runtime.KeepAlive(filters)
	return nil
}
