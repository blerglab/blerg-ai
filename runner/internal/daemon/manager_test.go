package daemon

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/creack/pty"
)

// TestResizeSchedulesDebouncedRefresh verifies that a burst of resizes coalesces
// into exactly one (debounced) refresh, carrying the right session ID. The
// refresh is what repaints the tmux client to clear post-resize residue; here we
// inject a fake refreshFunc so no real tmux is needed.
func TestResizeSchedulesDebouncedRefresh(t *testing.T) {
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})
	mgr.refreshDebounce = 20 * time.Millisecond
	called := make(chan string, 8)
	mgr.refreshFunc = func(id string) { called <- id }

	// A real PTY pair so sess.Resize succeeds without spawning a process.
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()

	mgr.mu.Lock()
	mgr.sessions["sess-r"] = &Session{ID: "sess-r", ptmx: ptmx, cmd: &exec.Cmd{}}
	mgr.mu.Unlock()

	// Three rapid resizes within the debounce window.
	for _, rc := range [][2]int{{100, 40}, {120, 40}, {120, 50}} {
		raw, _ := json.Marshal(protocol.ResizeSession{
			Type: "resize_session", SessionID: "sess-r", Cols: rc[0], Rows: rc[1],
		})
		mgr.handleResizeSession(raw)
	}

	select {
	case id := <-called:
		if id != "sess-r" {
			t.Errorf("refresh for %q, want sess-r", id)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected a debounced refresh, got none")
	}

	// The burst must coalesce — no second refresh.
	select {
	case id := <-called:
		t.Errorf("expected a single coalesced refresh, got another for %q", id)
	case <-time.After(80 * time.Millisecond):
	}
}

// TestDegenerateResizeIgnored verifies a too-small resize (the frontend's
// gridCount floor of 20x5, produced by an unlaid-out terminal element while
// navigating away) is ignored — it does NOT schedule a refresh, so the pane keeps
// its last good size and the state poller can still see Claude's status lines.
func TestDegenerateResizeIgnored(t *testing.T) {
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})
	mgr.refreshDebounce = 20 * time.Millisecond
	called := make(chan string, 8)
	mgr.refreshFunc = func(id string) { called <- id }

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()

	mgr.mu.Lock()
	mgr.sessions["sess-d"] = &Session{ID: "sess-d", ptmx: ptmx, cmd: &exec.Cmd{}}
	mgr.mu.Unlock()

	raw, _ := json.Marshal(protocol.ResizeSession{
		Type: "resize_session", SessionID: "sess-d", Cols: 20, Rows: 5,
	})
	mgr.handleResizeSession(raw)

	select {
	case id := <-called:
		t.Errorf("degenerate resize should be ignored, but scheduled a refresh for %q", id)
	case <-time.After(80 * time.Millisecond):
		// expected: no refresh scheduled
	}
}

// TestRequestScrollbackSendsCapturedHistory verifies the daemon answers a
// request_scrollback by capturing the tmux scrollback and sending it back as a
// SessionScrollback (base64-encoded), for a session it manages.
func TestRequestScrollbackSendsCapturedHistory(t *testing.T) {
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})
	mgr.scrollbackFunc = func(id string, maxLines int) (string, error) {
		if id != "sess-sb" {
			return "", errors.New("unexpected session id: " + id)
		}
		return "old line 1\nold line 2", nil
	}
	mgr.mu.Lock()
	mgr.sessions["sess-sb"] = &Session{ID: "sess-sb"}
	mgr.mu.Unlock()

	raw, _ := json.Marshal(protocol.RequestScrollback{Type: "request_scrollback", SessionID: "sess-sb"})
	mgr.handleRequestScrollback(raw)

	select {
	case msg := <-sender.ch:
		sb, ok := msg.(protocol.SessionScrollback)
		if !ok {
			t.Fatalf("expected SessionScrollback, got %T", msg)
		}
		if sb.SessionID != "sess-sb" {
			t.Errorf("session id = %q, want sess-sb", sb.SessionID)
		}
		got, err := base64.StdEncoding.DecodeString(sb.Data)
		if err != nil {
			t.Fatalf("data not base64: %v", err)
		}
		if string(got) != "old line 1\nold line 2" {
			t.Errorf("data = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a SessionScrollback, got none")
	}
}

// TestRequestScrollbackUnknownSessionIgnored verifies the daemon does not capture
// or reply for a session it does not manage (avoids exec'ing tmux for stray IDs).
func TestRequestScrollbackUnknownSessionIgnored(t *testing.T) {
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})
	captured := false
	mgr.scrollbackFunc = func(string, int) (string, error) { captured = true; return "x", nil }

	raw, _ := json.Marshal(protocol.RequestScrollback{Type: "request_scrollback", SessionID: "nope"})
	mgr.handleRequestScrollback(raw)

	if captured {
		t.Error("scrollbackFunc should not run for an unmanaged session")
	}
	select {
	case msg := <-sender.ch:
		t.Errorf("expected no message, got %T", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestRequestScrollbackPassesMaxLines verifies the message's MaxLines is forwarded
// as-is into the injected capture func, so a bounded request from the browser
// actually bounds the tmux capture rather than always pulling full history.
func TestRequestScrollbackPassesMaxLines(t *testing.T) {
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})
	var gotMaxLines int
	var gotCalls int
	mgr.scrollbackFunc = func(id string, maxLines int) (string, error) {
		gotMaxLines = maxLines
		gotCalls++
		return "line", nil
	}
	mgr.mu.Lock()
	mgr.sessions["sess-ml"] = &Session{ID: "sess-ml"}
	mgr.mu.Unlock()

	raw, _ := json.Marshal(protocol.RequestScrollback{
		Type: "request_scrollback", SessionID: "sess-ml", MaxLines: 500,
	})
	mgr.handleRequestScrollback(raw)

	select {
	case <-sender.ch:
	case <-time.After(time.Second):
		t.Fatal("expected a SessionScrollback, got none")
	}
	if gotCalls != 1 {
		t.Fatalf("scrollbackFunc called %d times, want 1", gotCalls)
	}
	if gotMaxLines != 500 {
		t.Errorf("scrollbackFunc maxLines = %d, want 500", gotMaxLines)
	}
}

