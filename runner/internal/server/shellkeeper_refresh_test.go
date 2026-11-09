package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestShellKeeperRefreshOnlyChangesEnrolledDesktopShellAuthority(t *testing.T) {
	for _, name := range []string{"enrolled", "unenrolled", "expired", "malformed", "oversized"} {
		t.Run(name, func(t *testing.T) {
			h := NewHub()
			h.shellCustodian = "manager"
			old := &protocol.ShellLinkWelcome{Version: 2, Daemon: "old", Challenge: "old"}
			dc := &DaemonConn{ID: "desktop", ShellLink: old, send: make(chan []byte, 1), DesktopBinding: &desktopauth.Binding{DesktopID: "desktop", OwnerID: "owner", TokenID: "credential", Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}}
			h.daemons[dc.ID] = dc
			if name == "unenrolled" {
				dc.DesktopBinding = nil
			}
			if name == "expired" {
				dc.DesktopBinding.ExpiresAt = time.Now().Add(-time.Hour)
			}
			hello := &protocol.ShellLinkHello{Version: 2, Daemon: "new-keeper", Challenge: "new-challenge"}
			raw, _ := json.Marshal(struct {
				Hello *protocol.ShellLinkHello `json:"shell_hello"`
			}{hello})
			if name == "malformed" {
				raw = []byte(`{"shell_hello":`)
			}
			if name == "oversized" {
				raw = make([]byte, 4097)
			}
			if !handleDaemonFrame(context.Background(), h, dc, nil, raw, envelope{Type: "daemon_shell_hello"}) {
				t.Fatal("shell refresh closed agent connection")
			}
			if name != "enrolled" {
				if dc.shellLink() != old {
					t.Fatal("unproven peer changed shell authority")
				}
				select {
				case <-dc.send:
					t.Fatal("unproven peer received shell authority")
				default:
				}
				return
			}
			if dc.ShellLink != old || dc.shellLink() == old || !dc.shellLink().Matches(*hello, time.Now()) {
				t.Fatal("shell-only authority refresh failed")
			}
			select {
			case reply := <-dc.send:
				var r struct {
					Type  string
					Hello protocol.ShellLinkHello    `json:"shell_hello"`
					Link  *protocol.ShellLinkWelcome `json:"shell_link"`
				}
				if json.Unmarshal(reply, &r) != nil || r.Type != "daemon_shell_welcome" || r.Hello != *hello || r.Link == nil || !r.Link.Matches(*hello, time.Now()) {
					t.Fatal("uncorrelated shell refresh")
				}
			default:
				t.Fatal("shell refresh did not reply")
			}
		})
	}
}
