package livestate

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// script writes a session's events one minute apart, seq from 1 (the topics tests' pattern).
type script struct {
	seq    int64
	at     time.Time
	events []Event
}

func newScript() *script { return &script{at: time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)} }

func (s *script) add(kind string, payload any) Event {
	return s.addID(kind, "", payload)
}

func (s *script) addID(kind, id string, payload any) Event {
	s.seq++
	s.at = s.at.Add(time.Minute)
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	if id == "" {
		id = fmt.Sprintf("e%d", s.seq)
	}
	ev := Event{Seq: s.seq, ID: id, Kind: kind, Payload: raw, Ts: s.at}
	s.events = append(s.events, ev)
	return ev
}

func (s *script) wait(d time.Duration) { s.at = s.at.Add(d) }

func (s *script) turnDone() { s.add("turn_done", agent.TurnDonePayload{StopReason: "end_turn"}) }
func (s *script) msg(source, text string) Event {
	return s.add("user_message", agent.UserMessagePayload{Text: text, Source: source})
}
func (s *script) answer(messageID, text string) Event {
	return s.add("user_message", agent.UserMessagePayload{Text: text, Source: "ask_answer", MessageID: messageID})
}

// serverAsk is an ask `blerg-runner ask` posted: the server records it under a client id derived
// from its message id (here "srv-<id>").
func (s *script) serverAsk(messageID, question string) Event {
	return s.addID("ask", "srv-"+messageID, agent.MessagingPayload{Body: question, MessageID: messageID})
}

// nativeAsk is the native loop's ask tool: the loop records it itself.
func (s *script) nativeAsk(messageID, question string) Event {
	return s.add("ask", agent.MessagingPayload{Body: question, MessageID: messageID})
}

func (s *script) sub(id, title string) Event {
	return s.add("subagent_started", agent.SubagentPayload{ChildID: id, AgentType: "general-purpose", Model: "sonnet", Summary: title})
}
func (s *script) subUpdate(p agent.SubagentPayload) Event { return s.add("subagent_update", p) }
func (s *script) subDone(p agent.SubagentPayload) Event   { return s.add("subagent_done", p) }
func (s *script) launch(id string) Event {
	return s.subDone(agent.SubagentPayload{ChildID: id, Summary: "running in the background", Background: true})
}
func (s *script) task(id, call, kind, desc string) Event {
	return s.add("background_task_started", agent.BackgroundTaskStartedPayload{TaskID: id, CallID: call, Kind: kind, Description: desc,
		StartedAt: s.at.Add(time.Minute).Format(time.RFC3339)})
}
func (s *script) taskDone(id, call, status string) Event {
	return s.add("background_task_done", agent.BackgroundTaskDonePayload{TaskID: id, CallID: call, Kind: "bash", Status: status, Summary: "exit 0"})
}
func (s *script) row(parent, kind string, payload map[string]any) Event {
	payload["parent_call_id"] = parent
	return s.add(kind, payload)
}

func (s *script) opts(status string) Options {
	return Options{
		Now: s.at.Add(time.Minute), Status: status, ShowTokens: true,
		ServerAsk: func(eventID, messageID string) bool { return eventID == "srv-"+messageID },
	}
}

func asks(st State) []string {
	var out []string
	for _, a := range st.Asks {
		out = append(out, a.MessageID+":"+a.Question)
	}
	return out
}

func subs(st State) []string {
	var out []string
	for _, a := range st.Subagents {
		state := "running"
		if a.Background {
			state = "background"
		}
		out = append(out, a.Key+":"+state)
	}
	return out
}

