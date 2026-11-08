package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Recovery belongs to the host. The session's token is in the agent's hands, so it never carries it.
func TestQuestionRecoveryUsesTheHostsCredentialOnDesktop(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/api/messages/recover" {
			t.Errorf("recovery route: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer bdt_fixture-current" {
			t.Errorf("recovery credential: %q", r.Header.Get("Authorization"))
		}
		var body struct {
			SessionID      string `json:"session_id"`
			HostGeneration int64  `json:"host_generation"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.SessionID != "fixture-session" || body.HostGeneration != 7 {
			t.Errorf("recovery body: %+v", body)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	h := &AgentHost{cfg: AgentHostConfig{ServerHTTP: srv.URL, DaemonToken: "bdt_fixture-desktop", DaemonTokenSource: func() string { return "bdt_fixture-current" }}}
	if err := h.recoverQuestionWaiters(t.Context(), protocol.SpawnSession{SessionID: "fixture-session", SessionToken: "fixture-session-token", HostGeneration: 7}); err == nil {
		t.Fatal("host would accept input after failed question cleanup")
	}
	if calls.Load() != 1 {
		t.Fatalf("recovery calls = %d", calls.Load())
	}
}

// Every retry keeps the boundary of the hosting that needed it, and a late success of an earlier
// hosting does not cancel the recovery of a later one.
func TestRecoveryRetriesKeepTheirHostingBoundary(t *testing.T) {
	var mu sync.Mutex
	var seen []int64
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			HostGeneration int64 `json:"host_generation"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, body.HostGeneration)
		mu.Unlock()
		if body.HostGeneration == 1 {
			<-release
		}
	}))
	defer srv.Close()
	h := &AgentHost{cfg: AgentHostConfig{ServerHTTP: srv.URL, DaemonToken: "fixture-daemon"}, questionRecovery: map[string]protocol.SpawnSession{}}
	h.questionRecoveryWait, h.questionRecoveryPause = 5*time.Second, time.Millisecond
	h.questionRecovery["s1"] = protocol.SpawnSession{SessionID: "s1", HostGeneration: 1}
	finished := make(chan struct{})
	go func() { defer close(finished); h.retryQuestionRecovery() }()
	waitUntil(t, "first retry in flight", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 1
	})
	h.mu.Lock()
	h.questionRecovery["s1"] = protocol.SpawnSession{SessionID: "s1", HostGeneration: 2}
	h.mu.Unlock()
	close(release)
	<-finished
	h.mu.Lock()
	defer h.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	if _, left := h.questionRecovery["s1"]; left {
		t.Fatal("the later hosting's recovery never ran")
	}
	if len(seen) != 2 || seen[0] != 1 || seen[1] != 2 {
		t.Fatalf("generations sent = %v", seen)
	}
}

func TestHostGenerationsOnlyRise(t *testing.T) {
	h := &AgentHost{}
	previous := h.nextHostGeneration(0)
	for range 1000 {
		next := h.nextHostGeneration(0)
		if next <= previous {
			t.Fatalf("generation went from %d to %d", previous, next)
		}
		previous = next
	}
}

// A failed recovery must not stop a restarted session, and it retries by itself.
func TestFailedQuestionRecoveryDoesNotStopTheRestart(t *testing.T) {
	host, _, _ := newTestHost(t, &scriptedAgentProvider{}, "")
	host.questionRecoveryWait, host.questionRecoveryPause = time.Second, 10*time.Millisecond
	var calls, healthy atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/messages/recover" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls.Add(1)
		if healthy.Load() == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	host.cfg.ServerHTTP = srv.URL
	host.cfg.DaemonToken = "fixture-daemon"
	host.SpawnResumed(protocol.SpawnSession{SessionID: "s1", Repo: "proj", Kind: "agent", InitialPrompt: "continue"}, nil, "resumed-from-wip")
	if !host.Has("s1") {
		t.Fatal("failed question recovery stopped the restart")
	}
	if calls.Load() == 0 {
		t.Fatal("restart with no acknowledged transcript skipped waiter recovery")
	}
	waitUntil(t, "retries exhausted", func() bool { return calls.Load() >= int32(questionRecoveryAttempts)+1 })
	time.Sleep(50 * time.Millisecond)
	if extra := calls.Load(); extra != int32(questionRecoveryAttempts)+1 {
		t.Fatalf("retries were not bounded: %d", extra)
	}
	healthy.Store(1)
	host.NoteConnected()
	waitUntil(t, "recovery done on connect", func() bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		return len(host.questionRecovery) == 0
	})
}

func TestOlderServerWithoutRecoveryRouteIsTolerated(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		h := &AgentHost{cfg: AgentHostConfig{ServerHTTP: srv.URL, DaemonToken: "fixture-daemon"}}
		err := h.recoverQuestionWaiters(t.Context(), protocol.SpawnSession{SessionID: "s", HostGeneration: 1})
		srv.Close()
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
	}
}

