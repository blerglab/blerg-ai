package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// recoveryHost is an agent host that keeps recovery records, on the fake per-turn `claude`
// (claudecode_test.go: it reports session id "cc-sess-1" and logs its argv).
func recoveryHost(t *testing.T) (host func() (*AgentHost, *agentTestSender), recDir, workDir, claudeLog string) {
	t.Helper()
	engineDir := fakeClaude(t)
	t.Setenv("PATH", engineDir+":"+os.Getenv("PATH"))
	claudeLog = filepath.Join(engineDir, "calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", claudeLog)
	reposRoot := t.TempDir()
	workDir = filepath.Join(reposRoot, "proj")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recDir = filepath.Join(t.TempDir(), "agents")
	home := t.TempDir()
	oldDelay := adoptContinueDelay
	adoptContinueDelay = 10 * time.Millisecond
	t.Cleanup(func() { adoptContinueDelay = oldDelay })
	host = func() (*AgentHost, *agentTestSender) {
		sender := &agentTestSender{}
		h := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: home, ClaudeCode: true, RecordsDir: recDir})
		// Nothing may still be writing a record when the test's directories are removed.
		t.Cleanup(func() {
			h.Freeze()
			for _, id := range h.ActiveIDs() {
				h.Kill(id)
			}
		})
		return h, sender
	}
	return host, recDir, workDir, claudeLog
}

func readRecord(t *testing.T, dir, id string) (AgentRecord, bool) {
	t.Helper()
	data, err := os.ReadFile(agentRecordPath(dir, id))
	if err != nil {
		return AgentRecord{}, false
	}
	var rec AgentRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("record %s: %v", id, err)
	}
	return rec, true
}

func waitRecord(t *testing.T, dir, id, what string, ok func(AgentRecord) bool) AgentRecord {
	t.Helper()
	var last AgentRecord
	waitUntil(t, what, func() bool {
		rec, found := readRecord(t, dir, id)
		last = rec
		return found && ok(rec)
	})
	return last
}

func errorTexts(s *agentTestSender) []string {
	var out []string
	for _, ev := range s.agentEvents() {
		if ev.Kind != "error" {
			continue
		}
		var p agent.ErrorPayload
		_ = json.Unmarshal(ev.Payload, &p)
		out = append(out, p.Message)
	}
	return out
}

func userTexts(s *agentTestSender) []string {
	var out []string
	for _, ev := range s.agentEvents() {
		if ev.Kind != "user_message" {
			continue
		}
		var p agent.UserMessagePayload
		_ = json.Unmarshal(ev.Payload, &p)
		out = append(out, p.Source+":"+p.Text)
	}
	return out
}

func sentTypes(s *agentTestSender) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.msgs {
		switch v := m.(type) {
		case protocol.SessionStarted:
			out = append(out, "session_started")
		case protocol.SessionEnded:
			out = append(out, "session_ended:"+v.SessionID)
		case protocol.SessionStateChanged:
			out = append(out, "state:"+v.Status)
		}
	}
	return out
}

func stubTranscripts(t *testing.T, ids ...string) {
	t.Helper()
	old := claudeTranscriptExists
	claudeTranscriptExists = func(id string) bool {
		for _, known := range ids {
			if known == id {
				return true
			}
		}
		return false
	}
	t.Cleanup(func() { claudeTranscriptExists = old })
}

// A record is private, round-trips, and holds only what adoption needs.
func TestAgentRecordRoundTripIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	rec := AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: "/w/proj", SessionToken: "tok", ClaudeSessionID: "c1", TurnActive: true}
	if err := writeAgentRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("records directory is %v, want 0700", info.Mode().Perm())
	}
	if info, _ := os.Stat(agentRecordPath(dir, "s1")); info.Mode().Perm() != 0o600 {
		t.Errorf("record is %v, want 0600", info.Mode().Perm())
	}
	got := listAgentRecords(dir)
	if len(got) != 1 || got[0].ClaudeSessionID != "c1" || !got[0].TurnActive || got[0].Version != agentRecordVersion {
		t.Fatalf("round trip: %+v", got)
	}
	raw, _ := os.ReadFile(agentRecordPath(dir, "s1"))
	for _, never := range []string{"initial_prompt", "git_token", "clone_from", "mcp_gateway", "restrict", "sandbox"} {
		if strings.Contains(string(raw), never) {
			t.Errorf("the record has a %q field", never)
		}
	}
}

