package shellkeeper_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/keepertest"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
)

// This peer has a genuinely incompatible custody protocol. Synthetic custody
// isolates maintenance policy from the unrelated kernel containment mechanism.
func TestRound9IncompatibleMaintenancePeer(t *testing.T) {
	dir := os.Getenv("BLERG_TEST_ROUND9_PEER")
	if dir == "" {
		return
	}
	server, err := shellkeeper.ListenService(t.Context(), dir, func() []shellkeeper.Candidate {
		if _, err := os.Stat(filepath.Join(dir, "live")); err == nil {
			return []shellkeeper.Candidate{{Allocation: "workspace", Custody: "custody", Shell: "shell"}}
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	fmt.Println("READY")
	select {
	case <-server.Done():
	case <-t.Context().Done():
		t.Fatal("peer not drained")
	}
	if !server.Drained() {
		t.Fatal("unsigned shutdown")
	}
}

func TestRound9IncompatibleLiveKeeperRemedyThenEmptyReplacement(t *testing.T) {
	binary := keepertest.BuildProtocolTest(t, shellkeeper.Version+1)
	dir := filepath.Join(t.TempDir(), "keeper")
	if err := shellkeeper.Initialize(dir); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "live")
	if err := os.WriteFile(marker, []byte("live"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestRound9IncompatibleMaintenancePeer$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "BLERG_TEST_ROUND9_PEER="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(stdout); ready <- scanner.Scan() && scanner.Text() == "READY" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("peer failed readiness")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("peer readiness timeout")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err = shellkeeper.CheckUpdate(ctx, dir); !errors.Is(err, shellkeeper.ErrRestartRequired) {
		t.Fatal("missing restart remedy", err)
	}
	select {
	case err := <-done:
		t.Fatal("live incompatible keeper was stopped", err)
	default:
	}
	if err = os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err = shellkeeper.CheckUpdate(ctx, dir); err != nil {
		t.Fatal("empty incompatible keeper not replaced", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("unclean drain", err)
		}
	case <-ctx.Done():
		t.Fatal("keeper retained empty custody")
	}
}
