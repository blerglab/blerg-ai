package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// newSteeringDriver starts a streaming-engine driver on the fake claude. The package runs with the kill
// switch on (testmain_test.go), so this turns the streaming engine on for this one driver.
func newSteeringDriver(t *testing.T, extraEnv ...string) (*claudeCodeDriver, *collectEmitter, string) {
	t.Helper()
	env, logPath := installFakeClaude(t, extraEnv...)
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, env)
	d.steer.legacy = false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("streaming driver did not stop")
		}
	})
	go func() { d.Run(ctx); close(done) }()
	return d, em, logPath
}

func (c *collectEmitter) snapshot() []agent.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]agent.Event(nil), c.events...)
}

// outline renders the events as short strings: "user:<text>", "status:running", "turn_done:<stop>", "error",
// "tool_call", "tool_result", "text:<text>".
func (c *collectEmitter) outline() []string {
	var out []string
	for _, e := range c.snapshot() {
		switch p := e.Payload.(type) {
		case agent.UserMessagePayload:
			out = append(out, "user:"+p.Text)
		case agent.StatusPayload:
			out = append(out, "status:"+p.Status)
		case agent.TurnDonePayload:
			out = append(out, "turn_done:"+p.StopReason)
		case agent.AssistantTextPayload:
			out = append(out, "text:"+p.Text)
		case agent.ErrorPayload:
			out = append(out, "error:"+p.Message)
		default:
			if e.Kind == "tool_call" || e.Kind == "tool_result" {
				out = append(out, e.Kind)
			}
		}
	}
	return out
}