// A file that is not a record of the session it is named after is never acted on.
func TestListAgentRecordsRefusesWhatItCannotTrust(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	if err := writeAgentRecord(dir, AgentRecord{SessionID: "good", WorkDir: "/w"}); err != nil {
		t.Fatal(err)
	}
	misnamed, _ := json.Marshal(AgentRecord{Version: agentRecordVersion, SessionID: "someone-else", WorkDir: "/w"})
	for name, body := range map[string][]byte{
		"renamed.json":  misnamed,
		"garbage.json":  []byte("{not json"),
		"old.json":      []byte(`{"version":0,"session_id":"old"}`),
		"half.json.tmp": []byte("{"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := listAgentRecords(dir)
	if len(got) != 1 || got[0].SessionID != "good" {
		t.Fatalf("only the good record is usable: %+v", got)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 1 {
		t.Errorf("the unusable files should be gone, %d files left", len(left))
	}
	// A directory another user could write into is not read at all.
	old := ownedByDaemon
	ownedByDaemon = func(os.FileInfo) bool { return false }
	defer func() { ownedByDaemon = old }()
	if got := listAgentRecords(dir); len(got) != 0 {
		t.Errorf("records in a directory that is not the daemon's were used: %+v", got)
	}
}

// A Claude Code session gets a record at spawn, the record follows the session, and a kill
// removes it. The initial prompt is never in it.
func TestAgentSpawnKeepsARecordAndKillRemovesIt(t *testing.T) {
	newHost, recDir, workDir, _ := recoveryHost(t)
	host, sender := newHost()
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s1", Repo: "proj", Kind: "agent",
		InitialPrompt: "the secret kickoff", Interaction: protocol.InteractionUnattended,
		SessionToken: "tok-1", ExtraEnv: map[string]string{"FOO": "bar"},
	})
	sender.waitForKind(t, "turn_done")
	rec := waitRecord(t, recDir, "s1", "the record to settle after the turn", func(r AgentRecord) bool {
		return r.ClaudeSessionID == "cc-sess-1" && !r.TurnActive && r.EnginePID == 0
	})
	if rec.WorkDir != workDir || rec.Repo != "proj" || rec.Interaction != protocol.InteractionUnattended ||
		rec.SessionToken != "tok-1" || rec.ExtraEnv["FOO"] != "bar" || rec.Model == "" {
		t.Errorf("record: %+v", rec)
	}
	raw, _ := os.ReadFile(agentRecordPath(recDir, "s1"))
	if strings.Contains(string(raw), "the secret kickoff") {
		t.Error("the initial prompt was written to the record")
	}
	host.SetModel("s1", "claude-opus-5", "")
	waitRecord(t, recDir, "s1", "the model change to reach the record", func(r AgentRecord) bool { return r.Model == "claude-opus-5" })

	host.Kill("s1")
	if _, found := readRecord(t, recDir, "s1"); found {
		t.Error("the record survived the kill")
	}
}

// Sessions that must not be brought back have no record.
func TestUnrecoverableSessionsKeepNoRecord(t *testing.T) {
	newHost, recDir, _, _ := recoveryHost(t)
	host, sender := newHost()
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "restricted", Repo: "proj", Kind: "agent",
		InitialPrompt: "go", RestrictTools: true})
	sender.waitForKind(t, "turn_done")
	if _, found := readRecord(t, recDir, "restricted"); found {
		t.Error("a restricted session has a recovery record")
	}
	for _, msg := range []protocol.SpawnSession{
		{Sandbox: true}, {MCPGateway: &protocol.MCPGatewayConfig{}}, {RestrictTools: true},
	} {
		if host.recordable(msg) {
			t.Errorf("recordable(%+v) = true", msg)
		}
	}
	if !host.recordable(protocol.SpawnSession{}) {
		t.Error("a plain session is not recordable")
	}
	bare := NewAgentHost(&agentTestSender{}, AgentHostConfig{})
	if bare.recordable(protocol.SpawnSession{}) {
		t.Error("a host with no records directory keeps records")
	}
}

