package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzSendsCORSForCore(t *testing.T) {
	t.Setenv("BLERG_CORE_PUBLIC_URL", "https://core.example.com/")
	rec := httptest.NewRecorder()
	healthzHandler()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://core.example.com" {
		t.Fatalf("ACAO = %q, want core's origin without the trailing slash", got)
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Fatal("Vary: Origin missing")
	}
}

func TestHealthzHasNoCORSWhenUnconfigured(t *testing.T) {
	t.Setenv("BLERG_CORE_PUBLIC_URL", "")
	rec := httptest.NewRecorder()
	healthzHandler()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("no ACAO header expected when BLERG_CORE_PUBLIC_URL is unset")
	}
}
