package db_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestClusterModelCatalogPersistenceAndIsolation(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	first := json.RawMessage(`{"models":[{"id":"example-model","efforts":["low","high"]}]}`)
	newer := json.RawMessage(`{"models":[{"id":"example-new-model"}]}`)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _, found, err := db.LoadClusterModelCatalog(ctx, p, "account-a", "codex", "image-a")
	check(err)
	if found {
		t.Fatal("missing catalog found")
	}
	check(db.StoreClusterModelCatalog(ctx, p, "account-a", "codex", "image-a", first, at))
	check(db.StoreClusterModelCatalog(ctx, p, "account-a", "codex", "image-b", first, at))
	check(db.StoreClusterModelCatalog(ctx, p, "account-b", "codex", "image-a", first, at))
	check(db.StoreClusterModelCatalog(ctx, p, "account-a", "codex", "image-a", newer, at.Add(time.Minute)))
	check(db.StoreClusterModelCatalog(ctx, p, "account-a", "codex", "image-a", first, at))
	raw, fetched, found, err := db.LoadClusterModelCatalog(ctx, p, "account-a", "codex", "image-a")
	check(err)
	var got, want any
	check(json.Unmarshal(raw, &got))
	check(json.Unmarshal(newer, &want))
	aa, _ := json.Marshal(got)
	bb, _ := json.Marshal(want)
	if !found || !fetched.Equal(at.Add(time.Minute)) || string(aa) != string(bb) {
		t.Fatalf("newest catalog was not retained: %s at %s", raw, fetched)
	}
	_, fetched, found, err = db.LoadClusterModelCatalog(ctx, p, "account-b", "codex", "image-a")
	check(err)
	if !found || !fetched.Equal(at) {
		t.Fatal("account isolation lost")
	}
	_, fetched, found, err = db.LoadClusterModelCatalog(ctx, p, "account-a", "codex", "image-b")
	check(err)
	if !found || !fetched.Equal(at) {
		t.Fatal("image isolation lost")
	}
	rows, err := db.ListClusterModelCatalogs(ctx, p, 2)
	check(err)
	if len(rows) != 2 || rows[0].AccountID != "account-a" || rows[0].Image != "image-a" || !rows[0].FetchedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("preload order/bound: %+v", rows)
	}
}

func TestClusterModelCatalogSeparatesEngines(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	_, err := p.Exec(ctx, `INSERT INTO cluster_engine_model_catalogs(account_id,engine,image,payload,fetched_at) VALUES
 ('account-shared','codex','image-shared','{"models":[]}',now()),
 ('account-shared','hermes','image-shared','{"models":[]}',now())`)
	if err != nil {
		t.Fatalf("engine-scoped catalogs rejected: %v", err)
	}
	var count int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM cluster_engine_model_catalogs WHERE account_id='account-shared' AND image='image-shared'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("engine catalogs collided: %d", count)
	}
}

func TestClusterModelCatalogUpgradePreservesLegacyCache(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	// Reconstruct the previous release's schema, then apply the new migration.
	_, err := p.Exec(ctx, `DROP TABLE cluster_engine_model_catalogs; DELETE FROM schema_migrations WHERE filename='048_cluster_engine_model_catalogs.sql'`)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `INSERT INTO cluster_model_catalogs(account_id,image,payload,fetched_at) VALUES('legacy-account','legacy-image','{"models":[{"id":"legacy-model"}]}',now()) ON CONFLICT(account_id,image) DO UPDATE SET payload=EXCLUDED.payload,fetched_at=EXCLUDED.fetched_at`
	if _, err = p.Exec(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err = db.RunMigrations(ctx, p); err != nil {
		t.Fatal(err)
	}
	_, _, found, err := db.LoadClusterModelCatalog(ctx, p, "legacy-account", "codex", "legacy-image")
	if err != nil || !found {
		t.Fatalf("old Codex catalog was not preserved: found=%v err=%v", found, err)
	}
	// An older replica may finish a refresh during the rolling deployment.
	if _, err = p.Exec(ctx, legacy); err != nil {
		t.Fatalf("legacy cache writer broke: %v", err)
	}
}
