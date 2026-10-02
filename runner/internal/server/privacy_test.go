package server

// Private sessions (spec 8, migration 022): a private session is visible to
// the account that started it and to nobody else, on every surface. These
// tests run against a real database (TEST_DATABASE_URL).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	privAcctA = "acct-a"
	privAcctB = "acct-b"
)

type privFixture struct {
	api  *API
	hub  *Hub
	dc   *DaemonConn
	pool *pgxpool.Pool
	priv ed25519.PrivateKey

	pubSession  string // not private, started by A
	privSession string // private, started by A
	privMsg     string // an open "ask" of privSession
	pubMsg      string // an open "ask" of pubSession
}

// tok mints a core token for account acct. kind "human" or "agent"; an agent
// token acts on behalf of acct.
func (f *privFixture) tok(t *testing.T, kind, acct string, caps ...string) string {
	t.Helper()
	c := identity.Claims{
		Sub: acct, Aud: coreAuthAudience, Kind: kind, Caps: caps,
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
	if kind == "agent" {
		c.Sub = "tok-" + acct
		c.OnBehalfOf = acct
	} else {
		c.Sid = testSID
	}
	return mintRunnerToken(t, f.priv, "core-1", c)
}

// privPool is a pool whose EVERY connection resolves tables in its own schema.
// The package's setupSrvTestSchema sets search_path on one pooled connection,
// which is enough for sequential tests but not for the concurrent handlers a
// WebSocket test runs.
func privPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	const schema = "test_blerg_runner_privacy"
	for _, q := range []string{"DROP SCHEMA IF EXISTS " + schema + " CASCADE", "CREATE SCHEMA " + schema} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		admin.Close()
	})
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return pool
}

