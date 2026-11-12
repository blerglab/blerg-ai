package server

import (
	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"testing"
	"time"
)

func TestPersonalShellWelcomeRequiresLiveManagerAndEnrollment(t *testing.T) {
	h := NewHub()
	hello := &protocol.ShellLinkHello{Version: 2, Daemon: "daemon", Challenge: "challenge"}
	enrollment := &desktopauth.Binding{DesktopID: "desktop", OwnerID: "owner", TokenID: "credential", Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}
	if h.personalShellWelcome(hello, enrollment) != nil {
		t.Fatal("welcome without manager")
	}
	h.shellCustodian = "manager"
	if h.personalShellWelcome(hello, nil) != nil {
		t.Fatal("compatibility token received custody authority")
	}
	welcome := h.personalShellWelcome(hello, enrollment)
	if welcome == nil || !welcome.Matches(*hello, time.Now()) || welcome.Runner != "manager" {
		t.Fatal("verified enrollment was not correlated")
	}
	enrollment.ExpiresAt = time.Now().Add(-time.Second)
	if h.personalShellWelcome(hello, enrollment) != nil {
		t.Fatal("expired enrollment accepted")
	}
}
