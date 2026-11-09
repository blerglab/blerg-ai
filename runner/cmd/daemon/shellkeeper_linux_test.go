//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
	"golang.org/x/sys/unix"
)

func TestKeeperStartupDoesNotFenceAgents(t *testing.T) {
	for _, name := range []string{"unprovisioned", "partial"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("BLERG_RUNNER_DAEMON_STATE_DIR", root)
			if name == "partial" {
				if err := os.Mkdir(filepath.Join(root, "shell-keeper"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			done, err := keeperStartup()
			if err != nil || done == nil {
				t.Fatal("keeper failure fenced daemon", err)
			}
			done()
		})
	}
}

func TestKeeperCLIProcess(t *testing.T) {
	if os.Getenv("BLERG_KEEPER_CLI_FIXTURE") != "1" {
		return
	}
	for index, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[index+1:]...)
			if os.Getenv("BLERG_KEEPER_PROTECT_ORACLE") == "1" {
				_ = unix.Prctl(unix.PR_SET_DUMPABLE, 1, 0, 0, 0)
				keeperListen = func(ctx context.Context, dir string) (*shellkeeper.Server, error) {
					value, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
					if err != nil || value != 0 {
						os.Exit(90)
					}
					return shellkeeper.Listen(ctx, dir)
				}
			}
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing fixture command")
}

func TestKeeperCLIReinstallPreservesIdentityAndRunningBroker(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "shell-keeper")
	command := func(action string) *exec.Cmd {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestKeeperCLIProcess$", "--", "shell-keeper", action)
		cmd.Env = append(os.Environ(), "BLERG_KEEPER_CLI_FIXTURE=1", "BLERG_KEEPER_PROTECT_ORACLE=1", "BLERG_RUNNER_DAEMON_STATE_DIR="+root, "NOTIFY_SOCKET=")
		return cmd
	}
	if out, err := command("unsupported-fixture").CombinedOutput(); err == nil || !bytes.Contains(out, []byte("unsupported_command")) {
		t.Fatal("unknown keeper command did not fail with a logged cause")
	}
	if out, err := command("init").CombinedOutput(); err != nil {
		t.Fatalf("bundled init: %v %s", err, out)
	}
	before, err := os.ReadFile(filepath.Join(dir, "daemon-key.json"))
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if _, err = unix.InotifyAddWatch(fd, dir, unix.IN_ATTRIB); err != nil {
		t.Fatal(err)
	}
	serve := command("serve")
	if err = serve.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serve.Process.Kill(); _ = serve.Wait() }()
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if n, err := unix.Poll(fds, 5000); err != nil || n != 1 {
		t.Fatal("no socket readiness signal", err)
	}
	events := make([]byte, 4096)
	n, err := unix.Read(fd, events)
	if err != nil || !bytes.Contains(events[:n], []byte("keeper.sock")) {
		t.Fatal("missing keeper socket chmod event", err)
	}
	for restart := range 2 {
		a, err := shellkeeper.AcquireDaemon(dir)
		if err != nil {
			t.Fatal(err)
		}
		err = shellkeeper.RequireEmpty(t.Context(), dir, a.Epoch)
		_ = a.Close()
		if err != nil {
			t.Fatal("daemon restart refused", restart, err)
		}
		if out, err := command("init").CombinedOutput(); err != nil {
			t.Fatalf("bundled reinstall init: %v %s", err, out)
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "daemon-key.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("reinstall changed the pinned identity")
	}
	if err = serve.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err = serve.Wait(); err != nil {
		t.Fatal("keeper did not stop cleanly", err)
	}
	if err = os.Remove(filepath.Join(dir, "epoch.json")); err != nil {
		t.Fatal(err)
	}
	if out, err := command("init").CombinedOutput(); err == nil || !bytes.Contains(out, []byte("epoch_read")) {
		t.Fatalf("partial reinstall did not refuse with a logged cause: %v", err)
	}
}

func TestKeeperUnitAndInstallerPreserveIndependentCustody(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "install", "desktop", "daemon")
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	keeper := read("blerg-shell-keeper.service.tmpl")
	for _, want := range []string{"Type=notify", "NotifyAccess=main", "ExecStart=__BIN_PATH__ shell-keeper serve", "KillMode=mixed", "Restart=always", "TasksMax=8192", "MemoryMax=8G", "UMask=0077", "LimitCORE=0", "OOMPolicy=continue", "MemoryOOMGroup=no"} {
		if !strings.Contains(keeper, want+"\n") {
			t.Fatalf("missing keeper service policy: %s", want)
		}
	}
	for _, line := range strings.Split(keeper, "\n") {
		if strings.HasPrefix(line, "PartOf=") || strings.HasPrefix(line, "BindsTo=") || strings.HasPrefix(line, "ProtectSystem=") {
			t.Fatalf("unsafe inherited/coupled policy: %s", line)
		}
	}
	if !strings.Contains(read("blerg-runner-daemon.service.tmpl"), "KillMode=mixed\n") {
		t.Fatal("daemon descendant cleanup weakened")
	}
	installer := read("install.sh")
	if !strings.Contains(installer, `"$BIN_PATH" shell-keeper init`) || !strings.Contains(installer, "enable --now --no-block blerg-shell-keeper.service") || strings.Contains(installer, "restart blerg-shell-keeper.service") {
		t.Fatal("installer must preserve identity and a live keeper")
	}
}

func TestDirectLaunchRecipesIncludeKeeper(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, path := range []string{"install/desktop/blerg-up.sh", "install/desktop/DAEMON.md", "install/k8s/README.md"} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte("shell-keeper init")) || !bytes.Contains(data, []byte("shell-keeper serve")) {
			t.Fatal("direct launch omits keeper", path)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "runner/Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mv -f", "shell-keeper init", "blerg-shell-keeper.service.tmpl", "enable --now --no-block blerg-shell-keeper.service", "KillMode=mixed"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatal("development install/restart omits keeper handling", want)
		}
	}
}

func TestInstallerNeverWaitsForKeeperUnit(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, path := range []string{"install/desktop/daemon/install.sh", "runner/Makefile"} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "enable --now --no-block blerg-shell-keeper.service") {
			t.Fatal("daemon installation waits for keeper readiness", path)
		}
		for _, want := range []string{"Daemon install failed (exit", "update handoff canceled", "preceding build, file operation,"} {
			if !strings.Contains(string(data), want) {
				t.Fatal("install failure misreports keeper cause", path, want)
			}
		}
	}
	for _, path := range []string{"install/desktop/daemon/blerg-runner-daemon.service.tmpl", "runner/Makefile"} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(data), `\n`, "\n"), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "After=") && strings.Contains(line, "blerg-shell-keeper.service") {
				t.Fatal("daemon start ordered behind keeper readiness", path)
			}
		}
	}
}
