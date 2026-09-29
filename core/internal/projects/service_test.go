package projects

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/core/internal/db"
)

// projectsTestSchema is distinct from every other package's test schema name so this
// package's isolated schema never collides with theirs, even though all run against the same
// shared DATABASE_URL (audit M-1: this package used to run its tests directly against the
// shared "public" schema with no isolation).
const projectsTestSchema = "test_blerg_core_projects"

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
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", projectsTestSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", projectsTestSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	setupPool.Close()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = projectsTestSchema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		pool.Exec(bg, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", projectsTestSchema))
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

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestProjectMembershipResolvesCapabilities(t *testing.T) {
	ctx := context.Background()
	st := store(t)
	svc := NewService(st)

	name := "team-alpha-" + randSuffix()
	p, err := svc.Create(ctx, name)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// No explicit cleanup needed: store's isolated schema is dropped entirely on t.Cleanup.

	if err := svc.AddMember(ctx, p.ID, "alice", "owner"); err != nil {
		t.Fatalf("add member: %v", err)
	}
	caps, err := svc.Capabilities(ctx, p.ID, "alice")
	if err != nil {
		t.Fatalf("caps: %v", err)
	}
	if !contains(caps, "card.write") {
		t.Fatalf("owner caps = %v, want card.write", caps)
	}
	// A non-member resolves to no capabilities.
	caps2, err := svc.Capabilities(ctx, p.ID, "mallory")
	if err != nil {
		t.Fatalf("non-member caps: %v", err)
	}
	if len(caps2) != 0 {
		t.Fatalf("non-member caps = %v, want none", caps2)
	}
}

// A lookup that fails is reported, not read as "not a member": a database outage must not
// look like a denial.
func TestCapabilitiesReportsLookupFailure(t *testing.T) {
	svc := NewService(store(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	caps, err := svc.Capabilities(ctx, "any-project", "alice")
	if err == nil {
		t.Fatalf("Capabilities on a cancelled context = %v, nil; want an error", caps)
	}
}