func newPrivFixture(t *testing.T) *privFixture {
	t.Helper()
	pool := privPool(t)
	daemonID := "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0d"
	if err := db.UpsertDaemon(context.Background(), pool, daemonID, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	dc := &DaemonConn{ID: daemonID, Name: "laptop", ReposRoot: "/repos", send: make(chan []byte, 8)}
	hub.Register(dc)
	api := NewAPI(hub, pool, spawnTokenDaemonTok, nil, "")
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	api.SetRunnerKey(runnerTestKey)
	f := &privFixture{api: api, hub: hub, dc: dc, pool: pool, priv: priv}
	ctx := context.Background()

	mk := func(title string) string {
		id := newUUID()
		if err := db.InsertSession(ctx, pool, id, dc.ID, "running", "/repos/app", "app", title, ""); err != nil {
			t.Fatal(err)
		}
		if err := db.SetSessionSpawningAccount(ctx, pool, id, privAcctA); err != nil {
			t.Fatal(err)
		}
		if err := db.SetSessionKind(ctx, pool, id, "agent"); err != nil {
			t.Fatal(err)
		}
		hub.SetSessionOwner(id, dc.ID)
		return id
	}
	f.pubSession = mk("public one")
	f.privSession = mk("private one")
	if err := api.MarkPrivate(ctx, f.privSession); err != nil {
		t.Fatalf("MarkPrivate: %v", err)
	}
	for _, id := range []string{f.pubSession, f.privSession} {
		if _, _, err := db.AppendAgentEvent(ctx, pool, id, newUUID(), protocol.CapabilitiesKind,
			`{"engine":"claude","groups":[{"id":"tools","label":"Tools","items":[{"name":"Bash"}]}]}`); err != nil {
			t.Fatal(err)
		}
	}
	if f.privMsg, err = db.CreateMessage(ctx, pool, f.privSession, "ask", "private question"); err != nil {
		t.Fatal(err)
	}
	if f.pubMsg, err = db.CreateMessage(ctx, pool, f.pubSession, "ask", "public question"); err != nil {
		t.Fatal(err)
	}
	return f
}

// do runs handler h for req with the given bearer.
func privDo(h http.HandlerFunc, method, path, id, bearer, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if id != "" {
		req.SetPathValue("id", id)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func listIDs(t *testing.T, rec *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", rec.Code, rec.Body.String())
	}
	var infos []protocol.SessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &infos); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, i := range infos {
		out[i.ID] = true
	}
	return out
}

func TestCanSeeMatrix(t *testing.T) {
	a, empty := privAcctA, ""
	priv := &db.SessionRow{Private: true, SpawningAccountID: &a}
	pub := &db.SessionRow{SpawningAccountID: &a}
	unowned := &db.SessionRow{Private: true, SpawningAccountID: &empty}
	nilOwner := &db.SessionRow{Private: true}

	human := func(sub string) runnerPrincipal { return runnerPrincipal{Kind: "human", Sub: sub} }
	agent := func(acct string, caps ...string) runnerPrincipal {
		return runnerPrincipal{Kind: agentPrincipalKind, Sub: "tok", OnBehalfOf: acct, Caps: caps}
	}
	svc := runnerPrincipal{Kind: "service", Sub: "svc-1"}
	key := runnerPrincipal{Kind: runnerKeyPrincipalKind}

	cases := []struct {
		name string
		p    runnerPrincipal
		row  *db.SessionRow
		want bool
	}{
		{"owner human sees private", human(privAcctA), priv, true},
		{"other human does not see private", human(privAcctB), priv, false},
		{"other human sees non-private (unchanged)", human(privAcctB), pub, true},
		{"owner's agent token sees private", agent(privAcctA), priv, true},
		{"other agent token does not see private", agent(privAcctB), priv, false},
		{"board.admin agent of another account has no bypass", agent(privAcctB, sessionAdminCap), priv, false},
		{"board.admin agent still sees non-private", agent(privAcctB, sessionAdminCap), pub, true},
		{"plain agent of another account: non-private scoped as before", agent(privAcctB), pub, false},
		{"service principal has no bypass", svc, priv, false},
		{"runner key has no bypass", key, priv, false},
		{"runner key sees non-private (unchanged)", key, pub, true},
		{"private with empty owner is nobody's", human(""), unowned, false},
		{"private with nil owner is nobody's", human(privAcctA), nilOwner, false},
		{"nil row is not visible", human(privAcctA), nil, false},
	}
	for _, c := range cases {
		if got := canSee(c.p, c.row); got != c.want {
			t.Errorf("%s: canSee = %v, want %v", c.name, got, c.want)
		}
	}
	if !canSeeAccount(privAcctA, priv) || canSeeAccount(privAcctB, priv) || canSeeAccount("", priv) {
		t.Error("canSeeAccount: private must be the owner's alone")
	}
	if !canSeeAccount(privAcctB, pub) || !canSeeAccount("", pub) {
		t.Error("canSeeAccount: non-private must stay visible to every human")
	}
}

func TestPrivateSession_ListAndPerSessionRoutes(t *testing.T) {
	f := newPrivFixture(t)
	tokA := f.tok(t, "human", privAcctA, coreAuthBrowserCap)
	tokB := f.tok(t, "human", privAcctB, coreAuthBrowserCap)

	// List: every filter variant goes through the same predicate.
	for _, q := range []string{"", "?status=running", "?daemon_id=" + f.dc.ID} {
		idsB := listIDs(t, privDo(f.api.HandleGetSessions, "GET", "/api/sessions"+q, "", tokB, ""))
		if idsB[f.privSession] {
			t.Errorf("list%s: B sees A's private session", q)
		}
		if !idsB[f.pubSession] {
			t.Errorf("list%s: B lost the non-private session", q)
		}
		idsA := listIDs(t, privDo(f.api.HandleGetSessions, "GET", "/api/sessions"+q, "", tokA, ""))
		if !idsA[f.privSession] || !idsA[f.pubSession] {
			t.Errorf("list%s: owner must see both, got %v", q, idsA)
		}
	}

	// PATCH: B cannot rename it, and gets what an unknown id gets.
	unknown := privDo(f.api.HandlePatchSession, "PATCH", "/x", newUUID(), tokB, `{"title":"x"}`)
	if rec := privDo(f.api.HandlePatchSession, "PATCH", "/x", f.privSession, tokB, `{"title":"pwned"}`); rec.Code != http.StatusNotFound || rec.Code != unknown.Code || rec.Body.String() != unknown.Body.String() {
		t.Errorf("PATCH by B: %d %s (unknown id: %d %s)", rec.Code, rec.Body.String(), unknown.Code, unknown.Body.String())
	}
	if row, _ := db.GetSession(context.Background(), f.pool, f.privSession); row == nil || row.Title == nil || *row.Title != "private one" {
		t.Errorf("title changed by a non-owner: %+v", row)
	}
	if rec := privDo(f.api.HandlePatchSession, "PATCH", "/x", f.privSession, tokA, `{"title":"mine"}`); rec.Code != http.StatusNoContent {
		t.Errorf("PATCH by owner: %d %s", rec.Code, rec.Body.String())
	}
	// Non-private: another human still may (unchanged).
	if rec := privDo(f.api.HandlePatchSession, "PATCH", "/x", f.pubSession, tokB, `{"title":"shared"}`); rec.Code != http.StatusNoContent {
		t.Errorf("PATCH non-private by B: %d %s", rec.Code, rec.Body.String())
	}

	// DELETE: B is refused and no kill reaches the daemon; the owner's goes through.
	if rec := privDo(f.api.HandleDeleteSession, "DELETE", "/x", f.privSession, tokB, ""); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE by B: %d, want 404", rec.Code)
	}
	select {
	case m := <-f.dc.send:
		t.Fatalf("a kill reached the daemon for a non-owner: %s", m)
	default:
	}
	if rec := privDo(f.api.HandleDeleteSession, "DELETE", "/x", f.privSession, tokA, ""); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE by owner: %d", rec.Code)
	}
	if rec := privDo(f.api.HandleDeleteSession, "DELETE", "/x", f.pubSession, tokB, ""); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE non-private by B: %d", rec.Code)
	}
}

