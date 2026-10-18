package daemon

import (
	"context"
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryReadyIsCurrentLinkAndFailClosed(t *testing.T) {
	h := NewAgentHost(&agentTestSender{}, AgentHostConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { h.waitRecoveryReady(ctx); close(done) }()
	assertHeld := func() {
		t.Helper()
		select {
		case <-done:
			t.Fatal("recovery input released before current-link policy ready")
		case <-time.After(15 * time.Millisecond):
		}
	}
	assertHeld()
	h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryLegacy})
	assertHeld() // old server confirmation must not release a marked child
	h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryUnknown})
	h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryReady})
	assertHeld() // late completion from a superseded link
	h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryHeld})
	assertHeld()
	h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryReady})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("current link did not release recovery")
	}
	h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryDisconnected})
	if h.recoveryInputReady() {
		t.Fatal("disconnection retained recovery authorization")
	}
	h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryReady})
	if h.recoveryInputReady() {
		t.Fatal("late ready reopened disconnected link")
	}
}

func TestRecoveryReadyWaitCancels(t *testing.T) {
	h := NewAgentHost(&agentTestSender{}, AgentHostConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.waitRecoveryReady(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("killed recovery input worker remained blocked")
	}
}

func TestMarkedAdoptionWaitsForRecoveryReady(t *testing.T) {
	for _, mode := range []string{"automatic", "manual", "killed"} {
		t.Run(mode, func(t *testing.T) {
			newHost, recDir, workDir, claudeLog := recoveryHost(t)
			t.Setenv("BLERG_CLAUDE_STEERING", "0")
			stubTranscripts(t, "conv-1")
			rec := AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: workDir, ClaudeSessionID: "conv-1", TurnActive: true, Interaction: protocol.InteractionUnattended, RequireRecoveryReady: true}
			if mode == "manual" {
				rec.Waiting = true
				rec.Interaction = protocol.InteractionInteractive
			}
			if err := writeAgentRecord(recDir, rec); err != nil {
				t.Fatal(err)
			}
			h, sender := newHost()
			h.NoteConnected() // the old startup signal and timeout are insufficient
			h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryHeld})
			if h.AdoptRecorded() != 1 {
				t.Fatal("not adopted")
			}
			saved, ok := readRecord(t, recDir, "s1")
			if !ok || !saved.RequireRecoveryReady {
				t.Fatal("recovery marker lost in adoption record")
			}
			delivered := make(chan struct{})
			if mode == "manual" {
				go func() { h.UserMessage("s1", "explicit recovery input", "chat"); close(delivered) }()
				select {
				case <-delivered:
				case <-time.After(time.Second):
					t.Fatal("socket input handler blocked waiting for ready")
				}
			}
			time.Sleep(4 * adoptContinueDelay) // beyond the injectable legacy grace
			assertNoCalls := func() {
				t.Helper()
				if raw, _ := os.ReadFile(claudeLog); len(raw) > 0 {
					t.Fatalf("engine ran before authorization: %s", raw)
				}
			}
			assertNoCalls()
			if mode == "killed" {
				h.Kill("s1")
			}
			h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryReady})
			if mode == "killed" {
				time.Sleep(30 * time.Millisecond)
				assertNoCalls()
				if h.Has("s1") {
					t.Fatal("killed session returned")
				}
				return
			}
			sender.waitForKind(t, "turn_done")
			raw, _ := os.ReadFile(claudeLog)
			want := adoptContinuePrompt
			if mode == "manual" {
				want = "explicit recovery input"
			}
			if !strings.Contains(string(raw), want) {
				t.Fatalf("missing released prompt %q", want)
			}
			// Disconnect must fence subsequent manual turns too, not just startup.
			h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryDisconnected})
			h.UserMessage("s1", "after disconnect", "chat")
			time.Sleep(30 * time.Millisecond)
			raw, _ = os.ReadFile(claudeLog)
			if strings.Contains(string(raw), "after disconnect") {
				t.Fatal("new turn ran on disconnected recovery authorization")
			}
			h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryHeld})
			h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryReady})
			time.Sleep(20 * time.Millisecond)
			raw, _ = os.ReadFile(claudeLog)
			if strings.Contains(string(raw), "after disconnect") {
				t.Fatal("stale ready released new link")
			}
			h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryReady})
			sender.waitForKindCount(t, "turn_done", 2)
		})
	}
}

