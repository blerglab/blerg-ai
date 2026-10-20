package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A session's messaging token must outlive every race between the server and
// the daemon at start: a session that runs holds a token that works. The live
// bug: a desktop session ran with a token revoked the second it was minted,
// so every `blerg-runner` call it made was refused.
//
// Requires TEST_DATABASE_URL; skips otherwise.

// liveTokens counts sessionID's unrevoked, unexpired message tokens.
func liveTokens(t *testing.T, pool *pgxpool.Pool, sessionID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM board_tokens
		 WHERE session_id = $1 AND revoked_at IS NULL AND expires_at > now()
		   AND 'message' = ANY(capabilities)`, sessionID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// sessionTokenMsgs drains dc.send and returns the session_token messages in it.
func sessionTokenMsgs(t *testing.T, dc *DaemonConn) []protocol.SessionToken {
	t.Helper()
	var out []protocol.SessionToken
	for {
		select {
		case raw := <-dc.send:
			var env envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("decode daemon message: %v", err)
			}
			if env.Type != "session_token" {
				continue
			}
			var msg protocol.SessionToken
			if err := json.Unmarshal(raw, &msg); err != nil {
				t.Fatalf("decode session_token: %v", err)
			}
			out = append(out, msg)
		default:
			return out
		}
	}
}

// The race that was live: the spawn is queued on the daemon (cloning,
// preparing plugins) and so not in the daemon's active list yet when a
// heartbeat or a reconnect's hello reconciles. The just-made row must not be
// read as "the daemon lost it": not stopped, and its token not revoked.
func TestReconcileLeavesAJustStartedSessionAndItsTokenAlone(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()

	sessionID, msg := spawnViaREST(t, api, dc, "app")
	reconcileSessions(ctx, hub, pool, dc.ID, nil, nil) // the spawn is still in the daemon's queue

	if row, _ := db.GetSession(ctx, pool, sessionID); row == nil || row.Status != "starting" {
		t.Fatalf("session after reconcile = %+v, want still starting", row)
	}
	if code := postAs(api, msg.SessionToken); code != http.StatusCreated {
		t.Fatalf("token after reconcile: %d, want 201 (revoked while the spawn was queued)", code)
	}

	// Past the grace the daemon has had every chance to list it: it is gone.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET started_at = now() - interval '10 minutes' WHERE id = $1`, sessionID); err != nil {
		t.Fatal(err)
	}
	reconcileSessions(ctx, hub, pool, dc.ID, nil, nil)
	if row, _ := db.GetSession(ctx, pool, sessionID); row == nil || row.Status != "stopped" {
		t.Fatalf("session after the grace = %+v, want stopped", row)
	}
	if liveTokens(t, pool, sessionID) != 0 {
		t.Fatal("an unreported session past the grace kept a live token")
	}
}

// The abort race: the server gave up on a spawn (its row and token are gone)
// but the daemon starts it anyway. The session_started that follows must leave
// a running session that can message: a fresh token, delivered to the daemon.
func TestSessionStartedAfterAnAbortGetsAFreshToken(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()

	sessionID, _ := spawnViaREST(t, api, dc, "app")
	abortSpawnSessionToken(ctx, pool, sessionID) // e.g. the start timed out on the server's side

	HandleSessionStarted(ctx, hub, pool, dc.ID, protocol.SessionStarted{
		Type: "session_started", SessionID: sessionID, Repo: "app", ProjectPath: "/repos/app", Kind: "agent",
	})
	if err := HandleSessionStateChanged(ctx, hub, pool, protocol.SessionStateChanged{
		Type: "session_state_changed", SessionID: sessionID, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetSession(ctx, pool, sessionID); row == nil || row.Status != "running" {
		t.Fatalf("session = %+v, want running", row)
	}
	assertTokenDelivered(t, api, dc, pool, sessionID)
}

// A session whose token was revoked by the reconcile race (the row was then
// revived by its own session_started) is healed on that session_started.
func TestSessionStartedAfterARevokeGetsAFreshToken(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()

	sessionID, msg := spawnViaREST(t, api, dc, "app")
	now := time.Now()
	setSessionStatusEnd(ctx, hub, pool, sessionID, "stopped", &now, nil, true, systemEnd(db.EndReasonDaemonUnreported))
	revokeSessionTokens(ctx, pool, sessionID, "reconcile: unreported by daemon")

	HandleSessionStarted(ctx, hub, pool, dc.ID, protocol.SessionStarted{
		Type: "session_started", SessionID: sessionID, Repo: "app", ProjectPath: "/repos/app",
	})
	if code := postAs(api, msg.SessionToken); code != http.StatusUnauthorized {
		t.Fatalf("the revoked token works again: %d", code)
	}
	assertTokenDelivered(t, api, dc, pool, sessionID)
}

// tokenSessions lists the session ids of msgs — never their tokens.
func tokenSessions(msgs []protocol.SessionToken) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.SessionID)
	}
	return out
}

