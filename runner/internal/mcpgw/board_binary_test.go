package mcpgw

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The executable belongs to the package process, not the first test that uses it.
type boardBinaryBuild struct {
	once    sync.Once
	build   func(context.Context, string) error
	dir     string
	path    string
	elapsed time.Duration
	err     error
}

func (b *boardBinaryBuild) get(ctx context.Context) (string, time.Duration, error) {
	b.once.Do(func() {
		started := time.Now()
		b.dir, b.err = os.MkdirTemp("", "mcpgw-board-")
		if b.err == nil {
			b.path = filepath.Join(b.dir, "board")
			b.err = b.build(ctx, b.path)
		}
		b.elapsed = time.Since(started)
	})
	return b.path, b.elapsed, b.err
}

func (b *boardBinaryBuild) cleanup() error {
	if b.dir == "" {
		return nil
	}
	return os.RemoveAll(b.dir)
}

var realBoardBinary = boardBinaryBuild{build: func(ctx context.Context, binary string) error {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	build := exec.CommandContext(ctx, "go", "build", "-p", "1", "-o", binary, "./cmd/blerg-board-server")
	build.Dir = filepath.Join(root, "board")
	// The Board is a separate module even if the caller uses a workspace.
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build real Board: %w: %s", err, output)
	}
	return nil
}}

func TestBoardBinaryBuildShared(t *testing.T) {
	var builds atomic.Int32
	b := boardBinaryBuild{build: func(_ context.Context, path string) error {
		builds.Add(1)
		return os.WriteFile(path, []byte("fixture"), 0600)
	}}
	t.Cleanup(func() {
		if err := b.cleanup(); err != nil {
			t.Error(err)
		}
	})
	var paths [8]string
	var durations [8]time.Duration
	var wg sync.WaitGroup
	for i := range paths {
		wg.Go(func() {
			var err error
			paths[i], durations[i], err = b.get(t.Context())
			if err != nil {
				t.Errorf("concurrent build: %v", err)
			}
		})
	}
	wg.Wait()
	first, elapsed, err := b.get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := range paths {
		if paths[i] != first || durations[i] != elapsed {
			t.Errorf("caller %d did not receive shared build", i)
		}
	}
	if builds.Load() != 1 {
		t.Fatalf("builds=%d; want 1", builds.Load())
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatal(err)
	}
	if err := b.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("executable survives package cleanup: %v", err)
	}
}

func TestBoardBinaryBuildFailureShared(t *testing.T) {
	cause := errors.New("build fixture failed")
	builds := 0
	b := boardBinaryBuild{build: func(context.Context, string) error {
		builds++
		return cause
	}}
	for range 2 {
		_, _, err := b.get(t.Context())
		if !errors.Is(err, cause) {
			t.Fatalf("lost build cause: %v", err)
		}
	}
	t.Cleanup(func() {
		if err := b.cleanup(); err != nil {
			t.Error(err)
		}
	})
	if builds != 1 {
		t.Fatalf("failed builds=%d; want 1", builds)
	}
	dir := b.dir
	if err := b.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed build directory survives cleanup: %v", err)
	}
}