func TestUnreachableServerDoesNotHoldTheRestart(t *testing.T) {
	host, _, _ := newTestHost(t, &scriptedAgentProvider{}, "")
	host.questionRecoveryWait, host.questionRecoveryPause = time.Second, 10*time.Millisecond
	srv := httptest.NewServer(http.NotFoundHandler())
	host.cfg.ServerHTTP = srv.URL
	host.cfg.DaemonToken = "fixture-daemon"
	srv.Close()
	started := time.Now()
	host.SpawnResumed(protocol.SpawnSession{SessionID: "s1", Repo: "proj", Kind: "agent"}, nil, "resumed-from-wip")
	if !host.Has("s1") || time.Since(started) > 5*time.Second {
		t.Fatalf("restart held: live=%t after %s", host.Has("s1"), time.Since(started))
	}
}

// A pod resume can start without restored events; its waiters are gone all the same.
func TestPodResumeWithoutRestoredEventsRecoversQuestions(t *testing.T) {
	host, _, _ := newTestHost(t, &scriptedAgentProvider{}, "")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/messages/recover" {
			calls.Add(1)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	host.cfg.ServerHTTP = srv.URL
	host.cfg.DaemonToken = "fixture-daemon"
	host.Spawn(protocol.SpawnSession{SessionID: "s1", Repo: "proj", Kind: "agent", RecoverQuestions: true})
	if calls.Load() != 1 {
		t.Fatalf("recovery calls = %d", calls.Load())
	}
}

// A restarted engine's questions reach the server under that hosting, however they are asked.
func TestQuestionsCarryTheirHostGeneration(t *testing.T) {
	bodies := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies <- body
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"q1"}`))
	}))
	defer srv.Close()
	token := func() string { return "fixture-daemon" }
	loop := NewLoopMessenger(LoopMessengerConfig{Context: t.Context(), BaseURL: srv.URL, SessionID: "s1", TokenSource: token, HostGeneration: 42})
	if _, err := loop.SendAsk("Which branch?"); err != nil {
		t.Fatal(err)
	}
	if body := <-bodies; body["host_generation"] != float64(42) {
		t.Fatalf("native ask: %v", body)
	}
	if err := loop.SendNote("not a question"); err != nil {
		t.Fatal(err)
	}
	if body := <-bodies; body["host_generation"] != nil {
		t.Fatalf("note carried a generation: %v", body)
	}
	h := &AgentHost{cfg: AgentHostConfig{ServerHTTP: srv.URL, DaemonToken: "fixture-daemon"}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.questionAskerAt(43, "s1", &collectEmitter{}, "", nil)(ctx, "Which region?")
	}()
	if body := <-bodies; body["host_generation"] != float64(43) {
		t.Fatalf("engine question: %v", body)
	}
	cancel()
	<-done
}

// A clock that steps back across a restart cannot reorder hostings, however far it steps, so a
// recovery still outstanding from the earlier hosting stays below the new engine's questions.
func TestHostGenerationSurvivesClockRollbackAcrossRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent-records")
	at := time.Unix(1_900_000_000, 0)
	before := &AgentHost{cfg: AgentHostConfig{GenerationDir: dir}, hostGenerationClock: func() time.Time { return at }}
	older := before.nextHostGeneration(0)

	var seen []int64
	var mu sync.Mutex
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			HostGeneration int64 `json:"host_generation"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		<-gate
		mu.Lock()
		seen = append(seen, body.HostGeneration)
		mu.Unlock()
	}))
	t.Cleanup(server.Close)
	before.cfg.ServerHTTP, before.cfg.DaemonToken = server.URL, "fixture-token"
	outstanding := make(chan error, 1)
	go func() {
		outstanding <- before.recoverQuestionWaiters(context.Background(), protocol.SpawnSession{SessionID: "session", HostGeneration: older})
	}()

	after := &AgentHost{cfg: AgentHostConfig{GenerationDir: dir}, hostGenerationClock: func() time.Time { return at.Add(-24 * time.Hour) }}
	newer := after.nextHostGeneration(0)
	if newer != older+1 {
		t.Fatalf("after rollback: %d, want %d", newer, older+1)
	}
	third := &AgentHost{cfg: AgentHostConfig{GenerationDir: dir}, hostGenerationClock: func() time.Time { return at.Add(-48 * time.Hour) }}
	if got := third.nextHostGeneration(0); got != newer+1 {
		t.Fatalf("second restart: %d, want %d", got, newer+1)
	}
	release()
	if err := <-outstanding; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] >= newer {
		t.Fatalf("the late recovery's boundary %v reaches generation %d", seen, newer)
	}
}

// A pod's home does not outlive it: the server's floor keeps the order there.
func TestHostGenerationHonorsTheServersFloor(t *testing.T) {
	at := time.Unix(1_900_000_000, 0)
	h := &AgentHost{hostGenerationClock: func() time.Time { return at }}
	floor := at.Add(time.Hour).UnixNano()
	if got := h.nextHostGeneration(floor); got != floor+1 {
		t.Fatalf("generation = %d, want %d", got, floor+1)
	}
}

func TestUnreadableHostGenerationDoesNotStopHosting(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent-records")
	if err := os.WriteFile(filepath.Join(root, hostGenerationFile), []byte("not a number"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &AgentHost{cfg: AgentHostConfig{GenerationDir: dir}}
	if h.nextHostGeneration(0) <= 0 {
		t.Fatal("no generation")
	}
	if readHostGeneration(filepath.Join(root, hostGenerationFile)) != h.hostGeneration.Load() {
		t.Fatal("the file was not repaired")
	}
}
