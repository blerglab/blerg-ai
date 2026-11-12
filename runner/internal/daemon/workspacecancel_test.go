package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestExplorerListCancellationStopsWithinBatch(t *testing.T) {
	e, root, _ := explorerFixture(t)
	for _, name := range []string{"one.txt", "two.txt", "three.txt"} {
		writeExplorerFile(t, root, name)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opened := 0
	budget := 100
	_, _, _, err := e.readDirectoryUsing(ctx, ".", &budget, func(e *LocalExplorer, name string) (*workspaceListing, error) {
		listing, err := workspaceOpenListing(e, name)
		if err != nil {
			return nil, err
		}
		open := listing.openChild
		listing.openChild = func(name string) (*os.File, error) { opened++; cancel(); return open(name) }
		return listing, nil
	})
	if !errors.Is(err, context.Canceled) || opened != 1 {
		t.Fatalf("canceled list: opened=%d err=%v", opened, err)
	}
}

func TestExplorerLargeRepositoryListIsBounded(t *testing.T) {
	e, root, req := explorerFixture(t)
	for i := 0; i < protocol.WorkspaceSearchVisited+100; i++ {
		writeExplorerFile(t, root, fmt.Sprintf("entry-%05d.txt", i))
	}
	budget := protocol.WorkspaceSearchVisited
	rows, _, truncated, err := e.readDirectory(t.Context(), ".", &budget)
	if err != nil || !truncated || len(rows) > protocol.WorkspaceSearchVisited || budget != 0 {
		t.Fatalf("bounded traversal: rows=%d remaining=%d truncated=%v err=%v", len(rows), budget, truncated, err)
	}
	res := e.Explore(t.Context(), req)
	if !res.OK || !res.Truncated || len(res.Entries) != protocol.WorkspaceExplorerEntries {
		t.Fatalf("bounded page: ok=%v truncated=%v rows=%d", res.OK, res.Truncated, len(res.Entries))
	}
}