// assertTokenDelivered checks that exactly one session_token reached the
// daemon for sessionID and that it is a working message token for it.
func assertTokenDelivered(t *testing.T, api *API, dc *DaemonConn, pool *pgxpool.Pool, sessionID string) {
	t.Helper()
	if n := liveTokens(t, pool, sessionID); n != 1 {
		t.Fatalf("%d live token(s), want 1", n)
	}
	msgs := sessionTokenMsgs(t, dc)
	if len(msgs) != 1 || msgs[0].SessionID != sessionID || msgs[0].Token == "" {
		t.Fatalf("session_token messages = %d (sessions %v), want one for %s", len(msgs), tokenSessions(msgs), sessionID)
	}
	row, err := db.ValidateBoardToken(context.Background(), pool, msgs[0].Token)
	if err != nil || row.SessionID != sessionID {
		t.Fatalf("delivered token: %+v, %v", row, err)
	}
	if code := postAs(api, msgs[0].Token); code != http.StatusCreated {
		t.Fatalf("delivered token cannot message: %d", code)
	}
	// The daemon stored it.
	handleSessionTokenResult(context.Background(), api.hub, pool,
		protocol.SessionTokenResult{Type: "session_token_result", SessionID: sessionID, OK: true})
}

// A session already running with a dead token (the live one) is healed when
// its daemon (re)connects: the hello lists it, the server mints a new token
// and sends it to the daemon as session_token. Only once: a second connect
// finds a live token and sends nothing.
func TestDaemonHelloHealsARunningSessionWithADeadToken(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()

	sessionID, msg := spawnViaREST(t, api, dc, "app")
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}
	revokeSessionTokens(ctx, pool, sessionID, "test: the live race")
	// A session stopped for good on the same daemon gets nothing, even while the
	// daemon still lists it (it is sent kill_session instead).
	stoppedID, _ := spawnViaREST(t, api, dc, "app")
	now := time.Now()
	setSessionStatusEnd(ctx, hub, pool, stoppedID, "stopped", &now, nil, true, systemEnd(db.EndReasonAutoStopped))
	revokeSessionTokens(ctx, pool, stoppedID, "test: ended")

	srv := httptest.NewServer(hub.ServeDaemon(spawnTokenDaemonTok, pool))
	defer srv.Close()
	hello := func() []protocol.SessionToken {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := conn.WriteJSON(protocol.DaemonHello{
			Type: "daemon_hello", Name: dc.Name, Token: spawnTokenDaemonTok, ProtocolVersion: "1",
			ActiveSessions: []string{sessionID, stoppedID}, SessionTokenFile: true,
		}); err != nil {
			t.Fatal(err)
		}
		var got []protocol.SessionToken
		_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return got // the deadline: nothing more is coming
			}
			var env envelope
			_ = json.Unmarshal(raw, &env)
			if env.Type != "session_token" {
				continue
			}
			var m protocol.SessionToken
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			got = append(got, m)
		}
	}

	got := hello()
	if len(got) != 1 || got[0].SessionID != sessionID {
		t.Fatalf("session_token messages on connect = %d (sessions %v), want one for %s", len(got), tokenSessions(got), sessionID)
	}
	if row, err := db.ValidateBoardToken(ctx, pool, got[0].Token); err != nil || row.SessionID != sessionID {
		t.Fatalf("delivered token: %+v, %v", row, err)
	}
	if code := postAs(api, got[0].Token); code != http.StatusCreated {
		t.Fatalf("delivered token cannot message: %d", code)
	}
	if code := postAs(api, msg.SessionToken); code != http.StatusUnauthorized {
		t.Fatalf("the revoked token works again: %d", code)
	}
	if liveTokens(t, pool, stoppedID) != 0 {
		t.Fatal("a stopped session was given a token")
	}

	if again := hello(); len(again) != 0 {
		t.Fatalf("a second connect re-minted for %v", tokenSessions(again))
	}
	if n := liveTokens(t, pool, sessionID); n != 1 {
		t.Fatalf("%d live tokens after two connects, want 1", n)
	}
}

