package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// The installer's marker makes the restart planned, with its reason; it is read once.
func TestReadRestartCauseConsumesThePlannedMarkerOnce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, plannedRestartFile), []byte("update\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := ReadRestartCause(dir); !c.Planned || c.Reason != "update" {
		t.Fatalf("with the installer's marker: %+v", c)
	}
	if _, err := os.Stat(filepath.Join(dir, plannedRestartFile)); !os.IsNotExist(err) {
		t.Errorf("the marker was left behind: %v", err)
	}
	if c := ReadRestartCause(dir); c.Planned || c.Reason != "" {
		t.Errorf("the marker explained a second restart: %+v", c)
	}
}

// No marker: the previous run crashed or was killed. No state directory says the same.
func TestReadRestartCauseWithoutAMarkerIsUnplanned(t *testing.T) {
	if c := ReadRestartCause(t.TempDir()); c.Planned {
		t.Errorf("no marker, but planned: %+v", c)
	}
	if c := ReadRestartCause(""); c.Planned {
		t.Errorf("no state directory, but planned: %+v", c)
	}
}

// The daemon's own note of a stop it was asked for makes the restart planned, with no reason, once.
func TestNoteCleanStopMakesTheNextRestartPlannedOnce(t *testing.T) {
	dir := t.TempDir()
	NoteCleanStop(dir)
	NoteCleanStop("") // no state directory: nothing, no panic
	if c := ReadRestartCause(dir); !c.Planned || c.Reason != "" {
		t.Fatalf("after a clean stop: %+v", c)
	}
	if c := ReadRestartCause(dir); c.Planned {
		t.Errorf("the clean stop explained a second restart: %+v", c)
	}
}

// An installer marker that is too old (its restart never happened) or names an unknown reason does
// not give a reason; an old one does not make a later crash planned. Both are removed.
func TestReadRestartCauseIgnoresAStaleOrStrangeMarker(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, plannedRestartFile)
	if err := os.WriteFile(marker, []byte("update\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * plannedRestartMaxAge)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}
	if c := ReadRestartCause(dir); c.Planned {
		t.Errorf("a stale marker made the restart planned: %+v", c)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("the stale marker was left behind: %v", err)
	}
	if err := os.WriteFile(marker, []byte("because I said so\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := ReadRestartCause(dir); !c.Planned || c.Reason != "" {
		t.Errorf("an unknown reason: %+v", c)
	}
}

// The words by cause: planned says nothing the agent ran caused it, unless a cut-off call could
// have; unplanned says ordinary commands cannot.
func TestRestartCauseWords(t *testing.T) {
	update := RestartCause{Planned: true, Reason: "update"}
	want := "The daemon was updated and restarted. Nothing you ran caused it. Continue where you left off; " +
		"check the state of anything that was mid-run first — never re-run anything destructive without checking."
	if got := restartCauseWords(update, nil); got != want {
		t.Errorf("planned update:\n got %q\nwant %q", got, want)
	}
	if got := restartCauseWords(RestartCause{}, nil); got != restartWordsUnplanned ||
		!strings.HasPrefix(got, "The daemon stopped unexpectedly. A test run, a build or ordinary commands cannot cause that") {
		t.Errorf("unplanned: %q", got)
	}
	if got := restartCauseWords(RestartCause{Planned: true}, nil); !strings.HasPrefix(got, "The daemon was restarted. Nothing you ran caused it.") {
		t.Errorf("a clean stop: %q", got)
	}
	tests := openCallOf("t", "Bash", bashIn("go test ./..."))
	install := openCallOf("i", "Bash", bashIn("cd install/desktop/daemon && ./install.sh install"))
	kill := openCallOf("k", "Bash", bashIn("pkill -f vite"))
	if got := restartCauseWords(update, []OpenCall{tests}); !strings.Contains(got, "Nothing you ran caused it.") {
		t.Errorf("a test run is not a cause: %q", got)
	}
	if got := restartCauseWords(update, []OpenCall{install}); strings.Contains(got, "Nothing you ran") {
		t.Errorf("the agent ran the installer, yet it is told it caused nothing: %q", got)
	}
	if got := restartCauseWords(update, []OpenCall{kill}); !strings.Contains(got, "Nothing you ran caused it.") {
		t.Errorf("the installer restarted it, not a pkill: %q", got)
	}
	if got := restartCauseWords(RestartCause{Planned: true}, []OpenCall{kill}); strings.Contains(got, "Nothing you ran") {
		t.Errorf("a stop request with a pkill cut off: %q", got)
	}
}

// End to end through adoption: after a planned restart the model is told so with the cut-off note,
// and never that the restart may have been its own doing.
func TestAdoptAfterAPlannedRestartSaysNothingYouRanCausedIt(t *testing.T) {
	newHost, recDir, workDir, claudeLog := recoveryHostAfter(t, RestartCause{Planned: true, Reason: "update"})
	stubTranscripts(t, "conv-1")
	running := true
	if err := writeAgentRecord(recDir, AgentRecord{SessionID: "s1", Repo: "proj", WorkDir: workDir,
		Interaction: protocol.InteractionUnattended, ClaudeSessionID: "conv-1", TurnActive: true, ToolRunning: &running,
		OpenCalls: []OpenCall{openCallOf("toolu_t", "Bash", bashIn("go test ./internal/..."))}}); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	host.AdoptRecorded()
	host.NoteConnected()
	sender.waitForKind(t, "turn_done")
	argv, _ := os.ReadFile(claudeLog)
	for _, want := range []string{"restarted while you were working", "go test ./internal/...",
		"The daemon was updated and restarted. Nothing you ran caused it.", adoptContinuePrompt} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("the engine was not told %q: %s", want, argv)
		}
	}
	for _, never := range []string{"stopped unexpectedly", "caused by a command you ran", "may have caused"} {
		if strings.Contains(string(argv), never) {
			t.Errorf("the engine was told %q after a planned restart: %s", never, argv)
		}
	}
	if users := userTexts(sender); len(users) != 1 || !strings.Contains(users[0], "Nothing you ran caused it.") ||
		!strings.HasSuffix(users[0], adoptContinuePrompt) {
		t.Errorf("the person sees what the agent was told: %v", users)
	}
}

