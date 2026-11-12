package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPMessengerIgnoresDiscussionWithoutAnAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"question_id":"question","answered":false,"outcome":"discussion","answer":"The question remains open; reply in its thread."}`))
	}))
	defer server.Close()
	messenger := &httpMessenger{base: server.URL, token: "fixture-token", client: server.Client()}
	answer, done, status := messenger.fetchAnswer(context.Background(), "question", "")
	if done || status != 200 {
		t.Fatalf("discussion: %+v %v %d", answer, done, status)
	}
}

func TestCodexQuestionDiscussionIsNotAnAnswer(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			_, _ = w.Write([]byte(`{"id":"question-fixture"}`))
			return
		}
		polls++
		if polls == 1 {
			_, _ = w.Write([]byte(`{"answered":false,"outcome":"discussion","answer":"What does this delete?"}`))
			return
		}
		_, _ = w.Write([]byte(`{"answered":true,"answer":"staging only"}`))
	}))
	defer server.Close()
	answer, err := codexQuestionHandler(server.URL, "fixture-token", nil, "session", &collectEmitter{})(t.Context(), "Deploy to staging?")
	if err != nil || answer != "staging only" || polls != 2 {
		t.Fatalf("Codex consumed discussion: %q %v (%d polls)", answer, err, polls)
	}
}
