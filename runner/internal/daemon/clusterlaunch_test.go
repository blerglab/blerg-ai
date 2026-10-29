package daemon

import (
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClusterEngineLaunchBoundary(t *testing.T) {
	wanted := errors.New("fixture boundary refusal")
	called := false
	cmd := exec.Command("sh", "-c", "exit 77")
	err := startEngineCommand(cmd, func(got *exec.Cmd) error {
		called = true
		if got != cmd {
			t.Fatal("changed command identity")
		}
		return wanted
	})
	if !called || !errors.Is(err, wanted) || cmd.Process != nil {
		t.Fatal("engine bypassed trusted launcher")
	}
	for _, name := range []string{"claudecode.go", "claudestream.go", "codexapp.go", "hermesdriver.go", "openclawdriver.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "startEngineCommand(cmd, d.startCommand)") || strings.Contains(string(raw), "cmd.Start()") {
			t.Fatalf("%s bypasses cluster launcher", name)
		}
	}
	// Exercise the configured AgentHost boundary, independent of the local
	// variable used to select cluster versus host lifecycle ownership.
	t.Setenv("PATH", fakeClaude(t)+string(filepath.ListSeparator)+os.Getenv("PATH"))
	host, _, _ := newTestHost(t, &scriptedAgentProvider{}, "")
	host.cfg.Provider = nil
	host.cfg.ClaudeCode = true
	host.cfg.StartCommand = func(got *exec.Cmd) error {
		if got != cmd {
			t.Fatal("changed configured command")
		}
		return wanted
	}
	host.Spawn(protocol.SpawnSession{SessionID: "cluster-launch-boundary", Kind: "agent", Repo: "proj"})
	defer host.Kill("cluster-launch-boundary")
	session := host.get("cluster-launch-boundary")
	if session == nil {
		t.Fatal("missing engine session")
	}
	driver, ok := session.loop.(*claudeCodeDriver)
	if !ok || driver.startCommand == nil {
		t.Fatal("AgentHost did not install driver boundary")
	}
	if err = startEngineCommand(cmd, driver.startCommand); !errors.Is(err, wanted) || cmd.Process != nil {
		t.Fatal("configured engine bypassed cluster launcher", err)
	}
}

func TestClusterStopProofPrecedesDriverCancellation(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	sender := &agentTestSender{}
	proven := false
	canceled := false
	interrupted := false
	sess := &agentSession{loop: &clusterOrderedStopDriver{proof: &proven, interrupted: &interrupted, t: t}, cancel: func() {
		if !proven {
			t.Error("driver canceled before writer receipt")
		}
		canceled = true
	}, emitter: newWSEmitter(sender, "session", nil)}
	host := &AgentHost{sender: sender, sessions: map[string]*agentSession{"session": sess}, cfg: AgentHostConfig{BeforeDriverStop: func(id string) error {
		if id != "session" {
			t.Fatal("wrong session")
		}
		proven = true
		return nil
	}}}
	host.Kill("session")
	if !proven || !canceled || !interrupted {
		t.Fatal("missing stop integration")
	}
}

type clusterOrderedStopDriver struct {
	fakeRefreshDriver
	proof, interrupted *bool
	t                  *testing.T
}

func (d *clusterOrderedStopDriver) Interrupt() {
	if !*d.proof {
		d.t.Error("driver interrupted before containment receipt")
	}
	*d.interrupted = true
}