func TestPrivateSession_CapabilitiesAndMessages(t *testing.T) {
	f := newPrivFixture(t)
	tokA := f.tok(t, "human", privAcctA, coreAuthBrowserCap)
	tokB := f.tok(t, "human", privAcctB, coreAuthBrowserCap)

	caps := func(bearer, id string) string {
		rec := privDo(f.api.HandleGetCapabilities, "GET", "/x", id, bearer, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("capabilities status %d", rec.Code)
		}
		var body map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return string(body["capabilities"])
	}
	if got := caps(tokB, f.privSession); got != "null" {
		t.Errorf("capabilities for B on a private session: %s, want null", got)
	}
	if got := caps(tokA, f.privSession); got == "null" {
		t.Error("capabilities for the owner: got null")
	}
	if got := caps(tokB, f.pubSession); got == "null" {
		t.Error("capabilities of a non-private session must stay visible to B")
	}

	// Message list.
	msgIDs := func(bearer string) map[string]bool {
		rec := privDo(f.api.HandleGetMessages, "GET", "/api/messages", "", bearer, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("messages status %d", rec.Code)
		}
		var ms []protocol.MessageInfo
		if err := json.Unmarshal(rec.Body.Bytes(), &ms); err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, m := range ms {
			out[m.ID] = true
		}
		return out
	}
	if ids := msgIDs(tokB); ids[f.privMsg] || !ids[f.pubMsg] {
		t.Errorf("messages for B = %v: private must be hidden, public kept", ids)
	}
	if ids := msgIDs(tokA); !ids[f.privMsg] || !ids[f.pubMsg] {
		t.Errorf("messages for owner = %v: want both", ids)
	}

	// Reply: a person cannot answer a private session's question unless it is
	// theirs; the session's own side (daemon token) still can.
	if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", f.privMsg, tokB, `{"answer":"hi"}`); rec.Code != http.StatusNotFound {
		t.Errorf("reply by B to a private question: %d, want 404", rec.Code)
	}
	if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", f.privMsg, tokA, `{"answer":"hi"}`); rec.Code != http.StatusOK {
		t.Errorf("reply by owner: %d %s", rec.Code, rec.Body.String())
	}
	if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", f.pubMsg, tokB, `{"answer":"hi"}`); rec.Code != http.StatusOK {
		t.Errorf("reply to a non-private question by B: %d %s", rec.Code, rec.Body.String())
	}
}

