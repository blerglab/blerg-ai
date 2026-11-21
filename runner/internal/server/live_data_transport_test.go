package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLiveApprovalDedicatedTransport(t *testing.T) {
	fallbackCalls, dedicatedCalls := 0, 0
	transportCalls := 0
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fallbackCalls++; w.WriteHeader(500) }))
	defer fallback.Close()
	dedicated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { dedicatedCalls++; _, _ = w.Write([]byte(`{}`)) }))
	defer dedicated.Close()
	api := &API{coreURL: fallback.URL, coreInternalKey: "fixture-component"}
	var out struct{}
	if e := api.liveCore(context.Background(), "/internal/live-data/receipt", struct{}{}, &out); e == nil {
		t.Fatal("missing dedicated transport accepted")
	}
	if fallbackCalls != 0 {
		t.Fatal("public Core fallback used")
	}
	api.SetLiveDataApprovalURL(dedicated.URL)
	client := dedicated.Client()
	client.Transport = liveFixtureTransport(func(r *http.Request) (*http.Response, error) {
		transportCalls++
		return http.DefaultTransport.RoundTrip(r)
	})
	api.SetLiveDataApprovalClient(client)
	if e := api.liveCore(context.Background(), "/internal/live-data/receipt", struct{}{}, &out); e != nil {
		t.Fatal(e)
	}
	if dedicatedCalls != 1 || fallbackCalls != 0 || transportCalls != 1 {
		t.Fatal("approval used wrong transport")
	}
}

type liveFixtureTransport func(*http.Request) (*http.Response, error)

func (f liveFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLiveApprovalTLSRefusalCause(t *testing.T) {
	api := &API{liveDataCoreURL: "https://core.example", coreInternalKey: "fixture-component"}
	api.SetLiveDataApprovalClient(&http.Client{Transport: liveFixtureTransport(func(*http.Request) (*http.Response, error) {
		return nil, &tls.CertificateVerificationError{Err: errors.New("fixture TLS refusal")}
	})})
	var out struct{}
	e := api.liveCore(context.Background(), "/internal/live-data/approve", struct{}{}, &out)
	var failure *boundAskError
	if !errors.As(e, &failure) || failure.cause != "live_service_tls_refused" {
		t.Fatalf("TLS refusal cause was %v", e)
	}
}
