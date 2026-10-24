package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type browseFx struct {
	*topicFx
	dc *DaemonConn
}

func newBrowseFx(t *testing.T, engines ...string) *browseFx {
	t.Helper()
	fx := newTopicFx(t)
	fx.api.RegisterPluginBrowseRoutes(fx.mux)
	fx.api.sessionPlugins = &pluginCoreFixture{}
	fx.api.pluginAllow, _ = pluginspec.ParseAllowlist("")
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 8)}
	dc.SetExtensionEngines(engines, engines)
	fixtureOwnDesktop(t, fx.pool, dc, topicOwner)
	fx.api.hub.Register(dc)
	for _, sid := range []string{fx.sess, fx.privSess, fx.ended} {
		fx.api.hub.SetSessionOwner(sid, dc.ID)
	}
	return &browseFx{fx, dc}
}

func (fx *browseFx) do(method, path, token string, body any) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec
}

// refused expects an answer with nothing sent to the daemon.
func (fx *browseFx) refused(method, path, token string, body any) *httptest.ResponseRecorder {
	fx.t.Helper()
	rec := fx.do(method, path, token, body)
	select {
	case raw := <-fx.dc.send:
		fx.t.Fatalf("a refused request reached the daemon: %s", raw)
	default:
	}
	return rec
}

// answered sends the request and lets the daemon reply with reply(raw request).
func (fx *browseFx) answered(method, path, token string, body any, reply func(raw []byte) any) (*httptest.ResponseRecorder, []byte) {
	fx.t.Helper()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var rd *bytes.Reader
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		fx.mux.ServeHTTP(rec, req)
	}()
	var sent []byte
	select {
	case sent = <-fx.dc.send:
	case <-time.After(15 * time.Second):
		fx.t.Fatal("request not dispatched")
	}
	var head struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(sent, &head)
	res := reply(sent)
	raw, _ := json.Marshal(res)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["request_id"], m["session_id"] = head.RequestID, head.SessionID
	raw, _ = json.Marshal(m)
	typ := "plugin_catalog_result"
	if head.Type == "mcp_sign_in" {
		typ = "mcp_sign_in_result"
	}
	fx.api.hub.deliverExtResult(fx.dc, typ, raw)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		fx.t.Fatal("response did not complete")
	}
	return rec, sent
}

func okCatalog([]byte) any {
	return protocol.PluginCatalogResult{OK: true,
		Marketplaces: []protocol.CatalogMarketplace{{Name: "fixture-market", Source: "example-org/plugins"}, {Name: "-bad"}},
		Plugins: []protocol.CatalogPlugin{
			{Name: "release-notes", Marketplace: "fixture-market", Description: "Writes notes.\n<script>"},
			{Name: "../escape", Marketplace: "fixture-market"},
		}}
}

