package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// call sends a request through a mux registered exactly as cmd/server/main.go does, so the
// table exercises the real method and pattern matching.
func (fx *mcpFx) call(method, path, bearer, body string) *httptest.ResponseRecorder {
	fx.t.Helper()
	mux := http.NewServeMux()
	fx.api.RegisterMCPRoutes(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The route group's table: no token 401, an agent token (even with session.start) 403, a
// human token without a login session 401, another account's connection 404.
func TestMCPToolRoutesAuthMatrix(t *testing.T) {
	fx := newMCPFx(t)
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/mcp/connections", ""},
		{http.MethodGet, "/api/mcp/connections/" + fx.connID + "/tools", ""},
		{http.MethodPatch, "/api/mcp/connections/" + fx.connID + "/defaults", `{"default_tools":{}}`},
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			if rec := fx.call(r.method, r.path, "", r.body); rec.Code != http.StatusUnauthorized {
				t.Errorf("no token: %d, want 401", rec.Code)
			}
			if rec := fx.call(r.method, r.path, "garbage", r.body); rec.Code != http.StatusUnauthorized {
				t.Errorf("bad token: %d, want 401", rec.Code)
			}
			if rec := fx.call(r.method, r.path, fx.token("agent", ""), r.body); rec.Code != http.StatusForbidden {
				t.Errorf("agent token with session.start: %d, want 403", rec.Code)
			}
			if rec := fx.call(r.method, r.path, fx.token("human", ""), r.body); rec.Code != http.StatusUnauthorized {
				t.Errorf("human token without a login session: %d, want 401", rec.Code)
			}
		})
	}
	if n := fx.up.listCount(); n != 0 {
		t.Errorf("refused requests reached the upstream %d times", n)
	}
	for _, r := range routes[1:] {
		other := strings.Replace(r.path, fx.connID, newUUID(), 1)
		if rec := fx.call(r.method, other, fx.human(), r.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s of another account's connection: %d, want 404", r.method, other, rec.Code)
		}
	}
	// The caller's own proof reached core, never anyone else's.
	for _, p := range fx.list.proofs {
		if p.AccountID != mcpAcct || p.SessionID != testSID || p.TokenID != "" {
			t.Errorf("core was asked with proof %+v", p)
		}
	}
	if n := fx.up.listCount(); n != 0 {
		t.Errorf("a foreign connection reached the upstream %d times", n)
	}
}