// TestRequestScrollbackIncludesModePrefix verifies the daemon queries pane mode
// state via the injected modeSyncFunc and carries the result on
// SessionScrollback.ModePrefix, base64-encoded, so the client can replay it into
// the live terminal before applying the scrollback text.
func TestRequestScrollbackIncludesModePrefix(t *testing.T) {
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})
	mgr.scrollbackFunc = func(string, int) (string, error) { return "old text", nil }
	const wantSeq = "\x1b[?1h\x1b[?25h"
	mgr.modeSyncFunc = func(id string) string {
		if id != "sess-mp" {
			t.Errorf("modeSyncFunc called with %q, want sess-mp", id)
		}
		return wantSeq
	}
	mgr.mu.Lock()
	mgr.sessions["sess-mp"] = &Session{ID: "sess-mp"}
	mgr.mu.Unlock()

	raw, _ := json.Marshal(protocol.RequestScrollback{Type: "request_scrollback", SessionID: "sess-mp"})
	mgr.handleRequestScrollback(raw)

	select {
	case msg := <-sender.ch:
		sb, ok := msg.(protocol.SessionScrollback)
		if !ok {
			t.Fatalf("expected SessionScrollback, got %T", msg)
		}
		got, err := base64.StdEncoding.DecodeString(sb.ModePrefix)
		if err != nil {
			t.Fatalf("ModePrefix not base64: %v", err)
		}
		if string(got) != wantSeq {
			t.Errorf("ModePrefix decoded = %q, want %q", got, wantSeq)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a SessionScrollback, got none")
	}
}

// TestRequestScrollbackModeSyncNotSentAsOutput verifies the mode-sync preamble
// never goes out as a session_output message — it rides only on the
// SessionScrollback.ModePrefix field. session_output is persisted to the event
// log and Seq-numbered by the PTY stream goroutine; a mode-sync frame sent that
// way would corrupt the log and the sequence numbering.
func TestRequestScrollbackModeSyncNotSentAsOutput(t *testing.T) {
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})
	mgr.scrollbackFunc = func(string, int) (string, error) { return "old text", nil }
	mgr.modeSyncFunc = func(string) string { return "\x1b[?1h\x1b[?25h" }
	mgr.mu.Lock()
	mgr.sessions["sess-no"] = &Session{ID: "sess-no"}
	mgr.mu.Unlock()

	raw, _ := json.Marshal(protocol.RequestScrollback{Type: "request_scrollback", SessionID: "sess-no"})
	mgr.handleRequestScrollback(raw)

	select {
	case msg := <-sender.ch:
		if _, ok := msg.(protocol.SessionScrollback); !ok {
			t.Fatalf("expected SessionScrollback, got %T", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a SessionScrollback, got none")
	}
	// Nothing else should follow — in particular no session_output.
	select {
	case msg := <-sender.ch:
		t.Errorf("expected exactly one message, got extra %T", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

// recordingSender captures all Send calls for inspection.
type recordingSender struct {
	mu   sync.Mutex
	msgs []any
	ch   chan any
}

func newRecordingSender(buf int) *recordingSender {
	return &recordingSender{ch: make(chan any, buf)}
}

func (r *recordingSender) Send(msg any) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
	select {
	case r.ch <- msg:
	default:
	}
	return nil
}

// messages returns every captured Send call, JSON-marshaled, for substring
// assertions in tests.
func (r *recordingSender) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.msgs))
	for _, m := range r.msgs {
		b, _ := json.Marshal(m)
		out = append(out, string(b))
	}
	return out
}

func TestManagerSpawnAndOutput(t *testing.T) {
	sender := newRecordingSender(64)
	reposRoot := t.TempDir()
	// Create the repo directory so the session can chdir into it.
	if err := os.MkdirAll(filepath.Join(reposRoot, "myrepo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := ManagerConfig{
		ReposRoot:  reposRoot,
		DaemonName: "test-daemon",
		Command:    []string{"echo", "hello"},
	}
	mgr := newManagerWithSender(sender, cfg)

	// Manually invoke handler with a spawn_session message.
	spawnMsg := protocol.SpawnSession{
		Type:      "spawn_session",
		SessionID: "sess-1",
		Repo:      "myrepo",
		Title:     "Test Session",
		Cols:      80,
		Rows:      24,
	}
	raw, _ := json.Marshal(spawnMsg)
	mgr.handleSpawnSession(raw)

	// Collect all messages until session_ended arrives (or timeout).
	// Process all types in a single loop so nothing gets dropped by a type-filter.
	var (
		sawStarted   bool
		outputChunks []string
		ended        *protocol.SessionEnded
		deadline     = time.After(5 * time.Second)
	)
collecting:
	for {
		select {
		case m := <-sender.ch:
			switch v := m.(type) {
			case protocol.SessionStarted:
				if v.SessionID != "sess-1" {
					t.Errorf("unexpected session ID: %s", v.SessionID)
				}
				sawStarted = true
			case protocol.SessionOutput:
				decoded, err := base64.StdEncoding.DecodeString(v.Data)
				if err != nil {
					t.Errorf("base64 decode error: %v", err)
					continue
				}
				outputChunks = append(outputChunks, string(decoded))
			case protocol.SessionEnded:
				ended = &v
				break collecting
			}
		case <-deadline:
			t.Fatal("timed out waiting for session_ended")
		}
	}

	if !sawStarted {
		t.Error("never received session_started")
	}
	if ended.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", ended.ExitCode)
	}

	// Verify "hello" appeared in output.
	combined := strings.Join(outputChunks, "")
	if !strings.Contains(combined, "hello") {
		t.Errorf("expected output to contain 'hello', got: %q", combined)
	}
}

