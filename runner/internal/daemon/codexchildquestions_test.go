package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func TestCodexOwnedChildQuestionUsesPrivateCallback(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_CHILD_QUESTION", "owned")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	bodies := make(chan string, 1)
	d.AskUser = func(_ context.Context, body string) (string, error) { bodies <- body; return "Blue", nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("Run child question", "chat")
	waitUntil(t, "question resolved", func() bool { return countKind(em, "turn_done") == 1 })
	select {
	case body := <-bodies:
		if !strings.Contains(body, "Codex subagent") || !strings.Contains(body, "Child question") {
			t.Fatalf("missing safe child attribution: %q", body)
		}
	default:
		t.Fatal("owned child question was refused")
	}
}

func TestCodexForeignAndStaleChildQuestionsAreRefused(t *testing.T) {
	for _, mode := range []string{"foreign", "foreign-item", "stale"} {
		t.Run(mode, func(t *testing.T) {
			bin := fakeCodex(t)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_CHILD_QUESTION", mode)
			em := &collectEmitter{}
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
			called := make(chan bool, 1)
			d.AskUser = func(context.Context, string) (string, error) { called <- true; return "Blue", nil }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopDriver := startTestCodexDriver(ctx, t, d)
			defer stopDriver()
			d.Enqueue("Run child question", "chat")
			waitUntil(t, "rejected child question", func() bool { return countKind(em, "turn_done") == 1 })
			select {
			case <-called:
				t.Fatal("foreign/stale child asked a person")
			default:
			}
		})
	}
}

func TestCodexRootInterruptCancelsChildQuestion(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_CHILD_QUESTION", "owned")
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: &collectEmitter{}})
	started := make(chan bool, 1)
	stopped := make(chan bool, 1)
	d.AskUser = func(ctx context.Context, _ string) (string, error) {
		started <- true
		<-ctx.Done()
		stopped <- true
		return "", errors.New("canceled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("Run child question", "chat")
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("owned child question did not start")
	}
	d.Interrupt()
	select {
	case <-stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("root interrupt left a child question open")
	}
}

func TestCodexChildQuestionCancellationIsThreadScoped(t *testing.T) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Emitter: em})
	d.threadID = "root"
	d.childStatus("child-one", "fixture-model", "One", "running")
	d.childStatus("child-two", "fixture-model", "Two", "running")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &codexApp{d: d, ctx: ctx, in: nopCodexWriter{}, turn: "root-turn", childTurns: map[string]string{"child-one": "same-turn", "child-two": "same-turn"}}
	started := make(chan string, 2)
	ended := make(chan string, 2)
	release := make(chan struct{})
	d.AskUser = func(ctx context.Context, body string) (string, error) {
		if _, ok := ctx.Value(codexQuestionOriginKey{}).(string); !ok {
			t.Error("child ask lost private routing identity")
		}
		label := "one"
		if strings.Contains(body, "Question two") {
			label = "two"
		}
		started <- label
		select {
		case <-ctx.Done():
			ended <- label
			return "", ctx.Err()
		case <-release:
			return "answer", nil
		}
	}
	done := make(chan bool, 2)
	for _, entry := range []struct{ id, thread, question string }{{"one", "child-one", "Question one"}, {"two", "child-two", "Question two"}} {
		raw, _ := json.Marshal(map[string]any{"threadId": entry.thread, "turnId": "same-turn", "questions": []any{map[string]string{"id": entry.id, "question": entry.question}}})
		go func(id string, raw []byte) {
			a.userQuestions(ctx, codexRPC{ID: json.RawMessage(`"` + id + `"`), Params: raw})
			done <- true
		}(entry.id, raw)
	}
	<-started
	<-started
	a.cancelDynamic("child-one", "same-turn")
	select {
	case label := <-ended:
		if label != "one" {
			t.Fatal("one child canceled another child's question")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("matching child question not canceled")
	}
	<-done
	select {
	case <-ended:
		t.Fatal("sibling question canceled by equal turn ID")
	default:
	}
	a.mu.Lock()
	remaining := a.blockingQuestionCountLocked()
	a.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("remaining blocking asks = %d", remaining)
	}
	close(release)
	<-done
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.blockingQuestionCountLocked() != 0 {
		t.Fatal("answer left a blocking child ask")
	}
}

