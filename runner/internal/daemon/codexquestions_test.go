package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCodexNativeUserQuestionUsesCallback(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_QUESTION", "1")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	bodies := make(chan string, 1)
	d.AskUser = func(_ context.Context, body string) (string, error) { bodies <- body; return "Blue", nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("ask me", "chat")
	waitUntil(t, "question turn completes", func() bool { return countKind(em, "turn_done") == 1 })
	select {
	case body := <-bodies:
		if !strings.Contains(body, "Choose a color") || !strings.Contains(body, "Blue — Cool tone") {
			t.Fatal("question options were lost")
		}
	default:
		t.Fatal("native question did not reach callback")
	}
}
func TestCodexSecretQuestionNeverReachesChat(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_QUESTION", "1")
	t.Setenv("FAKE_CODEX_SECRET", "1")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	called := make(chan struct{}, 1)
	d.AskUser = func(context.Context, string) (string, error) { called <- struct{}{}; return "", nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("ask", "chat")
	waitUntil(t, "secret request refused", func() bool { return countKind(em, "turn_done") == 1 })
	select {
	case <-called:
		t.Fatal("secret question reached ordinary chat")
	default:
	}
}
func TestCodexInterruptCancelsNativeQuestion(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_QUESTION", "1")
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: &collectEmitter{}})
	started := make(chan struct{})
	stopped := make(chan struct{})
	d.AskUser = func(ctx context.Context, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return "", errors.New("canceled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("ask", "chat")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("question did not start")
	}
	d.Interrupt()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt left the question waiting")
	}
}

func TestCodexConcurrentQuestionsRemainWaiting(t *testing.T) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	d.threadID = "thread"
	started := make(chan string, 2)
	release := map[string]chan struct{}{"first": make(chan struct{}), "second": make(chan struct{})}
	d.AskUser = func(ctx context.Context, body string) (string, error) {
		started <- body
		select {
		case <-release[body]:
			return "answer", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	a := &codexApp{d: d, ctx: context.Background(), in: nopCodexWriter{}, turn: "turn"}
	done := make(chan struct{}, 2)
	for _, name := range []string{"first", "second"} {
		p, _ := json.Marshal(map[string]any{"threadId": "thread", "turnId": "turn", "questions": []any{map[string]any{"id": name, "question": name}}})
		go func(name string, p []byte) {
			a.userQuestions(context.Background(), codexRPC{ID: json.RawMessage(`"` + name + `"`), Params: p})
			done <- struct{}{}
		}(name, p)
	}
	<-started
	<-started
	close(release["first"])
	<-done
	a.mu.Lock()
	if a.blockingQuestions["turn"] != 1 {
		t.Error("lost blocking question count")
	}
	for _, ev := range a.backlog {
		if ev.questionStatus != nil && ev.questionStatus.status == "running" {
			t.Error("running queued while second question blocked")
		}
	}
	a.mu.Unlock()
	em.mu.Lock()
	for _, ev := range em.events {
		if ev.Kind == "status_changed" {
			b, _ := json.Marshal(ev.Payload)
			if strings.Contains(string(b), `"running"`) {
				t.Error("first answer cleared another blocking question")
			}
		}
	}
	em.mu.Unlock()
	close(release["second"])
	<-done
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.blockingQuestions["turn"] != 0 || len(a.backlog) != 3 || a.backlog[2].questionStatus.status != "running" {
		t.Error("last answer did not queue running transition")
	}
}

type nopCodexWriter struct{}

func (nopCodexWriter) Write(p []byte) (int, error) { return len(p), nil }
func (nopCodexWriter) Close() error                { return nil }
