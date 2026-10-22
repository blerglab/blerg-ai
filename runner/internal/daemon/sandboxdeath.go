package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// A sandboxed session's engine runs inside its container (sandbox.go). When that container stops
// (killed, out of memory, Docker restarted, removed by hand), nothing can run in it again: every
// `docker exec` fails, and an engine restarted there only fails again. What the person and a parent
// must hear is that the container stopped, with what Docker recorded about it, not an engine's
// generic advice. This file finds that out and reports it once; the session then ends through the
// runtime_stopped path (agentsession.go runtimeStopped / killWith).

// sandboxWatchInterval is how often the daemon looks at a sandboxed agent session's container while
// nothing else asks. An engine failure asks at once (agentSession.sandboxCheck). Zero or less: no
// periodic look (tests that do not exercise it).
var sandboxWatchInterval = 5 * time.Second

// sandboxContainerState is the part of `docker inspect`'s State the daemon reads.
type sandboxContainerState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Paused     bool   `json:"Paused"`
	Restarting bool   `json:"Restarting"`
	OOMKilled  bool   `json:"OOMKilled"`
	ExitCode   int    `json:"ExitCode"`
	Error      string `json:"Error"`
	FinishedAt string `json:"FinishedAt"`
}

// sandboxContainerExit reports whether a container has stopped for good, and what Docker recorded
// about it. Only Docker's own word counts: a container it no longer knows (removed), or one that
// is neither running nor paused nor restarting. A paused container (the turn-end sync pauses it
// around a fetch) is alive. Any failure to ask (Docker unreachable, a slow daemon, output that does
// not parse) is "not known to be dead": a session is never ended on a guess. A var so tests can
// stub it.
//
// Every look goes to the Docker the container was started on (its pinned context, see
// pinSandboxContext): a person switching their docker CLI's context must not make a running
// container look removed. And "gone" is final only when a second look, sandboxGoneConfirm later,
// says it again; a stopped container's record (exited, dead) is final at once.
var sandboxContainerExit = func(container string) (agent.SandboxExit, bool) {
	return confirmSandboxExit(func() (agent.SandboxExit, bool) {
		return inspectSandboxExit(func(args ...string) ([]byte, error) {
			return dockerOutput(pinnedDockerArgs(container, args...)...)
		}, container)
	})
}

// sandboxGoneConfirm is the wait between the two "No such container" answers that make a container
// gone.
var sandboxGoneConfirm = 3 * time.Second

// confirmSandboxExit asks again, after sandboxGoneConfirm, when the first answer was "gone".
func confirmSandboxExit(look func() (agent.SandboxExit, bool)) (agent.SandboxExit, bool) {
	exit, dead := look()
	if !dead || !exit.Gone {
		return exit, dead
	}
	time.Sleep(sandboxGoneConfirm)
	return look()
}

// sandboxContexts pins each sandbox container to the docker context it was started on.
var sandboxContexts sync.Map // container name -> context name

// pinSandboxContext records the docker context a container was started on ("" = none known: the
// docker CLI's own choice is used).
func pinSandboxContext(container, name string) {
	if name == "" {
		sandboxContexts.Delete(container)
		return
	}
	sandboxContexts.Store(container, name)
}

// Restore the durable pin before a rediscovered container is watched. Legacy containers
// have no label; keep the first fallback for this daemon run and explain it once.
func restoreSandboxContext(container, name string) {
	if _, ok := sandboxContexts.Load(container); ok {
		return
	}
	if name == "" {
		name = currentDockerContext()
		if _, seen := sandboxContextFallbackLogged.LoadOrStore(container, struct{}{}); !seen {
			log.Printf("sandbox: container %s has no Docker context label; falling back to current context %q", container, name)
		}
	}
	sandboxContexts.LoadOrStore(container, name)
}

var sandboxContextFallbackLogged sync.Map

// pinnedDockerArgs prefixes a docker call about container with its pinned context.
func pinnedDockerArgs(container string, args ...string) []string {
	if v, ok := sandboxContexts.Load(container); ok {
		if name, _ := v.(string); name != "" {
			return append([]string{"--context", name}, args...)
		}
	}
	return args
}

// currentDockerContext is the context the docker CLI uses right now (`docker context show`; with
// DOCKER_HOST set that is "default", which reads DOCKER_HOST). "" when it cannot say. A var so
// tests can stub it.
var currentDockerContext = func() string {
	out, err := dockerOutput("context", "show")
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(out))
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " \t\n/") {
		return ""
	}
	return name
}

