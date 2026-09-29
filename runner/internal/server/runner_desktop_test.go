package server

// Desktop shape of the runner contract (R9): no cluster JobManager is
// configured, so POST /api/runner/start must run the board's agent on a
// connected daemon instead of 503-ing. These tests drive the same endpoints
// blerg-board drives (start / status / message / interrupt / stop) and assert
// the daemon actually receives the protocol messages.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const runnerTestKey = "runner-key-1234567890"

func desktopRunnerAPI(t *testing.T) (*API, *Hub) {
	t.Helper()
	pool := connectSrvTestDB(t)
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	hub := NewHub() // no JobManager: desktop shape
	api := NewAPI(hub, pool, "daemon-tok-1234567890", nil, "")
	api.SetRunnerKey(runnerTestKey)
	return api, hub
}

func registerDaemon(t *testing.T, api *API, hub *Hub, id, name string, repos ...string) *DaemonConn {
	t.Helper()
	dc := &DaemonConn{ID: id, Name: name, ReposRoot: "/repos", send: make(chan []byte, 8)}
	dc.SetCheckedOutRepos(repos)
	hub.Register(dc)
	if err := db.UpsertDaemon(context.Background(), api.dbPool, id, name, "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	return dc
}

func runnerReq(t *testing.T, api *API, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = strings.NewReader(string(raw))
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer "+runnerTestKey)
	if id := strings.TrimPrefix(path, "/api/runner/sessions/"); id != path {
		req.SetPathValue("id", strings.SplitN(id, "/", 2)[0])
	}
	rec := httptest.NewRecorder()
	switch {
	case path == "/api/runner/start":
		api.HandleRunnerStart(rec, req)
	case strings.HasSuffix(path, "/message"):
		api.HandleRunnerMessage(rec, req)
	case strings.HasSuffix(path, "/interrupt"):
		api.HandleRunnerInterrupt(rec, req)
	case strings.HasSuffix(path, "/stop"):
		api.HandleRunnerStop(rec, req)
	default:
		api.HandleRunnerStatus(rec, req)
	}
	return rec
}

func TestRunnerStartSpawnsOnDaemonWithRepo(t *testing.T) {
	api, hub := desktopRunnerAPI(t)
	dc := registerDaemon(t, api, hub, "00000000-0000-4000-8000-0000000000a1", "laptop", "app", "other")
	rec := runnerReq(t, api, http.MethodPost, "/api/runner/start", map[string]any{
		"repo": "app", "title": "card 1", "prompt": "do it",
		"env": map[string]string{"BLERG_BOARD_URL": "http://localhost:8082", "BLERG_BOARD_TOKEN": "bt", "BLERG_BOARD_BOARD": "b1"},
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	var spawn protocol.SpawnSession
	select {
	case raw := <-dc.send:
		if err := json.Unmarshal(raw, &spawn); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("nothing reached the daemon")
	}
	if spawn.Type != "spawn_session" || spawn.SessionID != out.SessionID || spawn.Kind != "agent" || spawn.Repo != "app" ||
		spawn.InitialPrompt != "do it" || spawn.SessionToken == "" || spawn.ExtraEnv["BLERG_BOARD_TOKEN"] != "bt" || len(spawn.ExtraEnv) != 3 {
		t.Fatalf("spawn = %+v", spawn)
	}
	// Board linkage also travels in the typed fields, so paths that do not
	// forward ExtraEnv still know which board this session answers to.
	if spawn.BoardID != "b1" || spawn.BoardToken != "bt" {
		t.Fatalf("typed board linkage = %q/%q, want b1/bt", spawn.BoardID, spawn.BoardToken)
	}
	if spawn.DangerouslySkipPermissions {
		t.Fatal("board-driven daemon sessions must never bypass permission prompts")
	}
	row, err := db.GetSession(context.Background(), api.dbPool, out.SessionID)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.DaemonID != dc.ID || row.Status != "starting" || row.Runtime == nil || *row.Runtime != "daemon" || row.SpawningAccountID != nil {
		t.Fatalf("row = %+v", row)
	}
	if row.SkipPermissions {
		t.Fatalf("row skip_permissions = true, want false")
	}
	tokRow, err := db.ValidateBoardToken(context.Background(), api.dbPool, spawn.SessionToken)
	if err != nil || tokRow.SessionID != out.SessionID {
		t.Fatalf("session token: %v %+v", err, tokRow)
	}

	// Status / message / interrupt / stop through the daemon.
	rec = runnerReq(t, api, http.MethodGet, "/api/runner/sessions/"+out.SessionID, nil)
	if !strings.Contains(rec.Body.String(), `"lifecycle":"starting"`) || !strings.Contains(rec.Body.String(), `"runtime":"daemon"`) {
		t.Fatalf("status = %s", rec.Body.String())
	}
	reason := "boom"
	_ = HandleSessionStateChanged(context.Background(), hub, api.dbPool, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: out.SessionID, Status: "error", Message: &reason})
	rec = runnerReq(t, api, http.MethodGet, "/api/runner/sessions/"+out.SessionID, nil)
	if !strings.Contains(rec.Body.String(), `"lifecycle":"error"`) || !strings.Contains(rec.Body.String(), `"error_reason":"boom"`) {
		t.Fatalf("status after error = %s", rec.Body.String())
	}
	rec = runnerReq(t, api, http.MethodPost, "/api/runner/sessions/"+out.SessionID+"/message", map[string]any{"text": "hi"})
	if rec.Code != http.StatusAccepted || !strings.Contains(string(<-dc.send), `"agent_user_message"`) {
		t.Fatalf("message = %d", rec.Code)
	}
	rec = runnerReq(t, api, http.MethodPost, "/api/runner/sessions/"+out.SessionID+"/interrupt", nil)
	if rec.Code != http.StatusAccepted || !strings.Contains(string(<-dc.send), `"interrupt_session"`) {
		t.Fatalf("interrupt = %d", rec.Code)
	}
	rec = runnerReq(t, api, http.MethodPost, "/api/runner/sessions/"+out.SessionID+"/stop", nil)
	if rec.Code != http.StatusOK || !strings.Contains(string(<-dc.send), `"kill_session"`) {
		t.Fatalf("stop = %d", rec.Code)
	}
	row, _ = db.GetSession(context.Background(), api.dbPool, out.SessionID)
	if row.Status != "ended" {
		t.Fatalf("after stop status = %s, want ended", row.Status)
	}
	if _, err := db.ValidateBoardToken(context.Background(), api.dbPool, spawn.SessionToken); err == nil {
		t.Fatal("session token must be revoked on stop")
	}
}

// The v1 contract reaches the local sandbox, and on a desktop install it is
// the default wherever the daemon can run it: with runtime omitted, a
// sandbox-capable daemon runs the session in the container and one without
// the image runs it on the host. An explicit choice is honoured verbatim
// either way — "daemon" is the opt-out, and "docker" against a daemon without
// the image is left for the daemon to refuse rather than silently demoted.
func TestRunnerStartSandboxRuntime(t *testing.T) {
	api, hub := desktopRunnerAPI(t)
	dc := registerDaemon(t, api, hub, "00000000-0000-4000-8000-0000000000c1", "laptop", "app")

	start := func(t *testing.T, body map[string]any) (protocol.SpawnSession, *db.SessionRow) {
		t.Helper()
		rec := runnerReq(t, api, http.MethodPost, "/api/runner/start", body)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		var spawn protocol.SpawnSession
		select {
		case raw := <-dc.send:
			if err := json.Unmarshal(raw, &spawn); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("nothing reached the daemon")
		}
		row, err := db.GetSession(context.Background(), api.dbPool, out.SessionID)
		if err != nil || row == nil {
			t.Fatalf("GetSession: %v", err)
		}
		return spawn, row
	}
	runtimeOf := func(t *testing.T, row *db.SessionRow) string {
		t.Helper()
		if row.Runtime == nil {
			t.Fatal("runtime not recorded on the session row")
		}
		return *row.Runtime
	}

	// Omitted runtime on a sandbox-capable daemon: the sandbox.
	dc.SetSandboxAvailable(true)
	spawn, row := start(t, map[string]any{"repo": "app", "prompt": "do it"})
	if !spawn.Sandbox || runtimeOf(t, row) != "docker" {
		t.Fatalf("default with sandbox available: Sandbox=%v runtime=%q, want true/docker", spawn.Sandbox, runtimeOf(t, row))
	}
	if spawn.Kind != "agent" || spawn.DangerouslySkipPermissions || row.SkipPermissions {
		t.Fatalf("a v1 start must stay agent-kind and never ask to skip permissions: %+v", spawn)
	}

	// Explicit runtime:"daemon" is the opt-out: the host, sandbox or not.
	spawn, row = start(t, map[string]any{"repo": "app", "prompt": "do it", "runtime": "daemon"})
	if spawn.Sandbox || runtimeOf(t, row) != "daemon" {
		t.Fatalf(`runtime="daemon": Sandbox=%v runtime=%q, want false/daemon`, spawn.Sandbox, runtimeOf(t, row))
	}

	// Explicit runtime:"docker" is the sandbox.
	spawn, row = start(t, map[string]any{"repo": "app", "prompt": "do it", "runtime": "docker"})
	if !spawn.Sandbox || runtimeOf(t, row) != "docker" {
		t.Fatalf(`runtime="docker": Sandbox=%v runtime=%q, want true/docker`, spawn.Sandbox, runtimeOf(t, row))
	}

	// Omitted runtime on a daemon without the image: the host, the only
	// runtime that daemon can actually run.
	dc.SetSandboxAvailable(false)
	spawn, row = start(t, map[string]any{"repo": "app", "prompt": "do it"})
	if spawn.Sandbox || runtimeOf(t, row) != "daemon" {
		t.Fatalf("default without the image: Sandbox=%v runtime=%q, want false/daemon", spawn.Sandbox, runtimeOf(t, row))
	}

	// ...but an explicit runtime:"docker" is honoured verbatim even there: the
	// daemon reports the missing image itself rather than the server demoting
	// the caller onto the bare host without saying so.
	spawn, row = start(t, map[string]any{"repo": "app", "prompt": "do it", "runtime": "docker"})
	if !spawn.Sandbox || runtimeOf(t, row) != "docker" {
		t.Fatalf(`runtime="docker" without the image: Sandbox=%v runtime=%q, want true/docker`, spawn.Sandbox, runtimeOf(t, row))
	}
}

// Where a cluster is configured, an omitted runtime still means the cluster:
// a connected, sandbox-capable daemon holding the repo does not pull the start
// onto a workstation.
func TestRunnerStartClusterDefaultIgnoresTheSandbox(t *testing.T) {
	f := &fakeK8s{}
	api, hub, pool := clusterRunnerAPI(t, f)
	dc := registerDaemon(t, api, hub, "00000000-0000-4000-8000-0000000000c2", "laptop", "org/proj")
	dc.SetSandboxAvailable(true)

	rec := runnerReq(t, api, http.MethodPost, "/api/runner/start", map[string]any{"repo": "org/proj", "prompt": "do it"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	select {
	case raw := <-dc.send:
		t.Fatalf("an omitted runtime on a cluster install reached the daemon: %s", raw)
	default:
	}
	row, err := db.GetSession(context.Background(), pool, out.SessionID)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.Runtime == nil || *row.Runtime != "cluster" {
		t.Fatalf("runtime = %v, want cluster", row.Runtime)
	}
	f.mu.Lock()
	jobs := len(f.created)
	f.mu.Unlock()
	if jobs != 1 {
		t.Fatalf("cluster Jobs created = %d, want 1", jobs)
	}
}

func TestRunnerStartDaemonSelection(t *testing.T) {
	api, hub := desktopRunnerAPI(t)
	body := map[string]any{"repo": "app", "prompt": "x"}
	if rec := runnerReq(t, api, http.MethodPost, "/api/runner/start", body); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no daemons = %d, want 503", rec.Code)
	}
	only := registerDaemon(t, api, hub, "00000000-0000-4000-8000-0000000000b1", "one") // has no repos listed
	if rec := runnerReq(t, api, http.MethodPost, "/api/runner/start", body); rec.Code != http.StatusAccepted {
		t.Fatalf("single daemon without the repo = %d %s, want 202 (single-daemon rule)", rec.Code, rec.Body.String())
	}
	<-only.send
	registerDaemon(t, api, hub, "00000000-0000-4000-8000-0000000000b2", "two")
	rec := runnerReq(t, api, http.MethodPost, "/api/runner/start", body)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `no connected daemon has repo \"app\"`) {
		t.Fatalf("two daemons, neither has repo = %d %s", rec.Code, rec.Body.String())
	}
	if rec := runnerReq(t, api, http.MethodPost, "/api/runner/start", map[string]any{"repo": "../x", "prompt": "x"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("traversal repo = %d, want 422", rec.Code)
	}
	if rec := runnerReq(t, api, http.MethodPost, "/api/runner/start", map[string]any{"repo": "app", "env": map[string]string{"BLERG_RUNNER_X": "1"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("reserved env = %d, want 422", rec.Code)
	}
}
