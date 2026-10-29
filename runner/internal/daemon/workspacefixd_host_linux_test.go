//go:build linux

package daemon

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This child is a real controller with a persisted P1 handoff, P5 shell and
// traced CLI writer. SIGKILL bypasses every in-process cleanup/defer.
func TestP6HostCrashController(t *testing.T) {
	root := os.Getenv("BLERG_FIXD_CRASH_ROOT")
	if root == "" {
		return
	}
	repo := filepath.Join(root, "repo")
	initChildRepo(t, repo)
	t.Setenv(DaemonStateDirEnv, filepath.Join(root, "state"))
	m := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: "owner", Runtime: "host", LaunchGeneration: "host-crash-launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	dir, err := os.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	common, err := os.Open(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	pins := &discovery.BoundPins{Manifest: m, Anchor: dir, AnchorPath: repo, Sources: []discovery.SourcePin{{Member: m.Members[0], CanonicalPath: repo, CommonDirectoryPath: filepath.Join(repo, ".git"), Directory: dir, CommonDirectory: common}}}
	authority := &hostExplorerAuthority{}
	service := hostLifecycleTestService(t, authority)
	rig := newCoordRig(t)
	service.Supervisor = newWorkspaceSupervisors(rig.service)
	msg := protocol.SpawnSession{SessionID: "host-crash", Kind: "agent", Workspace: &m}
	token := ccUUID()
	if _, err = service.beginHostIntent(msg, pins, token); err != nil {
		t.Fatal(err)
	}
	l, err := authority.Launch(t.Context(), msg.SessionID, m, token)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := dir.Stat()
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: l.LaunchGeneration, Generation: l.RuntimeGeneration}, ManifestDigest: m.Digest, Root: workspaceRootIdentity(st)}
	if err = authority.Bind(t.Context(), l, b); err != nil {
		t.Fatal(err)
	}
	w, err := service.prepareHostLifecycle(t.Context(), msg, pins, l, b, token, repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.host.driverStarting(); err != nil {
		t.Fatal(err)
	}
	identity, err := shellruntime.ResolveIdentity(repo)
	if err != nil {
		t.Fatal(err)
	}
	consent := &protocol.HostShellConsent{ID: "fresh", OwnerID: m.OwnerID, LaunchGeneration: m.LaunchGeneration, MachineID: identity.Machine, Account: identity.Account, UID: identity.UID, Shell: identity.Shell, Cwd: identity.Cwd, ChallengeID: "challenge"}
	// Registration exists but starts with no consent or enabled policy.
	if binding, e := rig.service.Binding(msg.SessionID); e != nil || binding.HostConsentValid {
		t.Fatal("host launch inherited consent")
	}
	rig.configure(t, msg.SessionID, consent)
	rig.open(t, "crash-shell", 0)
	rig.input(t, 1, "setsid sh -c 'echo $$ > shell.pid; while :; do echo x >> shell.out; sleep 0.01; done' &\n")
	writer := exec.Command("sh", "-c", `setsid sh -c 'echo $$ > "$1/writer.pid"; while :; do echo x >> "$1/writer.out"; sleep 0.01; done' sh "$1" & wait`, "sh", repo)
	if err = w.host.writers.Start(writer); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shell.pid", "writer.pid"} {
		waitUntilFor(t, 10*time.Second, name, func() bool { _, e := os.Stat(filepath.Join(repo, name)); return e == nil })
	}
	if _, err = w.Sync(t.Context()); err != nil {
		t.Fatal("host freeze/thaw", err)
	}
	if err = os.WriteFile(filepath.Join(root, "ready"), []byte(w.record), 0600); err != nil {
		t.Fatal(err)
	}
	select {}
}
func TestP6HostCrashRecoveryUsesDurableHelpers(t *testing.T) {
	if os.Getenv("BLERG_HOST_HELPER_TEST_BINARY") == "" {
		t.Skip("built daemon helper required")
	}
	root := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestP6HostCrashController$", "-test.timeout=1m")
	child.Env = append(os.Environ(), "BLERG_FIXD_CRASH_ROOT="+root)
	log, err := os.Create(filepath.Join(root, "controller.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	child.Stdout = log
	child.Stderr = log
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	joined := false
	defer func() {
		if !joined {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	waitUntilFor(t, 20*time.Second, "durable host helper readiness", func() bool {
		_, e := os.Stat(filepath.Join(root, "ready"))
		if e != nil {
			raw, _ := os.ReadFile(filepath.Join(root, "controller.log"))
			if strings.Contains(string(raw), "FAIL") {
				t.Fatal(string(raw))
			}
		}
		return e == nil
	})
	raw, err := os.ReadFile(filepath.Join(root, "ready"))
	if err != nil {
		t.Fatal(err)
	}
	record := string(raw)
	t.Setenv(DaemonStateDirEnv, filepath.Join(root, "state"))
	state, err := readPrivateWorkspace(record)
	if err != nil {
		t.Fatal(err)
	}
	helpers, err := readPrivateWorkspace(record + ".helpers")
	if err != nil || len(helpers.HostHelpers) < 2 {
		t.Fatal("HOST_DURABLE_HELPERS: shell and writer identities missing", err)
	}
	authority := &hostExplorerAuthority{}
	service := hostLifecycleTestService(t, authority)
	if err = authority.Bind(t.Context(), state.SupervisorLaunch, *state.Binding); err != nil {
		t.Fatal(err)
	}
	service.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) { return state.Launch, nil }
	if err = service.recoverHost(t.Context(), state, record, nil); err == nil {
		t.Fatal("recovery acted while original controller was alive")
	}
	for _, name := range []string{"shell.pid", "writer.pid"} {
		pidRaw, e := os.ReadFile(filepath.Join(root, "repo", name))
		if e != nil {
			t.Fatal(e)
		}
		pid, e := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
		if e != nil {
			t.Fatal(e)
		}
		if syscall.Kill(pid, 0) != nil {
			t.Fatal("LIVE_CONTROLLER: recovery reaped a live controller's descendant")
		}
		status, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if strings.Contains(string(status), ") Z ") {
			t.Fatal("LIVE_CONTROLLER: recovery killed a live controller's descendant")
		}
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	joined = true
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err = service.recoverHost(ctx, state, record, nil); err != nil {
		t.Fatal("HOST_CRASH_RECOVERY:", err)
	}
	saved, err := readPrivateWorkspace(record)
	if err != nil || !saved.TerminalAcknowledged {
		t.Fatal("HOST_CRASH_RECOVERY: no durable completion", err)
	}
	for _, name := range []string{"shell.pid", "writer.pid"} {
		pidRaw, e := os.ReadFile(filepath.Join(root, "repo", name))
		if e != nil {
			t.Fatal(e)
		}
		pid, e := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
		if e != nil {
			t.Fatal(e)
		}
		waitUntilFor(t, 5*time.Second, "descendant termination", func() bool {
			if syscall.Kill(pid, 0) != nil {
				return true
			}
			s, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
			return strings.Contains(string(s), ") Z ")
		})
	}
	// A receipt survives another process restart without requiring an empty P5 map.
	if err = service.recoverHost(ctx, saved, record, nil); err != nil {
		encoded, _ := json.Marshal(saved)
		t.Fatalf("receipt retry: %v %s", err, encoded)
	}
}