func TestWSRecoveryReadyNegotiationAndPump(t *testing.T) {
	release := make(chan struct{})
	doneServer := make(chan struct{})
	ts := newTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		defer close(doneServer)
		var hello protocol.DaemonHello
		if conn.ReadJSON(&hello) != nil {
			return
		}
		if !hello.RecoveryReady {
			t.Error("hello did not advertise recovery barrier")
		}
		if conn.WriteJSON(protocol.DaemonRecoveryReady{Type: "daemon_recovery_ready"}) != nil {
			return
		} // unsolicited: ignored
		if conn.WriteJSON(protocol.DaemonWelcome{Type: "daemon_welcome", RecoveryHold: true, SessionID: "fixture-session"}) != nil {
			return
		}
		// A frame for another scoped session must not open this link.
		if conn.WriteJSON(protocol.DaemonRecoveryReady{Type: "daemon_recovery_ready", SessionID: "foreign-session"}) != nil {
			return
		}
		if conn.WriteJSON(map[string]string{"type": "agent_user_message"}) != nil {
			return
		}
		<-release
		if conn.WriteJSON(protocol.DaemonRecoveryReady{Type: "daemon_recovery_ready", SessionID: "fixture-session"}) != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	c := NewWSClient(Config{ServerURL: ts.wsURL(), DaemonName: "fixture", HeartbeatInterval: time.Hour})
	h := NewAgentHost(c, AgentHostConfig{})
	handled := make(chan struct{})
	c.OnMessage("agent_user_message", func(_ []byte) { close(handled) })
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { _, _ = c.connect(ctx, func() []string { return nil }); close(finished) }()
	defer func() { cancel(); <-finished; <-doneServer }()
	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("read pump blocked before ready")
	}
	if h.recoveryInputReady() {
		t.Fatal("hello/welcome or unsolicited frame released recovery")
	}
	close(release)
	waitUntil(t, "negotiated ready", h.recoveryInputReady)
	cancel()
	<-finished
	if h.recoveryInputReady() {
		t.Fatal("connection shutdown left recovery open")
	}
}

func TestClaudeRecoveryRechecksAfterDiscovery(t *testing.T) {
	d, p := claudeMCPFixture(t)
	h := NewAgentHost(&agentTestSender{}, AgentHostConfig{})
	h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryReady})
	d.SetRecoveryInputGate(h.waitRecoveryReady, h.recoveryInputReady)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d.SetMCPInputGate(func(ctx context.Context) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Enqueue("held across discovery", "chat")
	go func() { d.deliverOne(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("discovery gate not entered")
	}
	h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryDisconnected})
	h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryHeld})
	close(release)
	select {
	case raw := <-p.writeCh:
		t.Fatalf("old link authorization survived discovery: %s", raw)
	case <-time.After(20 * time.Millisecond):
	}
	h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryReady})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("current ready did not release worker")
	}
	select {
	case <-p.writeCh:
	default:
		t.Fatal("released message missing")
	}
}

func TestCodexRecoveryHoldsManualSteeringWithoutBlockingRPC(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_HOLD_FIRST", "1")
	logPath := filepath.Join(bin, "calls.log")
	t.Setenv("FAKE_CODEX_LOG", logPath)
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	h := NewAgentHost(&agentTestSender{}, AgentHostConfig{})
	h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryReady})
	d.SetRecoveryInputGate(h.waitRecoveryReady, h.recoveryInputReady)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startTestCodexDriver(ctx, t, d)
	defer stop()
	d.Enqueue("start", "chat")
	waitUntil(t, "first accepted input", func() bool { return countKind(em, "user_message") == 1 })
	h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryDisconnected})
	h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryHeld})
	d.Enqueue("held manual steer", "chat")
	// Native RPC must continue while the socket input's turn is held.
	statusCtx, statusCancel := context.WithTimeout(ctx, time.Second)
	defer statusCancel()
	if _, err := d.MCPStatus(statusCtx); err != nil {
		t.Fatalf("held recovery blocked native status RPC: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	raw, _ := os.ReadFile(logPath)
	if strings.Contains(string(raw), "turn/steer") || countKind(em, "user_message") != 1 {
		t.Fatal("manual steering bypassed current-link barrier")
	}
	h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryReady})
	waitUntil(t, "released steering", func() bool { return countKind(em, "turn_done") == 1 })
	raw, _ = os.ReadFile(logPath)
	if !strings.Contains(string(raw), "turn/steer") || countKind(em, "user_message") != 2 {
		t.Fatal("released manual steering missing")
	}
}

