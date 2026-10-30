//go:build linux

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
	"golang.org/x/sys/unix"
)

type hostHelperIdentity struct {
	PID           int
	Start, Boot   string
	Device, Inode uint64
}

func hostHelperIdentityFor(pid int) (hostHelperIdentity, error) {
	result := hostHelperIdentity{PID: pid}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return result, err
	}
	end := strings.LastIndex(string(raw), ")")
	if end < 0 {
		return result, errors.New("helper identity unavailable")
	}
	fields := strings.Fields(string(raw)[end+1:])
	if len(fields) < 20 {
		return result, errors.New("helper identity unavailable")
	}
	result.Start = fields[19]
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return result, err
	}
	result.Boot = strings.TrimSpace(string(boot))
	var st unix.Stat_t
	if err = unix.Stat(fmt.Sprintf("/proc/%d/exe", pid), &st); err != nil {
		return result, err
	}
	result.Device = st.Dev
	result.Inode = st.Ino
	return result, nil
}

// pidfds pin the exact kernel process before checking start/boot/executable;
// signaling can never race numeric PID reuse. EXITKILL joins its traced tree.
func reapHostHelper(ctx context.Context, want hostHelperIdentity) error {
	if want.PID <= 0 || want.Start == "" || want.Boot == "" {
		return errors.New("invalid helper receipt")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(boot)) != want.Boot {
		return nil
	}
	fd, err := unix.PidfdOpen(want.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	got, err := hostHelperIdentityFor(want.PID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("host helper identity replaced")
	}
	if err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	for ctx.Err() == nil {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}} //nolint:gosec // Linux file descriptors are signed int32.
		n, e := unix.Poll(fds, 20)
		if e != nil && !errors.Is(e, unix.EINTR) {
			return e
		}
		if n > 0 {
			return nil
		}
	}
	return ctx.Err()
}

func hostControllerGone(want hostHelperIdentity) error {
	if want.PID <= 0 || want.Start == "" {
		return errors.New("host controller identity unavailable")
	}
	got, err := hostHelperIdentityFor(want.PID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if got != want {
		return nil
	}
	return errors.New("host controller still owns this launch")
}
func hostWriterGroupFor(executable, session string, started func(int) error) hostWriterGroup {
	g := processcontainment.NewGroup(executable, uint32(os.Getuid()), uint32(os.Getgid())) //nolint:gosec // Linux UID/GID are unsigned 32-bit values returned as int.
	g.BeforeExec = started
	g.Label = "session " + session
	return g
}
func ServeHostWriter(args []string) int { return processcontainment.ServeCommand(args) }
