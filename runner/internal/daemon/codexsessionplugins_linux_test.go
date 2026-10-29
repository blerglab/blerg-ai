package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCodexSessionPluginsRestartTerminatesNativeHelpers(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	helpers := filepath.Join(t.TempDir(), "helpers")
	t.Setenv("FAKE_CODEX_HELPERS", helpers)
	// Only PIDs recorded by this fixture belong to this test.
	defer func() {
		raw, _ := os.ReadFile(helpers)
		for _, line := range strings.Fields(string(raw)) {
			pid, _ := strconv.Atoi(line)
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}()
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	stop := startTestCodexDriver(context.Background(), t, d)
	defer stop()
	d.Enqueue("first", "chat")
	waitUntil(t, "first turn", func() bool { return countKind(em, "turn_done") == 1 })
	raw, err := os.ReadFile(helpers)
	if err != nil {
		t.Fatal(err)
	}
	oldPID, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	applied := make(chan struct{})
	d.queuePluginUpdate(func(context.Context) { close(applied) })
	select {
	case <-applied:
	case <-time.After(10 * time.Second):
		t.Fatal("planned restart did not complete")
	}
	waitUntil(t, "replacement thread ready", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.app != nil && d.app.mcpThreadReady
	})
	waitUntil(t, "native helper terminated by planned restart", func() bool {
		// Wait for signal delivery; an orphan zombie cannot do work even if
		// init has not reaped it yet.
		status, err := os.ReadFile("/proc/" + strconv.Itoa(oldPID) + "/stat")
		return os.IsNotExist(err) || strings.Contains(string(status), ") Z ")
	})
	d.Enqueue("second", "chat")
	waitUntil(t, "second turn", func() bool { return countKind(em, "turn_done") == 2 })
}