// The runner contract (REST; the MCP tools call the same operations).
func TestPrivateSession_RunnerContract(t *testing.T) {
	f := newPrivFixture(t)
	srv := httptest.NewServer(agentContractMux(f.api))
	t.Cleanup(srv.Close)
	get := func(bearer, method, path string, body any) int {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+bearer)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		resp, err := http.DefaultClient.Do(req.WithContext(ctx))
		if err != nil {
			return -1 // a stream that stays open is an "accepted" answer
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	humanB := f.tok(t, "human", privAcctB, coreAuthRunnerCap)
	humanA := f.tok(t, "human", privAcctA, coreAuthRunnerCap)
	agentB := f.tok(t, "agent", privAcctB, coreAuthRunnerCap)
	adminB := f.tok(t, "agent", privAcctB, coreAuthRunnerCap, sessionAdminCap)
	agentA := f.tok(t, "agent", privAcctA, coreAuthRunnerCap)

	for _, op := range sessionOps(f.privSession) {
		for name, bearer := range map[string]string{
			"human B": humanB, "agent B": agentB, "board.admin agent B": adminB, "runner key": runnerTestKey,
		} {
			if code := get(bearer, op.method, op.path, op.body); code != http.StatusNotFound {
				t.Errorf("%s as %s on a private session: %d, want 404", op.name, name, code)
			}
		}
	}
	// The owner reaches it (status is enough; the streaming op would hold open).
	for _, bearer := range []string{humanA, agentA} {
		if code := get(bearer, "GET", "/api/runner/sessions/"+f.privSession, nil); code != http.StatusOK {
			t.Errorf("status as the owner: %d, want 200", code)
		}
		if code := get(bearer, "GET", "/api/runner/sessions/"+f.privSession+"/events", nil); code != http.StatusOK {
			t.Errorf("events as the owner: %d, want 200", code)
		}
	}
	// Non-private sessions behave as before: an operator-class caller sees
	// them, an ordinary agent token of another account does not.
	for _, c := range []struct {
		name, bearer string
		want         int
	}{
		{"human B", humanB, 200}, {"runner key", runnerTestKey, 200}, {"board.admin agent B", adminB, 200},
		{"plain agent B", agentB, 404},
	} {
		if code := get(c.bearer, "GET", "/api/runner/sessions/"+f.pubSession, nil); code != c.want {
			t.Errorf("non-private status as %s: %d, want %d", c.name, code, c.want)
		}
	}
	// Nothing got the private session stopped or messaged along the way.
	if row, _ := db.GetSession(context.Background(), f.pool, f.privSession); row == nil || row.Status != "running" {
		t.Errorf("a refused call changed the session: %+v", row)
	}
}

// Webhooks: a private session's result is only ever posted to a receiver its
// owner chose.
func TestPrivateSession_CompletionWebhook(t *testing.T) {
	var hits atomic.Int32
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(recv.Close)

	// Private with no owning account: nobody chose the receiver, so nothing goes out.
	api, pool, id := webhookSession(t, recv.URL+"/hook", "")
	if err := api.MarkPrivate(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	api.deliverCompletion(context.Background(), id)
	if hits.Load() != 0 {
		t.Fatalf("an unowned private session's result was posted %d time(s)", hits.Load())
	}

	// Private with an owner: the owner's own callback is honoured.
	if err := db.SetSessionSpawningAccount(context.Background(), pool, id, privAcctA); err != nil {
		t.Fatal(err)
	}
	api.deliverCompletion(context.Background(), id)
	if hits.Load() != 1 {
		t.Fatalf("an owned private session's callback hits = %d, want 1", hits.Load())
	}
}

// Push: a private session's notification is never sent (the subscription has no account).
func TestPrivateSession_NoPush(t *testing.T) {
	f := newPrivFixture(t)
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	ctx := context.Background()
	if err := db.UpsertPushSubscription(ctx, f.pool, "http://127.0.0.1:1/never", "p", "a"); err != nil {
		t.Fatal(err)
	}
	var sent atomic.Int32
	saved := sendWebPush
	sendWebPush = func([]byte, *webpush.Subscription, *webpush.Options) (*http.Response, error) {
		sent.Add(1)
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = saved })

	SendPush(f.pool, "t", "b", "/sessions/"+f.privSession)
	if sent.Load() != 0 {
		t.Fatal("SendPush attempted a delivery for a private session")
	}
	SendPush(f.pool, "t", "b", "/sessions/"+f.pubSession)
	if sent.Load() == 0 {
		t.Fatal("SendPush must still attempt delivery for a non-private session")
	}

	// The two callers skip on their own, before ever reaching SendPush.
	row, err := db.GetSession(ctx, f.pool, f.privSession)
	if err != nil || row == nil || !row.Private {
		t.Fatalf("row: %+v %v", row, err)
	}
	before := sent.Load()
	f.api.pushForMessage(*row, db.MessageRow{Kind: "ask", Body: "q"})
	if sent.Load() != before {
		t.Error("pushForMessage pushed for a private session")
	}
}

// ─── the browser WebSocket ───────────────────────────────────────────────────

type privWS struct {
	conn *websocket.Conn
	msgs chan []byte
}

func (f *privFixture) dial(t *testing.T, srv *httptest.Server, bearer string) *privWS {
	t.Helper()
	conn, _, err := dialBrowserWS(srv, []string{wsBearerSubprotocol, bearer})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	w := &privWS{conn: conn, msgs: make(chan []byte, 256)}
	go func() {
		for {
			_, m, err := conn.ReadMessage()
			if err != nil {
				close(w.msgs)
				return
			}
			w.msgs <- m
		}
	}()
	return w
}

// until collects messages up to and including the first one containing marker.
func (w *privWS) until(t *testing.T, marker string) []string {
	t.Helper()
	var got []string
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-w.msgs:
			if !ok {
				t.Fatalf("socket closed before %q; got %v", marker, got)
			}
			got = append(got, string(m))
			if strings.Contains(string(m), marker) {
				return got
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q; got %v", marker, got)
		}
	}
}