// slicesEqual reports whether two string slices have equal contents.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestHandleSendInputStripsCtrlZ verifies Ctrl+Z (0x1a) is dropped before
// reaching the PTY. claude runs directly as the tmux pane's process with no
// shell in between, so there's no job control to "fg" it back from a
// SIGTSTP — forwarding the byte can wedge the Ink TUI's raw-mode input
// handling, so it must never reach the process.
func TestHandleSendInputStripsCtrlZ(t *testing.T) {
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})

	sess, r, w := mkPipeSession("sess-ctrlz")
	defer r.Close()
	mgr.mu.Lock()
	mgr.sessions["sess-ctrlz"] = sess
	mgr.mu.Unlock()

	payload := append([]byte("ab"), 0x1a)
	payload = append(payload, "cd"...)
	raw, _ := json.Marshal(protocol.SendInput{
		Type:      "send_input",
		SessionID: "sess-ctrlz",
		Data:      base64.StdEncoding.EncodeToString(payload),
	})
	mgr.handleSendInput(raw)

	// A second call carrying only Ctrl+Z must write nothing at all.
	rawOnlyCtrlZ, _ := json.Marshal(protocol.SendInput{
		Type:      "send_input",
		SessionID: "sess-ctrlz",
		Data:      base64.StdEncoding.EncodeToString([]byte{0x1a}),
	})
	mgr.handleSendInput(rawOnlyCtrlZ)

	w.Close()
	buf, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(buf) != "abcd" {
		t.Errorf("written = %q, want %q", buf, "abcd")
	}
}

// TestQueueInitialPromptQueuesForIdleFlush verifies the initial prompt is
// queued into the same pending-injection slot inject_note_reply uses, rather
// than written immediately — startStatePoller's flushPendingNoteReply is what
// actually submits it, on the first observed idle screen. This replaces a
// blind sleep-then-write that could fire before Claude's raw-mode input was
// ready, landing the text with no Enter.
func TestQueueInitialPromptQueuesForIdleFlush(t *testing.T) {
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})

	mgr.queueInitialPrompt("sess-init", "do the thing")

	mgr.mu.Lock()
	got, ok := mgr.noteReplies["sess-init"]
	mgr.mu.Unlock()
	if !ok || got != "do the thing" {
		t.Errorf("noteReplies[sess-init] = %q, %v; want %q, true", got, ok, "do the thing")
	}
}

// TestQueueInitialPromptEmptyIsNoop verifies an empty prompt (the common case:
// most sessions have none) doesn't create a spurious queue entry.
func TestQueueInitialPromptEmptyIsNoop(t *testing.T) {
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})

	mgr.queueInitialPrompt("sess-empty", "")

	mgr.mu.Lock()
	_, ok := mgr.noteReplies["sess-empty"]
	mgr.mu.Unlock()
	if ok {
		t.Error("expected no queue entry for an empty initial prompt")
	}
}

// TestClaudeCommand verifies the spawn and resume forms of claudeCommand.
func TestClaudeCommand(t *testing.T) {
	id := "sess-abc"
	model := "m"

	// Spawn form: uses --session-id.
	spawnArgs := claudeCommand(id, model, "", true, false, false)
	wantSpawn := []string{"--model", model, "--dangerously-skip-permissions", "--session-id", id}
	if got := spawnArgs[1:]; !slicesEqual(got, wantSpawn) {
		t.Errorf("spawn args[1:] = %v, want %v", got, wantSpawn)
	}

	// Resume form: uses --resume.
	resumeArgs := claudeCommand(id, model, "", true, true, false)
	wantResume := []string{"--model", model, "--dangerously-skip-permissions", "--resume", id}
	if got := resumeArgs[1:]; !slicesEqual(got, wantResume) {
		t.Errorf("resume args[1:] = %v, want %v", got, wantResume)
	}
}

// A tmux Claude session gets --model <id> --effort <level>, sandboxed or not,
// fresh or resumed.
func TestClaudeCommandModelAndEffort(t *testing.T) {
	for _, sandboxed := range []bool{false, true} {
		got := claudeCommand("s1", "claude-opus-5-5", "xhigh", false, false, sandboxed)
		want := []string{"--model", "claude-opus-5-5", "--effort", "xhigh", "--session-id", "s1"}
		if !slicesEqual(got[1:], want) {
			t.Errorf("sandboxed=%v args[1:] = %v, want %v", sandboxed, got[1:], want)
		}
	}
	got := claudeCommand("s1", "sonnet[1m]", "low", false, true, true)
	want := []string{"--model", "sonnet[1m]", "--effort", "low", "--resume", "s1"}
	if !slicesEqual(got[1:], want) {
		t.Errorf("resume args[1:] = %v, want %v", got[1:], want)
	}
}

// Hostile model/effort values never reach the command line: each is dropped
// whole (not split, not quoted) by modelEffortArgs.
func TestClaudeCommandDropsHostileModelAndEffort(t *testing.T) {
	for _, tc := range []struct{ model, effort string }{
		{"sonnet --dangerously-skip-permissions", "high"},
		{"--model", "max"},
		{"$(touch /tmp/pwned)", "low"},
		{"opus;id", "high; rm -rf /"},
		{"sonnet", "--effort max"},
		{"sonnet", "ultra"},
		{"sonnet\nid", "HIGH"},
	} {
		got := claudeCommand("s1", tc.model, tc.effort, false, false, true)
		for _, arg := range got {
			// The valid half of a pair may pass; the hostile half never.
			if (arg == tc.model && !models.ValidModelFor("", tc.model)) ||
				(arg == tc.effort && !models.ValidEffortFor("", tc.effort)) {
				t.Errorf("model %q effort %q: hostile value reached argv %v", tc.model, tc.effort, got)
			}
		}
		if slices.Contains(got, "--dangerously-skip-permissions") {
			t.Errorf("model %q smuggled a flag: %v", tc.model, got)
		}
	}
}

// The daemon refuses a spawn whose model or effort is invalid, before any
// workspace work, rather than trusting the server's 422.
func TestSpawnModelProblem(t *testing.T) {
	ok := []protocol.SpawnSession{
		{},
		{Model: "sonnet", Effort: "max"},
		{Model: "claude-haiku-4-5-20251001"},
		{Engine: "hermes", Model: "openrouter/anthropic/claude-sonnet-4"},
		{Engine: "hermes", Model: "qwen3-30b", Effort: "minimal"},
		{Engine: "codex", Model: "gpt-5.6-terra", Effort: "ultra"},
	}
	for _, m := range ok {
		if p := spawnModelProblem(m); p != "" {
			t.Errorf("%+v refused: %s", m, p)
		}
	}
	bad := []protocol.SpawnSession{
		{Model: "sonnet --x"},
		{Model: "-p"},
		{Effort: "ultra"},
		{Engine: "codex", Model: "gpt 5"},
		{Engine: "codex", Effort: "$(id)"},
		{Engine: "codex", Effort: "auto"},
		{Engine: "hermes", Effort: "auto"},
		// Engines that registered no effort levels take none.
		{Engine: "openclaw", Effort: "high"},
	}
	for _, m := range bad {
		if p := spawnModelProblem(m); p == "" {
			t.Errorf("%+v accepted", m)
		}
	}
}