func (c *collectEmitter) count(prefix string) int {
	n := 0
	for _, s := range c.outline() {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

func index(out []string, s string) int {
	for i, v := range out {
		if v == s {
			return i
		}
	}
	return -1
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitIdle(t *testing.T, em *collectEmitter, turns int) {
	t.Helper()
	waitUntil(t, "an idle status after the turn(s)", func() bool {
		o := em.outline()
		return em.count("turn_done") >= turns && len(o) > 0 && o[len(o)-1] == "status:idle"
	})
}

func logLines(t *testing.T, path string) []string {
	t.Helper()
	b, _ := os.ReadFile(path)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func starts(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, "start ") {
			out = append(out, l)
		}
	}
	return out
}

func shrink(t *testing.T, p *time.Duration, v time.Duration) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestSteeringEnabledByDefault(t *testing.T) {
	t.Setenv("BLERG_CLAUDE_STEERING", "")
	if !ccSteeringEnabled() {
		t.Fatal("steering must be on unless BLERG_CLAUDE_STEERING=0")
	}
	t.Setenv("BLERG_CLAUDE_STEERING", "0")
	if ccSteeringEnabled() {
		t.Fatal("BLERG_CLAUDE_STEERING=0 must select the per-turn engine")
	}
}

func TestReplayLineDecodesWithStringContent(t *testing.T) {
	var ev ccLine
	line := `{"type":"user","message":{"role":"user","content":"hello there"},"isReplay":true,"uuid":"u-1","session_id":"s"}`
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	txt, ok := ev.text()
	if !ev.IsReplay || ev.UUID != "u-1" || !ok || txt != "hello there" || ev.blocks() != nil {
		t.Fatalf("replay decoded wrongly: %+v text=%q ok=%v", ev, txt, ok)
	}
	var tr ccLine
	if err := json.Unmarshal([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}}`), &tr); err != nil {
		t.Fatal(err)
	}
	if _, isString := tr.text(); isString || len(tr.blocks()) != 1 {
		t.Fatalf("tool result decoded wrongly: %+v", tr)
	}
}

func TestSteeringTwoTurnsOneProcess(t *testing.T) {
	d, em, logPath := newSteeringDriver(t)
	d.Enqueue("one", "chat")
	waitIdle(t, em, 1)
	d.Enqueue("two", "ask_answer")
	waitIdle(t, em, 2)

	o := em.outline()
	want := []string{"status:running", "user:one", "text:ok:one", "turn_done:end_turn", "status:idle",
		"status:running", "user:two", "text:ok:two", "turn_done:end_turn", "status:idle"}
	if strings.Join(o, ",") != strings.Join(want, ",") {
		t.Fatalf("events:\n got  %v\n want %v", o, want)
	}
	if n := len(starts(logLines(t, logPath))); n != 1 {
		t.Fatalf("processes started = %d, want 1 for both turns", n)
	}
	// the source of each message is the one it was sent with
	var sources []string
	for _, e := range em.snapshot() {
		if p, ok := e.Payload.(agent.UserMessagePayload); ok {
			sources = append(sources, p.Source)
		}
	}
	if strings.Join(sources, ",") != "chat,ask_answer" {
		t.Fatalf("sources = %v", sources)
	}
	args := starts(logLines(t, logPath))[0]
	if !strings.Contains(args, "--input-format stream-json") || !strings.Contains(args, "--replay-user-messages") {
		t.Fatalf("streaming flags missing: %s", args)
	}
}

func TestSteeringMidTurnMessageFoldsIntoTheTurn(t *testing.T) {
	d, em, _ := newSteeringDriver(t, "FAKE_TOOL_MS=500")
	d.Enqueue("TOOL work", "chat")
	waitUntil(t, "the tool call", func() bool { return em.count("tool_call") == 1 })
	d.Enqueue("also this", "chat")
	waitIdle(t, em, 1)

	o := em.outline()
	if em.count("turn_done") != 1 {
		t.Fatalf("one turn expected, got %v", o)
	}
	first, tool, result, second := index(o, "user:TOOL work"), index(o, "tool_call"), index(o, "tool_result"), index(o, "user:also this")
	if first >= tool || tool >= result || result >= second {
		t.Fatalf("the steering message must show up after the running tool finished: %v", o)
	}
	if index(o, "text:ok:TOOL work|also this") < second {
		t.Fatalf("the answer must cover both messages: %v", o)
	}
}

func TestSteeringMessageInTheGapBeforeTheReplayIsNotLost(t *testing.T) {
	// two messages written back to back, before either is echoed: both get answered, the session never
	// shows idle in between
	d, em, _ := newSteeringDriver(t, "FAKE_REPLAY_MS=150")
	d.Enqueue("a", "chat")
	d.Enqueue("b", "chat")
	waitIdle(t, em, 2)
	if em.count("status:idle") != 1 || em.count("status:running") != 1 {
		t.Fatalf("expected one busy stretch, got %v", em.outline())
	}
	o := em.outline()
	if index(o, "user:a") >= index(o, "user:b") || index(o, "text:ok:a") < 0 || index(o, "text:ok:b") < 0 {
		t.Fatalf("both messages must be shown in order and answered: %v", o)
	}
}

func TestSteeringInterruptCutsTheTurnWithoutAnError(t *testing.T) {
	d, em, logPath := newSteeringDriver(t, "FAKE_TOOL_MS=5000")
	d.Enqueue("TOOL long", "chat")
	waitUntil(t, "the tool call", func() bool { return em.count("tool_call") == 1 })
	d.Interrupt()
	waitIdle(t, em, 1)
	if em.count("error") != 0 {
		t.Fatalf("an interrupt is not an error: %v", em.outline())
	}
	if !contains(em.outline(), "turn_done:interrupted") {
		t.Fatalf("turn_done must say interrupted: %v", em.outline())
	}
	// the process and its conversation survive: the next message goes to the same one
	d.Enqueue("after", "chat")
	waitIdle(t, em, 2)
	if n := len(starts(logLines(t, logPath))); n != 1 {
		t.Fatalf("an interrupt must not restart the process (%d starts)", n)
	}
	if !contains(em.outline(), "text:ok:after") {
		t.Fatalf("the next message was not answered: %v", em.outline())
	}
}

func TestSteeringInterruptRunsTheQueuedMessageNext(t *testing.T) {
	d, em, _ := newSteeringDriver(t, "FAKE_TOOL_MS=5000")
	d.Enqueue("TOOL long", "chat")
	waitUntil(t, "the tool call", func() bool { return em.count("tool_call") == 1 })
	d.Enqueue("take over", "chat")
	time.Sleep(100 * time.Millisecond) // written to the process
	d.Interrupt()
	waitIdle(t, em, 2)
	o := em.outline()
	if em.count("error") != 0 || index(o, "turn_done:interrupted") < 0 || index(o, "user:take over") < index(o, "turn_done:interrupted") {
		t.Fatalf("interrupted turn, then the queued message as its own turn: %v", o)
	}
	if index(o, "text:ok:take over") < 0 {
		t.Fatalf("queued message not answered: %v", o)
	}
	if em.count("status:idle") != 1 {
		t.Fatalf("no idle blink between the interrupted turn and the next: %v", o)
	}
}

func TestSteeringInterruptNotHonouredKillsAndRestartsWithResume(t *testing.T) {
	shrink(t, &ccInterruptKillAfter, 200*time.Millisecond)
	d, em, logPath := newSteeringDriver(t)
	d.Enqueue("HANG forever", "chat")
	waitUntil(t, "the tool call", func() bool { return em.count("tool_call") == 1 })
	d.Interrupt()
	waitIdle(t, em, 1)
	if em.count("turn_done") != 1 || !contains(em.outline(), "turn_done:interrupted") || em.count("error") != 0 {
		t.Fatalf("exactly one interrupted turn_done and no error: %v", em.outline())
	}
	d.Enqueue("again", "chat")
	waitIdle(t, em, 2)
	st := starts(logLines(t, logPath))
	if len(st) != 2 || !strings.Contains(st[1], "--resume fake-sess-") {
		t.Fatalf("the replacement must resume the session: %v", st)
	}
	if !contains(em.outline(), "text:ok:again") {
		t.Fatalf("not answered after the restart: %v", em.outline())
	}
}

func TestSteeringLateResultIsNotKilledByAStaleTimer(t *testing.T) {
	shrink(t, &ccInterruptKillAfter, 600*time.Millisecond)
	d, em, logPath := newSteeringDriver(t, "FAKE_TOOL_MS=5000")
	d.Enqueue("TOOL long", "chat")
	waitUntil(t, "the tool call", func() bool { return em.count("tool_call") == 1 })
	d.Interrupt() // honoured at once
	waitIdle(t, em, 1)
	d.Enqueue("next", "chat")
	waitIdle(t, em, 2)
	time.Sleep(900 * time.Millisecond) // past the kill timer of the first interrupt
	if n := len(starts(logLines(t, logPath))); n != 1 {
		t.Fatalf("a stale kill timer restarted the process (%d starts)", n)
	}
}

func TestSteeringCrashRetriesOnceThenReports(t *testing.T) {
	d, em, logPath := newSteeringDriver(t)
	d.Enqueue("CRASH now", "chat")
	waitUntil(t, "the failure report", func() bool { return em.count("error:message not delivered") == 1 })
	waitUntil(t, "idle", func() bool { o := em.outline(); return o[len(o)-1] == "status:idle" })
	if n := len(starts(logLines(t, logPath))); n != 2 {
		t.Fatalf("one retry expected (%d starts)", n)
	}
	if em.count("error") != 3 { // two exits and the report that the message was not delivered
		t.Fatalf("unexpected number of error cards: %v", em.outline())
	}
	if em.count("turn_done") != 0 {
		t.Fatalf("no turn ever began, so none may end: %v", em.outline())
	}
	if em.count("user:CRASH now") != 1 {
		t.Fatalf("the message must be shown exactly once: %v", em.outline())
	}
	// and the driver still works afterwards
	d.Enqueue("fine", "chat")
	waitUntil(t, "an answer", func() bool { return contains(em.outline(), "text:ok:fine") })
}

func TestSteeringSlashCommandWithoutAReplayIsShownAfterTheSettleTime(t *testing.T) {
	shrink(t, &ccSettleAfter, 250*time.Millisecond)
	d, em, _ := newSteeringDriver(t)
	d.Enqueue("SLASH /compact", "chat")
	waitIdle(t, em, 1)
	if em.count("user:SLASH /compact") != 1 {
		t.Fatalf("the command must be shown once: %v", em.outline())
	}
	// a later real message still works and is not confused with it
	d.Enqueue("plain", "chat")
	waitIdle(t, em, 2)
	if !contains(em.outline(), "text:ok:plain") || em.count("user:plain") != 1 {
		t.Fatalf("%v", em.outline())
	}
}

func TestSteeringAutonomousTurnShowsAsRunning(t *testing.T) {
	d, em, _ := newSteeringDriver(t)
	d.Enqueue("AUTO go", "chat")
	waitUntil(t, "the background turn", func() bool { return contains(em.outline(), "text:background done") })
	waitIdle(t, em, 2)
	o := em.outline()
	if em.count("status:running") != 2 || em.count("status:idle") != 2 {
		t.Fatalf("the autonomous turn needs its own running/idle: %v", o)
	}
	if em.count("user:") != 1 {
		t.Fatalf("an autonomous turn has no user message: %v", o)
	}
}

func TestSteeringModelChangeRestartsAtTheIdlePoint(t *testing.T) {
	d, em, logPath := newSteeringDriver(t, "FAKE_TOOL_MS=600")
	d.Enqueue("TOOL work", "chat")
	waitUntil(t, "the tool call", func() bool { return em.count("tool_call") == 1 })
	d.SetModel("claude-sonnet-5", "", "chat") // mid-turn: applies from the next idle point
	time.Sleep(100 * time.Millisecond)
	if n := len(starts(logLines(t, logPath))); n != 1 {
		t.Fatalf("no restart mid-turn (%d starts)", n)
	}
	waitIdle(t, em, 1)
	waitUntil(t, "the old process to close", func() bool { return contains(logLines(t, logPath), "eof") })
	d.Enqueue("next", "chat")
	waitIdle(t, em, 2)
	st := starts(logLines(t, logPath))
	if len(st) != 2 || !strings.Contains(st[1], "--model claude-sonnet-5") || !strings.Contains(st[1], "--resume fake-sess-") {
		t.Fatalf("the new process must carry the new model and resume: %v", st)
	}
	var models []string
	for _, e := range em.snapshot() {
		if p, ok := e.Payload.(agent.TurnDonePayload); ok {
			models = append(models, p.Model)
		}
	}
	if strings.Join(models, ",") != "claude-opus-5,claude-sonnet-5" {
		t.Fatalf("each turn_done must name the model its process ran: %v", models)
	}
}

func TestSteeringIdleShutdownThenResume(t *testing.T) {
	shrink(t, &ccIdleShutdown, 150*time.Millisecond)
	d, em, logPath := newSteeringDriver(t)
	d.Enqueue("one", "chat")
	waitIdle(t, em, 1)
	waitUntil(t, "the idle shutdown", func() bool { return contains(logLines(t, logPath), "eof") })
	d.Enqueue("two", "chat")
	waitIdle(t, em, 2)
	st := starts(logLines(t, logPath))
	if len(st) != 2 || !strings.Contains(st[1], "--resume fake-sess-") {
		t.Fatalf("restart after the idle shutdown must resume: %v", st)
	}
	if em.count("error") != 0 {
		t.Fatalf("a clean idle shutdown is not a crash: %v", em.outline())
	}
}

func TestSteeringUnknownOptionFallsBackToThePerTurnEngine(t *testing.T) {
	d, em, _ := newSteeringDriver(t, "FAKE_MODE=unknown")
	d.Enqueue("hello", "chat")
	waitIdle(t, em, 1)
	if !contains(em.outline(), "text:ok:hello") {
		t.Fatalf("the message must be answered by the per-turn engine: %v", em.outline())
	}
	if d.steering() {
		t.Fatal("the driver must have switched to the per-turn engine for good")
	}
	d.Enqueue("again", "chat")
	waitIdle(t, em, 2)
	if !contains(em.outline(), "text:ok:again") {
		t.Fatalf("%v", em.outline())
	}
}

func TestSteeringKillMidTurnSaysNothing(t *testing.T) {
	env, _ := installFakeClaude(t, "FAKE_TOOL_MS=5000")
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, env)
	d.steer.legacy = false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	d.Enqueue("TOOL long", "chat")
	waitUntil(t, "the tool call", func() bool { return em.count("tool_call") == 1 })
	before := len(em.snapshot())
	cancel() // AgentHost.Kill: the context is cancelled and the process goes with it
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	time.Sleep(300 * time.Millisecond)
	for _, s := range em.outline()[before:] {
		if strings.HasPrefix(s, "error") || strings.HasPrefix(s, "turn_done") {
			t.Fatalf("a killed session must not report an error or a turn end: %v", em.outline()[before:])
		}
	}
}

func TestSteeringQueueIsBounded(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.steer.legacy = false
	for i := 0; i < ccMaxPending+1; i++ {
		d.Enqueue("m", "chat")
	}
	if em.count("error:message queue full") != 1 {
		t.Fatalf("the message over the cap must be refused with an error: %v", em.outline())
	}
}

func TestSteeringRaceStress(t *testing.T) {
	// many messages and interrupts at once: the point is that -race and the state machine hold up
	d, em, _ := newSteeringDriver(t, "FAKE_TOOL_MS=60")
	for i := 0; i < 12; i++ {
		d.Enqueue("TOOL step", "chat")
		if i%3 == 0 {
			d.Interrupt()
		}
		time.Sleep(15 * time.Millisecond)
	}
	waitUntil(t, "everything to settle", func() bool {
		o := em.outline()
		return len(o) > 0 && o[len(o)-1] == "status:idle" && em.count("user:TOOL step") == 12
	})
	if em.count("error") != 0 {
		t.Fatalf("no errors expected: %v", em.outline())
	}
}

func TestSteeringBadResumeReportsOnceAndContinuesFresh(t *testing.T) {
	d, em, logPath := newSteeringDriver(t)
	d.mu.Lock()
	d.ccSessionID = "bad-session"
	d.mu.Unlock()
	d.Enqueue("hello", "chat")
	waitIdle(t, em, 1)
	o := em.outline()
	if em.count("error") != 1 || !strings.Contains(strings.Join(o, "\n"), "could not be resumed") {
		t.Fatalf("one error card about the lost conversation, not a pile: %v", o)
	}
	if !contains(o, "text:ok:hello") || em.count("user:hello") != 1 {
		t.Fatalf("the message must still be answered, once: %v", o)
	}
	st := starts(logLines(t, logPath))
	if len(st) != 2 || !strings.Contains(st[0], "--resume bad-session") || strings.Contains(st[1], "--resume") {
		t.Fatalf("the retry must start a fresh conversation: %v", st)
	}
}

func TestSteeringSlowButAcknowledgedInterruptIsNotKilled(t *testing.T) {
	shrink(t, &ccInterruptKillAfter, 200*time.Millisecond)
	shrink(t, &ccInterruptResultAfter, 5*time.Second)
	d, em, logPath := newSteeringDriver(t, "FAKE_TOOL_MS=5000", "FAKE_CUT_MS=700")
	d.Enqueue("SLOWCUT work", "chat")
	waitUntil(t, "the tool call", func() bool { return em.count("tool_call") == 1 })
	d.Interrupt() // acknowledged at once, but the turn ends 700 ms later: past the short timer
	waitIdle(t, em, 1)
	if !contains(em.outline(), "turn_done:interrupted") || em.count("error") != 0 {
		t.Fatalf("%v", em.outline())
	}
	if n := len(starts(logLines(t, logPath))); n != 1 {
		t.Fatalf("an acknowledged interrupt must not be killed (%d starts)", n)
	}
}

func TestSteeringEscInTheGapBeforeTheReplayCutsTheMessage(t *testing.T) {
	d, em, logPath := newSteeringDriver(t, "FAKE_REPLAY_MS=300")
	d.Enqueue("x", "chat")
	waitUntil(t, "the message to be written", func() bool { return em.count("status:running") == 1 })
	d.Interrupt() // the message is with the CLI but not echoed yet
	waitIdle(t, em, 1)
	o := em.outline()
	if !contains(o, "turn_done:interrupted") || em.count("error") != 0 || contains(o, "text:ok:x") {
		t.Fatalf("the message's turn must be cut before it answers: %v", o)
	}
	time.Sleep(6 * time.Second / 5) // well past where a stale kill timer would fire
	d.Enqueue("y", "chat")
	waitIdle(t, em, 2)
	if n := len(starts(logLines(t, logPath))); n != 1 {
		t.Fatalf("no process restart expected (%d starts)", n)
	}
}

func TestSteeringIgnoresOutputOfAProcessBeingKilled(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.steer.legacy = false
	p := &ccProc{dying: true, killedByDriver: true}
	d.steer.proc = p
	d.onLine(p, []byte(`{"type":"user","message":{"role":"user","content":"late"},"isReplay":true,"uuid":"u1"}`))
	d.onLine(p, []byte(`{"type":"result","subtype":"success","is_error":false,"result":"late"}`))
	d.onLine(p, []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"late"}]}}`))
	if len(em.snapshot()) != 0 {
		t.Fatalf("a killed process must not speak: %v", em.outline())
	}
}

