package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// The driver tells the recovery record what is running in the background as it changes: two
// background subagents are both a task and a subagent (with the agent id a message resumes them
// by) while they run, and nothing is left once their notifications have ended them.
func TestDriverReportsOpenWork(t *testing.T) {
	f, err := os.Open("testdata/claude_background.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", &collectEmitter{}, nil)
	d.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	d.steer.legacy = false
	p := &ccProc{}
	d.steer.proc = p
	type snap struct {
		tasks []agent.BackgroundTaskStartedPayload
		subs  []OpenSubagent
	}
	var snaps []snap
	d.onOpenWork = func(tasks []agent.BackgroundTaskStartedPayload, subs []OpenSubagent) {
		snaps = append(snaps, snap{tasks, subs})
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var afterResult snap
	for sc.Scan() {
		d.onLine(p, sc.Bytes())
		if strings.Contains(sc.Text(), `"type": "result"`) {
			afterResult = snaps[len(snaps)-1]
		}
	}
	wantTasks := []agent.BackgroundTaskStartedPayload{
		{TaskID: "a0000000000000002", CallID: "toolu_bg2", Kind: "agent", Description: "Read the docs", StartedAt: fixtureStart},
		{TaskID: "a5ef89e0e0329265b", CallID: "toolu_bg", Kind: "agent", Description: "Watch the build", StartedAt: fixtureStart},
	}
	if !reflect.DeepEqual(afterResult.tasks, wantTasks) {
		t.Errorf("open tasks while both run:\n got  %+v\n want %+v", afterResult.tasks, wantTasks)
	}
	if len(afterResult.subs) != 2 {
		t.Fatalf("open subagents while both run: %+v", afterResult.subs)
	}
	ids := map[string]string{}
	for _, s := range afterResult.subs {
		ids[s.ChildID] = s.AgentID
	}
	if ids["toolu_bg"] != "a5ef89e0e0329265b" || ids["toolu_bg2"] != "a0000000000000002" {
		t.Errorf("a background subagent is recorded with its agent id: %+v", afterResult.subs)
	}
	last := snaps[len(snaps)-1]
	if len(last.tasks) != 0 || len(last.subs) != 0 {
		t.Errorf("nothing runs once the notifications ended them: %+v", last)
	}
}

// The record keeps the open work, and is not rewritten when nothing changed.
func TestRecordKeepsOpenWork(t *testing.T) {
	dir := t.TempDir() + "/agents"
	k := newRecordKeeper(dir, nil, AgentRecord{SessionID: "s1", WorkDir: "/w"})
	tasks := []agent.BackgroundTaskStartedPayload{{TaskID: "b1", CallID: "c1", Kind: "bash", Description: "tests"}}
	subs := []OpenSubagent{{ChildID: "c2", AgentType: "Explore", Summary: "look", AgentID: "a2"}}
	k.setOpenWork(tasks, subs)
	rec, _ := readRecord(t, dir, "s1")
	if !reflect.DeepEqual(rec.OpenTasks, tasks) || !reflect.DeepEqual(rec.OpenSubagents, subs) {
		t.Fatalf("record: %+v", rec)
	}
	info, _ := os.Stat(agentRecordPath(dir, "s1"))
	time.Sleep(10 * time.Millisecond)
	k.setOpenWork(append([]agent.BackgroundTaskStartedPayload(nil), tasks...), append([]OpenSubagent(nil), subs...))
	if again, _ := os.Stat(agentRecordPath(dir, "s1")); !again.ModTime().Equal(info.ModTime()) {
		t.Error("an unchanged set was written again")
	}
	k.setOpenWork(nil, []OpenSubagent{})
	if rec, _ := readRecord(t, dir, "s1"); rec.OpenTasks != nil || rec.OpenSubagents != nil {
		t.Errorf("nothing open: %+v", rec)
	}
}

// openWorkRecord is a session that had a background shell, a background subagent and a
// foreground subagent running when the daemon stopped; its turn had ended.
func openWorkRecord(workDir, interaction string) AgentRecord {
	return AgentRecord{
		SessionID: "s1", Repo: "proj", WorkDir: workDir, Interaction: interaction, ClaudeSessionID: "conv-1",
		OpenTasks: []agent.BackgroundTaskStartedPayload{
			{TaskID: "bu90kf38e", CallID: "toolu_sh", Kind: "bash", Description: "Wait for the deploy", StartedAt: fixtureStart},
			{TaskID: "a5ef89e0e0329265b", CallID: "toolu_bg", Kind: "agent", Description: "Fix the parser", StartedAt: fixtureStart},
		},
		OpenSubagents: []OpenSubagent{
			{ChildID: "toolu_bg", AgentType: "general-purpose", Summary: "Fix the parser", AgentID: "a5ef89e0e0329265b"},
			{ChildID: "toolu_fg", AgentType: "Explore", Summary: "Find the callers"},
		},
	}
}

func stoppedEvents(s *agentTestSender) []string {
	var out []string
	for _, ev := range s.agentEvents() {
		switch ev.Kind {
		case "background_task_done":
			var p agent.BackgroundTaskDonePayload
			_ = json.Unmarshal(ev.Payload, &p)
			out = append(out, "task:"+p.TaskID+":"+p.Status+":"+p.Summary)
		case "subagent_done":
			var p agent.SubagentPayload
			_ = json.Unmarshal(ev.Payload, &p)
			out = append(out, "subagent:"+p.ChildID+":"+p.Summary)
		}
	}
	return out
}

// After a restart, what was running in the background is reported stopped (which is what moves
// the server's count of running tasks back), and an unattended session — its background work
// would have woken the agent by itself — is asked, once, to pick it up, by a system message that
// lists it and says how.
func TestAdoptStopsCutOffWorkAndTellsTheAgent(t *testing.T) {
	newHost, recDir, workDir, claudeLog := recoveryHost(t)
	stubTranscripts(t, "conv-1")
	if err := writeAgentRecord(recDir, openWorkRecord(workDir, protocol.InteractionUnattended)); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	host.AdoptRecorded()
	want := []string{
		"task:bu90kf38e:stopped:the daemon restarted",
		"task:a5ef89e0e0329265b:stopped:the daemon restarted",
		"subagent:toolu_bg:stopped: the daemon restarted",
		"subagent:toolu_fg:stopped: the daemon restarted",
	}
	if got := stoppedEvents(sender); !reflect.DeepEqual(got, want) {
		t.Fatalf("stopped events:\n got  %v\n want %v", got, want)
	}
	notes := errorTexts(sender)
	if len(notes) != 1 || !strings.Contains(notes[0], "1 background task and 2 subagents were stopped") ||
		!strings.Contains(notes[0], "being asked to continue") {
		t.Fatalf("the person is told: %v", notes)
	}
	// The adopted record starts clean: what it listed has been reported, and the continue is counted.
	if rec, _ := readRecord(t, recDir, "s1"); len(rec.OpenTasks) != 0 || len(rec.OpenSubagents) != 0 || rec.AutoContinues != 1 {
		t.Errorf("record after adoption: %+v", rec)
	}

	host.NoteConnected()
	sender.waitForKind(t, "turn_done")
	users := userTexts(sender)
	if len(users) != 1 || !strings.HasPrefix(users[0], "system:[system] ↻ Host restarted") ||
		!strings.HasSuffix(users[0], " · continued") {
		t.Fatalf("one system message tells the agent: %v", users)
	}
	argv, _ := os.ReadFile(claudeLog)
	for _, w := range []string{
		"--resume conv-1",
		"Background tasks that were cut off:\n- bash bu90kf38e: Wait for the deploy",
		"Subagents that were cut off:\n- general-purpose \"Fix the parser\", agent id a5ef89e0e0329265b",
		"- Explore \"Find the callers\", in the foreground",
		"check the state of their worktrees and outputs",
		"sending a message to its agent id",
		"Do not re-run anything destructive",
	} {
		if !strings.Contains(string(argv), w) {
			t.Errorf("the engine was not told %q: %s", w, argv)
		}
	}
	if strings.Count(string(argv), "Subagents that were cut off") != 1 {
		t.Errorf("the list is told once: %s", argv)
	}
}

// Work cut off again after the session was already continued once: it is reported stopped but
// nothing runs by itself; the list goes to the model with the person's next message, which the
// transcript keeps as they wrote it.
func TestAdoptOfCutOffWorkAlreadyContinuedWaits(t *testing.T) {
	newHost, recDir, workDir, claudeLog := recoveryHost(t)
	stubTranscripts(t, "conv-1")
	rec := openWorkRecord(workDir, protocol.InteractionUnattended)
	rec.AutoContinues = 1
	if err := writeAgentRecord(recDir, rec); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	host.NoteConnected()
	host.AdoptRecorded()
	if got := stoppedEvents(sender); len(got) != 4 {
		t.Fatalf("stopped events: %v", got)
	}
	if n := errorTexts(sender); len(n) != 1 || !strings.Contains(n[0], "waiting for a message") {
		t.Errorf("notice: %v", n)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(claudeLog); err == nil {
		t.Fatal("work that was already continued once was continued again")
	}
	host.UserMessage("s1", "status?", "chat")
	sender.waitForKind(t, "turn_done")
	argv, _ := os.ReadFile(claudeLog)
	if !strings.Contains(string(argv), "agent id a5ef89e0e0329265b") || !strings.Contains(string(argv), "status?") {
		t.Errorf("the next message carries the list: %s", argv)
	}
	if got := userTexts(sender); len(got) != 2 || !strings.HasPrefix(got[0], "system:[system] ↻ Host restarted") ||
		strings.Contains(got[0], adoptContinuePrompt) || got[1] != "chat:status?" {
		t.Errorf("the notice shows as the system's, then the person's own words: %v", got)
	}
}

// An interactive session whose cut-off work is only background tasks and subagents — the agent
// was waiting on them, between tool calls of its own — is picked up again by itself, once, exactly
// as an unattended one: one system message lists what was cut off and asks it to continue.
func TestAdoptResumesInteractiveCutOffWork(t *testing.T) {
	for name, running := range map[string]*bool{"tracked": new(bool), "older record, idle": nil} {
		t.Run(name, func(t *testing.T) {
			newHost, recDir, workDir, claudeLog := recoveryHost(t)
			stubTranscripts(t, "conv-1")
			rec := openWorkRecord(workDir, protocol.InteractionInteractive)
			rec.ToolRunning = running
			if err := writeAgentRecord(recDir, rec); err != nil {
				t.Fatal(err)
			}
			host, sender := newHost()
			host.AdoptRecorded()
			if got := stoppedEvents(sender); len(got) != 4 {
				t.Fatalf("stopped events: %v", got)
			}
			if n := errorTexts(sender); len(n) != 1 || !strings.Contains(n[0], "being asked to continue") {
				t.Errorf("notice: %v", n)
			}
			if rec, _ := readRecord(t, recDir, "s1"); rec.AutoContinues != 1 {
				t.Errorf("the continue is counted before it runs: %+v", rec)
			}
			host.NoteConnected()
			sender.waitForKind(t, "turn_done")
			users := userTexts(sender)
			if len(users) != 1 || !strings.HasPrefix(users[0], "system:[system] ↻ Host restarted") ||
				!strings.HasSuffix(users[0], " · continued") {
				t.Fatalf("one system message tells the agent: %v", users)
			}
			argv, _ := os.ReadFile(claudeLog)
			if !strings.Contains(string(argv), "agent id a5ef89e0e0329265b") || !strings.Contains(string(argv), adoptContinuePrompt) ||
				strings.Count(string(argv), "Subagents that were cut off") != 1 {
				t.Errorf("the engine is told the list and to continue, once: %s", argv)
			}
		})
	}
}

// A message from the person that reaches a session before recovery's continue — typed while the
// daemon was down (held, then delivered), or during the wait for the server — wins: no continue is
// sent, and their message carries the restart note and the list of what was cut off to the engine,
// while the transcript keeps their words.
func TestAdoptContinueGivesWayToThePerson(t *testing.T) {
	for _, interaction := range []string{protocol.InteractionInteractive, protocol.InteractionUnattended} {
		t.Run(interaction, func(t *testing.T) {
			newHost, recDir, workDir, claudeLog := recoveryHost(t)
			stubTranscripts(t, "conv-1")
			rec := openWorkRecord(workDir, interaction)
			rec.ToolRunning = new(bool)
			if err := writeAgentRecord(recDir, rec); err != nil {
				t.Fatal(err)
			}
			host, sender := newHost()
			host.BeginAdoption()
			host.UserMessage("s1", "typed during the restart", "chat")
			host.RunAdoption()
			sender.waitForKind(t, "turn_done")
			host.NoteConnected()
			time.Sleep(150 * time.Millisecond)
			if n := sender.countKind("turn_done"); n != 1 {
				t.Fatalf("turns = %d: recovery's continue ran after the person's message", n)
			}
			argv, _ := os.ReadFile(claudeLog)
			for _, w := range []string{"Subagents that were cut off", "agent id a5ef89e0e0329265b", "typed during the restart"} {
				if !strings.Contains(string(argv), w) {
					t.Errorf("the person's message did not carry %q: %s", w, argv)
				}
			}
			if strings.Contains(string(argv), adoptContinuePrompt) {
				t.Errorf("the continue was sent: %s", argv)
			}
			if got := userTexts(sender); len(got) != 1 || got[0] != "chat:typed during the restart" {
				t.Errorf("transcript: %v", got)
			}
		})
	}
}

// An interactive session whose turn was cut off with only a foreground subagent running (its
// Agent call is the subagent, not a command of the agent's own) is picked up again by itself: the
// subagent's card is ended and the model is told it was cut off, with the restart note.
func TestAdoptResumesAnInteractiveTurnWaitingOnASubagent(t *testing.T) {
	newHost, recDir, workDir, claudeLog := recoveryHost(t)
	stubTranscripts(t, "conv-1")
	rec := openWorkRecord(workDir, protocol.InteractionInteractive)
	rec.TurnActive = true
	rec.ToolRunning = new(bool)
	rec.OpenTasks = nil
	rec.OpenSubagents = rec.OpenSubagents[1:]
	if err := writeAgentRecord(recDir, rec); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	host.NoteConnected()
	host.AdoptRecorded()
	if got := stoppedEvents(sender); !reflect.DeepEqual(got, []string{"subagent:toolu_fg:stopped: the daemon restarted"}) {
		t.Fatalf("stopped events: %v", got)
	}
	sender.waitForKind(t, "turn_done")
	argv, _ := os.ReadFile(claudeLog)
	for _, w := range []string{"restarted while you were working", "- Explore \"Find the callers\", in the foreground", adoptContinuePrompt} {
		if !strings.Contains(string(argv), w) {
			t.Errorf("the engine was not given %q: %s", w, argv)
		}
	}
}

// idleThenPaused reports whether a session_state_changed "idle" was sent before the first
// session_paused.
func idleThenPaused(s *agentTestSender) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	idle := false
	for _, m := range s.msgs {
		switch v := m.(type) {
		case protocol.SessionStateChanged:
			if v.Status == "idle" {
				idle = true
			}
		case protocol.SessionPaused:
			return idle
		}
	}
	return false
}

// pausedReports are the session_paused messages the daemon sent.
func pausedReports(s *agentTestSender) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.msgs {
		if p, ok := m.(protocol.SessionPaused); ok {
			out = append(out, p.SessionID+":"+p.Reason)
		}
	}
	return out
}

