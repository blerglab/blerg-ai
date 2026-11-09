//go:build linux

package shellkeeper

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "stdio-protect-oracle" {
		os.Exit(stdioProtectOracle())
	}
	if len(os.Args) == 3 && os.Args[1] == "file-crash" {
		_ = writePrivateAt(os.Args[2], "epoch-copy.json", Epoch{Number: 1, ID: "fixture"}, false, func() { os.Exit(91) })
		os.Exit(92)
	}
	if len(os.Args) == 2 && os.Args[1] == "pinned-keeper" {
		fmt.Fprintln(os.Stdout, "ready")
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		build, err := BuildID()
		if err != nil {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stdout, "build", build)
		os.Exit(Stdio())
	}
	if len(os.Args) == 4 && os.Args[1] == "initialize-crash" {
		_ = initializeAt(os.Args[2], func(stage string) {
			if stage == os.Args[3] {
				os.Exit(91)
			}
		})
		os.Exit(92)
	}
	if len(os.Args) == 2 && os.Args[1] == "owned-leaf" {
		fmt.Fprintln(os.Stdout, os.Getpid(), os.Getppid())
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "owned-parent" {
		cmd := exec.Command(os.Args[0], "owned-leaf")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		done := make(chan struct{})
		if StartOwned(cmd, done) != nil {
			os.Exit(1)
		}
		_ = cmd.Wait()
		close(done)
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "shell-supervisor" {
		fmt.Fprintln(os.Stdout, os.Getppid())
		_, _ = io.Copy(os.Stdout, os.Stdin)
		os.Exit(3)
	}
	if len(os.Args) == 3 && os.Args[1] == "shell-keeper" && os.Args[2] == "--stdio" {
		os.Exit(Stdio())
	}
	os.Exit(m.Run())
}

func TestReplacedBinaryKeepsExecAndBuildIdentityPinned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keeper")
	copyFile := func(from, to string) {
		t.Helper()
		in, err := os.Open(from)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0700)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil || closeErr != nil {
			t.Fatal("copy", err, closeErr)
		}
	}
	copyFile("/proc/self/exe", path)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close(); _ = write.Close() }()
	cmd := exec.CommandContext(t.Context(), path, "pinned-keeper")
	cmd.Stdin = read
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	reader := bufio.NewReader(out)
	if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatal("pin fixture", err)
	}
	copyFile("/bin/false", path+".new")
	if err = os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	_, _ = write.Write([]byte("x"))
	_ = write.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	want, err := BuildID()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("build "+want+"\n")) || !bytes.Contains(data, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid))) {
		t.Fatal("exec/build followed replaced pathname")
	}
	var exit *exec.ExitError
	if err = cmd.Wait(); !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatal("pinned helper failed", err)
	}
}

func TestKeeperDeathKillsItsOwnedHelper(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close(); _ = write.Close() }()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "owned-parent")
	cmd.Stdin = read
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var helperPID, keeperPID int
	if _, err = fmt.Fscanln(stdout, &helperPID, &keeperPID); err != nil || keeperPID != cmd.Process.Pid {
		t.Fatal("helper did not prove its keeper parent", err)
	}
	fd, err := unix.PidfdOpen(helperPID, 0)
	if errors.Is(err, unix.ENOSYS) {
		t.Skip("kernel has no pidfd exit observation")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 5000) // exit signal with a bounded failure guard, never a progress sleep
	if err != nil || n != 1 || fds[0].Revents&unix.POLLIN == 0 {
		t.Fatal("helper survived keeper death", err)
	}
}

func TestStdioKeepsHelperAsChildAndPreservesExit(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "shell-keeper", "--stdio")
	cmd.Stdin = strings.NewReader("private fixture bytes\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("identity exit: %v", err)
	}
	want := fmt.Sprintf("%d\nprivate fixture bytes\n", cmd.Process.Pid)
	if stdout.String() != want {
		t.Fatalf("inherited private transport: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), `"cause":"identity_changed"`) || strings.Contains(stderr.String(), "private fixture bytes") {
		t.Fatalf("failure diagnostic: %q", stderr.String())
	}
}

func TestStartOwnedPreservesAttributesAndReportsStartFailure(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/nonexistent-shell-keeper-fixture")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	done := make(chan struct{})
	defer close(done)
	if err := StartOwned(cmd, done); err == nil {
		t.Fatal("missing executable accepted")
	}
	if !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatal("child ownership or preexisting attributes lost")
	}
}
