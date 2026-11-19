package mcpconn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn/mcptest"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
	"github.com/jackc/pgx/v5/pgxpool"
)

const sessionTestRedirect = "https://core.example.com/auth/mcp/callback"

type sessionFlowEnv struct {
	svc           *Service
	pool          *pgxpool.Pool
	provider      *mcptest.Server
	auth          SessionOAuthAuthorization
	revoked       atomic.Bool
	dispatchCalls atomic.Int64
	dispatchHook  func()
}

func newSessionFlowEnv(t *testing.T) *sessionFlowEnv {
	t.Helper()
	pool := testsupport.Pool(t, db.Migrate)
	ctx := context.Background()
	e := &sessionFlowEnv{pool: pool, provider: mcptest.NewTLS()}
	t.Cleanup(e.provider.Close)
	var account, sid string
	if err := pool.QueryRow(ctx, `INSERT INTO accounts(provider,provider_subject,email,role) VALUES('local','session-flow','session-flow@example.com','member') RETURNING id::text`).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO human_sessions(account_id,token_hash,chain_id) VALUES($1,'synthetic-session',gen_random_uuid()) RETURNING id::text`, account).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE human_sessions SET chain_id=id WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	e.auth = SessionOAuthAuthorization{Binding: SessionBinding{SessionScope: SessionScope{AccountID: account, RunnerInstallationID: "synthetic-runner", RunnerSessionID: "00000000-0000-4000-8000-000000000145"}, HumanSessionID: sid, HumanDeadline: time.Now().Add(time.Hour)}, Definition: SessionServerDefinition{ID: "00000000-0000-4000-8000-000000000146", Version: 1, Name: "session-server", URL: e.provider.MCPURL(), Issuer: e.provider.Issuer(), Source: "settings"}, LaunchGeneration: 1}
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Key") != "synthetic-authority-key" {
			w.WriteHeader(403)
			return
		}
		if e.revoked.Load() {
			w.WriteHeader(404)
			return
		}
		if r.URL.Path == "/internal/mcp/session-oauth/admit" {
			e.dispatchCalls.Add(1)
			if e.dispatchHook != nil {
				e.dispatchHook()
			}
		}
		if r.URL.Path == "/internal/mcp/session-oauth/resolve" {
			_ = json.NewEncoder(w).Encode(e.auth)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"live": true})
	}))
	t.Cleanup(authority.Close)
	backend, err := keybackend.NewLocal(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	e.svc = NewService(db.NewPgStore(pool), backend, netguard.Policy{AllowPrivateHosts: []string{"127.0.0.1"}}, WithSessionOAuthAuthority(&HTTPSessionOAuthAuthority{BaseURL: authority.URL, InternalKey: "synthetic-authority-key"}))
	// Test-only TLS trust; production never replaces the netguard client.
	e.svc.httpc = e.provider.Client()
	e.svc.httpc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return e
}
func (e *sessionFlowEnv) preview() SessionOAuthPreviewInput {
	return SessionOAuthPreviewInput{SessionOAuthRequest: SessionOAuthRequest{AccountID: e.auth.Binding.AccountID, HumanSessionID: e.auth.Binding.HumanSessionID, RunnerSessionID: e.auth.Binding.RunnerSessionID, DefinitionID: e.auth.Definition.ID}, Scopes: "read read", RedirectURI: sessionTestRedirect, SessionLive: e.live}
}
func (e *sessionFlowEnv) live(ctx context.Context, account, sid string) (bool, error) {
	var live bool
	err := e.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM human_sessions WHERE account_id=$1 AND chain_id=$2 AND revoked_at IS NULL AND expires_at>now())`, account, sid).Scan(&live)
	return live, err
}