// Once the daemon is stopping nothing is written: the engine's death must not clear the
// "a turn was running" the next daemon needs. A kill still removes the record.
func TestFrozenRecordIsNotRewritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	var frozen atomic.Bool
	k := newRecordKeeper(dir, &frozen, AgentRecord{SessionID: "s1", WorkDir: "/w"})
	k.setTurnActive(true)
	frozen.Store(true)
	k.turnEnded("end_turn")
	k.setTurnActive(false)
	k.setClaudeSessionID("later")
	if rec, _ := readRecord(t, dir, "s1"); !rec.TurnActive || rec.ClaudeSessionID != "" {
		t.Errorf("a frozen record was rewritten: %+v", rec)
	}
	k.delete()
	if _, found := readRecord(t, dir, "s1"); found {
		t.Error("a kill while frozen left the record")
	}
	var none *recordKeeper // a session with no record takes every call
	none.setTurnActive(true)
	none.turnEnded("end_turn")
	none.setEngine(1, true)
	none.delete()
}

// A turn that ended because its engine died was cut off, whoever killed the engine: going idle
// after it does not clear "a turn was running". (This is what covers a stop signal that reaches
// the engine before the daemon has frozen its records.) A turn that then finishes clears it.
func TestEngineDeathDoesNotClearTheCutOffTurn(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	var frozen atomic.Bool
	k := newRecordKeeper(dir, &frozen, AgentRecord{SessionID: "s1", WorkDir: "/w", AutoContinues: 1})
	k.setTurnActive(true)
	k.turnEnded("error") // the engine exited mid-turn
	k.setTurnActive(false)
	if rec, _ := readRecord(t, dir, "s1"); !rec.TurnActive || rec.AutoContinues != 1 {
		t.Fatalf("a turn cut off by its engine's death was recorded as finished: %+v", rec)
	}
	k.setTurnActive(true)
	k.turnEnded("end_turn")
	k.setTurnActive(false)
	if rec, _ := readRecord(t, dir, "s1"); rec.TurnActive || rec.AutoContinues != 0 {
		t.Fatalf("a finished turn did not clear the record: %+v", rec)
	}
}

// The engine a record names is the latest one: a late "went away" about an older process does
// not erase it.
func TestRecordEngineIsNotErasedByAnOlderProcess(t *testing.T) {
	old := processStartTime
	processStartTime = func(pid int) string { return "start-of-" + string(rune('0'+pid)) }
	defer func() { processStartTime = old }()
	dir := filepath.Join(t.TempDir(), "agents")
	var frozen atomic.Bool
	k := newRecordKeeper(dir, &frozen, AgentRecord{SessionID: "s1", WorkDir: "/w"})
	k.setEngine(3, true)
	k.setEngine(4, true)
	k.setEngine(3, false) // the old process's exit, reported late
	if rec, _ := readRecord(t, dir, "s1"); rec.EnginePID != 4 || rec.EngineStarted != "start-of-4" {
		t.Fatalf("record: %+v", rec)
	}
	k.setEngine(4, false)
	if rec, _ := readRecord(t, dir, "s1"); rec.EnginePID != 0 || rec.EngineStarted != "" {
		t.Fatalf("record: %+v", rec)
	}
}

// Only one process may use a records directory.
func TestLockAgentRecordsIsExclusive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	if !LockAgentRecords(dir) {
		t.Fatal("the first lock was refused")
	}
	held := agentRecordsLock
	defer func() { _ = held.Close() }()
	if LockAgentRecords(dir) {
		t.Fatal("a second holder got the lock")
	}
	if LockAgentRecords("") {
		t.Error("no directory, but a lock")
	}
	if got := listAgentRecords(dir); len(got) != 0 {
		t.Errorf("the lock file was read as a record: %+v", got)
	}
}

