package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func validShellCustody() ShellCustody {
	return ShellCustody{Container: "container", Version: 2, Owner: "owner", Login: "login", Session: "session", Launch: "launch", Runtime: "sandbox", RuntimeGeneration: 1, Root: WorkspaceRoot{Device: 1, Inode: 2}, Manifest: "manifest", Desktop: "desktop", Credential: "credential", OwnershipRevision: 1, Runner: "runner-instance", Daemon: "daemon-instance", Shell: "shell", Nonce: "custody-nonce", Identity: "identity-digest", Created: 1000, LifetimeUntil: 1000 + int64(7*24*time.Hour/time.Second)}
}

func TestShellCustodyDigestPinsThePhysicalAllocation(t *testing.T) {
	c := validShellCustody()
	if c.Validate() != nil || c.Digest() == "" {
		t.Fatal("valid custody refused")
	}
	for _, field := range []string{"owner", "login", "session", "launch", "runtime", "generation", "root", "manifest", "desktop", "credential", "revision", "runner", "daemon", "shell", "nonce", "identity", "lifetime", "consent"} {
		t.Run(field, func(t *testing.T) {
			next := c
			switch field {
			case "owner":
				next.Owner = "other"
			case "login":
				next.Login = "other"
			case "session":
				next.Session = "other"
			case "launch":
				next.Launch = "other"
			case "runtime":
				next.Runtime = "host"
				next.Consent = "consent"
			case "generation":
				next.RuntimeGeneration++
			case "root":
				next.Root.Inode++
			case "manifest":
				next.Manifest = "other"
			case "desktop":
				next.Desktop = "other"
			case "credential":
				next.Credential = "other"
			case "revision":
				next.OwnershipRevision++
			case "runner":
				next.Runner = "other"
			case "daemon":
				next.Daemon = "other"
			case "shell":
				next.Shell = "other"
			case "nonce":
				next.Nonce = "other"
			case "identity":
				next.Identity = "other"
			case "lifetime":
				next.LifetimeUntil--
			case "consent":
				next.Consent = "other"
			}
			if next.Digest() == c.Digest() {
				t.Fatal("changed allocation retained digest")
			}
		})
	}
}

func TestShellCustodyRefusesUnprovenOrClusterPersistence(t *testing.T) {
	for _, field := range []string{"version", "credential", "root", "login", "deadline", "cluster", "host-consent", "daemon", "runner"} {
		t.Run(field, func(t *testing.T) {
			c := validShellCustody()
			switch field {
			case "version":
				c.Version = 1
			case "credential":
				c.Credential = ""
			case "root":
				c.Root.Inode = 0
			case "login":
				c.Login = ""
			case "deadline":
				c.LifetimeUntil = c.Created
			case "cluster":
				c.Runtime = "cluster"
			case "host-consent":
				c.Runtime = "host"
			case "daemon":
				c.Daemon = ""
			case "runner":
				c.Runner = ""
			}
			if c.Validate() == nil || c.Digest() != "" {
				t.Fatal("unproven custody accepted")
			}
		})
	}
}

func TestShellPhysicalScopeDoesNotForgetRestartedDaemon(t *testing.T) {
	old := validShellCustody()
	next := old
	next.Daemon = "new-daemon"
	next.Runner = "new-runner"
	next.Nonce = "new-custody"
	next.Shell = "new-shell"
	next.Login = "new-login"
	next.Consent = "new-consent"
	if old.PhysicalScope() != next.PhysicalScope() || old.Digest() == next.Digest() {
		t.Fatal("restart hid old physical cleanup obligations")
	}
	next.RuntimeGeneration++
	if old.PhysicalScope() == next.PhysicalScope() {
		t.Fatal("different physical runtime shared scope")
	}
}

func TestHostShellCustodyRejectsLifetimeBeyondDesktopLimit(t *testing.T) {
	c := validShellCustody()
	c.Runtime = "host"
	c.Consent = "consent"
	c.LifetimeUntil = c.Created + int64(DesktopShellLifetime/time.Second) + 1
	if c.Validate() == nil {
		t.Fatal("desktop custody exceeded seven-day maximum")
	}
}

func TestShellCustodyPinsContainerIdentity(t *testing.T) {
	c := validShellCustody()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["container_identity"] = "different-container"
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var changed ShellCustody
	if err = json.Unmarshal(raw, &changed); err != nil {
		t.Fatal(err)
	}
	if changed.Digest() == c.Digest() || changed.PhysicalScope() == c.PhysicalScope() {
		t.Fatal("container identity omitted from custody or physical scope")
	}
}
