package server

// A startup sweep for live files (BLG-470 round 3 #3): a crash between
// UpdateLiveArtifactContent's commit and its rename leaves two things behind that nothing else
// notices until a person happens to look — an orphaned ".live-staging-*" file (the bytes the
// rename never landed) and a live row describing content the disk does not have. This runs once,
// early, logging whatever it finds or removes; it never changes a row, only the files, so a
// mismatch it cannot explain still shows up at serve time (liveFileIntegrityMismatch).

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

const liveStagingPrefix = ".live-staging-"

// removeOrphanedLiveStagingFiles deletes every ".live-staging-*" file under root: a successful
// republish always renames or removes its own staging file before returning, so one still present
// at startup can only be left over from a crash mid-write, with nothing waiting to rename it.
func removeOrphanedLiveStagingFiles(root string) (removed int, err error) {
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), liveStagingPrefix) {
			return nil
		}
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) { //nolint:gosec // path comes from WalkDir under artifactsDir(), our own data directory with no untrusted symlinks
			return fmt.Errorf("remove orphaned staging file %s: %w", path, rmErr)
		}
		removed++
		return nil
	})
	if walkErr != nil {
		return removed, walkErr
	}
	return removed, nil
}

// recheckLiveArtifactsAgainstDisk compares every live row to the bytes at its own path, logging
// each one that does not match (a missing file is reported elsewhere, at serve time, as Gone —
// not a mismatch to repeat here). It changes nothing: a row this finds wrong still needs a person,
// the same as one liveFileIntegrityMismatch catches later on a read.
func recheckLiveArtifactsAgainstDisk(ctx context.Context, pool *pgxpool.Pool) (checked, mismatched int, err error) {
	live, err := db.ListLiveArtifacts(ctx, pool)
	if err != nil {
		return 0, 0, fmt.Errorf("list live artifacts: %w", err)
	}
	for _, a := range live {
		checked++
		f, openErr := os.Open(artifactPath(a.SessionID, a))
		if openErr != nil {
			if os.IsNotExist(openErr) {
				continue // reported at serve time (Gone), not here
			}
			log.Printf("artifacts: startup sweep could not open live file %s/%s (%s): %v", a.SessionID, a.ID, a.Name, openErr)
			continue
		}
		mismatch, detail := liveFileIntegrityMismatch(f, a)
		_ = f.Close()
		if mismatch {
			mismatched++
			log.Printf("artifacts: startup sweep found live file %s/%s (%s) does not match its row on disk (%s); needs a person to repair it",
				a.SessionID, a.ID, a.Name, detail)
		}
	}
	return checked, mismatched, nil
}

// SweepLiveArtifacts runs both checks once, at startup, and logs a one-line summary. Called from
// main after the database connects; cheap enough (a stat and, under the cap, a hash per live row)
// to run inline rather than needing its own retry loop.
func SweepLiveArtifacts(ctx context.Context, pool *pgxpool.Pool) {
	removed, err := removeOrphanedLiveStagingFiles(artifactsDir())
	if err != nil {
		log.Printf("artifacts: startup sweep could not finish removing orphaned staging files: %v", err)
	} else if removed > 0 {
		log.Printf("artifacts: startup sweep removed %d orphaned live-staging file(s)", removed)
	}
	checked, mismatched, err := recheckLiveArtifactsAgainstDisk(ctx, pool)
	if err != nil {
		log.Printf("artifacts: startup sweep could not finish rechecking live files: %v", err)
		return
	}
	log.Printf("artifacts: startup sweep checked %d live file(s), found %d mismatched", checked, mismatched)
}
