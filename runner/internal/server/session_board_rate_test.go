package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type commandBoard struct{ fakeSessionBoard }

func (f *commandBoard) CallBoard(ctx context.Context, call mcpgw.BoardCall) (json.RawMessage, error) {
	_, _ = f.fakeSessionBoard.CallBoard(ctx, call)
	card := map[string]any{"id": "aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb", "title": "Example card", "version": 1, "board_id": "board-a"}
	var value any
	switch call.Tool {
	case "blerg_board_get":
		value = map[string]string{"id": "board-a", "name": "Example board"}
	case "blerg_column_list":
		value = []any{map[string]string{"id": "cccccccc-1111-2222-3333-dddddddddddd", "name": "Doing"}}
	case "blerg_card_search":
		value = []any{card}
	default:
		value = card
	}
	body, _ := json.Marshal(value)
	result, _ := json.Marshal(map[string]any{"content": []any{map[string]string{"type": "text", "text": string(body)}}})
	return result, nil
}

func TestTenNamedBoardCommandsWithinOneMinute(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	setLevel(fx, fx.token(topicOwner, "human"), "board-a", db.BoardLevelFull)
	board := &commandBoard{}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	mux := http.NewServeMux()
	fx.api.RegisterSessionBoardRoutes(mux)
	var mu sync.Mutex
	counts := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts[r.Method+" "+r.URL.Path]++
		mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()
	script, err := filepath.Abs("../../.claude/skills/session-messaging/blerg-runner")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "BLERG_RUNNER_") {
			env = append(env, v)
		}
	}
	env = append(env, "BLERG_RUNNER_SERVER_HTTP="+srv.URL, "BLERG_RUNNER_SESSION_ID="+fx.sess, "BLERG_RUNNER_SESSION_TOKEN="+fx.sessTok, "BLERG_RUNNER_DAEMON_STATE_DIR="+t.TempDir())
	for i := 0; i < 10; i++ {
		cmd := exec.CommandContext(t.Context(), "python3", script, "ticket", "move", "Example card", "--column", "Doing")
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("command %d: %v: %s", i+1, err, out)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if got := counts["GET /api/sessions/"+fx.sess+"/board-access"]; got != 10 {
		t.Fatalf("access lookups: %d, want 10", got)
	}
	if got := counts["POST /api/sessions/"+fx.sess+"/board-tools"]; got != 60 {
		t.Fatalf("tool requests: %d, want 60", got)
	}
	if board.count() != 60 {
		t.Fatalf("upstream calls: %d", board.count())
	}
}

func TestBoardRateWindowAndRetryDeadline(t *testing.T) {
	old := boardCallsPerMinute
	boardCallsPerMinute = 2
	t.Cleanup(func() { boardCallsPerMinute = old })
	sid := "rate-window-fixture"
	boardCallLimit.Lock()
	delete(boardCallLimit.m, sid)
	boardCallLimit.Unlock()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	allowed := func(now time.Time) bool { return boardCallRetryAfter(sid, now, boardCallsPerMinute) == 0 }
	if !allowed(start) || !allowed(start.Add(time.Second)) {
		t.Fatal("initial calls refused")
	}
	if allowed(start.Add(20 * time.Second)) {
		t.Fatal("burst exceeded cap")
	}
	if allowed(start.Add(59 * time.Second)) {
		t.Fatal("window ended too early")
	}
	if !allowed(start.Add(time.Minute)) {
		t.Fatal("boundary did not reset: rejected retries must not extend the window")
	}
	if !allowed(start.Add(time.Minute + time.Second)) {
		t.Fatal("second timestamp did not expire")
	}
}

func TestBoardRateRefusalSaysWhenToRetry(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	old := boardCallsPerMinute
	boardCallsPerMinute = 1
	t.Cleanup(func() { boardCallsPerMinute = old })
	boardCallLimit.Lock()
	boardCallLimit.m[fx.sess] = []time.Time{time.Now().Add(-30 * time.Second)}
	boardCallLimit.Unlock()
	rec := filingRequest(fx, http.MethodGet, "/api/sessions/"+fx.sess+"/board-access", fx.sessTok, nil)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "30" || !strings.Contains(rec.Body.String(), "retry in 30 seconds") {
		t.Fatalf("rate refusal: %d retry=%q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}
