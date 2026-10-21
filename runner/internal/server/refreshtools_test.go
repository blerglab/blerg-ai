package server

import (
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestHubRefreshSessionToolsRoutesToTheDaemon(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "d1", send: make(chan []byte, 8)}
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	h.Register(dc)
	h.SetSessionOwner("s1", "d1")
	if !h.RefreshSessionTools("s1") {
		t.Fatal("expected the message to be queued")
	}
	select {
	case raw := <-dc.send:
		var msg protocol.RefreshSessionTools
		if err := json.Unmarshal(raw, &msg); err != nil || msg.Type != "refresh_session_tools" || msg.SessionID != "s1" {
			t.Fatalf("sent = %s err=%v", raw, err)
		}
	default:
		t.Fatal("nothing sent")
	}
	if h.RefreshSessionTools("nope") {
		t.Fatal("an unknown session has no daemon")
	}
}
