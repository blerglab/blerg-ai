package livedata

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livetls"
)

func TestIssuerDedicatedTransport(t *testing.T) {
	s, store, _ := issuer(t)
	root := t.TempDir()
	if e := livetls.Provision(root); e != nil {
		t.Fatal(e)
	}
	core, e := livetls.Load(filepath.Join(root, "core"), "core")
	if e != nil {
		t.Fatal(e)
	}
	runner, e := livetls.Load(filepath.Join(root, "runner"), "runner")
	if e != nil {
		t.Fatal(e)
	}
	scope := store.grant.Scope
	scope.Installation = core.Installation
	scope.Runner = core.Runner
	store.grant = approvedFixture(scope, s.now())
	s.Installation = core.Installation
	s.Runner = core.Runner
	mux := http.NewServeMux()
	transport := Transport{Issuer: s, Verify: core.VerifyRequest, Enabled: true}
	used := false
	transport.Provider = func(_ context.Context, r ld.AttemptReceipt) (ld.ProviderCredential, error) {
		if used || r.Scope != store.grant.Scope || r.Attempt != "00000000-0000-4000-8000-000000000111" || r.Stage != "list" {
			return ld.ProviderCredential{}, ld.ErrDenied
		}
		used = true
		return ld.ProviderCredential{URL: "https://market.example/mcp", HeaderName: "Authorization", Value: "Bearer canary_live_provider_secret"}, nil
	}

	transport.Register(mux)
	var logs bytes.Buffer
	recorder := failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	listener, e := core.Listen(context.Background(), "127.0.0.1:0", recorder.HTTP("core-live-data", mux), func() { t.Error("listener failed") })
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	httpClient := runner.Client()
	httpClient.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, listener.(*http.Server).Addr)
	}
	client := &ld.ServiceClient{URL: "https://blerg-core", HTTP: httpClient}
	ctx := context.Background()
	token, e := client.Mint(ctx, store.grant.Scope.Binding, 1, false)
	if e != nil {
		t.Fatal(e)
	}

	request := ld.ProviderRequest{Token: token, Attempt: "00000000-0000-4000-8000-000000000111", Stage: "list"}
	credential, e := client.ProviderCredential(ctx, request)
	if e != nil || credential.Value != "Bearer canary_live_provider_secret" {
		t.Fatal("audited provider credential refused", e)
	}
	if _, e = client.ProviderCredential(ctx, request); !errors.Is(e, ld.ErrDenied) {
		t.Fatal("provider receipt replay accepted")
	}
	request.Token = "forged"
	if _, e = client.ProviderCredential(ctx, request); !errors.Is(e, ld.ErrDenied) {
		t.Fatal("forged capability accepted")
	}
	if strings.Contains(logs.String(), "canary_live_provider_secret") {
		t.Fatal("credential reached access logs")
	}
	keys, e := client.PublicKeys(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ld.Verify(token, keys, s.Installation, s.Runner, s.now()); e != nil {
		t.Fatal(e)
	}
	if _, e = client.Mint(ctx, store.grant.Scope.Binding, 1, true); e != nil {
		t.Fatal(e)
	}
	before := store.reads
	for i := 0; i < 2; i++ {
		snapshot, e := client.Current(ctx, store.grant.Scope)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = snapshot.Grant.ApprovedRevision(); e != nil {
			t.Fatal(e)
		}
	}
	if store.reads != before+2 {
		t.Fatal("authority cached")
	}
	if e = client.Invalidate(ctx, store.grant.Scope); e != nil {
		t.Fatal(e)
	}
	if _, e = client.Mint(ctx, store.grant.Scope.Binding, 1, false); e == nil {
		t.Fatal("revoked grant minted")
	}
	if _, e = client.Current(ctx, store.grant.Scope); !errors.Is(e, ld.ErrDenied) {
		t.Fatal("authoritative revocation was collapsed into transient failure")
	}
	store.denied = false
	store.auditDown = true
	if _, e = client.Current(ctx, store.grant.Scope); !errors.Is(e, ld.ErrAuthorityUnavailable) {
		t.Fatal("audit outage was collapsed into authorization loss")
	}
	store.denied = true
	if _, e = client.Current(ctx, store.grant.Scope); !errors.Is(e, ld.ErrDenied) {
		t.Fatal("audit outage hid an already-known revocation")
	}
	store.auditDown = false
	if len(store.outcomes) < 6 {
		t.Fatal("missing per-operation audits")
	}
	// Rollout gate is checked even for a valid, active grant and provisioned Runner.
	store.denied = false
	disabledMux := http.NewServeMux()
	Transport{Issuer: s, Verify: core.VerifyRequest}.Register(disabledMux)
	disabledListener, e := core.Listen(ctx, "127.0.0.1:0", disabledMux, func() { t.Error("listener failed") })
	if e != nil {
		t.Fatal(e)
	}
	defer disabledListener.Close()
	disabledHTTP := runner.Client()
	disabledHTTP.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, disabledListener.(*http.Server).Addr)
	}
	disabled := &ld.ServiceClient{URL: "https://blerg-core", HTTP: disabledHTTP}
	reads := store.reads
	audits := len(store.outcomes)
	if _, e = disabled.Mint(ctx, store.grant.Scope.Binding, 1, false); e == nil || store.reads != reads || len(store.outcomes) != audits+1 {
		t.Fatal("disabled gate did not refuse and audit")
	}
	if _, e = disabled.Mint(ctx, store.grant.Scope.Binding, 1, true); e == nil || store.outcomes[len(store.outcomes)-1] != "renew:unavailable" {
		t.Fatal("disabled renewal refusal was not audited as renewal")
	}
	bad := store.grant.Scope
	bad.Installation = testScope().Installation
	if _, e = client.Current(ctx, bad); e == nil || store.reads != reads {
		t.Fatal("wrong installation reached durable authority")
	}
	canary := "issuer-access-log-canary"
	req, _ := http.NewRequest("POST", "https://blerg-core"+ld.IssuerPath+"mint", strings.NewReader(`{"unexpected":"`+canary+`"}`))
	req.Header.Set("Authorization", "Bearer "+canary)
	response, e := httpClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if logs.Len() == 0 || strings.Contains(logs.String(), canary) || strings.Contains(logs.String(), token) {
		t.Fatal("access middleware omitted refusal or logged credentials")
	}
	// Public mux does not register issuer routes. A component key cannot reach it.
	public := httptest.NewServer(http.NotFoundHandler())
	defer public.Close()
	publicClient := &ld.ServiceClient{URL: public.URL, HTTP: &http.Client{Transport: http.DefaultTransport}}
	if _, e = publicClient.Mint(ctx, store.grant.Scope.Binding, 1, false); e == nil {
		t.Fatal("public endpoint minted")
	}
}

func TestIssuerTransportDisabledAndPreAuthAudit(t *testing.T) {
	s, store, _ := issuer(t)
	mux := http.NewServeMux()
	Transport{Issuer: s, Verify: func(*http.Request) error { return ld.ErrDenied }}.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", ld.IssuerPath+"mint", nil))
	if rec.Code != 401 || len(store.outcomes) != 0 {
		t.Fatal("pre-auth request wrote a database audit")
	}
}

func TestIssuerTransportRequiresDedicatedVerifier(t *testing.T) {
	s, store, _ := issuer(t)
	mux := http.NewServeMux()
	Transport{Issuer: s, Enabled: true, Verify: func(*http.Request) error { return ld.ErrDenied }}.Register(mux)
	raw, _ := json.Marshal(ld.MintRequest{Binding: store.grant.Scope.Binding, Revision: 1})
	req := httptest.NewRequest("POST", ld.IssuerPath+"mint", bytes.NewReader(raw))
	uri, _ := url.Parse("spiffe://blerg/installation/" + s.Installation + "/runner/" + s.Runner)
	req.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{URIs: []*url.URL{uri}}}}}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 401 || len(store.outcomes) != 0 {
		t.Fatal("foreign listener identity bypassed dedicated verifier")
	}
}
