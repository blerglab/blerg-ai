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
	answer, err := codexQuestionHandler(server.URL, "fixture-token", "session", em)(context.Background(), "Pick a fixture")
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
	_, err := codexQuestionHandler(server.URL, "fixture-token", "session", &collectEmitter{})(context.Background(), "Question")
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := codexQuestionHandler(server.URL, "fixture-token", "session", &collectEmitter{})(ctx, "Question")
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
	answer, err := codexQuestionHandler(server.URL, "fixture-token", "session", &collectEmitter{}, func() string { return "root-turn" })(ctx, "Codex subagent asks: Choose a color")
	if err != nil || answer != "Blue" {
		t.Fatal("child private question did not resolve")
	}
	if route := <-posted; route != "canonical-child-run" {
		t.Fatalf("child question bound to unrelated root turn: %q", route)
	}
}
