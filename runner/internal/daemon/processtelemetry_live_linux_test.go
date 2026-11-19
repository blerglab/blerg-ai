//go:build linux

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
	"github.com/blerglab/blerg-ai/runner/internal/processtelemetry"
)

// TestLiveLongGoTestEvidence runs a real go test under a real containment helper
// with the session's own monitor. It needs a built daemon (the helper) and its own
// ptrace tracer, so it runs only when asked:
// BLERG_LIVE_WRITER_BIN=<daemon> BLERG_LIVE_SECONDS=60 go test -run TestLiveLongGoTestEvidence
func TestLiveLongGoTestEvidence(t *testing.T) {
	helper := os.Getenv("BLERG_LIVE_WRITER_BIN")
	seconds, _ := strconv.Atoi(os.Getenv("BLERG_LIVE_SECONDS"))
	if helper == "" || seconds < 1 {
		t.Skip("set BLERG_LIVE_WRITER_BIN and BLERG_LIVE_SECONDS")
	}
	module := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":        "module livefixture\n\ngo 1.26\n",
		"sleep_test.go": "package livefixture\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestSleep(t *testing.T) { time.Sleep(" + strconv.Itoa(seconds) + " * time.Second) }\n",
	} {
		if err := os.WriteFile(filepath.Join(module, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	group := processcontainment.NewGroup(helper, uint32(os.Getuid()), uint32(os.Getgid()))
	var mu sync.Mutex
	var rows, spans int
	covered := map[string]time.Duration{}
	monitor := newToolProcessMonitor(group.ProcessActivityForRoots, func(ev agent.Event) {
		batch, ok := ev.Payload.(processtelemetry.Batch)
		if !ok {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		rows++
		spans += len(batch.Spans)
		for _, s := range batch.Spans {
			covered[s.Program] += s.End.Sub(s.Start)
		}
	})
	cmd := exec.Command(goTool, "test", "-count=1", "-timeout=10m", "./...")
	cmd.Dir = module
	cmd.Env = os.Environ()
	if err = group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	monitor.started(cmd)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { monitor.run(ctx); close(stopped) }()
	started := time.Now()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	if err = <-waited; err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	stop := time.Now()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err = group.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	t.Logf("stop receipt %s", time.Since(stop))
	cancel()
	<-stopped
	mu.Lock()
	defer mu.Unlock()
	t.Logf("elapsed %s: %d process_activity rows (%.1f/min), %d spans, covered %v", elapsed.Round(time.Millisecond), rows, float64(rows)/elapsed.Minutes(), spans, covered)
	if covered["go-test"] < time.Duration(seconds-2)*time.Second {
		t.Fatalf("go-test evidence %s for a %ds test", covered["go-test"], seconds)
	}
}
