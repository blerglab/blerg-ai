package discovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
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
	reg := NewRegistry(st, time.Hour)

	// No explicit cleanup needed: store's isolated database is dropped on t.Cleanup.
	name := "blerg-board-" + randSuffix()

	err := reg.Register(ctx, agentsmanifest.ComponentEntry{
		Name: name, BaseURL: "http://board", Version: "v0.1.0",
		ContractVersion: "v0", Capabilities: []string{"mcp", "forge.change_requests"},
		UI: &agentsmanifest.UIInfo{ChatPackage: &agentsmanifest.PackageInfo{Name: "@blerglab/chat", Version: "0.1.0", URL: "http://board/packages/chat.tgz"}},
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
	// The self-described UI package survives the round trip through the stored manifest.
	if e.UI == nil || e.UI.ChatPackage == nil || e.UI.ChatPackage.Name != "@blerglab/chat" {
		t.Fatalf("ui package lost in aggregate: %+v", e.UI)
	}
	// Age the stored heartbeat explicitly so suite load cannot make the fresh
	// assertion race a short wall-clock TTL. Stale entries remain present.
	if _, err := st.Pool().Exec(ctx, `UPDATE components SET last_seen = now() - interval '2 hours' WHERE name = $1`, name); err != nil {
		t.Fatalf("age heartbeat: %v", err)
	}
	agg2, err := reg.Aggregate(ctx)
	if err != nil {
		t.Fatalf("aggregate aged entry: %v", err)
	}
	e2 := find(agg2, name)
	if e2 == nil || !e2.Stale {
		t.Fatalf("expected stale-but-present entry, got %+v", e2)
	}
}
