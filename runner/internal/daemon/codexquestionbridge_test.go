package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func TestCodexQuestionLeavesAnswerRecordingToServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			_, _ = w.Write([]byte(`{"id":"question-fixture"}`))
			return
		}
		_, _ = w.Write([]byte(`{"answered":true,"answer":"fixture choice"}`))
	}))
	defer server.Close()
	em := &collectEmitter{}
	answer, err := codexQuestionHandler(server.URL, "fixture-token", nil, "session", em)(context.Background(), "Pick a fixture")
	if err != nil || answer != "fixture choice" {
		t.Fatal("question did not receive its answer")
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	asks, answers := 0, 0
	for _, event := range em.events {
		if event.Kind == "ask" {
			asks++
		}
		if event.Kind == "user_message" {
			answers++
			p := event.Payload.(agent.UserMessagePayload)
			if p.MessageID != "question-fixture" || p.Source != "ask_answer" {
				t.Fatal("answer lost its question identity")
			}
		}
	}
	if asks != 1 || answers != 0 {
		t.Fatal("question or answer was duplicated")
	}
}

func TestCodexQuestionErrorsAreSafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fixture-secret-never-display", 500) }))
	defer server.Close()
	_, err := codexQuestionHandler(server.URL, "fixture-token", nil, "session", &collectEmitter{})(context.Background(), "Question")
	if err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("unsafe question API error")
	}
}

func TestCodexQuestionStopsWhenItsMessageExpires(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_, _ = w.Write([]byte(`{"id":"expired-question"}`))
			return
		}
		w.WriteHeader(http.StatusGone)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := codexQuestionHandler(server.URL, "fixture-token", nil, "session", &collectEmitter{})(ctx, "Question")
	if err == nil || ctx.Err() != nil {
		t.Fatal("an expired ask waited until the turn was interrupted")
	}
}

func TestCodexChildQuestionKeepsIndependentPrivateRoute(t *testing.T) {
	posted := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			var p struct {
				TurnID string `json:"turn_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Error(err)
			}
			posted <- p.TurnID
			_, _ = w.Write([]byte(`{"id":"child-question"}`))
			return
		}
		_, _ = w.Write([]byte(`{"answered":true,"answer":"Blue"}`))
	}))
	defer server.Close()
	ctx := context.WithValue(context.Background(), codexQuestionOriginKey{}, "canonical-child-run")
	answer, err := codexQuestionHandler(server.URL, "fixture-token", nil, "session", &collectEmitter{}, func() string { return "root-turn" })(ctx, "Codex subagent asks: Choose a color")
	if err != nil || answer != "Blue" {
		t.Fatal("child private question did not resolve")
	}
	if route := <-posted; route != "canonical-child-run" {
		t.Fatalf("child question bound to unrelated root turn: %q", route)
	}
}

// An owner-only approval that could not be posted says why: the runner-key explanation only on
// the server's 409, a delivery failure otherwise (a server error, an unreachable server).
func TestOwnerOnlyApprovalFailureSaysWhy(t *testing.T) {
	refusing := func(status int) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "refused", status)
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	for name, tc := range map[string]struct {
		base      string
		runnerKey bool
	}{
		"409":         {refusing(http.StatusConflict), true},
		"500":         {refusing(http.StatusInternalServerError), false},
		"unreachable": {closedURL, false},
	} {
		t.Run(name, func(t *testing.T) {
			em := &collectEmitter{}
			ask := codexQuestionHandler(tc.base, "fixture-token", nil, "session", em)
			if _, err := ask(withOwnerOnlyAsk(t.Context()), "Allow rm?"); err == nil {
				t.Fatal("an undelivered approval was answered")
			}
			var msg string
			for _, ev := range em.snapshot() {
				if p, ok := ev.Payload.(agent.ErrorPayload); ok && ev.Kind == "error" {
					msg = p.Message
				}
			}
			if msg == "" || strings.Contains(strings.ToLower(msg), "runner-key") != tc.runnerKey {
				t.Fatalf("error %q, want runner-key explanation %v", msg, tc.runnerKey)
			}
		})
	}
}

func TestDesktopQuestionsUseRotatingSessionCredential(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	desktopToken := "bdt_" + strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := "Bearer session-initial"
		if r.Method == http.MethodGet {
			expected = "Bearer session-renewed"
		}
		if r.Header.Get("Authorization") != expected {
			t.Errorf("question used a credential outside its session")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			if err := writeSessionTokenFile(DaemonStateDir(), "session", "session-renewed"); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"id":"question"}`))
		} else {
			_, _ = w.Write([]byte(`{"answered":true,"answer":"choice"}`))
		}
	}))
	defer server.Close()
	h := &AgentHost{cfg: AgentHostConfig{ServerHTTP: server.URL, DaemonToken: desktopToken}}
	answer, err := h.questionAsker("session", &collectEmitter{}, "", nil, "session-initial")(t.Context(), "Choose")
	if err != nil || answer != "choice" {
		t.Fatal("session-scoped question failed", err)
	}
}
