package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// failingCodexAppServer puts a `codex` on PATH whose app-server dies before it answers
// (`initialize` fails) for the first failFirst launches, then runs the fixture app-server.
// failFirst < 0 fails every launch: the runtime is gone (its container removed, the binary broken).
func failingCodexAppServer(t *testing.T, failFirst int) {
	t.Helper()
	fixture := filepath.Join(fakeCodex(t), "codex")
	dir := t.TempDir()
	counter := filepath.Join(dir, "launches")
	script := `#!/bin/sh
case " $* " in *" app-server "*) ;; *) exec "` + fixture + `" "$@" ;; esac
n=$(cat "` + counter + `" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "` + counter + `"
if [ ` + itoaTest(failFirst) + ` -lt 0 ] || [ "$n" -le ` + itoaTest(failFirst) + ` ]; then exit 1; fi
exec "` + fixture + `" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func itoaTest(n int) string { return strconv.Itoa(n) }

func (s *agentTestSender) sessionEnds() []protocol.SessionEnded {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []protocol.SessionEnded
	for _, m := range s.msgs {
		if e, ok := m.(protocol.SessionEnded); ok {
			out = append(out, e)
		}
	}
	return out
}

func spawnCodexTestSession(t *testing.T, sender *agentTestSender) *AgentHost {
	t.Helper()
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir()})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s1", Repo: "proj",
		InitialPrompt: "hello", Kind: "agent", Engine: "codex",
	})
	t.Cleanup(func() { host.Kill("s1") })
	return host
}

// A Codex session whose app-server can never come back (card #108: its sandbox container was
// gone) ends: a non-zero exit carrying the engine's reason, no longer hosted, so no heartbeat
// lists it and the server cannot revive it to "running". Before the fix the daemon kept hosting
// it after the driver gave up, and the server showed it running forever.
func TestAgentHostEndsSessionWhenCodexRuntimeStops(t *testing.T) {
	failingCodexAppServer(t, -1)
	sender := &agentTestSender{}
	host := spawnCodexTestSession(t, sender)

	waitUntilFor(t, 20*time.Second, "the session ends", func() bool { return len(sender.sessionEnds()) > 0 })
	ends := sender.sessionEnds()
	if len(ends) != 1 || ends[0].SessionID != "s1" || ends[0].ExitCode == 0 {
		t.Fatalf("session_ended = %+v, want one non-zero exit for s1", ends)
	}
	if want := "Codex app-server repeatedly failed; update Codex and start a new session"; ends[0].Message != want {
		t.Fatalf("end reason = %q, want %q", ends[0].Message, want)
	}
	if host.Has("s1") {
		t.Fatal("the stopped session is still hosted")
	}
	for _, id := range host.ActiveIDs() {
		if id == "s1" {
			t.Fatal("the stopped session is still reported active (the server would revive it)")
		}
	}
	// The end carries the reason; no separate error status races it.
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("unexpected error statuses alongside the end: %v", errs)
	}
}

// Retryable failures the driver recovers from do not end the session: an app-server that dies
// once at start is restarted, and the session goes on to take its turn.
func TestAgentHostKeepsCodexSessionThatRecovers(t *testing.T) {
	failingCodexAppServer(t, 1)
	sender := &agentTestSender{}
	host := spawnCodexTestSession(t, sender)

	sender.waitForKind(t, "turn_done")
	if ends := sender.sessionEnds(); len(ends) > 0 {
		t.Fatalf("a recovered session ended: %+v", ends)
	}
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("a recovered session was marked in error: %v", errs)
	}
	if !host.Has("s1") {
		t.Fatal("a recovered session is no longer hosted")
	}
}

func waitUntilFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A Hermes session whose gateway can never run (an unsupported gateway: permanentFailure) ends the
// same way, with Hermes's reason. Before, its driver returned and the session stayed listed.
func TestAgentHostEndsHermesSessionOnPermanentFailure(t *testing.T) {
	binDir := fakeHermes(t, "unused", 0)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_HERMES_BRIDGE_ERROR", "1")
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir()})
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "s1", Repo: "proj", InitialPrompt: "hello", Kind: "agent", Engine: "hermes"})
	t.Cleanup(func() { host.Kill("s1") })

	waitUntilFor(t, 20*time.Second, "the session ends", func() bool { return len(sender.sessionEnds()) > 0 })
	ends := sender.sessionEnds()
	if want := "A compatible Hermes native gateway is required; install a supported Hermes version"; len(ends) != 1 || ends[0].ExitCode == 0 || ends[0].Message != want {
		t.Fatalf("session_ended = %+v, want one non-zero exit with %q", ends, want)
	}
	if host.Has("s1") {
		t.Fatal("the stopped session is still hosted")
	}
	// The runtime_stopped code: one end carries the reason, with no separate error status racing it.
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("unexpected error statuses alongside the end: %v", errs)
	}
	stopped := false
	for _, ev := range sender.agentEvents() {
		if ev.Kind == "error" && strings.Contains(string(ev.Payload), `"code":"`+agent.ErrorCodeRuntimeStopped+`"`) {
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("no runtime_stopped error event: a watcher could not tell the child stopped for good")
	}
}

// The reason a session ends with is the error that stopped it, not an earlier turn's error the
// engine went on from.
func TestStopReasonIsTheErrorSinceTheLastTurnStarted(t *testing.T) {
	s := &agentSession{}
	errEv := func(msg string) agent.Event {
		return agent.Event{Kind: "error", Payload: agent.ErrorPayload{Message: msg}}
	}
	s.trackStopReason(errEv("an earlier turn failed"), "an earlier turn failed")
	s.trackStopReason(agent.Event{Kind: "status_changed", Payload: agent.StatusPayload{Status: "running"}}, "")
	if p := s.stopReason.Load(); p != nil {
		t.Fatalf("stop reason after a new turn started = %q, want none", *p)
	}
	s.trackStopReason(agent.Event{Kind: "error", Payload: agent.ErrorPayload{Message: "retry", Retryable: true}}, "retry")
	s.trackStopReason(errEv("the runtime stopped"), "the runtime stopped")
	s.trackStopReason(agent.Event{Kind: "status_changed", Payload: agent.StatusPayload{Status: "idle"}}, "")
	if p := s.stopReason.Load(); p == nil || *p != "the runtime stopped" {
		t.Fatalf("stop reason = %v, want the runtime stopped", p)
	}
}

// hermesErrors are the error payloads a driver emitted.
func hermesErrors(em *collectEmitter) []agent.ErrorPayload {
	var out []agent.ErrorPayload
	for _, ev := range em.snapshot() {
		if p, ok := ev.Payload.(agent.ErrorPayload); ok && ev.Kind == "error" {
			out = append(out, p)
		}
	}
	return out
}

// M-a: a Hermes gateway whose shutdown cannot be confirmed while the session is meant to run stops
// the runtime for good, said with the runtime_stopped code (so the session ends with it and a
// watcher sees it). The gateway ignores SIGTERM and outlives its stdin; the driver cancels it
// itself after an unsupported-gateway answer.
func TestHermesUnverifiedShutdownStopsTheRuntime(t *testing.T) {
	binDir := fakeHermes(t, "unused", 0)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_HERMES_BRIDGE_ERROR", "1")
	t.Setenv("FAKE_HERMES_STUBBORN", "1")
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), SessionID: "s1", Emitter: em})
	done := make(chan struct{})
	go func() { d.Run(t.Context()); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return")
	}
	const want = "Hermes shutdown did not finish gracefully; automatic restart is disabled"
	found := false
	for _, p := range hermesErrors(em) {
		if p.Message == want {
			found = true
			if p.Code != agent.ErrorCodeRuntimeStopped || p.Retryable {
				t.Fatalf("unverified shutdown error = %+v, want a non-retryable runtime_stopped", p)
			}
		}
	}
	if !found {
		t.Fatalf("no unverified-shutdown error; errors: %+v", hermesErrors(em))
	}
}

// M-b: a stop the person or a parent asked for is not a failure, even when the gateway's shutdown
// cannot be confirmed: no runtime_stopped is said, so a watcher of that child sees it ended, not
// failed (exit 0, not 4). Restart stays disabled.
func TestHermesRequestedStopIsNotARuntimeFailure(t *testing.T) {
	binDir := fakeHermes(t, "done", 0)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_HERMES_STUBBORN", "1")
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), SessionID: "s1", Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	d.Enqueue("hello", "chat")
	waitUntil(t, "the turn", func() bool { return countKind(em, "turn_done") == 1 })
	cancel() // the session is being stopped
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return")
	}
	d.mu.Lock()
	uncertain := d.cleanupUncertain
	d.mu.Unlock()
	if !uncertain {
		t.Fatal("the gateway's shutdown was confirmed: the test did not reach the unverified path")
	}
	for _, p := range hermesErrors(em) {
		if p.Code == agent.ErrorCodeRuntimeStopped || strings.Contains(p.Message, "did not finish gracefully") {
			t.Fatalf("a requested stop was reported as a runtime failure: %+v", p)
		}
	}
}

// M-b at the host: killing a Hermes child whose gateway will not confirm its shutdown ends it as
// stopped (exit 0, no reason) with no runtime_stopped event, so `session watch` reports it ended.
func TestHostKillOfAStubbornHermesSessionIsAStop(t *testing.T) {
	binDir := fakeHermes(t, "done", 0)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_HERMES_STUBBORN", "1")
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir()})
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "s1", Repo: "proj", InitialPrompt: "hello", Kind: "agent", Engine: "hermes"})
	sender.waitForKind(t, "turn_done")
	host.Kill("s1")
	ends := sender.sessionEnds()
	if len(ends) != 1 || ends[0].ExitCode != 0 || ends[0].Message != "" {
		t.Fatalf("session_ended = %+v, want one stop (exit 0)", ends)
	}
	for _, ev := range sender.agentEvents() {
		if ev.Kind == "error" && strings.Contains(string(ev.Payload), agent.ErrorCodeRuntimeStopped) {
			t.Fatalf("runtime_stopped sent for a requested stop: %s", ev.Payload)
		}
	}
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("error statuses for a requested stop: %v", errs)
	}
}
