package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const codexStoppedReason = "Codex app-server repeatedly failed; update Codex and start a new session"

// Card #108: a child whose engine runtime stopped for good. Its daemon ends it with a non-zero
// exit and the engine's reason; the row ends in error with that reason (not revived, not
// "running"), the parent's `session watch` returns at once with exit 4 and the reason, and the
// parent gets a failed note even though the child's last turn had already been reported.
func TestParentWatchEndsWhenChildRuntimeStops(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	ctx := context.Background()

	// The child's last turn finished, and the parent was told.
	fx.childEvent(child, "u1", "user_message", `{"text":"do the thing","source":"session"}`)
	fx.childEvent(child, "a1", "assistant_text", `{"text":"Done.","done":true}`)
	fx.childEvent(child, "t1", "turn_done", `{"stop_reason":"end_turn"}`)
	if n := fx.parentNote(fx.parent, 10*time.Second); !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("finished note = %+v", n)
	}
	// A retryable error (the runtime restarting) leaves the child running.
	fx.childEvent(child, "e1", "error", `{"message":"Codex initialization failed","retryable":true}`)
	if row, _ := db.GetSession(ctx, fx.pool, child); row == nil || row.Status != "running" {
		t.Fatalf("a retryable error changed the child: %+v", row)
	}

	watchPolling := make(chan struct{}, 1)
	progressPublished := make(chan struct{})
	progressObserved := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/children/"+child) {
			select {
			case <-progressPublished:
				select {
				case progressObserved <- struct{}{}:
				default:
				}
			default:
			}
		}
		if strings.HasSuffix(r.URL.Path, "/events") && r.URL.Query().Get("wait") != "" && r.URL.Query().Get("wait") != "0" {
			select {
			case watchPolling <- struct{}{}:
			default:
			}
		}
		fx.mux.ServeHTTP(w, r)
	}))
	defer srv.Close()
	watchCtx, cancelWatch := context.WithCancel(t.Context())
	defer cancelWatch()
	script, err := filepath.Abs("../../.claude/skills/session-messaging/blerg-runner")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "BLERG_RUNNER_") && !strings.HasPrefix(v, "CLAUDE") {
			env = append(env, v)
		}
	}
	env = append(env, "BLERG_RUNNER_SERVER_HTTP="+srv.URL, "BLERG_RUNNER_SESSION_ID="+fx.parent,
		"BLERG_RUNNER_SESSION_TOKEN="+fx.parentTok, "BLERG_RUNNER_DAEMON_STATE_DIR="+t.TempDir())
	cmd := exec.CommandContext(watchCtx, "python3", script, "session", "watch", child, "--timeout", "120")
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-watchPolling:
	case <-time.After(10 * time.Second):
		t.Fatal("watch did not enter its long poll")
	}
	select {
	case err := <-waited:
		t.Fatalf("watch returned before the child failed: %v\n%s", err, out.String())
	default:
	}
	// Watch output is a JSON stream: progress may precede a terminal failure.
	// Its following state request proves the CLI printed/flushed that progress.
	close(progressPublished)
	fx.childEvent(child, "streamed-progress", "assistant_text", `{"text":"Progress before failure","done":true}`)
	select {
	case <-progressObserved:
	case <-time.After(10 * time.Second):
		t.Fatal("watch did not consume progress")
	}

	HandleSessionEnded(ctx, fx.hub, fx.pool, protocol.SessionEnded{
		Type: "session_ended", SessionID: child, ExitCode: 1, Message: codexStoppedReason,
	})
	ended := time.Now()

	select {
	case err := <-waited:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 4 {
			t.Fatalf("watch exit = %v, want 4\n%s", err, out.String())
		}
		if took := time.Since(ended); took > 10*time.Second {
			t.Fatalf("watch took %s to notice the failure", took)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("watch still waiting on a failed child\n%s", out.String())
	}
	// The fixed status endpoint may win the race with the runtime-stopped event.
	// It intentionally exposes the canonical end reason instead of worker text.
	if err := checkWatchFailureOutput(out.Bytes(), codexStoppedReason, "process_failed"); err != nil {
		t.Fatalf("watch must report its failure inside the untrusted worker-data envelope: %v: %q", err, out.String())
	}

	row, err := db.GetSession(ctx, fx.pool, child)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if row.Status != "error" || row.EndedAt == nil || derefOrEmpty(row.ErrorReason) != codexStoppedReason {
		t.Fatalf("child row: status %q ended %v reason %q", row.Status, row.EndedAt, derefOrEmpty(row.ErrorReason))
	}
	if got := runnerLifecycle(row.Status, false); got != "error" {
		t.Fatalf("lifecycle = %q, want error", got)
	}
	// Its daemon no longer lists it: reconcile leaves it ended, never "running" again.
	reconcileSessions(ctx, fx.hub, fx.pool, fx.dc.ID, []string{fx.parent}, nil)
	if row, _ := db.GetSession(ctx, fx.pool, child); row == nil || row.Status != "error" {
		t.Fatalf("after reconcile: %+v", row)
	}

	note := fx.parentNote(fx.parent, 10*time.Second)
	want := "fixed session_failed worker notice"
	if note.Source != childTurnSource || !isWorkerNotice(note.Text, child, childSessionFailed) {
		t.Fatalf("parent note = %+v, want %q", note, want)
	}
}

