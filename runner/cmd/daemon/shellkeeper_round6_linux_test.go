//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

func TestKeeperConnectFailureAfterVerifiedSnapshotReturnsNilInterface(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keeper")
	if err := shellkeeper.Initialize(dir); err != nil {
		t.Fatal(err)
	}
	server, err := shellkeeper.Listen(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	authority, err := shellkeeper.AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authority.Close() }()
	original := keeperConnect
	t.Cleanup(func() { keeperConnect = original })
	want := errors.New("fixture connect failure after inspection")
	called := false
	keeperConnect = func(_ context.Context, _ string, _ shellkeeper.Epoch, generation string) (*shellruntime.KeeperBridge, error) {
		called = true
		if len(generation) != 64 {
			t.Fatal("snapshot was not verified")
		}
		return nil, want
	}
	bridge, err := dialKeeper(t.Context(), dir, authority.Epoch)
	if !called || !errors.Is(err, want) {
		t.Fatal("connect failure path not reached", err)
	}
	if bridge != nil {
		t.Fatal("failed connection returned a typed-nil HostBridge; reconnect cleanup would panic")
	}
}

func TestKeeperUnsupportedProcessFixture(t *testing.T) {
	if os.Getenv("BLERG_KEEPER_UNSUPPORTED_FIXTURE") != "1" {
		return
	}
	keeperListen = func(context.Context, string) (*shellkeeper.Server, error) { return nil, shellkeeper.ErrUnsupported }
	os.Exit(serveKeeper(t.TempDir()))
}

func TestUnsupportedKeeperStopsRestartLoop(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestKeeperUnsupportedProcessFixture$")
	cmd.Env = append(os.Environ(), "BLERG_KEEPER_UNSUPPORTED_FIXTURE=1", "GORACE=atexit_sleep_ms=0")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 78 {
		t.Fatalf("unsupported keeper did not stop with configuration status: %v %s", err, out)
	}
	unit, err := os.ReadFile("../../../install/desktop/daemon/blerg-shell-keeper.service.tmpl")
	if err != nil || !strings.Contains(string(unit), "RestartPreventExitStatus=78") {
		t.Fatal("unsupported kernel would restart-loop", err)
	}
}