func TestCodexNativeChildQuestionProofPreservesTurnOrder(t *testing.T) {
	for _, entry := range []struct {
		name, metadata string
		allowed        bool
	}{
		{"native-source", `{"owned-child":{"id":"owned-child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"cx-thread-1"}}}}}`, true},
		{"same-session-only", `{"owned-child":{"id":"owned-child","sessionId":"cx-thread-1","forkedFromId":"cx-thread-1","source":"cli"}}`, false},
		{"conflicting-parent", `{"owned-child":{"id":"owned-child","parentThreadId":"foreign-root","source":{"subagent":{"thread_spawn":{"parent_thread_id":"cx-thread-1"}}}}}`, false},
		{"wrong-response-identity", `{"owned-child":{"id":"different-child","parentThreadId":"cx-thread-1"}}`, false},
		{"ancestry-cycle", `{"owned-child":{"id":"owned-child","parentThreadId":"cycle-parent"},"cycle-parent":{"id":"cycle-parent","parentThreadId":"owned-child"}}`, false},
	} {
		t.Run(entry.name, func(t *testing.T) {
			bin := fakeCodex(t)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_CHILD_QUESTION", "native-source")
			t.Setenv("FAKE_CODEX_CHILD_READ", entry.metadata)
			em := &collectEmitter{}
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
			called := make(chan bool, 1)
			d.AskUser = func(context.Context, string) (string, error) { called <- true; return "Blue", nil }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopDriver := startTestCodexDriver(ctx, t, d)
			defer stopDriver()
			d.Enqueue("Run native child question", "chat")
			waitUntil(t, "native child question settled", func() bool { return countKind(em, "turn_done") == 1 })
			got := false
			select {
			case <-called:
				got = true
			default:
			}
			if got != entry.allowed {
				t.Fatalf("private callback allowed=%v, want %v", got, entry.allowed)
			}
			if d.childKnown("owned-child") != entry.allowed {
				t.Fatal("unproven native candidate changed ownership")
			}
		})
	}
}

func TestCodexQuestionFeatureOnlyEnabledForUnrestrictedRuntime(t *testing.T) {
	d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	if !strings.Contains(strings.Join(d.launchArgs(), " "), "features.default_mode_request_user_input=true") {
		t.Fatal("native question tool not enabled")
	}
	d.RestrictedRuntime = &codexRestrictedRuntime{Restricted: true}
	if strings.Contains(strings.Join(d.launchArgs(), " "), "default_mode_request_user_input=true") {
		t.Fatal("restricted runtime enabled native interactive tool")
	}
}

func TestCodexOptionalQuestionDoesNotBlockEventLoop(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_QUESTION", "1")
	t.Setenv("FAKE_CODEX_NONBLOCKING_QUESTION", "1")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	started := make(chan bool, 1)
	stopped := make(chan bool, 1)
	d.AskUser = func(ctx context.Context, _ string) (string, error) {
		started <- true
		<-ctx.Done()
		stopped <- true
		return "", ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("Ask optional question", "chat")
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("optional question did not start")
	}
	d.Enqueue("Continue independent work", "chat")
	waitUntil(t, "independent work progressed", func() bool { return countKind(em, "turn_done") == 1 })
	if countKind(em, "user_message") != 2 || countKind(em, "assistant_text") == 0 {
		t.Fatal("optional question blocked unrelated message/events")
	}
	select {
	case <-stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("native turn completion left optional question open")
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, event := range em.events {
		if p, ok := event.Payload.(agent.StatusPayload); ok && p.Status == "waiting" {
			t.Fatal("optional native question marked session waiting")
		}
	}
}

func TestCodexResolvedQuestionCannotOpenLatePrivateCard(t *testing.T) {
	d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	d.threadID = "root"
	called := false
	d.AskUser = func(context.Context, string) (string, error) { called = true; return "answer", nil }
	a := &codexApp{d: d, ctx: context.Background(), in: nopCodexWriter{}, turn: "root-turn"}
	id := json.RawMessage(`"question"`)
	if !a.markQuestionPending("root", id) {
		t.Fatal("question registration failed")
	}
	a.resolveQuestionRequest("root", id)
	raw := json.RawMessage(`{"threadId":"root","turnId":"root-turn","questions":[{"id":"choice","question":"Pick a color"}],"isBlocking":false}`)
	a.userQuestions(context.Background(), codexRPC{ID: id, Params: raw})
	if called || len(a.pendingQuestions) != 0 {
		t.Fatal("native-resolved question opened a stale card or leaked registration")
	}
}

func TestCodexNativeResolutionCannotCancelOtherThreadQuestion(t *testing.T) {
	d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	d.threadID = "root"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &codexApp{d: d, ctx: ctx, in: nopCodexWriter{}, turn: "root-turn"}
	started := make(chan bool, 1)
	stopped := make(chan bool, 1)
	d.AskUser = func(ctx context.Context, _ string) (string, error) {
		started <- true
		<-ctx.Done()
		stopped <- true
		return "", ctx.Err()
	}
	id := json.RawMessage(`"question"`)
	a.markQuestionPending("root", id)
	raw := json.RawMessage(`{"threadId":"root","turnId":"root-turn","questions":[{"id":"choice","question":"Pick a color"}]}`)
	done := make(chan bool, 1)
	go func() { a.userQuestions(ctx, codexRPC{ID: id, Params: raw}); done <- true }()
	<-started
	a.resolveQuestionRequest("foreign-thread", id)
	select {
	case <-stopped:
		t.Fatal("foreign native resolution canceled owned question")
	default:
	}
	a.resolveQuestionRequest("root", id)
	select {
	case <-stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("matching native resolution left question open")
	}
	<-done
}