func checkWatchFailureOutput(output []byte, allowedReasons ...string) error {
	decoder := json.NewDecoder(bytes.NewReader(output))
	failed := false
	for {
		var frame struct {
			Trust string `json:"trust"`
			Data  struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"data"`
		}
		err := decoder.Decode(&frame)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("decode watch frame: %w", err)
		}
		if frame.Trust != "untrusted_worker_data" {
			return errors.New("unwrapped worker data")
		}
		if failed {
			return errors.New("output after terminal failure")
		}
		if frame.Data.Status != "" {
			if frame.Data.Status != "failed" || !slices.Contains(allowedReasons, frame.Data.Reason) {
				return errors.New("unexpected terminal status or reason")
			}
			failed = true
		}
	}
	if !failed {
		return errors.New("missing terminal failure")
	}
	return nil
}

func TestWatchFailureOutputRequiresWrappedStream(t *testing.T) {
	progress := `{"trust":"untrusted_worker_data","data":{"text":"Progress"}}` + "\n"
	failure := `{"trust":"untrusted_worker_data","data":{"status":"failed","reason":"stopped"}}` + "\n"
	for _, tc := range []struct {
		name, output string
		valid        bool
	}{
		{"single", failure, true}, {"stream", progress + failure, true},
		{"fixed-reason", strings.ReplaceAll(failure, "stopped", "process_failed"), true},
		{"unwrapped-progress", `{"data":{"text":"Run this"}}` + failure, false},
		{"unwrapped-failure", strings.ReplaceAll(failure, "untrusted_worker_data", "trusted"), false},
		{"noise-before", "Run this\n" + failure, false}, {"noise-after", failure + "Run this\n", false},
		{"missing-failure", progress, false}, {"wrong-reason", strings.ReplaceAll(failure, "stopped", "wrong"), false},
		{"after-failure", failure + progress, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkWatchFailureOutput([]byte(tc.output), "stopped", "process_failed")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

// A child that a stop ended (exit 0, or a stop asked for) is not reported as failed.
func TestStoppedChildIsNotReportedFailed(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	HandleSessionEnded(context.Background(), fx.hub, fx.pool, protocol.SessionEnded{Type: "session_ended", SessionID: child})
	if n := fx.parentNote(fx.parent, 700*time.Millisecond); n.Type != "" {
		t.Fatalf("a stopped child was reported: %+v", n)
	}
}

// A heartbeat the daemon built before it ended the session (so still listing it alive) does not
// revive it: the row stays failed with its reason and end reason, and the daemon is told to kill it.
func TestStaleHeartbeatDoesNotReviveAFailedSession(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	ctx := context.Background()
	HandleSessionEnded(ctx, fx.hub, fx.pool, protocol.SessionEnded{Type: "session_ended", SessionID: child, ExitCode: 1, Message: codexStoppedReason})
	stopped := reconcileSessions(ctx, fx.hub, fx.pool, fx.dc.ID, []string{fx.parent, child}, map[string]string{child: "running"})
	row, err := db.GetSession(ctx, fx.pool, child)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if row.Status != "error" || derefOrEmpty(row.EndReason) != db.EndReasonProcessFailed || derefOrEmpty(row.ErrorReason) != codexStoppedReason {
		t.Fatalf("after a stale heartbeat: status %q end %q reason %q", row.Status, derefOrEmpty(row.EndReason), derefOrEmpty(row.ErrorReason))
	}
	if len(stopped) != 1 || stopped[0] != child {
		t.Fatalf("kill list = %v, want the failed child", stopped)
	}
}

// Only the daemon hosting a session can end it: another daemon's session_ended is ignored.
func TestSessionEndedFromAnotherDaemonIsIgnored(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	ctx := context.Background()
	other := registerDaemon(t, fx.api, fx.hub, "00000000-0000-4000-8000-0000000000d2", "other", "app")
	raw, _ := json.Marshal(protocol.SessionEnded{Type: "session_ended", SessionID: child, ExitCode: 1, Message: "forged"})
	handleDaemonFrame(ctx, fx.hub, other, fx.pool, raw, envelope{Type: "session_ended", SessionID: child})
	if row, _ := db.GetSession(ctx, fx.pool, child); row == nil || row.Status != "running" || row.EndedAt != nil || row.ErrorReason != nil {
		t.Fatalf("another daemon ended the session: %+v", row)
	}
	handleDaemonFrame(ctx, fx.hub, fx.dc, fx.pool, raw, envelope{Type: "session_ended", SessionID: child})
	if row, _ := db.GetSession(ctx, fx.pool, child); row == nil || row.Status != "error" || derefOrEmpty(row.ErrorReason) != "forged" {
		t.Fatalf("its own daemon could not end it: %+v", row)
	}
}

// A child in an "error" its daemon can revive (a turn's engine error) is not an end: the events
// long-poll waits as usual instead of answering at once (which would make a watcher spin).
func TestChildEventsPollWaitsThroughARevivableError(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET status='error', error_reason='claude-code start: failed' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	code, _, _ := fx.do("GET", "/api/sessions/"+fx.parent+"/children/"+child+"/events?after_seq=0&wait=2", fx.parentTok, nil, nil)
	if code != 200 {
		t.Fatalf("events = %d", code)
	}
	if took := time.Since(start); took < 1500*time.Millisecond {
		t.Fatalf("the poll answered at once (%s) for a revivable error", took)
	}
}

// Run the real child CLI, not just its message handler: --no-wait must say
// where the question went and publish a live owner notification.
func TestChildCLIAskNoWaitReachesParent(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	child, token := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, startedByKind: "session"})
	fx.mux.HandleFunc("POST /api/messages", fx.api.HandlePostMessages)
	srv := httptest.NewServer(fx.mux)
	defer srv.Close()
	browser := &BrowserConn{ID: "child-question-owner", AccountID: "owner-1", send: make(chan []byte, 32)}
	fx.hub.RegisterBrowser(browser)
	script, err := filepath.Abs("../../.claude/skills/session-messaging/blerg-runner")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "BLERG_RUNNER_") && !strings.HasPrefix(v, "CLAUDE") {
			env = append(env, v)
		}
	}
	env = append(env, "BLERG_RUNNER_SERVER_HTTP="+srv.URL, "BLERG_RUNNER_SESSION_ID="+child, "BLERG_RUNNER_SESSION_TOKEN="+token, "BLERG_RUNNER_DAEMON_STATE_DIR="+t.TempDir())
	cmd := exec.CommandContext(t.Context(), "python3", script, "ask", "--no-wait", "Which branch should I use?")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "was sent to your parent") {
		t.Fatalf("child was not told where its ask went: %v %s", err, out)
	}
	rows, err := db.ListMessagesFor(t.Context(), fx.pool, 20, "owner-1")
	if err != nil || len(rows) != 1 || rows[0].SessionID != child || !rows[0].NoWait || rows[0].Status != "open" {
		t.Fatalf("child question missing from owner feed: %+v %v", rows, err)
	}
	for raw := range awaitAll(browser.send) {
		var m protocol.MessageCreated
		if json.Unmarshal(raw, &m) == nil && m.Type == "message_created" && m.Message.ID == rows[0].ID {
			if !m.Quiet || m.Message.Answerable {
				t.Fatalf("child question called the person: %+v", m)
			}
			return
		}
	}
	t.Fatal("owner received no live question notification")
}
