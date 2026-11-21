package daemon

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCodexDockerStopKillsInnerAppBeforeResume(t *testing.T) {
	image := os.Getenv("BLERG_STOP_DOCKER_IMAGE")
	if image == "" {
		t.Skip("set BLERG_STOP_DOCKER_IMAGE for disposable Docker Stop proof")
	}
	shrink(t, &codexInterruptResultAfter, 150*time.Millisecond)
	bin := fakeCodex(t)
	label := labelTestContainers(t)
	name := "cx_stop_" + strings.ReplaceAll(ccUUID(), "-", "")[:12]
	docker := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("Docker fixture: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	docker("run", "-d", "--name", name, "--label", label, "--label", sandboxTestLabels[0], "--network", "none", "--mount", "type=bind,source="+bin+",target=/fixture,readonly", "-e", "PATH=/fixture:/usr/bin:/bin", "-e", "FAKE_CODEX_STUCK_STOP=/tmp/stopped", "-e", "FAKE_CODEX_LOG=/tmp/calls", "-e", "FAKE_CODEX_IGNORE_EOF=1", "--entrypoint", "sleep", image, "infinity")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	d.useSandbox(sandboxExec{"docker", "exec", "-i", name})
	stop := startTestCodexDriver(t.Context(), t, d)
	defer stop()
	d.Enqueue("first", "chat")
	waitUntil(t, "first sandbox turn", func() bool { return em.count("user:first") == 1 })
	old := docker("exec", name, "cat", sandboxTurnPIDFile)
	d.Interrupt()
	d.Enqueue("next", "chat")
	waitUntil(t, "resumed sandbox turn", func() bool { return em.count("user:next") == 1 && countKind(em, "turn_done") == 2 })
	state := docker("exec", name, "sh", "-c", "if test -r /proc/"+old+"/stat; then cat /proc/"+old+"/stat; fi")
	if state != "" && !strings.Contains(state, ") Z ") {
		t.Fatal("old app-server still alive after replacement")
	}
	if countKind(em, "error") != 0 {
		t.Fatal(em.outline())
	}
}

func TestClaudeDockerStopOwnsBackgroundChild(t *testing.T) {
	image := os.Getenv("BLERG_STOP_DOCKER_IMAGE")
	if image == "" {
		t.Skip("set BLERG_STOP_DOCKER_IMAGE for disposable Docker child cleanup proof")
	}
	shrink(t, &ccInterruptKillAfter, 60*time.Millisecond)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(root+"/claude", []byte("#!/bin/sh\nexec /fixture-helper -test.run='^TestHelperFakeClaude$' -- \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	label := labelTestContainers(t)
	name := "cx_claude_stop_" + strings.ReplaceAll(ccUUID(), "-", "")[:12]
	docker := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("Docker fixture: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	docker("run", "-d", "--name", name, "--label", label, "--label", sandboxTestLabels[0], "--network", "none", "--mount", "type=bind,source="+root+",target=/fixture,readonly", "--mount", "type=bind,source="+exe+",target=/fixture-helper,readonly", "-e", "PATH=/fixture:/usr/bin:/bin", "-e", "GO_FAKE_CLAUDE=1", "-e", "FAKE_CHILD_PID=/tmp/child", "-e", "FAKE_LOG=/tmp/calls", "--entrypoint", "sleep", image, "infinity")
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.steer.legacy = false
	d.useSandbox(sandboxExec{"docker", "exec", "-i", name})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	defer func() { cancel(); <-done }()
	d.Enqueue("HANG wait", "chat")
	waitUntil(t, "sandbox tool", func() bool { return em.count("tool_call") == 1 })
	child := docker("exec", name, "cat", "/tmp/child")
	d.Interrupt()
	waitIdle(t, em, 1)
	waitUntil(t, "sandbox background child stopped", func() bool {
		state := docker("exec", name, "sh", "-c", "if test -r /proc/"+child+"/stat; then cat /proc/"+child+"/stat; fi")
		return state == "" || strings.Contains(state, ") Z ")
	})
	if countKind(em, "error") != 0 {
		t.Fatal(em.outline())
	}
}

// The stall wake-up on a real container: the task's output exists only inside it.
func TestClaudeDockerStalledTaskWakesTheSession(t *testing.T) {
	image := os.Getenv("BLERG_STOP_DOCKER_IMAGE")
	if image == "" {
		t.Skip("set BLERG_STOP_DOCKER_IMAGE for disposable Docker stall proof")
	}
	shrink(t, &ccIdleShutdown, 200*time.Millisecond)
	shrink(t, &ccBgStallAfter, time.Minute)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(root+"/claude", []byte("#!/bin/sh\nexec /fixture-helper -test.run='^TestHelperFakeClaude$' -- \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	label := labelTestContainers(t)
	name := "cx_claude_stall_" + strings.ReplaceAll(ccUUID(), "-", "")[:12]
	docker := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("Docker fixture: %v: %s", err, out)
		}
	}
	docker("run", "-d", "--name", name, "--label", label, "--label", sandboxTestLabels[0], "--network", "none", "--mount", "type=bind,source="+root+",target=/fixture,readonly", "--mount", "type=bind,source="+exe+",target=/fixture-helper,readonly", "-e", "PATH=/fixture:/usr/bin:/bin", "-e", "GO_FAKE_CLAUDE=1", "-e", "FAKE_LOG=/tmp/calls", "-e", "FAKE_BG_SECONDS=30", "-e", "FAKE_BG_MARKER=/tmp/gate", "--entrypoint", "sleep", image, "infinity")
	docker("exec", name, "touch", "-d", "2000-01-01", "/tmp/gate.output")
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.steer.legacy = false
	d.useSandbox(sandboxExec{"docker", "exec", "-i", name})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	defer func() { cancel(); <-done }()
	d.Enqueue("REALBG run the gate", "chat")
	waitUntil(t, "the stalled task's wake-up", func() bool {
		for _, line := range em.outline() {
			if strings.HasPrefix(line, "user:Background work has written no output for") {
				return true
			}
		}
		return false
	})
}

func TestCancelExitedEngineIsNotARefusal(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := cancelEngineCommand(cmd, false); err != nil {
		t.Fatalf("finished engine reported a Stop refusal: %v", err)
	}
}