// Codex: the adoption note it carries to the next turn ends with why the daemon restarted, and its
// shell tool ("bash") counts like Claude's for what may have stopped the daemon.
func TestCodexAdoptionNoteSaysWhyTheDaemonRestarted(t *testing.T) {
	oldTranscriptExists := codexTranscriptExists
	codexTranscriptExists = func(string) bool { return true }
	t.Cleanup(func() { codexTranscriptExists = oldTranscriptExists })
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	root, records := t.TempDir(), t.TempDir()
	call := openCallOf("command-1", "bash", bashIn("go test ./..."))
	rec := AgentRecord{SessionID: "cx1", Engine: "codex", CodexThreadID: "cx-thread-1", WorkDir: root, Model: "gpt-6.1-sol",
		Interaction: protocol.InteractionInteractive, TurnActive: true, Waiting: true, OpenCalls: []OpenCall{call}}
	if err := writeAgentRecord(records, rec); err != nil {
		t.Fatal(err)
	}
	h := NewAgentHost(&agentTestSender{}, AgentHostConfig{ReposRoot: root, HomeDir: t.TempDir(), RecordsDir: records,
		RestartCause: RestartCause{Planned: true, Reason: "update"}})
	defer func() { h.Freeze(); h.Kill("cx1") }()
	if n := h.AdoptRecorded(); n != 1 {
		t.Fatalf("adopted %d sessions", n)
	}
	cx, ok := h.get("cx1").loop.(*codexDriver)
	if !ok {
		t.Fatal("not a Codex driver")
	}
	cx.mu.Lock()
	note := cx.adoptionNote
	cx.mu.Unlock()
	if !strings.Contains(note, "go test ./...") || !strings.HasSuffix(note, restartCauseWords(RestartCause{Planned: true, Reason: "update"}, nil)) {
		t.Errorf("Codex adoption note: %q", note)
	}
	if strings.Contains(restartCauseWords(RestartCause{Planned: true}, []OpenCall{openCallOf("k", "bash", bashIn("pkill -f vite"))}), "Nothing you ran") {
		t.Error("a Codex bash pkill is not counted for a stop request")
	}
}
