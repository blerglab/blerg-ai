package server

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLiveDefinitionServiceIdentity(t *testing.T) {
	const expected = "spiffe://blerg/installation/00000000-0000-4000-8000-000000000001/core"
	uri, _ := url.Parse(expected)
	leaf := &x509.Certificate{URIs: []*url.URL{uri}}
	for _, tc := range []struct {
		name, header, value  string
		verified, configured bool
		want                 int
	}{
		{name: "dedicated CA refusal", verified: true, configured: true, want: 401},
		{name: "key alone", configured: true, want: 401},
		{name: "wrong identity", verified: true, configured: true, want: 401},
		{name: "wrong key", verified: true, configured: true, want: 401},
		{name: "empty forwarded", header: "Forwarded", verified: true, configured: true, want: 401},
		{name: "not configured", verified: true, want: 401},
		{name: "verified core", verified: true, configured: true, want: 409},
		{name: "forwarded for", header: "X-Forwarded-For", value: "192.0.2.1", verified: true, configured: true, want: 401},
		{name: "forwarded host", header: "X-Forwarded-Host", value: "example.com", verified: true, configured: true, want: 401},
		{name: "forwarded proto", header: "X-Forwarded-Proto", value: "https", verified: true, configured: true, want: 401},
		{name: "real ip", header: "X-Real-IP", value: "192.0.2.1", verified: true, configured: true, want: 401},
		{name: "forwarded", header: "Forwarded", value: "for=192.0.2.1", verified: true, configured: true, want: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &API{coreInternalKey: "fixture"}
			if tc.configured {
				a.liveDataCoreIdentity = expected
				if tc.name == "wrong identity" {
					a.liveDataCoreIdentity = expected + "-other"
				}
			}
			if tc.name == "dedicated CA refusal" {
				a.SetLiveDataPeerVerifier(func(*http.Request) error { return errors.New("fixture-ca-refusal") })
			}
			// An empty Hub refuses listing, without contacting any fallback service.
			a.hub = NewHub()
			req := httptest.NewRequest("POST", "/internal/live-data/definition", strings.NewReader(`{}`))
			req.Header.Set("X-Internal-Key", "fixture")
			if tc.name == "wrong key" {
				req.Header.Set("X-Internal-Key", "wrong")
			}
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
			if tc.verified {
				req.TLS.VerifiedChains = [][]*x509.Certificate{{leaf}}
			}
			rec := httptest.NewRecorder()
			a.handleLiveDefinition(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