// An interactive session waits for the person, whatever else was cut off, when it was waiting for
// them: its status was "waiting", or its own open call was a question (`blerg-runner ask`). The
// notice alone shows as the system's message, nothing runs, the server is told it is paused by the
// restart, and the agent gets the list with the person's next message (which the transcript keeps
// as they wrote it).
func TestAdoptOfInteractiveCutOffWorkThatNeedsThePersonWaits(t *testing.T) {
	cases := map[string]func(*AgentRecord){
		"waiting for the person": func(r *AgentRecord) { r.TurnActive, r.ToolRunning, r.Waiting = true, new(bool), true },
		"a blerg-runner ask open": func(r *AgentRecord) {
			r.TurnActive, r.ToolRunning = true, new(bool)
			*r.ToolRunning = true
			r.OpenCalls = []OpenCall{openCallOf("toolu_ask", "Bash", bashIn(`blerg-runner ask "Which database?"`))}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			newHost, recDir, workDir, claudeLog := recoveryHost(t)
			stubTranscripts(t, "conv-1")
			rec := openWorkRecord(workDir, protocol.InteractionInteractive)
			change(&rec)
			if err := writeAgentRecord(recDir, rec); err != nil {
				t.Fatal(err)
			}
			host, sender := newHost()
			host.NoteConnected()
			host.AdoptRecorded()
			if got := stoppedEvents(sender); len(got) != 4 {
				t.Fatalf("stopped events: %v", got)
			}
			notes := errorTexts(sender)
			if len(notes) != 1 || strings.Contains(notes[0], "being asked to continue") || !strings.Contains(notes[0], "waiting for your answer") ||
				!strings.Contains(notes[0], "told what was cut off with the next message") {
				t.Errorf("notice: %v", notes)
			}
			users := userTexts(sender)
			if len(users) != 1 || !strings.HasPrefix(users[0], "system:[system] ↻ Host restarted") ||
				strings.Contains(users[0], adoptContinuePrompt) {
				t.Fatalf("the notice alone, as the system's: %v", users)
			}
			waitUntil(t, "the pause to be reported", func() bool { return len(pausedReports(sender)) == 1 })
			if got := pausedReports(sender); got[0] != "s1:restart" {
				t.Errorf("paused reports: %v", got)
			}
			if got := idleThenPaused(sender); !got {
				t.Errorf("the session was not reported idle before paused: %v", sentTypes(sender))
			}
			time.Sleep(100 * time.Millisecond)
			if _, err := os.Stat(claudeLog); err == nil {
				t.Fatal("an interactive session waiting for the person ran by itself after the restart")
			}
			if rec, _ := readRecord(t, recDir, "s1"); rec.AutoContinues != 0 {
				t.Errorf("nothing was continued, nothing is counted: %+v", rec)
			}
			host.UserMessage("s1", "Postgres", "chat")
			sender.waitForKind(t, "turn_done")
			argv, _ := os.ReadFile(claudeLog)
			if !strings.Contains(string(argv), "agent id a5ef89e0e0329265b") || !strings.Contains(string(argv), "Postgres") ||
				!strings.Contains(string(argv), "restarted while you were working") || strings.Contains(string(argv), adoptContinuePrompt) {
				t.Errorf("the next message carries the note and the list, and no continue: %s", argv)
			}
			if got := userTexts(sender); len(got) != 2 || got[1] != "chat:Postgres" {
				t.Errorf("the transcript keeps the person's words: %v", got)
			}
			if got := pausedReports(sender); len(got) != 1 {
				t.Errorf("paused reported again: %v", got)
			}
		})
	}
}

