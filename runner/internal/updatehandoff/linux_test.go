//go:build linux

package updatehandoff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBeginProcess(t *testing.T) {
	if os.Getenv("UPDATE_TEST_HELPER") != "1" {
		return
	}
	if err := Begin(os.Getenv("UPDATE_TEST_DIR")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("UPDATE_TEST_DIR"), "begun"), nil, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyProcess(t *testing.T) {
	if os.Getenv("UPDATE_TEST_HELPER") != "1" {
		return
	}
	err := Notify(os.Getenv("UPDATE_TEST_DIR"), os.Getenv("UPDATE_TEST_BINARY"), "abc1234", false)
	if os.Getenv("UPDATE_TEST_REJECT") == "1" {
		if err == nil {
			t.Fatal("installer attributed to a later call")
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("UPDATE_TEST_DIR"), "done"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestObservedInstallerProcessTree(t *testing.T) {
	for _, shape := range []string{"plain", "make", "background with wait loop", "later tool call"} {
		t.Run(shape, func(t *testing.T) {
			// Short socket path, still beneath the caller's worktree-local TMPDIR.
			dir, err := os.MkdirTemp(os.TempDir(), "u-")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.RemoveAll(dir) }()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(dir, "install.sh")
			if err = os.WriteFile(script, fmt.Appendf(nil, "#!/bin/sh\n%q -test.run='^TestBeginProcess$' || exit 1\nwhile [ ! -f prepare ]; do sleep .01; done\nexec %q -test.run='^TestNotifyProcess$'\n", exe, exe), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "Makefile"), []byte("restart-daemon:\n\t./install.sh\n"), 0600); err != nil {
				t.Fatal(err)
			}
			command := "./install.sh"
			if shape == "make" {
				command = "make restart-daemon"
			}
			if shape == "background with wait loop" {
				command = "(setsid nohup ./install.sh >installer.log 2>&1 &); while [ ! -f done ]; do sleep .01; done"
			}
			// An actual fake engine owns the tool shell, including an orphaned setsid
			// installer. Its sole live call is supplied by the daemon-side observer.
			cmd := exec.Command("bash", "-c", "while [ ! -f gate ]; do sleep .01; done; "+command+"; result=$?; exit $result")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "BLERG_RUNNER_SESSION_ID=s1", "UPDATE_TEST_HELPER=1", "UPDATE_TEST_DIR="+dir, "UPDATE_TEST_BINARY="+exe)
			if shape == "later tool call" {
				cmd.Env = append(cmd.Env, "UPDATE_TEST_REJECT=1")
			}
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			o := &Observer{}
			// A new root bash for a background launch remains in the wait loop;
			// direct launches can exec the installer without changing process identity.
			o.Observe(Request{SessionID: "s1", Call: json.RawMessage(`{"call_id":"update-1"}`), EnginePID: cmd.Process.Pid, EngineStart: ProcessStart(cmd.Process.Pid)})
			closeServer, err := o.Serve(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer closeServer()
			if err = os.WriteFile(filepath.Join(dir, "gate"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if shape == "later tool call" {
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(dir, "begun")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("installer never began")
					}
					time.Sleep(time.Millisecond)
				}
				o.Observe(Request{SessionID: "s1", Call: json.RawMessage(`{"call_id":"different-call"}`), EnginePID: cmd.Process.Pid, EngineStart: ProcessStart(cmd.Process.Pid)})
			}
			if err = os.WriteFile(filepath.Join(dir, "prepare"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() { finished <- cmd.Wait() }()
			select {
			case err = <-finished:
				if err != nil {
					b, _ := os.ReadFile(filepath.Join(dir, "installer.log"))
					t.Fatalf("installer failed: %v %s %s", err, b, output.String())
				}
			case <-time.After(15 * time.Second):
				t.Fatal("installer timed out")
			}
			got := Take(dir, exe, "abc1234", true)
			if shape == "later tool call" {
				if got != nil {
					t.Fatalf("later call inherited update: %+v", got)
				}
				return
			}
			if got == nil || got.Request.SessionID != "s1" || !strings.Contains(string(got.Request.Call), "update-1") {
				t.Fatalf("installer not attributed: %+v", got)
			}
			if again := Take(dir, exe, "abc1234", true); again != nil {
				t.Fatal("replayed update")
			}
		})
	}
}

func TestAttributionRequiresLiveUnambiguousEngine(t *testing.T) {
	o := &Observer{}
	r := Request{SessionID: "s1", Call: json.RawMessage(`{"call_id":"update-1"}`), EnginePID: os.Getpid(), EngineStart: ProcessStart(os.Getpid())}
	if o.requester(os.Getpid()) != nil {
		t.Fatal("disk-only requester")
	}
	o.Observe(r)
	if o.requester(os.Getpid()) == nil {
		t.Fatal("live requester missing")
	}
	r.SessionID = "s2"
	o.Observe(r)
	if o.requester(os.Getpid()) != nil {
		t.Fatal("ambiguous requester")
	}
	o.Observe(Request{SessionID: "s2"})
	r.SessionID = "s1"
	r.EngineStart = "reused pid"
	o.Observe(r)
	if o.requester(os.Getpid()) != nil {
		t.Fatal("reused engine PID")
	}
}

func TestPreparedUpdateRequiresOriginalInstallerAncestry(t *testing.T) {
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := Request{SessionID: "s1", Call: json.RawMessage(`{"call_id":"update-1"}`), EnginePID: os.Getpid(), EngineStart: ProcessStart(os.Getpid())}
	o := &Observer{}
	o.Observe(r)
	// The same engine and call are insufficient: preparation must share an
	// observed installer/make ancestor from the initial notification.
	o.attempts = map[string]attempt{"s1": {request: r, parents: map[int]string{1: "unrelated"}, started: time.Now()}}
	err = o.prepare(dir, os.Getpid(), wireRequest{Binary: binary, Commit: "abc1234"})
	if err == nil || !strings.Contains(err.Error(), "ancestry changed") {
		t.Fatalf("unrelated installer accepted: %v", err)
	}
}

func TestTimerRequesterRequiresObservedUnitAndLiveEngine(t *testing.T) {
	// The installer PID is deliberately unrelated to the live engine. Unit
	// membership is the kernel boundary input; all identity checks stay real.
	engine := exec.Command("cat")
	pipe, err := engine.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pipe.Close(); _ = engine.Wait() })
	o := &Observer{}
	r := Request{SessionID: "s1", Call: json.RawMessage(`{"call_id":"timer-1"}`), EnginePID: engine.Process.Pid, EngineStart: ProcessStart(engine.Process.Pid), TimerUnit: "reinstall.service"}
	o.Observe(r)
	if o.requesterInUnit(os.Getpid(), "reinstall.service") == nil {
		t.Fatal("observed timer not attributed")
	}
	for _, unit := range []string{"", "other.service", "reinstall.timer"} {
		if o.requesterInUnit(os.Getpid(), unit) != nil {
			t.Fatalf("unrelated unit %q accepted", unit)
		}
	}
	o.observed[r.SessionID] = time.Now().Add(-46 * time.Minute)
	if o.requesterInUnit(os.Getpid(), "reinstall.service") != nil {
		t.Fatal("stale timer accepted")
	}
	o.observed[r.SessionID] = time.Now()
	r.EngineStart = "reused pid"
	o.Observe(r)
	if o.requesterInUnit(os.Getpid(), "reinstall.service") != nil {
		t.Fatal("stale engine accepted")
	}
	for _, tc := range []struct{ data, want string }{
		{"0::/user.slice/user-1000.slice/user@1000.service/app.slice/reinstall.service\n", "reinstall.service"},
		{"0::/user.slice/user@1000.service/app.slice/reinstall.service/nested\n", ""},
		{"1:name=systemd:/reinstall.service\n", "reinstall.service"},
		{"1:memory:/reinstall.service\n", ""},
		{"0::/other.service\n1:name=systemd:/reinstall.service\n", ""},
	} {
		if got := cgroupUnit(tc.data); got != tc.want {
			t.Errorf("cgroup %q: %q, want %q", tc.data, got, tc.want)
		}
	}
}

// Opt-in proof through a real systemd user service, outside any session tracer.
func TestProcessUnitInSystemdService(t *testing.T) {
	prefix := os.Getenv("BLERG_TEST_SYSTEMD_UNIT_PREFIX")
	if prefix == "" {
		t.Skip("requires a named systemd user service")
	}
	unit := processUnit(os.Getpid())
	if !strings.HasPrefix(unit, prefix) || !strings.HasSuffix(unit, ".service") {
		t.Fatalf("kernel unit does not match expected service prefix")
	}
}
