package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// agentTestSender captures everything sent to the server.
type agentTestSender struct {
	mu   sync.Mutex
	msgs []any
}

func (s *agentTestSender) Send(msg any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, msg)
	return nil
}

func (s *agentTestSender) agentEvents() []protocol.AgentEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []protocol.AgentEvent
	for _, m := range s.msgs {
		if ev, ok := m.(protocol.AgentEvent); ok {
			out = append(out, ev)
		}
	}
	return out
}

func (s *agentTestSender) waitForKind(t *testing.T, kind string) protocol.AgentEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range s.agentEvents() {
			if ev.Kind == kind {
				return ev
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no agent_event kind %q; got %v", kind, s.kinds())
	return protocol.AgentEvent{}
}

func (s *agentTestSender) kinds() []string {
	var out []string
	for _, ev := range s.agentEvents() {
		out = append(out, ev.Kind)
	}
	return out
}

// scriptedAgentProvider adapts scripted responses (like the agent package's
// fake) for host tests.
type scriptedAgentProvider struct {
	mu      sync.Mutex
	scripts [][]agent.StreamEvent
}

func (p *scriptedAgentProvider) Stream(ctx context.Context, req agent.Request) (<-chan agent.StreamEvent, error) {
	p.mu.Lock()
	var script []agent.StreamEvent
	if len(p.scripts) > 0 {
		script = p.scripts[0]
		p.scripts = p.scripts[1:]
	} else {
		script = []agent.StreamEvent{
			{Kind: "block", Block: &agent.Block{Type: "text", Text: "done"}},
			{Kind: "done", StopReason: "end_turn", Usage: &agent.Usage{}},
		}
	}
	p.mu.Unlock()
	ch := make(chan agent.StreamEvent)
	go func() {
		defer close(ch)
		for _, ev := range script {
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
	}()
	return ch, nil
}

func newTestHost(t *testing.T, provider agent.Provider, serverHTTP string) (*AgentHost, *agentTestSender, string) {
	t.Helper()
	reposRoot := t.TempDir()
	workDir := filepath.Join(reposRoot, "proj")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{
		ReposRoot:  reposRoot,
		ServerHTTP: serverHTTP,
		HomeDir:    t.TempDir(),
		Provider:   provider,
	})
	return host, sender, workDir
}

func TestAgentHostSpawnRunsTurnAndShipsEvents(t *testing.T) {
	provider := &scriptedAgentProvider{}
	host, sender, _ := newTestHost(t, provider, "")
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s1", Repo: "proj",
		InitialPrompt: "hello agent", Kind: "agent",
	})
	td := sender.waitForKind(t, "turn_done")
	if td.ClientEventID == "" || td.Transient {
		t.Fatalf("turn_done malformed: %+v", td)
	}
	// model_changed bootstrap must be present
	sender.waitForKind(t, "model_changed")
	// transient deltas are marked
	// (scripted provider emits no text_delta here; check user_message persisted)
	um := sender.waitForKind(t, "user_message")
	var p agent.UserMessagePayload
	if err := json.Unmarshal(um.Payload, &p); err != nil || p.Text != "hello agent" {
		t.Fatalf("user_message payload = %s err=%v", um.Payload, err)
	}
	if got := host.States()["s1"]; got != "idle" {
		t.Fatalf("state = %q", got)
	}
}

// statusReports lists the session_state_changed statuses sent, in order.
func (s *agentTestSender) statusReports() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.msgs {
		if st, ok := m.(protocol.SessionStateChanged); ok {
			out = append(out, st.Status)
		}
	}
	return out
}

// A session with nothing to do is idle — and says so right after
// session_started — rather than reporting "running" until its first turn.
func TestAgentHostSpawnWithoutPromptIsReadyAndIdle(t *testing.T) {
	host, sender, _ := newTestHost(t, &scriptedAgentProvider{}, "")
	host.Spawn(protocol.SpawnSession{SessionID: "s1", Repo: "proj", Kind: "agent"})
	if got := host.States()["s1"]; got != "idle" {
		t.Fatalf("heartbeat state = %q, want idle", got)
	}
	sender.mu.Lock()
	var sawStarted bool
	idleAfterStarted := false
	for _, m := range sender.msgs {
		switch v := m.(type) {
		case protocol.SessionStarted:
			sawStarted = true
		case protocol.SessionStateChanged:
			if sawStarted && v.Status == "idle" {
				idleAfterStarted = true
			}
		}
	}
	sender.mu.Unlock()
	if !idleAfterStarted {
		t.Fatalf("want session_started then idle; statuses %v", sender.statusReports())
	}
}

