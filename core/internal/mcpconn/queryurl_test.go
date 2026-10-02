package mcpconn_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

// MINOR 34: rows that predate the query-string rule are counted (the startup warning) without
// their URLs being returned.
func TestCountQueryURLs(t *testing.T) {
	svc, pool := newSvc(t)
	ctx := context.Background()
	acct := newAccount(t, pool)
	for _, name := range []string{"clean", "keyed", "frag"} {
		if _, err := svc.Create(ctx, acct, mcpconn.CreateInput{Name: name, URL: goodURL, AuthKind: "none"}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := svc.CountQueryURLs(ctx); err != nil || n != 0 {
		t.Fatalf("CountQueryURLs = %d, %v; want 0", n, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE mcp_connections SET url = url || '?key=x' WHERE name = 'keyed'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE mcp_connections SET url = url || '#f' WHERE name = 'frag'`); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.CountQueryURLs(ctx); err != nil || n != 2 {
		t.Fatalf("CountQueryURLs = %d, %v; want 2", n, err)
	}
}
