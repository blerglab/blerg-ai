//go:build linux || darwin

package daemon

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// A listing retains its complete ancestry once. Each child open revalidates
// every edge, just like workspaceWalkOpen, without reopening the entire chain
// (and repeatedly checking every prefix) for every directory entry.
type workspaceListingChain struct {
	files []*os.File
	infos []os.FileInfo
	parts []string
}

func (c *workspaceListingChain) close() error {
	var err error
	for _, file := range c.files {
		err = errors.Join(err, file.Close())
	}
	return err
}

func (c *workspaceListingChain) valid() bool {
	for i, file := range c.files {
		info, err := file.Stat()
		if err != nil || !os.SameFile(info, c.infos[i]) || workspaceMetadataFingerprint(info) != workspaceMetadataFingerprint(c.infos[i]) {
			return false
		}
		if i == 0 {
			continue
		}
		var child, parent unix.Stat_t
		if unix.Fstatat(int(c.files[i-1].Fd()), c.parts[i-1], &child, unix.AT_SYMLINK_NOFOLLOW) != nil || workspaceUnixIdentity(child) != workspaceRootIdentity(c.infos[i]) {
			return false
		}
		if unix.Fstatat(int(file.Fd()), "..", &parent, unix.AT_SYMLINK_NOFOLLOW) != nil || workspaceUnixIdentity(parent) != workspaceRootIdentity(c.infos[i-1]) {
			return false
		}
	}
	return true
}

func (c *workspaceListingChain) openChild(name string) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") || !c.valid() {
		return nil, os.ErrPermission
	}
	parent := c.files[len(c.files)-1]
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	var named unix.Stat_t
	if err != nil || workspaceRootIdentity(info).Device != workspaceRootIdentity(c.infos[0]).Device || !c.valid() || unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || workspaceUnixIdentity(named) != workspaceRootIdentity(info) {
		_ = file.Close()
		return nil, os.ErrPermission
	}
	return file, nil
}

// This seam is also compiled on Linux so ancestry races and deep listings can
// exercise the Darwin implementation without selecting it for Linux callers.
func workspaceRetainedListing(e *LocalExplorer, name string) (*workspaceListing, error) {
	root, relative, _ := e.anchorFor(name)
	if root == nil {
		return nil, os.ErrPermission
	}
	anchor, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	c := &workspaceListingChain{files: []*os.File{anchor}}
	info, err := anchor.Stat()
	if err != nil {
		_ = c.close()
		return nil, err
	}
	c.infos = append(c.infos, info)
	if relative != "." {
		c.parts = strings.Split(relative, "/")
		for i := range c.parts {
			file, err := workspaceWalkOpen(root, strings.Join(c.parts[:i+1], "/"), nil)
			if err != nil {
				_ = c.close()
				return nil, err
			}
			c.files = append(c.files, file)
			info, err := file.Stat()
			if err != nil || !info.IsDir() {
				_ = c.close()
				return nil, os.ErrPermission
			}
			c.infos = append(c.infos, info)
		}
	}
	if !c.valid() {
		_ = c.close()
		return nil, os.ErrPermission
	}
	return &workspaceListing{file: c.files[len(c.files)-1], openChild: c.openChild, close: c.close, validate: c.valid}, nil
}