// With an initial prompt the first turn's own "running" is the transition —
// no idle is reported in between.
func TestAgentHostSpawnWithPromptReportsNoIdleFirst(t *testing.T) {
	host, sender, _ := newTestHost(t, &scriptedAgentProvider{}, "")
	host.Spawn(protocol.SpawnSession{SessionID: "s1", Repo: "proj", InitialPrompt: "go", Kind: "agent"})
	sender.waitForKind(t, "turn_done")
	for _, st := range sender.statusReports() {
		if st == "idle" {
			t.Fatalf("idle reported for a session that had work queued: %v", sender.statusReports())
		}
	}
}

// A resumed session whose resume carries the user's message (the cluster
// resume path) must not report idle first — that read as a finished turn.
func TestAgentHostResumeWithPromptReportsNoIdleFirst(t *testing.T) {
	host, sender, _ := newTestHost(t, &scriptedAgentProvider{}, "")
	host.SpawnResumed(protocol.SpawnSession{SessionID: "s1", Repo: "proj", InitialPrompt: "carry on", Kind: "agent"},
		nil, "resumed-from-wip")
	sender.waitForKind(t, "turn_done")
	for _, st := range sender.statusReports() {
		if st == "idle" {
			t.Fatalf("idle reported before the resumed turn: %v", sender.statusReports())
		}
	}
	um := sender.waitForKind(t, "user_message")
	var p agent.UserMessagePayload
	if err := json.Unmarshal(um.Payload, &p); err != nil || p.Text != "carry on" {
		t.Fatalf("user_message = %s", um.Payload)
	}
}

func TestAgentHostAckAndResend(t *testing.T) {
	provider := &scriptedAgentProvider{}
	host, sender, _ := newTestHost(t, provider, "")
	host.Spawn(protocol.SpawnSession{SessionID: "s1", Repo: "proj", InitialPrompt: "go", Kind: "agent"})
	sender.waitForKind(t, "turn_done")

	sess := host.get("s1")
	before := sess.emitter.pendingCount()
	if before == 0 {
		t.Fatal("expected pending unacked events")
	}
	// Ack every pending event.
	for _, ev := range sender.agentEvents() {
		if !ev.Transient {
			host.HandleAck("s1", ev.ClientEventID)
		}
	}
	if got := sess.emitter.pendingCount(); got != 0 {
		t.Fatalf("pending after acks = %d", got)
	}

	// Nothing to resend now.
	countBefore := len(sender.agentEvents())
	host.ResendPending()
	if got := len(sender.agentEvents()); got != countBefore {
		t.Fatalf("resend after full ack sent %d extra", got-countBefore)
	}
}

func TestAgentHostResendReplaysUnacked(t *testing.T) {
	provider := &scriptedAgentProvider{}
	host, sender, _ := newTestHost(t, provider, "")
	host.Spawn(protocol.SpawnSession{SessionID: "s1", Repo: "proj", InitialPrompt: "go", Kind: "agent"})
	sender.waitForKind(t, "turn_done")
	firstCount := len(sender.agentEvents())
	host.ResendPending() // nothing acked → everything re-sent
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sender.agentEvents()) >= 2*firstCount {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("resend did not replay: %d → %d", firstCount, len(sender.agentEvents()))
}

