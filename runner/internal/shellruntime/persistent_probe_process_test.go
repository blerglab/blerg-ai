//go:build linux

package shellruntime

import (
	"os"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

func TestPersistentProbeProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "p5-persistent-probe" || os.Args[len(os.Args)-1] != "--probe-persistence" {
		return
	}
	clock := func() (ownershell.LeaseClock, error) {
		return func() (time.Time, time.Duration, error) { return time.Now(), time.Second, nil }, nil
	}
	if probePersistent(os.Stdin, os.Stdout, ResolvePersistentIdentity, clock) != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestPersistentProbeCommandChecksInstalledHelper(t *testing.T) {
	base, b, _, _ := fixture(t)
	command := base.command
	command.Root = b.Root
	command.PublicRoot = command.Identity.Cwd
	command.Args = []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestPersistentProbeProcess$", "--", "p5-persistent-probe"}
	identity, err := ProbePersistentCommand(t.Context(), command)
	expected, e := ResolvePersistentIdentity(command.Identity.Cwd)
	if err != nil || e != nil || identity != expected {
		t.Fatal("installed probe lost runtime identity", err, e)
	}
	legacy := base.command
	legacy.Root = b.Root
	legacy.PublicRoot = legacy.Identity.Cwd
	if ProbeCommand(t.Context(), legacy) != nil {
		t.Fatal("legacy helper probe regressed")
	}
	if _, err := ProbePersistentCommand(t.Context(), legacy); err == nil {
		t.Fatal("legacy helper advertised persistence")
	}
}
