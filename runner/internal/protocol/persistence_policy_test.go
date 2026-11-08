package protocol

import "testing"

func TestSandboxPersistenceFailsClosedEvenWhenPeersClaimIt(t *testing.T) {
	c := WorkspaceCapabilities{ManifestVersion: WorkspaceVersion, ShellVersion: 1, Sandbox: true, ShellPersistence: 2, PersistentSandbox: true}
	if c.SupportsPersistence("sandbox") || NegotiateWorkspaceCapabilities(c, c).PersistentSandbox {
		t.Fatal("sandbox v2 persistence enabled")
	}
	if !c.Supports("sandbox", true) || !NegotiateWorkspaceCapabilities(c, c).Supports("sandbox", true) {
		t.Fatal("sandbox v1 shells changed")
	}
}
