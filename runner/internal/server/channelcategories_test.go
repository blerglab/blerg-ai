package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http"
	"testing"
	"time"
)

func TestChannelCategoriesPersistenceAndScope(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	path := "/api/sessions/" + fx.sess + "/categories"
	owner := fx.token(topicOwner, "human")
	request := func(method, token string, body any, want int) string {
		t.Helper()
		rec := filingRequest(fx, method, path, token, body)
		if rec.Code != want {
			t.Fatalf("%s: got %d want %d: %s", method, rec.Code, want, rec.Body.String())
		}
		return rec.Body.String()
	}
	request(http.MethodGet, fx.token(topicOther, "human"), nil, 404)
	request(http.MethodGet, fx.otherTok, nil, 403)
	request(http.MethodPost, fx.sessTok, map[string]any{"action": "create", "name": "Research"}, 200)
	body := request(http.MethodGet, owner, nil, 200)
	var state categoryState
	if err := json.Unmarshal([]byte(body), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Categories) != 1 || state.Categories[0].Name != "Research" {
		t.Fatalf("not persisted: %s", body)
	}
	id := state.Categories[0].ID
	child := newUUID()
	_, err := fx.pool.Exec(t.Context(), `INSERT INTO sessions(id,daemon_id,status,project_path,repo,title,parent_session_id,spawning_account_id) VALUES($1,$2,'running','/repos/app','app','Child',$3,$4)`, child, topicDaemonID, fx.sess, topicOwner)
	if err != nil {
		t.Fatal(err)
	}
	childToken, err := db.MintSessionToken(t.Context(), fx.pool, child, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, childToken, nil, 403)
	request(http.MethodPost, owner, map[string]any{"action": "file", "id": id, "session": child}, 200)
	body = request(http.MethodGet, fx.sessTok, nil, 200)
	if err := json.Unmarshal([]byte(body), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Categories[0].Sessions) != 1 || state.Categories[0].Sessions[0] != child {
		t.Fatalf("child not filed: %s", body)
	}
	request(http.MethodPost, owner, map[string]any{"action": "file", "id": id, "session": fx.privSess}, 404)
	request(http.MethodPost, owner, map[string]any{"action": "file", "id": id, "session": fx.sess}, 400)
	request(http.MethodPost, owner, map[string]any{"action": "rename", "id": id, "name": " "}, 400)
	request(http.MethodPost, owner, map[string]any{"action": "rename", "id": id, "name": "Reading"}, 200)
	request(http.MethodPost, owner, map[string]any{"action": "delete", "id": id}, 200)
	request(http.MethodGet, owner, nil, 200)
	var exists bool
	if err := fx.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=$1)`, child).Scan(&exists); err != nil || !exists {
		t.Fatalf("deleting category deleted child: %v", err)
	}
}

func TestChannelCategoriesConcurrentCreates(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	path := "/api/sessions/" + fx.sess + "/categories"
	codes := make(chan int, 8)
	for range 8 {
		go func() {
			codes <- filingRequest(fx, http.MethodPost, path, fx.sessTok, map[string]any{"action": "create", "name": "Topic"}).Code
		}()
	}
	for range 8 {
		if code := <-codes; code != 200 {
			t.Fatalf("create: %d", code)
		}
	}
	rec := filingRequest(fx, http.MethodGet, path, fx.sessTok, nil)
	var state categoryState
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Categories) != 8 {
		t.Fatalf("lost a concurrent write: %s", rec.Body.String())
	}
	first, last := state.Categories[0].ID, state.Categories[7].ID
	rec = filingRequest(fx, http.MethodPost, path, fx.sessTok, map[string]any{"action": "move", "id": last, "before": first})
	if rec.Code != 200 {
		t.Fatalf("move: %s", rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Categories[0].ID != last || state.Categories[1].ID != first {
		t.Fatalf("category order: %s", rec.Body.String())
	}
	rec = filingRequest(fx, http.MethodPost, path, fx.sessTok, map[string]any{"action": "move", "id": last, "before": last})
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Categories[0].ID != last {
		t.Fatalf("self drop moved category: %s", rec.Body.String())
	}
}