func TestPluginCatalogOwnerOnly(t *testing.T) {
	fx := newBrowseFx(t, "claude")
	url := "/api/sessions/" + fx.sess + "/plugins/catalog"
	owner := fx.token(topicOwner, "human")
	other := fx.token(topicOther, "human")
	admin := fx.token(topicOther, "human", "account.manage")

	rec, sent := fx.answered(http.MethodGet, url, owner, nil, okCatalog)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("owner %d %s", rec.Code, rec.Body)
	}
	var q protocol.ReadPluginCatalog
	if json.Unmarshal(sent, &q) != nil || q.Type != "read_plugin_catalog" || q.SessionID != fx.sess || q.AddMarketplace != "" {
		t.Fatalf("request %s", sent)
	}
	var got struct {
		Engine       string                        `json:"engine"`
		Marketplaces []protocol.CatalogMarketplace `json:"marketplaces"`
		Plugins      []protocol.CatalogPlugin      `json:"plugins"`
		Truncated    bool                          `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Engine != "claude" || len(got.Marketplaces) != 1 || len(got.Plugins) != 1 || got.Plugins[0].Description != "Writes notes. <script>" || !got.Truncated {
		t.Fatalf("reply not re-validated: %s", rec.Body)
	}
	// The catalog is the owner's machine's inventory: an administrator does not read it either.
	if rec := fx.refused(http.MethodGet, url, admin, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("administrator read: %d", rec.Code)
	}
	if rec := fx.refused(http.MethodGet, url, other, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("another person: %d", rec.Code)
	}
	priv := fx.refused(http.MethodGet, "/api/sessions/"+fx.privSess+"/plugins/catalog", fx.token(topicOwner, "human", "account.manage"), nil)
	unknown := fx.refused(http.MethodGet, "/api/sessions/"+newUUID()+"/plugins/catalog", owner, nil)
	if priv.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound {
		t.Fatalf("private %d unknown %d", priv.Code, unknown.Code)
	}
	for _, tok := range []string{fx.token(topicOwner, "agent"), fx.token(topicOwner, "service")} {
		if rec := fx.refused(http.MethodGet, url, tok, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("non-person token: %d", rec.Code)
		}
	}
	if rec := fx.refused(http.MethodGet, url, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := fx.refused(http.MethodGet, "/api/sessions/"+fx.ended+"/plugins/catalog", owner, nil); rec.Code != http.StatusConflict {
		t.Fatalf("ended session: %d", rec.Code)
	}
}

func TestPluginCatalogFallsBackForEnginesWithoutAProvider(t *testing.T) {
	fx := newBrowseFx(t) // the daemon advertises no engine
	rec := fx.refused(http.MethodGet, "/api/sessions/"+fx.sess+"/plugins/catalog", fx.token(topicOwner, "human"), nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"unsupported"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	fx2 := newBrowseFx(t, "claude")
	if _, err := fx2.pool.Exec(context.Background(), `UPDATE sessions SET runtime = 'docker' WHERE id = $1`, fx2.sess); err != nil {
		t.Fatal(err)
	}
	rec = fx2.refused(http.MethodPost, "/api/sessions/"+fx2.sess+"/mcp/sign-in", fx2.token(topicOwner, "human"), map[string]string{"server": "tracker"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"sandboxed"`) {
		t.Fatalf("sandboxed: %d %s", rec.Code, rec.Body)
	}
}