// TestClaudeCommandSandboxed verifies that a sandboxed session's command uses
// the bare "claude" name rather than an absolute host path from LookPath — that
// host path (e.g. /home/<user>/.local/bin/claude) doesn't exist inside a
// Docker sandbox, and using it there kills the pane (and its tmux server)
// instantly. See sandbox.go / createDockerSandbox.
func TestClaudeCommandSandboxed(t *testing.T) {
	got := claudeCommand("sess-abc", "", "", true, false, true)
	if got[0] != "claude" {
		t.Errorf("claudeCommand(sandboxed=true)[0] = %q, want bare %q (not an absolute host path)", got[0], "claude")
	}
}

// Codex/Hermes/OpenClaw's terminal-command flag mapping is now covered by
// engines_test.go's TestBuildTerminalCommand (one generic builder driven by
// EngineSpec data instead of a per-engine xCommand function).

// TestKillSessionDeletesRecord verifies that handleKillSession deletes the
// recovery record for the killed session.
func TestKillSessionDeletesRecord(t *testing.T) {
	sender := newRecordingSender(8)
	reposRoot := t.TempDir()
	cfg := ManagerConfig{
		ReposRoot:  reposRoot,
		DaemonName: "test-daemon",
		// Non-empty Command skips tmux and killTmuxSession, safe for unit tests.
		Command: []string{"sleep", "60"},
	}
	mgr := newManagerWithSender(sender, cfg)

	// Write a recovery record for the session we are about to kill.
	rec := SessionRecord{
		Version:     recordSchemaVersion,
		SessionID:   "sess-k",
		Repo:        "testrepo",
		ProjectPath: filepath.Join(reposRoot, "testrepo"),
		CreatedAt:   "2026-06-12T00:00:00Z",
		UpdatedAt:   "2026-06-12T00:00:00Z",
	}
	if err := writeSessionRecord(reposRoot, rec); err != nil {
		t.Fatalf("writeSessionRecord: %v", err)
	}

	// Inject a fake session directly (same package access). cmd.Process is nil so
	// sess.Kill() is a safe no-op. OutputCh/done are present only to satisfy the
	// struct — streamSession is never started here, so nothing drains or closes them.
	fakeSess := &Session{
		ID:       "sess-k",
		OutputCh: make(chan []byte),
		done:     make(chan struct{}),
		cmd:      &exec.Cmd{},
	}
	mgr.mu.Lock()
	mgr.sessions["sess-k"] = fakeSess
	mgr.mu.Unlock()

	// Kill the session.
	killMsg := protocol.KillSession{Type: "kill_session", SessionID: "sess-k"}
	raw, _ := json.Marshal(killMsg)
	mgr.handleKillSession(raw)

	// The record file must be gone.
	p := recordPath(reposRoot, "sess-k")
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected record to be deleted after kill; os.Stat err = %v", err)
	}
}

// TestKillSessionUnknownDeletesRecord verifies that a kill for a session the
// daemon is not currently managing (e.g. arriving during the post-restart
// reconnect window, before ReattachSessions re-registers it) still deletes the
// recovery record — otherwise it would resurrect the killed session on the next
// reconnect.
func TestKillSessionUnknownDeletesRecord(t *testing.T) {
	sender := newRecordingSender(8)
	reposRoot := t.TempDir()
	cfg := ManagerConfig{
		ReposRoot:  reposRoot,
		DaemonName: "test-daemon",
		// Non-empty Command skips tmux and killTmuxSession, safe for unit tests.
		Command: []string{"sleep", "60"},
	}
	mgr := newManagerWithSender(sender, cfg)

	rec := SessionRecord{
		Version:     recordSchemaVersion,
		SessionID:   "sess-orphan",
		Repo:        "testrepo",
		ProjectPath: filepath.Join(reposRoot, "testrepo"),
		CreatedAt:   "2026-06-12T00:00:00Z",
		UpdatedAt:   "2026-06-12T00:00:00Z",
	}
	if err := writeSessionRecord(reposRoot, rec); err != nil {
		t.Fatalf("writeSessionRecord: %v", err)
	}

	// No session is registered in mgr.sessions — simulate the reconnect window.
	killMsg := protocol.KillSession{Type: "kill_session", SessionID: "sess-orphan"}
	raw, _ := json.Marshal(killMsg)
	mgr.handleKillSession(raw)

	p := recordPath(reposRoot, "sess-orphan")
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected record to be deleted after kill of unmanaged session; os.Stat err = %v", err)
	}
}

// TestSessionEndDeletesRecord verifies that streamSession's cleanup deletes the
// recovery record when a session ends naturally.
func TestSessionEndDeletesRecord(t *testing.T) {
	sender := newRecordingSender(64)
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "myrepo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := ManagerConfig{
		ReposRoot:  reposRoot,
		DaemonName: "test-daemon",
		Command:    []string{"echo", "done"},
	}
	mgr := newManagerWithSender(sender, cfg)

	// Pre-write a recovery record. (handleSpawnSession with Command set does not
	// write one itself; we simulate a record that was written on the original spawn.)
	rec := SessionRecord{
		Version:     recordSchemaVersion,
		SessionID:   "sess-e",
		Repo:        "myrepo",
		ProjectPath: filepath.Join(reposRoot, "myrepo"),
		CreatedAt:   "2026-06-12T00:00:00Z",
		UpdatedAt:   "2026-06-12T00:00:00Z",
	}
	if err := writeSessionRecord(reposRoot, rec); err != nil {
		t.Fatalf("writeSessionRecord: %v", err)
	}

	// Spawn the session; it will exit quickly (echo).
	spawnMsg := protocol.SpawnSession{
		Type:      "spawn_session",
		SessionID: "sess-e",
		Repo:      "myrepo",
		Cols:      80,
		Rows:      24,
	}
	raw, _ := json.Marshal(spawnMsg)
	mgr.handleSpawnSession(raw)

	// Wait for session_ended; by then deleteSessionRecord has already been called.
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case m := <-sender.ch:
			if _, ok := m.(protocol.SessionEnded); ok {
				break loop
			}
		case <-deadline:
			t.Fatal("timed out waiting for session_ended")
		}
	}

	// The record file must be gone.
	p := recordPath(reposRoot, "sess-e")
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected record to be deleted after session end; os.Stat err = %v", err)
	}
}