func TestSteeringHandsQueuedMessagesToThePerTurnEngineInOrder(t *testing.T) {
	d, em, _ := newSteeringDriver(t, "FAKE_MODE=unknown")
	d.Enqueue("first", "chat")
	d.Enqueue("second", "chat")
	d.Enqueue("third", "chat")
	waitUntil(t, "all three answers", func() bool { return em.count("turn_done") >= 3 })
	o := em.outline()
	a, b, c := index(o, "text:ok:first"), index(o, "text:ok:second"), index(o, "text:ok:third")
	if a < 0 || b < a || c < b {
		t.Fatalf("messages must be answered in order, none lost: %v", o)
	}
}

// A user-role message the CLI wrote itself — a subagent's completion, a system reminder — is
// not the person's: it reaches the transcript as source "system" (hidden), never as "chat".
func TestSteeringKeepsHarnessMessagesOffThePersonsName(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.steer.legacy = false
	p := &ccProc{}
	d.steer.proc = p
	for i, text := range []string{
		"<task-notification>\n<task-id>abc</task-id>\n<summary>Agent \"Research\" finished</summary>\n</task-notification>",
		"<system-reminder>\nThe file changed on disk.\n</system-reminder>",
		"[SYSTEM NOTIFICATION - NOT USER INPUT]\nThis is an automated event.",
		"a line a hook injected",
	} {
		line := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q},"isReplay":true,"uuid":"h%d"}`, text, i)
		d.onLine(p, []byte(line))
	}
	var sources []string
	for _, ev := range em.snapshot() {
		if ev.Kind == "user_message" {
			sources = append(sources, ev.Payload.(agent.UserMessagePayload).Source)
		}
	}
	if want := []string{"system", "system", "system", "chat"}; !slices.Equal(sources, want) {
		t.Fatalf("sources = %v, want %v", sources, want)
	}
}

// The engine gets an input's Text (a skill's instructions); the transcript records its Shown text
// (what the person typed) with the client id it was sent with.
func TestSteeringRecordsShownTextAndClientID(t *testing.T) {
	d, em, _ := newSteeringDriver(t)
	d.EnqueueInput(agent.Input{Text: "SKILL BODY", Shown: "/review x", Source: "chat", ClientID: "cl-7"})
	waitIdle(t, em, 1)
	o := em.outline()
	if index(o, "user:/review x") < 0 || index(o, "text:ok:SKILL BODY") < 0 {
		t.Fatalf("events = %v: want the typed text recorded and the skill body answered", o)
	}
	for _, e := range em.snapshot() {
		if p, ok := e.Payload.(agent.UserMessagePayload); ok && p.ClientID != "cl-7" {
			t.Fatalf("user_message = %+v, want client id cl-7", p)
		}
	}
}

// A second interrupt can already be in stdin when the first interrupted turn ends. Its ACK
// belongs to the following turn; the preceding result must not erase its interruption state.
func TestSteeringInterruptAckAfterPreviousResult(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.steer.legacy = false
	p := &ccProc{gen: 1, sawInit: true, inTurn: true, writeCh: make(chan []byte, 8)}
	d.steer.proc, d.steer.running = p, true
	t.Cleanup(func() {
		d.sm.Lock()
		defer d.sm.Unlock()
		d.stopTimerLocked(&d.steer.killTimer)
		d.stopTimerLocked(&d.steer.settleTimer)
		d.stopIdleTimerLocked()
	})
	ack := func(id string) {
		d.onControlResponse(p, []byte(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q}}`, id)))
	}
	d.Interrupt()
	ack(p.interruptID)
	d.Interrupt() // queued for the next CLI turn while its predecessor is finishing
	second := p.interruptID
	d.Interrupt() // a third request must not hide the second request's later ACK
	third := p.interruptID
	d.onLine(p, []byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`))
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	ack(second)
	d.onLine(p, []byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`))
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	ack(third)
	d.onLine(p, []byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`))
	if em.count("error") != 0 || em.count("turn_done:interrupted") != 3 {
		t.Fatalf("all interrupted turns must be recognized: %v", em.outline())
	}
	// The acknowledgment is consumed: a later, unrelated CLI failure remains a failure.
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	d.onLine(p, []byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`))
	if em.count("error") != 1 {
		t.Fatalf("unrelated failure hidden: %v", em.outline())
	}
}