func tasks(st State) []string {
	var out []string
	for _, t := range st.Tasks {
		out = append(out, t.TaskID)
	}
	return out
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

func TestAnOpenAskStaysOpenPastItsTurn(t *testing.T) {
	s := newScript()
	s.msg("chat", "go")
	s.serverAsk("m1", "Ship it?")
	// Claude Code cut `blerg-runner ask` off and the turn ended: the server still holds it open.
	s.turnDone()
	eq(t, "asks", asks(Build(s.events, s.opts("idle"))), []string{"m1:Ship it?"})
}

func TestAnAnsweredAskIsClosed(t *testing.T) {
	s := newScript()
	s.serverAsk("m1", "Ship it?")
	s.serverAsk("m2", "Which branch?")
	s.answer("m1", "yes")
	eq(t, "asks", asks(Build(s.events, s.opts("running"))), []string{"m2:Which branch?"})
	// An answer without a message id answers the newest open one.
	s.add("user_message", agent.UserMessagePayload{Text: "main", Source: "ask_answer"})
	eq(t, "asks after the bare answer", asks(Build(s.events, s.opts("running"))), nil)
}

// A question the agent did not wait on (`blerg-runner ask --no-wait`) is listed as one, stays open
// past its turn and through the person's chat messages, and closes with its answer. The message
// that brings the answer to the agent (question_answer) closes nothing.
func TestANoWaitQuestionIsListedUntilItsAnswer(t *testing.T) {
	s := newScript()
	s.addID("ask", "srv-q1", agent.MessagingPayload{Body: "Ship blue?", MessageID: "q1", NoWait: true})
	s.turnDone()
	s.nativeAsk("", "Proceed?")
	s.msg("question_answer", "Answer to your question q1: yes")
	got := Build(s.events, s.opts("idle"))
	if len(got.Asks) != 2 || got.Asks[0].MessageID != "q1" || !got.Asks[0].NoWait || got.Asks[1].NoWait {
		t.Fatalf("asks = %+v, want the native ask and the no-wait question q1", got.Asks)
	}
	s.answer("q1", "yes")
	eq(t, "asks after the answer", asks(Build(s.events, s.opts("idle"))), []string{":Proceed?"})
}

// Card #155 review I2: an owner-only ask says so, so the rail never offers Dismiss on an engine
// command approval (owner-only and waited on), which is allowed or denied, never dismissed.
func TestAnOwnerOnlyAskSaysSo(t *testing.T) {
	s := newScript()
	s.addID("ask", "srv-a1", agent.MessagingPayload{Body: "Run rm -rf build?", MessageID: "a1", OwnerOnly: true})
	s.addID("ask", "srv-q1", agent.MessagingPayload{Body: "Which name?", MessageID: "q1"})
	got := Build(s.events, s.opts("waiting"))
	if len(got.Asks) != 2 || !got.Asks[0].OwnerOnly || got.Asks[1].OwnerOnly {
		t.Fatalf("asks = %+v, want a1 owner-only and q1 not", got.Asks)
	}
	b, _ := json.Marshal(got.Asks[0])
	if !strings.Contains(string(b), `"owner_only":true`) {
		t.Errorf("json = %s", b)
	}
}

func TestAnAskTheServerClosedIsClosed(t *testing.T) {
	s := newScript()
	s.serverAsk("m1", "Ship it?")
	s.serverAsk("m2", "Which branch?")
	opt := s.opts("running")
	opt.Closed = map[string]bool{"m1": true} // expired, or answered on the Messages page
	eq(t, "asks", asks(Build(s.events, opt)), []string{"m2:Which branch?"})
}

func TestAnEndedSessionHasNoOpenAsk(t *testing.T) {
	s := newScript()
	s.serverAsk("m1", "Ship it?")
	for _, st := range []string{"stopped", "ended", "error"} {
		if got := Build(s.events, s.opts(st)); len(got.Asks) != 0 {
			t.Errorf("%s: asks = %v", st, asks(got))
		}
	}
}

func TestANativeAskClosesWithItsTurn(t *testing.T) {
	s := newScript()
	s.nativeAsk("m1", "Proceed?")
	eq(t, "asks while the turn runs", asks(Build(s.events, s.opts("waiting"))), []string{"m1:Proceed?"})
	s.turnDone()
	eq(t, "asks after the turn", asks(Build(s.events, s.opts("idle"))), nil)
}

func TestAnAskWithoutAMessageIDClosesWithThePersonsNextMessage(t *testing.T) {
	s := newScript()
	s.nativeAsk("", "Proceed?")
	s.msg("system", "harness text")
	eq(t, "asks after a system line", asks(Build(s.events, s.opts("running"))), []string{":Proceed?"})
	// As in the chat (asks.ts): a board's, a cron's or a message Blerg relays is not the person answering.
	for _, src := range []string{"blerg-board", "cron", "review", "proposal"} {
		s.msg(src, "not an answer")
		eq(t, "asks after a "+src+" message", asks(Build(s.events, s.opts("running"))), []string{":Proceed?"})
	}
	s.msg("chat", "yes")
	eq(t, "asks after the person's message", asks(Build(s.events, s.opts("running"))), nil)
}

func TestARunningForegroundSubagent(t *testing.T) {
	s := newScript()
	s.msg("chat", "go")
	s.sub("toolu_1", "Implement task 3")
	s.subUpdate(agent.SubagentPayload{ChildID: "toolu_1", Model: "claude-sonnet-4-5", Usage: &agent.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 1000}})
	s.row("toolu_1", "tool_call", map[string]any{"tool": "Bash", "call_id": "c9", "input": map[string]any{"command": "npm test\nnpm run lint"}})
	st := Build(s.events, s.opts("running"))
	eq(t, "subagents", subs(st), []string{"toolu_1:running"})
	got := st.Subagents[0]
	if got.Title != "Implement task 3" || got.Type != "general-purpose" || got.Model != "claude-sonnet-4-5" ||
		got.Tokens == nil || *got.Tokens != 1120 || got.Doing != "running Bash · npm test" ||
		!got.StartedAt.Equal(s.events[1].Ts) || !got.ActiveAt.Equal(s.events[3].Ts) {
		t.Errorf("subagent = %+v", got)
	}
	// The parts behind the total, for the Running now chip's tooltip.
	if got.Usage == nil || *got.Usage != (Usage{Input: 100, Output: 20, CacheRead: 1000}) {
		t.Errorf("subagent usage = %+v", got.Usage)
	}
}

