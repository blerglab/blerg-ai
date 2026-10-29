package daemon

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceNegotiationFailsClosed(t *testing.T) {
	c := NewWSClient(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c.WaitWorkspace(ctx) == nil {
		t.Fatal("unconfirmed legacy connection accepted")
	}
	c.workspaceNegotiated.Store(true)
	for _, version := range []int32{0, 1, 3} {
		c.serverWorkspaceVersion.Store(version)
		if c.WaitWorkspace(context.Background()) == nil {
			t.Fatal("unsupported peer", version)
		}
	}
	c.serverWorkspaceVersion.Store(protocol.WorkspaceVersion)
	if e := c.WaitWorkspace(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestWorkspacePayloadWaitsForNegotiatedWelcome(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	helloSeen := make(chan struct{})
	allowWelcome := make(chan struct{})
	received := make(chan string, 1)
	var premature atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, e := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer conn.Close()
		if _, _, e = conn.ReadMessage(); e != nil {
			return
		}
		_ = conn.WriteJSON(map[string]string{"type": "unconfirmed_request"})
		close(helloSeen)
		select {
		case <-allowWelcome:
		case <-ctx.Done():
			return
		}
		if e = conn.WriteJSON(protocol.DaemonWelcome{Type: "daemon_welcome", WorkspaceCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2, LinuxHost: true, Sandbox: true, Cluster: true}}); e != nil {
			return
		}
		for {
			_, raw, e := conn.ReadMessage()
			if e != nil {
				return
			}
			var msg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &msg) != nil {
				continue
			}
			if msg.Type == "daemon_heartbeat" {
				continue
			}
			received <- msg.Type
			return
		}
	}))
	defer server.Close()
	required := protocol.WorkspaceManifest{Kind: "folder", Runtime: "host"}
	client := NewWSClient(Config{ServerURL: "ws" + strings.TrimPrefix(server.URL, "http"), RequiredWorkspace: &required, WorkspaceCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2, LinuxHost: true, Sandbox: true, Cluster: true}})
	client.OnMessage("unconfirmed_request", func([]byte) { premature.Store(true) })
	connected := make(chan struct{})
	client.SetOnConnect(func() { close(connected) })
	done := make(chan struct{})
	go func() { client.Run(ctx, func() []string { return nil }); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case <-helloSeen:
	case <-ctx.Done():
		t.Fatal("hello not sent")
	}
	if client.Send(map[string]string{"type": "unconfirmed_private_fixture"}) == nil {
		t.Fatal("payload escaped before capability negotiation")
	}
	close(allowWelcome)
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("supported welcome not accepted")
	}
	if premature.Load() {
		t.Fatal("request dispatched before capability negotiation")
	}
	if e := client.Send(map[string]string{"type": "accepted_fixture"}); e != nil {
		t.Fatal(e)
	}
	select {
	case kind := <-received:
		if kind != "accepted_fixture" {
			t.Fatal("unexpected pre-negotiation payload")
		}
	case <-ctx.Done():
		t.Fatal("negotiated payload not received")
	}
}

func TestWorkspaceUnconfirmedWindowRefusesLegacyAcceptance(t *testing.T) {
	rejected := false
	required := protocol.WorkspaceManifest{Kind: "folder", Runtime: "host"}
	client := NewWSClient(Config{RequiredWorkspace: &required, OnUnsupportedWorkspace: func() { rejected = true }})
	watch := &linkWatch{c: client}
	watch.windowPassed()
	if !rejected || client.workspaceDispatchAllowed() {
		t.Fatal("unknown peer treated as accepted workspace")
	}
}
