package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

const backgroundFixtureSession = "22222222-2222-4222-8222-222222222222"

func backgroundFixtureEvent(id, kind, task string) protocol.AgentEvent {
	payload, _ := json.Marshal(map[string]string{"task_id": task, "description": "not inventory metadata", "summary": "not inventory metadata"})
	return protocol.AgentEvent{Type: "agent_event", SessionID: backgroundFixtureSession, ClientEventID: id, Kind: kind, Payload: payload}
}
func TestWSBackgroundInventoryDisconnectedAndReplay(t *testing.T) {
	c := NewWSClient(Config{})
	initial := c.backgroundTaskInventory()
	if !protocol.ValidBackgroundTaskInventory(initial) || len(initial.Sessions) != 0 {
		t.Fatal("fresh client lacks complete empty inventory")
	}
	start := backgroundFixtureEvent("start-id", "background_task_started", "task-1")
	if err := c.Send(start); err == nil {
		t.Fatal("fixture client unexpectedly connected")
	}
	if got := c.backgroundTaskInventory(); len(got.Sessions[backgroundFixtureSession]) != 1 {
		t.Fatalf("disconnected start lost: %+v", got)
	}
	done := backgroundFixtureEvent("done-id", "background_task_done", "task-1")
	_ = c.Send(done)
	raw, _ := json.Marshal(start)
	_ = c.Send(json.RawMessage(raw)) // an old start can remain pending after its done was acked
	if got := c.backgroundTaskInventory(); len(got.Sessions) != 0 {
		t.Fatalf("replay resurrected finished task: %+v", got)
	}
	start.ClientEventID = "fresh-start-id"
	_ = c.Send(start)
	got := c.backgroundTaskInventory()
	if len(got.Sessions[backgroundFixtureSession]) != 1 || got.Lifetime != initial.Lifetime {
		t.Fatal("fresh task-ID reuse must reopen within same lifetime")
	}
	got.Sessions[backgroundFixtureSession][0] = "mutated"
	if c.backgroundTaskInventory().Sessions[backgroundFixtureSession][0] != "task-1" {
		t.Fatal("snapshot aliases mutable cache")
	}
	other := NewWSClient(Config{}).backgroundTaskInventory()
	if other.Lifetime == initial.Lifetime {
		t.Fatal("new client reused prior daemon lifetime")
	}
}
func TestWSBackgroundInventoryInvalidCannotBecomeEmpty(t *testing.T) {
	for _, kind := range []string{"missing event ID", "invalid session", "invalid task", "conflicting duplicate"} {
		t.Run(kind, func(t *testing.T) {
			c := NewWSClient(Config{})
			msg := backgroundFixtureEvent("event", "background_task_started", "task")
			switch kind {
			case "missing event ID":
				msg.ClientEventID = ""
			case "invalid session":
				msg.SessionID = "not-a-session"
			case "invalid task":
				msg.Payload = json.RawMessage(`{"task_id":"bad\nID"}`)
			case "conflicting duplicate":
				_ = c.Send(msg)
				msg.Kind = "background_task_done"
			}
			_ = c.Send(msg)
			if c.backgroundTaskInventory() != nil {
				t.Fatal("invalid/incomplete inventory advertised as authoritative")
			}
		})
	}
}
func TestWSBackgroundInventoryConcurrentSnapshots(t *testing.T) {
	c := NewWSClient(Config{})
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				id := fmt.Sprintf("w%d-%d", worker, i)
				_ = c.Send(backgroundFixtureEvent(id+"-start", "background_task_started", id))
				_ = c.Send(backgroundFixtureEvent(id+"-done", "background_task_done", id))
				if snapshot := c.backgroundTaskInventory(); !protocol.ValidBackgroundTaskInventory(snapshot) {
					t.Error("concurrent snapshot malformed")
				}
			}
		}(worker)
	}
	wg.Wait()
	if got := c.backgroundTaskInventory(); len(got.Sessions) != 0 {
		t.Fatal("completed tasks remained live")
	}
}
func TestWSBackgroundInventoryHelloHeartbeatAndReconnect(t *testing.T) {
	hellos := make(chan protocol.DaemonHello, 4)
	heartbeats := make(chan protocol.DaemonHeartbeat, 16)
	connections := make(chan *websocket.Conn, 4)
	server := newTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		connections <- conn
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var envelope struct{ Type string }
			_ = json.Unmarshal(raw, &envelope)
			switch envelope.Type {
			case "daemon_hello":
				var msg protocol.DaemonHello
				_ = json.Unmarshal(raw, &msg)
				hellos <- msg
			case "daemon_heartbeat":
				var msg protocol.DaemonHeartbeat
				_ = json.Unmarshal(raw, &msg)
				heartbeats <- msg
			}
		}
	})
	c := NewWSClient(Config{ServerURL: server.wsURL(), ReposRoot: t.TempDir(), HeartbeatInterval: time.Hour, ReconnectInitial: time.Millisecond, ReconnectMax: time.Millisecond})
	_ = c.Send(backgroundFixtureEvent("start", "background_task_started", "task"))
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { c.Run(ctx, func() []string { return []string{backgroundFixtureSession} }); close(finished) }()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(15 * time.Second):
			t.Error("client did not stop")
		}
	}()
	receiveHello := func() protocol.DaemonHello {
		t.Helper()
		select {
		case h := <-hellos:
			return h
		case <-time.After(15 * time.Second):
			t.Fatal("hello missing")
		}
		return protocol.DaemonHello{}
	}
	first := receiveHello()
	if first.BackgroundTasks == nil || len(first.BackgroundTasks.Sessions[backgroundFixtureSession]) != 1 {
		t.Fatal("hello missing live task snapshot")
	}
	_ = c.Send(backgroundFixtureEvent("done", "background_task_done", "task"))
	c.RequestHeartbeat()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case hb := <-heartbeats:
			if hb.BackgroundTasks != nil && len(hb.BackgroundTasks.Sessions) == 0 {
				goto updated
			}
		case <-deadline:
			t.Fatal("heartbeat did not update snapshot")
		}
	}