// What used to wait for the person is now picked up again by itself: one of the agent's own tool
// calls cut off mid-run (the notice names it, with what it ran, and says to check its state and
// never re-run anything destructive), a turn cut off mid-generation with nothing else running,
// and a record from an older daemon that kept neither open calls nor waiting.
func TestAdoptResumesAnInteractiveTurnCutOffMidRun(t *testing.T) {
	cases := map[string]struct {
		rec  func(r *AgentRecord)
		want []string
	}{
		"its own tool call running": {
			rec: func(r *AgentRecord) {
				r.ToolRunning = new(bool)
				*r.ToolRunning = true
				r.OpenCalls = []OpenCall{openCallOf("toolu_mk", "Bash", json.RawMessage(`{"command":"make build","description":"Build the binaries"}`))}
			},
			want: []string{"Tool calls cut off before they returned:\n- Bash: make build (Build the binaries)",
				"Check its state first", "Never re-run anything destructive", "restarted while you were working"},
		},
		"mid-generation": {
			rec:  func(r *AgentRecord) { r.ToolRunning = new(bool) },
			want: []string{"restarted while you were working"},
		},
		"older record": {
			rec:  func(r *AgentRecord) {},
			want: []string{"restarted while you were working"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			newHost, recDir, workDir, claudeLog := recoveryHost(t)
			stubTranscripts(t, "conv-1")
			rec := AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: workDir, Interaction: protocol.InteractionInteractive,
				ClaudeSessionID: "conv-1", TurnActive: true}
			c.rec(&rec)
			if err := writeAgentRecord(recDir, rec); err != nil {
				t.Fatal(err)
			}
			host, sender := newHost()
			host.AdoptRecorded()
			if n := errorTexts(sender); len(n) != 1 || !strings.Contains(n[0], "being asked to continue") {
				t.Errorf("notice: %v", n)
			}
			host.NoteConnected()
			sender.waitForKind(t, "turn_done")
			argv, _ := os.ReadFile(claudeLog)
			for _, w := range append(c.want, adoptContinuePrompt, "--resume conv-1") {
				if !strings.Contains(string(argv), w) {
					t.Errorf("the engine was not given %q: %s", w, argv)
				}
			}
			if users := userTexts(sender); len(users) != 1 || !strings.HasPrefix(users[0], "system:") || !strings.HasSuffix(users[0], " · continued") {
				t.Errorf("the continue is the system's message: %v", users)
			}
			if got := pausedReports(sender); len(got) != 0 {
				t.Errorf("a continued session was reported paused: %v", got)
			}
		})
	}
}

