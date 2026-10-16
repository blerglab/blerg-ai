package daemon

import (
	"os"
	"strings"
	"syscall"
)

// Open each component relative to an already-open directory and refuse all
// symlinks atomically. os.Root confines the initial handle; openat no-follow
// also blocks aliases into denied in-workspace state and symlink races.
func codexOpenRestrictedFile(root *os.Root, name string, write, create, directory bool) (*os.File, error) {
	base, err := root.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = base.Close() }()
	current := int(base.Fd())
	owned := false
	defer func() {
		if owned {
			_ = syscall.Close(current)
		}
	}()
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := syscall.Openat(current, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err == syscall.ENOENT && create {
			if err = syscall.Mkdirat(current, part, 0o755); err == nil || err == syscall.EEXIST {
				next, err = syscall.Openat(current, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
			}
		}
		if err != nil {
			return nil, err
		}
		if owned {
			_ = syscall.Close(current)
		}
		current = next
		owned = true
	}
	flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
	if write {
		flags = syscall.O_RDWR | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
	}
	if create {
		flags |= syscall.O_CREAT
	}
	if directory {
		flags |= syscall.O_DIRECTORY
	}
	fd, err := syscall.Openat(current, parts[len(parts)-1], flags, 0o644)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func codexRestrictedRegular(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return info.Mode().IsRegular() && ok && stat.Nlink == 1
}
