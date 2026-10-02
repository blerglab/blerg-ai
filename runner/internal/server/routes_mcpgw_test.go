package server

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// lazyPool builds a pool that never connects unless used; the requests below are all
// rejected before the database is consulted.
func lazyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestStartMCPGatewayDisabledWithoutConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := lazyPool(t)
	for name, tc := range map[string]struct {
		env       map[string]string
		pool      *pgxpool.Pool
		core, key string
	}{
		"addr unset":      {map[string]string{}, pool, "http://core", "k"},
		"no pool":         {map[string]string{"BLERG_RUNNER_MCP_GW_ADDR": "127.0.0.1:0"}, nil, "http://core", "k"},
		"no core url":     {map[string]string{"BLERG_RUNNER_MCP_GW_ADDR": "127.0.0.1:0"}, pool, "", "k"},
		"no internal key": {map[string]string{"BLERG_RUNNER_MCP_GW_ADDR": "127.0.0.1:0"}, pool, "http://core", ""},
		"bad address":     {map[string]string{"BLERG_RUNNER_MCP_GW_ADDR": "not-an-address"}, pool, "http://core", "k"},
	} {
		if h := startMCPGateway(ctx, envMap(tc.env), tc.pool, tc.core, tc.key); h != nil {
			t.Errorf("%s: gateway must be disabled", name)
		}
	}
}

func TestStartMCPGatewayServesOnItsOwnListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := startMCPGateway(ctx, envMap(map[string]string{
		"BLERG_RUNNER_MCP_GW_ADDR":        "127.0.0.1:0",
		"BLERG_RUNNER_MCP_MAX_CONCURRENT": "2",
	}), lazyPool(t), "http://core.invalid", "key")
	if h == nil || h.Gateway == nil || h.Addr == nil {
		t.Fatal("gateway should be running")
	}
	base := "http://" + h.Addr.String()

	// No token: refused before anything else, and only the gateway path exists on this listener.
	resp, err := http.Post(base+"/mcp-gw/alpha", "application/json", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", resp.StatusCode)
	}
	for _, path := range []string{"/healthz", "/api/sessions", "/mcp", "/"} {
		r, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		if r.StatusCode != http.StatusNotFound && r.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s on the gateway listener: %d, the control plane must not be reachable here", path, r.StatusCode)
		}
	}

	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := (&http.Client{Timeout: time.Second}).Post(base+"/mcp-gw/alpha", "application/json", http.NoBody)
		if err != nil {
			break // listener closed
		}
		_ = c.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("the gateway server must stop with its context")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEnvPositiveInt(t *testing.T) {
	env := envMap(map[string]string{"A": "5", "B": "-1", "C": "x", "D": "0"})
	if envPositiveInt(env, "A") != 5 || envPositiveInt(env, "B") != 0 || envPositiveInt(env, "C") != 0 ||
		envPositiveInt(env, "D") != 0 || envPositiveInt(env, "missing") != 0 {
		t.Error("only positive integers are accepted")
	}
}