func TestAddMarketplaceOwnerOnlyAndValidated(t *testing.T) {
	fx := newBrowseFx(t, "claude")
	url := "/api/sessions/" + fx.sess + "/plugins/marketplaces"
	owner := fx.token(topicOwner, "human")
	if rec := fx.refused(http.MethodPost, url, fx.token(topicOther, "human", "account.manage"), map[string]string{"source": pluginspec.DefaultMarketplaces}); rec.Code != http.StatusForbidden {
		t.Fatalf("administrator add: %d", rec.Code)
	}
	for _, bad := range []string{"https://gitlab.com/a/b", "git@github.com:a/b.git", "-x/y", "file:///etc"} {
		if rec := fx.refused(http.MethodPost, url, owner, map[string]string{"source": bad}); rec.Code != http.StatusBadRequest {
			t.Fatalf("%q: %d", bad, rec.Code)
		}
	}
	if rec := fx.refused(http.MethodPost, url, owner, map[string]string{"source": "someone/else"}); rec.Code != http.StatusForbidden {
		t.Fatalf("not allow-listed: %d", rec.Code)
	}
	rec, sent := fx.answered(http.MethodPost, url, owner, map[string]string{"source": "https://github.com/" + pluginspec.DefaultMarketplaces + ".git"}, okCatalog)
	var q protocol.ReadPluginCatalog
	if rec.Code != http.StatusOK || json.Unmarshal(sent, &q) != nil || q.AddMarketplace != pluginspec.DefaultMarketplaces {
		t.Fatalf("%d %s / %s", rec.Code, rec.Body, sent)
	}
	rec, _ = fx.answered(http.MethodPost, url, owner, map[string]string{"source": pluginspec.DefaultMarketplaces}, func([]byte) any {
		return protocol.PluginCatalogResult{ErrorCode: "add_failed"}
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("add failed: %d", rec.Code)
	}
}

func TestMCPSignInOwnerOnlyAndHTTPSURLsOnly(t *testing.T) {
	fx := newBrowseFx(t, "claude")
	url := "/api/sessions/" + fx.sess + "/mcp/sign-in"
	owner := fx.token(topicOwner, "human")
	body := map[string]string{"server": "tracker"}
	if rec := fx.refused(http.MethodPost, url, fx.token(topicOther, "human", "account.manage"), body); rec.Code != http.StatusForbidden {
		t.Fatalf("administrator sign-in: %d", rec.Code)
	}
	if rec := fx.refused(http.MethodPost, url, owner, map[string]string{"server": "bad\nname"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid name: %d", rec.Code)
	}
	rec, sent := fx.answered(http.MethodPost, url, owner, body, func([]byte) any {
		return protocol.MCPSignInResult{OK: true, AuthURL: "https://auth.example/authorize?state=s", RequiresUserAction: true, CallbackExpected: true, Source: "user"}
	})
	var q protocol.MCPSignIn
	if rec.Code != http.StatusOK || json.Unmarshal(sent, &q) != nil || q.Type != "mcp_sign_in" || q.Server != "tracker" {
		t.Fatalf("%d %s / %s", rec.Code, rec.Body, sent)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["auth_url"] != "https://auth.example/authorize?state=s" || got["callback_expected"] != true || got["source"] != "user" {
		t.Fatalf("reply %s", rec.Body)
	}
	for _, res := range []protocol.MCPSignInResult{
		{OK: true, AuthURL: "http://auth.example/authorize", RequiresUserAction: true},
		{OK: true, AuthURL: "javascript:alert(1)", RequiresUserAction: true},
		{OK: true, RequiresUserAction: true},
	} {
		rec, _ := fx.answered(http.MethodPost, url, owner, body, func([]byte) any { return res })
		if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "auth.example") || strings.Contains(rec.Body.String(), "javascript") {
			t.Fatalf("%+v: %d %s", res, rec.Code, rec.Body)
		}
	}
	rec, _ = fx.answered(http.MethodPost, url, owner, body, func([]byte) any { return protocol.MCPSignInResult{ErrorCode: "connector"} })
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"connector"`) {
		t.Fatalf("connector: %d %s", rec.Code, rec.Body)
	}
	// A server the workspace defined: refused with no link, signed in to from a terminal.
	rec, _ = fx.answered(http.MethodPost, url, owner, body, func([]byte) any { return protocol.MCPSignInResult{ErrorCode: "untrusted"} })
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"untrusted"`) || strings.Contains(rec.Body.String(), "auth_url") {
		t.Fatalf("untrusted: %d %s", rec.Code, rec.Body)
	}
	// R4: only a daemon that says "user" gets a link through: any other source, and an old-shaped
	// reply with no source at all (a daemon from before the trust check), is untrusted.
	for _, src := range []string{"<b>project</b>", "project", ""} {
		rec, _ = fx.answered(http.MethodPost, url, owner, body, func([]byte) any {
			return protocol.MCPSignInResult{OK: true, AuthURL: "https://auth.example/a", RequiresUserAction: true, CallbackExpected: true, Source: src}
		})
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"untrusted"`) || strings.Contains(rec.Body.String(), "auth.example") {
			t.Fatalf("source %q: %d %s", src, rec.Code, rec.Body)
		}
	}
	rec, _ = fx.answered(http.MethodPost, url, owner, body, func([]byte) any {
		return map[string]any{"type": "mcp_sign_in_result", "ok": true, "auth_url": "https://auth.example/a", "requires_user_action": true, "callback_expected": true}
	})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"untrusted"`) {
		t.Fatalf("old-shaped reply: %d %s", rec.Code, rec.Body)
	}
}

func TestExtResultOnlyFromTheDaemonAskedOnce(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	done := make(chan error, 1)
	go func() {
		_, err := h.requestExt(t.Context(), dc, "acct", "session", "mcp_sign_in_result", func(id string) any {
			return protocol.MCPSignIn{Type: "mcp_sign_in", RequestID: id, SessionID: "session", Server: "x"}
		}, 30*time.Second)
		done <- err
	}()
	var req protocol.MCPSignIn
	if err := json.Unmarshal(<-dc.send, &req); err != nil {
		t.Fatal(err)
	}
	reply, _ := json.Marshal(protocol.MCPSignInResult{RequestID: req.RequestID, SessionID: "session", OK: true})
	h.deliverExtResult(&DaemonConn{ID: "other"}, "mcp_sign_in_result", reply) // another daemon
	h.deliverExtResult(dc, "plugin_catalog_result", reply)                    // another type
	foreign, _ := json.Marshal(protocol.MCPSignInResult{RequestID: req.RequestID, SessionID: "foreign", OK: true})
	h.deliverExtResult(dc, "mcp_sign_in_result", foreign) // another session
	select {
	case err := <-done:
		t.Fatalf("forged reply accepted: %v", err)
	default:
	}
	h.deliverExtResult(dc, "mcp_sign_in_result", reply)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Single use: a second delivery of the consumed reply finds no request and blocks nothing.
	delivered := make(chan struct{})
	go func() { h.deliverExtResult(dc, "mcp_sign_in_result", reply); close(delivered) }()
	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("a second delivery blocked")
	}
	h.extReqs.mu.Lock()
	defer h.extReqs.mu.Unlock()
	if len(h.extReqs.pending) != 0 {
		t.Fatal("pending request leaked")
	}
}

