package server

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundProcessWorkers(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{{"", 2}, {"1", 1}, {"2", 2}, {"8", 2}, {"0", 1}, {"-1", 1}, {"invalid", 1}} {
		if got := boundProcessWorkers(tc.value); got != tc.want {
			t.Errorf("workers(%q)=%d; want %d", tc.value, got, tc.want)
		}
	}
}

func TestCutBoardBinaryBuildConcurrent(t *testing.T) {
	var builds atomic.Int32
	b := cutBoardBinaryBuild{build: func(_ context.Context, path string) error {
		builds.Add(1)
		return os.WriteFile(path, []byte("fixture"), 0600)
	}}
	t.Cleanup(func() {
		if err := b.cleanup(); err != nil {
			t.Error(err)
		}
	})
	var paths [8]string
	var elapsed [8]time.Duration
	var wg sync.WaitGroup
	for i := range paths {
		wg.Go(func() {
			var err error
			paths[i], elapsed[i], err = b.get(t.Context())
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if builds.Load() != 1 {
		t.Fatalf("built %d executables; want 1", builds.Load())
	}
	for i := range paths {
		if paths[i] != paths[0] || elapsed[i] != elapsed[0] {
			t.Fatal("concurrent callers did not share the build")
		}
	}
	path := paths[0]
	if err := b.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("executable survives package cleanup: %v", err)
	}
}

func TestCutBoardBinaryBuildFailureShared(t *testing.T) {
	cause := errors.New("fixture compiler failed")
	builds := 0
	b := cutBoardBinaryBuild{build: func(context.Context, string) error { builds++; return cause }}
	t.Cleanup(func() {
		if err := b.cleanup(); err != nil {
			t.Error(err)
		}
	})
	for range 2 {
		if _, _, err := b.get(t.Context()); !errors.Is(err, cause) {
			t.Fatalf("lost build cause: %v", err)
		}
	}
	if builds != 1 {
		t.Fatalf("retried failed build %d times; want 1", builds)
	}
	dir := b.dir
	if err := b.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed build directory survives cleanup: %v", err)
	}
}
