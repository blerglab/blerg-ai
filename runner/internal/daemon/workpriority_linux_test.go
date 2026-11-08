//go:build linux

package daemon

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Exercise the real desktop driver boundary. Grandchildren stand in for browsers.
func TestDesktopWorkerPriorityInheritance(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	t.Setenv("PATH", fakeClaude(t)+string(filepath.ListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proj"), 0700); err != nil {
		t.Fatal(err)
	}
	before, err := exec.Command("ps", "-o", "ni=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatal(err)
	}
	host := NewAgentHost(&agentTestSender{}, AgentHostConfig{ReposRoot: root, HomeDir: t.TempDir(), PreferClaudeCLI: true})
	host.Spawn(protocol.SpawnSession{SessionID: "priority", Kind: "agent", Repo: "proj"})
	defer host.Kill("priority")
	sess := host.get("priority")
	if sess == nil {
		t.Fatal("desktop session missing")
	}
	driver := sess.loop.(*claudeCodeDriver)
	cmd := exec.Command("sh", "-c", `ps -o ni= -p $$; sh -c 'ps -o ni= -p $$; ionice -p $$'`)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := startEngineCommand(cmd, driver.startCommand); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(output.String())
	base, _ := strconv.Atoi(strings.TrimSpace(string(before)))
	if len(fields) < 3 {
		t.Fatalf("missing priority observations: %q", output.String())
	}
	for _, f := range fields[:2] {
		n, e := strconv.Atoi(f)
		if e != nil || n <= base {
			t.Errorf("worker and child must yield to daemon (nice %d), got %q", base, output.String())
			break
		}
	}
	if !strings.Contains(output.String(), "idle") {
		t.Errorf("worker child lacks idle I/O: %q", output.String())
	}
	after, err := exec.Command("ps", "-o", "ni=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("desktop request process priority changed")
	}
}

// The opt-in proof uses the shipped ptrace boundary, including the scheduler
// wrapper, so descendants remain owned and reapable after changing cgroups.
func TestDesktopWorkerPriorityContained(t *testing.T) {
	helper := os.Getenv("BLERG_PRIORITY_HELPER")
	if helper == "" {
		t.Skip("set BLERG_PRIORITY_HELPER to a built desktop daemon")
	}
	group := processcontainment.NewGroup(helper, uint32(os.Getuid()), uint32(os.Getgid()))
	p := NewWorkerPriority("")
	cmd := exec.CommandContext(t.Context(), "sh", "-c", `sh -c 'ps -o ni= -p $$; ionice -p $$'`)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	wrapWorkerCommand(cmd, p.prefix)
	if err := group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err, output.String())
	}
	if !strings.Contains(output.String(), "5") || !strings.Contains(output.String(), "idle") {
		t.Fatal(output.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := group.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
