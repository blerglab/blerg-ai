package db

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/testsupport"
)

func testDSN(t *testing.T) string {
	t.Helper()
	return testsupport.Empty(t).ConnString()
}

func TestOpenRunsMigrationsAndPings(t *testing.T) {
	ctx := context.Background()
	dsn := testDSN(t)
	st, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := st.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	// Migrations are idempotent: a second Open must not error.
	st2, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("second Open (idempotent migrate): %v", err)
	}
	st2.Close()
}