// A record from an older daemon cannot say a question was open; the server's open asks can. Such a
// session is not continued after all (and the continue is not counted), the person is told, and the
// server is told it is paused by the restart.
func TestAdoptOfAnOlderRecordWithAServerAskWaits(t *testing.T) {
	newHost, recDir, workDir, claudeLog := recoveryHost(t)
	stubTranscripts(t, "conv-1")
	if err := writeAgentRecord(recDir, AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: workDir,
		Interaction: protocol.InteractionInteractive, ClaudeSessionID: "conv-1", TurnActive: true}); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	host.AdoptRecorded()
	host.NoteOpenAsks([]string{"s1", "other"})
	host.NoteConnected()
	waitUntil(t, "the pause to be reported", func() bool { return len(pausedReports(sender)) == 1 })
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(claudeLog); err == nil {
		t.Fatal("a session with a question open on the server was continued")
	}
	if n := errorTexts(sender); len(n) != 2 || !strings.Contains(n[1], "had asked a question") {
		t.Errorf("notices: %v", n)
	}
	if rec, _ := readRecord(t, recDir, "s1"); rec.AutoContinues != 0 {
		t.Errorf("the continue that did not run is still counted: %+v", rec)
	}
	if users := userTexts(sender); len(users) != 1 || !strings.Contains(users[0], "paused — waiting for your message") {
		t.Errorf("the person is not shown the question was cut off: %v", users)
	}
	host.UserMessage("s1", "Postgres", "chat")
	sender.waitForKind(t, "turn_done")
	argv, _ := os.ReadFile(claudeLog)
	for _, w := range []string{"restarted while you were working", adoptAskCutOff, "Postgres"} {
		if !strings.Contains(string(argv), w) {
			t.Errorf("the engine was not told %q: %s", w, argv)
		}
	}
}

