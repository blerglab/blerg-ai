package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type apiSessionDispatchAuthority struct{}

func (apiSessionDispatchAuthority) Resolve(context.Context, mcpconn.SessionOAuthRequest) (mcpconn.SessionOAuthAuthorization, error) {
	return mcpconn.SessionOAuthAuthorization{}, errors.New("unused fixture operation")
}
func (apiSessionDispatchAuthority) Validate(context.Context, mcpconn.SessionOAuthAuthorization) error {
	return nil
}
func (apiSessionDispatchAuthority) ValidateSessionDispatch(context.Context, mcpconn.SessionDispatchRequest, mcpconn.SessionOAuthAuthorization) error {
	return nil
}
func newSessionDispatchAPI(t *testing.T) (*mcpEnv, mcpconn.SessionDispatchRequest) {
	t.Helper()
	e := newMCPEnv(t)
	backend, err := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc := mcpconn.NewService(db.NewPgStore(e.pool), backend, netguard.Policy{}, mcpconn.WithSessionOAuthAuthority(apiSessionDispatchAuthority{}))
	binding := mcpconn.SessionBinding{SessionScope: mcpconn.SessionScope{AccountID: e.acctA, RunnerInstallationID: "synthetic-runner", RunnerSessionID: "00000000-0000-4000-8000-000000000145"}, HumanSessionID: e.sidA, HumanDeadline: time.Now().Add(time.Hour)}
	auth := mcpconn.SessionOAuthAuthorization{Binding: binding, Definition: mcpconn.SessionServerDefinition{ID: "00000000-0000-4000-8000-000000000146", Version: 1, Name: "api-scoped", URL: "https://resource.example.com/mcp", Issuer: "https://provider.example.com", Source: "settings"}, LaunchGeneration: 7}
	c, err := svc.StoreSessionOAuth(context.Background(), binding, mcpconn.SessionOAuthCredential{Authorization: &auth, Name: auth.Definition.Name, URL: auth.Definition.URL, Issuer: auth.Definition.Issuer, TokenEndpoint: "https://provider.example.com/token", ClientID: "synthetic-client", AuthMethod: "client_secret_basic", ClientSecret: "scoped-client-canary", AccessToken: "scoped-access-canary", RefreshToken: "scoped-refresh-canary", ExpiresAt: time.Now().Add(time.Hour), Scopes: "read"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.pool.Exec(context.Background(), `UPDATE mcp_connections SET lifecycle='active',credential_generation=credential_generation+1,suspended_at=NULL,cleanup_after=NULL,binding_lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	e.deps.MCPConnections = svc
	e.srv.Close()
	e.srv = httptest.NewServer(api.NewRouter(e.deps))
	t.Cleanup(e.srv.Close)
	return e, mcpconn.SessionDispatchRequest{SessionScope: binding.SessionScope, ConnectionID: c.ID, HumanSessionID: e.sidA, LaunchGeneration: 7, CredentialGeneration: 2, GrantTokenHash: strings.Repeat("a", 64)}
}
func TestSessionDispatchInternalTransportAndAccessOnly(t *testing.T) {
	e, in := newSessionDispatchAPI(t)
	for _, path := range []string{"/internal/mcp/session-connections/admit", "/internal/mcp/session-connections/token"} {
		for _, key := range []string{"", e.humanA, e.agentA} {
			code, _, _ := e.internal(t, path, key, in)
			if code != 401 {
				t.Fatalf("untrusted caller status %d", code)
			}
		}
		code, body, headers := e.internal(t, path, e.deps.InternalKey, in)
		if code != 200 || headers.Get("Cache-Control") != "no-store" {
			t.Fatalf("live scoped transport status %d", code)
		}
		for _, canary := range []string{"scoped-refresh-canary", "scoped-client-canary"} {
			if strings.Contains(body, canary) {
				t.Fatal("provider refresh/client credential escaped Core")
			}
		}
		if strings.HasSuffix(path, "/token") && !strings.Contains(body, "Bearer scoped-access-canary") {
			t.Fatal("gateway did not receive the access credential")
		}
		if strings.HasSuffix(path, "/admit") && strings.Contains(body, "scoped-access-canary") {
			t.Fatal("admission returned a credential")
		}
	}
	code, _, _ := e.internal(t, "/internal/mcp/connections/token", e.deps.InternalKey, map[string]string{"account_id": in.AccountID, "connection_id": in.ConnectionID, "session_id": in.HumanSessionID})
	if code != 404 {
		t.Fatal("legacy account route exposed scoped secret")
	}
}
func TestSessionDispatchInternalUniformRefusalAndBusy(t *testing.T) {
	e, in := newSessionDispatchAPI(t)
	for _, path := range []string{"/internal/mcp/session-connections/admit", "/internal/mcp/session-connections/token"} {
		unknown := in
		unknown.ConnectionID = "00000000-0000-4000-8000-000000000199"
		a, unknownBody, _ := e.internal(t, path, e.deps.InternalKey, unknown)
		foreign := in
		foreign.AccountID = e.acctB
		b, foreignBody, _ := e.internal(t, path, e.deps.InternalKey, foreign)
		if a != 404 || b != 404 || unknownBody != foreignBody {
			t.Fatal("unknown and foreign scoped references differ")
		}
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE mcp_connections SET session_refresh_lease_until=clock_timestamp()+interval '30 seconds' WHERE id=$1`, in.ConnectionID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/internal/mcp/session-connections/admit", "/internal/mcp/session-connections/token"} {
		code, body, headers := e.internal(t, path, e.deps.InternalKey, in)
		if code != 503 || !strings.Contains(body, `"error":"refresh_in_progress"`) || headers.Get("Cache-Control") != "no-store" {
			t.Fatal("refresh contention is not explicitly retryable")
		}
	}
}

func TestSessionDispatchInternalRejectsProxyUnknownFieldsAndTrailingJSON(t *testing.T) {
	e, in := newSessionDispatchAPI(t)
	for _, path := range []string{"/internal/mcp/session-connections/admit", "/internal/mcp/session-connections/token"} {
		code, _ := e.do(t, "POST", path, "", in, map[string]string{"X-Internal-Key": e.deps.InternalKey, "X-Forwarded-Proto": "https"})
		if code != 401 {
			t.Fatal("proxied internal request accepted")
		}
		raw, _ := json.Marshal(in)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		for _, field := range []string{"url", "token_id", "refresh_token"} {
			body[field] = "synthetic-private-input"
			code, _, _ := e.internal(t, path, e.deps.InternalKey, body)
			if code != 400 {
				t.Fatal("untrusted scope override field accepted")
			}
			delete(body, field)
		}
		request, err := http.NewRequest("POST", e.srv.URL+path, bytes.NewReader(append(raw, []byte(` {}`)...)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-Internal-Key", e.deps.InternalKey)
		response, err := e.srv.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatal("trailing JSON was accepted")
		}
	}
}

func TestSessionDispatchInternalUnwiredRefusesBothRoutes(t *testing.T) {
	e, in := newSessionDispatchAPI(t)
	backend, err := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	e.deps.MCPConnections = mcpconn.NewService(db.NewPgStore(e.pool), backend, netguard.Policy{})
	e.srv.Close()
	e.srv = httptest.NewServer(api.NewRouter(e.deps))
	t.Cleanup(e.srv.Close)
	for _, path := range []string{"/internal/mcp/session-connections/admit", "/internal/mcp/session-connections/token"} {
		code, body, _ := e.internal(t, path, e.deps.InternalKey, in)
		if code != 503 || strings.Contains(body, "scoped-access-canary") {
			t.Fatalf("unwired route admitted dispatch: %d", code)
		}
	}
}

func TestSessionLifecycleInternalTransport(t *testing.T) {
	e, in := newSessionDispatchAPI(t)
	payload := struct {
		mcpconn.SessionDispatchRequest
		Action string `json:"action"`
	}{in, "renew"}
	path := "/internal/mcp/session-connections/lifecycle"
	for _, key := range []string{"", e.humanA, e.agentA} {
		code, _, _ := e.internal(t, path, key, payload)
		if code != 401 {
			t.Errorf("untrusted lifecycle status %d", code)
		}
	}
	code, body, headers := e.internal(t, path, e.deps.InternalKey, payload)
	if code != 200 || headers.Get("Cache-Control") != "no-store" || strings.Contains(body, "canary") {
		t.Fatal("lifecycle renewal transport", code, body)
	}
	payload.Action = "suspend"
	code, body, _ = e.internal(t, path, e.deps.InternalKey, payload)
	if code != 200 {
		t.Fatal("lifecycle suspend", code)
	}
	var acknowledged struct {
		Generation int64 `json:"credential_generation"`
	}
	if json.Unmarshal([]byte(body), &acknowledged) != nil || acknowledged.Generation != in.CredentialGeneration+1 {
		t.Fatal("suspend did not acknowledge exact resulting fence", body)
	}
	// A second Runner replica (or retry after losing the first HTTP response)
	// receives the same authoritative suspended fence using the original body.
	code, body, _ = e.internal(t, path, e.deps.InternalKey, payload)
	if code != 200 || json.Unmarshal([]byte(body), &acknowledged) != nil || acknowledged.Generation != in.CredentialGeneration+1 {
		t.Fatal("duplicate suspend lost its acknowledged fence", code)
	}
	c, err := e.deps.MCPConnections.GetSessionCredential(context.Background(), in.SessionScope, in.ConnectionID)
	if err != nil || c.Lifecycle != "suspended" || c.Generation != in.CredentialGeneration+1 {
		t.Fatal("suspension was not applied", c, err)
	}
	payload.Action = "renew"
	code, _, _ = e.internal(t, path, e.deps.InternalKey, payload)
	if code != 404 {
		t.Fatal("stale renewal resurrected binding", code)
	}
	payload.Action = "attach"
	code, _, _ = e.internal(t, path, e.deps.InternalKey, payload)
	if code != 400 {
		t.Fatal("runtime lifecycle accepted attachment", code)
	}
	payload.Action = "close"
	code, _, _ = e.internal(t, path, e.deps.InternalKey, payload)
	if code != 404 {
		t.Fatal("stale close erased a newer generation", code)
	}
	payload.CredentialGeneration = c.Generation
	code, _, _ = e.internal(t, path, e.deps.InternalKey, payload)
	if code != 200 {
		t.Fatal("acknowledged close refused", code)
	}

}
