package shellruntime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
)

// A real keeper process with a controlled ordinary-operation lock. Heartbeats
// remain runnable; SIGSTOP would not prove separation from slow operations.
func TestRound10SlowKeeperProcess(t *testing.T) {
	dir := os.Getenv("BLERG_ROUND10_SLOW_KEEPER")
	if dir == "" {
		return
	}
	k := NewKeeperRuntime()
	server, err := shellkeeper.ListenService(t.Context(), dir, k.Inventory, k.Serve)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close(); _ = k.Shutdown(context.Background()) }()
	fmt.Println("ready")
	locked := false
	defer func() {
		if locked {
			k.operation.Unlock()
		}
	}()
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		switch scanner.Text() {
		case "lock":
			k.operation.Lock()
			locked = true
			fmt.Println("locked")
		case "unlock":
			k.operation.Unlock()
			locked = false
			fmt.Println("unlocked")
		case "stop":
			return
		default:
			t.Fatal("invalid fixture control")
		}
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRound10RealSlowKeeperOperationKeepsBridge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keeper")
	if err := shellkeeper.Initialize(dir); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRound10SlowKeeperProcess$", "-test.timeout=30s")
	child.Env = append(os.Environ(), "BLERG_ROUND10_SLOW_KEEPER="+dir)
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	joined := make(chan error, 1)
	go func() { joined <- child.Wait() }()
	defer func() {
		_ = child.Process.Kill()
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Error("keeper process not joined")
		}
	}()
	lines := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-lines:
			if got != want {
				t.Fatalf("fixture control %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("fixture did not acknowledge control")
		}
	}
	expect("ready")
	authority, err := shellkeeper.AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authority.Close() }()
	b := NewReconnectingHostBridge(t.Context(), func(ctx context.Context) (HostBridge, error) {
		view, err := shellkeeper.InspectSnapshot(ctx, dir, authority.Epoch)
		if err != nil {
			return nil, err
		}
		raw, err := ConnectKeeper(ctx, dir, authority.Epoch, view.Generation)
		if err != nil {
			return nil, err
		}
		return raw, nil
	})
	defer func() { _ = b.Close() }()
	b.SetReceiver(func(context.Context, RPC) error { return nil })
	b.SetRecovery(func(ctx context.Context) error { return b.Invoke(ctx, "rebind", nil, nil) })
	guard, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err = b.WaitReady(guard); err != nil {
		t.Fatal(err)
	}
	original := b.snapshot()
	if _, err = io.WriteString(input, "lock\n"); err != nil {
		t.Fatal(err)
	}
	expect("locked")
	call, stop := context.WithTimeout(t.Context(), 200*time.Millisecond)
	err = b.Invoke(call, "call", RPC{Version: 1, SessionID: "missing", Op: "describe"}, nil)
	stop()
	if !errors.Is(err, ErrKeeperUnavailable) {
		t.Fatal("slow real operation cause", err)
	}
	if !b.available() || b.snapshot() != original {
		t.Fatal("slow operation disconnected or replaced authenticated stream")
	}
	if err = b.heartbeat(original); err != nil {
		t.Fatal("slow operation blocked heartbeat", err)
	}
	if _, err = io.WriteString(input, "unlock\n"); err != nil {
		t.Fatal(err)
	}
	expect("unlocked")
	if err = b.Invoke(guard, "hello", nil, nil); err != nil {
		t.Fatal("same bridge unusable after slow operation", err)
	}
	if b.snapshot() != original {
		t.Fatal("operation timeout triggered a hidden reconnect")
	}
}
