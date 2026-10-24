package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceServerNegotiatesBothHopVersions(t *testing.T) {
	for _, peer := range []protocol.WorkspaceCapabilities{{}, {ManifestVersion: 1, ShellVersion: 1}, {ManifestVersion: 3, ShellVersion: 1}, {ManifestVersion: 2}, {ManifestVersion: 2, ShellVersion: 1}, {ManifestVersion: 2, ShellVersion: 3}} {
		hub := NewHub()
		// A ready transport is still unavailable in an ordinary gated build.
		hub.runtimeShellReady.Store(true)
		conn := dialDaemon(t, hub)
		if err := conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: "negotiation-desktop", Token: "tok-1234567890", ProtocolVersion: "1", Welcome: true, WorkspaceCapabilities: peer}); err != nil {
			t.Fatal(err)
		}
		var welcome protocol.DaemonWelcome
		if err := json.Unmarshal(readFirstMessage(t, conn), &welcome); err != nil {
			t.Fatal(err)
		}
		want := protocol.WorkspaceCapabilities{}
		if protocol.RuntimeGateEnabled(protocol.WorkspaceRuntimeGateVerified) {
			if peer.ManifestVersion == 2 {
				want.ManifestVersion = 2
				if peer.ShellVersion == 1 {
					want.ShellVersion = 1
				}
			}
		}
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
	f := newPrivFixture(t)
	dc := f.dc
	dc.WorkspaceCapabilities = protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1}
	dc.SetAvailableEngines([]string{"claude"})
	dc.SetHostClaude(true, false, nil)
	dc.SetCheckedOutRepos([]string{"workspace"})
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "host", OwnerID: privAcctA, LaunchGeneration: newUUID(), Digest: "trusted-selection", Members: []protocol.WorkspaceMember{
		{ID: "one", Name: "one", Path: "one", SourceIdentity: "source-one", CommonDirectoryIdentity: "common-one", Destination: "source-one"},
		{ID: "two", Name: "two", Path: "two", SourceIdentity: "source-two", CommonDirectoryIdentity: "common-two", Destination: "source-two"},
	}}
	request := runnerStartRequest{Workspace: &m, Repo: "workspace", DaemonID: dc.ID, Runtime: "daemon", Engine: "claude", Title: "Selected workspace"}
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
}
