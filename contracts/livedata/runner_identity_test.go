package livedata

import (
	"crypto/tls"
	"crypto/x509"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestRunnerIdentityRequiresVerifiedCertificate(t *testing.T) {
	r := httptest.NewRequest("POST", "https://core.example/internal/live-data/proposals", nil)
	r.Header.Set("X-Runner-ID", scope().Runner)
	if _, err := AuthenticatedRunner(r); err == nil {
		t.Fatal("header chose runner")
	}
	uri, _ := url.Parse("spiffe://blerg/installation/" + scope().Installation + "/runner/" + scope().Runner)
	leaf := &x509.Certificate{URIs: []*url.URL{uri}}
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	if _, err := AuthenticatedRunner(r); err == nil {
		t.Fatal("unverified certificate accepted")
	}
	r.TLS.VerifiedChains = [][]*x509.Certificate{{leaf}}
	if got, err := AuthenticatedRunner(r); err != nil || got.Runner != scope().Runner {
		t.Fatal("verified runner refused", err)
	}
	uri.Host = "attacker.example"
	if _, err := AuthenticatedRunner(r); err == nil {
		t.Fatal("wrong identity authority accepted")
	}
}

func TestRunnerIdentityShapeBoundaries(t *testing.T) {
	for name, address := range map[string]string{"scheme": "https://blerg/installation/" + scope().Installation + "/runner/" + scope().Runner, "query": "spiffe://blerg/installation/" + scope().Installation + "/runner/" + scope().Runner + "?override=1", "fragment": "spiffe://blerg/installation/" + scope().Installation + "/runner/" + scope().Runner + "#override", "uuid": "spiffe://blerg/installation/invalid/runner/" + scope().Runner} {
		t.Run(name, func(t *testing.T) {
			u, _ := url.Parse(address)
			r := httptest.NewRequest("POST", "https://core.example/", nil)
			r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{URIs: []*url.URL{u}}}}}
			if _, e := AuthenticatedRunner(r); e == nil {
				t.Fatal("invalid runner identity accepted")
			}
		})
	}
	for _, count := range []int{0, 2} {
		r := httptest.NewRequest("POST", "https://core.example/", nil)
		u, _ := url.Parse("spiffe://blerg/installation/" + scope().Installation + "/runner/" + scope().Runner)
		uris := []*url.URL{}
		for range count {
			uris = append(uris, u)
		}
		r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{URIs: uris}}}}
		if _, e := AuthenticatedRunner(r); e == nil {
			t.Fatal("ambiguous runner identity accepted")
		}
	}
}