func TestMCPConnectionsListing(t *testing.T) {
	fx := newMCPFx(t)
	rec := fx.call(http.MethodGet, "/api/mcp/connections", fx.human(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Connections []struct {
			ID           string                     `json:"id"`
			Name         string                     `json:"name"`
			URL          string                     `json:"url"`
			Status       string                     `json:"status"`
			DefaultTools map[string]mcpgw.ToolGrant `json:"default_tools"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Connections) != 1 {
		t.Fatalf("body %s (%v)", rec.Body.String(), err)
	}
	c := out.Connections[0]
	if c.ID != fx.connID || c.Name != mcpConn || c.URL != fx.up.url() || c.Status != "ok" || c.DefaultTools["echo"].Mode != "allow" {
		t.Errorf("connection = %+v", c)
	}

	fx.list.mu.Lock()
	fx.list.conns = nil
	fx.list.mu.Unlock()
	rec = fx.call(http.MethodGet, "/api/mcp/connections", fx.human(), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"connections":[]`) {
		t.Errorf("empty list: %d %s, want 200 with []", rec.Code, rec.Body.String())
	}

	fx.list.err = ErrMCPProofInvalid
	if rec := fx.call(http.MethodGet, "/api/mcp/connections", fx.human(), ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("proof no longer live: %d, want 401", rec.Code)
	}
	fx.list.err = errors.New("dial tcp 192.0.2.9:443: refused")
	rec = fx.call(http.MethodGet, "/api/mcp/connections", fx.human(), "")
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "192.0.2.9") {
		t.Errorf("core failure: %d %s, want a clean 502", rec.Code, rec.Body.String())
	}
	fx.api.setMCPStart(&mcpStartConfig{GatewayURL: mcpGatewayURL})
	if rec := fx.call(http.MethodGet, "/api/mcp/connections", fx.human(), ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("core not configured: %d, want 503", rec.Code)
	}
}

func TestMCPConnectionToolsListing(t *testing.T) {
	fx := newMCPFx(t)
	fx.up.mu.Lock()
	fx.up.tools[0].Annotations = `{"readOnlyHint":true,"title":"Echo"}`
	fx.up.mu.Unlock()

	rec := fx.call(http.MethodGet, "/api/mcp/connections/"+fx.connID+"/tools", fx.human(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{"upstream-secret", "X-Api-Key", "sess-1"} {
		if strings.Contains(body, secret) {
			t.Errorf("the response leaks %q: %s", secret, body)
		}
	}
	var out struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
			Annotations map[string]any  `json:"annotations"`
			Hash        string          `json:"hash"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Tools) != 2 {
		t.Fatalf("body %s (%v)", body, err)
	}
	echo, send := out.Tools[0], out.Tools[1]
	if echo.Name != "echo" || echo.Description != "echoes" || echo.Hash != fx.up.hash("echo") || echo.Annotations["readOnlyHint"] != true ||
		!strings.Contains(string(echo.InputSchema), `"msg"`) {
		t.Errorf("echo = %+v", echo)
	}
	if send.Name != "send" || send.Hash != fx.up.hash("send") || send.Annotations != nil {
		t.Errorf("send = %+v", send)
	}
	// Nothing was stored: no grant row came of listing.
	if fx.count("session_mcp_grants") != 0 {
		t.Error("listing tools created a grant")
	}
}

func TestMCPConnectionToolsFailures(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(fx *mcpFx)
		status int
	}{
		{"upstream 500", func(fx *mcpFx) { fx.up.mu.Lock(); fx.up.status = 500; fx.up.mu.Unlock() }, http.StatusBadGateway},
		{"credential gone", func(fx *mcpFx) { fx.tokens.err = mcpgw.ErrConnectionGone }, http.StatusNotFound},
		{"core credential error", func(fx *mcpFx) { fx.tokens.err = errors.New("core unreachable: dial tcp 192.0.2.3:8080") }, http.StatusBadGateway},
		{"connection needs sign in", func(fx *mcpFx) { fx.list.conns[0].Status = "needs_auth" }, http.StatusConflict},
		{"gateway not running", func(fx *mcpFx) { fx.api.setMCPStart(&mcpStartConfig{GatewayURL: mcpGatewayURL, Core: fx.list}) }, http.StatusServiceUnavailable},
		{"upstream url refused by netguard", func(fx *mcpFx) {
			fx.tokens.url = "http://169.254.169.254/mcp"
			fx.list.conns[0].URL = "http://169.254.169.254/mcp"
		}, http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newMCPFx(t)
			tc.setup(fx)
			rec := fx.call(http.MethodGet, "/api/mcp/connections/"+fx.connID+"/tools", fx.human(), "")
			if rec.Code != tc.status {
				t.Fatalf("%d %s, want %d", rec.Code, rec.Body.String(), tc.status)
			}
			for _, leak := range []string{"upstream-secret", "192.0.2.3", "127.0.0.1", "169.254", "X-Api-Key"} {
				if strings.Contains(rec.Body.String(), leak) {
					t.Errorf("the error leaks %q: %s", leak, rec.Body.String())
				}
			}
		})
	}
}

// PATCH .../defaults forwards the caller's login session (never a token) and the tool map to
// core, after the same person and ownership checks as the other routes.
func TestMCPDefaultsSave(t *testing.T) {
	path := func(fx *mcpFx) string { return "/api/mcp/connections/" + fx.connID + "/defaults" }
	good := `{"default_tools":{"echo":{"mode":"allow","hash":"h1"},"send":{"mode":"propose","hash":"h2"}}}`

	t.Run("saves through core with the caller's proof", func(t *testing.T) {
		fx := newMCPFx(t)
		rec := fx.call(http.MethodPatch, path(fx), fx.human(), good)
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		fx.list.mu.Lock()
		defer fx.list.mu.Unlock()
		if len(fx.list.saved) != 1 {
			t.Fatalf("core saw %d saves", len(fx.list.saved))
		}
		got := fx.list.saved[0]
		if got.proof.AccountID != mcpAcct || got.proof.SessionID != testSID || got.proof.TokenID != "" || got.connID != fx.connID {
			t.Errorf("proof/connection = %+v", got)
		}
		if got.tools["echo"] != (mcpgw.ToolGrant{Mode: "allow", Hash: "h1"}) || got.tools["send"].Mode != "propose" {
			t.Errorf("tools = %+v", got.tools)
		}
	})
	t.Run("another account's connection is 404 and nothing is sent", func(t *testing.T) {
		fx := newMCPFx(t)
		rec := fx.call(http.MethodPatch, "/api/mcp/connections/"+newUUID()+"/defaults", fx.human(), good)
		if rec.Code != http.StatusNotFound || len(fx.list.saved) != 0 {
			t.Errorf("%d, saves %d", rec.Code, len(fx.list.saved))
		}
	})
	t.Run("bad bodies are refused before core", func(t *testing.T) {
		for name, body := range map[string]string{
			"not json":       `nope`,
			"missing":        `{}`,
			"null":           `{"default_tools":null}`,
			"unknown field":  `{"default_tools":{},"secret":"x"}`,
			"bad mode":       `{"default_tools":{"a":{"mode":"off","hash":"h"}}}`,
			"missing hash":   `{"default_tools":{"a":{"mode":"allow"}}}`,
			"too many tools": `{"default_tools":` + manyTools(501) + `}`,
		} {
			fx := newMCPFx(t)
			if rec := fx.call(http.MethodPatch, path(fx), fx.human(), body); rec.Code != http.StatusBadRequest {
				t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
			}
			if len(fx.list.saved) != 0 {
				t.Errorf("%s reached core", name)
			}
		}
	})
	t.Run("an empty object clears the defaults", func(t *testing.T) {
		fx := newMCPFx(t)
		if rec := fx.call(http.MethodPatch, path(fx), fx.human(), `{"default_tools":{}}`); rec.Code != http.StatusOK || len(fx.list.saved) != 1 {
			t.Errorf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("core answers", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			err    error
			status int
		}{
			{"validation error keeps its message", &mcpDefaultsInvalid{Msg: "default_tools: hash is required"}, http.StatusBadRequest},
			{"proof no longer live", ErrMCPProofInvalid, http.StatusUnauthorized},
			{"core failure is a clean 502", errors.New("dial tcp 192.0.2.9:8080: refused"), http.StatusBadGateway},
		} {
			fx := newMCPFx(t)
			fx.list.saveErr = tc.err
			rec := fx.call(http.MethodPatch, path(fx), fx.human(), good)
			if rec.Code != tc.status || strings.Contains(rec.Body.String(), "192.0.2.9") {
				t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body.String(), tc.status)
			}
			if tc.status == http.StatusBadRequest && !strings.Contains(rec.Body.String(), "hash is required") {
				t.Errorf("%s: message lost: %s", tc.name, rec.Body.String())
			}
		}
	})
	t.Run("core without the save call", func(t *testing.T) {
		fx := newMCPFx(t)
		fx.api.setMCPStart(&mcpStartConfig{GatewayURL: mcpGatewayURL, Core: listOnlyCore{fx.list}})
		if rec := fx.call(http.MethodPatch, path(fx), fx.human(), good); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%d, want 503", rec.Code)
		}
	})
}

// listOnlyCore hides the fake's SetDefaultTools, as a lister that cannot save defaults.
type listOnlyCore struct{ l *fakeMCPList }

func (c listOnlyCore) ListConnections(ctx context.Context, p mcpgw.Proof) ([]MCPConnection, error) {
	return c.l.ListConnections(ctx, p)
}

func manyTools(n int) string {
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"t%d":{"mode":"allow","hash":"h"}`, i)
	}
	b.WriteString("}")
	return b.String()
}
