package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Every daemon-routed spawn mints a per-session messaging token and ships it
// to the daemon in spawn_session.session_token, so the session's env can
// carry that instead of the daemon master token (desktop-safety C1/C2). The
// token is scoped to exactly that session: it can post messages as that
// session and read that session's answers, and nothing else — not another
// session's, and never the daemon's own routes.
//
// Requires TEST_DATABASE_URL; skips otherwise.

const spawnTokenDaemonTok = "daemon-master-token-1234567890"

func spawnTokenFixture(t *testing.T) (*API, *Hub, *DaemonConn, *pgxpool.Pool) {
	t.Helper()
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	daemonID := "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0d"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "laptop", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	hub := NewHub()
	dc := &DaemonConn{ID: daemonID, Name: "laptop", ReposRoot: "/repos", send: make(chan []byte, 8)}
	hub.Register(dc)
	api := NewAPI(hub, pool, spawnTokenDaemonTok, nil, "")
	return api, hub, dc, pool
}

// spawnViaREST POSTs /api/sessions with a session.start token and returns the
// session id the API reported and the spawn_session the daemon received.
func spawnViaREST(t *testing.T, api *API, dc *DaemonConn, repo string) (string, protocol.SpawnSession) {
	t.Helper()
	tok := enableBrowserAuth(t, api)("session.start")
	body, _ := json.Marshal(map[string]any{"daemon_id": dc.ID, "repo": repo, "runtime": "docker", "title": "t"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostSessions(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp["session_id"], recvSpawn(t, dc)
}

func recvSpawn(t *testing.T, dc *DaemonConn) protocol.SpawnSession {
	t.Helper()
	var msg protocol.SpawnSession
	select {
	case raw := <-dc.send:
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("decode spawn_session: %v", err)
		}
	default:
		t.Fatal("no spawn_session reached the daemon")
	}
	return msg
}

func TestPostSessionsMintsSessionToken(t *testing.T) {
	api, _, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()

	sessionID, msg := spawnViaREST(t, api, dc, "app")
	if msg.Type != "spawn_session" || msg.SessionID != sessionID {
		t.Fatalf("spawn_session = %+v, want session %s", msg, sessionID)
	}
	if msg.SessionToken == "" {
		t.Fatal("spawn_session.session_token is empty: the session would have no messaging credential")
	}
	if msg.SessionToken == spawnTokenDaemonTok {
		t.Fatal("spawn_session.session_token is the daemon master token")
	}
	row, err := db.ValidateBoardToken(ctx, pool, msg.SessionToken)
	if err != nil {
		t.Fatalf("ValidateBoardToken: %v", err)
	}
	if row.SessionID != sessionID || row.BoardID != "" {
		t.Fatalf("token row = %+v, want session %s with no board", row, sessionID)
	}
	if len(row.Capabilities) != 1 || row.Capabilities[0] != "message" {
		t.Fatalf("token caps = %v, want [message]", row.Capabilities)
	}
	// The row is pre-created so the token's FK resolves before the daemon
	// confirms via session_started.
	sess, err := db.GetSession(ctx, pool, sessionID)
	if err != nil || sess == nil {
		t.Fatalf("GetSession: %v, %v", sess, err)
	}
	if sess.Status != "starting" || sess.DaemonID != dc.ID || sess.Repo != "app" {
		t.Fatalf("pre-created session = %+v", sess)
	}
	// ExtraEnv is a Task 15 input; the REST path sends none.
	if len(msg.ExtraEnv) != 0 {
		t.Fatalf("REST spawn sent extra_env %v", msg.ExtraEnv)
	}
}

func TestForwardBrowserSpawnMintsSessionToken(t *testing.T) {
	_, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()

	sessionID, err := hub.forwardBrowserSpawn(&BrowserConn{ID: "b1"}, protocol.BrowserSpawnSession{
		Type: "spawn_session", DaemonID: dc.ID, Repo: "app", Title: "t",
	}, pool)
	if err != nil {
		t.Fatalf("forwardBrowserSpawn: %v", err)
	}
	msg := recvSpawn(t, dc)
	if msg.SessionID != sessionID || msg.SessionToken == "" {
		t.Fatalf("spawn_session = %+v", msg)
	}
	row, err := db.ValidateBoardToken(ctx, pool, msg.SessionToken)
	if err != nil {
		t.Fatalf("ValidateBoardToken: %v", err)
	}
	if row.SessionID != sessionID || row.BoardID != "" || len(row.Capabilities) != 1 || row.Capabilities[0] != "message" {
		t.Fatalf("token row = %+v", row)
	}
	if sess, err := db.GetSession(ctx, pool, sessionID); err != nil || sess == nil || sess.Status != "starting" {
		t.Fatalf("pre-created session = %+v, %v", sess, err)
	}
}

// A DB-less server still spawns (nothing to mint against); the daemon simply
// gets no token. Guards the nil-pool branch.
func TestForwardBrowserSpawnWithoutDBSendsNoToken(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "d1", Name: "laptop", send: make(chan []byte, 8)}
	hub.Register(dc)
	if _, err := hub.forwardBrowserSpawn(&BrowserConn{ID: "b1"}, protocol.BrowserSpawnSession{Type: "spawn_session", DaemonID: "d1", Repo: "app"}, nil); err != nil {
		t.Fatal(err)
	}
	if msg := recvSpawn(t, dc); msg.SessionToken != "" {
		t.Fatalf("token minted without a DB: %+v", msg)
	}
}

// postAs posts an "update" message with tok and returns the status code —
// what `blerg-runner update` does from inside the session.
func postAs(api *API, tok string) int {
	b, _ := json.Marshal(map[string]any{"kind": "update", "body": "still here"})
	req := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostMessages(rec, req)
	return rec.Code
}

// A session the connected daemon no longer reports is gone for good:
// reconcile stops it and its token dies with it. setSessionStatus itself has
// no revocation side effect — only callers that know an end is final revoke.
func TestReconcileUnreportedSessionRevokesToken(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()

	sessionID, msg := spawnViaREST(t, api, dc, "app")
	if code := postAs(api, msg.SessionToken); code != http.StatusCreated {
		t.Fatalf("fresh token cannot message: %d", code)
	}

	// Plain status writes never revoke, whatever the status.
	for _, status := range []string{"idle", "disconnected", "error", "stopped"} {
		now := time.Now()
		setSessionStatus(ctx, hub, pool, sessionID, status, &now, nil, false)
		if _, err := db.ValidateBoardToken(ctx, pool, msg.SessionToken); err != nil {
			t.Fatalf("setSessionStatus(%s) revoked the token: %v", status, err)
		}
	}
	// Put it back to active so reconcile has something to stop.
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}

	reconcileSessions(ctx, hub, pool, dc.ID, nil, nil) // daemon reports nothing
	if row, _ := db.GetSession(ctx, pool, sessionID); row == nil || row.Status != "stopped" {
		t.Fatalf("session after reconcile = %+v, want stopped", row)
	}
	if _, err := db.ValidateBoardToken(ctx, pool, msg.SessionToken); !errors.Is(err, db.ErrTokenInvalid) {
		t.Errorf("reconcile did not revoke the token: %v", err)
	}
	if code := postAs(api, msg.SessionToken); code != http.StatusUnauthorized {
		t.Errorf("revoked token still messages: %d", code)
	}
}

