package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceServerNegotiatesBothHopVersions(t *testing.T) {
	for _, peer := range []protocol.WorkspaceCapabilities{{}, {ManifestVersion: 1, ShellVersion: 1}, {ManifestVersion: 3, ShellVersion: 1}, {ManifestVersion: 2}, {ManifestVersion: 2, ShellVersion: 1}, {ManifestVersion: 2, ShellVersion: 1, LinuxHost: true}, {ManifestVersion: 2, ShellVersion: 1, Sandbox: true}, {ManifestVersion: 2, ShellVersion: 1, MacOSHost: true}, {ManifestVersion: 2, ShellVersion: 1, Cluster: true}, {ManifestVersion: 2, ShellVersion: 3, Sandbox: true}} {
		hub := NewHub()
		// A ready transport may offer only the independently enabled runtimes.
		hub.runtimeShellReady.Store(true)
		conn := dialDaemon(t, hub)
		if err := conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: "negotiation-desktop", Token: "tok-1234567890", ProtocolVersion: "1", Welcome: true, WorkspaceCapabilities: peer}); err != nil {
			t.Fatal(err)
		}
		var welcome protocol.DaemonWelcome
		if err := json.Unmarshal(readFirstMessage(t, conn), &welcome); err != nil {
			t.Fatal(err)
		}
		want := protocol.NegotiateWorkspaceCapabilities(protocol.EnabledWorkspaceCapabilities(), peer)
		if welcome.WorkspaceCapabilities != want {
			t.Fatalf("peer=%+v welcome=%+v want=%+v", peer, welcome.WorkspaceCapabilities, want)
		}
		dc := desktopWait(t, hub, welcome.DaemonID)
		if dc.WorkspaceCapabilities != want {
			t.Fatal("hub retained unnegotiated capability", dc.WorkspaceCapabilities)
		}
	}
}

func TestWorkspaceRunnerLaunchCarriesNegotiatedCapabilities(t *testing.T) {
	for _, tc := range []struct{ runtime, wire string }{{"host", "daemon"}, {"sandbox", "docker"}} {
		t.Run(tc.runtime, func(t *testing.T) {
			f := newPrivFixture(t)
			dc := f.dc
			dc.WorkspaceCapabilities = protocol.WorkspaceCapabilitiesFor(tc.runtime, "linux")
			dc.SetSandboxAvailable(true)
			dc.SetAvailableEngines([]string{"claude"})
			dc.SetHostClaude(true, false, nil)
			dc.SetCheckedOutRepos([]string{"workspace"})
			m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: tc.runtime, OwnerID: privAcctA, LaunchGeneration: newUUID(), Digest: "trusted-selection", Members: []protocol.WorkspaceMember{
				{ID: "one", Name: "one", Path: "one", SourceIdentity: "source-one", CommonDirectoryIdentity: "common-one", Destination: "source-one"},
				{ID: "two", Name: "two", Path: "two", SourceIdentity: "source-two", CommonDirectoryIdentity: "common-two", Destination: "source-two"},
			}}
			request := runnerStartRequest{Workspace: &m, Repo: "workspace", DaemonID: dc.ID, Runtime: tc.wire, Engine: "claude", Title: "Selected workspace"}
			principal := RunnerPrincipal{Kind: "human", Sub: privAcctA, Sid: testSID, Caps: []string{"session.start"}}
			if apiErr := f.api.startBoardSessionOnDaemon(context.Background(), request, principal, newUUID()); apiErr != nil {
				t.Fatal(apiErr)
			}
			select {
			case raw := <-dc.send:
				var spawn protocol.SpawnSession
				if err := json.Unmarshal(raw, &spawn); err != nil {
					t.Fatal(err)
				}
				if spawn.WorkspaceCapabilities != dc.WorkspaceCapabilities || spawn.Workspace == nil {
					t.Fatal("RUNNER_LAUNCH_CAPABILITIES: negotiated runtime support lost", spawn.WorkspaceCapabilities)
				}
			default:
				t.Fatal("workspace launch was not dispatched")
			}
		})
	}
}

func TestWorkspaceDisabledRuntimeRefusesBeforeSessionAllocation(t *testing.T) {
	for _, tc := range []struct {
		runtime, wire string
		caps          protocol.WorkspaceCapabilities
	}{
		{"host", "daemon", protocol.WorkspaceCapabilitiesFor("sandbox", "darwin")},
		{"sandbox", "docker", protocol.WorkspaceCapabilitiesFor("host", "linux")},
	} {
		t.Run(tc.runtime, func(t *testing.T) {
			f := newPrivFixture(t)
			dc := f.dc
			dc.WorkspaceCapabilities = tc.caps
			dc.SetAvailableEngines([]string{"claude"})
			dc.SetHostClaude(true, false, nil)
			dc.SetSandboxAvailable(true)
			dc.SetCheckedOutRepos([]string{"workspace"})
			m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: tc.runtime, OwnerID: privAcctA, LaunchGeneration: newUUID(), Digest: "selection", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
			// Observe committed allocations even if a later refusal deletes the row.
			// A final absence check alone would miss mint-and-revoke side effects.
			if _, err := f.pool.Exec(t.Context(), `CREATE TABLE release_launch_allocations (session_id text NOT NULL);
    CREATE FUNCTION record_release_launch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO release_launch_allocations VALUES (NEW.id::text); RETURN NEW; END $$;
    CREATE TRIGGER record_release_launch AFTER INSERT ON sessions FOR EACH ROW EXECUTE FUNCTION record_release_launch()`); err != nil {
				t.Fatal(err)
			}
			sid := newUUID()
			problem := f.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Workspace: &m, Repo: "workspace", DaemonID: dc.ID, Runtime: tc.wire, Engine: "claude"}, RunnerPrincipal{Kind: "human", Sub: privAcctA, Sid: testSID, Caps: []string{"session.start"}}, sid)
			if problem == nil {
				t.Fatal("disabled runtime dispatched")
			}
			var allocations int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM release_launch_allocations WHERE session_id=$1`, sid).Scan(&allocations); err != nil || allocations != 0 {
				t.Fatal("disabled runtime allocated then revoked session", allocations, err)
			}
			row, err := db.GetSession(t.Context(), f.pool, sid)
			if err != nil || row != nil {
				t.Fatal("disabled runtime allocated session", err)
			}
			select {
			case <-dc.send:
				t.Fatal("disabled runtime sent a command")
			default:
			}
		})
	}
}
