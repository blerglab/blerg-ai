package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"testing"
)

func TestShellRuntimeOutputNeverBroadcastOrCrossConnection(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	hub.Register(dc)
	hub.SetSessionOwner("session", dc.ID)
	agent := make(chan []byte, 4)
	hub.Subscribe("session", "agent-history-view", agent)
	out := make(chan []byte, 4)
	hub.shellRPC.outputs = map[string]shellOutput{"shell": {dc: dc, session: "session", out: out}}
	frame := shellruntime.RPC{Version: 1, Type: "owner_shell_output", SessionID: "session", Shell: "shell", Data: []byte("private-terminal-payload")}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !handleDaemonFrame(t.Context(), hub, dc, nil, raw, envelope{Type: frame.Type, SessionID: frame.SessionID}) {
		t.Fatal("dedicated output rejected")
	}
	select {
	case data := <-out:
		if string(data) != "private-terminal-payload" {
			t.Fatal("output changed")
		}
	default:
		t.Fatal("output did not reach its private channel")
	}
	select {
	case <-agent:
		t.Fatal("shell payload reached agent/history broadcast")
	default:
	}
	replacement := &DaemonConn{BoundSessionID: "session", ID: dc.ID, send: make(chan []byte, 8)}
	hub.Register(replacement)
	hub.deliverShell(dc, frame)
	select {
	case <-out:
		t.Fatal("stale authenticated connection forwarded payload")
	default:
	}
	hub.SetSessionOwner("foreign", replacement.ID)
	foreign := make(chan []byte, 1)
	hub.shellRPC.outputs["foreign-shell"] = shellOutput{dc: replacement, session: "foreign", out: foreign}
	frame.Shell = "foreign-shell"
	frame.SessionID = "foreign"
	hub.deliverShell(replacement, frame)
	select {
	case <-foreign:
		t.Fatal("pod forged a foreign session on its bound connection")
	default:
	}
	select {
	case <-out:
		t.Fatal("cross-session shell payload forwarded")
	default:
	}
}