// clearBlergRunnerVars unsets the given env keys for the duration of the test,
// restoring them (or leaving them unset) via t.Cleanup. This ensures
// "omitted when config empty" assertions hold regardless of host env.
func clearBlergRunnerVars(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		k := k
		if v, had := os.LookupEnv(k); had {
			t.Setenv(k, v) // registers cleanup; then immediately unset below
			os.Unsetenv(k)
		} else {
			t.Cleanup(func() { os.Unsetenv(k) })
		}
	}
}

// TestSessionEnv verifies that sessionEnv returns os.Environ plus the expected
// Blerg Runner vars, including the per-session BLERG_RUNNER_SESSION_ID and the optional
// BLERG_RUNNER_SERVER_HTTP (omitted when the config value is empty).
func TestSessionEnv(t *testing.T) {
	// Scrub host BLERG_RUNNER_* so assertions on omitted vars are hermetic.
	clearBlergRunnerVars(t,
		"BLERG_RUNNER_PREVIEW_URL", "BLERG_RUNNER_DAEMON_TOKEN", "BLERG_RUNNER_SERVER_HTTP",
		"BLERG_RUNNER_SESSION_ID", "BLERG_RUNNER_BOARD_ID", "BLERG_RUNNER_BOARD_TOKEN",
	)

	t.Run("all config vars set", func(t *testing.T) {
		sender := newRecordingSender(4)
		mgr := newManagerWithSender(sender, ManagerConfig{
			PreviewURL:  "http://example.com/preview",
			DaemonToken: "tok-abc",
			ServerHTTP:  "http://runner.example.test",
		})
		env := mgr.sessionEnv("sess-x", false, "", "", "", nil)

		// BLERG_RUNNER_DAEMON_TOKEN is deliberately NOT in this map: the master
		// token never reaches a session env (TestSessionEnvNeverCarriesDaemonToken).
		want := map[string]string{
			"BLERG_RUNNER_SESSION_ID":  "sess-x",
			"BLERG_RUNNER_SERVER_HTTP": "http://runner.example.test",
			"BLERG_RUNNER_PREVIEW_URL": "http://example.com/preview",
		}
		for k, v := range want {
			needle := k + "=" + v
			found := false
			for _, e := range env {
				if e == needle {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("sessionEnv missing %q; env = %v", needle, env)
			}
		}
	})

	t.Run("ServerHTTP empty is omitted", func(t *testing.T) {
		sender := newRecordingSender(4)
		mgr := newManagerWithSender(sender, ManagerConfig{
			DaemonToken: "tok-abc",
			ServerHTTP:  "",
		})
		env := mgr.sessionEnv("sess-y", false, "", "", "", nil)

		for _, e := range env {
			if strings.HasPrefix(e, "BLERG_RUNNER_SERVER_HTTP=") {
				t.Errorf("BLERG_RUNNER_SERVER_HTTP must be omitted when ServerHTTP is empty; got %q", e)
			}
		}

		// SESSION_ID must still be present.
		found := false
		for _, e := range env {
			if e == "BLERG_RUNNER_SESSION_ID=sess-y" {
				found = true
				break
			}
		}
		if !found {
			t.Error("BLERG_RUNNER_SESSION_ID=sess-y missing from env")
		}
	})
}

// TestSessionEnvAssist verifies Assist-session env injection:
//   - board vars are present when assist=true
//   - BLERG_RUNNER_DAEMON_TOKEN is absent even when an ambient value is in os.Environ
//   - a non-assist env does NOT carry BLERG_RUNNER_DAEMON_TOKEN either (it
//     used to; the per-session token replaced it)
func TestSessionEnvAssist(t *testing.T) {
	// Simulate the daemon process having BLERG_RUNNER_DAEMON_TOKEN in its own env.
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "ambient-daemon-tok")

	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{
		DaemonToken: "config-daemon-tok",
		ServerHTTP:  "http://runner.example.test",
	})

	// --- Assist env ---
	assistEnv := mgr.sessionEnv("sess-assist", true, "board-123", "btok-xyz", "", nil)

	// Board vars must be present.
	for _, needle := range []string{"BLERG_RUNNER_BOARD_ID=board-123", "BLERG_RUNNER_BOARD_TOKEN=btok-xyz"} {
		found := false
		for _, e := range assistEnv {
			if e == needle {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("assist env missing %q; env = %v", needle, assistEnv)
		}
	}

	// BLERG_RUNNER_DAEMON_TOKEN must NOT appear at all (strip from os.Environ AND skip config append).
	for _, e := range assistEnv {
		if strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") {
			t.Errorf("assist env must not contain BLERG_RUNNER_DAEMON_TOKEN; got %q", e)
		}
	}

	// SESSION_ID must still be injected.
	found := false
	for _, e := range assistEnv {
		if e == "BLERG_RUNNER_SESSION_ID=sess-assist" {
			found = true
			break
		}
	}
	if !found {
		t.Error("BLERG_RUNNER_SESSION_ID=sess-assist missing from assist env")
	}

	// --- Non-assist env must not carry the daemon token either ---
	normalEnv := mgr.sessionEnv("sess-normal", false, "", "", "", nil)
	for _, e := range normalEnv {
		if strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") {
			t.Errorf("non-assist env must not contain BLERG_RUNNER_DAEMON_TOKEN; got %q", e)
		}
	}
}

