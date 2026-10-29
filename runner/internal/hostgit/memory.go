package hostgit

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// ErrMemoryLimit distinguishes an observed RSS budget overrun from Git failure or cancellation.
var ErrMemoryLimit = errors.New("git process group exceeded its resident memory budget")

// ErrMemoryMonitor means the resident memory budget could not be observed. Fail closed.
var ErrMemoryMonitor = errors.New("git resident memory supervisor unavailable")

// memoryRefusal preserves a machine-readable reason and a refusal exit code even though the
// supervised process itself exits on SIGKILL. 125 is a setup/observation refusal; 126 is an overrun.
type memoryRefusal struct{ reason error }

func (e *memoryRefusal) Error() string { return e.reason.Error() }
func (e *memoryRefusal) Unwrap() error { return e.reason }
func (e *memoryRefusal) ExitCode() int {
	if errors.Is(e.reason, ErrMemoryLimit) {
		return 126
	}
	return 125
}

const memoryPollInterval = 20 * time.Millisecond

type memoryWatch struct {
	refused <-chan error
	stop    func()
}

type groupRSS func(context.Context, int) (uint64, error)

// watchGroupMemory is shared with Linux tests; only Darwin installs it in production. RSS is
// aggregated across the entire group, including Git's serving/pack helpers. This is a sampled
// resident-memory bound, not an address-space reservation: allocations can overshoot between polls.
func watchGroupMemory(cmd *exec.Cmd, limitKB int, sample groupRSS) *memoryWatch {
	return watchGroupMemoryObserved(cmd, limitKB, sample, nil)
}

func watchGroupMemoryObserved(cmd *exec.Cmd, limitKB int, sample groupRSS, diagnostic func(string, time.Duration, error)) *memoryWatch {
	if limitKB <= 0 {
		limitKB = MemoryLimitKB
	}
	limit := uint64(limitKB)
	ctx, cancel := context.WithCancel(context.Background())
	refused := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		o := Options{Diagnostic: diagnostic}
		started := time.Now()
		defer func() { recordDiagnostic(o, "RSS supervisor stopped", started, ctx.Err()) }()
		ticker := time.NewTicker(memoryPollInterval)
		defer ticker.Stop()
		first := true
		for {
			sampling := time.Now()
			if first {
				recordDiagnostic(o, "RSS first sample begin", sampling, nil)
			}
			rss, err := sample(ctx, cmd.Process.Pid)
			if first || err != nil {
				recordDiagnostic(o, "RSS sample", sampling, err)
			}
			first = false
			if ctx.Err() != nil {
				return
			}
			switch {
			case err != nil:
				refused <- &memoryRefusal{fmt.Errorf("%w: %w", ErrMemoryMonitor, err)}
				return
			case rss > limit:
				refused <- &memoryRefusal{ErrMemoryLimit}
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return &memoryWatch{refused: refused, stop: func() { cancel(); <-done }}
}

func memoryLimitKB(o Options) int {
	if o.LimitKB > 0 {
		return o.LimitKB
	}
	return MemoryLimitKB
}
