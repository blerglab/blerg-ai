package mcpgw

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// A token that was served and is then refused (grant deleted, refused by the public route's
// accept, or presented on another connection's path) must leave the recent-grant cache at once,
// so it never again skips the global lookup budget (review I1).
func TestRecentGrantForgottenOnRefusal(t *testing.T) {
	h := newHarness(t, nil)
	var accept atomic.Bool
	accept.Store(true)
	pub := httptest.NewServer(h.gw.PublicHandler(func(context.Context, *db.MCPGrant) (bool, error) { return accept.Load(), nil }))
	t.Cleanup(pub.Close)
	ping := func(token, name string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, pub.URL+PathPrefix+name,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	cached := func(gh grantHandle) bool {
		return h.gw.recent.contains(sha256.Sum256([]byte(gh.token)), h.clock.Now())
	}
	serve := func(gh grantHandle) {
		t.Helper()
		if got := ping(gh.token, gh.name); got != http.StatusOK {
			t.Fatalf("valid token: %d, want 200", got)
		}
		if !cached(gh) {
			t.Fatal("a served token was not remembered")
		}
	}

	t.Run("accept refuses", func(t *testing.T) {
		gh := h.grant("refused", map[string]string{"echo": "allow"}, 0)
		serve(gh)
		accept.Store(false)
		defer accept.Store(true)
		if got := ping(gh.token, gh.name); got != http.StatusUnauthorized {
			t.Fatalf("refused grant: %d, want 401", got)
		}
		if cached(gh) {
			t.Fatal("a grant refused by accept still bypasses the lookup budget")
		}
	})
	t.Run("wrong path", func(t *testing.T) {
		gh := h.grant("pathcheck", map[string]string{"echo": "allow"}, 0)
		serve(gh)
		if got := ping(gh.token, "another"); got != http.StatusUnauthorized {
			t.Fatalf("wrong path: %d, want 401", got)
		}
		if cached(gh) {
			t.Fatal("a token presented on another connection's path still bypasses the lookup budget")
		}
	})
	t.Run("grant deleted", func(t *testing.T) {
		gh := h.grant("deleted", map[string]string{"echo": "allow"}, 0)
		serve(gh)
		if _, err := h.pool.Exec(context.Background(), `DELETE FROM session_mcp_grants WHERE session_id = $1`, gh.sessionID); err != nil {
			t.Fatal(err)
		}
		if got := ping(gh.token, gh.name); got != http.StatusUnauthorized {
			t.Fatalf("deleted grant: %d, want 401", got)
		}
		if cached(gh) {
			t.Fatal("a revoked token still bypasses the lookup budget")
		}
	})
}

// When the public route's accept cannot answer (its database failed), the request gets a
// retryable 503: a valid token keeps its place in the recent-grant cache and no failure is
// counted against it or its address (security N2).
func TestPublicAcceptErrorIsNotARefusal(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.FailedAuthLimit = 3 })
	var broken atomic.Bool
	pub := httptest.NewServer(h.gw.PublicHandler(func(context.Context, *db.MCPGrant) (bool, error) {
		if broken.Load() {
			return false, errors.New("database unavailable")
		}
		return true, nil
	}))
	t.Cleanup(pub.Close)
	ping := func(gh grantHandle) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, pub.URL+PathPrefix+gh.name,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+gh.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	gh := h.grant("flaky", map[string]string{"echo": "allow"}, 0)
	if got := ping(gh).StatusCode; got != http.StatusOK {
		t.Fatalf("valid token: %d, want 200", got)
	}
	broken.Store(true)
	for range 2 * h.gw.cfg.FailedAuthLimit {
		resp := ping(gh)
		if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
			t.Fatalf("accept error: %d (Retry-After %q), want a retryable 503", resp.StatusCode, resp.Header.Get("Retry-After"))
		}
	}
	if !h.gw.recent.contains(sha256.Sum256([]byte(gh.token)), h.clock.Now()) {
		t.Fatal("a database error dropped a valid token from the recent-grant cache")
	}
	h.gw.limiter.mu.Lock()
	tokenFails := len(h.gw.limiter.byKey)
	h.gw.limiter.mu.Unlock()
	h.gw.ipLimiter.mu.Lock()
	addrFails := len(h.gw.ipLimiter.byKey)
	h.gw.ipLimiter.mu.Unlock()
	if tokenFails != 0 || addrFails != 0 {
		t.Fatalf("a database error was counted as a failure: %d token, %d address", tokenFails, addrFails)
	}
	broken.Store(false)
	if got := ping(gh).StatusCode; got != http.StatusOK {
		t.Fatalf("after the database recovered: %d, want 200", got)
	}
}
