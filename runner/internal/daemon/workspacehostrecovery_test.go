package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Start with an old daemon's journal: no new recovery flag is required.
func TestHostRestartRetainsSessionAfterFailedFreeze(t *testing.T) {
	for _, thawFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "freeze", true: "freeze_and_thaw"}[thawFails], func(t *testing.T) {
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
			record, err := workspaceRecord("session")
			if err != nil {
				t.Fatal(err)
			}
			w := &LocalWorkspace{mu: new(sync.Mutex), record: record, state: workspacePrivateManifest{Version: privateWorkspaceVersion, Host: true, HostDriverStopped: true, Session: "session", Launch: m, Binding: &b, SupervisorLaunch: l, RuntimeToken: "token"}}
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
				t.Fatal("freeze fault was not exercised")
			}
			service.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) { return m, nil }
			service.RecoverableHost = func(id string) bool { return id == "session" }
			ended := 0
			if err = service.RecoverAll(t.Context(), func(protocol.SessionEnded) error { ended++; return nil }); err != nil {
				t.Fatal(err)
			}
			saved, err := readPrivateWorkspace(record)
			if err != nil {
				t.Fatal(err)
			}
			if ended != 0 || saved.StopRequested || saved.Stopped || saved.TerminalAcknowledged {
				t.Fatal("restart ended a resumable session")
			}
		})
	}
}