func (w *privWS) send(t *testing.T, v any) {
	t.Helper()
	if err := w.conn.WriteJSON(v); err != nil {
		t.Fatal(err)
	}
}

func anyContains(msgs []string, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

func TestPrivateSession_BrowserSocketOutbound(t *testing.T) {
	f := newPrivFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", f.hub.ServeBrowser(f.pool, f.api.AuthorizeBrowserWS))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	a := f.dial(t, srv, f.tok(t, "human", privAcctA, coreAuthBrowserCap))
	b := f.dial(t, srv, f.tok(t, "human", privAcctB, coreAuthBrowserCap))

	// initial_state: B's carries neither the private session nor its message.
	initA := a.until(t, "initial_state")
	initB := b.until(t, "initial_state")
	if !anyContains(initA, f.privSession) || !anyContains(initA, "private question") {
		t.Error("initial_state for the owner must include the private session and message")
	}
	if anyContains(initB, f.privSession) || anyContains(initB, "private question") {
		t.Errorf("initial_state for B leaks the private session: %v", initB)
	}
	if !anyContains(initB, f.pubSession) || !anyContains(initB, "public question") {
		t.Error("initial_state for B lost non-private data")
	}

	// Every kind of broadcast that names the private session.
	msgInfo := protocol.MessageInfo{ID: "m1", SessionID: f.privSession, Kind: "update", Body: "secret body"}
	f.hub.BroadcastJSON(protocol.BrowserSessionStarted{Type: "session_started", Session: protocol.SessionInfo{ID: f.privSession, Title: "secret title"}})
	f.hub.BroadcastJSON(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: f.privSession, Status: "waiting"})
	f.hub.BroadcastJSON(protocol.SessionMetaChanged{Type: "session_meta_changed", SessionID: f.privSession, Model: "secret-model"})
	f.hub.BroadcastJSON(protocol.MessageCreated{Type: "message_created", Message: msgInfo})
	f.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: msgInfo})
	f.hub.BroadcastJSON(protocol.SessionReadChanged{Type: "session_read_changed", SessionID: f.privSession})
	f.hub.BroadcastJSON(protocol.SessionStarChanged{Type: "session_star_changed", SessionID: f.privSession, Starred: true})
	f.hub.BroadcastJSON(protocol.PreviewUpdated{Type: "preview_updated", HTML: "<b>secret preview</b>", SessionID: f.privSession})
	f.hub.BroadcastTitleChanged(f.privSession, "secret retitle")
	f.hub.BroadcastJSONPerAccount(func(string) any {
		return protocol.SessionEnded{Type: "session_ended", SessionID: f.privSession}
	})
	f.hub.BroadcastToBrowsers([]byte(`{"type":"agent_event","session_id":"` + f.privSession + `","kind":"assistant_text","payload":{"text":"secret text"}}`))
	f.hub.BroadcastJSON(protocol.DaemonDisconnected{Type: "daemon_disconnected", DaemonID: "d1",
		AffectedSessionIDs: []string{f.privSession, f.pubSession}})
	// The marker: a non-private broadcast both sockets must see, sent last.
	f.hub.BroadcastJSON(protocol.SessionMetaChanged{Type: "session_meta_changed", SessionID: f.pubSession, Model: "marker-model"})

	gotB := b.until(t, "marker-model")
	gotA := a.until(t, "marker-model")

	for _, secret := range []string{"secret title", "secret-model", "secret body", "secret preview", "secret retitle", "secret text"} {
		if anyContains(gotB, secret) {
			t.Errorf("B received %q: %v", secret, gotB)
		}
		if !anyContains(gotA, secret) {
			t.Errorf("owner did not receive %q", secret)
		}
	}
	// B's daemon_disconnected keeps only what B may see; the owner's is whole.
	for _, m := range gotB {
		if strings.Contains(m, "daemon_disconnected") {
			if strings.Contains(m, f.privSession) || !strings.Contains(m, f.pubSession) {
				t.Errorf("daemon_disconnected for B = %s", m)
			}
		}
	}
	for _, m := range gotA {
		if strings.Contains(m, "daemon_disconnected") && (!strings.Contains(m, f.privSession) || !strings.Contains(m, f.pubSession)) {
			t.Errorf("daemon_disconnected for the owner = %s", m)
		}
	}
	if !anyContains(gotB, `"type":"daemon_disconnected"`) {
		t.Error("B must still learn the daemon went away")
	}
}