func TestSteeringOutstandingInterruptsAreBounded(t *testing.T) {
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", &collectEmitter{}, nil)
	d.steer.legacy = false
	p := &ccProc{inTurn: true, writeCh: make(chan []byte, ccMaxPending*2)}
	d.steer.proc, d.steer.running = p, true
	t.Cleanup(func() { d.sm.Lock(); defer d.sm.Unlock(); d.stopTimerLocked(&d.steer.killTimer) })
	for range ccMaxPending * 2 {
		d.Interrupt()
	}
	if len(p.interrupts) != ccMaxPending || len(p.writeCh) != ccMaxPending {
		t.Fatalf("pending=%d sent=%d", len(p.interrupts), len(p.writeCh))
	}
}

func TestSteeringInterruptAckBeforeInit(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.steer.legacy = false
	p := &ccProc{gen: 1, writeCh: make(chan []byte, 8)}
	d.steer.proc, d.steer.running = p, true
	d.steer.awaiting = []awaitMsg{{uuid: "pending"}}
	t.Cleanup(func() {
		d.sm.Lock()
		defer d.sm.Unlock()
		d.stopTimerLocked(&d.steer.killTimer)
		d.stopTimerLocked(&d.steer.settleTimer)
		d.stopIdleTimerLocked()
	})
	d.Interrupt()
	d.onControlResponse(p, []byte(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q}}`, p.interruptID)))
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	d.onLine(p, []byte(`{"type":"user","uuid":"pending","isReplay":true,"message":{"content":"queued work"}}`))
	d.onLine(p, []byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`))
	if em.count("error") != 0 || em.count("turn_done:interrupted") != 1 {
		t.Fatalf("early acknowledged interrupt became an engine error: %v", em.outline())
	}
}
func TestSteeringFailedInterruptsDoNotDisableFutureStop(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.steer.legacy = false
	p := &ccProc{gen: 1, writeCh: make(chan []byte, 2)}
	d.steer.proc = p
	t.Cleanup(func() {
		d.sm.Lock()
		defer d.sm.Unlock()
		d.stopTimerLocked(&d.steer.killTimer)
		d.stopTimerLocked(&d.steer.settleTimer)
		d.stopIdleTimerLocked()
	})
	for range ccMaxPending {
		d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
		d.Interrupt()
		<-p.writeCh
		d.onControlResponse(p, []byte(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"error","request_id":%q,"error":"turn already finished"}}`, p.interruptID)))
		d.onLine(p, []byte(`{"type":"result","subtype":"success"}`))
	}
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	d.Interrupt()
	if len(p.writeCh) != 1 {
		t.Fatal("completed error responses permanently disable Interrupt for this process")
	}
}

// The same early ACK ordering also occurs between turns of an existing process.
func TestSteeringInterruptAckBeforeNextInit(t *testing.T) {
	d, p, em := newInterruptFixture(t)
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	d.onLine(p, []byte(`{"type":"result","subtype":"success"}`))
	d.steer.awaiting = []awaitMsg{{uuid: "pending"}}
	d.steer.running = true
	d.Interrupt()
	interruptReply(d, p, p.interruptID, "success")
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	d.onLine(p, []byte(`{"type":"user","uuid":"pending","isReplay":true,"message":{"content":"queued work"}}`))
	d.onLine(p, []byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`))
	if em.count("error") != 0 || em.count("turn_done:interrupted") != 1 {
		t.Fatalf("acknowledged next turn failed: %v", em.outline())
	}
}