func TestSessionOAuthTLSFlowStoresOnlySuspendedCredential(t *testing.T) {
	e := newSessionFlowEnv(t)
	ctx := context.Background()
	preview, err := e.svc.PreviewSessionOAuth(ctx, e.preview())
	if err != nil {
		t.Fatal(err)
	}
	if preview.ProviderHost == "" || preview.IssuerHost == "" || len(preview.Scopes) != 1 || preview.Scopes[0] != "read" {
		t.Fatal("preview missing provider identity or reviewed scopes")
	}
	start, err := e.svc.StartSessionOAuth(ctx, e.auth.Binding.AccountID, e.auth.Binding.HumanSessionID, preview.DraftID, sessionTestRedirect)
	if err != nil {
		t.Fatal(err)
	}
	authURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if authURL.Query().Get("scope") != "read" || authURL.Query().Get("code_challenge_method") != "S256" || authURL.Query().Get("redirect_uri") != sessionTestRedirect {
		t.Fatal("authorization request lost reviewed binding")
	}
	response, err := e.provider.Approve(start.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	input := OAuthCallbackInput{State: response.Get("state"), Code: response.Get("code"), RedirectURI: sessionTestRedirect, SessionLive: e.live, Iss: response.Get("iss"), IssPresent: response.Has("iss")}
	c, err := e.svc.CompleteSessionOAuth(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if c.Lifecycle != "suspended" {
		t.Fatal("callback must not activate gateway authority")
	}
	if _, err := e.svc.FetchSecret(ctx, e.auth.Binding.AccountID, c.ID, Proof{SessionID: e.auth.Binding.HumanSessionID}); !errors.Is(err, ErrNotFound) {
		t.Fatal("account secret route read scoped token")
	}
	if _, err := e.svc.CompleteSessionOAuth(ctx, input); err == nil {
		t.Fatal("authorization code replay accepted")
	}
}

func TestSessionOAuthUnwiredAlwaysRefuses(t *testing.T) {
	s := NewService(nil, nil, netguard.Policy{})
	if _, err := s.PreviewSessionOAuth(context.Background(), SessionOAuthPreviewInput{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unwired preview accepted")
	}
	if _, err := s.StartSessionOAuth(context.Background(), "", "", "", sessionTestRedirect); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unwired start accepted")
	}
	if _, err := s.CompleteSessionOAuth(context.Background(), OAuthCallbackInput{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unwired callback accepted")
	}
}

func sessionPrepared(t *testing.T, e *sessionFlowEnv) (SessionOAuthPreview, OAuthStartResult, OAuthCallbackInput) {
	t.Helper()
	ctx := context.Background()
	preview, err := e.svc.PreviewSessionOAuth(ctx, e.preview())
	if err != nil {
		t.Fatal(err)
	}
	start, err := e.svc.StartSessionOAuth(ctx, e.auth.Binding.AccountID, e.auth.Binding.HumanSessionID, preview.DraftID, sessionTestRedirect)
	if err != nil {
		t.Fatal(err)
	}
	q, err := e.provider.Approve(start.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	return preview, start, OAuthCallbackInput{State: q.Get("state"), Code: q.Get("code"), RedirectURI: sessionTestRedirect, SessionLive: e.live, Iss: q.Get("iss"), IssPresent: q.Has("iss")}
}
func TestSessionOAuthEmptyScopesDoNotUseDiscoveryHints(t *testing.T) {
	e := newSessionFlowEnv(t)
	e.provider.Hint = true
	in := e.preview()
	in.Scopes = ""
	preview, err := e.svc.PreviewSessionOAuth(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	start, err := e.svc.StartSessionOAuth(context.Background(), e.auth.Binding.AccountID, e.auth.Binding.HumanSessionID, preview.DraftID, sessionTestRedirect)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(start.AuthorizationURL)
	if len(preview.Scopes) != 0 || target.Query().Has("scope") {
		t.Fatal("discovery expanded empty human scope selection")
	}
}
func TestSessionOAuthRejectsUntrustedDefinitions(t *testing.T) {
	for name, change := range map[string]func(*sessionFlowEnv){
		"foreign owner":           func(e *sessionFlowEnv) { e.auth.Binding.AccountID = "00000000-0000-4000-8000-000000000999" },
		"unapproved suggestion":   func(e *sessionFlowEnv) { e.auth.Definition.Source = "agent_text" },
		"missing selected issuer": func(e *sessionFlowEnv) { e.auth.Definition.Issuer = "" },
		"unbound runtime":         func(e *sessionFlowEnv) { e.auth.LaunchGeneration = 0 },
		"HTTP resource": func(e *sessionFlowEnv) {
			e.auth.Definition.URL = strings.Replace(e.auth.Definition.URL, "https:", "http:", 1)
		},
		"no S256": func(e *sessionFlowEnv) { e.provider.NoPKCE = true },
	} {
		t.Run(name, func(t *testing.T) {
			e := newSessionFlowEnv(t)
			in := e.preview()
			change(e)
			if _, err := e.svc.PreviewSessionOAuth(context.Background(), in); err == nil {
				t.Fatal("untrusted definition accepted")
			}
		})
	}
}
func TestSessionOAuthCallbackFailuresCannotStoreCredentials(t *testing.T) {
	for _, name := range []string{"wrong issuer", "missing advertised issuer", "wrong redirect", "provider denial", "canceled", "expired", "ownership revoked", "login revoked", "expanded scope"} {
		t.Run(name, func(t *testing.T) {
			e := newSessionFlowEnv(t)
			e.provider.AdvertiseIss = true
			ctx := context.Background()
			preview, _, input := sessionPrepared(t, e)
			switch name {
			case "wrong issuer":
				input.Iss = "https://other.example.com"
			case "missing advertised issuer":
				input.Iss = ""
				input.IssPresent = false
			case "wrong redirect":
				input.RedirectURI = "https://other.example.com/auth/mcp/callback"
			case "provider denial":
				input.ProviderError = true
				input.Code = ""
			case "canceled":
				if err := e.svc.CancelSessionOAuth(ctx, e.auth.Binding.AccountID, e.auth.Binding.HumanSessionID, preview.DraftID); err != nil {
					t.Fatal(err)
				}
			case "expired":
				if _, err := e.pool.Exec(ctx, `UPDATE mcp_session_oauth_attempts SET expires_at=now()-interval '1 second' WHERE id=$1`, preview.DraftID); err != nil {
					t.Fatal(err)
				}
			case "ownership revoked":
				e.revoked.Store(true)
			case "login revoked":
				if _, err := e.pool.Exec(ctx, `UPDATE human_sessions SET revoked_at=now() WHERE chain_id=$1`, e.auth.Binding.HumanSessionID); err != nil {
					t.Fatal(err)
				}
			case "expanded scope":
				e.provider.ExchangeStatus = 200
				e.provider.ExchangeRaw = `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","token_type":"Bearer","expires_in":3600,"scope":"read write"}`
			}
			if _, err := e.svc.CompleteSessionOAuth(ctx, input); err == nil {
				t.Fatal("invalid callback accepted")
			}
			var count int
			if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_connections WHERE runner_session_id IS NOT NULL`).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed callback persisted a credential")
			}
			if name == "expanded scope" {
				e.provider.Lock()
				defer e.provider.Unlock()
				if len(e.provider.Revoked) != 2 {
					t.Fatal("expanded grant not revoked")
				}
			}
		})
	}
}
func TestSessionOAuthVerifierEncryptedAndResultExpires(t *testing.T) {
	e := newSessionFlowEnv(t)
	e.provider.Confidential = true
	preview, start, input := sessionPrepared(t, e)
	ctx := context.Background()
	var ct, draft []byte
	var kid string
	if err := e.pool.QueryRow(ctx, `SELECT verifier_ciphertext,verifier_key_id,draft FROM mcp_session_oauth_attempts WHERE id=$1`, preview.DraftID).Scan(&ct, &kid, &draft); err != nil {
		t.Fatal(err)
	}
	verifier, err := e.svc.backend.Decrypt(ctx, ct, kid)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(start.AuthorizationURL)
	if pkceChallenge(string(verifier)) != target.Query().Get("code_challenge") || bytes.Contains(ct, verifier) || bytes.Contains(draft, verifier) || bytes.Contains(draft, []byte("client-secret")) {
		t.Fatal("verifier or client secret persisted in plaintext")
	}
	if _, err := e.svc.CompleteSessionOAuth(ctx, input); err != nil {
		t.Fatal(err)
	}
	status, err := e.svc.SessionOAuthStatus(ctx, e.auth.Binding.AccountID, e.auth.Binding.HumanSessionID, preview.DraftID)
	if err != nil || status.Stage != "completed" {
		t.Fatal("server status did not establish signed-in state")
	}
	var cleared bool
	if err := e.pool.QueryRow(ctx, `SELECT verifier_ciphertext IS NULL AND client_ciphertext IS NULL FROM mcp_session_oauth_attempts WHERE id=$1`, preview.DraftID).Scan(&cleared); err != nil || !cleared {
		t.Fatal("consumed attempt retained secrets")
	}
	if _, err := e.svc.SessionOAuthStatus(ctx, e.auth.Binding.AccountID, "00000000-0000-4000-8000-000000000999", preview.DraftID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign reference distinguished from unknown")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_session_oauth_attempts SET expires_at=now()-interval '1 second' WHERE id=$1`, preview.DraftID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SessionOAuthStatus(ctx, e.auth.Binding.AccountID, e.auth.Binding.HumanSessionID, preview.DraftID); !errors.Is(err, ErrNotFound) {
		t.Fatal("completed result reference did not expire")
	}
}

func TestSessionOAuthRevocationDuringExchangeWins(t *testing.T) {
	for _, name := range []string{"cancel", "logout", "ownership change", "preview replacement"} {
		t.Run(name, func(t *testing.T) {
			e := newSessionFlowEnv(t)
			preview, _, input := sessionPrepared(t, e)
			ctx := context.Background()
			hookError := make(chan error, 1)
			e.provider.AfterCodeExchange = func() {
				switch name {
				case "cancel":
					hookError <- e.svc.CancelSessionOAuth(ctx, e.auth.Binding.AccountID, e.auth.Binding.HumanSessionID, preview.DraftID)
				case "logout":
					_, err := e.pool.Exec(ctx, `UPDATE human_sessions SET revoked_at=now() WHERE chain_id=$1`, e.auth.Binding.HumanSessionID)
					hookError <- err
				case "preview replacement":
					_, err := e.svc.PreviewSessionOAuth(ctx, e.preview())
					hookError <- err
				case "ownership change":
					e.revoked.Store(true)
					hookError <- nil
				}
			}
			if _, err := e.svc.CompleteSessionOAuth(ctx, input); err == nil {
				t.Fatal("revocation during exchange did not win")
			}
			if err := <-hookError; err != nil {
				t.Fatal(err)
			}
			var count int
			if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_connections WHERE runner_session_id IS NOT NULL`).Scan(&count); err != nil || count != 0 {
				t.Fatal("revoked exchange persisted credential")
			}
			e.provider.Lock()
			defer e.provider.Unlock()
			if len(e.provider.Revoked) != 2 {
				t.Fatal("issued tokens from revoked exchange not revoked")
			}
		})
	}
}

func TestSessionOAuthDeadlineCannotOutliveCoreLogin(t *testing.T) {
	e := newSessionFlowEnv(t)
	ctx := context.Background()
	var deadline time.Time
	if err := e.pool.QueryRow(ctx, `UPDATE human_sessions SET expires_at=clock_timestamp()+interval '5 minutes' WHERE chain_id=$1 RETURNING expires_at`, e.auth.Binding.HumanSessionID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.PreviewSessionOAuth(ctx, e.preview())
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := e.pool.QueryRow(ctx, `SELECT draft FROM mcp_session_oauth_attempts WHERE id=$1`, p.DraftID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var draft sessionOAuthDraft
	if err := json.Unmarshal(raw, &draft); err != nil {
		t.Fatal(err)
	}
	if draft.Authorization.Binding.HumanDeadline.After(deadline) {
		t.Fatal("authority extended the Core login deadline")
	}
}

func TestSessionOAuthPreviewReplacementAndStartTTL(t *testing.T) {
	e := newSessionFlowEnv(t)
	ctx := context.Background()
	first, err := e.svc.PreviewSessionOAuth(ctx, e.preview())
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.PreviewSessionOAuth(ctx, e.preview())
	if err != nil {
		t.Fatal("reload cannot replace old preview", err)
	}
	if first.DraftID == second.DraftID {
		t.Fatal("replacement must have new attempt identity")
	}
	var stage string
	if err = e.pool.QueryRow(ctx, `SELECT stage FROM mcp_session_oauth_attempts WHERE id=$1`, first.DraftID).Scan(&stage); err != nil || stage != "canceled" {
		t.Fatal("replaced attempt still usable")
	}
	if _, err = e.pool.Exec(ctx, `UPDATE mcp_session_oauth_attempts SET expires_at=clock_timestamp()+interval '1 minute' WHERE id=$1`, second.DraftID); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	if _, err = e.svc.StartSessionOAuth(ctx, e.auth.Binding.AccountID, e.auth.Binding.HumanSessionID, second.DraftID, sessionTestRedirect); err != nil {
		t.Fatal(err)
	}
	var expires time.Time
	if err = e.pool.QueryRow(ctx, `SELECT expires_at FROM mcp_session_oauth_attempts WHERE id=$1`, second.DraftID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires.Before(before.Add(9*time.Minute)) || expires.After(time.Now().Add(10*time.Minute)) {
		t.Fatal("start did not restart a bounded ten-minute TTL")
	}
}

func TestSessionOAuthFinishedURLMatchesFrozenDraft(t *testing.T) {
	d := sessionOAuthDraft{Authorization: SessionOAuthAuthorization{Definition: SessionServerDefinition{URL: "https://resource.example.com/mcp"}}, Server: authServer{AuthorizationEndpoint: "https://provider.example.com/authorize?prompt=consent"}, Scopes: "read", RedirectURI: sessionTestRedirect}
	u, _ := url.Parse(d.Server.AuthorizationEndpoint)
	q := u.Query()
	for k, v := range map[string]string{"response_type": "code", "client_id": "synthetic-client", "state": "synthetic-state", "redirect_uri": d.RedirectURI, "code_challenge": pkceChallenge("synthetic-verifier"), "code_challenge_method": "S256", "resource": d.Authorization.Definition.URL, "scope": "read"} {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	if err := validateSessionAuthorizationURL(u.String(), d, "synthetic-client", "synthetic-state", "synthetic-verifier"); err != nil {
		t.Fatal("frozen URL refused", err)
	}
	for _, field := range []string{"host", "path", "scheme", "userinfo", "scope", "state", "redirect_uri", "resource", "client_id", "response_type", "code_challenge", "code_challenge_method", "prompt", "extra", "repeated"} {
		t.Run(field, func(t *testing.T) {
			v := *u
			params := v.Query()
			switch field {
			case "host":
				v.Host = "attacker.example.com"
			case "path":
				v.Path = "/other"
			case "scheme":
				v.Scheme = "http"
			case "userinfo":
				v.User = url.User("synthetic")
			case "repeated":
				params.Add("state", "synthetic-state")
			default:
				params.Set(field, "tampered")
			}
			v.RawQuery = params.Encode()
			if err := validateSessionAuthorizationURL(v.String(), d, "synthetic-client", "synthetic-state", "synthetic-verifier"); err == nil {
				t.Fatal("finished URL diverged from frozen approval")
			}
		})
	}
}

func TestSessionOAuthAuthorityPlaintextHostBoundary(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "runner.example.svc", "runner.example.svc.cluster.local"} {
		if !sessionAuthorityPlaintextHost(host, true) {
			t.Fatalf("internal host refused: %s", host)
		}
	}
	for _, host := range []string{"runner", "svc", "cluster.local", "runner.svc.attacker.example.com", "runner.example.svc.cluster.local.attacker.example.com", "runner..svc", "-runner.example.svc"} {
		if sessionAuthorityPlaintextHost(host, true) {
			t.Fatalf("untrusted host allowed: %s", host)
		}
	}
}

func TestSessionOAuthPreviewCannotCancelOtherLogin(t *testing.T) {
	e := newSessionFlowEnv(t)
	ctx := context.Background()
	first, err := e.svc.PreviewSessionOAuth(ctx, e.preview())
	if err != nil {
		t.Fatal(err)
	}
	var sid string
	if err = e.pool.QueryRow(ctx, `INSERT INTO human_sessions(account_id,token_hash,chain_id) VALUES($1,'synthetic-other-login',gen_random_uuid()) RETURNING id::text`, e.auth.Binding.AccountID).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	if _, err = e.pool.Exec(ctx, `UPDATE human_sessions SET chain_id=id WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	e.auth.Binding.HumanSessionID = sid
	if _, err = e.svc.PreviewSessionOAuth(ctx, e.preview()); err == nil {
		t.Fatal("other login replaced an active attempt")
	}
	var stage string
	if err = e.pool.QueryRow(ctx, `SELECT stage FROM mcp_session_oauth_attempts WHERE id=$1`, first.DraftID).Scan(&stage); err != nil || stage != "preview" {
		t.Fatal("other login canceled the initiating login's attempt")
	}
}
