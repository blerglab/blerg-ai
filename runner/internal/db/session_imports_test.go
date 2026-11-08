package db_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/fileview"
)

func TestNextImportNumberStartsAtOneAndAdvancesAfterReport(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "2a2a2a2a-2a2a-4a2a-8a2a-2a2a2a2a2a2a", "2b2b2b2b-2b2b-4b2b-8b2b-2b2b2b2b2b2b"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}

	n, err := db.NextImportNumber(ctx, pool, session)
	if err != nil || n != 1 {
		t.Fatalf("NextImportNumber (fresh child) = %d, %v; want 1", n, err)
	}

	if err := db.ReportImport(ctx, pool, session, "art-1", 1, 1); err != nil {
		t.Fatal(err)
	}
	n, err = db.NextImportNumber(ctx, pool, session)
	if err != nil || n != 2 {
		t.Fatalf("NextImportNumber (after one report) = %d, %v; want 2", n, err)
	}

	// Reporting the same (artifact, version) again must be a harmless no-op, not a second row.
	if err := db.ReportImport(ctx, pool, session, "art-1", 1, 1); err != nil {
		t.Fatal(err)
	}
	n, err = db.NextImportNumber(ctx, pool, session)
	if err != nil || n != 2 {
		t.Fatalf("NextImportNumber (after a repeated report) = %d, %v; want still 2", n, err)
	}
}

func TestUnimportedBundlesListsOnlyWhatHasNotBeenReported(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "2c2c2c2c-2c2c-4c2c-8c2c-2c2c2c2c2c2c", "2d2d2d2d-2d2d-4d2d-8d2d-2d2d2d2d2d2d"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}

	bundleA, err := db.InsertArtifact(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 1), SessionID: session, Name: "a.bundle", Size: 1, ContentType: fileview.GitBundle, SHA256: "x",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	bundleB, err := db.InsertArtifact(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 2), SessionID: session, Name: "b.bundle", Size: 1, ContentType: fileview.GitBundle, SHA256: "y",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// An ordinary file (not a bundle) must never show up as "outstanding."
	if _, err := db.InsertArtifact(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 3), SessionID: session, Name: "c.png", Size: 1, ContentType: "image/png", SHA256: "z",
	}, 0); err != nil {
		t.Fatal(err)
	}

	out, err := db.UnimportedBundles(ctx, pool, session)
	if err != nil || len(out) != 2 {
		t.Fatalf("UnimportedBundles (none reported) = %d, %v; want 2", len(out), err)
	}

	if err := db.ReportImport(ctx, pool, session, bundleA.ID, bundleA.Version, 1); err != nil {
		t.Fatal(err)
	}
	out, err = db.UnimportedBundles(ctx, pool, session)
	if err != nil || len(out) != 1 || out[0].ID != bundleB.ID {
		t.Fatalf("UnimportedBundles (one reported) = %v, %v; want just %s outstanding", out, err, bundleB.ID)
	}

	if err := db.ReportImport(ctx, pool, session, bundleB.ID, bundleB.Version, 2); err != nil {
		t.Fatal(err)
	}
	out, err = db.UnimportedBundles(ctx, pool, session)
	if err != nil || len(out) != 0 {
		t.Fatalf("UnimportedBundles (both reported) = %d, %v; want 0", len(out), err)
	}
}
