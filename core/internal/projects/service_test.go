package projects

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
)

// store provides a fresh database cloned from the migrated package template.
func store(t *testing.T) db.Store {
	t.Helper()
	return db.NewPgStore(testsupport.Pool(t, db.Migrate))
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