// A daemon WS drop (laptop sleep, network blip, daemon restart) is not the
// end of the session: the pane keeps running with the token it was spawned
// with, the daemon reattaches on reconnect and reconcile revives the row.
// The token must survive all of that, or every later `blerg-runner ask`
// would 401 for the rest of the session.
func TestSessionTokenSurvivesDaemonReconnect(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()

	sessionID, msg := spawnViaREST(t, api, dc, "app")
	// The daemon confirmed it and it is running.
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}

	handleDaemonDisconnect(hub, dc, pool)
	if row, _ := db.GetSession(ctx, pool, sessionID); row == nil || row.Status != "error" {
		t.Fatalf("session after disconnect = %+v, want error (marked by disconnect)", row)
	}
	if _, err := db.ValidateBoardToken(ctx, pool, msg.SessionToken); err != nil {
		t.Fatalf("disconnect revoked the token: %v", err)
	}

	// Daemon comes back, re-registers, and reports the session alive.
	dc2 := &DaemonConn{ID: dc.ID, Name: dc.Name, ReposRoot: dc.ReposRoot, send: make(chan []byte, 8)}
	hub.Register(dc2)
	reconcileSessions(ctx, hub, pool, dc.ID, []string{sessionID}, map[string]string{sessionID: "running"})
	if row, _ := db.GetSession(ctx, pool, sessionID); row == nil || row.Status != "running" {
		t.Fatalf("session after reconcile = %+v, want running", row)
	}
	if _, err := db.ValidateBoardToken(ctx, pool, msg.SessionToken); err != nil {
		t.Fatalf("token did not survive the reconnect: %v", err)
	}
	if code := postAs(api, msg.SessionToken); code != http.StatusCreated {
		t.Fatalf("messaging after reconnect: %d, want 201", code)
	}

	// The genuinely final end still revokes.
	HandleSessionEnded(ctx, hub, pool, protocol.SessionEnded{Type: "session_ended", SessionID: sessionID})
	if code := postAs(api, msg.SessionToken); code != http.StatusUnauthorized {
		t.Fatalf("token outlived session_ended: %d", code)
	}
}