updated:
	conn := <-connections
	_ = conn.Close()
	second := receiveHello()
	if second.BackgroundTasks == nil || second.BackgroundTasks.Lifetime != first.BackgroundTasks.Lifetime || len(second.BackgroundTasks.Sessions) != 0 {
		t.Fatal("reconnect changed daemon lifetime or resurrected task")
	}
}

func TestWSBackgroundInventoryLimitsAndSessionEnd(t *testing.T) {
	t.Run("seen overflow", func(t *testing.T) {
		c := NewWSClient(Config{})
		c.mu.Lock()
		for i := range backgroundSeenLimit {
			c.background.seen[fmt.Sprintf("seen-%d", i)] = backgroundObservedEvent{kind: "background_task_done", task: "old"}
		}
		c.mu.Unlock()
		_ = c.Send(backgroundFixtureEvent("fresh", "background_task_started", "task"))
		if c.backgroundTaskInventory() != nil {
			t.Fatal("replay tracking overflow produced authoritative partial inventory")
		}
	})
	t.Run("active overflow", func(t *testing.T) {
		c := NewWSClient(Config{})
		c.mu.Lock()
		tasks := map[string]bool{}
		for i := range protocol.MaxBackgroundTasks {
			tasks[fmt.Sprintf("task-%d", i)] = true
		}
		c.background.live[backgroundFixtureSession] = tasks
		c.background.total = len(tasks)
		c.mu.Unlock()
		_ = c.Send(backgroundFixtureEvent("fresh", "background_task_started", "extra"))
		if c.backgroundTaskInventory() != nil {
			t.Fatal("active task overflow produced truncated inventory")
		}
	})
	t.Run("session ended", func(t *testing.T) {
		c := NewWSClient(Config{})
		_ = c.Send(backgroundFixtureEvent("start", "background_task_started", "task"))
		_ = c.Send(protocol.SessionEnded{Type: "session_ended", SessionID: backgroundFixtureSession})
		if got := c.backgroundTaskInventory(); got == nil || len(got.Sessions) != 0 {
			t.Fatal("ended session retained live task")
		}
		_ = c.Send(backgroundFixtureEvent("start", "background_task_started", "task"))
		if got := c.backgroundTaskInventory(); got == nil || len(got.Sessions) != 0 {
			t.Fatal("ended-session start replay resurrected task")
		}
	})
}
