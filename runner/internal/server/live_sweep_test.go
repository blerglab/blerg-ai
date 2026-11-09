package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The startup sweep (BLG-470 round 3 #3) must remove an orphaned ".live-staging-*" file left by a
// crash mid-write, without touching the real file sitting right next to it.
func TestRemoveOrphanedLiveStagingFilesRemovesOnlyStagingFiles(t *testing.T) {
	fx := newArtFx(t)
	live, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("real bytes"))
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("live publish = %d: %s", rec.Code, rec.Body.String())
	}
	dir := fx.path(fx.sess, live.ID)
	orphan := filepath.Join(dir, ".live-staging-dead-attempt")
	if err := os.WriteFile(orphan, []byte("never renamed"), 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := removeOrphanedLiveStagingFiles(artifactsDir())
	if err != nil {
		t.Fatalf("removeOrphanedLiveStagingFiles: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d; want 1", removed)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan staging file still exists after the sweep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "chart.png")); err != nil {
		t.Fatalf("the real file was touched by the sweep: %v", err)
	}

	// Idempotent: a second pass with nothing left to remove must not error or find anything.
	removed, err = removeOrphanedLiveStagingFiles(artifactsDir())
	if err != nil || removed != 0 {
		t.Fatalf("second pass: removed=%d err=%v; want 0, nil", removed, err)
	}
}

// recheckLiveArtifactsAgainstDisk must find a live file whose bytes no longer match its row —
// the state a crash between the commit and the rename leaves behind — and must not flag a clean
// one.
func TestRecheckLiveArtifactsAgainstDiskFindsAMismatchButNotAHealthyFile(t *testing.T) {
	fx := newArtFx(t)
	healthy, rec := fx.publishLive(fx.sessTok, fx.sess, "ok.png", []byte("fine"))
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("live publish (healthy) = %d: %s", rec.Code, rec.Body.String())
	}
	broken, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("the committed bytes"))
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("live publish (broken) = %d: %s", rec.Code, rec.Body.String())
	}
	if err := os.WriteFile(filepath.Join(fx.path(fx.sess, broken.ID), "chart.png"), []byte("stale, never renamed"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = healthy

	checked, mismatched, err := recheckLiveArtifactsAgainstDisk(context.Background(), fx.api.dbPool)
	if err != nil {
		t.Fatalf("recheckLiveArtifactsAgainstDisk: %v", err)
	}
	if checked != 2 {
		t.Fatalf("checked = %d; want 2 (one healthy, one tampered)", checked)
	}
	if mismatched != 1 {
		t.Fatalf("mismatched = %d; want exactly 1 (only the tampered file)", mismatched)
	}
}
