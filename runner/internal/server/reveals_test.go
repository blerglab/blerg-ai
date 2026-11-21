package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateReveal(t *testing.T) {
	for _, r := range []revealRequest{{Action: "reveal", Target: "src/main.go"}, {Action: "open", Target: "readme.md"}, {Action: "attachment", Target: "11111111111111111111111111111111"}, {Action: "panel", Target: "history"}} {
		if err := r.validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	for _, r := range []revealRequest{{Action: "open", Target: "a\u202eb"}, {Action: "open", Target: "a\u2066b"}, {Action: "open", Target: "a\u200fb"}, {Action: "reveal", Target: "."}, {Action: "reveal", Target: "../secret"}, {Action: "open", Target: "/etc/passwd"}, {Action: "open", Target: "a\\b"}, {Action: "panel", Target: "shell"}, {Action: "attachment", Target: "bad"}, {Action: "execute", Target: "foo"}, {Action: "open", Target: "a\nfile"}} {
		if r.validate() == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}

func TestRevealRoutePersistsOnlyOwnSessionRequests(t *testing.T) {
	fx := newArtFx(t)
	for _, tc := range []struct {
		token, session, body string
		status               int
	}{
		{fx.sessTok, fx.sess, `{"action":"open","target":"src/main.go"}`, 201},
		{fx.otherTok, fx.sess, `{"action":"panel","target":"files"}`, 403},
		{"", fx.sess, `{"action":"open","target":"a"}`, 401},
		{fx.sessTok, fx.sess, `{"action":"open","target":"../a"}`, 400},
		{fx.sessTok, fx.sess, `{"action":"open","target":"a","extra":true}`, 400},
	} {
		req := httptest.NewRequest("POST", "/api/sessions/"+tc.session+"/reveal", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("got %d: %s, want %d", rec.Code, rec.Body.String(), tc.status)
		}
	}
	var count int
	if err := fx.api.dbPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='ui_reveal'`, fx.sess).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("persisted %d reveals", count)
	}
	select {
	case raw := <-fx.events:
		if !strings.Contains(string(raw), `"kind":"ui_reveal"`) {
			t.Fatalf("wrong event: %s", raw)
		}
	default:
		t.Fatal("no live reveal event")
	}
}

func TestRevealHistoryIsExplicitlyMarkedAsReplay(t *testing.T) {
	event := replayedAgentEvent(db.AgentEventRow{SessionID: "fixture", ClientEventID: "request", Kind: "ui_reveal", Payload: `{"action":"open","target":"src/main.go"}`, Ts: time.Now(), Seq: 1})
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["replayed"] != true {
		t.Fatalf("history can be confused with a live reveal: %s", raw)
	}
}

func TestRevealRouteRateLimit(t *testing.T) {
	fx := newArtFx(t)
	for i := 0; i < 31; i++ {
		req := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/reveal", strings.NewReader(`{"action":"panel","target":"files"}`))
		req.Header.Set("Authorization", "Bearer "+fx.sessTok)
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, req)
		want := 201
		if i == 30 {
			want = 429
		}
		if rec.Code != want {
			t.Fatalf("request %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
}