// A session cut off in a command on the deny-list is paused, unattended too: the notice says why,
// the agent is told not to run it again unless asked, nothing runs, the server hears idle and then
// paused. Resuming it is the person's message.
func TestAdoptOfADestructiveCutOffPauses(t *testing.T) {
	for _, interaction := range []string{protocol.InteractionInteractive, protocol.InteractionUnattended} {
		t.Run(interaction, func(t *testing.T) {
			newHost, recDir, workDir, claudeLog := recoveryHost(t)
			stubTranscripts(t, "conv-1")
			running := true
			if err := writeAgentRecord(recDir, AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: workDir, Interaction: interaction,
				ClaudeSessionID: "conv-1", TurnActive: true, ToolRunning: &running,
				OpenCalls: []OpenCall{openCallOf("toolu_up", "Bash", bashIn("cd install/desktop && ./install.sh install --yes"))}}); err != nil {
				t.Fatal(err)
			}
			host, sender := newHost()
			host.NoteConnected()
			host.AdoptRecorded()
			if n := errorTexts(sender); len(n) != 1 || !strings.Contains(n[0], "looked destructive") || strings.Contains(n[0], "being asked to continue") {
				t.Errorf("notice: %v", n)
			}
			waitUntil(t, "the pause to be reported", func() bool { return len(pausedReports(sender)) == 1 })
			if !idleThenPaused(sender) {
				t.Errorf("not idle before paused: %v", sentTypes(sender))
			}
			time.Sleep(100 * time.Millisecond)
			if _, err := os.Stat(claudeLog); err == nil {
				t.Fatal("a session cut off in a destructive command was continued")
			}
			if users := userTexts(sender); len(users) != 1 || !strings.Contains(users[0], "paused — check the interrupted command\n") || !strings.Contains(users[0], "install.sh") {
				t.Errorf("the cut-off list: %v", users)
			}
		})
	}
}

// A message the server held while the daemon was down, delivered at the hello, replaces the
// continue and the pause: the session is not reported paused.
func TestAdoptHeldMessagePreventsThePause(t *testing.T) {
	newHost, recDir, workDir, _ := recoveryHost(t)
	stubTranscripts(t, "conv-1")
	if err := writeAgentRecord(recDir, AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: workDir,
		Interaction: protocol.InteractionInteractive, ClaudeSessionID: "conv-1", TurnActive: true, Waiting: true}); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	host.BeginAdoption()
	host.UserMessage("s1", "Postgres, as we said", "chat")
	host.RunAdoption()
	host.NoteConnected()
	sender.waitForKind(t, "turn_done")
	time.Sleep(150 * time.Millisecond)
	if got := pausedReports(sender); len(got) != 0 {
		t.Errorf("a session the person had written to was reported paused: %v", got)
	}
}

// A session cut off before it had a conversation pauses: it cannot continue without its task.
// Reporting the pause also lets a child tell its parent to send the task again.
func TestAdoptOfASessionCutOffBeforeItsConversation(t *testing.T) {
	newHost, recDir, workDir, claudeLog := recoveryHost(t)
	if err := writeAgentRecord(recDir, AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: workDir,
		Interaction: protocol.InteractionInteractive, TurnActive: true}); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	host.NoteConnected()
	host.AdoptRecorded()
	if n := errorTexts(sender); len(n) != 1 || !strings.Contains(n[0], "send it its task again") {
		t.Errorf("notice: %v", n)
	}
	time.Sleep(150 * time.Millisecond)
	if got := pausedReports(sender); len(got) != 1 {
		t.Errorf("first-turn pause was not reported: %v", got)
	}
	if _, err := os.Stat(claudeLog); err == nil {
		t.Fatal("it ran by itself")
	}
}

// The deny-list, on the full command: what pauses a session cut off in it, and what does not.
func TestRestartDenyList(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		deny bool
	}{
		{"git push --force origin main", true},
		{"git push -f", true},
		{"git push origin +main", true},
		{"git push --force-with-lease", true},
		{"git -C repo push -uf origin x", true},
		{"rm -rf build/", true},
		{"rm -fr /tmp/x", true},
		{"rm -Rf node_modules", true},
		{"rm -r -f dist", true},
		{"npm run migrate", true},
		{"go run ./cmd/migrate up", true},
		{"./scripts/run-migrations.sh", true},
		{"alembic upgrade head", true},
		{"make deploy", true},
		{"./deploy.sh prod", true},
		{"helm upgrade blerg ./chart", true},
		{"kubectl apply -f k8s/", true},
		{"kubectl -n blerg delete pod x", true},
		{"./install.sh install --yes", true},
		{"systemctl --user restart blerg-runner-daemon", true},
		{"launchctl kickstart -k gui/501/x", true},
		{`psql -c "DROP TABLE sessions"`, true},
		{`psql -c "truncate messages"`, true},
		{"git push origin main", false},
		{"git push -u origin feature-fix", false},
		{"git push --follow-tags", false},
		{"rm -r dist", false},
		{"rm -f a.out", false},
		{"go test ./internal/db -run TestMigrations", false},
		{"kubectl get pods", false},
		{"helm list", false},
		{"systemctl --user status blerg-runner-daemon", false},
		{"make check", false},
		{"git stash drop", false},
		{"rm --recursive --force build", true},
		{"rm --force -r build", true},
		{"rm build -rf", true},
		{"rm -r build -f", true},
		{"cd x && rm -v -R -f y", true},
		{"rm -- -rf", false},
		{"rmdir -p a/b", false},
		{"./redeploy.sh", true},
		{"make redeploy", true},
		{"terraform apply -auto-approve", true},
		{"terraform -chdir=infra destroy", true},
		{"terraform plan", false},
		{`psql -c "DELETE FROM sessions WHERE true"`, true},
		{"git reset --hard origin/main", true},
		{"git reset --soft HEAD~1", false},
		{"git clean -fdx", true},
		{"git clean --force -d", true},
		{"git clean -n", false},
	} {
		if got := destructiveCommand(c.cmd); got != c.deny {
			t.Errorf("destructiveCommand(%q) = %v, want %v", c.cmd, got, c.deny)
		}
	}
	// Decided from the command alone: a description that mentions a deploy does not pause.
	if c := openCallOf("c2", "Bash", json.RawMessage(`{"command":"make build","description":"Build before deploying"}`)); c.destructive() || !c.Checked {
		t.Errorf("a description decided it: %+v", c)
	}
	if cont, _ := adoptPlan(AgentRecord{TurnActive: true, OpenCalls: []OpenCall{openCallOf("c2", "Bash",
		json.RawMessage(`{"command":"make build","description":"Build before deploying"}`))}}, true, protocol.InteractionInteractive); !cont {
		t.Error("a harmless command with a deploy in its description paused the session")
	}
	// Recorded from the whole command, not the shortened Detail.
	long := strings.Repeat("echo step && ", 20) + "git push --force origin main"
	c := openCallOf("c1", "Bash", bashIn(long))
	if strings.Contains(c.Detail, "--force") || !c.Destructive {
		t.Errorf("a long destructive command: detail %q, destructive %v", c.Detail, c.Destructive)
	}
}

