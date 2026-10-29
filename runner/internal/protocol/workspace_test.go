package protocol

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func testManifest() WorkspaceManifest {
	return WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "host", LaunchGeneration: "launch", Root: WorkspaceRoot{1, 2}, Digest: "digest", Members: []WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Project: "org/one", Destination: "example-org/one"}}}
}
func TestWorkspaceRoundTrip(t *testing.T) {
	m := testManifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(SpawnSession{Workspace: &m})
	if err != nil {
		t.Fatal(err)
	}
	var got SpawnSession
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, *got.Workspace) {
		t.Fatal("manifest changed")
	}
	grant := ShellGrant{Version: 1, Audience: "runtime", OwnerID: "owner", LoginID: "login", SessionID: "session", LaunchGeneration: "launch", ConnectionID: "conn", ShellGeneration: "shell", Runtime: "host", ConsentID: "consent", PolicyRevision: 3, IssuedAt: 1, ExpiresAt: 30}
	raw, _ = json.Marshal(grant)
	var out ShellGrant
	_ = json.Unmarshal(raw, &out)
	if out != grant {
		t.Fatal("grant changed")
	}
}
func TestWorkspaceLegacyPeers(t *testing.T) {
	m := testManifest()
	if (WorkspaceCapabilities{}).Require(m, false) == nil {
		t.Fatal("legacy accepted folder")
	}
	m.Kind = "repo"
	if err := (WorkspaceCapabilities{}).Require(m, false); err == nil {
		t.Fatal("legacy accepted workspace manifest")
	}
	if (WorkspaceCapabilities{}).Require(m, true) == nil {
		t.Fatal("legacy accepted shell")
	}
	if err := (WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true}).Require(m, true); err != nil {
		t.Fatal(err)
	}
}
func TestWorkspaceChildAndProvenance(t *testing.T) {
	p := testManifest()
	c := testManifest()
	c.LaunchGeneration = "child"
	if err := ValidateWorkspaceChild(p, c); err != nil {
		t.Fatal(err)
	}
	c.Members[0].Project = "org/other"
	if ValidateWorkspaceChild(p, c) == nil {
		t.Fatal("child escalated project")
	}
	c = testManifest()
	c.LaunchGeneration = "child"
	c.Members[0].Destination = "other-org/one"
	if ValidateWorkspaceChild(p, c) == nil {
		t.Fatal("child changed authorized destination")
	}
	c = testManifest()
	c.LaunchGeneration = "child"
	c.Runtime = "sandbox"
	if ValidateWorkspaceChild(p, c) == nil {
		t.Fatal("child changed runtime")
	}
	if p.ValidateResults([]WorkspaceMemberResult{{WorkspaceMemberRef: WorkspaceMemberRef{"one", "old"}}}) == nil {
		t.Fatal("stale result accepted")
	}
	if p.ValidateResults([]WorkspaceMemberResult{{WorkspaceMemberRef: WorkspaceMemberRef{"other", "launch"}}}) == nil {
		t.Fatal("unknown member accepted")
	}
}
func TestWorkspaceRejectDuplicateAndEscape(t *testing.T) {
	m := testManifest()
	m.Members = append(m.Members, m.Members[0])
	if m.Validate() == nil {
		t.Fatal("duplicate accepted")
	}
	m = testManifest()
	m.Members[0].Path = "../escape"
	if m.Validate() == nil {
		t.Fatal("escape accepted")
	}
}

func TestWorkspaceProjectIsolation(t *testing.T) {
	m := testManifest()
	for _, tc := range []struct {
		member, launch, project string
		want                    bool
	}{
		{"one", "launch", "org/one", true},
		{"one", "old", "org/one", false},
		{"other", "launch", "org/one", false},
		{"one", "launch", "org/other", false},
		{"", "launch", "org/one", false},
	} {
		if m.AuthorizesProject(WorkspaceMemberRef{tc.member, tc.launch}, tc.project) != tc.want {
			t.Fatal("project binding mismatch", tc)
		}
	}
}

func TestWorkspaceResultRoundTrip(t *testing.T) {
	in := SessionEnded{WorkspaceResults: []WorkspaceMemberResult{{WorkspaceMemberRef: WorkspaceMemberRef{"one", "launch"}, Base: "base", Head: "head", Branch: "wip/session", SyncError: "sync", PushError: "push", Pending: true, RecoveryLocation: "recovery"}}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out SessionEnded
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatal("results changed")
	}
}

func TestShellPolicyConsentRoundTrip(t *testing.T) {
	in := struct {
		Policy  ShellPolicy
		Consent HostShellConsent
	}{
		Policy:  ShellPolicy{Version: 1, Revision: 2, Enabled: true},
		Consent: HostShellConsent{ID: "consent", OwnerID: "owner", LaunchGeneration: "launch", MachineID: "machine", Account: "account", UID: 42, Shell: "/bin/sh", Cwd: "/workspace", ChallengeID: "challenge"},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out := in
	out.Policy = ShellPolicy{}
	out.Consent = HostShellConsent{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatal("policy/consent changed")
	}
}

func TestLegacyHandshakeOmitsWorkspaceCapabilities(t *testing.T) {
	for _, v := range []any{DaemonHello{}, DaemonWelcome{}, SpawnSession{}} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("workspace_capabilities")) {
			t.Fatal("zero capabilities changed legacy handshake")
		}
	}
}

func TestWorkspaceRejectsLayoutChanges(t *testing.T) {
	parent := testManifest()
	parent.Layout = WorkspaceLayoutStructure
	child := parent
	child.LaunchGeneration = "child"
	child.Layout = WorkspaceLayoutNames
	if ValidateWorkspaceChild(parent, child) == nil {
		t.Fatal("child changed parent layout")
	}
	parent.Layout = "invalid"
	if parent.Validate() == nil {
		t.Fatal("invalid layout accepted")
	}
}