func TestASubagentEndsWithItsFinalDone(t *testing.T) {
	s := newScript()
	s.sub("toolu_1", "a")
	s.subDone(agent.SubagentPayload{ChildID: "toolu_1", Summary: "ok"})
	eq(t, "subagents", subs(Build(s.events, s.opts("running"))), nil)
}

func TestAForegroundSubagentIsNotRunningOnceTheTurnIsOver(t *testing.T) {
	s := newScript()
	s.sub("toolu_1", "a")
	eq(t, "while idle", subs(Build(s.events, s.opts("idle"))), nil) // its call is not in flight
	s.turnDone()
	eq(t, "after turn_done", subs(Build(s.events, s.opts("running"))), nil)
}

// The chat's rule: a message that is not the system's ends the calls in flight, so a foreground
// subagent stops counting when the person writes mid-turn (or answers); a background one runs on.
func TestAPersonsMidTurnMessageEndsAForegroundSubagent(t *testing.T) {
	s := newScript()
	s.msg("chat", "go")
	s.sub("toolu_1", "foreground")
	s.sub("toolu_2", "background")
	s.launch("toolu_2")
	s.msg("system", "harness line")
	eq(t, "after a system line", subs(Build(s.events, s.opts("running"))), []string{"toolu_1:running", "toolu_2:background"})
	s.msg("chat", "also update the docs")
	eq(t, "after the person's message", subs(Build(s.events, s.opts("running"))), []string{"toolu_2:background"})
	if got := Pending(s.events, s.opts("running")); len(got) != 1 || got[0].Key != "toolu_2" {
		t.Errorf("Pending = %+v", got)
	}
}

func TestABackgroundSubagent(t *testing.T) {
	s := newScript()
	s.sub("toolu_1", "Watch the deploy")
	s.launch("toolu_1")
	s.task("a5ef", "toolu_1", "agent", "Watch the deploy")
	s.turnDone()
	st := Build(s.events, s.opts("idle"))
	eq(t, "subagents", subs(st), []string{"toolu_1:background"})
	// Its background task is the same work: listed once, as the subagent.
	eq(t, "tasks", tasks(st), nil)
	// Its real end, from the CLI's notice.
	s.subDone(agent.SubagentPayload{ChildID: "toolu_1", Summary: "deployed"})
	s.taskDone("a5ef", "toolu_1", "completed")
	st = Build(s.events, s.opts("idle"))
	eq(t, "subagents after the end", subs(st), nil)
	eq(t, "tasks after the end", tasks(st), nil)
	// A session that is over runs nothing.
	s2 := newScript()
	s2.sub("toolu_1", "x")
	s2.launch("toolu_1")
	eq(t, "stopped session", subs(Build(s2.events, s2.opts("stopped"))), nil)
}

