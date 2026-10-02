package api

import (
	"net/http/httptest"
	"testing"
)

// The /internal routes are for components inside the cluster or compose network, which call core directly.
// A request that came through a reverse proxy or ingress (it carries a forwarding header) is refused even with
// the right key, so a public ingress that routes the whole core host cannot expose them.
func TestValidInternalKeyRefusesProxiedRequests(t *testing.T) {
	const key = "k-internal-123"
	direct := httptest.NewRequest("POST", "/internal/tokens/status", nil)
	direct.Header.Set("Authorization", "Bearer "+key)
	if !validInternalKey(direct, key) {
		t.Fatal("a direct call with the right key must be accepted")
	}
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip", "Forwarded"} {
		r := httptest.NewRequest("POST", "/internal/tokens/status", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set(h, "203.0.113.9")
		if validInternalKey(r, key) {
			t.Errorf("a request carrying %s came through a proxy and must be refused", h)
		}
	}
	// the key itself is still checked
	bad := httptest.NewRequest("POST", "/internal/tokens/status", nil)
	bad.Header.Set("Authorization", "Bearer wrong")
	if validInternalKey(bad, key) {
		t.Error("a wrong key must be refused")
	}
}