// The rule on its own: who is picked up again after a restart, and who waits. Everything that was
// cut off is picked up again, except an interactive session that was waiting for the person.
func TestAdoptPlan(t *testing.T) {
	yes, no := true, false
	sub := []OpenSubagent{{ChildID: "c1", AgentID: "a1"}}
	task := []agent.BackgroundTaskStartedPayload{{TaskID: "b1", CallID: "c2", Kind: "bash"}}
	build := []OpenCall{{CallID: "c3", Tool: "Bash", Detail: "make build"}}
	ask := []OpenCall{{CallID: "c4", Tool: "Bash", Detail: `blerg-runner ask "ok to merge?"`}}
	nativeAsk := []OpenCall{{CallID: "c5", Tool: "AskUserQuestion"}}
	const (
		in = protocol.InteractionInteractive
		un = protocol.InteractionUnattended
	)
	cases := []struct {
		name        string
		rec         AgentRecord
		interaction string
		cont        bool
	}{
		{"interactive, subagents only", AgentRecord{OpenSubagents: sub, ToolRunning: &no}, in, true},
		{"interactive, tasks only, turn over", AgentRecord{OpenTasks: task}, in, true},
		{"interactive, turn waiting on a subagent", AgentRecord{TurnActive: true, OpenSubagents: sub, ToolRunning: &no}, in, true},
		{"interactive, its own tool call running", AgentRecord{TurnActive: true, OpenSubagents: sub, ToolRunning: &yes, OpenCalls: build}, in, true},
		{"interactive, mid-generation, nothing else", AgentRecord{TurnActive: true, ToolRunning: &no}, in, true},
		{"interactive, older record mid-turn", AgentRecord{TurnActive: true, OpenTasks: task}, in, true},
		{"interactive, older record, only the turn", AgentRecord{TurnActive: true}, in, true},
		{"interactive, waiting for the person", AgentRecord{TurnActive: true, OpenTasks: task, ToolRunning: &no, Waiting: true}, in, false},
		{"interactive, a blerg-runner ask open", AgentRecord{TurnActive: true, ToolRunning: &yes, OpenCalls: ask}, in, false},
		{"interactive, an AskUserQuestion open", AgentRecord{TurnActive: true, ToolRunning: &yes, OpenCalls: nativeAsk}, in, false},
		{"interactive, nothing", AgentRecord{}, in, false},
		{"interactive (unset), mid-generation", AgentRecord{TurnActive: true}, "", true},
		{"unattended, a tool call running", AgentRecord{TurnActive: true, ToolRunning: &yes}, un, true},
		{"unattended, tasks", AgentRecord{OpenTasks: task, Waiting: true}, un, true},
		{"unattended, an ask open", AgentRecord{TurnActive: true, ToolRunning: &yes, OpenCalls: ask}, un, true},
		{"unattended, subagents only, turn over", AgentRecord{OpenSubagents: sub}, un, true},
		{"unattended, nothing", AgentRecord{}, un, false},
		{"interactive, a destructive command running", AgentRecord{TurnActive: true, ToolRunning: &yes, OpenCalls: []OpenCall{{CallID: "c6", Tool: "Bash", Detail: "make deploy", Destructive: true}}}, in, false},
		{"unattended, a destructive command running", AgentRecord{TurnActive: true, ToolRunning: &yes, OpenCalls: []OpenCall{{CallID: "c6", Tool: "Bash", Detail: "make deploy", Destructive: true}}}, un, false},
		{"unattended, an older record's destructive Detail", AgentRecord{TurnActive: true, OpenCalls: []OpenCall{{CallID: "c7", Tool: "Bash", Detail: "git push --force origin main"}}}, un, false},
	}
	for _, c := range cases {
		if cont, gaveUp := adoptPlan(c.rec, true, c.interaction); cont != c.cont || gaveUp {
			t.Errorf("%s: cont=%v gaveUp=%v, want cont=%v", c.name, cont, gaveUp, c.cont)
		}
		if cont, _ := adoptPlan(c.rec, false, c.interaction); cont {
			t.Errorf("%s: continued a conversation that could not be resumed", c.name)
		}
		c.rec.AutoContinues = 1
		if cont, gaveUp := adoptPlan(c.rec, true, c.interaction); cont || gaveUp != c.cont {
			t.Errorf("%s, already continued once: cont=%v gaveUp=%v", c.name, cont, gaveUp)
		}
	}
}

func bashIn(cmd string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"command": cmd})
	return raw
}

