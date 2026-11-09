package mcpgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dispatchFixture() SessionDispatchRequest {
	return SessionDispatchRequest{MCPSessionScope: db.MCPSessionScope{AccountID: "00000000-0000-4000-8000-000000000145", RunnerSessionID: "00000000-0000-4000-8000-000000000146", HumanSessionID: "00000000-0000-4000-8000-000000000147", RunnerInstallationID: "synthetic-runner", LaunchGeneration: 1, CredentialGeneration: 2, GrantTokenHash: strings.Repeat("a", 64)}, ConnectionID: "00000000-0000-4000-8000-000000000148"}
}
func TestSessionCoreUsesScopedContractAndRefusesFallback(t *testing.T) {
	in := dispatchFixture()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Internal-Key") != "synthetic-internal" {
			t.Error("missing internal authentication")
		}
		var got SessionDispatchRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got != in {
			t.Error("lost frozen scope")
		}
		switch r.URL.Path {
		case "/internal/mcp/session-connections/admit":
			w.Write([]byte(`{"live":true}`))
		case "/internal/mcp/session-connections/token":
			w.Write([]byte(`{"url":"https://resource.example/mcp","header_name":"Authorization","value":"Bearer synthetic-access","pin_policy":"strict"}`))
		default:
			t.Error("account fallback used")
			http.Error(w, "not found", 404)
		}
	}))
	defer srv.Close()
	c := &HTTPCoreClient{BaseURL: srv.URL, InternalKey: "synthetic-internal"}
	if err := c.SessionAdmit(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	cred, err := c.SessionToken(context.Background(), in)
	if err != nil || cred.Value != "Bearer synthetic-access" {
		t.Fatalf("scoped token failed: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}
func TestSessionCoreRefusesMalformedAndPrivateErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		gone   bool
	}{
		{"unknown", 404, "not found", true},
		{"missing-route", 404, "404 page not found", false},
		{"false", 200, `{"live":false}`, false},
		{"trailing", 200, `{"live":true} {"live":true}`, false},
		{"canary", 500, "synthetic-private-provider-body", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer srv.Close()
			c := &HTTPCoreClient{BaseURL: srv.URL, InternalKey: "synthetic-internal"}
			err := c.SessionAdmit(context.Background(), dispatchFixture())
			if err == nil || errors.Is(err, ErrConnectionGone) != tc.gone || strings.Contains(err.Error(), "synthetic-private") {
				t.Fatalf("unsafe refusal: %v", err)
			}
		})
	}
}

func TestSessionCoreRefreshContentionIsBounded(t *testing.T) {
	for _, finish := range []bool{true, false} {
		t.Run(fmt.Sprint(finish), func(t *testing.T) {
			var calls atomic.Int64
			waits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if finish && calls.Load() == 3 {
					w.Write([]byte(`{"live":true}`))
					return
				}
				w.WriteHeader(503)
				w.Write([]byte(`{"error":"refresh_in_progress","retry_after_ms":100}`))
			}))
			defer srv.Close()
			c := &HTTPCoreClient{BaseURL: srv.URL, InternalKey: "synthetic-internal", scopedWait: func(ctx context.Context, d time.Duration) error {
				if d != 100*time.Millisecond {
					t.Error("unexpected wait")
				}
				waits++
				return nil
			}}
			err := c.SessionAdmit(context.Background(), dispatchFixture())
			if finish {
				if err != nil || calls.Load() != 3 || waits != 2 {
					t.Fatalf("did not wait for rotation: calls=%d waits=%d err=%v", calls.Load(), waits, err)
				}
			} else if err == nil || calls.Load() != 20 || waits != 19 {
				t.Fatalf("unbounded retry: calls=%d waits=%d err=%v", calls.Load(), waits, err)
			}
		})
	}
}

func TestSessionCoreNeverFollowsRedirectWithInternalKey(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true); w.Write([]byte(`{"live":true}`)) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := &HTTPCoreClient{BaseURL: source.URL, InternalKey: "synthetic-internal"}
	if err := c.SessionAdmit(context.Background(), dispatchFixture()); err == nil {
		t.Fatal("redirect admitted")
	}
	if leaked.Load() {
		t.Fatal("internal key followed redirect")
	}
}

func TestSessionCoreRejectsUntrustedPlaintextBeforeSendingKey(t *testing.T) {
	for _, base := range []string{"http://public.example", "http://blerg-core.attacker.example", "http://resource.svc.attacker.example", "http://other-service", "https://user:private@example.test", "https://core.example?key=private", "ftp://core.example"} {
		t.Run(base, func(t *testing.T) {
			var calls atomic.Int64
			c := &HTTPCoreClient{BaseURL: base, InternalKey: "synthetic-internal", HTTP: &http.Client{Transport: scopedTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"live":true}`)), Header: http.Header{}}, nil
			})}}
			if err := c.SessionAdmit(context.Background(), dispatchFixture()); err == nil || calls.Load() != 0 {
				t.Fatal("untrusted transport sent the internal key")
			}
		})
	}
}

type scopedTransportFunc func(*http.Request) (*http.Response, error)

func (f scopedTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSessionCorePermittedInternalTransportHosts(t *testing.T) {
	for _, base := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080", "http://blerg-core:8080", "http://core.synthetic.svc:8080", "http://core.synthetic.svc.cluster.local:8080", "https://core.example"} {
		t.Run(base, func(t *testing.T) {
			var calls atomic.Int64
			c := &HTTPCoreClient{ClusterDeployment: true, BaseURL: base, InternalKey: "synthetic-internal", HTTP: &http.Client{Transport: scopedTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"live":true}`)), Header: http.Header{}}, nil
			})}}
			if err := c.SessionAdmit(context.Background(), dispatchFixture()); err != nil || calls.Load() != 1 {
				t.Fatalf("supported internal transport refused: %v", err)
			}
		})
	}
}