func TestAPersonStoppedSubagentIsNotRunning(t *testing.T) {
	s := newScript()
	s.sub("toolu_1", "a")
	s.launch("toolu_1")
	s.subDone(agent.SubagentPayload{ChildID: "toolu_1", Summary: "killed: by the person"})
	eq(t, "subagents", subs(Build(s.events, s.opts("idle"))), nil)
}

func TestAStaleSubagentIsNotRunning(t *testing.T) {
	s := newScript()
	s.sub("toolu_1", "a")
	s.launch("toolu_1")
	opt := s.opts("idle")
	opt.Now = s.at.Add(13 * time.Hour)
	eq(t, "13 h later", subs(Build(s.events, opt)), nil)
	opt.Now = s.at.Add(11 * time.Hour)
	eq(t, "11 h later", subs(Build(s.events, opt)), []string{"toolu_1:background"})
	// Its own rows are signs of life too.
	s.wait(10 * time.Hour)
	s.row("toolu_1", "assistant_text", map[string]any{"text": "still at it", "done": true})
	opt.Now = s.at.Add(5 * time.Hour)
	st := Build(s.events, opt)
	eq(t, "15 h after the start, 5 h after its last row", subs(st), []string{"toolu_1:background"})
	if st.Subagents[0].Doing != "still at it" {
		t.Errorf("doing = %q", st.Subagents[0].Doing)
	}
}

func TestABackgroundTaskEndsWithItsSystemNotice(t *testing.T) {
	s := newScript()
	s.task("b1", "c1", "bash", "npm run dev")
	s.task("b2", "c2", "monitor", "watch the log")
	s.turnDone()
	eq(t, "tasks", tasks(Build(s.events, s.opts("idle"))), []string{"b1", "b2"})
	// The notice of a task that ended while the session was idle (the system line form).
	s.taskDone("b1", "c1", "completed")
	eq(t, "tasks after b1's notice", tasks(Build(s.events, s.opts("idle"))), []string{"b2"})
	eq(t, "a stopped session's tasks", tasks(Build(s.events, s.opts("stopped"))), nil)
	opt := s.opts("idle")
	opt.Now = s.at.Add(13 * time.Hour)
	eq(t, "13 h later", tasks(Build(s.events, opt)), nil)
}

func TestTokensFollowTheInsightsRule(t *testing.T) {
	s := newScript()
	s.sub("toolu_1", "a")
	s.subUpdate(agent.SubagentPayload{ChildID: "toolu_1", Usage: &agent.Usage{OutputTokens: 9}})
	opt := s.opts("running")
	opt.ShowTokens = false
	st := Build(s.events, opt)
	if !st.TokensHidden || st.Subagents[0].Tokens != nil || st.Subagents[0].Usage != nil {
		t.Errorf("hidden tokens: %+v %+v", st.TokensHidden, st.Subagents[0])
	}
}