// Which calls only wait on background work.
func TestCCWaitCall(t *testing.T) {
	for _, c := range []struct {
		tool, cmd string
		wait      bool
	}{
		{"TaskOutput", "", true},
		{"BashOutput", "", true},
		{"Monitor", "", true},
		{"Bash", "sleep 60", true},
		{"Bash", "sleep 30s; tail -n 20 build.log", true},
		{"Bash", "sleep 60 && grep -q done out.log || echo waiting", true},
		{"Bash", "make check", false},
		{"Bash", "sleep 60 && make deploy", false},
		{"Bash", "sleep 5 && cat a > b", false},
		{"Bash", "sleep $(cat n) && ls", false},
		{"Bash", "sleep", false},
		{"Bash", "blerg-runner ask \"ok to deploy?\"", false},
		{"AskUserQuestion", "", false},
	} {
		in := json.RawMessage(`{}`)
		if c.tool == "Bash" {
			in = bashIn(c.cmd)
		}
		if got := ccWaitCall(c.tool, in); got != c.wait {
			t.Errorf("ccWaitCall(%s %q) = %v, want %v", c.tool, c.cmd, got, c.wait)
		}
	}
}

// The record follows the agent's own tool calls: a command or a question running is recorded, a
// subagent's calls and the calls that run or message a subagent are not, a result clears it, and
// so do the end of a turn and the start of the next. A session waiting for the person is recorded.
func TestRecordKeepsToolCallsAndWaiting(t *testing.T) {
	dir := t.TempDir() + "/agents"
	k := newRecordKeeper(dir, nil, AgentRecord{SessionID: "s1", WorkDir: "/w"})
	running := func() bool {
		t.Helper()
		rec, _ := readRecord(t, dir, "s1")
		if rec.ToolRunning == nil {
			t.Fatal("a new record must say whether a tool call runs")
		}
		return *rec.ToolRunning
	}
	if running() {
		t.Fatal("a new session runs no tool call")
	}
	k.setStatus("running")
	k.toolCall("c1", "Agent", bashIn("make check"), "", true)
	k.toolCall("c2", ccSendTool, bashIn("make check"), "", true)
	k.toolCall("c3", "Bash", bashIn("make check"), "c1", true) // the subagent's
	if running() {
		t.Error("subagent calls were recorded as the agent's own")
	}
	k.toolCall("c4", "Bash", bashIn("make check"), "", true)
	if !running() {
		t.Error("a running command was not recorded")
	}
	if rec, _ := readRecord(t, dir, "s1"); len(rec.OpenCalls) != 1 || rec.OpenCalls[0] != (openCallOf("c4", "Bash", bashIn("make check"))) {
		t.Errorf("the running command is not named: %+v", rec.OpenCalls)
	}
	k.toolCall("c4", "", nil, "", false)
	if running() {
		t.Error("a returned command is still recorded")
	}
	// Waiting on background work is not a step of the agent's own.
	k.toolCall("w1", "TaskOutput", json.RawMessage(`{"task_id":"b1","block":true}`), "", true)
	k.toolCall("w2", "Bash", bashIn("sleep 60 && tail -5 /tmp/out.log | grep -c ok"), "", true)
	if running() {
		t.Error("a wait on background work was recorded as a running step")
	}
	k.toolCall("c5", "AskUserQuestion", bashIn("make check"), "", true)
	k.setStatus("waiting")
	if rec, _ := readRecord(t, dir, "s1"); !*rec.ToolRunning || !rec.Waiting {
		t.Errorf("an open question: %+v", rec)
	}
	k.setStatus("running") // answered: the same turn
	if rec, _ := readRecord(t, dir, "s1"); !*rec.ToolRunning || rec.Waiting {
		t.Errorf("after the answer: %+v", rec)
	}
	k.turnEnded("error") // the engine died: what ran is kept
	k.setStatus("idle")
	if !running() {
		t.Error("a turn cut off by its engine's death lost its running call")
	}
	k.setStatus("running") // the next turn
	if running() {
		t.Error("a new turn starts with the old turn's calls")
	}
	k.toolCall("c6", "Bash", bashIn("make check"), "", true)
	k.turnEnded("end_turn")
	if running() {
		t.Error("a finished turn left a call running")
	}
}

// The notice and the count, on their own.
func TestCutOffNoticeAndCount(t *testing.T) {
	if cutOffNotice(nil, nil, nil) != "" || cutOffCount(nil, nil) != "" {
		t.Fatal("nothing cut off: nothing to say")
	}
	one := []agent.BackgroundTaskStartedPayload{{TaskID: "m1", CallID: "c1", Kind: "monitor", Description: "Watch CI"}}
	if got := cutOffCount(one, nil); got != "1 background task was" {
		t.Errorf("count = %q", got)
	}
	// A subagent task with no subagent of its own is still listed, as a subagent.
	orphan := []agent.BackgroundTaskStartedPayload{{TaskID: "a9", CallID: "c9", Kind: "agent", Description: "Lone"}}
	n := cutOffNotice(nil, orphan, nil)
	if !strings.Contains(n, "Subagents that were cut off:\n- subagent \"Lone\", agent id a9") || strings.Contains(n, "Background tasks") {
		t.Errorf("notice: %s", n)
	}
	if got := cutOffCount(orphan, nil); got != "1 subagent was" {
		t.Errorf("count = %q", got)
	}
	// The agent's own call, named with what it ran, and how to pick it up safely.
	calls := []OpenCall{openCallOf("c1", "Bash", json.RawMessage(`{"command":"make\n  build","description":"Build the binaries"}`))}
	n = cutOffNotice(calls, nil, nil)
	for _, w := range []string{
		"Tool calls cut off before they returned:\n- Bash: make build (Build the binaries)",
		"Check its state first", "Never re-run anything destructive",
	} {
		if !strings.Contains(n, w) {
			t.Errorf("notice lacks %q: %s", w, n)
		}
	}
	if strings.Contains(n, "question") || strings.Contains(n, "Background tasks") {
		t.Errorf("notice says too much: %s", n)
	}
	ask := []OpenCall{openCallOf("c2", "Bash", bashIn(`blerg-runner ask "Which database?"`))}
	if n = cutOffNotice(ask, one, nil); !strings.Contains(n, `- Bash: blerg-runner ask "Which database?"`) ||
		!strings.Contains(n, "the message that follows may answer it") || !strings.Contains(n, "- monitor m1: Watch CI") {
		t.Errorf("an ask cut off: %s", n)
	}
	if d := callDetail(json.RawMessage(`{"file_path":"/w/a.go","old_string":"x"}`)); d != "/w/a.go" {
		t.Errorf("detail = %q", d)
	}
}

