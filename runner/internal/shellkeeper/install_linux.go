//go:build linux

package shellkeeper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
)

// Install replaces only the daemon pathname. A live keeper retains its old
// executable inode and can exec helpers through /proc/self/exe. Keeper state
// and replacement maintenance are deliberately outside the install boundary.
func Install(ctx context.Context, dir, destination string) error {
	return installAt(ctx, dir, destination, nil)
}

func installAt(_ context.Context, _, destination string, beforeSwap func()) error {
	return installImage(destination, beforeSwap, true)
}

func installImage(destination string, beforeSwap func(), replace bool) error {
	in, err := os.Open("/proc/self/exe")
	if err != nil {
		return refusal("install_image", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.CreateTemp(filepath.Dir(destination), ".keeper-install-")
	if err != nil {
		return refusal("install_temp", err)
	}
	defer func() { _ = os.Remove(out.Name()) }()
	if err = out.Chmod(0755); err == nil {
		_, err = io.Copy(out, in)
	}
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return refusal("install_copy", err)
	}
	if beforeSwap != nil {
		beforeSwap()
	}
	if replace {
		err = os.Rename(out.Name(), destination)
	} else {
		err = os.Link(out.Name(), destination)
	}
	if err != nil {
		return refusal("install_rename", err)
	}
	parent, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return refusal("install_parent", err)
	}
	defer func() { _ = parent.Close() }()
	if err = parent.Sync(); err != nil {
		return refusal("install_sync", err)
	}
	return nil
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // trusted installer destination, never decoded from an agent request
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
