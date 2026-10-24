package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestActualDaemonPumpPrioritizesRevocationOverInputFlood(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	hub := NewHub()
	dc := &DaemonConn{ID: "private-daemon", send: make(chan []byte, 8), shellControl: make(chan []byte, 32), shellData: make(chan []byte, 16)}
	hub.Register(dc)
	hub.SetSessionOwner("session", dc.ID)
	var wg sync.WaitGroup
	call := func(op string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := hub.shellCall(ctx, dc, shellruntime.RPC{SessionID: "session", Op: op}); err != nil {
				t.Error(err)
			}
		}()
	}
	for n := 0; n < 12; n++ {
		if n%2 == 0 {
			call("input")
		} else {
			call("resize")
		}
	}
	waitQueued := func(count int) {
		t.Helper()
		for len(dc.shellControl)+len(dc.shellData) < count {
			select {
			case <-ctx.Done():
				t.Fatal("RPCs not queued")
			default:
				runtime.Gosched()
			}
		}
	}
	waitQueued(12)
	call("close_launch")
	call("renew")
	waitQueued(14)
	dc.send <- []byte(`{"type":"workspace_file_request","session_id":"session"}`)
	dc.send <- []byte(`{"type":"agent_message","session_id":"session"}`)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer close(done)
		daemonWritePump(ctx, dc, conn, nil)
	}))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var order []string
	for range 16 {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var frame shellruntime.RPC
		if err = json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == "owner_shell_request" {
			order = append(order, frame.Op)
			frame.Type = "owner_shell_result"
			frame.OK = true
			hub.deliverShell(dc, frame)
		} else {
			order = append(order, frame.Type)
		}
	}
	wg.Wait()
	cancel()
	<-done
	if (order[0] != "close_launch" && order[0] != "renew") || (order[1] != "close_launch" && order[1] != "renew") {
		t.Fatalf("input FIFO delayed cleanup/renewal: %v", order)
	}
	file, agent, data := 99, 99, 99
	for i, kind := range order {
		switch kind {
		case "workspace_file_request":
			file = i
		case "agent_message":
			agent = i
		case "input", "resize":
			data = min(data, i)
		}
	}
	if file > 6 || agent > 6 || data > 3 {
		t.Fatalf("shell/file/agent lane starved: %v", order)
	}
}
