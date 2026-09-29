package discovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
	"github.com/blerglab/blerg-ai/core/internal/db"
)

// discoveryTestSchema is distinct from every other package's test schema name so this
// package's isolated schema never collides with theirs, even though all run against the same
// shared DATABASE_URL (audit M-1: this package used to run its tests directly against the
// shared "public" schema with no isolation).
const discoveryTestSchema = "test_blerg_core_discovery"

// store gives the caller a fresh, isolated, migrated schema (dropped and recreated, then
// dropped again on cleanup) wrapped in a db.Store — the same ConnConfig.RuntimeParams pattern
// established in internal/api/auth_handlers_test.go's testPool.
func store(t *testing.T) db.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()

	setupPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", discoveryTestSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", discoveryTestSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	setupPool.Close()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = discoveryTestSchema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		pool.Exec(bg, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", discoveryTestSchema))
		pool.Close()
	})
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return db.NewPgStore(pool)
}

func randSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func find(agg agentsmanifest.AggregateManifest, name string) *agentsmanifest.ComponentEntry {
	for i := range agg.Components {
		if agg.Components[i].Name == name {
			return &agg.Components[i]
		}
	}
	return nil
}

func TestRegisterAndAggregateMarksStale(t *testing.T) {
	ctx := context.Background()
	st := store(t)
	reg := NewRegistry(st, 50*time.Millisecond)

	// No explicit cleanup needed: store's isolated schema is dropped entirely on t.Cleanup.
	name := "blerg-board-" + randSuffix()

	err := reg.Register(ctx, agentsmanifest.ComponentEntry{
		Name: name, BaseURL: "http://board", Version: "v0.1.0",
		ContractVersion: "v0", Capabilities: []string{"mcp", "forge.change_requests"},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	agg, err := reg.Aggregate(ctx)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	e := find(agg, name)
	if e == nil || e.Stale {
		t.Fatalf("fresh entry missing or stale: %+v", e)
	}
	// After the TTL elapses, the entry is marked stale (not evicted).
	time.Sleep(80 * time.Millisecond)
	agg2, _ := reg.Aggregate(ctx)
	e2 := find(agg2, name)
	if e2 == nil || !e2.Stale {
		t.Fatalf("expected stale-but-present entry, got %+v", e2)
	}
}
