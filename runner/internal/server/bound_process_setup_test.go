package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
)

// Bound cases keep separate processes and databases. Only migration templates
// and the immutable Board executable belong to the package process.
func boundProcessDatabase(t *testing.T, env []string) []string {
	t.Helper()
	for _, value := range env {
		if strings.HasPrefix(value, "BLERG_BOUND_DATABASE=") {
			return env
		}
	}
	pool := runnerContractPool(t)
	return append(env, "BLERG_BOUND_DATABASE="+pool.Config().ConnString())
}

func boundProcessWorkers(value string) int {
	if value == "" {
		return 2
	}
	workers, err := strconv.Atoi(value)
	if err != nil || workers < 1 {
		return 1
	}
	return min(2, workers)
}

var boundProcessSlots = make(chan struct{}, boundProcessWorkers(os.Getenv("BLERG_TOOL_WORKERS")))

func parallelBoundProcess(t *testing.T) {
	t.Helper()
	t.Parallel()
	select {
	case boundProcessSlots <- struct{}{}:
		// Registered first so the slot covers every process and database cleanup.
		t.Cleanup(func() { <-boundProcessSlots })
	case <-t.Context().Done():
		t.Fatalf("wait for bound process slot: %v", t.Context().Err())
	}
}

type cutBoardBinaryBuild struct {
	once      sync.Once
	build     func(context.Context, string) error
	dir, path string
	elapsed   time.Duration
	err       error
}

func (b *cutBoardBinaryBuild) get(ctx context.Context) (string, time.Duration, error) {
	b.once.Do(func() {
		started := time.Now()
		b.dir, b.err = os.MkdirTemp("", "server-cut-board-")
		if b.err == nil {
			b.path = filepath.Join(b.dir, "board-cut.test")
			b.err = b.build(ctx, b.path)
		}
		b.elapsed = time.Since(started)
	})
	return b.path, b.elapsed, b.err
}

func (b *cutBoardBinaryBuild) cleanup() error {
	if b.dir == "" {
		return nil
	}
	return os.RemoveAll(b.dir)
}

var sharedCutBoardBinary = cutBoardBinaryBuild{build: func(ctx context.Context, binary string) error {
	goTool := "go"
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, "go", "bin", "go")
		if _, err := os.Stat(candidate); err == nil {
			goTool = candidate
		}
	}
	command := exec.CommandContext(ctx, goTool, "test", "-p", strconv.Itoa(boundProcessWorkers(os.Getenv("BLERG_TOOL_WORKERS"))), "-c", "-o", binary, "./internal/api")
	command.Dir = "../../../board"
	command.Env = append(os.Environ(), "GOWORK=off")
	if out, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("build real board fixture: %w\n%s", err, out)
	}
	return nil
}}

func buildCutBoardBinary(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("BLERG_CUT_BOARD_BINARY"); path != "" {
		return path
	}
	phase := testsupport.Phase(t, "board_binary")
	defer phase.End()
	binary, _, err := sharedCutBoardBinary.get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return binary
}
