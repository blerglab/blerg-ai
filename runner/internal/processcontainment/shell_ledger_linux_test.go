//go:build linux

package processcontainment

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestShellLedgerWaitsForAllRecordedProcesses(t *testing.T) {
	ledger, child, err := NewShellLedger()
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestShellLedgerProcessFixture$")
	cmd.Env = append(os.Environ(), "BLERG_SHELL_LEDGER_FIXTURE=1", "GORACE=atexit_sleep_ms=0")
	cmd.Stdin = reader
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	fd, err := unix.PidfdOpen(cmd.Process.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err = RecordShellProcess(child, fd); err != nil {
		t.Fatal(err)
	}
	_ = child.Close()
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if err = ledger.Wait(expired); err == nil {
		t.Fatal("live recorded process accepted as empty")
	}
	_ = writer.Close()
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	guard, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if err = ledger.Wait(guard); err != nil {
		t.Fatal("dead recorded process never settled", err)
	}
}
func TestShellLedgerProcessFixture(t *testing.T) {
	if os.Getenv("BLERG_SHELL_LEDGER_FIXTURE") == "1" {
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
	}
}
func TestShellLedgerMissingOrBrokenEvidenceRefuses(t *testing.T) {
	for _, broken := range []bool{false, true} {
		ledger, child, err := NewShellLedger()
		if err != nil {
			t.Fatal(err)
		}
		if broken {
			ledger.ledger.mu.Lock()
			ledger.ledger.breakLocked("fixture truncated ledger")
			ledger.ledger.records = 1
			ledger.ledger.mu.Unlock()
		}
		_ = child.Close()
		if err = ledger.Wait(t.Context()); err == nil {
			t.Fatal("missing/broken ledger manufactured proof", broken)
		}
		ledger.Close()
	}
}
