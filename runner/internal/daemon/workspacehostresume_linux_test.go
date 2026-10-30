package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestHostRestartResumesWorkspaceAfterFailedFreeze(t *testing.T) {
	for _, thawFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "freeze", true: "freeze_and_thaw"}[thawFails], func(t *testing.T) {
			if os.Getenv("BLERG_HOST_HELPER_TEST_BINARY") == "" {
				t.Skip("built daemon helper required")
			}
			newHost, records, repo, log := recoveryHost(t)
			stubTranscripts(t, "conv-1")
			initChildRepo(t, repo)
			t.Setenv(DaemonStateDirEnv, t.TempDir())
			m := legacyExplorerStart("session").Manifest
			m.Runtime = "host"
			authority := &hostExplorerAuthority{}
			l, err := authority.Launch(t.Context(), "session", m, "old-token")
			if err != nil {
				t.Fatal(err)
			}
			info, _ := os.Stat(repo)
			common, _ := os.Stat(filepath.Join(repo, ".git"))
			b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: l.LaunchGeneration, Generation: l.RuntimeGeneration}, ManifestDigest: m.Digest, Root: workspaceRootIdentity(info)}
			if err = authority.Bind(t.Context(), l, b); err != nil {
				t.Fatal(err)
			}
			record, _ := workspaceRecord("session")
			w := &LocalWorkspace{mu: new(sync.Mutex), record: record, state: workspacePrivateManifest{Version: privateWorkspaceVersion, Host: true, HostDriverStopped: true, Session: "session", Launch: m, Binding: &b, SupervisorLaunch: l, RuntimeToken: "old-token", Root: repo, HostWorkDir: repo, HostController: hostHelperIdentity{PID: os.Getpid(), Start: "previous-incarnation"}, Sources: []WorkspaceSource{{Member: m.Members[0], Path: repo, CommonPath: filepath.Join(repo, ".git"), RootIdentity: workspaceRootIdentity(info), CommonIdentity: workspaceRootIdentity(common)}}, Members: []workspacePrivateMember{{Member: m.Members[0]}}}}
			w.state.HostHelpersJournaled = true
			helpers := &LocalWorkspace{mu: new(sync.Mutex), record: record + ".helpers", state: workspacePrivateManifest{Session: "session", Launch: m, RuntimeToken: "old-token"}}
			if err = helpers.save(); err != nil {
				t.Fatal(err)
			}
			if err = w.save(); err != nil {
				t.Fatal(err)
			}
			writers := &freezeFailWriters{}
			if thawFails {
				writers.thawErr = errors.New("helper unavailable")
			}
			w.authorize = func(context.Context) error { return nil }
			w.host = &hostWorkspace{w: w, writers: writers, service: &LocalWorkspaceService{Supervisor: &phaseRecorder{}}}
			if _, err = w.Sync(t.Context()); err == nil {
				t.Fatal("fault not exercised")
			}
			if err = writeAgentRecord(records, AgentRecord{SessionID: "session", Repo: "proj", WorkDir: repo, ClaudeSessionID: "conv-1", Interaction: protocol.InteractionUnattended, TurnActive: true}); err != nil {
				t.Fatal(err)
			}
			host, sender := newHost()
			service := hostLifecycleTestService(t, authority)
			service.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) { return m, nil }
			service.RecoverableHost = func(string) bool { return true }
			// Neither a live controller nor replaced directory identities may
			// be treated as proof that a failed freeze is safe to resume.
			for _, fault := range []string{"live_controller", "changed_root"} {
				t.Run(fault, func(t *testing.T) {
					saved := w.state
					defer func() {
						w.state = saved
						if err := w.save(); err != nil {
							t.Fatal(err)
						}
					}()
					if fault == "live_controller" {
						current, e := hostHelperIdentityFor(os.Getpid())
						if e != nil {
							t.Fatal(e)
						}
						w.state.HostController = current
					} else {
						changed := *w.state.Binding
						changed.Root.Inode++
						w.state.Binding = &changed
					}
					if err := w.save(); err != nil {
						t.Fatal(err)
					}
					msg := protocol.SpawnSession{SessionID: "session", Workspace: &m}
					pins, e := service.reopenHostPins(t.Context(), msg)
					if e != nil {
						t.Fatal(e)
					}
					defer pins.Close()
					if _, e = service.resumeHostIntent(t.Context(), msg, pins); e == nil {
						t.Fatal("unproven workspace resumed")
					}
				})
			}
			manager := newManagerWithSender(sender, ManagerConfig{ReposRoot: filepath.Dir(repo), Workspaces: service})
			// A new daemon has no P1 in-memory selection: the old journal must suffice.
			manager.discovery = discovery.New(func() string { return filepath.Dir(repo) })
			host.cfg.Workspaces = service
			host.cfg.PrepareHostWorkspace = manager.prepareHostWorkspace
			if err = service.RecoverAll(t.Context(), func(protocol.SessionEnded) error { t.Fatal("restart ended session"); return nil }); err != nil {
				t.Fatal(err)
			}
			host.NoteConnected()
			if n := host.AdoptRecorded(); n != 1 || !host.Has("session") {
				t.Fatalf("adopted %d; errors %v", n, errorTexts(sender))
			}
			if host.get("session").localWorkspace == nil {
				t.Fatal("workspace lifecycle lost")
			}
			sender.waitForKind(t, "turn_done")
			raw, err := os.ReadFile(log)
			if err != nil || !strings.Contains(string(raw), "--resume conv-1") {
				t.Fatal("conversation not resumed", err)
			}
			for _, kind := range sentTypes(sender) {
				if strings.HasPrefix(kind, "session_ended:") {
					t.Fatal(kind)
				}
			}
		})
	}
}