// The heart of it: a recorded session is hosted again without a start, resumes its Claude
// conversation, and tells the model what happened on the engine's line only.
func TestAdoptResumesAnInteractiveSessionAndWaits(t *testing.T) {
	newHost, recDir, workDir, claudeLog := recoveryHost(t)
	stubTranscripts(t, "conv-1")
	if err := writeAgentRecord(recDir, AgentRecord{
		SessionID: "s1", Repo: "proj", WorkDir: workDir, Interaction: protocol.InteractionInteractive,
		Model: "claude-opus-5", ClaudeSessionID: "conv-1", TurnActive: true, EnginePID: 0,
	}); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	if n := host.AdoptRecorded(); n != 1 || !host.Has("s1") {
		t.Fatalf("adopted %d, hosted %v", n, host.Has("s1"))
	}
	if types := sentTypes(sender); len(types) != 0 {
		t.Errorf("an adoption is not a start, but the server was sent %v", types)
	}
	notices := errorTexts(sender)
	if len(notices) != 1 || !strings.Contains(notices[0], "host restarted") || !strings.Contains(notices[0], "conversation was restored") ||
		!strings.Contains(notices[0], "cut off") {
		t.Fatalf("the person is told once what happened: %v", notices)
	}
	// Interactive: nothing runs until the person says something.
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(claudeLog); err == nil {
		t.Fatal("an interactive session ran a turn by itself after the restart")
	}
	if host.States()["s1"] != "idle" {
		t.Errorf("state after adoption = %q, want idle", host.States()["s1"])
	}

	host.UserMessage("s1", "where were we?", "chat")
	sender.waitForKind(t, "turn_done")
	argv, _ := os.ReadFile(claudeLog)
	for _, want := range []string{"--resume conv-1", "restarted while you were working", "do not run it again", "where were we?", "claude-opus-5", "This session is interactive"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("the engine was not given %q: %s", want, argv)
		}
	}
	if got := userTexts(sender); len(got) != 1 || got[0] != "chat:where were we?" {
		t.Errorf("the transcript must keep the person's own words, got %v", got)
	}

	// The note is said once.
	if err := os.Remove(claudeLog); err != nil {
		t.Fatal(err)
	}
	host.UserMessage("s1", "and then?", "chat")
	sender.waitForKindCount(t, "turn_done", 2)
	argv, _ = os.ReadFile(claudeLog)
	if strings.Contains(string(argv), "restarted") {
		t.Errorf("the adoption note was repeated: %s", argv)
	}
	// The record follows the adopted session like any other.
	waitRecord(t, recDir, "s1", "the adopted record to settle", func(r AgentRecord) bool {
		return r.ClaudeSessionID == "cc-sess-1" && !r.TurnActive
	})
	// A second pass (there is none in the daemon, but it must be harmless) adopts nothing twice.
	if n := host.AdoptRecorded(); n != 0 {
		t.Errorf("a hosted session was adopted again (%d)", n)
	}
}

// Until a turn has run, the record still says a turn was cut off: a second restart before the
// person has written anything must tell the model about the first.
func TestAdoptedRecordKeepsTheCutOffUntilATurnRuns(t *testing.T) {
	newHost, recDir, workDir, _ := recoveryHost(t)
	stubTranscripts(t, "conv-1")
	if err := writeAgentRecord(recDir, AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: workDir,
		ClaudeSessionID: "conv-1", TurnActive: true, EnginePID: 4242, EngineStarted: "long ago"}); err != nil {
		t.Fatal(err)
	}
	host, _ := newHost()
	host.AdoptRecorded()
	rec, _ := readRecord(t, recDir, "s1")
	if !rec.TurnActive || rec.ClaudeSessionID != "conv-1" {
		t.Errorf("after adoption: %+v", rec)
	}
	if rec.EnginePID != 0 {
		t.Errorf("the dead daemon's engine pid was carried over: %d", rec.EnginePID)
	}
}

