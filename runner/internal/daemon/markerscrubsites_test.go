package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// messagesServer records every POST /api/messages body and answers each ask at once.
func messagesServer(t *testing.T) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(raw))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"m1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"answered":true,"answer":"1"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), bodies...) }
}

// Every place the daemon posts or reports text of the model's own takes the session's code out:
// the native loop's update, note and ask; a Codex or Hermes question (both use questionAsker);
// a session's fatal error.
func TestMarkerCodeIsScrubbedAtEveryPostingSite(t *testing.T) {
	leak := "the code is " + testMarkerNonce + " and " + strings.ToUpper(testMarkerNonce)

	t.Run("native messenger", func(t *testing.T) {
		srv, bodies := messagesServer(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m := &httpMessenger{base: srv.URL, token: "fixture-token", sessionID: "s1", markerNonce: testMarkerNonce, client: srv.Client(), ctx: ctx, onAnswer: func(string) {}}
		if err := m.SendUpdate(leak); err != nil {
			t.Fatal(err)
		}
		if err := m.SendNote(leak); err != nil {
			t.Fatal(err)
		}
		if _, err := m.SendAsk(leak); err != nil {
			t.Fatal(err)
		}
		got := bodies()
		if len(got) != 3 {
			t.Fatalf("posted %d bodies", len(got))
		}
		for _, b := range got {
			assertNoCode(t, b)
		}
	})

	t.Run("questions", func(t *testing.T) {
		srv, bodies := messagesServer(t)
		host := &AgentHost{cfg: AgentHostConfig{ServerHTTP: srv.URL, DaemonToken: "fixture-token"}}
		em := &collectEmitter{}
		for _, ask := range []func(context.Context, string) (string, error){
			host.questionAsker("s1", em, testMarkerNonce, func() string { return "turn-1" }), // Codex
			host.questionAsker("s1", em, testMarkerNonce, nil),                               // Hermes
		} {
			if _, err := ask(t.Context(), leak); err != nil {
				t.Fatal(err)
			}
		}
		got := bodies()
		if len(got) != 2 {
			t.Fatalf("posted %d questions", len(got))
		}
		for _, b := range got {
			assertNoCode(t, b)
		}
		for _, ev := range em.snapshot() {
			raw, _ := json.Marshal(ev.Payload)
			assertNoCode(t, string(raw))
		}
	})

	t.Run("session error", func(t *testing.T) {
		engineDir := fakeClaude(t)
		t.Setenv("PATH", engineDir+":"+os.Getenv("PATH"))
		t.Setenv("FAKE_CLAUDE_LOG", engineDir+"/calls.log")
		reposRoot := t.TempDir()
		if err := os.MkdirAll(reposRoot+"/proj", 0o755); err != nil {
			t.Fatal(err)
		}
		sender := &agentTestSender{}
		host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
		host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-err", Repo: "proj", Kind: "agent", InitialPrompt: "hello"})
		sender.waitForKind(t, "turn_done")
		defer host.Kill("s-err")
		host.mu.Lock()
		sess := host.sessions["s-err"]
		host.mu.Unlock()
		code := sessionMarkerNonce(host.markerKeyDirFor(), "s-err")
		sess.emitter.Emit(agent.Event{ClientEventID: "fatal", Kind: "error", Payload: agent.ErrorPayload{Message: "engine said " + code, Retryable: false}})
		found := false
		sender.mu.Lock()
		msgs := append([]any(nil), sender.msgs...)
		sender.mu.Unlock()
		for _, m := range msgs {
			if st, ok := m.(protocol.SessionStateChanged); ok && st.Status == "error" && st.Message != nil {
				found = true
				if strings.Contains(*st.Message, code) || !strings.Contains(*st.Message, "‹code›") {
					t.Fatalf("session error = %q", *st.Message)
				}
			}
		}
		if !found {
			t.Fatal("no session error was reported")
		}
	})
}