func TestOtherEnginesRecoveryAdmission(t *testing.T) {
	for _, engine := range []string{"hermes", "openclaw"} {
		t.Run(engine, func(t *testing.T) {
			var bin string
			if engine == "hermes" {
				bin = fakeHermes(t, "done", 0)
			} else {
				bin = fakeOpenclaw(t, `{"ok":true,"final":"done"}`, 0)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			logPath := filepath.Join(bin, "calls.log")
			t.Setenv("FAKE_HERMES_LOG", logPath)
			t.Setenv("FAKE_OPENCLAW_LOG", logPath)
			em := &collectEmitter{}
			opts := AgentDriverOpts{Spec: engineRegistry[engine], WorkDir: t.TempDir(), SessionID: "fixture", Emitter: em}
			var d interface {
				sessionDriver
				SetRecoveryInputGate(func(context.Context), func() bool)
			}
			if engine == "hermes" {
				d = newHermesDriver(opts)
			} else {
				d = newOpenclawDriver(opts)
			}
			h := NewAgentHost(&agentTestSender{}, AgentHostConfig{})
			d.SetRecoveryInputGate(h.waitRecoveryReady, h.recoveryInputReady)
			ctx, cancel := context.WithCancel(context.Background())
			finished := make(chan struct{})
			go func() { d.Run(ctx); close(finished) }()
			defer func() { cancel(); <-finished }()
			d.EnqueueInput(agent.Input{Text: "first fixture"})
			time.Sleep(20 * time.Millisecond)
			if countKind(em, "user_message") != 0 {
				t.Fatal("engine bypassed initial recovery hold")
			}
			h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryHeld})
			h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryReady})
			waitUntil(t, "first turn", func() bool { return countKind(em, "turn_done") == 1 })
			h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryDisconnected})
			d.EnqueueInput(agent.Input{Text: "second fixture"})
			d.EnqueueInput(agent.Input{Text: "third fixture"})
			time.Sleep(20 * time.Millisecond)
			if countKind(em, "user_message") != 1 {
				t.Fatal("reconnect did not fence engine worker")
			}
			h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryHeld})
			h.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryReady})
			waitUntil(t, "queued turns", func() bool { return countKind(em, "turn_done") == 3 })
			em.mu.Lock()
			defer em.mu.Unlock()
			var texts []string
			for _, event := range em.events {
				if p, ok := event.Payload.(agent.UserMessagePayload); ok {
					texts = append(texts, p.Text)
				}
			}
			if strings.Join(texts, ",") != "first fixture,second fixture,third fixture" {
				t.Fatalf("input order changed: %v", texts)
			}
		})
	}
}

func TestFreshMarkedSpawnPersistsAndUsesRecoveryBarrier(t *testing.T) {
	newHost, recDir, _, logPath := recoveryHost(t)
	t.Setenv("BLERG_CLAUDE_STEERING", "0")
	h, sender := newHost()
	h.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "fresh", Kind: "agent", Repo: "proj", RequireRecoveryReady: true, InitialPrompt: "fresh authorized work"})
	rec, ok := readRecord(t, recDir, "fresh")
	if !ok || !rec.RequireRecoveryReady {
		t.Fatal("new delegated marker not persisted before work")
	}
	time.Sleep(20 * time.Millisecond)
	if raw, _ := os.ReadFile(logPath); len(raw) > 0 {
		t.Fatal("new marked input ran on unconfirmed link")
	}
	h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryHeld})
	h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryReady})
	sender.waitForKind(t, "turn_done")
}

