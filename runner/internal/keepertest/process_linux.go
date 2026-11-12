// Package keepertest owns disposable real-process keeper fixtures. It never
// reads, installs, or controls an installed daemon or keeper service.
package keepertest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func Build(t *testing.T) string {
	t.Helper()
	_, source, _, _ := runtime.Caller(0)
	binary := filepath.Join(t.TempDir(), "daemon")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/daemon") //nolint:gosec // Test-owned output and repository-relative package, never request input.
	cmd.Dir = filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, out)
	}
	return binary
}

// BuildProtocol builds a genuinely incompatible peer without mutating the checkout.
func BuildProtocol(t *testing.T, version int) string {
	return buildProtocol(t, version, false)
}

// BuildProtocolTest builds a maintenance peer with a different custody protocol.
func BuildProtocolTest(t *testing.T, version int) string {
	return buildProtocol(t, version, true)
}

func buildProtocol(t *testing.T, version int, testBinary bool) string {
	t.Helper()
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	protocol := filepath.Join(root, "internal/shellkeeper/protocol_linux.go")
	b, err := os.ReadFile(protocol)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(b), "const Version = ")
	if start < 0 {
		t.Fatal("protocol version missing")
	}
	end := start + strings.Index(string(b[start:]), "\n")
	modified := string(b[:start]) + fmt.Sprintf("const Version = %d", version) + string(b[end:])
	dir := t.TempDir()
	replacement := filepath.Join(dir, "protocol.go")
	if err = os.WriteFile(replacement, []byte(modified), 0600); err != nil { //nolint:gosec // Replacement is a fixture-only file beneath t.TempDir, never input from a request.
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]any{"Replace": map[string]string{protocol: replacement}})
	if err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	if err = os.WriteFile(overlay, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "daemon")
	args := []string{"build", "-overlay=" + overlay, "-o", binary, "./cmd/daemon"}
	if testBinary {
		args = []string{"test", "-c", "-overlay=" + overlay, "-o", binary, "./internal/shellkeeper"}
	}
	cmd := exec.CommandContext(t.Context(), "go", args...) //nolint:gosec // Test-owned overlay and output, fixed repository packages.
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("protocol fixture build: %v %s", err, out)
	}
	return binary
}
func DifferentImage(t *testing.T, binary string) string {
	t.Helper()
	b, err := os.ReadFile(binary) //nolint:gosec // Path returned by this fixture's Build.
	if err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(t.TempDir(), "daemon-next")
	if err = os.WriteFile(image, append(b, []byte("round8-compatible-image")...), 0755); err != nil { //nolint:gosec // Executable fixture in a test-owned temporary directory.
		t.Fatal(err)
	}
	return image
}

type Process struct {
	Cmd     *exec.Cmd
	stopped bool
	done    chan error
}

func Start(t *testing.T, binary, state string) *Process {
	t.Helper()
	ctx := t.Context()
	// Short socket path: Go's normal nested test directories may exceed sun_path.
	notifyRoot, err := os.MkdirTemp("", "keeper-notify-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(notifyRoot) })
	path := filepath.Join(notifyRoot, "ready")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	log, err := os.CreateTemp(t.TempDir(), "keeper-log-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	cmd := exec.CommandContext(ctx, binary, "shell-keeper", "serve")
	cmd.Env = append(os.Environ(), "BLERG_RUNNER_DAEMON_STATE_DIR="+state, "NOTIFY_SOCKET="+path)
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &Process{Cmd: cmd, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() { p.Close(t) })
	if err = listener.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [64]byte
	n, _, err := listener.ReadFromUnix(b[:])
	if err != nil || string(b[:n]) != "READY=1" {
		t.Fatalf("real keeper did not become ready: %v", err)
	}
	return p
}
func (p *Process) Signal(t *testing.T, signal syscall.Signal) {
	t.Helper()
	if err := p.Cmd.Process.Signal(signal); err != nil {
		t.Fatal(err)
	}
}
func (p *Process) Pause(t *testing.T) {
	t.Helper()
	p.Signal(t, syscall.SIGSTOP)
	var info unix.Siginfo
	if err := unix.Waitid(unix.P_PID, p.Cmd.Process.Pid, &info, unix.WSTOPPED|unix.WNOWAIT, nil); err != nil {
		t.Fatal("observe keeper SIGSTOP", err)
	}
}
func (p *Process) Close(t *testing.T) {
	t.Helper()
	if p.stopped {
		return
	}
	p.stopped = true
	_ = p.Cmd.Process.Signal(syscall.SIGCONT)
	_ = p.Cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		_ = p.Cmd.Process.Kill()
		<-p.done
	}
}
func (p *Process) Wait(t *testing.T) {
	t.Helper()
	select {
	case err := <-p.done:
		p.stopped = true
		if err != nil {
			t.Fatal("keeper replacement exit", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("empty old keeper was not replaced")
	}
}
func Run(t *testing.T, binary, state string, args ...string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...) //nolint:gosec // Runs only test-built binaries and literal test CLI requests.
	cmd.Env = append(os.Environ(), "BLERG_RUNNER_DAEMON_STATE_DIR="+state, "NOTIFY_SOCKET=")
	return cmd.Run()
}
