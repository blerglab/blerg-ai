package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// sidebarHubFixture is a hub with one workstation daemon, one cluster pod
// (daemon mode "runner") and a browser socket attached.
func sidebarHubFixture(t *testing.T) (*Hub, *DaemonConn, *DaemonConn, *BrowserConn) {
	t.Helper()
	hub := NewHub()
	ws := &DaemonConn{ID: "00000000-0000-4000-8000-0000000000b1", Name: "desk", Mode: "local", ReposRoot: "/r", send: make(chan []byte, 4)}
	ws.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	pod := &DaemonConn{ID: "runner-abc", Name: "runner-abc", Mode: "runner", send: make(chan []byte, 4)}
	pod.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	pool := connectSrvTestDB(t)
	NewAPI(hub, pool, "", nil, "")
	fixtureOwnDesktop(t, pool, ws, "user-1")
	hub.Register(ws)
	hub.Register(pod)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bc := &BrowserConn{ID: "b1", AccountID: "user-1", send: make(chan []byte, 16), ctx: ctx, cancel: cancel}
	hub.RegisterBrowser(bc)
	return hub, ws, pod, bc
}

func TestInitialStateOmitsClusterPods(t *testing.T) {
	hub, _, _, bc := sidebarHubFixture(t)
	sendInitialState(context.Background(), bc, hub, nil)

	var st protocol.InitialState
	if err := json.Unmarshal(<-bc.send, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Daemons) != 1 || st.Daemons[0].ID != "00000000-0000-4000-8000-0000000000b1" {
		t.Fatalf("initial_state daemons = %+v, want only the workstation daemon", st.Daemons)
	}
}

// A cluster pod's connection is not a machine the owner picks: its drop is
// not announced as a daemon going away (the sessions' own status changes
// are), while a workstation daemon's is.
func TestDaemonDisconnectBroadcastSkipsClusterPods(t *testing.T) {
	hub, ws, pod, bc := sidebarHubFixture(t)

	handleDaemonDisconnect(hub, pod, nil)
	select {
	case m := <-bc.send:
		t.Fatalf("a pod disconnect was broadcast to browsers: %s", m)
	case <-time.After(100 * time.Millisecond):
	}

	handleDaemonDisconnect(hub, ws, nil)
	select {
	case m := <-bc.send:
		if !strings.Contains(string(m), `"type":"daemon_disconnected"`) || !strings.Contains(string(m), `"00000000-0000-4000-8000-0000000000b1"`) {
			t.Fatalf("workstation disconnect broadcast = %s", m)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("a workstation daemon disconnect was not broadcast")
	}
	for _, id := range []string{"runner-abc", "00000000-0000-4000-8000-0000000000b1"} {
		for _, d := range hub.GetAllDaemons() {
			if d.ID == id {
				t.Errorf("daemon %s still registered after its disconnect", id)
			}
		}
	}
}