func TestPrivateSession_BrowserSocketInbound(t *testing.T) {
	f := newPrivFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", f.hub.ServeBrowser(f.pool, f.api.AuthorizeBrowserWS))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	a := f.dial(t, srv, f.tok(t, "human", privAcctA, coreAuthBrowserCap))
	b := f.dial(t, srv, f.tok(t, "human", privAcctB, coreAuthBrowserCap))
	a.until(t, "initial_state")
	b.until(t, "initial_state")

	// B tries every session command against the private session, then makes
	// one legitimate request whose answer marks the end of what B will get.
	psid := f.privSession
	b.send(t, map[string]any{"type": "subscribe_agent_events", "session_id": psid, "after_seq": 0})
	b.send(t, map[string]any{"type": "subscribe_session", "session_id": psid})
	b.send(t, map[string]any{"type": "send_input", "session_id": psid, "data": "rm -rf"})
	b.send(t, map[string]any{"type": "resize_session", "session_id": psid, "cols": 80, "rows": 24})
	b.send(t, map[string]any{"type": "request_scrollback", "session_id": psid})
	b.send(t, map[string]any{"type": "agent_user_message", "session_id": psid, "text": "do it"})
	b.send(t, map[string]any{"type": "interrupt_session", "session_id": psid})
	b.send(t, map[string]any{"type": "set_session_model", "session_id": psid, "model": "opus"})
	b.send(t, map[string]any{"type": "mark_session_read", "session_id": psid})
	b.send(t, map[string]any{"type": "set_session_star", "session_id": psid, "starred": true})
	b.send(t, map[string]any{"type": "subscribe_agent_events", "session_id": f.pubSession, "after_seq": 0})
	got := b.until(t, "agent_events_replay_done")
	if anyContains(got, psid) {
		t.Fatalf("B got data about the private session: %v", got)
	}
	select {
	case m := <-f.dc.send:
		t.Fatalf("a non-owner's command reached the daemon: %s", m)
	default:
	}
	if row, _ := db.GetSession(context.Background(), f.pool, psid); row == nil || row.Starred {
		t.Error("a non-owner starred a private session")
	}
	// Live fan-out: B holds no subscription, so terminal/agent output for the
	// private session cannot reach it.
	f.hub.FanOutSessionOutput(psid, []byte(`{"type":"session_output","session_id":"`+psid+`","data":"c2VjcmV0"}`))
	f.hub.BroadcastJSON(protocol.SessionMetaChanged{Type: "session_meta_changed", SessionID: f.pubSession, Model: "marker-2"})
	if anyContains(b.until(t, "marker-2"), "c2VjcmV0") {
		t.Fatal("B received the private session's live output")
	}

	// The owner, by contrast, is served (subscribe, replay, input).
	a.send(t, map[string]any{"type": "subscribe_agent_events", "session_id": psid, "after_seq": 0})
	a.send(t, map[string]any{"type": "send_input", "session_id": psid, "data": "ls"})
	ga := a.until(t, "agent_events_replay_done")
	if !anyContains(ga, protocol.CapabilitiesKind) {
		t.Errorf("owner's replay missed the persisted events: %v", ga)
	}
	select {
	case <-f.dc.send:
	case <-time.After(3 * time.Second):
		t.Fatal("the owner's send_input never reached the daemon")
	}
}

