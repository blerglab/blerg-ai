package releaseperf

import (
	"errors"
	"testing"
)

func TestUISuiteLockPathMatchesOverride(t *testing.T) {
	t.Setenv("BLERG_UI_SUITE_LOCK", "/tmp/example-lock-path/ui-suite.lock")
	got, err := UISuiteLockPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/example-lock-path/ui-suite.lock" {
		t.Fatalf("expected the override path to win, got %s", got)
	}
}

// This is the same flock scripts/ui_suite_lock.py uses to serialize
// pre-merge.sh's browser/performance suites: release-perf takes the exact
// same lock instead of inventing a second one that could race it.
func TestAcquireUISuiteLockIsExclusive(t *testing.T) {
	path := t.TempDir() + "/ui-suite.lock"
	held, err := AcquireUISuiteLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ReleaseUISuiteLock(held)

	_, err = AcquireUISuiteLock(path)
	if err == nil {
		t.Fatal("expected a second, non-blocking acquire on the same lock to fail")
	}
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld, got: %v", err)
	}
}

func TestAcquireUISuiteLockSucceedsAfterRelease(t *testing.T) {
	path := t.TempDir() + "/ui-suite.lock"
	held, err := AcquireUISuiteLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseUISuiteLock(held); err != nil {
		t.Fatal(err)
	}
	held2, err := AcquireUISuiteLock(path)
	if err != nil {
		t.Fatalf("expected the lock to be acquirable again after release, got: %v", err)
	}
	ReleaseUISuiteLock(held2)
}