func TestSteeringInterruptAckBeforeInitArmsResultTimeout(t *testing.T) {
	d, p, em := newInterruptFixture(t)
	d.steer.running = true
	d.steer.awaiting = []awaitMsg{{uuid: "pending"}}
	d.Interrupt()
	interruptReply(d, p, p.interruptID, "success")
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	d.sm.Lock()
	timer := d.steer.killTimer
	if timer != nil {
		timer.Reset(0) // exercise the callback captured for the new turn sequence
	}
	d.sm.Unlock()
	if timer == nil {
		t.Fatal("early ACK did not arm the acknowledged-result timeout at init")
	}
	waitUntil(t, "the acknowledged turn timeout", func() bool { return em.count("turn_done:interrupted") == 1 })
	d.sm.Lock()
	killed := p.killedByDriver && p.dying
	d.sm.Unlock()
	if !killed || em.count("error") != 0 {
		t.Fatalf("acknowledged turn was not stopped: %v", em.outline())
	}
}

func TestSteeringIdleInterruptAckDoesNotHideNextError(t *testing.T) {
	d, p, em := newInterruptFixture(t)
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	d.Interrupt()
	id := p.interruptID
	d.onLine(p, []byte(`{"type":"result","subtype":"success"}`))
	interruptReply(d, p, id, "success") // no written message awaits init
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	d.onLine(p, []byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`))
	if em.count("error") != 1 || em.count("turn_done:interrupted") != 0 {
		t.Fatalf("idle ACK hid an unrelated failure: %v", em.outline())
	}
}

func TestSteeringAbandonedInterruptsExpire(t *testing.T) {
	d, p, em := newInterruptFixture(t)
	for range ccMaxPending {
		d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
		d.Interrupt()
		<-p.writeCh
		d.onLine(p, []byte(`{"type":"result","subtype":"success"}`))
	}
	if len(p.interrupts) != ccMaxPending {
		t.Fatalf("results discarded outstanding ACKs: %d", len(p.interrupts))
	}
	// Advance request ages without wall-clock sleeps. Keep one recent request to
	// prove expiry does not evict an outstanding ACK that can still arrive.
	recent := p.interruptID
	expired := ""
	for id := range p.interrupts {
		if id != recent {
			p.interrupts[id] = time.Now().Add(-time.Second)
			expired = id
		}
	}
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	d.Interrupt()
	if len(p.writeCh) != 1 || len(p.interrupts) != 2 {
		t.Fatalf("abandoned requests disabled Stop: pending=%d sent=%d", len(p.interrupts), len(p.writeCh))
	}
	interruptReply(d, p, recent, "success")
	d.onLine(p, []byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`))
	if em.count("error") != 0 || em.count("turn_done:interrupted") != 1 {
		t.Fatalf("recent outstanding ACK was lost: %v", em.outline())
	}
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	interruptReply(d, p, expired, "success")
	d.onLine(p, []byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`))
	if em.count("error") != 1 {
		t.Fatalf("expired ACK hid an unrelated failure: %v", em.outline())
	}
}

func TestSteeringSaturatedInterruptsKeepKillFallback(t *testing.T) {
	d, p, em := newInterruptFixture(t)
	for range ccMaxPending {
		d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
		d.Interrupt()
		<-p.writeCh
		d.onLine(p, []byte(`{"type":"result","subtype":"success"}`))
	}
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	d.Interrupt()
	if len(p.interrupts) != ccMaxPending || len(p.writeCh) != 0 {
		t.Fatalf("saturation evicted outstanding requests: pending=%d sent=%d", len(p.interrupts), len(p.writeCh))
	}
	d.sm.Lock()
	timer := d.steer.killTimer
	if timer != nil {
		timer.Reset(0)
	}
	d.sm.Unlock()
	if timer == nil {
		t.Fatal("saturated interrupt budget disabled the kill fallback")
	}
	waitUntil(t, "the saturated Stop fallback", func() bool { return em.count("turn_done:interrupted") == 1 })
}

// Stop must survive the written-to-running transition even without a success ACK.
func TestSteeringInterruptBeforeInitKeepsKillFallback(t *testing.T) {
	for _, reply := range []string{"unacknowledged", "rejected", "saturated", "expired-before-init"} {
		t.Run(reply, func(t *testing.T) {
			d, p, em := newInterruptFixture(t)
			if reply == "saturated" {
				p.interrupts = make(map[string]time.Time)
				for i := range ccMaxPending {
					p.interrupts[fmt.Sprint(i)] = time.Now().Add(time.Minute)
				}
			}
			d.steer.running = true
			d.steer.awaiting = []awaitMsg{{uuid: "pending"}}
			d.Interrupt()
			if reply == "rejected" {
				interruptReply(d, p, p.interruptID, "error")
			}
			if reply == "saturated" && (len(p.interrupts) != ccMaxPending || len(p.writeCh) != 0) {
				t.Fatal("full-capacity Stop must retain outstanding requests without sending another")
			}
			oldTurn := d.steer.turnSeq
			if reply == "expired-before-init" {
				d.interruptTimeout(p.gen, oldTurn)
			}
			d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
			// A callback already dispatched before init must remain fenced out.
			d.interruptTimeout(p.gen, oldTurn)
			d.sm.Lock()
			staleKilled := p.killedByDriver
			timer := d.steer.killTimer
			if timer != nil {
				timer.Reset(0) // run the actual fallback captured for the started turn
			}
			d.sm.Unlock()
			if staleKilled || timer == nil {
				t.Fatal("init must fence the old callback and retain a fallback for the started turn")
			}
			deadline := time.Now().Add(time.Second)
			for em.count("turn_done:interrupted") == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			d.sm.Lock()
			killed := p.killedByDriver && p.dying
			d.sm.Unlock()
			if !killed || em.count("turn_done:interrupted") != 1 || em.count("error") != 0 {
				t.Fatalf("pre-init %s Stop lost its fallback: %v", reply, em.outline())
			}
		})
	}
}

func TestSteeringInterruptBeforeInitClearedWhenSettled(t *testing.T) {
	for _, end := range []string{"result", "settlement"} {
		t.Run(end, func(t *testing.T) {
			d, p, em := newInterruptFixture(t)
			// An existing process can have a written turn between results and init.
			d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
			d.onLine(p, []byte(`{"type":"result","subtype":"success"}`))
			d.steer.running = true
			d.steer.awaiting = []awaitMsg{{uuid: "pending"}}
			d.Interrupt()
			oldTurn := d.steer.turnSeq
			d.sm.Lock()
			oldTimer := d.steer.killTimer
			d.sm.Unlock()
			// Expiry before init must not kill an idle process or discard Stop intent.
			d.interruptTimeout(p.gen, oldTurn)
			if p.killedByDriver {
				t.Fatal("pre-init fallback killed a process with no active turn")
			}
			if end == "result" {
				d.onLine(p, []byte(`{"type":"result","subtype":"success"}`))
				d.steer.awaiting = nil
			} else {
				d.settleFired(p.gen, oldTurn)
			}
			d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
			d.interruptTimeout(p.gen, oldTurn)
			d.sm.Lock()
			killed, timer := p.killedByDriver, d.steer.killTimer
			d.sm.Unlock()
			if killed || (end == "result" && timer != nil) || (end == "settlement" && timer != oldTimer) {
				t.Fatalf("settled Stop reached an unrelated turn: %v", em.outline())
			}
		})
	}
}

func TestSteeringInterruptBeforeInitIgnoredByEngine(t *testing.T) {
	shrink(t, &ccInterruptKillAfter, 200*time.Millisecond)
	d, em, _ := newSteeringDriver(t, "FAKE_REPLAY_MS=100")
	d.Enqueue("HANG forever", "chat")
	waitUntil(t, "written pending turn", func() bool {
		d.sm.Lock()
		defer d.sm.Unlock()
		return d.steer.running && len(d.steer.awaiting) > 0 && d.steer.proc != nil && !d.steer.proc.inTurn
	})
	d.sm.Lock()
	p := d.steer.proc
	d.sm.Unlock()
	d.Interrupt()
	waitUntil(t, "the uncooperative tool", func() bool { return em.count("tool_call") == 1 })
	// Observe the natural timer; do not reset it or invoke its callback.
	deadline := time.Now().Add(3 * ccInterruptKillAfter)
	for em.count("turn_done:interrupted") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	d.sm.Lock()
	killed := p.killedByDriver
	d.sm.Unlock()
	if !killed || em.count("turn_done:interrupted") != 1 || em.count("error") != 0 {
		t.Fatalf("engine ignored Stop and its pre-init fallback did not kill it: %v", em.outline())
	}
}

func TestSteeringExpiredInterruptAckDoesNotHideNextError(t *testing.T) {
	d, p, em := newInterruptFixture(t)
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	d.Interrupt()
	id := p.interruptID
	p.interrupts[id] = time.Now().Add(-time.Second)
	d.onLine(p, []byte(`{"type":"result","subtype":"success"}`))
	d.onLine(p, []byte(`{"type":"system","subtype":"init","session_id":"fixture"}`))
	interruptReply(d, p, id, "success") // expired, but not yet pruned by another Interrupt
	d.onLine(p, []byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`))
	if len(p.interrupts) != 0 || em.count("error") != 1 {
		t.Fatalf("expired ACK hid an unrelated failure: %v", em.outline())
	}
}

func newInterruptFixture(t *testing.T) (*claudeCodeDriver, *ccProc, *collectEmitter) {
	t.Helper()
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.steer.legacy = false
	// No child process is needed: timeout tests exercise the real cancellation path
	// with an empty command, whose Process is nil.
	p := &ccProc{gen: 1, cmd: &exec.Cmd{}, writeCh: make(chan []byte, ccMaxPending*2)}
	d.steer.proc = p
	t.Cleanup(func() {
		d.sm.Lock()
		defer d.sm.Unlock()
		d.stopTimerLocked(&d.steer.killTimer)
		d.stopTimerLocked(&d.steer.settleTimer)
		d.stopIdleTimerLocked()
	})
	return d, p, em
}

func interruptReply(d *claudeCodeDriver, p *ccProc, id, subtype string) {
	d.onControlResponse(p, []byte(fmt.Sprintf(`{"type":"control_response","response":{"subtype":%q,"request_id":%q}}`, subtype, id)))
}