// Nobody reads an unattended session, so a cut-off turn is picked up again — once.
func TestAdoptContinuesAnUnattendedCutOffTurnOnce(t *testing.T) {
	newHost, recDir, workDir, claudeLog := recoveryHost(t)
	stubTranscripts(t, "conv-1")
	rec := AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: workDir, Interaction: protocol.InteractionUnattended,
		ClaudeSessionID: "conv-1", TurnActive: true}
	if err := writeAgentRecord(recDir, rec); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	host.AdoptRecorded()
	// Not before the daemon is connected: the server's answer to the hello may be to stop it.
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(claudeLog); err == nil {
		t.Fatal("a cut-off turn was continued before the daemon had a server to ask")
	}
	if rec, _ := readRecord(t, recDir, "s1"); rec.AutoContinues != 1 {
		t.Fatalf("the continue must be counted before it runs: %+v", rec)
	}
	host.NoteConnected()
	sender.waitForKind(t, "turn_done")
	argv, _ := os.ReadFile(claudeLog)
	if !strings.Contains(string(argv), adoptContinuePrompt) || !strings.Contains(string(argv), "do not run it again") ||
		!strings.Contains(string(argv), "--resume conv-1") {
		t.Fatalf("the cut-off turn was not continued with the note: %s", argv)
	}
	if got := userTexts(sender); len(got) != 1 || got[0] != "system:"+adoptContinuePrompt {
		t.Errorf("the continue is the system's message, not the person's: %v", got)
	}
	if n := errorTexts(sender); len(n) != 1 || !strings.Contains(n[0], "being asked to continue") {
		t.Errorf("notice: %v", n)
	}
	// It finished, so the next cut-off turn may be continued again.
	waitRecord(t, recDir, "s1", "a finished turn to reset the counter", func(r AgentRecord) bool { return r.AutoContinues == 0 && !r.TurnActive })
	host.Kill("s1")

	// Cut off AGAIN after being continued (the turn's own command restarts the daemon): it waits.
	rec.AutoContinues = 1
	if err := writeAgentRecord(recDir, rec); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(claudeLog); err != nil {
		t.Fatal(err)
	}
	host2, sender2 := newHost()
	host2.NoteConnected()
	host2.AdoptRecorded()
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(claudeLog); err == nil {
		t.Fatal("a turn that was already continued once was run a third time")
	}
	if n := errorTexts(sender2); len(n) != 1 || !strings.Contains(n[0], "waiting for a message") {
		t.Errorf("notice: %v", n)
	}
}

// No transcript: the conversation starts fresh, and the model and the person are told so.
func TestAdoptWithoutATranscriptStartsFresh(t *testing.T) {
	newHost, recDir, workDir, claudeLog := recoveryHost(t)
	stubTranscripts(t) // none
	if err := writeAgentRecord(recDir, AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: workDir,
		Interaction: protocol.InteractionUnattended, ClaudeSessionID: "gone", TurnActive: true}); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	host.NoteConnected()
	host.AdoptRecorded()
	if n := errorTexts(sender); len(n) != 1 || !strings.Contains(n[0], "could not be restored") {
		t.Fatalf("notice: %v", n)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(claudeLog); err == nil {
		t.Fatal("a session with no conversation to continue was continued by itself")
	}
	host.UserMessage("s1", "hello", "chat")
	sender.waitForKind(t, "turn_done")
	argv, _ := os.ReadFile(claudeLog)
	if strings.Contains(string(argv), "--resume") || !strings.Contains(string(argv), "earlier conversation could not be restored") {
		t.Errorf("want a fresh conversation that is told so: %s", argv)
	}
}

// A session that never had a conversation is simply hosted again, with nothing to explain.
func TestAdoptOfASessionThatNeverRanATurn(t *testing.T) {
	newHost, recDir, workDir, claudeLog := recoveryHost(t)
	if err := writeAgentRecord(recDir, AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: workDir}); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	host.AdoptRecorded()
	host.UserMessage("s1", "first", "chat")
	sender.waitForKind(t, "turn_done")
	argv, _ := os.ReadFile(claudeLog)
	if strings.Contains(string(argv), "[system]") || strings.Contains(string(argv), "--resume") {
		t.Errorf("nothing to resume and nothing to say: %s", argv)
	}
}