// The 401 a dead token gets says so, distinctly from a token that was never
// valid, so the CLI can tell the session what is wrong — and the token is not
// in the answer.
func TestRevokedTokenIsToldApartFromAnUnknownOne(t *testing.T) {
	api, _, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	sessionID, msg := spawnViaREST(t, api, dc, "app")
	revokeSessionTokens(ctx, pool, sessionID, "test")

	post := func(tok string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"kind": "update", "body": "x"})
		req := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		api.HandlePostMessages(rec, req)
		return rec
	}
	dead := post(msg.SessionToken)
	if dead.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", dead.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(dead.Body.Bytes(), &body)
	if body["code"] != "session_token_revoked" {
		t.Fatalf("revoked-token body = %s, want code session_token_revoked", dead.Body.String())
	}
	if strings.Contains(dead.Body.String(), msg.SessionToken) {
		t.Fatal("the 401 echoes the token")
	}
	unknown := post(strings.Repeat("ab", 32))
	if unknown.Code != http.StatusUnauthorized || strings.Contains(unknown.Body.String(), "session_token_revoked") {
		t.Fatalf("unknown token: %d %s", unknown.Code, unknown.Body.String())
	}
}

// dialDaemonHello connects a daemon websocket to srv and sends hello.
func dialDaemonHello(t *testing.T, srvURL string, hello protocol.DaemonHello) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srvURL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	hello.Type, hello.Token, hello.ProtocolVersion = "daemon_hello", spawnTokenDaemonTok, "1"
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	return conn
}

// tokenReader reads conn in the background and passes on its session_token
// messages. (A gorilla read that times out breaks the connection for good, so
// a test that reads more than once cannot use read deadlines.)
func tokenReader(t *testing.T, conn *websocket.Conn) <-chan protocol.SessionToken {
	t.Helper()
	ch := make(chan protocol.SessionToken, 16)
	go func() {
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env envelope
			_ = json.Unmarshal(raw, &env)
			if env.Type != "session_token" {
				continue
			}
			var m protocol.SessionToken
			if json.Unmarshal(raw, &m) == nil {
				ch <- m
			}
		}
	}()
	return ch
}

// collectTokens returns what arrives on ch until it is quiet for wait.
func collectTokens(ch <-chan protocol.SessionToken, wait time.Duration) []protocol.SessionToken {
	var got []protocol.SessionToken
	for {
		select {
		case m := <-ch:
			got = append(got, m)
		case <-time.After(wait):
			return got
		}
	}
}

