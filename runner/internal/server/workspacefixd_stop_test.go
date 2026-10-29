package server

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"testing"
)

func TestP6DesktopStopKeepsOnlyCleanupAuthority(t *testing.T) {
	for _, runtime := range []string{"host", "sandbox"} {
		for _, preparing := range []bool{false, true} {
			name := runtime + "-bound"
			if preparing {
				name = runtime + "-preparing"
			}
			t.Run(name, func(t *testing.T) { testDesktopCleanupAuthority(t, runtime, preparing) })
		}
	}
}
func testDesktopCleanupAuthority(t *testing.T, runtime string, preparing bool) {
	f := newTopicFx(t)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "review-stop-desktop")
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='docker' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	if runtime == "host" {
		if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, f.sess); err != nil {
			t.Fatal(err)
		}
	}
	f.api.hub.SetSessionOwner(f.sess, dc.ID)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: runtime, OwnerID: topicOwner, LaunchGeneration: newUUID(), Digest: "review-stop-digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.sess, m); err != nil {
		t.Fatal(err)
	}
	call := func(req protocol.LocalWorkspaceControl) protocol.LocalWorkspaceControlResult {
		req.SessionID = f.sess
		req.RequestID = newUUID()
		var reply protocol.LocalWorkspaceControlResult
		if err := db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
			reply = f.api.hub.localWorkspaceControl(ctx, f.pool, dc, req)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	token := newUUID()
	launch := call(protocol.LocalWorkspaceControl{Operation: "launch", Token: token, Manifest: &m})
	if !launch.OK {
		t.Fatal("launch failed")
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: launch.Launch.RuntimeGeneration}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Inode: 7}}
	if !preparing && !call(protocol.LocalWorkspaceControl{Operation: "bind", Launch: launch.Launch, Binding: binding}).OK {
		t.Fatal("bind failed")
	}
	current := protocol.LocalWorkspaceControl{Operation: "current", Launch: launch.Launch, Binding: binding}
	if !preparing && !call(current).OK {
		t.Fatal("positive Current failed")
	}
	if err := f.api.stopSession(t.Context(), f.sess, db.SessionEnd{Reason: db.EndReasonStoppedByAgent, ByKind: db.EndedByRunnerKey}); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(t.Context(), f.pool, f.sess)
	if err != nil {
		t.Fatal(err)
	}
	after := call(current)
	if row.EndedAt == nil || after.OK {
		t.Fatal("stop did not immediately revoke ordinary admission")
	}
	if preparing {
		// Lost bind reply / interrupted preparation can only bind for cleanup.
		cleanupLaunch := call(protocol.LocalWorkspaceControl{Operation: "cleanup_launch", Token: "preparation-cleanup", Manifest: &m})
		if cleanupLaunch.OK {
			t.Fatal("cleanup replaced an active preparation token")
		}
		if !call(protocol.LocalWorkspaceControl{Operation: "cleanup_bind", Launch: launch.Launch, Binding: binding}).OK {
			t.Fatal("PREPARED_CLEANUP: unable to bind exact preparation after stop")
		}
		if call(current).OK {
			t.Fatal("cleanup bind reopened ordinary authority")
		}
	}
	cleanup := current
	cleanup.Operation = "cleanup_current"
	if !call(cleanup).OK {
		t.Fatal("STOP_AUTHORITY: exact desktop cleanup was revoked before settlement")
	}
	cleanup.Binding.Root.Inode++
	if call(cleanup).OK {
		t.Fatal("cleanup admitted a replaced root")
	}
	cleanup = current
	cleanup.Operation = "cleanup_results"
	cleanup.Results = []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "one", LaunchGeneration: m.LaunchGeneration}, Pending: true, SyncError: "retained"}}
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET discard_requested_at=now() WHERE id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	if !call(cleanup).OK {
		t.Fatal("discard lost exact member settlement authority")
	}
	final := protocol.WorkspaceFinalization{Binding: binding, Message: protocol.SessionEnded{Type: "session_ended", SessionID: f.sess}, Results: cleanup.Results}
	if !call(protocol.LocalWorkspaceControl{Operation: "final", Launch: launch.Launch, Binding: binding, Final: &final}).OK {
		t.Fatal("discard cleanup could not commit immutable results")
	}
	if call(cleanup).OK {
		t.Fatal("cleanup rewrote an immutable completion")
	}

}