// A mint that never reaches the daemon (send buffer full → 503) must not
// leave a live token or a phantom "starting" row behind.
func TestPostSessionsAbortsTokenWhenDaemonBufferFull(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	full := &DaemonConn{ID: dc.ID, Name: dc.Name, ReposRoot: dc.ReposRoot, send: make(chan []byte)} // unbuffered, nobody reading
	hub.Register(full)

	tok := enableBrowserAuth(t, api)("session.start")
	body, _ := json.Marshal(map[string]any{"daemon_id": dc.ID, "repo": "app", "runtime": "docker"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostSessions(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", rec.Code, rec.Body.String())
	}
	rows, err := db.ListSessionsByDaemon(ctx, pool, dc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("phantom session rows after 503: %+v", rows)
	}
	var live int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM board_tokens WHERE revoked_at IS NULL`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d live token(s) after 503", live)
	}

	// Same guarantee on the browser-WS path.
	if _, err := hub.forwardBrowserSpawn(&BrowserConn{ID: "b1"}, protocol.BrowserSpawnSession{Type: "spawn_session", DaemonID: dc.ID, Repo: "app"}, pool); err == nil {
		t.Fatal("expected send-channel-full error")
	}
	if rows, _ := db.ListSessionsByDaemon(ctx, pool, dc.ID); len(rows) != 0 {
		t.Fatalf("phantom session rows after WS failure: %+v", rows)
	}
}

// The minted token is what `blerg-runner ask/update/note` sends. It must act
// as its own session only: session_id in the body is overridden by the
// token, another session's messages are 403, and it is not a daemon
// credential anywhere.
func TestSessionTokenIsScopedToItsSession(t *testing.T) {
	api, _, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()

	sessA, msgA := spawnViaREST(t, api, dc, "app")
	sessB, msgB := spawnViaREST(t, api, dc, "app")
	tokA, tokB := msgA.SessionToken, msgB.SessionToken
	if tokA == "" || tokB == "" || tokA == tokB {
		t.Fatalf("tokens: %q %q", tokA, tokB)
	}

	post := func(tok string, body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		api.HandlePostMessages(rec, req)
		return rec
	}

	// A's token posting "as B" lands on A: the sending session is derived
	// from the token, never the body.
	rec := post(tokA, map[string]any{"session_id": sessB, "kind": "ask", "body": "which branch?"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("post with A's token: %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	msgRow, err := db.GetMessage(ctx, pool, created["id"])
	if err != nil || msgRow == nil {
		t.Fatalf("GetMessage: %v %v", msgRow, err)
	}
	if msgRow.SessionID != sessA {
		t.Fatalf("message attributed to %s, want A (%s) — body session_id was honoured over the token", msgRow.SessionID, sessA)
	}

	// B's token cannot read A's answer. (wait=1: the message is unanswered,
	// so A's own read holds the long-poll for the wait, not the 25s default.)
	get := func(tok, id string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/messages/"+id+"/answer?wait=1", nil)
		req.SetPathValue("id", id)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		api.HandleGetMessageAnswer(rec, req)
		return rec.Code
	}
	if code := get(tokB, created["id"]); code != http.StatusForbidden {
		t.Fatalf("B reading A's message: %d, want 403", code)
	}
	if code := get(tokA, created["id"]); code != http.StatusOK {
		t.Fatalf("A reading its own message: %d, want 200", code)
	}

	// Not a daemon credential: neither the constant-time master comparison
	// nor the daemon-or-core gate accepts it.
	if api.isDaemonToken(tokA) || tokenEqual(tokA, spawnTokenDaemonTok) {
		t.Fatal("session token passes as the daemon master token")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/anything", nil)
	req.Header.Set("Authorization", "Bearer "+tokA)
	rec = httptest.NewRecorder()
	if _, ok := api.authDaemonOrCoreCap(rec, req, "session.start"); ok || rec.Code != http.StatusUnauthorized {
		t.Fatalf("session token accepted on a daemon/core-only gate: ok=%v code=%d", ok, rec.Code)
	}
	// And it carries no "board" capability, so board-surface routes refuse it.
	req = httptest.NewRequest(http.MethodGet, "/api/boards/x", nil)
	req.Header.Set("Authorization", "Bearer "+tokA)
	rec = httptest.NewRecorder()
	if _, ok := api.authBrowserOrBoard(rec, req, "", "board.read"); ok || rec.Code != http.StatusForbidden {
		t.Fatalf("session token accepted on a board gate: ok=%v code=%d", ok, rec.Code)
	}

	// Session end revokes it (same table as assist board tokens).
	HandleSessionEnded(ctx, api.hub, pool, protocol.SessionEnded{Type: "session_ended", SessionID: sessA})
	if rec := post(tokA, map[string]any{"kind": "update", "body": "still here?"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token still accepted: %d", rec.Code)
	}
}
