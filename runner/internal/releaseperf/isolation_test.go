package releaseperf

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A free port and a fresh (nonexistent) data directory are the ordinary case:
// nothing to refuse.
func TestCheckIsolationAllowsAFreeHost(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	dir := filepath.Join(t.TempDir(), "release-perf-data")
	if err := CheckIsolation(IsolationCheck{Ports: []int{port}, DataDirs: []string{dir}}); err != nil {
		t.Fatalf("expected a free port and fresh data dir to pass isolation, got: %v", err)
	}
}

// A port already bound by something else — standing in for a live daemon's
// listener — must refuse the whole run, naming the colliding port.
func TestCheckIsolationRefusesAPortALiveDaemonHolds(t *testing.T) {
	liveDaemon, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer liveDaemon.Close()
	port := liveDaemon.Addr().(*net.TCPAddr).Port
	dir := filepath.Join(t.TempDir(), "release-perf-data")
	err = CheckIsolation(IsolationCheck{Ports: []int{port}, DataDirs: []string{dir}})
	if err == nil {
		t.Fatal("expected a refusal: the port is already held by a live listener")
	}
	if got := err.Error(); !strings.Contains(got, strconv.Itoa(port)) {
		t.Fatalf("refusal must name the colliding port %d, got: %s", port, got)
	}
}

// A data directory that already holds state — standing in for a live
// daemon's own data directory — must refuse the whole run, naming the
// colliding directory. It must never be silently reused or wiped.
func TestCheckIsolationRefusesADataDirALiveDaemonOwns(t *testing.T) {
	liveDaemonDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(liveDaemonDir, "daemon.state"), []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	err = CheckIsolation(IsolationCheck{Ports: []int{port}, DataDirs: []string{liveDaemonDir}})
	if err == nil {
		t.Fatal("expected a refusal: the data directory already holds state")
	}
	if got := err.Error(); !strings.Contains(got, liveDaemonDir) {
		t.Fatalf("refusal must name the colliding directory %s, got: %s", liveDaemonDir, got)
	}
}
