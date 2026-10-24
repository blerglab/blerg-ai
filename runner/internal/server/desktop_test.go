package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func desktopFakeCore(t *testing.T, a *API, bindings map[string]desktopauth.Binding) {
	t.Helper()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Key") != "fixture-internal-key" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/internal/desktop-tokens/validate":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			b, ok := bindings[body["token"]]
			if !ok {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(b)
		case "/internal/desktop-owners":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": privAcctA, "name": "Person A"}, {"id": privAcctB, "name": "Person B"}})
		case "/internal/desktop-tokens/issue":
			var body struct {
				ID       string `json:"desktop_id"`
				Owner    string `json:"owner_id"`
				Revision int64  `json:"ownership_revision"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Owner == "" {
				body.Owner = privAcctA
			}
			b := desktopauth.Binding{DesktopID: body.ID, OwnerID: body.Owner, Revision: body.Revision, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
			_ = json.NewEncoder(w).Encode(map[string]any{"binding": b, "token": "bdt_" + strings.Repeat("c", 64)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(core.Close)
	a.coreURL, a.coreInternalKey = core.URL, "fixture-internal-key"
}
func desktopConnect(t *testing.T, url, token, id, name string, ok bool) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(url, "http")+"/ws/daemon", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Token: token, DesktopID: id, Name: name, Mode: "local", ReposRoot: "/repos", Welcome: true}); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := c.ReadMessage()
	if ok && (err != nil || !bytes.Contains(raw, []byte("daemon_welcome"))) {
		t.Fatalf("desktop refused: %s %v", raw, err)
	}
	if !ok && err == nil {
		t.Fatalf("forged credential accepted: %s", raw)
	}
	_ = c.SetReadDeadline(time.Time{})
	return c
}
func desktopWait(t *testing.T, h *Hub, id string) *DaemonConn {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if d := h.GetDaemon(id); d != nil {
			return d
		}
		select {
		case <-deadline:
			t.Fatal("desktop not registered")
			return nil
		case <-time.After(time.Millisecond):
		}
	}
}
func TestDesktopCredentialsCannotCrossOwnersOrReassignedConnections(t *testing.T) {
	t.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", "false")
	fx := newPrivFixture(t)
	idA, idB := newUUID(), newUUID()
	tokenA, tokenB := "bdt_"+strings.Repeat("a", 64), "bdt_"+strings.Repeat("b", 64)
	bindings := map[string]desktopauth.Binding{tokenA: {DesktopID: idA, OwnerID: privAcctA, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}, tokenB: {DesktopID: idB, OwnerID: privAcctB, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}}
	bad := "bdt_" + strings.Repeat("d", 64)
	forged := bindings[tokenA]
	forged.DesktopID = idB
	bindings[bad] = forged
	fresh := "bdt_" + strings.Repeat("e", 64)
	bindings[fresh] = desktopauth.Binding{DesktopID: idA, OwnerID: privAcctB, Revision: 2, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
	desktopFakeCore(t, fx.api, bindings)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", fx.hub.ServeDaemon(spawnTokenDaemonTok, fx.pool))
	fx.api.RegisterDesktopRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	checkCredential := func(token string, expected int) {
		req := httptest.NewRequest(http.MethodGet, "/api/daemon/whoami", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		fx.api.HandleDaemonWhoami(w, req)
		if w.Code != expected {
			t.Fatalf("installer credential preflight: %d %s", w.Code, w.Body.String())
		}
	}
	checkCredential(tokenA, 200) // Hosted enrollment precedes the first socket/row.
	a := desktopConnect(t, srv.URL, tokenA, idA, "same-name", true)
	dcA := desktopWait(t, fx.hub, idA)
	desktopConnect(t, srv.URL, tokenB, idB, "same-name", true)
	desktopWait(t, fx.hub, idB)
	for _, id := range []string{idA, idB} {
		d, err := db.GetDesktop(t.Context(), fx.pool, id)
		if err != nil || d.ID != id || d.OwnerID == "" {
			t.Fatalf("identity: %+v %v", d, err)
		}
	}
	desktopConnect(t, srv.URL, tokenA, idB, "same-name", false)
	desktopConnect(t, srv.URL, bad, idB, "same-name", false)
	desktopConnect(t, srv.URL, spawnTokenDaemonTok, idA, "same-name", false)
	admin := fx.tok(t, "human", privAcctA, "session.start", "account.manage")
	// A human administrator may assign a known desktop, but cannot mint its
	// current owner's credential while acting as a different person.
	credentialRequest := func(id, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/desktop-ownership/"+id+"/credential", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if w := credentialRequest(idB, admin); w.Code != 404 {
		t.Fatalf("admin minted another person's credential: %d", w.Code)
	}
	if w := credentialRequest(idA, admin); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("owner enrollment: %d", w.Code)
	}
	request := func(tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/desktop-ownership/"+idA, strings.NewReader(fmt.Sprintf(`{"owner_id":%q,"ownership_revision":1}`, privAcctB)))
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if w := request(fx.tok(t, "human", privAcctA, "session.start")); w.Code != 403 {
		t.Fatalf("member assignment: %d %s", w.Code, w.Body.String())
	}
	if w := request(admin); w.Code != 200 {
		t.Fatalf("assignment: %d %s", w.Code, w.Body.String())
	}
	_ = a.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := a.ReadMessage(); err == nil {
		t.Fatal("old connection survived reassignment")
	}
	if fx.hub.GetDaemon(idA) != nil {
		t.Fatal("old connection remained routable")
	}
	if db.ValidateDesktopConnection(t.Context(), fx.pool, *dcA.DesktopBinding, dcA.DesktopGeneration) == nil {
		t.Fatal("stale queued delivery remained authorized")
	}
	checkCredential(tokenA, 401)
	desktopConnect(t, srv.URL, tokenA, idA, "same-name", false)
	if w := request(admin); w.Code != 409 {
		t.Fatalf("stale assignment: %d", w.Code)
	}
	desktopConnect(t, srv.URL, fresh, idA, "same-name", true)
	next := desktopWait(t, fx.hub, idA)
	if next.DesktopBinding.OwnerID != privAcctB {
		t.Fatal("new owner's credential refused")
	}

}
func TestDesktopBootstrapMigratesExistingIDAndUnknownInventoryIsAdminOnly(t *testing.T) {
	fx := newPrivFixture(t)
	fx.dc.LegacyDesktop = true
	fx.dc.DesktopBinding = nil
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=NULL,connection_generation=NULL WHERE id=$1`, fx.dc.ID); err != nil {
		t.Fatal(err)
	}
	desktopFakeCore(t, fx.api, map[string]desktopauth.Binding{})
	mux := http.NewServeMux()
	fx.api.RegisterDesktopRoutes(mux)
	admin := fx.tok(t, "human", privAcctA, "session.start", "account.manage")
	for _, c := range []struct {
		token string
		want  int
	}{{fx.tok(t, "human", privAcctB, "session.start"), 403}, {admin, 200}} {
		req := httptest.NewRequest(http.MethodGet, "/api/desktop-ownership", nil)
		req.Header.Set("Authorization", "Bearer "+c.token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Fatalf("inventory: %d %s", w.Code, w.Body.String())
		}
		if c.want == 200 && !strings.Contains(w.Body.String(), fx.dc.ID) {
			t.Fatal("admin cannot see unknown desktop")
		}
	}
	if fx.hub.GetHostDaemon(fx.dc.ID) != nil {
		t.Fatal("unknown legacy desktop is a launch target")
	}
	boot := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/internal/desktops/bootstrap", strings.NewReader(`{"name":"laptop"}`))
		req.Header.Set("X-Internal-Key", key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if w := boot(spawnTokenDaemonTok); w.Code != 401 {
		t.Fatalf("master enrolled a desktop: %d", w.Code)
	}
	w := boot("fixture-internal-key")
	if w.Code != 200 {
		t.Fatalf("bootstrap: %d %s", w.Code, w.Body.String())
	}
	d, err := db.GetDesktop(t.Context(), fx.pool, fx.dc.ID)
	if err != nil || d.OwnerID != privAcctA || d.Revision != 2 {
		t.Fatalf("migration: %+v %v", d, err)
	}
	session, err := db.GetSession(t.Context(), fx.pool, fx.pubSession)
	if err != nil || session.DaemonID != fx.dc.ID {
		t.Fatal("upgrade replaced the desktop or its session association")
	}
	if strings.Contains(w.Header().Get("Cache-Control"), "no-store") == false {
		t.Fatal("credential response can be cached")
	}
}

func TestDesktopReassignmentFencesQueuedDeliveryAcrossReplicas(t *testing.T) {
	t.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", "false")
	fx := newPrivFixture(t)
	id := newUUID()
	raw := "bdt_" + strings.Repeat("f", 64)
	binding := desktopauth.Binding{DesktopID: id, OwnerID: privAcctA, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
	desktopFakeCore(t, fx.api, map[string]desktopauth.Binding{raw: binding})
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", fx.hub.ServeDaemon(spawnTokenDaemonTok, fx.pool))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	conn := desktopConnect(t, server.URL, raw, id, "workstation", true)
	old := desktopWait(t, fx.hub, id)
	// A second runner replica has the same DB but cannot close this local socket.
	// Its persisted revision must still fence this replica's already-queued work.
	second := NewAPI(NewHub(), fx.pool, spawnTokenDaemonTok, nil, "")
	second.coreAuth = fx.api.coreAuth
	second.coreURL, second.coreInternalKey = fx.api.coreURL, fx.api.coreInternalKey
	routes := http.NewServeMux()
	second.RegisterDesktopRoutes(routes)
	req := httptest.NewRequest(http.MethodPut, "/api/desktop-ownership/"+id, strings.NewReader(fmt.Sprintf(`{"owner_id":%q,"ownership_revision":1}`, privAcctB)))
	req.Header.Set("Authorization", "Bearer "+fx.tok(t, "human", privAcctA, "session.start", "account.manage"))
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatalf("remote assignment: %d %s", response.Code, response.Body.String())
	}
	old.send <- []byte(`{"type":"set_repos_root","root":"/must-not-be-delivered"}`)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, payload, err := conn.ReadMessage(); err == nil {
		t.Fatalf("old queued authority delivered: %s", payload)
	} else {
		var timeout interface{ Timeout() bool }
		if errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatal("old connection was not fenced; read timed out")
		}
	}
}

func TestDesktopCredentialsDoNotTransferOldSessions(t *testing.T) {
	fx := newPrivFixture(t)
	// The known desktop now belongs to B, but the preexisting session belongs to A.
	binding := desktopauth.Binding{DesktopID: fx.dc.ID, OwnerID: privAcctB, Revision: 2, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
	fx.dc.DesktopBinding = &binding
	if _, err := db.AssignDesktop(t.Context(), fx.pool, fx.dc.ID, privAcctB, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET started_at=now()-interval '10 minutes' WHERE id=$1`, fx.pubSession); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileSessionsQueuedChecked(t.Context(), fx.hub, fx.pool, fx.dc.ID, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	prior, err := db.GetSession(t.Context(), fx.pool, fx.pubSession)
	if err != nil || prior.Status != "running" {
		t.Fatal("new owner's recovery changed an old session", err)
	}
	raw, _ := json.Marshal(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: fx.pubSession, Status: "idle"})
	if handleDaemonFrame(t.Context(), fx.hub, fx.dc, fx.pool, raw, envelope{Type: "session_state_changed", SessionID: fx.pubSession}) {
		t.Fatal("new desktop owner could mutate the previous owner's session")
	}
	if allowed, err := db.DesktopSessionIDs(t.Context(), fx.pool, fx.dc.ID, privAcctB); err != nil || allowed[fx.pubSession] {
		t.Fatal("old session entered new owner's recovery inventory", err)
	}
}