// waitFor polls cond until the readiness deadline.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The real fix: a daemon lists the spawns waiting in its queue, and a queued
// session is alive however long it waits — past the grace, across a reconnect:
// not stopped, its token not revoked, no completion webhook, its MCP grants
// kept.
func TestQueuedSpawnSurvivesAReconnectPastTheGrace(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	var completed []string
	hub.SetCompletionNotifier(func(id string) { completed = append(completed, id) })

	sessionID, msg := spawnViaREST(t, api, dc, "app")
	if _, err := mcpgw.CreateGrants(ctx, pool, sessionID, "acct-1", mcpgw.Proof{TokenID: "tok-1"},
		[]mcpgw.GrantSpec{mcpgw.BoardGrantSpec("board-1", "http://runner.example.test/mcp")}); err != nil {
		t.Fatalf("CreateGrants: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET started_at = now() - interval '10 minutes' WHERE id = $1`, sessionID); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(hub.ServeDaemon(spawnTokenDaemonTok, pool))
	defer srv.Close()
	conn := dialDaemonHello(t, srv.URL, protocol.DaemonHello{Name: dc.Name, SessionTokenFile: true,
		ActiveSessions: []string{}, QueuedSessions: []string{sessionID}})
	_ = collectTokens(tokenReader(t, conn), 300*time.Millisecond) // the hello is processed
	// And the heartbeat's form of the same report.
	reconcileSessionsQueued(ctx, hub, pool, dc.ID, nil, []string{sessionID}, nil)

	row, _ := db.GetSession(ctx, pool, sessionID)
	if row == nil || row.Status != "starting" || row.EndReason != nil {
		t.Fatalf("queued session after reconnect = %+v, want starting with no end", row)
	}
	if code := postAs(api, msg.SessionToken); code != http.StatusCreated {
		t.Fatalf("queued session's token: %d, want 201", code)
	}
	if len(completed) != 0 {
		t.Fatalf("completion webhook fired for a queued session: %v", completed)
	}
	var grants int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_mcp_grants WHERE session_id = $1`, sessionID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 1 {
		t.Fatalf("%d MCP grants left, want 1", grants)
	}
	if hub.FindDaemonForSession(sessionID) == nil {
		t.Fatal("a queued session has no owner: a kill could not reach it")
	}
}

// A daemon that does not say it can store a re-issued token is never sent
// one: it would drop it, and the live token nobody holds would stop every
// later heal for a day.
func TestNoHealForADaemonWithoutTheCapability(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}
	revokeSessionTokens(ctx, pool, sessionID, "test")

	srv := httptest.NewServer(hub.ServeDaemon(spawnTokenDaemonTok, pool))
	defer srv.Close()
	conn := dialDaemonHello(t, srv.URL, protocol.DaemonHello{Name: dc.Name, ActiveSessions: []string{sessionID}})
	if got := collectTokens(tokenReader(t, conn), 800*time.Millisecond); len(got) != 0 {
		t.Fatalf("an old daemon was sent %d token(s)", len(got))
	}
	if n := liveTokens(t, pool, sessionID); n != 0 {
		t.Fatalf("%d token(s) minted for a daemon that cannot take them", n)
	}
}

// A daemon that reports it could not store the token gets it revoked, so the
// next heal (the next heartbeat) tries again; one that acknowledges keeps it.
func TestSessionTokenResultFailureRevokesForARetry(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}
	revokeSessionTokens(ctx, pool, sessionID, "test")

	srv := httptest.NewServer(hub.ServeDaemon(spawnTokenDaemonTok, pool))
	defer srv.Close()
	conn := dialDaemonHello(t, srv.URL, protocol.DaemonHello{Name: dc.Name, SessionTokenFile: true, ActiveSessions: []string{sessionID}})
	tokens := tokenReader(t, conn)
	got := collectTokens(tokens, 500*time.Millisecond)
	if len(got) != 1 || liveTokens(t, pool, sessionID) != 1 {
		t.Fatalf("first heal: %d message(s), %d live token(s)", len(got), liveTokens(t, pool, sessionID))
	}
	if err := conn.WriteJSON(protocol.SessionTokenResult{Type: "session_token_result", SessionID: sessionID, OK: false, Error: "state dir not writable"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failed token to be revoked", func() bool { return liveTokens(t, pool, sessionID) == 0 })

	// The next heartbeat heals again, and this time the daemon stores it.
	if err := conn.WriteJSON(protocol.DaemonHeartbeat{Type: "daemon_heartbeat", ActiveSessions: []string{sessionID}}); err != nil {
		t.Fatal(err)
	}
	again := collectTokens(tokens, 500*time.Millisecond)
	if len(again) != 1 || again[0].SessionID != sessionID {
		t.Fatalf("retry: %v", tokenSessions(again))
	}
	if err := conn.WriteJSON(protocol.SessionTokenResult{Type: "session_token_result", SessionID: sessionID, OK: true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := liveTokens(t, pool, sessionID); n != 1 {
		t.Fatalf("%d live token(s) after an acknowledged heal, want 1", n)
	}
}

// No answer from the daemon within the ack timeout counts as a failure.
func TestUnacknowledgedSessionTokenIsRevoked(t *testing.T) {
	old := sessionTokenAckTimeout
	sessionTokenAckTimeout = 200 * time.Millisecond
	t.Cleanup(func() { sessionTokenAckTimeout = old })
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	revokeSessionTokens(ctx, pool, sessionID, "test")

	healSessionTokens(ctx, hub, pool, dc, []string{sessionID})
	if len(sessionTokenMsgs(t, dc)) != 1 || liveTokens(t, pool, sessionID) != 1 {
		t.Fatal("no heal")
	}
	waitFor(t, "the unacknowledged token to be revoked", func() bool { return liveTokens(t, pool, sessionID) == 0 })
}

// Two heals of one session at once (a reconnect overlapping the old
// connection's last heartbeat) mint one token, not two.
func TestHealIsSingleFlightPerSession(t *testing.T) {
	var h tokenHeals
	if !h.claim("s1") {
		t.Fatal("first claim refused")
	}
	if h.claim("s1") {
		t.Fatal("a second heal of the same session was allowed while the first is pending")
	}
	if !h.claim("s2") {
		t.Fatal("another session's heal was blocked")
	}
	h.set("s1", "raw-1")
	if h.takeIf("s1", "raw-other") {
		t.Fatal("takeIf matched another token")
	}
	if raw, ok := h.take("s1"); !ok || raw != "raw-1" {
		t.Fatalf("take = %q, %v", raw, ok)
	}
	if !h.claim("s1") {
		t.Fatal("a settled heal still blocks the session")
	}
}

// A session_started from a daemon that is not the one running the session
// does not get that session a token.
func TestSessionStartedFromAnotherDaemonIsNotHealed(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}
	revokeSessionTokens(ctx, pool, sessionID, "test")
	otherID := "0e0e0e0e-0e0e-4e0e-8e0e-0e0e0e0e0e0e"
	if err := db.UpsertDaemon(ctx, pool, otherID, "other", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	other := &DaemonConn{ID: otherID, Name: "other", send: make(chan []byte, 8)}
	other.SetSessionTokenFile(true)
	hub.Register(other)

	HandleSessionStarted(ctx, hub, pool, otherID, protocol.SessionStarted{Type: "session_started", SessionID: sessionID, Repo: "app"})
	if n := liveTokens(t, pool, sessionID); n != 0 {
		t.Fatalf("another daemon's session_started minted %d token(s)", n)
	}
	if got := sessionTokenMsgs(t, other); len(got) != 0 {
		t.Fatal("another daemon was sent the session's token")
	}
}