// M2: a person signed in to the browser, acting for themselves: no browser session id, or a token
// minted on someone's behalf, is refused on every route before any daemon is asked.
func TestPluginBrowseNeedsABrowserSessionAndNoOnBehalfOf(t *testing.T) {
	fx := newBrowseFx(t, "claude")
	mint := func(sid, obo string) string {
		return mintRunnerToken(t, fx.priv, "core-1", identity.Claims{
			Sub: topicOwner, Aud: coreAuthAudience, Kind: "human", Sid: sid, OnBehalfOf: obo,
			Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Minute).Unix(),
		})
	}
	for name, tok := range map[string]string{"no sid": mint("", ""), "on behalf of": mint(testSID, "someone-else")} {
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{http.MethodGet, "/api/sessions/" + fx.sess + "/plugins/catalog", nil},
			{http.MethodPost, "/api/sessions/" + fx.sess + "/plugins/marketplaces", map[string]string{"source": pluginspec.DefaultMarketplaces}},
			{http.MethodPost, "/api/sessions/" + fx.sess + "/mcp/sign-in", map[string]string{"server": "tracker"}},
		} {
			if rec := fx.refused(c.method, c.path, tok, c.body); rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s %s: %d", name, c.method, c.path, rec.Code)
			}
		}
	}
}

// R3: where an engine keeps its own configuration ("user scope") cannot be set by a start's custom
// variables, for any session.
func TestStartRefusesEngineHomeVariables(t *testing.T) {
	a := NewAPI(NewHub(), nil, "fixture-daemon", nil, "")
	for _, k := range protocol.ReservedEngineHomeEnv {
		_, problem := a.StartSession(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind},
			runnerStartRequest{NoRepo: true, Engine: "claude", Env: map[string]string{k: "/tmp/elsewhere"}}, "")
		if problem == nil || problem.Status != http.StatusUnprocessableEntity || !strings.Contains(problem.Message, "reserved") {
			t.Fatalf("%s: %+v", k, problem)
		}
	}
}