// TestSessionEnvNeverCarriesDaemonToken is the desktop-safety C1 guarantee:
// whatever kind of session is spawned (assist or not, with or without extra
// env), the master daemon token — which is full runner admin — is never in
// the child's environment, neither from config nor inherited from the
// daemon's own os.Environ. The per-session token and caller-supplied extra
// env are what the session gets instead.
func TestSessionEnvNeverCarriesDaemonToken(t *testing.T) {
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "ambient-daemon-tok")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-inherited")
	mgr := newManagerWithSender(newRecordingSender(2), ManagerConfig{DaemonToken: "config-daemon-tok", ServerHTTP: "http://srv"})
	for _, assist := range []bool{false, true} {
		env := mgr.sessionEnv("s1", assist, "", "", "sess-tok-123", map[string]string{"BLERG_BOARD_URL": "http://localhost:8082"})
		for _, e := range env {
			if strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") {
				t.Fatalf("assist=%v: master token leaked: %s", assist, e)
			}
		}
		if !hasEntry(env, "BLERG_RUNNER_SESSION_TOKEN=sess-tok-123") || !hasEntry(env, "BLERG_BOARD_URL=http://localhost:8082") {
			t.Fatalf("assist=%v: session token / extra env missing: %v", assist, env)
		}
		// The engine's own credential is inherited: the engine cannot run
		// without it (ruling R1).
		if !hasEntry(env, "ANTHROPIC_API_KEY=sk-ant-inherited") {
			t.Fatalf("assist=%v: engine credential not inherited: %v", assist, env)
		}
		if !hasEntry(env, "BLERG_RUNNER_SESSION_ID=s1") {
			t.Fatalf("assist=%v: session id missing: %v", assist, env)
		}
	}
	// No token minted (older recovery record, or DB-less server): no
	// BLERG_RUNNER_SESSION_TOKEN entry at all rather than an empty one.
	for _, e := range mgr.sessionEnv("s2", false, "", "", "", nil) {
		if strings.HasPrefix(e, "BLERG_RUNNER_SESSION_TOKEN=") {
			t.Fatalf("empty session token must not be injected: %s", e)
		}
	}

	// sanitizedEnviron is the base every session process (tmux, sandbox, and
	// every agent-kind driver) starts from.
	s := sanitizedEnviron("X=1")
	if !hasEntry(s, "X=1") {
		t.Fatalf("sanitizedEnviron dropped extra: %v", s)
	}
	if !hasEntry(s, "ANTHROPIC_API_KEY=sk-ant-inherited") {
		t.Fatalf("sanitizedEnviron dropped the engine credential: %v", s)
	}
	for _, e := range s {
		if strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") {
			t.Fatalf("sanitizedEnviron leaked the master token: %s", e)
		}
	}

	// extraEnvPairs is what the agent-kind host feeds sanitizedEnviron:
	// deterministic order, token first, no entry for an empty token.
	pairs := extraEnvPairs("tok", map[string]string{"B": "2", "A": "1"})
	if len(pairs) != 3 || pairs[0] != "BLERG_RUNNER_SESSION_TOKEN=tok" || pairs[1] != "A=1" || pairs[2] != "B=2" {
		t.Fatalf("extraEnvPairs = %v", pairs)
	}
	if got := extraEnvPairs("", nil); len(got) != 0 {
		t.Fatalf("extraEnvPairs(\"\", nil) = %v, want empty", got)
	}

	// ExtraEnv is caller-supplied: it can't smuggle the master token back in,
	// override the engine credential or PATH-style daemon-owned vars, or
	// inject malformed keys. Only the clean key survives.
	hostile := map[string]string{
		"BLERG_RUNNER_DAEMON_TOKEN":  "smuggled",
		"BLERG_RUNNER_SESSION_TOKEN": "forged",
		"BLERG_RUNNER_ANYTHING":      "daemon-owned",
		"ANTHROPIC_API_KEY":          "stolen",
		"CLAUDE_CODE_OAUTH_TOKEN":    "stolen",
		"OPENAI_API_KEY":             "stolen",
		"":                           "empty-key",
		"HAS=EQUALS":                 "x",
		"HAS\x00NUL":                 "x",
		"HAS\nNEWLINE":               "x",
		"BLERG_BOARD_URL":            "http://board",
	}
	got := extraEnvPairs("real-tok", hostile)
	if len(got) != 2 || got[0] != "BLERG_RUNNER_SESSION_TOKEN=real-tok" || got[1] != "BLERG_BOARD_URL=http://board" {
		t.Fatalf("extraEnvPairs(hostile) = %q", got)
	}
	full := mgr.sessionEnv("s3", false, "", "", "real-tok", hostile)
	for _, e := range full {
		if strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") || e == "ANTHROPIC_API_KEY=stolen" || e == "BLERG_RUNNER_SESSION_TOKEN=forged" {
			t.Fatalf("hostile extra env reached the session: %s", e)
		}
	}
	if !hasEntry(full, "ANTHROPIC_API_KEY=sk-ant-inherited") {
		t.Fatalf("engine credential clobbered: %v", full)
	}
}

