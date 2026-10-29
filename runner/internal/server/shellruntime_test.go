package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testShellOutput builds an output entry as remoteShell.Open does.
func testShellOutput(dc *DaemonConn, session string) (shellOutput, <-chan []byte) {
	return newShellOutput(dc, session, "shell")
}

// received reports whether out yields a frame within a short wait.
func received(out <-chan []byte) ([]byte, bool) {
	select {
	case data, ok := <-out:
		return data, ok
	case <-time.After(100 * time.Millisecond):
		return nil, false
	}
}

func TestShellRuntimeOutputNeverBroadcastOrCrossConnection(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	hub.Register(dc)
	hub.SetSessionOwner("session", dc.ID)
	agent := make(chan []byte, 4)
	hub.Subscribe("session", "agent-history-view", agent)
	entry, out := testShellOutput(dc, "session")
	hub.shellRPC.outputs = map[string]shellOutput{"shell": entry}
	frame := shellruntime.RPC{Version: 1, Type: "owner_shell_output", SessionID: "session", Shell: "shell", Data: []byte("private-terminal-payload")}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !handleDaemonFrame(t.Context(), hub, dc, nil, raw, envelope{Type: frame.Type, SessionID: frame.SessionID}) {
		t.Fatal("dedicated output rejected")
	}
	if data, ok := received(out); !ok {
		t.Fatal("output did not reach its private channel")
	} else if string(data) != "private-terminal-payload" {
		t.Fatal("output changed")
	}
	select {
	case <-agent:
		t.Fatal("shell payload reached agent/history broadcast")
	default:
	}
	replacement := &DaemonConn{BoundSessionID: "session", ID: dc.ID, send: make(chan []byte, 8)}
	hub.Register(replacement)
	hub.deliverShell(dc, frame)
	if _, ok := received(out); ok {
		t.Fatal("stale authenticated connection forwarded payload")
	}
	hub.SetSessionOwner("foreign", replacement.ID)
	foreignEntry, foreign := testShellOutput(replacement, "foreign")
	hub.shellRPC.outputs["foreign-shell"] = foreignEntry
	frame.Shell = "foreign-shell"
	frame.SessionID = "foreign"
	hub.deliverShell(replacement, frame)
	if _, ok := received(foreign); ok {
		t.Fatal("pod forged a foreign session on its bound connection")
	}
	if _, ok := received(out); ok {
		t.Fatal("cross-session shell payload forwarded")
	}
}

func TestShellCallPreservesOnlySafeRefusal(t *testing.T) {
	for _, code := range []string{"runtime_consent_refused", "runtime_cleanup_refused", "private-upstream-text"} {
		t.Run(code, func(t *testing.T) {
			hub := NewHub()
			dc := &DaemonConn{ID: "desktop", send: make(chan []byte, 8), shellControl: make(chan []byte, 1)}
			hub.Register(dc)
			hub.SetSessionOwner("session", dc.ID)
			done := make(chan error, 1)
			go func() {
				_, err := hub.shellCall(t.Context(), dc, shellruntime.RPC{SessionID: "session", Op: "configure"})
				done <- err
			}()
			var req shellruntime.RPC
			if err := json.Unmarshal(<-dc.shellControl, &req); err != nil {
				t.Fatal(err)
			}
			req.Type, req.ErrorCode = "owner_shell_result", code
			hub.deliverShell(dc, req)
			err := <-done
			want := ownershell.Reason(code)
			if code == "private-upstream-text" {
				want = ownershell.ReasonUnavailable
			}
			if ownershell.RefusalReason(err) != want {
				t.Fatalf("runtime code: %q", ownershell.RefusalReason(err))
			}
			w := httptest.NewRecorder()
			shellError(w, err)
			if !strings.Contains(w.Body.String(), string(want)) || strings.Contains(w.Body.String(), "private-upstream-text") {
				t.Fatal("unsafe or missing browser reason", w.Body.String())
			}
		})
	}
}

// The daemon reader never waits on shell output. A burst of small frames is
// queued (bounded) and reaches the manager complete, in order and coalesced; a
// runtime that ignores its pacing past the bound has its stream ended.
func TestShellOutputBurstQueuesBoundedAndCoalesced(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	hub.Register(dc)
	hub.SetSessionOwner("session", dc.ID)
	entry, out := testShellOutput(dc, "session")
	hub.shellRPC.outputs = map[string]shellOutput{"shell": entry}
	var want []byte
	for n := range 600 {
		line := []byte("ls: cannot access missing-" + strings.Repeat("x", n%7) + "\n")
		want = append(want, line...)
		hub.deliverShell(dc, shellruntime.RPC{Version: 1, Type: "owner_shell_output", SessionID: "session", Shell: "shell", Data: line})
	}
	var got []byte
	frames := 0
	for len(got) < len(want) {
		data, ok := received(out)
		if !ok {
			t.Fatalf("stream ended after %d of %d bytes", len(got), len(want))
		}
		if len(data) > ownershell.MaxFrame {
			t.Fatal("frame over MaxFrame")
		}
		frames++
		got = append(got, data...)
	}
	if string(got) != string(want) {
		t.Fatal("output reordered or changed")
	}
	if frames >= 600 {
		t.Fatalf("%d frames for 600 queued writes: nothing coalesced", frames)
	}
	chunk := make([]byte, ownershell.MaxFrame)
	// The pump may hold one frame beyond the queue while it waits on the manager.
	for range remoteOutputQueue/ownershell.MaxFrame + 2 {
		hub.deliverShell(dc, shellruntime.RPC{Version: 1, Type: "owner_shell_output", SessionID: "session", Shell: "shell", Data: chunk})
	}
	hub.shellRPC.mu.Lock()
	_, kept := hub.shellRPC.outputs["shell"]
	hub.shellRPC.mu.Unlock()
	if kept {
		t.Fatal("output past the bound was kept")
	}
	total := 0
	for {
		data, ok := <-out
		if !ok {
			break
		}
		total += len(data)
	}
	if total > remoteOutputQueue+ownershell.MaxFrame {
		t.Fatalf("%d bytes queued past the %d byte bound", total, remoteOutputQueue+ownershell.MaxFrame)
	}
}
