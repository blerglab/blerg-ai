package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Exercise the real hello/heartbeat reconciliation and the real ended-report
// predicate. The fixture is an independently serialized authenticated P2
// journal, so reconstruction cannot rely on an in-memory live agent.
func TestWorkspaceFinalOutageLivenessPreservesDelayedResults(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := t.Context()
	const owner = "00000000-0000-4000-8000-0000000000d1"
	const id = "00000000-0000-4000-8000-0000000000d2"
	if err := db.UpsertDaemon(ctx, pool, owner, "workspace-test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, id, owner, "running", "/workspace", "", "Workspace", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET started_at=now()-interval '10 minutes',spawning_account_id='owner',runtime='sandbox' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "sandbox", LaunchGeneration: "selection", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if err := db.InsertSessionWorkspace(ctx, pool, id, m); err != nil {
		t.Fatal(err)
	}
	generation, err := db.BeginWorkspaceRuntime(ctx, pool, id, m.LaunchGeneration, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: generation}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Inode: 1}}
	if err := db.BindWorkspaceRuntime(ctx, pool, id, b.WorkspaceRuntimeRef, b); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	t.Setenv(daemon.DaemonStateDirEnv, stateDir)
	directory := filepath.Join(stateDir, "sandbox-clones")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	key := []byte("test-only-private-journal-key-00")
	if err := os.WriteFile(filepath.Join(directory, "workspace-integrity.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	message := protocol.SessionEnded{Type: "session_ended", SessionID: id, ExitCode: 1, Message: "exact retained outcome"}
	results := []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "one", LaunchGeneration: m.LaunchGeneration}, Base: "base", Head: "saved", Branch: "wip/test", Pending: true, RecoveryLocation: "/private/recovery"}}
	raw, err := json.Marshal(map[string]any{"version": 2, "session": id, "launch": m, "binding": b, "stopped": true, "final_delivery": message, "final_results": results})
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	envelope, _ := json.Marshal(map[string]any{"state": json.RawMessage(raw), "mac": hex.EncodeToString(mac.Sum(nil))})
	if err := os.WriteFile(filepath.Join(directory, id+".workspace.json"), envelope, 0600); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	// Both hello (before reattach) and heartbeat must retain a pending result,
	// despite being much older than the unreported grace period.
	for range 2 {
		fresh := &daemon.LocalWorkspaceService{}
		ids := fresh.PendingIDs()
		if len(ids) != 1 || ids[0] != id {
			t.Fatalf("pending finalization absent before hello: %v", ids)
		}
		reconcileSessions(ctx, hub, pool, owner, ids, nil)
	}
	if err := db.SetWorkspaceRuntimeResults(ctx, pool, id, b.WorkspaceRuntimeRef, results); err != nil {
		t.Fatal("outage made final results ineligible", err)
	}
	stale := b.WorkspaceRuntimeRef
	stale.Generation++
	if err := db.SetWorkspaceRuntimeResults(ctx, pool, id, stale, results); err == nil {
		t.Fatal("stale generation passed after outage")
	}
	HandleSessionEnded(ctx, hub, pool, message)
	workspace, err := db.GetSessionWorkspace(ctx, pool, id)
	if err != nil || len(workspace.Results) != 1 || workspace.Results[0].Head != "saved" {
		t.Fatal("final results lost", err)
	}
	if err := db.SetWorkspaceRuntimeResults(ctx, pool, id, b.WorkspaceRuntimeRef, results); err == nil {
		t.Fatal("real ended predicate did not reject late ordinary results")
	}
}
