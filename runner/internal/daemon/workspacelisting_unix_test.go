//go:build linux || darwin

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceDarwinRetainedListingDeepDirectory(t *testing.T) {
	e, _, _ := explorerFixture(t)
	name := workspaceLongDirectoryFixture(t, e)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	budget := protocol.WorkspaceSearchVisited
	started := time.Now()
	rows, fingerprint, truncated, err := e.readDirectoryUsing(ctx, name, &budget, workspaceRetainedListing)
	if err != nil || ctx.Err() != nil || len(rows) != 501 || fingerprint == "" || truncated {
		t.Fatalf("Darwin retained listing: err=%v context=%v entries=%d fingerprint=%q truncated=%v elapsed=%v", err, ctx.Err(), len(rows), fingerprint, truncated, time.Since(started))
	}
	t.Logf("Darwin retained listing: %d entries at path length %d in %v", len(rows), len(name), time.Since(started))
}

func TestWorkspaceDarwinRetainedListingMovedAncestors(t *testing.T) {
	for _, schedule := range []string{"outside", "replace-deeper", "out-and-back", "unchanged"} {
		t.Run(schedule, func(t *testing.T) {
			e, root, _ := explorerFixture(t)
			outside := t.TempDir()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(os.MkdirAll(filepath.Join(root, "a", "b"), 0700))
			writeExplorerFile(t, filepath.Join(root, "a", "b"), "visible.txt")
			listing, err := workspaceRetainedListing(e, "a/b")
			must(err)
			defer func() { _ = listing.close() }()
			switch schedule {
			case "replace-deeper":
				must(os.Rename(filepath.Join(root, "a", "b"), filepath.Join(root, "a", "saved")))
				must(os.Mkdir(filepath.Join(root, "a", "b"), 0700))
				writeExplorerFile(t, filepath.Join(root, "a", "b"), "visible.txt")
			case "outside", "out-and-back":
				must(os.Rename(filepath.Join(root, "a"), filepath.Join(outside, "a")))
				if schedule == "out-and-back" {
					must(os.Rename(filepath.Join(outside, "a"), filepath.Join(root, "a")))
					// Some filesystems coalesce timestamps for immediate renames.
					// Give the retained ancestor an observable metadata change.
					changed := time.Now().Add(-time.Hour)
					must(os.Chtimes(filepath.Join(root, "a"), changed, changed))
				}
			}
			file, err := listing.openChild("visible.txt")
			if file != nil {
				_ = file.Close()
			}
			if schedule == "unchanged" {
				must(err)
				if !listing.validate() {
					t.Fatal("unchanged ancestry invalidated")
				}
			} else if err == nil || listing.validate() {
				t.Fatalf("changed ancestry accepted: schedule=%s err=%v", schedule, err)
			}
		})
	}
}
