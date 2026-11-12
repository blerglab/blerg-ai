//go:build linux

package main

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRealKeeperInstallerRetryAndBuildReplacement(t *testing.T) {
	scratch, err := os.MkdirTemp("", "keeper-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	old := filepath.Join(scratch, "old")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", old, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("real daemon build: %v %s", err, out)
	}
	// ELF permits a trailing marker. Two real executable images exercise build
	// pinning without repeating an identical expensive daemon compilation.
	image, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(scratch, "next")
	if err = os.WriteFile(next, append(image, []byte("keeper-replacement-fixture")...), 0700); err != nil {
		t.Fatal(err)
	}
	installer := exec.CommandContext(t.Context(), "bash", "../../../install/desktop/daemon_install_test.sh")
	installer.Env = append(os.Environ(), "BLERG_REAL_KEEPER_INSTALL_CASES_ONLY=1", "BLERG_TEST_REAL_KEEPER_BIN="+next)
	if out, err := installer.CombinedOutput(); err != nil {
		t.Fatalf("real CLI installer retries: %v %s", err, out)
	}
	state := filepath.Join(scratch, "state")
	command := func(binary, action string) *exec.Cmd {
		cmd := exec.CommandContext(t.Context(), binary, "shell-keeper", action)
		cmd.Env = append(os.Environ(), "BLERG_RUNNER_DAEMON_STATE_DIR="+state, "NOTIFY_SOCKET=")
		return cmd
	}
	run := func(binary, action string) {
		t.Helper()
		if out, err := command(binary, action).CombinedOutput(); err != nil {
			t.Fatalf("real %s: %v %s", action, err, out)
		}
	}
	run(old, "init")
	run(next, "check-update") // before first enable: real absence proof
	start := func(binary string) *exec.Cmd {
		t.Helper()
		notify := filepath.Join(scratch, "notify")
		listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: notify, Net: "unixgram"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close(); _ = os.Remove(notify) }()
		cmd := command(binary, "serve")
		cmd.Env = append(cmd.Env, "NOTIFY_SOCKET="+notify)
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		_ = listener.SetReadDeadline(time.Now().Add(5 * time.Second))
		data := make([]byte, 128)
		n, _, err := listener.ReadFromUnix(data)
		if err != nil || !bytes.Contains(data[:n], []byte("READY=1")) {
			t.Fatal("real keeper readiness", err)
		}
		return cmd
	}
	keeper := start(old)
	run(next, "check-update") // signed drain, then wait for lock release
	if err = keeper.Wait(); err != nil {
		t.Fatal("drain was not a clean systemd exit", err)
	}
	keeper = start(next)
	run(next, "check-update") // matching build must remain alive
	if err = keeper.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("same-build update killed keeper", err)
	}
	if err = keeper.Wait(); err != nil {
		t.Fatal("replacement keeper shutdown", err)
	}
	keeper = start(next)
	run(next, "drain")
	if err = keeper.Wait(); err != nil {
		t.Fatal("same-build operator drain", err)
	}
	run(next, "check-update") // uninstall-like stop; retry remains possible
}