func TestAgentHostModelSlashCommand(t *testing.T) {
	provider := &scriptedAgentProvider{}
	host, sender, _ := newTestHost(t, provider, "")
	host.Spawn(protocol.SpawnSession{SessionID: "s1", Repo: "proj", Kind: "agent"})
	sender.waitForKind(t, "model_changed") // bootstrap
	host.UserMessage("s1", "/model claude-opus-5", "chat")
	// applied at turn boundary → next user message triggers it
	host.UserMessage("s1", "do something", "chat")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range sender.agentEvents() {
			if ev.Kind == "model_changed" {
				var p agent.ModelChangedPayload
				_ = json.Unmarshal(ev.Payload, &p)
				if p.Model == "claude-opus-5" && p.Source == "command" {
					return
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no model_changed for slash command; kinds %v", sender.kinds())
}

func TestHTTPMessengerAskFlow(t *testing.T) {
	answered := make(chan string, 1)
	var mu sync.Mutex
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/messages":
			body, _ := io.ReadAll(r.Body)
			if r.Header.Get("Authorization") != "Bearer tok" {
				t.Errorf("missing bearer: %v", r.Header)
			}
			var req map[string]string
			_ = json.Unmarshal(body, &req)
			if req["kind"] != "ask" || req["session_id"] != "s1" {
				t.Errorf("req = %v", req)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "msg-9"})
		case r.Method == "GET" && r.URL.Path == "/api/messages/msg-9/answer":
			mu.Lock()
			polls++
			n := polls
			mu.Unlock()
			if n < 2 {
				_ = json.NewEncoder(w).Encode(map[string]any{"answered": false})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"answered": true, "answer": "blue"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	m := &httpMessenger{
		base: srv.URL, token: "tok", sessionID: "s1",
		client:   &http.Client{Timeout: 5 * time.Second},
		onAnswer: func(a string) { answered <- a },
	}
	id, err := m.SendAsk("pick a color")
	if err != nil || id != "msg-9" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	select {
	case a := <-answered:
		if a != "blue" {
			t.Fatalf("answer = %q", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("answer never delivered")
	}
}

func TestBuildSystemPromptLayers(t *testing.T) {
	workDir, homeDir := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".claude", "CLAUDE.md"), []byte("USER RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "CLAUDE.md"), []byte("PROJECT RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := BuildSystemPrompt(workDir, homeDir, nil, agent.DiscoverAgentTypes(workDir, homeDir), "")
	for _, want := range []string{"check_in", "USER RULES", "PROJECT RULES", "general-purpose", "This session is interactive"} {
		if !containsStr(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	// The mode's paragraph comes before the person's own instructions, and an unattended
	// session gets the other one.
	if indexOf(prompt, "This session is interactive") > indexOf(prompt, "USER RULES") {
		t.Fatal("the interaction paragraph must come before the user's instructions")
	}
	unattended := BuildSystemPrompt(workDir, homeDir, nil, nil, protocol.InteractionUnattended)
	if !containsStr(unattended, "This session is unattended") || containsStr(unattended, "This session is interactive") {
		t.Fatalf("an unattended session's prompt states the wrong mode")
	}
	if interactive := BuildSystemPrompt(workDir, homeDir, nil, nil, protocol.InteractionInteractive); !containsStr(interactive, "This session is interactive") {
		t.Fatalf("an interactive session's prompt does not say so")
	}
}

// TestAgentHostNativeLoopRequiresAPIKey verifies that spawning an agent-kind
// session with no Provider/APIKey and no CLI engine selected fails with a
// clear error, rather than either silently hanging (a broken anthropic
// client that's never actually invoked) or — the old bug this replaces —
// being unreachable in the first place because AgentHost itself never got
// constructed without ANTHROPIC_API_KEY (see manager.go's NewManager).
func TestAgentHostNativeLoopRequiresAPIKey(t *testing.T) {
	reposRoot := t.TempDir()
	workDir := filepath.Join(reposRoot, "proj")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir()})
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "s1", Repo: "proj", Kind: "agent"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sender.mu.Lock()
		for _, m := range sender.msgs {
			if sc, ok := m.(protocol.SessionStateChanged); ok && sc.Status == "error" {
				sender.mu.Unlock()
				return // found the expected error — test passes
			}
		}
		sender.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected a session_state_changed(status=error), got none")
}

// TestAgentHostDriverErrorBecomesSessionError verifies that a driver-level
// failure (here: the claude-code driver with no `claude` on PATH) becomes a
// session_state_changed{status:"error"} and not just a transcript event — it
// is the only message that reaches SetSessionError, and so the only way the
// board card ever shows a reason instead of sitting at "running".
func TestAgentHostDriverErrorBecomesSessionError(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no `claude`
	reposRoot := t.TempDir()
	workDir := filepath.Join(reposRoot, "proj")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s1", Repo: "proj",
		InitialPrompt: "hello", Kind: "agent",
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sender.mu.Lock()
		for _, m := range sender.msgs {
			if sc, ok := m.(protocol.SessionStateChanged); ok && sc.Status == "error" {
				msg := ""
				if sc.Message != nil {
					msg = *sc.Message
				}
				sender.mu.Unlock()
				if !containsStr(msg, "claude-code") {
					t.Fatalf("error message must carry the driver's own reason, got %q", msg)
				}
				return
			}
		}
		sender.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("driver error never became a session_state_changed(status=error)")
}

// countKind reports how many non-transient events of a kind were shipped.
func (s *agentTestSender) countKind(kind string) int {
	n := 0
	for _, ev := range s.agentEvents() {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

func (s *agentTestSender) waitForKindCount(t *testing.T, kind string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.countKind(kind) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("saw %d %q events, want %d; got %v", s.countKind(kind), kind, want, s.kinds())
}

func (s *agentTestSender) sessionErrors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.msgs {
		if sc, ok := m.(protocol.SessionStateChanged); ok && sc.Status == "error" {
			msg := ""
			if sc.Message != nil {
				msg = *sc.Message
			}
			out = append(out, msg)
		}
	}
	return out
}

// A turn that ends badly is not a dead session. claude-code reports a failed
// turn as a `result` line with is_error — the driver emits it as an error
// event, and if that became session_state_changed{error} the board would
// settle the session, revoke its token and stop ingesting while the driver
// went on to turn_done/idle with the session still alive and usable. Only
// non-retryable (start/pipe/driver) failures may end a session.
func TestAgentHostPerTurnErrorDoesNotEndTheSession(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"cc-1"}'
echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"the model could not complete this turn"}'
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s1", Repo: "proj",
		InitialPrompt: "hello", Kind: "agent",
	})
	sender.waitForKind(t, "turn_done")

	// The failed turn IS in the transcript...
	if sender.countKind("error") == 0 {
		t.Fatal("the failed turn never reached the transcript")
	}
	// ...but it did not end the session.
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("a per-turn error ended the session: %v", errs)
	}

	// And the session still takes another turn.
	host.UserMessage("s1", "try again", "user")
	sender.waitForKindCount(t, "turn_done", 2)
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("a per-turn error ended the session: %v", errs)
	}
}

