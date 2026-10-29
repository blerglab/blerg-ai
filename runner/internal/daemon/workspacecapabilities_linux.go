//go:build linux

package daemon

import "golang.org/x/sys/unix"

func workspacePlatformProbe() error {
	if _, err := hostHelperIdentityFor(unix.Getpid()); err != nil {
		return err
	}
	pidfd, err := unix.PidfdOpen(unix.Getpid(), 0)
	if err != nil {
		return err
	}
	_ = unix.Close(pidfd)
	fd, err := unix.Openat2(unix.AT_FDCWD, "/", &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return err
	}
	return unix.Close(fd)
}