// A turn that ends while background tasks run does not reset the auto-continue count: a task that
// restarts the daemon (an install run in the background) is not picked up again on every start.
// The count resets once a turn ends with nothing left running.
func TestAutoContinueCountWaitsForBackgroundWork(t *testing.T) {
	dir := t.TempDir() + "/agents"
	k := newRecordKeeper(dir, nil, AgentRecord{SessionID: "s1", WorkDir: "/w", AutoContinues: 1})
	k.setOpenWork([]agent.BackgroundTaskStartedPayload{{TaskID: "b1", CallID: "c1", Kind: "bash"}}, nil)
	k.turnEnded("end_turn")
	if rec, _ := readRecord(t, dir, "s1"); rec.AutoContinues != 1 {
		t.Fatalf("reset while a background task runs: %+v", rec)
	}
	k.setOpenWork(nil, nil)
	k.turnEnded("end_turn")
	if rec, _ := readRecord(t, dir, "s1"); rec.AutoContinues != 0 {
		t.Fatalf("not reset once nothing runs: %+v", rec)
	}
}

// When the engine process ends (an idle close, a crash), the subagents it ran end with it: a final
// subagent_done, nothing left in the record, so a later restart reports nothing again.
func TestEngineEndEndsItsSubagents(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	dir := t.TempDir() + "/agents"
	k := newRecordKeeper(dir, nil, AgentRecord{SessionID: "s1", WorkDir: "/w"})
	d.onOpenWork = k.setOpenWork
	d.startSubagent("toolu_bg", json.RawMessage(`{"subagent_type":"Explore","description":"Find it","run_in_background":true}`))
	d.endSubagent("toolu_bg", "Async agent launched successfully.\nagentId: a1234 (internal ID)", false)
	if rec, _ := readRecord(t, dir, "s1"); len(rec.OpenSubagents) != 1 || rec.OpenSubagents[0].AgentID != "a1234" || len(rec.OpenTasks) != 1 {
		t.Fatalf("open while it runs: %+v", rec)
	}
	d.endBackgroundTasks()
	var ends []agent.SubagentPayload
	for _, ev := range em.snapshot() {
		if p, ok := ev.Payload.(agent.SubagentPayload); ok && ev.Kind == "subagent_done" && !p.Background {
			ends = append(ends, p)
		}
	}
	if len(ends) != 1 || ends[0].ChildID != "toolu_bg" || ends[0].Summary != "stopped: the engine process ended" {
		t.Fatalf("final subagent_done: %+v", ends)
	}
	rec, _ := readRecord(t, dir, "s1")
	if len(rec.OpenSubagents) != 0 || len(rec.OpenTasks) != 0 {
		t.Fatalf("record after the engine ended: %+v", rec)
	}
	if n := cutOffNotice(rec.OpenCalls, rec.OpenTasks, rec.OpenSubagents); n != "" {
		t.Errorf("a later restart would report: %s", n)
	}
	// A notification for it that arrives anyway ends nothing a second time.
	before := len(em.snapshot())
	d.closeBackgroundSubagent(noticeFromBlock("<task-id>a1234</task-id><status>completed</status>"))
	if after := len(em.snapshot()); after != before {
		t.Errorf("a late notification ended the subagent again")
	}
}

// A healthy planned restart leaves idle sessions quiet; sessions with background work
// still have work to recover even when their foreground turn already ended.
func TestPlannedRestartIdleSessionHasNoRow(t *testing.T) {
	factory, records, work, _ := recoveryHostAfter(t, RestartCause{Planned: true, Reason: "service"})
	stubTranscripts(t, "conv-1")
	if err := writeAgentRecord(records, AgentRecord{SessionID: "s1", WorkDir: work, ClaudeSessionID: "conv-1"}); err != nil {
		t.Fatal(err)
	}
	host, sender := factory()
	if host.AdoptRecorded() != 1 {
		t.Fatal("idle session not adopted")
	}
	host.NoteConnected()
	if got := userTexts(sender); len(got) != 0 {
		t.Fatalf("idle planned restart added a visible row: %v", got)
	}
	if countRecoveryPauses(sender) != 0 {
		t.Fatal("idle session reported paused")
	}
}

func TestPausedRestartRowListsCutOffWork(t *testing.T) {
	factory, records, work, _ := recoveryHostAfter(t, RestartCause{Planned: true, Reason: "service"})
	stubTranscripts(t, "conv-1")
	rec := openWorkRecord(work, protocol.InteractionUnattended)
	rec.TurnActive = true
	rec.OpenCalls = []OpenCall{
		openCallOf("c1", "Bash", bashIn("make deploy")),
		openCallOf("c2", "Read", []byte(`{"file_path":"notes.txt"}`)),
	}
	if err := writeAgentRecord(records, rec); err != nil {
		t.Fatal(err)
	}
	host, sender := factory()
	host.AdoptRecorded()
	host.NoteConnected()
	waitUntil(t, "restart pause", func() bool { return countRecoveryPauses(sender) == 1 })
	rows := userTexts(sender)
	if len(rows) != 1 {
		t.Fatalf("want one row, got %v", rows)
	}
	for _, text := range []string{"paused — check the interrupted command\n", "- Bash: make deploy", "- Read: notes.txt", "bash bu90kf38e: Wait for the deploy", `general-purpose "Fix the parser", agent id a5ef89e0e0329265b`, `Explore "Find the callers"`} {
		if !strings.Contains(rows[0], text) {
			t.Errorf("row missing %q: %s", text, rows[0])
		}
	}
	for _, text := range []string{"Never re-run", "Before trusting", "Resume a subagent", "Check its state"} {
		if strings.Contains(rows[0], text) {
			t.Errorf("agent instructions leaked into disclosure: %s", rows[0])
		}
	}
}
