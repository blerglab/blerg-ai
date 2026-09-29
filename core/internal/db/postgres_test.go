package db

import (
	"context"
	"os"
	"testing"
)

func testDSN(t *testing.T) string {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; run `make db-up` and export it")
	}
	return dsn
}

func TestOpenRunsMigrationsAndPings(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := st.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	// Migrations are idempotent: a second Open must not error.
	st2, err := Open(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("second Open (idempotent migrate): %v", err)
	}
	st2.Close()
}