// failingThenScriptedProvider errors on its first Stream call (a provider /
// API failure, after the loop's own retry budget is spent) and behaves
// normally afterwards.
type failingThenScriptedProvider struct {
	mu     sync.Mutex
	failed bool
}

func (p *failingThenScriptedProvider) Stream(ctx context.Context, req agent.Request) (<-chan agent.StreamEvent, error) {
	p.mu.Lock()
	first := !p.failed
	p.failed = true
	p.mu.Unlock()
	if first {
		return nil, errors.New("provider unavailable: 503")
	}
	ch := make(chan agent.StreamEvent)
	go func() {
		defer close(ch)
		ch <- agent.StreamEvent{Kind: "block", Block: &agent.Block{Type: "text", Text: "recovered"}}
		ch <- agent.StreamEvent{Kind: "done", StopReason: "end_turn", Usage: &agent.Usage{}}
	}()
	return ch, nil
}

// The native loop's half of the same rule: a provider call failure is a turn
// outcome — the loop emits idle and keeps the session alive — so it must not
// become a terminal session_state_changed{error} that settles the session on
// the board while the daemon-side session is still taking turns.
func TestAgentHostNativeLoopProviderErrorDoesNotEndTheSession(t *testing.T) {
	provider := &failingThenScriptedProvider{}
	host, sender, _ := newTestHost(t, provider, "")
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s1", Repo: "proj",
		InitialPrompt: "hello", Kind: "agent",
	})

	// The failed turn reaches the transcript...
	sender.waitForKind(t, "error")
	// ...and the loop parks the session at idle rather than ending it.
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("a provider error ended the session: %v", errs)
	}

	// The session still completes a subsequent turn.
	host.UserMessage("s1", "try again", "user")
	sender.waitForKind(t, "turn_done")
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("a provider error ended the session: %v", errs)
	}
	if got := host.States()["s1"]; got != "idle" {
		t.Fatalf("session state = %q, want idle (still usable)", got)
	}
}