// A host journal claimed by a recovery record is kept for adoption, but the
// previous incarnation's helpers are reaped by the sweep all the same: a
// session whose adoption stays paused must leave no writer running.
func TestHostRestartSweepReapsHelpersOfAClaimedSession(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	authority := &hostExplorerAuthority{}
	service := hostLifecycleTestService(t, authority)
	m := legacyExplorerStart("session").Manifest
	m.Runtime = "host"
	l, err := authority.Launch(t.Context(), "session", m, "token")
	if err != nil {
		t.Fatal(err)
	}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: l.LaunchGeneration, Generation: l.RuntimeGeneration}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Device: 1, Inode: 1}}
	if err = authority.Bind(t.Context(), l, b); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command("sleep", "60")
	if err = helper.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = helper.Wait(); close(exited) }()
	t.Cleanup(func() { _ = helper.Process.Kill(); <-exited })
	identity, err := hostHelperIdentityFor(helper.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	record, err := workspaceRecord("session")
	if err != nil {
		t.Fatal(err)
	}
	helpers := &LocalWorkspace{mu: new(sync.Mutex), record: record + ".helpers", state: workspacePrivateManifest{Session: "session", Launch: m, RuntimeToken: "token", HostHelpers: []hostHelperIdentity{identity}}}
	if err = helpers.save(); err != nil {
		t.Fatal(err)
	}
	w := &LocalWorkspace{mu: new(sync.Mutex), record: record, state: workspacePrivateManifest{Version: privateWorkspaceVersion, Host: true, HostDriverStopped: true, HostHelpersJournaled: true, Session: "session", Launch: m, Binding: &b, SupervisorLaunch: l, RuntimeToken: "token", HostController: hostHelperIdentity{PID: os.Getpid(), Start: "previous-incarnation"}}}
	if err = w.save(); err != nil {
		t.Fatal(err)
	}
	service.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) { return m, nil }
	service.RecoverableHost = func(id string) bool { return id == "session" }
	if err = service.RecoverAll(t.Context(), func(protocol.SessionEnded) error { t.Fatal("restart ended a resumable session"); return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-t.Context().Done():
		t.Fatal("the previous incarnation's helper is still running")
	}
	if _, err = readPrivateWorkspace(record); err != nil {
		t.Fatal("journal not kept for adoption", err)
	}
}
