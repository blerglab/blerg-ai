package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// A browser refresh is a new socket. Asking for replay, it first receives one
// replay notice and exactly the kept bytes (including output produced while no
// socket was attached), then live output with nothing repeated. A page that
// does not ask gets live output only.
func TestShellSocketReplaysScrollbackOnRefresh(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := NewAPI(NewHub(), nil, "", nil, "")
	a.coreAuth = newTestCoreAuthClient(t, pub, "shell-key")
	tok := mintTestToken(t, key, "shell-key", identity.Claims{Kind: "human", Sub: "owner", Aud: coreAuthAudience, Sid: "login", Caps: []string{coreAuthBrowserCap, "account.manage"}, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	rt := &shellTestRuntime{binding: shellTestBinding(), out: make(chan []byte, 8), input: make(chan []byte, 8)}
	m, err := ownershell.New(shellTestAuthority{}, rt, rt, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.shells = m
	m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	t.Cleanup(func() { m.Unavailable(context.Background()) })
	mux := http.NewServeMux()
	a.RegisterShellRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	req := httptest.NewRequest("POST", server.URL+"/api/sessions/session/shells", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", server.URL)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var info ownershell.Info
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &info) != nil {
		t.Fatal(rec.Code)
	}
	dial := func(query string) *websocket.Conn {
		d := websocket.Dialer{Subprotocols: []string{"bearer", tok}}
		conn, res, err := d.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/api/shells/"+info.Handle+"/socket"+query, http.Header{"Origin": []string{server.URL}})
		if res != nil {
			_ = res.Body.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		return conn
	}
	first := dial("?replay=1")
	notice := func(conn *websocket.Conn) (int, uint64) {
		var n struct {
			Type  string
			Bytes int
			Seq   uint64
		}
		if err := conn.ReadJSON(&n); err != nil || n.Type != "replay" {
			t.Fatal("no replay notice", err, n.Type)
		}
		return n.Bytes, n.Seq
	}
	if n, seq := notice(first); n != 0 || seq != 0 {
		t.Fatal("a new shell replayed something")
	}
	rt.out <- []byte("before refresh\r\n")
	if _, b, err := first.ReadMessage(); err != nil || string(b) != "before refresh\r\n" {
		t.Fatal("live output", string(b), err)
	}
	_ = first.Close() // the page reloads
	waitUntil(t, "socket not detached", func() bool {
		infos, err := m.List(t.Context(), ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"}, "session")
		return err == nil && len(infos) == 1 && !infos[0].Attached
	})
	rt.out <- []byte("while away\r\n")
	time.Sleep(100 * time.Millisecond)

	second := dial("?replay=1")
	defer second.Close()
	n, seq := notice(second)
	var replay []byte
	for len(replay) < n {
		kind, b, err := second.ReadMessage()
		if err != nil || kind != websocket.BinaryMessage {
			t.Fatal("replay bytes", err)
		}
		replay = append(replay, b...)
	}
	if string(replay) != "before refresh\r\nwhile away\r\n" || seq != uint64(len(replay)) {
		t.Fatalf("replay %q seq %d", replay, seq)
	}
	rt.out <- []byte("live again")
	if _, b, err := second.ReadMessage(); err != nil || string(b) != "live again" {
		t.Fatal("live output after replay", string(b), err)
	}
	_ = second.Close()

	legacy := dial("?takeover=true")
	defer legacy.Close()
	rt.out <- []byte("only live")
	if kind, b, err := legacy.ReadMessage(); err != nil || kind != websocket.BinaryMessage || string(b) != "only live" {
		t.Fatal("a page that did not ask for replay got it", string(b), err)
	}
}