// sandboxSnapshot is one `docker ps` of this daemon's sandbox containers on one docker context:
// container name -> State.
type sandboxSnapshot struct {
	at      time.Time
	states  map[string]string
	err     bool
	loading bool
}

var (
	sandboxSnapMu sync.Mutex
	sandboxSnaps  = map[string]*sandboxSnapshot{} // by context name ("" = the CLI's own)
)

// sandboxLooksAlive is the periodic look's cheap first step: one `docker ps -a` of this daemon's
// own containers (by its owner label), shared by every session on that docker context and made at
// most once per sandboxWatchInterval, says whether the container is running, paused or
// restarting. Only a container it does not show that way is inspected. A failed listing (Docker
// unreachable) is "alive": nothing is known to be dead.
func sandboxLooksAlive(container string) bool {
	own := sandboxOwner()
	if own == "" {
		return false // no label to list by: inspect
	}
	pinned := pinnedDockerArgs(container)
	key := ""
	if len(pinned) == 2 {
		key = pinned[1]
	}
	sandboxSnapMu.Lock()
	snap := sandboxSnaps[key]
	if snap != nil && snap.loading {
		sandboxSnapMu.Unlock()
		return true // an in-flight listing knows nothing dead; do not stall another watcher
	}
	if snap == nil || time.Since(snap.at) >= sandboxWatchInterval/2 {
		sandboxSnaps[key] = &sandboxSnapshot{loading: true}
		sandboxSnapMu.Unlock()
		snap = &sandboxSnapshot{states: map[string]string{}}
		out, err := dockerOutput(append(pinned, "ps", "-a", "--filter", "label="+sandboxOwnerLabel+"="+own,
			"--format", "{{.Names}}\t{{.State}}")...)
		if err != nil {
			snap.err = true
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if name, state, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
				snap.states[name] = strings.TrimSpace(state)
			}
		}
		snap.at = time.Now()
		sandboxSnapMu.Lock()
		sandboxSnaps[key] = snap
	}
	sandboxSnapMu.Unlock()
	if snap.err {
		return true
	}
	switch snap.states[container] {
	case "running", "paused", "restarting":
		return true
	}
	return false
}

// inspectSandboxExit is sandboxContainerExit through a given docker client.
func inspectSandboxExit(output func(args ...string) ([]byte, error), container string) (agent.SandboxExit, bool) {
	out, err := output("inspect", "--type", "container", "--format", "{{json .State}}", container)
	if err != nil {
		if isNoSuchContainer(dockerErrorText(err, "docker inspect")) {
			return agent.SandboxExit{Gone: true}, true
		}
		return agent.SandboxExit{}, false
	}
	var st sandboxContainerState
	if json.Unmarshal(out, &st) != nil || st.Status == "" {
		return agent.SandboxExit{}, false
	}
	if st.Running || st.Paused || st.Restarting {
		return agent.SandboxExit{}, false
	}
	return agent.SandboxExit{
		Status: st.Status, ExitCode: st.ExitCode, OOMKilled: st.OOMKilled,
		FinishedAt: st.FinishedAt, Error: firstN(strings.TrimSpace(st.Error), 300),
	}, true
}

// isNoSuchContainer matches Docker's answers for a container it does not know ("Error: No such
// container: x", "Error response from daemon: No such container: x", "No such object: x").
func isNoSuchContainer(text string) bool {
	t := strings.ToLower(text)
	return strings.Contains(t, "no such container") || strings.Contains(t, "no such object")
}

// sandboxExitReason is what the session is told, and ends with, when its container stopped.
func sandboxExitReason(e agent.SandboxExit) string {
	const next = "; start a new session to continue the work"
	if e.Gone {
		return "The sandbox container is gone (it was removed, so Docker kept no exit details)" + next
	}
	oom := "no"
	if e.OOMKilled {
		oom = "yes"
	}
	at := "at an unknown time"
	if ts, err := time.Parse(time.RFC3339Nano, e.FinishedAt); err == nil && ts.Year() > 1 {
		at = "at " + ts.UTC().Format(time.RFC3339)
	}
	reason := fmt.Sprintf("The sandbox container stopped (exit code %d, OOMKilled: %s, %s)", e.ExitCode, oom, at)
	if e.Error != "" {
		reason += "; Docker said: " + e.Error
	}
	return reason + next
}

