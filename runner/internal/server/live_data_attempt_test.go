package server

import (
	"crypto/tls"
	"crypto/x509"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLiveAttemptDedicatedIdentityAndDisabledGate(t *testing.T) {
	identity := "spiffe://blerg/installation/00000000-0000-4000-8000-000000000001/core"
	uri, err := url.Parse(identity)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{URIs: []*url.URL{uri}}
	for _, test := range []struct {
		name           string
		tls, forwarded bool
		want           int
	}{{"host internal key only", false, false, 401}, {"forwarded request", true, true, 401}, {"dedicated identity disabled", true, false, 503}} {
		t.Run(test.name, func(t *testing.T) {
			api := &API{coreInternalKey: "fixture", liveDataCoreIdentity: identity}
			request := httptest.NewRequest("POST", "/internal/live-data/attempt", strings.NewReader(`{}`))
			request.Header.Set("X-Internal-Key", "fixture")
			if test.tls {
				request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
			}
			if test.forwarded {
				request.Header.Set("Forwarded", "for=192.0.2.1")
			}
			response := httptest.NewRecorder()
			api.handleLiveAttempt(response, request)
			if response.Code != test.want {
				t.Fatalf("got %d, want %d", response.Code, test.want)
			}
		})
	}
}
