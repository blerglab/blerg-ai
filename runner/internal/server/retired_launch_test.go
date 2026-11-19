package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestRetiredAssistRouteIsAbsent(t *testing.T) {
	rec := httptest.NewRecorder()
	newAuthzMux(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/boards/fixture/assist", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("retired Assist route status = %d, want 404", rec.Code)
	}
	// The production registration must agree with the authorization test mux.
	source, err := os.ReadFile("../../cmd/server/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "POST /api/boards/{id}/assist") {
		t.Fatal("production mux still registers the retired Assist route")
	}
}

func TestBrowserSocketCannotSpawnSessions(t *testing.T) {
	_, hub, dc, pool := spawnTokenFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(pool, func(*http.Request) (identity.Principal, bool) {
		return identity.Principal{Claims: identity.Claims{Sub: "user-1", Kind: "human"}}, true
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	conn := dialBrowserTestServer(t, srv)
	defer conn.Close()
	_ = readMessageWithDeadline(t, conn)

	for _, kind := range []string{"agent", "tmux", ""} {
		if err := conn.WriteJSON(map[string]any{"type": "spawn_session", "daemon_id": dc.ID, "repo": "app", "kind": kind}); err != nil {
			t.Fatal(err)
		}
	}
	// A subscription on the same socket is an ordered barrier after all spawn requests.
	marker := "00000000-0000-4000-8000-000000000123"
	if err := conn.WriteJSON(protocol.SubscribeSession{Type: "subscribe_session", SessionID: marker}); err != nil {
		t.Fatal(err)
	}
	// The subscription history response proves the read pump consumed earlier requests.
	var done protocol.HistoryDone
	if err := json.Unmarshal(readMessageWithDeadline(t, conn), &done); err != nil {
		t.Fatal(err)
	}
	if done.Type != "history_done" || done.SessionID != marker {
		t.Fatalf("unexpected subscription barrier: %+v", done)
	}
	var sessions, tokens int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM sessions), (SELECT count(*) FROM board_tokens)`).Scan(&sessions, &tokens); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || tokens != 0 || len(dc.send) != 0 {
		t.Fatalf("browser spawn created sessions=%d tokens=%d daemon messages=%d", sessions, tokens, len(dc.send))
	}
}
