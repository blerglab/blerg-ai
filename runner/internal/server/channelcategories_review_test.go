package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func categoryReadRequest(ctx context.Context, fx *topicFx, method string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/sessions/"+fx.sess+"/categories", body).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+fx.sessTok)
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec
}
func TestChannelCategoriesReadDoesNotLockWorkspace(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	tx, err := fx.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), `SELECT id FROM sessions WHERE id=$1 FOR NO KEY UPDATE`, fx.sess); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	rec := categoryReadRequest(ctx, fx, http.MethodGet, nil)
	if rec.Code != 200 {
		t.Fatalf("category read waited on workspace row: %d %s", rec.Code, rec.Body.String())
	}
}

type categorySlowBody struct {
	started, release chan struct{}
	once             sync.Once
	reader           io.Reader
}

func (b *categorySlowBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started); <-b.release })
	return b.reader.Read(p)
}
func TestChannelCategoriesDecodeBeforeWorkspaceLock(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	body := &categorySlowBody{started: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`{"action":"create","name":"Research"}`)}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- categoryReadRequest(t.Context(), fx, http.MethodPost, body) }()
	defer func() {
		close(body.release)
		if rec := <-done; rec.Code != 200 {
			t.Errorf("create: %s", rec.Body.String())
		}
	}()
	select {
	case <-body.started:
	case <-time.After(3 * time.Second):
		t.Fatal("handler never read request")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := fx.pool.Exec(ctx, `INSERT INTO sessions(id,daemon_id,status,project_path,repo,title,parent_session_id) VALUES($1,$2,'running','/repos/app','app','Child',$3)`, newUUID(), topicDaemonID, fx.sess)
	if err != nil {
		t.Fatalf("slow body blocked a child FK insert: %v", err)
	}
}
func TestChannelCategoriesWritesAllowChildInserts(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	gate, err := fx.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gate.Exec(t.Context(), `LOCK TABLE workspace_channel_categories IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	var gatePID int
	if err = gate.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&gatePID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- categoryReadRequest(ctx, fx, http.MethodPost, strings.NewReader(`{"action":"create","name":"Research"}`))
	}()
	defer func() {
		_ = gate.Rollback(t.Context())
		if rec := <-done; rec.Code != 200 {
			t.Errorf("create: %s", rec.Body.String())
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var blocked bool
		if err = fx.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, gatePID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("category write never reached its persistence gate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	insertCtx, insertCancel := context.WithTimeout(t.Context(), time.Second)
	defer insertCancel()
	_, err = fx.pool.Exec(insertCtx, `INSERT INTO sessions(id,daemon_id,status,project_path,repo,title,parent_session_id) VALUES($1,$2,'running','/repos/app','app','Child',$3)`, newUUID(), topicDaemonID, fx.sess)
	if err != nil {
		t.Fatalf("category write blocked a child FK insert: %v", err)
	}
}
func TestChannelCategoriesReadPrunesDiscardedChildren(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	active, discarded, missing := newUUID(), newUUID(), newUUID()
	for _, id := range []string{active, discarded} {
		if _, err := fx.pool.Exec(t.Context(), `INSERT INTO sessions(id,daemon_id,status,project_path,repo,title,parent_session_id,spawning_account_id) VALUES($1,$2,'running','/repos/app','app','Child',$3,$4)`, id, topicDaemonID, fx.sess, topicOwner); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET discard_requested_at=now() WHERE id=$1`, discarded); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(categoryState{Categories: []channelCategory{{ID: newUUID(), Name: "Research", Sessions: []string{active, discarded, missing, fx.ended}}}})
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO workspace_channel_categories(session_id,state) VALUES($1,$2)`, fx.sess, raw); err != nil {
		t.Fatal(err)
	}
	rec := categoryReadRequest(t.Context(), fx, http.MethodGet, nil)
	var state categoryState
	if rec.Code != 200 {
		t.Fatalf("read: %s", rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Categories[0].Sessions) != 1 || state.Categories[0].Sessions[0] != active {
		t.Fatalf("stale children survived read: %s", rec.Body.String())
	}
}
func TestChannelCategoriesCLIThroughHandler(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	fx := newTopicFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	srv := httptest.NewServer(fx.mux)
	defer srv.Close()
	child := newUUID()
	if _, err = fx.pool.Exec(t.Context(), `INSERT INTO sessions(id,daemon_id,status,project_path,repo,title,parent_session_id,spawning_account_id) VALUES($1,$2,'running','/repos/app','app','Child',$3,$4)`, child, topicDaemonID, fx.sess, topicOwner); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) categoryState {
		t.Helper()
		argv := append([]string{filepath.Join("..", "..", ".claude", "skills", "session-messaging", "blerg-runner"), "category"}, args...)
		cmd := exec.CommandContext(t.Context(), python, argv...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "BLERG_RUNNER_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "BLERG_RUNNER_SERVER_HTTP="+srv.URL, "BLERG_RUNNER_SESSION_ID="+fx.sess, "BLERG_RUNNER_SESSION_TOKEN="+fx.sessTok)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("category command failed: %v: %s", err, out)
		}
		var state categoryState
		if err = json.Unmarshal(out, &state); err != nil {
			t.Fatalf("category output: %v", err)
		}
		return state
	}
	created := run("create", "Research")
	if len(created.Categories) != 1 {
		t.Fatal("CLI did not create category")
	}
	id := created.Categories[0].ID
	run("file", child, "--category", id)
	renamed := run("rename", id, "Reading")
	if renamed.Categories[0].Name != "Reading" || len(renamed.Categories[0].Sessions) != 1 || renamed.Categories[0].Sessions[0] != child {
		t.Fatal("CLI filing or rename did not persist")
	}
	listed := run("list")
	if listed.Categories[0].ID != id {
		t.Fatal("CLI list lost category")
	}
	run("file", child)
	deleted := run("delete", id)
	if len(deleted.Categories) != 0 {
		t.Fatal("CLI delete did not persist")
	}
}
