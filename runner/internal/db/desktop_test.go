package db_test

import (
	"os"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestDesktopOwnerRevisionAndNameIsolation(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	legacy := newUUID(t, pool)
	if err := db.UpsertDaemon(ctx, pool, legacy, "workstation", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	before, err := db.GetDesktop(ctx, pool, legacy)
	if err != nil || before.OwnerID != "" || before.Revision != 1 {
		t.Fatalf("legacy migration: %+v %v", before, err)
	}
	a := desktopauth.Binding{DesktopID: legacy, OwnerID: "person-a", Revision: 1, TokenID: "a", ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.BindDesktopConnection(ctx, pool, a, "workstation", "local", "/repos", newUUID(t, pool)); err == nil {
		t.Fatal("credential adopted an unknown desktop without assignment")
	}
	assigned, err := db.AssignDesktop(ctx, pool, legacy, a.OwnerID, 1)
	if err != nil || assigned.ID != legacy || assigned.Revision != 2 {
		t.Fatalf("assignment: %+v %v", assigned, err)
	}
	a.Revision = 2
	gen := newUUID(t, pool)
	if err := db.BindDesktopConnection(ctx, pool, a, "workstation", "local", "/repos", gen); err != nil {
		t.Fatal(err)
	}
	b := a
	b.DesktopID = newUUID(t, pool)
	b.OwnerID = "person-b"
	b.Revision = 1
	if err := db.BindDesktopConnection(ctx, pool, b, "workstation", "local", "/different", newUUID(t, pool)); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{a.OwnerID, b.OwnerID} {
		got, err := db.ListDesktops(ctx, pool, owner, false)
		if err != nil || len(got) != 1 || got[0].OwnerID != owner {
			t.Fatalf("owner list: %+v %v", got, err)
		}
	}
	forged := a
	forged.DesktopID = b.DesktopID
	if db.BindDesktopConnection(ctx, pool, forged, "workstation", "local", "/forged", newUUID(t, pool)) == nil {
		t.Fatal("another person's credential took over a desktop")
	}
	if db.ValidateDesktopConnection(ctx, pool, a, gen) != nil {
		t.Fatal("valid current binding rejected")
	}
	next, err := db.AssignDesktop(ctx, pool, legacy, b.OwnerID, 2)
	if err != nil || next.Revision != 3 || next.Generation != "" {
		t.Fatalf("revision: %+v %v", next, err)
	}
	if db.ValidateDesktopConnection(ctx, pool, a, gen) == nil {
		t.Fatal("old connection survived ownership change")
	}
	if db.BindDesktopConnection(ctx, pool, a, "workstation", "local", "/repos", newUUID(t, pool)) == nil {
		t.Fatal("old token reconnected after owner change")
	}
	if _, err := db.AssignDesktop(ctx, pool, legacy, a.OwnerID, 2); err == nil {
		t.Fatal("stale assignment accepted")
	}
}

func TestDesktopMigrationPreservesExistingDesktop(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	// Reconstruct the pre-077 columns inside this test's isolated database.
	if _, err := pool.Exec(ctx, `ALTER TABLE daemons DROP COLUMN desktop_owner_id,DROP COLUMN ownership_revision,DROP COLUMN connection_generation`); err != nil {
		t.Fatal(err)
	}
	id := newUUID(t, pool)
	if err := db.UpsertDaemon(ctx, pool, id, "existing-workstation", "local", "/repos/existing"); err != nil {
		t.Fatal(err)
	}
	session := mustSession(t, pool, id, "person-a")
	sql, err := os.ReadFile("migrations/077_desktop_ownership.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	d, err := db.GetDesktop(ctx, pool, id)
	if err != nil || d.ID != id || d.OwnerID != "" || d.Revision != 1 {
		t.Fatalf("migrated desktop %+v %v", d, err)
	}
	row, err := db.GetSession(ctx, pool, session)
	if err != nil || row.DaemonID != id {
		t.Fatal("migration changed existing session binding")
	}
}

func TestDesktopUpgradeNeverGuessesAnAmbiguousLegacyName(t *testing.T) {
	pool := connect(t)
	for range 2 {
		if err := db.UpsertDaemon(t.Context(), pool, newUUID(t, pool), "same-name", "local", "/repos"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.FindUnownedDesktopByName(t.Context(), pool, "same-name"); err == nil {
		t.Fatal("ambiguous legacy identity was guessed")
	}
}