// sandboxDeathReported holds the containers whose stop has been reported, so the driver that ran
// into it and the host's watch never both say it. Cleared when a container of that name starts.
var sandboxDeathReported sync.Map // container name -> struct{}

// forgetSandboxDeath drops what the daemon keeps about a container: called when one of that name
// starts, and when it is removed.
func forgetSandboxDeath(container string) {
	sandboxDeathReported.Delete(container)
	sandboxContexts.Delete(container)
}

// reportSandboxDeath emits the stop of container, once per container: a non-retryable
// runtime_stopped error carrying Docker's record of the exit. The session's event hook keeps it as
// the reason the session ends with.
func reportSandboxDeath(emit func(kind string, payload any), container string, exit agent.SandboxExit) {
	if _, dup := sandboxDeathReported.LoadOrStore(container, struct{}{}); dup {
		return
	}
	reason := sandboxExitReason(exit)
	log.Printf("sandbox: container %s stopped: %s", container, reason)
	e := exit
	emit("error", agent.ErrorPayload{Message: reason, Retryable: false, Code: agent.ErrorCodeRuntimeStopped, SandboxExit: &e})
}

// sandboxStopped is a driver's check before it restarts its engine: when the engine runs in a
// sandbox whose container has stopped, the stop is reported (once) and the driver must not start
// the engine again; it returns from Run and the session ends with that reason. ctx bounds the look
// (its cancellation does not: a gateway being cancelled still needs to know why it ended).
func sandboxStopped(ctx context.Context, prefix sandboxExec, emit func(kind string, payload any)) bool {
	if !prefix.enabled() {
		return false
	}
	var exit agent.SandboxExit
	var dead bool
	if prefix[0] == "docker" {
		exit, dead = sandboxContainerExit(prefix.container())
	} else {
		// The same docker client the engine was started through.
		exit, dead = confirmSandboxExit(func() (agent.SandboxExit, bool) {
			return inspectSandboxExit(func(args ...string) ([]byte, error) {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sandboxProbeTimeout)
				defer cancel()
				cmd := exec.CommandContext(ctx, prefix[0], args...) //nolint:gosec // the daemon-built sandbox prefix's docker client; daemon-built argv
				cmd.WaitDelay = time.Second
				return cmd.Output()
			}, prefix.container())
		})
	}
	if !dead {
		return false
	}
	reportSandboxDeath(emit, prefix.container(), exit)
	return true
}

// askSandboxCheck asks the host's watch of a sandboxed session to look at its container now (an
// engine failure may be the container's). Never blocks; a nil channel (no sandbox) is a no-op.
func askSandboxCheck(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// watchSandbox watches a sandboxed agent session's container until the session ends: every
// sandboxWatchInterval, and whenever the session's engine reports a failure. When the container has
// stopped, the session is told why and ended with that reason (exit 1, through killWith), whatever
// its engine: Claude and Hermes would otherwise go on starting their engine in a container that can
// never run it.
func (h *AgentHost) watchSandbox(ctx context.Context, sessionID string, sess *agentSession, container string, check <-chan struct{}) {
	var tick <-chan time.Time
	if sandboxWatchInterval > 0 {
		t := time.NewTicker(sandboxWatchInterval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			if sandboxLooksAlive(container) {
				continue
			}
		case <-check:
		}
		if h.endIfSandboxStopped(ctx, sessionID, sess, container) {
			return
		}
	}
}

// endIfSandboxStopped ends the session when its container has stopped; reports whether it did (or
// someone else already ended it).
func (h *AgentHost) endIfSandboxStopped(ctx context.Context, sessionID string, sess *agentSession, container string) bool {
	exit, dead := sandboxContainerExit(container)
	if !dead {
		return false
	}
	// A stop or kill already under way removes the container itself: that is not a failure.
	if ctx.Err() != nil || h.get(sessionID) != sess {
		return true
	}
	reportSandboxDeath(func(kind string, payload any) {
		sess.emitter.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: kind, Payload: payload})
	}, container, exit)
	reason := agent.ScrubCode(sandboxExitReason(exit), sess.emitter.markerNonce)
	log.Printf("agent session %s: its sandbox container stopped; ending the session", sessionID)
	h.killWith(sessionID, 1, reason)
	return true
}