// A record that cannot be acted on is removed, and its session's end is reported once connected.
func TestAdoptFailureRemovesTheRecordAndReportsTheEnd(t *testing.T) {
	newHost, recDir, workDir, _ := recoveryHost(t)
	gone := filepath.Join(filepath.Dir(workDir), "deleted")
	if err := writeAgentRecord(recDir, AgentRecord{SessionID: "lost", Repo: "deleted", WorkDir: gone, ClaudeSessionID: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := writeAgentRecord(recDir, AgentRecord{SessionID: "relative", Repo: "proj", WorkDir: "proj"}); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	if n := host.AdoptRecorded(); n != 0 || host.Has("lost") {
		t.Fatalf("adopted %d", n)
	}
	if left := listAgentRecords(recDir); len(left) != 0 {
		t.Errorf("records left behind: %+v", left)
	}
	host.ReportAdoptionFailures()
	if types := sentTypes(sender); len(types) != 0 {
		t.Errorf("nothing is sent before the daemon is connected: %v", types)
	}
	host.NoteConnected()
	types := strings.Join(sentTypes(sender), " ")
	if !strings.Contains(types, "session_ended:lost") || !strings.Contains(types, "session_ended:relative") {
		t.Errorf("ends reported: %v", types)
	}
	host.NoteConnected()
	if got := len(sentTypes(sender)); got != 2 {
		t.Errorf("the ends were reported again (%d messages)", got)
	}
}

// Between the claim and the hosting a session is this daemon's: it is reported, a message for
// it waits and is delivered in order, and a stop means it never comes back.
func TestSessionsBeingAdoptedAreClaimedHeldAndStoppable(t *testing.T) {
	newHost, recDir, workDir, claudeLog := recoveryHost(t)
	stubTranscripts(t, "conv-1", "conv-2")
	for id, conv := range map[string]string{"keep": "conv-1", "stop": "conv-2"} {
		if err := writeAgentRecord(recDir, AgentRecord{SessionID: id, Repo: "proj", WorkDir: workDir, ClaudeSessionID: conv}); err != nil {
			t.Fatal(err)
		}
	}
	host, sender := newHost()
	if n := host.BeginAdoption(); n != 2 {
		t.Fatalf("claimed %d", n)
	}
	if ids := host.ActiveIDs(); len(ids) != 2 {
		t.Fatalf("claimed sessions must be reported as this daemon's: %v", ids)
	}
	if host.Has("keep") {
		t.Fatal("hosted before RunAdoption")
	}
	host.UserMessage("keep", "typed during the restart", "chat")
	host.UserMessage("stop", "never delivered", "chat")
	if n := errorTexts(sender); len(n) != 0 {
		t.Fatalf("a message for a session being restored was refused: %v", n)
	}
	host.DropRecord("stop") // kill_session arrived: it was stopped while the daemon was down

	if n := host.RunAdoption(); n != 1 || !host.Has("keep") || host.Has("stop") {
		t.Fatalf("adopted %d (keep hosted=%v, stop hosted=%v)", n, host.Has("keep"), host.Has("stop"))
	}
	sender.waitForKind(t, "turn_done")
	argv, _ := os.ReadFile(claudeLog)
	if !strings.Contains(string(argv), "typed during the restart") || strings.Contains(string(argv), "never delivered") {
		t.Errorf("held messages: %s", argv)
	}
	if got := userTexts(sender); len(got) != 1 || got[0] != "chat:typed during the restart" {
		t.Errorf("transcript: %v", got)
	}
	if _, found := readRecord(t, recDir, "stop"); found {
		t.Error("the stopped session's record is still there")
	}
	if ids := host.ActiveIDs(); len(ids) != 1 || ids[0] != "keep" {
		t.Errorf("after adoption the daemon reports %v", ids)
	}
}

// A daemon that would no longer run Claude Code cannot continue a Claude Code conversation.
func TestAdoptRefusesAnotherDriver(t *testing.T) {
	_, recDir, workDir, _ := recoveryHost(t)
	if err := writeAgentRecord(recDir, AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: workDir}); err != nil {
		t.Fatal(err)
	}
	// No forced Claude Code, no CLI preference, an API key: the native loop.
	host := NewAgentHost(&agentTestSender{}, AgentHostConfig{RecordsDir: recDir, APIKey: "k", HomeDir: t.TempDir()})
	if n := host.AdoptRecorded(); n != 0 || host.Has("s1") {
		t.Fatal("a Claude Code session was adopted onto another driver")
	}
}

// A message for a session the daemon is not hosting is reported in its transcript, and a stop
// for one removes its record.
func TestUnhostedSessionMessageAndStop(t *testing.T) {
	newHost, recDir, workDir, _ := recoveryHost(t)
	host, sender := newHost()
	host.UserMessage("nobody", "hello?", "chat")
	if n := errorTexts(sender); len(n) != 1 || !strings.Contains(n[0], "not delivered") {
		t.Errorf("an undeliverable message was swallowed: %v", n)
	}
	if err := writeAgentRecord(recDir, AgentRecord{SessionID: "stopped-while-down", Repo: "proj", WorkDir: workDir}); err != nil {
		t.Fatal(err)
	}
	host.DropRecord("stopped-while-down")
	if left := listAgentRecords(recDir); len(left) != 0 {
		t.Errorf("the record of a stopped session is still there: %+v", left)
	}
}

// The engine a dead daemon left running is stopped — if it is still that process.
func TestStopLeftoverEngineChecksItIsTheSameProcess(t *testing.T) {
	old := leftoverKillGrace
	leftoverKillGrace = 3 * time.Second
	defer func() { leftoverKillGrace = old }()
	start := func() *exec.Cmd {
		cmd := exec.Command("sleep", "60")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
		return cmd
	}
	alive := func(cmd *exec.Cmd) bool { return syscall.Kill(cmd.Process.Pid, 0) == nil }

	other := start()
	if stopLeftoverEngine(AgentRecord{SessionID: "s", EnginePID: other.Process.Pid, EngineStarted: "Mon Jan  1 00:00:00 2001"}) {
		t.Error("a process with another start time was taken for the engine")
	}
	if stopLeftoverEngine(AgentRecord{SessionID: "s", EnginePID: other.Process.Pid}) {
		t.Error("a record with no start time was acted on")
	}
	if !alive(other) {
		t.Fatal("a process that is not the recorded engine was killed")
	}

	engine := start()
	started := processStartTime(engine.Process.Pid)
	if started == "" {
		t.Skip("ps does not report a start time here")
	}
	done := make(chan struct{})
	go func() { _, _ = engine.Process.Wait(); close(done) }()
	if !stopLeftoverEngine(AgentRecord{SessionID: "s", EnginePID: engine.Process.Pid, EngineStarted: started}) {
		t.Fatal("the leftover engine was not recognised")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the leftover engine is still running")
	}
}

// The streaming engine: the note goes on the stdin line, survives the retry after a refused
// resume, and becomes the "could not be restored" note then.
func TestSteeringAdoptionNoteFollowsARefusedResume(t *testing.T) {
	env, logPath := installFakeClaude(t)
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, env)
	d.steer.legacy = false
	var reported []string
	d.onSessionID = func(id string) { reported = append(reported, id) }
	d.adopt("bad-conv", adoption{hadConversation: true, cutOff: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	d.Enqueue("hello", "chat")
	waitIdle(t, em, 1)
	o := em.outline()
	if em.count("user:hello") != 1 {
		t.Fatalf("the transcript keeps the message as written, once: %v", o)
	}
	joined := strings.Join(o, "\n")
	if !strings.Contains(joined, "text:ok:"+adoptNoteLost+"\n\nhello") {
		t.Fatalf("after the refused resume the engine must be told the conversation is gone: %v", o)
	}
	st := starts(logLines(t, logPath))
	if len(st) != 2 || !strings.Contains(st[0], "--resume bad-conv") || strings.Contains(st[1], "--resume") {
		t.Fatalf("starts: %v", st)
	}
	if len(reported) == 0 || strings.HasPrefix(reported[len(reported)-1], "bad-") {
		t.Errorf("the record was not told the new conversation's id: %v", reported)
	}

	d.Enqueue("again", "chat")
	waitIdle(t, em, 2)
	if !contains(em.outline(), "text:ok:again") {
		t.Errorf("the note was repeated on the second message: %v", em.outline())
	}
}

// A clean resume of a session that was idle says nothing to the model.
func TestSteeringAdoptionOfAnIdleSessionSaysNothing(t *testing.T) {
	env, logPath := installFakeClaude(t)
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, env)
	d.steer.legacy = false
	d.adopt("conv-9", adoption{hadConversation: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.Enqueue("hello", "chat")
	waitIdle(t, em, 1)
	if !contains(em.outline(), "text:ok:hello") {
		t.Errorf("outline: %v", em.outline())
	}
	if st := starts(logLines(t, logPath)); len(st) != 1 || !strings.Contains(st[0], "--resume conv-9") {
		t.Errorf("starts: %v", st)
	}
}
