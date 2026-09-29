package daemon

// spawn_session used to be handled inline on the websocket read pump, and its
// first action is prepareWorkspace → EnsureCloned with a five-minute timeout.
// A cold board-driven start on an uncloned repo therefore wedged ALL daemon
// message handling for minutes — kill_session, send_input, and the websocket
// control frames ReadMessage processes, so the server's heartbeat could
// declare the daemon dead while the clone was still running.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// The dispatch the read pump performs must return promptly for spawn_session,
// and a message that arrives during a slow clone must be handled while that
// clone is still in flight.
func TestSpawnSessionDoesNotBlockMessageDispatch(t *testing.T) {
	root := t.TempDir()
	client := NewWSClient(Config{}) // never connected: we only want its dispatch table
	mgr := NewManager(client, ManagerConfig{ReposRoot: root, GithubOrg: "org", Command: []string{"unused"}})

	entered := make(chan struct{})
	release := make(chan struct{})
	mgr.ensureClonedFunc = func(_ context.Context, reposRoot, repo, provider, org string) error {
		close(entered)
		<-release
		return nil
	}
	t.Cleanup(func() { close(release) })

	spawn := client.handlers["spawn_session"]
	kill := client.handlers["kill_session"]
	if spawn == nil || kill == nil {
		t.Fatal("daemon message handlers are not registered")
	}

	raw, _ := json.Marshal(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-slow", Repo: "cold", Kind: "agent",
	})
	dispatched := make(chan struct{})
	go func() {
		spawn(raw)
		close(dispatched)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the clone never started")
	}
	select {
	case <-dispatched:
	case <-time.After(5 * time.Second):
		t.Fatal("spawn_session blocked the read pump for the duration of the clone")
	}

	// ...and the next message is handled while the clone is still blocked.
	killRaw, _ := json.Marshal(protocol.KillSession{Type: "kill_session", SessionID: "s-other"})
	handled := make(chan struct{})
	go func() {
		kill(killRaw)
		close(handled)
	}()
	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("kill_session was not handled while a clone was in flight")
	}
}

// A kill_session for a spawn that is still queued or cloning must cancel it:
// detaching the spawn means a kill can now overtake it, and without this the
// daemon would finish creating a session the user already killed.
func TestKillCancelsAQueuedSpawn(t *testing.T) {
	root := t.TempDir()
	client := NewWSClient(Config{})
	mgr := NewManager(client, ManagerConfig{ReposRoot: root, GithubOrg: "org", Command: []string{"unused"}})

	entered := make(chan struct{})
	release := make(chan struct{})
	mgr.ensureClonedFunc = func(_ context.Context, reposRoot, repo, provider, org string) error {
		close(entered)
		<-release
		return nil
	}

	raw, _ := json.Marshal(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-killed", Repo: "cold", Title: "t",
	})
	client.handlers["spawn_session"](raw)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the clone never started")
	}

	killRaw, _ := json.Marshal(protocol.KillSession{Type: "kill_session", SessionID: "s-killed"})
	client.handlers["kill_session"](killRaw)
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.Lock()
		_, live := mgr.sessions["s-killed"]
		mgr.mu.Unlock()
		if live {
			t.Fatal("a spawn that was killed while cloning still created a session")
		}
		mgr.mu.Lock()
		_, pending := mgr.pendingSpawns["s-killed"]
		mgr.mu.Unlock()
		if !pending {
			return // the spawn ran to completion and abandoned itself
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the cancelled spawn never finished")
}
