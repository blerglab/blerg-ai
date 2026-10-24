//go:build linux

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

type coordCrashFixture struct {
	Record, Ready, Context, ID, Owner, Session, Launch string
	Paths                                              map[string]bool
}

func (f coordCrashFixture) docker() *DockerWorkspaceContainer {
	return &DockerWorkspaceContainer{Context: f.Context, ID: f.ID, Owner: f.Owner, Session: f.Session, Launch: f.Launch, Paths: f.Paths}
}

// Runs in a separately owned daemon-test process. Abrupt exit skips every defer,
// including the freeze watchdog and shell shutdown. Recovery must use the Docker
// namespace, not a surviving Go map or this parent's helper cleanup callback.
func TestCoordCrashChildProcess(t *testing.T) {
	name := os.Getenv("BLERG_COORD_CRASH_FIXTURE")
	if name == "" {
		return
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var f coordCrashFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	d := f.docker()
	state, err := readPrivateWorkspace(f.Record)
	if err != nil {
		t.Fatal(err)
	}
	rig := newCoordRig(t)
	registry := newWorkspaceSupervisors(rig.service)
	if err = registry.register(t.Context(), state, d); err != nil {
		t.Fatal(err)
	}
	d.Supervisor = registry
	d.SupervisorLaunch = state.SupervisorLaunch
	rig.configure(t, state.Session, nil)
	rig.open(t, "crashed", 0)
	rig.input(t, 1, "setsid /bin/sh -c 'while :; do printf x >> /workspace/crash-background.txt; sleep 0.01; done' </dev/null >/dev/null 2>&1 &\nprintf '\\122\\105\\101\\104\\131'\n")
	rig.wait(t, "READY")
	if err = d.Freeze(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.Ready, []byte("frozen"), 0600); err != nil {
		t.Fatal(err)
	}
	os.Exit(73)
}

func TestCoordDockerDaemonProcessCrashRestart(t *testing.T) {
	w, d, _, _ := dockerCoordRig(t)
	name := filepath.Join(t.TempDir(), "fixture.json")
	ready := filepath.Join(t.TempDir(), "ready")
	snapshot := &DockerWorkspaceContainer{Context: d.Context, ID: d.ID, Owner: d.Owner, Session: d.Session, Launch: d.Launch, Paths: d.Paths}
	raw, err := json.Marshal(coordCrashFixture{Record: w.record, Ready: ready, Context: d.Context, ID: d.ID, Owner: d.Owner, Session: d.Session, Launch: d.Launch, Paths: d.Paths})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCoordCrashChildProcess$")
	cmd.Env = append(os.Environ(), "BLERG_COORD_CRASH_FIXTURE="+name, testStateDirEnv+"="+os.Getenv(DaemonStateDirEnv))
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("owned daemon did not reach crash boundary: %v %s", err, output)
	}
	if data, _ := os.ReadFile(ready); string(data) != "frozen" {
		t.Fatal("crash did not leave frozen shell")
	}
	raw, err = d.command(t.Context(), "inspect", "--format", "{{.State.Running}} {{.State.Paused}}", d.ID)
	if err != nil || strings.TrimSpace(string(raw)) != "true true" {
		t.Fatal("real paused incarnation not left for recovery")
	}
	fresh := newWorkspaceSupervisors(shellruntime.NewService(func(shellruntime.RPC) error { return nil }))
	if err = fresh.registerRecovery(t.Context(), w.state, snapshot); err != nil {
		t.Fatal(err)
	}
	if absent, e := snapshot.Absent(t.Context()); e != nil || !absent {
		t.Fatal("recovery registered proof before exact runtime absence")
	}
	proof, err := fresh.RecoverLaunch(t.Context(), w.state.SupervisorLaunch)
	if err != nil || proof.ValidateFor(w.state.SupervisorLaunch, "") != nil {
		t.Fatal("restarted coordinator cleanup", err)
	}
	snapshot.Supervisor = fresh
	snapshot.SupervisorLaunch = w.state.SupervisorLaunch
	snapshot.CancelStatus = func(context.Context) error { return nil }
	recovered, err := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, w.storage, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recovered.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
