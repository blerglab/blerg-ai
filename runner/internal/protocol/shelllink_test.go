package protocol

import (
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
)

func TestShellLinkWelcomeRequiresCorrelatedPersonalEnrollment(t *testing.T) {
	hello := ShellLinkHello{Version: 2, Daemon: "daemon", Challenge: "fresh-challenge"}
	welcome := ShellLinkWelcome{Version: 2, Runner: "runner", Daemon: hello.Daemon, Challenge: hello.Challenge, Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: "owner", TokenID: "credential", Revision: 1, ExpiresAt: time.Unix(2000, 0)}}
	if !welcome.Matches(hello, time.Unix(1000, 0)) {
		t.Fatal("fresh enrolled welcome refused")
	}
	for _, field := range []string{"version", "runner", "daemon", "challenge", "desktop", "owner", "credential", "revision", "expiry"} {
		t.Run(field, func(t *testing.T) {
			next := welcome
			switch field {
			case "version":
				next.Version = 1
			case "runner":
				next.Runner = ""
			case "daemon":
				next.Daemon = "other"
			case "challenge":
				next.Challenge = "stale"
			case "desktop":
				next.Desktop.DesktopID = ""
			case "owner":
				next.Desktop.OwnerID = ""
			case "credential":
				next.Desktop.TokenID = ""
			case "revision":
				next.Desktop.Revision = 0
			case "expiry":
				next.Desktop.ExpiresAt = time.Unix(999, 0)
			}
			if next.Matches(hello, time.Unix(1000, 0)) {
				t.Fatal("unverified link gained custody authority")
			}
		})
	}
}

func TestShellLinkTransportChangesCannotHideAuthorityReplacement(t *testing.T) {
	previous := ShellLinkWelcome{Version: 2, Runner: "runner", Daemon: "daemon", Challenge: "old", Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: "owner", TokenID: "credential", Revision: 1, ExpiresAt: time.Unix(2000, 0)}}
	next := previous
	next.Challenge = "fresh"
	if !next.SameCustodian(previous) {
		t.Fatal("transport nonce replaced process custody")
	}
	for _, field := range []string{"runner", "daemon", "credential", "revision", "owner", "desktop"} {
		t.Run(field, func(t *testing.T) {
			changed := next
			switch field {
			case "runner":
				changed.Runner = "other"
			case "daemon":
				changed.Daemon = "other"
			case "credential":
				changed.Desktop.TokenID = "other"
			case "revision":
				changed.Desktop.Revision++
			case "owner":
				changed.Desktop.OwnerID = "other"
			case "desktop":
				changed.Desktop.DesktopID = "other"
			}
			if changed.SameCustodian(previous) {
				t.Fatal("authority replacement retained custody")
			}
		})
	}
}

func TestShellPersistenceNeedsIndependentExactNegotiation(t *testing.T) {
	full := WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true, Sandbox: true, Cluster: true, ShellPersistence: 2, PersistentHost: true, PersistentSandbox: true}
	if !full.SupportsPersistence("host") || full.SupportsPersistence("sandbox") || full.SupportsPersistence("cluster") {
		t.Fatal("desktop persistence policy")
	}
	legacy := full
	legacy.ShellPersistence = 0
	if NegotiateWorkspaceCapabilities(full, legacy).SupportsPersistence("host") {
		t.Fatal("legacy peer silently promoted")
	}
	for _, runtime := range []string{"host"} {
		caps := NegotiateWorkspaceCapabilities(full, full)
		if !caps.SupportsPersistence(runtime) {
			t.Fatal("affirmative negotiation failed")
		}
	}
	incompatible := full
	incompatible.ShellPersistence = 3
	if NegotiateWorkspaceCapabilities(full, incompatible).SupportsPersistence("sandbox") {
		t.Fatal("unknown version accepted")
	}
	clusterOnly := full
	clusterOnly.LinuxHost = false
	clusterOnly.Sandbox = false
	if clusterOnly.SupportsPersistence("cluster") || NegotiateWorkspaceCapabilities(full, clusterOnly).ShellPersistence != 0 {
		t.Fatal("cluster gained desktop lifetime")
	}
}
