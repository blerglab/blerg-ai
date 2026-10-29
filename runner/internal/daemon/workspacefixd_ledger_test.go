package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestP6StatusSharesSessionMetadataAllowance(t *testing.T) {
	root := t.TempDir()
	initChildRepo(t, root)
	index, err := os.Create(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if err = index.Truncate(140 << 20); err != nil {
		t.Fatal(err)
	}
	_ = index.Close()
	st, _ := os.Stat(root)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: "owner", Runtime: "sandbox", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: ".", SourceIdentity: "one", CommonDirectoryIdentity: "one", Destination: "one"}}}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, ManifestDigest: m.Digest, Root: workspaceRootIdentity(st)}
	reader, err := NewLocalExplorer(root, m, b, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if _, safe := reader.statusMetadata(t.Context(), ".git", ""); !safe {
		t.Fatal("source setup was not within its allowance")
	}
	snapshot, err := reader.statusSnapshot(t.Context(), ".", hostWorktreePin{}, false)
	if snapshot != nil {
		snapshot.close()
	}
	if err == nil {
		t.Fatal("AGGREGATE_METADATA: accepted a source plus private copy exceeding the session allowance")
	}
	if reader.ledger == nil || !reader.ledger.empty() {
		t.Fatal("failed snapshot did not release its joined reservation")
	}
}
func TestP6SnapshotEntryReservationAndRelease(t *testing.T) {
	remaining := 1
	ledger := &workspaceSessionLedger{measure: func(context.Context) (sessionGitUsage, error) {
		return sessionGitUsage{entries: protocol.DefaultWorkspaceBudget().MetadataEntries - remaining}, nil
	}}
	if err := ledger.reserve(t.Context(), sessionGitUsage{entries: 1}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.reserve(t.Context(), sessionGitUsage{entries: 1}); err == nil {
		t.Fatal("AGGREGATE_ENTRIES: private entries escaped the session allowance")
	}
	ledger.release(sessionGitUsage{entries: 1})
	if !ledger.empty() {
		t.Fatal("joined cleanup did not return entry allowance")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	ledger.measure = func(ctx context.Context) (sessionGitUsage, error) { return sessionGitUsage{}, ctx.Err() }
	if err := ledger.reserve(canceled, sessionGitUsage{entries: 1}); err == nil || !ledger.empty() {
		t.Fatal("cancellation leaked a reservation")
	}
}
