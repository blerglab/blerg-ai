package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCardDataOwnerScope(t *testing.T) {
	fx := newTopicFx(t)
	api, _, _ := newLiveCardAPI(t)
	api.dbPool = fx.pool
	api.coreAuth = fx.api.coreAuth
	_, err := fx.pool.Exec(context.Background(), `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) VALUES($1,1,$2,'mark','{"kind":"review","text":"Review","by":"codex","board_id":"board-a","card":7,"verdict":"approved","target":"abcdef1","round":1}')`, fx.sess, newUUID())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api.RegisterMarkRoutes(mux)
	get := func(account string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/board-cards/board-a/7/data", nil)
		req.Header.Set("Authorization", "Bearer "+fx.token(account, "human"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	w := get(topicOwner)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"verdict":"approved"`) {
		t.Fatalf("card data: %d %s", w.Code, w.Body.String())
	}
	w = get(topicOther)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"verdict":"approved"`) {
		t.Fatalf("other owner saw review: %d %s", w.Code, w.Body.String())
	}
}