func TestPendingNamesWhatTheSecondReadFetches(t *testing.T) {
	s := newScript()
	s.sub("toolu_1", "ended")
	s.subDone(agent.SubagentPayload{ChildID: "toolu_1", Summary: "ok"})
	s.sub("toolu_2", "background")
	s.launch("toolu_2")
	s.sub("toolu_3", "foreground")
	got := Pending(s.events, s.opts("running"))
	want := []Tail{{Key: "toolu_2", Seq: 3}, {Key: "toolu_3", Seq: 5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Pending = %+v, want %+v", got, want)
	}
	if got := Pending(s.events, s.opts("stopped")); len(got) != 0 {
		t.Errorf("a stopped session needs no second read: %+v", got)
	}
}

func TestEventsAreReadInSeqOrder(t *testing.T) {
	s := newScript()
	s.sub("toolu_1", "a")
	s.subDone(agent.SubagentPayload{ChildID: "toolu_1", Summary: "ok"})
	rev := []Event{s.events[1], s.events[0]}
	eq(t, "subagents", subs(Build(rev, s.opts("running"))), nil)
}

// A background subagent with no final end stays pending, but its tails are read only over the last
// StaleRunning: an event older than that cannot revive it, so the walk starts at the newest live
// event older than that, not at its start.
func TestPendingReadsTailsOverTheStaleWindowOnly(t *testing.T) {
	s := newScript()
	s.sub("toolu_old", "silently died")
	s.launch("toolu_old")
	for range 3000 {
		s.msg("system", "a long session goes on")
	}
	floor := s.events[len(s.events)-1].Seq
	s.wait(13 * time.Hour)
	s.msg("chat", "later")
	s.sub("toolu_new", "fresh")
	s.launch("toolu_new")
	got := Pending(s.events, s.opts("idle"))
	want := []Tail{{Key: "toolu_old", Seq: floor}, {Key: "toolu_new", Seq: floor + 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Pending = %+v, want %+v", got, want)
	}
}

// A task id started twice is listed once, as its newest start (the chat keeps the newest).
func TestATaskStartedTwiceIsListedOnceAsItsNewestStart(t *testing.T) {
	s := newScript()
	s.task("b1", "c1", "bash", "first")
	s.task("b1", "c1", "bash", "again")
	st := Build(s.events, s.opts("idle"))
	if len(st.Tasks) != 1 || st.Tasks[0].Description != "again" {
		t.Errorf("tasks = %+v", st.Tasks)
	}
}

func (s *script) resumed(id, from, title string, background bool) Event {
	return s.add("subagent_started", agent.SubagentPayload{ChildID: id, AgentType: "general-purpose", Summary: title,
		Background: background, Resumed: true, ResumedFrom: from})
}

// A finished subagent the main agent resumed is a new running instance (keyed by the SendMessage
// call), whatever came before: the person's message, the turn's end. It runs until its own final
// done; a background original's run is listed as background. The original stays ended.
func TestAResumedSubagentRunsUntilItsOwnEnd(t *testing.T) {
	s := newScript()
	s.msg("chat", "go")
	s.sub("toolu_fg", "Implement task 3")
	s.subDone(agent.SubagentPayload{ChildID: "toolu_fg", Summary: "done"})
	s.msg("chat", "review found two issues")
	s.resumed("toolu_send1", "toolu_fg", "Implement task 3", false)
	s.row("toolu_send1", "tool_call", map[string]any{"tool": "Bash", "call_id": "c1", "input": map[string]any{"command": "make check"}})
	st := Build(s.events, s.opts("running"))
	eq(t, "after the resume", subs(st), []string{"toolu_send1:running"})
	if got := st.Subagents[0]; got.Title != "Implement task 3" || !got.Resumed || got.Doing != "running Bash · make check" {
		t.Errorf("resumed = %+v", got)
	}
	s.msg("chat", "and the docs")
	s.turnDone()
	eq(t, "after the person's message and the turn's end", subs(Build(s.events, s.opts("idle"))), []string{"toolu_send1:running"})
	if got := Pending(s.events, s.opts("idle")); len(got) != 1 || got[0].Key != "toolu_send1" {
		t.Errorf("Pending = %+v", got)
	}
	s.subDone(agent.SubagentPayload{ChildID: "toolu_send1", Summary: "fixed", Resumed: true, ResumedFrom: "toolu_fg"})
	eq(t, "after its end", subs(Build(s.events, s.opts("idle"))), nil)

	s.sub("toolu_bg", "Watch")
	s.launch("toolu_bg")
	s.subDone(agent.SubagentPayload{ChildID: "toolu_bg", Summary: "green"})
	s.resumed("toolu_send2", "toolu_bg", "Watch", true)
	eq(t, "a background original's run", subs(Build(s.events, s.opts("idle"))), []string{"toolu_send2:background"})
	eq(t, "in a stopped session", subs(Build(s.events, s.opts("stopped"))), nil)
}

func TestUsageNoticeLeavesWorkAndAsksOpen(t *testing.T) {
	s := newScript()
	s.sub("foreground", "Review")
	s.nativeAsk("", "Which branch?")
	s.msg("usage_limit", "95% used")
	st := Build(s.events, s.opts("running"))
	if len(st.Subagents) != 1 || len(st.Asks) != 1 {
		t.Fatalf("usage notice changed live work: %+v", st)
	}
}