func hasEntry(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// TestMergeSessionIDsThreeGroups verifies that mergeSessionIDs deduplicates
// across three overlapping slices.
func TestMergeSessionIDsThreeGroups(t *testing.T) {
	a := []string{"x", "y"}
	b := []string{"y", "z"}
	c := []string{"z", "w", "x"}

	got := mergeSessionIDs(a, b, c)
	sort.Strings(got)
	want := []string{"w", "x", "y", "z"}
	if !slicesEqual(got, want) {
		t.Errorf("mergeSessionIDs = %v, want %v", got, want)
	}
}

// TestInjectBytes verifies the pure byte-builder: CR/LF stripped from text, exactly
// one \r appended. No PTY or tmux needed.
func TestInjectBytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "hello", "hello\r"},
		{"strips LF", "hello\nworld", "helloworld\r"},
		{"strips CR", "hello\rworld", "helloworld\r"},
		{"strips CRLF", "hello\r\nworld", "helloworld\r"},
		{"trailing CR stripped then re-added", "text\r", "text\r"},
		{"empty string", "", "\r"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(injectBytes(c.in))
			if got != c.want {
				t.Errorf("injectBytes(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// mkPipeSession returns a Session whose ptmx is the write end of an os.Pipe, and the
// read end so tests can inspect what was written. Callers must close w after the
// function under test to unblock ReadAll on r.
func mkPipeSession(id string) (sess *Session, r, w *os.File) {
	var err error
	r, w, err = os.Pipe()
	if err != nil {
		panic("os.Pipe: " + err.Error())
	}
	return &Session{ID: id, ptmx: w}, r, w
}

// TestHandleInjectNoteReply_Idle verifies that when the captured screen is "idle",
// the reply is written to the PTY immediately and the slot is left empty.
func TestHandleInjectNoteReply_Idle(t *testing.T) {
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})

	sess, r, w := mkPipeSession("sess-inj-idle")
	defer r.Close()
	mgr.mu.Lock()
	mgr.sessions["sess-inj-idle"] = sess
	mgr.mu.Unlock()

	mgr.capturePaneFunc = func(string) (string, error) { return idleScreen, nil }

	raw, _ := json.Marshal(protocol.InjectNoteReply{
		Type:      "inject_note_reply",
		SessionID: "sess-inj-idle",
		Text:      `[Re: "think about X"] yes do it`,
	})
	mgr.handleInjectNoteReply(raw)
	w.Close()

	buf, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	want := `[Re: "think about X"] yes do it` + "\r"
	if string(buf) != want {
		t.Errorf("injected = %q, want %q", buf, want)
	}

	mgr.mu.Lock()
	_, pending := mgr.noteReplies["sess-inj-idle"]
	mgr.mu.Unlock()
	if pending {
		t.Error("slot should be empty after idle immediate inject")
	}
}

// TestHandleInjectNoteReply_Waiting verifies that when the screen is "waiting"
// (a confirmation dialog), the reply is queued in the slot, not written to the PTY.
func TestHandleInjectNoteReply_Waiting(t *testing.T) {
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})

	sess, r, w := mkPipeSession("sess-inj-wait")
	defer r.Close()
	mgr.mu.Lock()
	mgr.sessions["sess-inj-wait"] = sess
	mgr.mu.Unlock()

	// classifyScreen with [y/n] in the last lines → "waiting"
	mgr.capturePaneFunc = func(string) (string, error) {
		return "Do you want to proceed? [y/n]", nil
	}

	raw, _ := json.Marshal(protocol.InjectNoteReply{
		Type:      "inject_note_reply",
		SessionID: "sess-inj-wait",
		Text:      "yes",
	})
	mgr.handleInjectNoteReply(raw)
	w.Close()

	buf, _ := io.ReadAll(r)
	if len(buf) != 0 {
		t.Errorf("expected no bytes written for waiting session, got %q", buf)
	}

	mgr.mu.Lock()
	text, pending := mgr.noteReplies["sess-inj-wait"]
	mgr.mu.Unlock()
	if !pending {
		t.Error("slot should be populated for waiting session")
	}
	if text != "yes" {
		t.Errorf("slot text = %q, want %q", text, "yes")
	}
}

// TestHandleInjectNoteReply_Running verifies that a "running" session also queues.
func TestHandleInjectNoteReply_Running(t *testing.T) {
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})

	sess, r, w := mkPipeSession("sess-inj-run")
	defer r.Close()
	mgr.mu.Lock()
	mgr.sessions["sess-inj-run"] = sess
	mgr.mu.Unlock()

	// "esc to interrupt" in last non-empty line → "running"
	mgr.capturePaneFunc = func(string) (string, error) {
		return "esc to interrupt", nil
	}

	raw, _ := json.Marshal(protocol.InjectNoteReply{
		Type:      "inject_note_reply",
		SessionID: "sess-inj-run",
		Text:      "queued",
	})
	mgr.handleInjectNoteReply(raw)
	w.Close()

	buf, _ := io.ReadAll(r)
	if len(buf) != 0 {
		t.Errorf("expected no bytes written for running session, got %q", buf)
	}

	mgr.mu.Lock()
	text, pending := mgr.noteReplies["sess-inj-run"]
	mgr.mu.Unlock()
	if !pending {
		t.Error("slot should be populated for running session")
	}
	if text != "queued" {
		t.Errorf("slot text = %q, want %q", text, "queued")
	}
}

// TestHandleInjectNoteReply_LatestWins verifies that a second reply overwrites a
// pending first reply (latest-wins, single slot).
func TestHandleInjectNoteReply_LatestWins(t *testing.T) {
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})

	sess, r, w := mkPipeSession("sess-latest")
	defer r.Close()
	defer w.Close()
	mgr.mu.Lock()
	mgr.sessions["sess-latest"] = sess
	mgr.mu.Unlock()

	// Running → always queues.
	mgr.capturePaneFunc = func(string) (string, error) { return "esc to interrupt", nil }

	for _, text := range []string{"first reply", "second reply"} {
		raw, _ := json.Marshal(protocol.InjectNoteReply{
			Type:      "inject_note_reply",
			SessionID: "sess-latest",
			Text:      text,
		})
		mgr.handleInjectNoteReply(raw)
	}

	mgr.mu.Lock()
	text, ok := mgr.noteReplies["sess-latest"]
	mgr.mu.Unlock()
	if !ok {
		t.Fatal("expected pending slot")
	}
	if text != "second reply" {
		t.Errorf("slot = %q, want %q (latest-wins)", text, "second reply")
	}
}

// TestFlushPendingNoteReply_WritesAndClearsSlot verifies that flushPendingNoteReply
// writes the queued text + \r to the session and clears the slot (clear-on-consume).
func TestFlushPendingNoteReply_WritesAndClearsSlot(t *testing.T) {
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})

	sess, r, w := mkPipeSession("sess-flush")
	defer r.Close()
	mgr.mu.Lock()
	mgr.sessions["sess-flush"] = sess
	mgr.noteReplies["sess-flush"] = `[Re: "my note"] answer`
	mgr.mu.Unlock()

	mgr.flushPendingNoteReply("sess-flush")
	w.Close()

	buf, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	want := `[Re: "my note"] answer` + "\r"
	if string(buf) != want {
		t.Errorf("flushed bytes = %q, want %q", buf, want)
	}

	// Slot must be cleared (clear-on-consume prevents re-injection on next tick).
	mgr.mu.Lock()
	_, pending := mgr.noteReplies["sess-flush"]
	mgr.mu.Unlock()
	if pending {
		t.Error("slot should be cleared after flush")
	}
}

