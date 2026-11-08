package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// A disposable real runner for the browser acceptance path. Only launch/runtime
// dependencies are faked; auth, routes, persistence and channel admission are real.
func TestChannelRecommendationBrowserFixture(t *testing.T) {
	path := os.Getenv("BLERG_SIDE_CHANNEL_FIXTURE")
	if path == "" {
		t.Skip("browser-owned fixture")
	}
	fx := newDelegFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	for _, text := range []string{"Unrelated earlier message", "Write a TDD skill", "Unrelated later message"} {
		raw, _ := json.Marshal(map[string]string{"text": text})
		if _, _, err := db.AppendAgentEvent(t.Context(), fx.pool, fx.parent, newUUID(), "user_message", string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	input := map[string]any{"topic": "Write a TDD skill", "from_seq": 2, "to_seq": 2, "context": "Create a reusable skill"}
	endpoint := "/api/sessions/" + fx.parent + "/channel-recommendations"
	status, rec, _ := fx.do("POST", endpoint, fx.parentTok, input, nil)
	if status != 201 {
		t.Fatalf("recommend %d %v", status, rec)
	}
	token := fx.human("owner-1")
	done := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			writeError(w, 403, "fixture owner only")
			return
		}
		switch r.URL.Path {
		case "/fixture/stop":
			writeJSON(w, 200, map[string]bool{"ok": true})
			select {
			case done <- struct{}{}:
			default:
			}
		case "/fixture/repeat":
			code, body, _ := fx.do("POST", endpoint, fx.parentTok, input, nil)
			writeJSON(w, code, body)
		case "/fixture/launch":
			spawn := fx.spawn()
			writeJSON(w, 200, map[string]any{"prompt": spawn.InitialPrompt, "session_id": spawn.SessionID})
		default:
			fx.mux.ServeHTTP(w, r)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	raw, _ := json.Marshal(map[string]any{"url": server.URL, "token": token, "sessionId": fx.parent, "recommendation": rec})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("browser fixture timed out")
	}
}