func TestWSRecoveryStateRejectsLegacyAndStaleReady(t *testing.T) {
	c := NewWSClient(Config{})
	h := NewAgentHost(c, AgentHostConfig{})
	generation := c.beginRecoveryLink()
	c.noteRecoveryLink(generation, RecoveryReady)
	if h.recoveryInputReady() {
		t.Fatal("unsolicited ready authorized unknown link")
	}
	c.noteRecoveryLink(generation, RecoveryLegacy)
	c.noteRecoveryLink(generation, RecoveryReady)
	if h.recoveryInputReady() {
		t.Fatal("legacy welcome accepted ready")
	}
	c.noteRecoveryLink(generation, RecoveryDisconnected)
	next := c.beginRecoveryLink()
	c.noteRecoveryLink(next, RecoveryHeld)
	c.noteRecoveryLink(generation, RecoveryReady)
	if h.recoveryInputReady() {
		t.Fatal("old callback opened current link")
	}
	c.noteRecoveryLink(next, RecoveryReady)
	if !h.recoveryInputReady() {
		t.Fatal("current negotiated ready rejected")
	}
	c.noteRecoveryLink(next, RecoveryDisconnected)
	c.noteRecoveryLink(next, RecoveryReady)
	if h.recoveryInputReady() {
		t.Fatal("late ready reopened closed link")
	}
}

func TestMarkedAdoptionUsesOpenAskSnapshotBeforeReady(t *testing.T) {
	newHost, recDir, workDir, logPath := recoveryHost(t)
	t.Setenv("BLERG_CLAUDE_STEERING", "0")
	stubTranscripts(t, "conv-1")
	if err := writeAgentRecord(recDir, AgentRecord{SessionID: "waiting", Repo: "proj", WorkDir: workDir, ClaudeSessionID: "conv-1", TurnActive: true, Interaction: protocol.InteractionInteractive, RequireRecoveryReady: true}); err != nil {
		t.Fatal(err)
	}
	h, sender := newHost()
	h.NoteConnected()
	h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryHeld})
	h.AdoptRecorded()
	// The server's open-question snapshot precedes ready on the same FIFO.
	h.NoteOpenAsks([]string{"waiting"})
	h.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryReady})
	waitUntil(t, "waiting recovery to pause", func() bool { return h.States()["waiting"] == "idle" })
	if raw, _ := os.ReadFile(logPath); len(raw) > 0 {
		t.Fatal("question-waiting child automatically continued")
	}
	h.UserMessage("waiting", "the answer", "chat")
	sender.waitForKind(t, "turn_done")
}

// A desktop Docker agent runs its driver in the outer daemon, not an inner
// runner. Exercise the real Manager -> AgentHost -> docker exec boundary.
func TestManagerDockerSpawnPreservesRecoveryBarrier(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, dockerLog, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)
	t.Setenv("BLERG_CLAUDE_STEERING", "0")
	engineLog := filepath.Join(engineDir, "engine-calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", engineLog)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: host.cfg.ReposRoot})
	mgr.agents = host
	mgr.sandboxImagePresent = func() bool { return true }
	mgr.sandboxEnginePresent = func(string) error { return nil }
	defer host.Kill("docker-child")
	raw, err := json.Marshal(protocol.SpawnSession{Type: "spawn_session", SessionID: "docker-child", Repo: "proj", Kind: "agent", Sandbox: true, RequireRecoveryReady: true, InitialPrompt: "authorized Docker prompt"})
	if err != nil {
		t.Fatal(err)
	}
	mgr.handleSpawnSession(raw)
	if !host.Has("docker-child") {
		t.Fatalf("Docker child did not spawn: %v", sender.sessionErrors())
	}
	assertSandboxRun(t, dockerCalls(t, dockerLog), "docker-child")
	time.Sleep(20 * time.Millisecond)
	if calls, _ := os.ReadFile(engineLog); len(calls) > 0 {
		t.Fatal("container engine received input before current-link ready")
	}
	host.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryHeld})
	host.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryReady})
	sender.waitForKind(t, "turn_done")
	assertExecsInContainer(t, dockerCalls(t, dockerLog), "docker-child", "claude -p authorized Docker prompt")
	host.NoteRecoveryState(RecoveryState{Generation: 1, Phase: RecoveryDisconnected})
	mgr.handleAgentUserMessage([]byte(`{"type":"agent_user_message","session_id":"docker-child","text":"held Docker followup"}`))
	time.Sleep(20 * time.Millisecond)
	if calls, _ := os.ReadFile(engineLog); strings.Contains(string(calls), "held Docker followup") {
		t.Fatal("container followup bypassed reconnect hold")
	}
	host.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryHeld})
	host.NoteRecoveryState(RecoveryState{Generation: 2, Phase: RecoveryReady})
	sender.waitForKindCount(t, "turn_done", 2)
	calls, _ := os.ReadFile(engineLog)
	if !strings.Contains(string(calls), "held Docker followup") {
		t.Fatal("container followup lost after reconnect ready")
	}
}
