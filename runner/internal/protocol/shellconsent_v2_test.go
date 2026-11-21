package protocol

import "testing"

func TestPersistentConsentPinsOneOriginalLoginAndChild(t *testing.T) {
	c := ShellCustody{Owner: "owner", Login: "login", Session: "session", Launch: "launch", Runtime: "host", RuntimeGeneration: 1, Root: WorkspaceRoot{Device: 1, Inode: 2}, Manifest: "manifest", Identity: "identity", Desktop: "desktop", Credential: "credential", OwnershipRevision: 1, Runner: "runner", Daemon: "daemon", Consent: "consent", Shell: "shell", Nonce: "nonce", Created: 1, LifetimeUntil: 2}
	consent := NewShellConsentV2(HostShellConsent{ID: "consent", ChallengeID: "challenge"}, c, 100)
	if !consent.Allows(c) {
		t.Fatal("original pins did not match")
	}
	for _, change := range []func(*ShellCustody){func(c *ShellCustody) { c.Login = "other" }, func(c *ShellCustody) { c.Credential = "new" }, func(c *ShellCustody) { c.Root.Inode++ }, func(c *ShellCustody) { c.Identity = "new" }, func(c *ShellCustody) { c.Daemon = "new" }, func(c *ShellCustody) { c.Runner = "new" }, func(c *ShellCustody) { c.Consent = "other" }} {
		bad := c
		change(&bad)
		if consent.Allows(bad) {
			t.Fatal("changed pins authorized")
		}
	}
}
