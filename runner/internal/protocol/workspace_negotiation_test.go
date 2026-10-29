package protocol

import "testing"

func TestWorkspaceCapabilitiesNegotiateExactVersions(t *testing.T) {
	cases := []struct{ peer, want WorkspaceCapabilities }{
		{WorkspaceCapabilities{}, WorkspaceCapabilities{}},
		{WorkspaceCapabilities{ManifestVersion: 1, ShellVersion: 1}, WorkspaceCapabilities{}},
		{WorkspaceCapabilities{ManifestVersion: 3, ShellVersion: 1}, WorkspaceCapabilities{}},
		{WorkspaceCapabilities{ManifestVersion: 2}, WorkspaceCapabilities{}},
		{WorkspaceCapabilities{ManifestVersion: 2, LinuxHost: true}, WorkspaceCapabilities{ManifestVersion: 2, LinuxHost: true}},
		{WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 3, LinuxHost: true}, WorkspaceCapabilities{ManifestVersion: 2, LinuxHost: true}},
		{WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true}, WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true}},
	}
	local := WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true}
	for _, c := range cases {
		if got := NegotiateWorkspaceCapabilities(local, c.peer); got != c.want {
			t.Fatalf("peer=%+v got=%+v want=%+v", c.peer, got, c.want)
		}
	}
	if got := NegotiateWorkspaceCapabilities(WorkspaceCapabilities{}, local); got != (WorkspaceCapabilities{}) {
		t.Fatal("legacy local endpoint upgraded", got)
	}
}
