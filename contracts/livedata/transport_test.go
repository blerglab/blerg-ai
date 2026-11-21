package livedata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServiceClientClosedTransportAndAck(t *testing.T) {
	calls := 0
	body := `{}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(body)) }))
	defer server.Close()
	client := &ServiceClient{URL: server.URL}
	if e := client.Invalidate(context.Background(), Scope{}); e == nil || calls != 0 {
		t.Fatal("default transport used")
	}
	client.HTTP = &http.Client{Transport: http.DefaultTransport}
	if e := client.Invalidate(context.Background(), Scope{}); e == nil {
		t.Fatal("missing revocation acknowledgment accepted")
	}
	body = `{"revoked":true}`
	if e := client.Invalidate(context.Background(), Scope{}); e != nil {
		t.Fatal("valid acknowledgment refused")
	}
}

func TestServiceClientNeverRedirects(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls++
		_, _ = w.Write([]byte(`{"revoked":true}`))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client := &ServiceClient{URL: redirect.URL, HTTP: &http.Client{Transport: http.DefaultTransport}}
	if e := client.Invalidate(context.Background(), Scope{}); e == nil || targetCalls != 0 {
		t.Fatal("issuer redirect followed")
	}
}

func TestServiceClientResponseBoundaries(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"forbidden": {403, `{"token":"forbidden"}`}, "unauthorized": {401, `{"token":"forbidden"}`}, "unavailable": {503, `{"token":"forbidden"}`}, "oversize": {200, `{"token":"` + strings.Repeat("x", 128*1024) + `"}`}, "malformed": {200, `{`}, "empty token": {200, `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c := ServiceClient{URL: server.URL, HTTP: &http.Client{Transport: http.DefaultTransport}}
			if token, e := c.Mint(context.Background(), "binding", 1, false); e == nil || token != "" {
				t.Fatal("invalid issuer response accepted")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"keys":{}}`)) }))
	defer server.Close()
	c := ServiceClient{URL: server.URL, HTTP: &http.Client{Transport: http.DefaultTransport}}
	if _, e := c.PublicKeys(context.Background()); e == nil {
		t.Fatal("empty signing key set accepted")
	}
}
