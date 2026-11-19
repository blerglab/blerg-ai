//go:build darwin || linux

package hostgit

import (
	"context"
	"encoding/binary"
	"errors"
	"syscall"
	"time"
)

const memorySampleTimeout = 100 * time.Millisecond

// These are Darwin's proc_info ABI constants. proc_taskinfo is six uint64s
// followed by twelve int32s on both amd64 and arm64; resident bytes start at 8.
// See https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/proc_info.h
// and https://github.com/apple-oss-distributions/xnu/blob/main/libsyscall/wrappers/libproc/libproc.c.
const (
	procInfoListPIDs = 1
	procInfoPIDInfo  = 2
	procPgrpOnly     = 2
	procPIDTaskInfo  = 4
	procTaskInfoSize = 96
	maxGroupPIDBytes = 64 << 10
)

type procInfo func(call, pid, flavor int, buf []byte) (int, error)

// nativeGroupRSS is shared with Linux tests, but only used on Darwin.
// A full PID buffer could hide helpers, so fail closed instead of accepting
// a truncated group. This bound covers 16K processes (above our group limit).
func nativeGroupRSS(ctx context.Context, pgid int, info procInfo) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, memorySampleTimeout)
	defer cancel()
	if pgid <= 0 || pgid > 1<<31-1 {
		return 0, errors.New("invalid RSS process group")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	pids := make([]byte, maxGroupPIDBytes)
	n, err := info(procInfoListPIDs, procPgrpOnly, pgid, pids)
	if err != nil {
		return 0, err
	}
	if n < 0 || n >= len(pids) || n%4 != 0 {
		return 0, errors.New("incomplete RSS process group sample")
	}
	var total uint64
	for offset := 0; offset < n; offset += 4 {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		pid := binary.LittleEndian.Uint32(pids[offset:])
		if pid == 0 || pid > 1<<31-1 {
			return 0, errors.New("invalid RSS process ID")
		}
		var task [procTaskInfoSize]byte
		size, err := info(procInfoPIDInfo, int(pid), procPIDTaskInfo, task[:])
		// A member may exit between enumeration and observation. Only the kernel's
		// explicit "no such process" is safe to omit; permissions/short reads fail.
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if size != len(task) {
			return 0, errors.New("incomplete RSS task sample")
		}
		rss := binary.LittleEndian.Uint64(task[8:16])
		if rss > ^uint64(0)-total {
			return 0, errors.New("RSS sample overflow")
		}
		total += rss
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// Round the sum up to KiB, preserving even sub-KiB observations.
	kb := total / 1024
	if total%1024 != 0 {
		kb++
	}
	return kb, nil
}