// TestFlushPendingNoteReply_NoopWhenEmpty verifies no write happens when slot is empty.
func TestFlushPendingNoteReply_NoopWhenEmpty(t *testing.T) {
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})

	sess, r, w := mkPipeSession("sess-flush-noop")
	defer r.Close()
	defer w.Close()
	mgr.mu.Lock()
	mgr.sessions["sess-flush-noop"] = sess
	mgr.mu.Unlock()

	// Slot is empty — flush should be a no-op.
	mgr.flushPendingNoteReply("sess-flush-noop")

	// Non-blocking read: nothing should be in the pipe.
	w.Close()
	buf, _ := io.ReadAll(r)
	if len(buf) != 0 {
		t.Errorf("expected no write on empty slot, got %q", buf)
	}
}

// TestHandleInjectNoteReply_IdleClearsPendingSlot is the regression test for the
// double-inject bug: reply A queued while running, then reply B arrives while idle
// and is injected immediately — the pending slot must be cleared so a subsequent
// flushPendingNoteReply is a no-op and only reply B's bytes reach the PTY.
//
// Against the unfixed code this test FAILS because flushPendingNoteReply also writes
// reply A, producing two chunks in the pipe instead of one.
func TestHandleInjectNoteReply_IdleClearsPendingSlot(t *testing.T) {
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{Command: []string{"unused"}})

	sess, r, w := mkPipeSession("sess-double")
	defer r.Close()
	mgr.mu.Lock()
	mgr.sessions["sess-double"] = sess
	mgr.mu.Unlock()

	// Phase 1: running screen → reply A queues, nothing written.
	mgr.capturePaneFunc = func(string) (string, error) { return "esc to interrupt", nil }
	rawA, _ := json.Marshal(protocol.InjectNoteReply{
		Type:      "inject_note_reply",
		SessionID: "sess-double",
		Text:      "reply A (should not inject)",
	})
	mgr.handleInjectNoteReply(rawA)

	mgr.mu.Lock()
	slotText, slotOK := mgr.noteReplies["sess-double"]
	mgr.mu.Unlock()
	if !slotOK || slotText != "reply A (should not inject)" {
		t.Fatalf("precondition: expected reply A in slot, got ok=%v text=%q", slotOK, slotText)
	}

	// Phase 2: idle screen → reply B injects immediately and must clear the slot.
	mgr.capturePaneFunc = func(string) (string, error) { return idleScreen, nil }
	rawB, _ := json.Marshal(protocol.InjectNoteReply{
		Type:      "inject_note_reply",
		SessionID: "sess-double",
		Text:      "reply B (should inject)",
	})
	mgr.handleInjectNoteReply(rawB)

	// Slot must be empty — the stale A must have been evicted.
	mgr.mu.Lock()
	_, stillPending := mgr.noteReplies["sess-double"]
	mgr.mu.Unlock()
	if stillPending {
		t.Error("pending slot should be cleared after idle immediate inject (double-inject guard)")
	}

	// A subsequent flush must be a no-op (slot already gone).
	mgr.flushPendingNoteReply("sess-double")
	w.Close()

	buf, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	// Only reply B should have reached the PTY.
	wantB := "reply B (should inject)\r"
	if string(buf) != wantB {
		t.Errorf("injected bytes = %q\nwant only      = %q\n(extra bytes indicate double-inject bug)", buf, wantB)
	}
}

func TestSpawnRejectsRepoOutsideReposRoot(t *testing.T) {
	root := t.TempDir()
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root, Command: []string{"unused"}})
	for _, repo := range []string{"../..", "..", "x/../../y", "/tmp"} {
		raw, _ := json.Marshal(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-" + repo, Repo: repo})
		mgr.handleSpawnSession(raw)
	}
	msgs := sender.messages()
	if len(msgs) != 4 {
		t.Fatalf("want 4 error messages, got %d: %v", len(msgs), msgs)
	}
	for _, m := range msgs {
		if !strings.Contains(m, `"status":"error"`) || !strings.Contains(m, "invalid folder path") {
			t.Errorf("unexpected message %s", m)
		}
	}
}

func TestResolveProjectPath(t *testing.T) {
	cases := map[string]bool{"app": true, "app/../app": true, "../app": false, "..": false, "/abs": false, "a/b": true}
	for repo, ok := range cases {
		_, err := resolveProjectPath("/home/dev/repos", repo)
		if (err == nil) != ok {
			t.Errorf("resolveProjectPath(%q) err=%v, want ok=%v", repo, err, ok)
		}
	}
}

// TestResolveProjectPathSymlinkEscape verifies that a symlink inside
// reposRoot pointing outside it is rejected even though the Clean-based
// containment check on the un-resolved path passes.
func TestResolveProjectPathSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := resolveProjectPath(root, "escape"); err == nil {
		t.Error("resolveProjectPath(escape) should reject a symlink pointing outside reposRoot")
	}

	// A real subdirectory must still resolve.
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := resolveProjectPath(root, "real"); err != nil {
		t.Errorf("resolveProjectPath(real) should succeed, got %v", err)
	}

	// A non-existent repo (new_repo path) keeps the Clean-based check only.
	if _, err := resolveProjectPath(root, "new_repo"); err != nil {
		t.Errorf("resolveProjectPath(new_repo) should succeed for a not-yet-created repo, got %v", err)
	}
}

// TestSpawnReportsMissingEngine verifies that when the engine binary is not on
// the daemon service's PATH, the spawn fails fast with a named, actionable
// reason (trial blocker 2) instead of a bare tmux/docker failure — and nothing
// gets spawned.
func TestSpawnReportsMissingEngine(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing on PATH
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root}) // no Command override: real engine resolution
	raw, _ := json.Marshal(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-noeng", Repo: "app"})
	mgr.handleSpawnSession(raw)
	msgs := sender.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], `"status":"error"`) || !strings.Contains(msgs[0], "claude not found on PATH") {
		t.Fatalf("want one error naming the missing binary, got %v", msgs)
	}
}

// TestSpawnReportsMissingSandboxImage verifies that a sandboxed spawn fails
// fast when the sandbox image isn't present, via the sandboxImagePresent seam.
func TestSpawnReportsMissingSandboxImage(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root})
	mgr.sandboxImagePresent = func() bool { return false }
	raw, _ := json.Marshal(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-noimg", Repo: "app", Sandbox: true})
	mgr.handleSpawnSession(raw)
	msgs := sender.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "sandbox image "+sandboxImage()+" missing") {
		t.Fatalf("want one error naming the missing image, got %v", msgs)
	}
}
