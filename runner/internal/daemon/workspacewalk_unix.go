//go:build linux || darwin

package daemon

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Retain and revalidate every parent/child and '..' identity before using the
// next descriptor and before returning. Changed metadata also fails closed.
func workspaceWalkOpen(root *os.Root, name string, afterOpen func(int)) (*os.File, error) {
	return workspaceWalkOpenOptions(root, name, afterOpen, true)
}
func workspaceWalkOpenOptions(root *os.Root, name string, afterOpen func(int), sameDevice bool) (*os.File, error) {
	anchor, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	chain := []*os.File{anchor}
	defer func() {
		for _, f := range chain {
			_ = f.Close()
		}
	}()
	info, err := anchor.Stat()
	if err != nil {
		return nil, err
	}
	infos := []os.FileInfo{info}
	parts := strings.Split(name, "/")
	check := func() bool {
		for i, f := range chain {
			now, err := f.Stat()
			if err != nil || (!os.SameFile(now, infos[i]) || sameDevice && workspaceMetadataFingerprint(now) != workspaceMetadataFingerprint(infos[i])) {
				return false
			}
			if i == 0 {
				continue
			}
			var child, parent unix.Stat_t
			if unix.Fstatat(int(chain[i-1].Fd()), parts[i-1], &child, unix.AT_SYMLINK_NOFOLLOW) != nil || workspaceUnixIdentity(child) != workspaceRootIdentity(infos[i]) {
				return false
			}
			if unix.Fstatat(int(f.Fd()), "..", &parent, unix.AT_SYMLINK_NOFOLLOW) != nil || workspaceUnixIdentity(parent) != workspaceRootIdentity(infos[i-1]) {
				return false
			}
		}
		return true
	}
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(chain[len(chain)-1].Fd()), part, flags, 0)
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(fd), part)
		current, err := file.Stat()
		if err != nil || sameDevice && workspaceRootIdentity(current).Device != workspaceRootIdentity(infos[0]).Device {
			_ = file.Close()
			return nil, os.ErrPermission
		}
		if afterOpen != nil {
			afterOpen(i)
		}
		if !check() {
			_ = file.Close()
			return nil, os.ErrPermission
		}
		var named unix.Stat_t
		if unix.Fstatat(int(chain[len(chain)-1].Fd()), part, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || workspaceUnixIdentity(named) != workspaceRootIdentity(current) {
			_ = file.Close()
			return nil, os.ErrPermission
		}
		if i == len(parts)-1 {
			return file, nil
		}
		chain = append(chain, file)
		infos = append(infos, current)
	}
	return nil, os.ErrPermission
}