// TestAgentHostCodexEngineWorksWithoutAPIKey verifies the other half of the
// same fix: engine=codex must still work with no Provider/APIKey at all,
// since it never touches either — it shells out to the codex CLI, billed to
// the operator's own Codex/ChatGPT login.
func TestAgentHostCodexEngineWorksWithoutAPIKey(t *testing.T) {
	binDir := fakeCodex(t)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_LOG", filepath.Join(binDir, "calls.log"))

	reposRoot := t.TempDir()
	workDir := filepath.Join(reposRoot, "proj")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir()})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s1", Repo: "proj",
		InitialPrompt: "hello", Kind: "agent", Engine: "codex",
	})
	sender.waitForKind(t, "turn_done")

	// Regression check for a real bug caught live: spawn() defaulted an empty
	// model to "claude-sonnet-5" unconditionally, which got forwarded to
	// codex as --model claude-sonnet-5 — an invalid model name there, making
	// every codex agent-kind session fail immediately (exit status 1). The
	// fake binary here doesn't validate --model like the real CLI does, so
	// this has to assert on the actual args passed rather than just success.
	raw, err := os.ReadFile(filepath.Join(binDir, "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	if containsStr(string(raw), "claude-sonnet-5") {
		t.Errorf("codex invoked with the claude-only default model; calls:\n%s", raw)
	}
}

// TestAgentHostHermesEngineWorksWithoutAPIKey mirrors
// TestAgentHostCodexEngineWorksWithoutAPIKey for the third engine: engine=hermes
// must also work with no Provider/APIKey, and must not receive the
// claude-only default model.
func TestAgentHostHermesEngineWorksWithoutAPIKey(t *testing.T) {
	binDir := fakeHermes(t, "hi there", 0)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_HERMES_LOG", filepath.Join(binDir, "calls.log"))

	reposRoot := t.TempDir()
	workDir := filepath.Join(reposRoot, "proj")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir()})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s1", Repo: "proj",
		InitialPrompt: "hello", Kind: "agent", Engine: "hermes",
	})
	sender.waitForKind(t, "turn_done")

	raw, err := os.ReadFile(filepath.Join(binDir, "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	if containsStr(string(raw), "claude-sonnet-5") {
		t.Errorf("hermes invoked with the claude-only default model; calls:\n%s", raw)
	}
	if !containsStr(string(raw), "--continue s1") {
		t.Errorf("hermes not invoked with its own session id as --continue; calls:\n%s", raw)
	}
}

// TestAgentHostOpenclawEngineWorksWithoutAPIKey mirrors the Codex/Hermes
// versions for the third registered CLI engine: engine=openclaw must also
// work with no Provider/APIKey, and must not receive the claude-only
// default model.
func TestAgentHostOpenclawEngineWorksWithoutAPIKey(t *testing.T) {
	binDir := fakeOpenclaw(t, `{"ok":true,"status":"ok","final":"hi there","payloads":[{"text":"hi there","mediaUrl":null}]}`, 0)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_OPENCLAW_LOG", filepath.Join(binDir, "calls.log"))

	reposRoot := t.TempDir()
	workDir := filepath.Join(reposRoot, "proj")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir()})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s1", Repo: "proj",
		InitialPrompt: "hello", Kind: "agent", Engine: "openclaw",
	})
	sender.waitForKind(t, "turn_done")

	raw, err := os.ReadFile(filepath.Join(binDir, "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	if containsStr(string(raw), "claude-sonnet-5") {
		t.Errorf("openclaw invoked with the claude-only default model; calls:\n%s", raw)
	}
	if !containsStr(string(raw), "agent exec") {
		t.Errorf("openclaw not invoked with agent exec; calls:\n%s", raw)
	}
}

func containsStr(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
