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
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestShellRenameAndDetailsHTTPIsolation(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := NewAPI(NewHub(), nil, "", nil, "")
	a.coreAuth = newTestCoreAuthClient(t, pub, "shell-key")
	rt := &shellTestRuntime{binding: shellTestBinding(), out: make(chan []byte, 8), input: make(chan []byte, 8)}
	m, err := ownershell.New(shellTestAuthority{}, rt, rt, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.shells = m
	m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	t.Cleanup(func() { m.Unavailable(context.Background()) })
	actor := ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"}
	info, err := m.Open(t.Context(), actor, "session")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.RegisterShellRoutes(mux)
	request := func(method, path, login, origin, body string) *httptest.ResponseRecorder {
		token := mintTestToken(t, key, "shell-key", identity.Claims{Kind: "human", Sub: "owner", Aud: coreAuthAudience, Sid: login, Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(method, "http://example.com/api/shells/"+info.Handle+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	for _, test := range []struct {
		login, origin, body string
		code                int
	}{
		{"login", "http://example.com", `{"name":"Test watcher"}`, 200},
		{"other", "http://example.com", `{"name":"stolen"}`, 403},
		{"login", "https://foreign.example", `{"name":"stolen"}`, 403},
		{"login", "http://example.com", `{"name":"test","env":"TOKEN_SECRET"}`, 400},
		{"login", "http://example.com", `{"name":"bad\nname"}`, 400},
	} {
		if res := request("PATCH", "", test.login, test.origin, test.body); res.Code != test.code {
			t.Fatalf("rename: %d %s", res.Code, res.Body.String())
		}
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()
	socketToken := mintTestToken(t, key, "shell-key", identity.Claims{Kind: "human", Sub: "owner", Aud: coreAuthAudience, Sid: "login", Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	dial := websocket.Dialer{Subprotocols: []string{"bearer", socketToken}}
	first, upgrade, err := dial.DialContext(t.Context(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/shells/"+info.Handle+"/socket", http.Header{"Origin": []string{srv.URL}})
	if upgrade != nil {
		defer upgrade.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, upgrade, err := dial.DialContext(t.Context(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/shells/"+info.Handle+"/socket?takeover=true", http.Header{"Origin": []string{srv.URL}, "User-Agent": []string{"Browser Windows"}})
	if upgrade != nil {
		defer upgrade.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := first.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var handoff map[string]string
	if err := first.ReadJSON(&handoff); err != nil || handoff["type"] != "control" || handoff["reason"] != "taken_over" || handoff["device"] != "Windows" || len(handoff) != 3 {
		t.Fatalf("handoff: %v %v", handoff, err)
	}
	if err := m.Close(t.Context(), actor, info.Handle); err != nil {
		t.Fatal(err)
	}
	res := request("GET", "/details", "login", "", "")
	var details map[string]string
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &details) != nil || details["reason"] != "requested" || len(details) != 1 {
		t.Fatalf("details: %d %s", res.Code, res.Body.String())
	}
	if res = request("GET", "/details", "other", "", ""); res.Code != 404 {
		t.Fatal("details leaked", res.Code)
	}
}

func TestShellCloseDeletesStoredName(t *testing.T) {
	a, pool := runnerTestAPI(t)
	const desktop = "00000000-0000-0000-0000-000000000001"
	const session = "00000000-0000-0000-0000-000000000002"
	if err := db.UpsertDaemon(t.Context(), pool, desktop, "test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(t.Context(), pool, session, desktop, "running", "/repos/one", "org/one", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, end := range []string{"close", "revoke", "unavailable", "shutdown", "rename_race"} {
		t.Run(end, func(t *testing.T) {
			rt := &shellTestRuntime{binding: shellTestBinding(), out: make(chan []byte, 8), input: make(chan []byte, 8)}
			rt.binding.Session = session
			m, err := ownershell.New(shellTestAuthority{}, rt, rt, shellAuditLog{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			m.SetNameCleanup(a.forgetShellName)
			m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
			actor := ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"}
			info, err := m.Open(t.Context(), actor, session)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = m.Rename(t.Context(), actor, info.Handle, "mistaken paste", a.persistShellName(actor)); err != nil {
				t.Fatal(err)
			}
			var renamed chan error
			var finishRename chan struct{}
			if end == "rename_race" {
				started := make(chan struct{})
				finishRename = make(chan struct{})
				renamed = make(chan error, 1)
				go func() {
					_, err := m.Rename(t.Context(), actor, info.Handle, "late paste", func(ctx context.Context, next ownershell.Info) error {
						if next.Name != "" {
							close(started)
							<-finishRename
						}
						// Model a write whose commit raced the request cancellation.
						return a.persistShellName(actor)(context.WithoutCancel(ctx), next)
					})
					renamed <- err
				}()
				<-started
			}
			switch end {
			case "close":
				if err = m.Close(t.Context(), actor, info.Handle); err != nil {
					t.Fatal(err)
				}
			case "revoke":
				m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 2, Enabled: false})
			case "unavailable", "rename_race":
				m.Unavailable(t.Context())
			case "shutdown":
				m.Shutdown(t.Context())
			}
			if end == "rename_race" {
				close(finishRename)
				if err := <-renamed; err == nil {
					t.Fatal("rename succeeded after revocation")
				}
			}
			names, err := db.ShellNames(t.Context(), pool, session, actor.Owner, actor.Login)
			if err != nil || len(names) != 0 {
				t.Fatalf("name retained after close: %v %v", names, err)
			}
		})
	}
}
