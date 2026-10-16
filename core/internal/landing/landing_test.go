package landing_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/landing"
)

func TestAssetHandlerServesBrandTokens(t *testing.T) {
	rec := httptest.NewRecorder()
	landing.AssetHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tokens.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /tokens.css = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "--blaze") {
		t.Fatal("tokens.css does not define the brand accent token")
	}
	for _, p := range []string{"/favicon.svg", "/logo.svg"} {
		rec := httptest.NewRecorder()
		landing.AssetHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, rec.Code)
		}
	}
}

func TestVersionFallsBackToDev(t *testing.T) {
	t.Setenv("BLERG_VERSION", "")
	if got := landing.Version(); got != "dev" {
		t.Fatalf("Version() = %q, want dev", got)
	}
	t.Setenv("BLERG_VERSION", "abc123")
	if got := landing.Version(); got != "abc123" {
		t.Fatalf("Version() = %q, want abc123", got)
	}
}

func TestReleaseAndCommitDefaultEmptyAndHonourEnv(t *testing.T) {
	t.Setenv("BLERG_RELEASE", "")
	t.Setenv("BLERG_COMMIT", "")
	if got := landing.Release(); got != "" {
		t.Fatalf("Release() = %q, want empty when unstamped", got)
	}
	if got := landing.Commit(); got != "" {
		t.Fatalf("Commit() = %q, want empty when unstamped", got)
	}
	t.Setenv("BLERG_RELEASE", "0.2.0")
	t.Setenv("BLERG_COMMIT", "510d0c0")
	if landing.Release() != "0.2.0" || landing.Commit() != "510d0c0" {
		t.Fatalf("Release/Commit = %q/%q, want 0.2.0/510d0c0", landing.Release(), landing.Commit())
	}
}
