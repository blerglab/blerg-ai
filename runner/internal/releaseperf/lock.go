package releaseperf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrLockHeld means another process already holds the UI suite lock.
var ErrLockHeld = errors.New("the UI suite lock is already held")

// UISuiteLockPath computes the exact same lock file path
// scripts/ui_suite_lock.py uses to serialize pre-merge.sh's browser and
// performance suites, so release-perf shares that lock instead of inventing
// a second one that could race it: a repository-identity-keyed path under
// $XDG_CACHE_HOME (or ~/.cache)/blerg/gates/<hash>/ui-suite.lock.
// BLERG_UI_SUITE_LOCK overrides it, matching the Python script.
func UISuiteLockPath() (string, error) {
	if override := os.Getenv("BLERG_UI_SUITE_LOCK"); override != "" {
		return override, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-common-dir: %w", err)
	}
	common := strings.TrimSpace(string(out))
	identity, err := filepath.Abs(common)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(identity))
	key := hex.EncodeToString(sum[:])[:20]
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		cacheHome = filepath.Join(home, ".cache")
	}
	return filepath.Join(cacheHome, "blerg", "gates", key, "ui-suite.lock"), nil
}

// AcquireUISuiteLock takes the shared UI suite lock non-blockingly, returning
// ErrLockHeld if another process (or this one, through another open
// descriptor) already holds it.
func AcquireUISuiteLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: path is UISuiteLockPath's own hash-keyed cache path, never caller input
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrLockHeld, path)
		}
		return nil, err
	}
	return f, nil
}

// ReleaseUISuiteLock unlocks and closes the held lock file.
func ReleaseUISuiteLock(f *os.File) error {
	if f == nil {
		return nil
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}