// Marking a session private cuts off sockets that were already watching it.
func TestMarkPrivateEvictsExistingSubscribers(t *testing.T) {
	f := newPrivFixture(t)
	id := newUUID()
	ctx := context.Background()
	if err := db.InsertSession(ctx, f.pool, id, f.dc.ID, "running", "/repos/app", "app", "later private", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionSpawningAccount(ctx, f.pool, id, privAcctA); err != nil {
		t.Fatal(err)
	}
	other := &BrowserConn{ID: "b-other", AccountID: privAcctB, send: make(chan []byte, 4), subs: map[string]context.CancelFunc{}}
	owner := &BrowserConn{ID: "b-owner", AccountID: privAcctA, send: make(chan []byte, 4), subs: map[string]context.CancelFunc{}}
	f.hub.RegisterBrowser(other)
	f.hub.RegisterBrowser(owner)
	chOther, chOwner := make(chan []byte, 4), make(chan []byte, 4)
	f.hub.Subscribe(id, other.ID, chOther)
	f.hub.Subscribe(id, owner.ID, chOwner)

	if err := f.api.MarkPrivate(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.hub.FanOutSessionOutput(id, []byte("x"))
	if len(chOther) != 0 {
		t.Error("a subscriber that is not the owner still receives output after MarkPrivate")
	}
	if len(chOwner) != 1 {
		t.Error("the owner's subscription must survive MarkPrivate")
	}
	if err := f.api.MarkPrivate(ctx, newUUID()); err == nil {
		t.Error("MarkPrivate of an unknown session must fail")
	}
}

// A private session is never bound to a ticket, and marking one unbinds it: a
// ticket card is visible to the whole board and names its session.
func TestPrivateSession_NotBoundToTickets(t *testing.T) {
	f := newPrivFixture(t)
	ctx := context.Background()
	board, err := db.CreateBoard(ctx, f.pool, "priv-board", nil, []string{"repo-x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := db.CreateTicket(ctx, f.pool, db.TicketInput{BoardID: board.ID, Title: "t", Priority: "medium", Repos: []string{"repo-x"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetTicketSession(ctx, f.pool, tk.ID, f.privSession); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetTicketCard(ctx, f.pool, tk.ID); got.SessionID != nil {
		t.Errorf("a private session was bound to a ticket: %v", *got.SessionID)
	}
	if err := db.SetTicketSession(ctx, f.pool, tk.ID, f.pubSession); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetTicketCard(ctx, f.pool, tk.ID); got.SessionID == nil {
		t.Error("a non-private session must still bind (unchanged)")
	}
	if err := f.api.MarkPrivate(ctx, f.pubSession); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetTicketCard(ctx, f.pool, tk.ID); got.SessionID != nil {
		t.Error("marking a session private must unbind it from its ticket")
	}
}

// The preview endpoint: a push naming a private session is delivered to the
// owner alone and does not become the shared latest preview.
func TestPrivateSession_Preview(t *testing.T) {
	f := newPrivFixture(t)
	owner := &BrowserConn{ID: "p-owner", AccountID: privAcctA, send: make(chan []byte, 8)}
	other := &BrowserConn{ID: "p-other", AccountID: privAcctB, send: make(chan []byte, 8)}
	f.hub.RegisterBrowser(owner)
	f.hub.RegisterBrowser(other)

	push := func(sid, html string) {
		body, _ := json.Marshal(map[string]string{"session_id": sid, "html": html})
		req := httptest.NewRequest("POST", "/api/preview", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+spawnTokenDaemonTok)
		rec := httptest.NewRecorder()
		f.hub.HandlePreview(spawnTokenDaemonTok)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview status %d", rec.Code)
		}
	}
	push(f.privSession, "<i>secret</i>")
	if len(other.send) != 0 || len(owner.send) != 1 {
		t.Errorf("private preview delivery: other=%d owner=%d", len(other.send), len(owner.send))
	}
	if strings.Contains(f.hub.Preview(), "secret") {
		t.Error("a private session's preview became the shared latest")
	}
	push(f.pubSession, "<i>shared</i>")
	if len(other.send) != 1 || f.hub.Preview() != "<i>shared</i>" {
		t.Errorf("non-private preview must behave as before: other=%d preview=%q", len(other.send), f.hub.Preview())
	}
}

// The hub learns which sessions are private at start-up (a restart forgets
// nothing), and when it cannot read them it fails closed.
func TestPrivacyLoadedAtStartupAndFailsClosed(t *testing.T) {
	f := newPrivFixture(t)

	fresh := NewHub()
	fresh.SetPrivacyPool(f.pool)
	owners, ok := fresh.privateOwners([]string{f.privSession, f.pubSession})
	if !ok || len(owners) != 1 || owners[f.privSession] != privAcctA {
		t.Fatalf("after a restart the hub knows %v (ok=%v), want only the private session owned by %s", owners, ok, privAcctA)
	}
	if fresh.accountCanSee(privAcctB, f.privSession) || !fresh.accountCanSee(privAcctA, f.privSession) || !fresh.accountCanSee(privAcctB, f.pubSession) {
		t.Error("accountCanSee disagrees with the loaded record")
	}

	// A database that cannot be read: nothing naming a session may go out.
	blind := NewHub()
	dead, err := pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	dead.Close()
	blind.SetPrivacyPool(dead)
	rcv := &BrowserConn{ID: "blind", AccountID: privAcctA, send: make(chan []byte, 4)}
	blind.RegisterBrowser(rcv)
	blind.BroadcastJSON(protocol.SessionMetaChanged{Type: "session_meta_changed", SessionID: f.pubSession, Model: "m"})
	blind.BroadcastJSON(protocol.DaemonConnected{Type: "daemon_connected", Daemon: protocol.DaemonInfo{ID: "d"}})
	if len(rcv.send) != 1 || !strings.Contains(string(<-rcv.send), "daemon_connected") {
		t.Error("a hub that cannot tell must withhold session events but still deliver the rest")
	}
	if blind.accountCanSee(privAcctA, f.pubSession) {
		t.Error("accountCanSee must fail closed when the hub cannot tell")
	}
}
