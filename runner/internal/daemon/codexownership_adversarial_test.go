package daemon

import (
	"context"
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"io"
	"os"
	"testing"
	"time"
)

func TestCodexChildStartedNotificationCannotGrantForeignThreadOwnership(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	// The notification claims root ancestry. Authoritative native metadata says
	// this thread belongs to a sibling session, so no display or private asks may
	// be admitted through the notification's claim alone.
	t.Setenv("FAKE_CODEX_CHILD_QUESTION", "owned")
	t.Setenv("FAKE_CODEX_CHILD_READ", `{"owned-child":{"id":"owned-child","parentThreadId":"foreign-root"},"foreign-root":{"id":"foreign-root","source":"cli"}}`)
	emitter := &collectEmitter{}
	driver := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: emitter})
	called := make(chan struct{}, 1)
	driver.AskUser = func(context.Context, string) (string, error) { called <- struct{}{}; return "Blue", nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); driver.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("driver did not stop")
		}
	}()
	driver.Enqueue("Exercise native ownership proof", "chat")
	waitUntil(t, "foreign request settled", func() bool { return countKind(emitter, "turn_done") == 1 })
	select {
	case <-called:
		t.Error("foreign notification self-granted private question access")
	default:
	}
	if driver.childKnown("owned-child") {
		t.Error("foreign notification self-granted durable ownership")
	}
	if countKind(emitter, "subagent_started") != 0 || countKind(emitter, "background_task_started") != 0 {
		t.Error("unproven thread emitted lifecycle events")
	}
}

type codexTurnCompletionBarrier struct {
	collectEmitter
	reached chan struct{}
	release chan struct{}
}

func (b *codexTurnCompletionBarrier) Emit(event agent.Event) {
	if event.Kind == "turn_done" {
		close(b.reached)
		<-b.release
	}
	b.collectEmitter.Emit(event)
}
func TestCodexCompletingTurnCannotOpenQuestionBeforeDisplayUnblocks(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	emitter := &codexTurnCompletionBarrier{reached: make(chan struct{}), release: make(chan struct{})}
	driver := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: emitter})
	called := false
	driver.AskUser = func(context.Context, string) (string, error) { called = true; return "fixture-answer", nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); driver.Run(ctx) }()
	defer func() {
		close(emitter.release)
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("driver did not stop")
		}
	}()
	driver.Enqueue("Exercise completion registration boundary", "chat")
	select {
	case <-emitter.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not reach display boundary")
	}
	driver.mu.Lock()
	app := driver.app
	driver.mu.Unlock()
	id := json.RawMessage(`"deferred-question"`)
	if !app.markQuestionPending("cx-thread-1", id) {
		t.Fatal("could not register pending question")
	}
	raw := json.RawMessage(`{"threadId":"cx-thread-1","turnId":"turn-1","questions":[{"id":"choice","question":"Stale question"}],"isBlocking":false}`)
	app.userQuestions(ctx, codexRPC{ID: id, Params: raw})
	if called {
		t.Fatal("a turn completing behind a blocked display opened a stale question")
	}
}

func TestCodexBlockedNativeWriteHonorsRPCDeadlineWithoutHoldingLifecycleMutex(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	driver := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	driver.threadID = "root"
	app := &codexApp{d: driver, ctx: appCtx, in: writer, pending: map[int]chan codexRPC{}, done: make(chan struct{}), turn: "root-turn"}
	ctx, cancel := context.WithTimeout(appCtx, 40*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := app.callContext(ctx, "thread/read", map[string]string{"threadId": "child"})
		finished <- err
	}()
	time.Sleep(10 * time.Millisecond)
	unlocked := make(chan struct{})
	go func() { app.mu.Lock(); _ = app.turn; app.mu.Unlock(); close(unlocked) }()
	select {
	case <-unlocked:
	case <-time.After(100 * time.Millisecond):
		t.Error("blocked native write held the lifecycle mutex")
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Error("blocked native RPC unexpectedly succeeded")
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("RPC context deadline did not bound a blocked native stdin write")
		_ = reader.Close()
		<-finished
	}
}

func TestCodexDuplicateRPCRepliesCannotBlockNativeReader(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	app := &codexApp{ctx: context.Background(), pending: map[int]chan codexRPC{7: make(chan codexRPC, 1)}, events: make(chan struct{}, 1), done: make(chan struct{})}
	written := make(chan struct{})
	go func() {
		defer close(written)
		_, _ = io.WriteString(writer, "{\"id\":7,\"result\":{}}\n{\"id\":7,\"result\":{}}\n{\"id\":7,\"result\":{}}\n")
		_ = writer.Close()
	}()
	go app.read(reader)
	select {
	case <-app.done:
	case <-time.After(200 * time.Millisecond):
		t.Error("duplicate response blocked the native reader after its reply slot filled")
		// Release an old reader so a red regression does not leak goroutines.
		for i := 0; i < 3; i++ {
			select {
			case <-app.pending[7]:
			case <-app.done:
			}
		}
	}
	<-written
}

func TestCodexResumeCannotReplaceRootIdentityAndInheritOldChildAllowlist(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	emitter := &collectEmitter{}
	driver := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: emitter})
	driver.threadID = "saved-root"
	driver.seedKnownChildren([]OpenSubagent{{AgentID: "saved-child"}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); driver.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("driver did not stop")
		}
	}()
	// The fixture returns cx-thread-1 regardless of the resume request. The
	// driver must reject that mismatch before changing its trusted root identity.
	waitUntil(t, "native resume result", func() bool { return countKind(emitter, "model_changed") > 0 || countKind(emitter, "error") > 0 })
	driver.mu.Lock()
	root := driver.threadID
	driver.mu.Unlock()
	if root != "saved-root" {
		t.Fatal("mismatched resume silently moved the root while retaining old child ownership")
	}
}
