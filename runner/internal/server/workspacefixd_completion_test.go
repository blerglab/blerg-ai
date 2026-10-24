package server

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"sync/atomic"
	"testing"
)

func TestP6FinalReceiptCompletesTerminalEffects(t *testing.T) {
	f := newTopicFx(t)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "workspace-desktop")
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='docker' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	f.api.hub.SetSessionOwner(f.sess, dc.ID)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "sandbox", OwnerID: topicOwner, LaunchGeneration: newUUID(), Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "members/one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.sess, m); err != nil {
		t.Fatal(err)
	}
	call := func(peer *DaemonConn, req protocol.LocalWorkspaceControl) protocol.LocalWorkspaceControlResult {
		req.SessionID = f.sess
		req.RequestID = newUUID()
		var reply protocol.LocalWorkspaceControlResult
		if err := db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
			reply = f.api.hub.localWorkspaceControl(ctx, f.pool, peer, req)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	token := newUUID()
	request := protocol.LocalWorkspaceControl{Operation: "launch", Token: token, Manifest: &m}
	launch := call(dc, request)
	if !launch.OK || launch.Launch.RuntimeGeneration != 1 {
		t.Fatalf("launch refused: %+v", launch)
	}
	if retry := call(dc, request); !retry.OK || retry.Launch != launch.Launch {
		t.Fatal("lost launch reply was not idempotent")
	}
	if call(dc, protocol.LocalWorkspaceControl{Operation: "launch", Token: newUUID(), Manifest: &m}).OK {
		t.Fatal("allocated a second writer without fencing")
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: 1}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Inode: 7}}
	request = protocol.LocalWorkspaceControl{Operation: "bind", Launch: launch.Launch, Binding: binding}
	firstBind, secondBind := call(dc, request), call(dc, request)
	if !firstBind.OK || !secondBind.OK {
		t.Fatal("bind/exact retry refused")
	}
	changed := binding
	changed.Root.Inode++
	if call(dc, protocol.LocalWorkspaceControl{Operation: "bind", Launch: launch.Launch, Binding: changed}).OK {
		t.Fatal("changed root accepted")
	}
	results := []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: m.LaunchGeneration, MemberID: "one"}, Branch: "wip/" + f.sess, Pending: true, SyncError: "retained fixture"}}
	if !call(dc, protocol.LocalWorkspaceControl{Operation: "results", Launch: launch.Launch, Binding: binding, Results: results}).OK {
		t.Fatal("results refused")
	}
	message := protocol.SessionEnded{Type: "session_ended", SessionID: f.sess}
	if _, err := localWorkspaceTerminalContext(t.Context(), f.pool, f.sess, message); err == nil {
		t.Fatal("unacknowledged terminal notification accepted")
	}
	final := protocol.WorkspaceFinalization{Binding: binding, Message: message, Results: results}
	request = protocol.LocalWorkspaceControl{Operation: "final", Launch: launch.Launch, Binding: binding, Final: &final}
	var completions atomic.Int32
	f.api.hub.SetCompletionNotifier(func(string) { completions.Add(1) })
	var before int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM session_events WHERE session_id=$1 AND type='state_change'`, f.sess).Scan(&before); err != nil {
		t.Fatal(err)
	}
	reply := call(dc, request)
	if !reply.OK || reply.Receipt.Digest != final.Digest() {
		t.Fatal("no durable completion receipt")
	}
	row, _ := db.GetSession(t.Context(), f.pool, f.sess)
	if got := liveBoardTokens(t, f.pool, f.sess); got != 0 {
		t.Fatalf("receipt left %d live credentials", got)
	}
	if row.EndedAt == nil || row.Status != "stopped" {
		t.Fatal("receipt preceded terminal commit")
	}

	// A received receipt is complete even if raw session_ended never arrives.
	if completions.Load() != 1 {
		t.Fatal("receipt did not deliver completion effects")
	}
	if !call(dc, request).OK {
		t.Fatal("identical receipt retry failed")
	}
	f.api.reconcileLocalWorkspaceFinals(t.Context())
	if completions.Load() != 1 {
		t.Fatal("duplicate receipt repeated completion")
	}
	var after int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM session_events WHERE session_id=$1 AND type='state_change'`, f.sess).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatal("completion state event was not exactly once")
	}
	// Model a lost best-effort terminal notification and subsequent hello.
	reconcileSessions(t.Context(), f.api.hub, f.pool, dc.ID, nil, nil)
	if got := liveBoardTokens(t, f.pool, f.sess); got != 0 {
		t.Fatalf("durably ended session retains %d unrevoked tokens after receipt and reconciliation", got)
	}
}

// A server can die after committing the receipt but before delivering effects.
func TestP6FinalReceiptOutboxSurvivesServerRestart(t *testing.T) {
	f := newTopicFx(t)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "receipt-restart")
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='docker' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: topicOwner, Runtime: "sandbox", LaunchGeneration: newUUID(), Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.sess, m); err != nil {
		t.Fatal(err)
	}
	ask, err := db.CreateMessage(t.Context(), f.pool, f.sess, "ask", "What next?")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.AcceptSessionInput(t.Context(), f.pool, f.sess, "pending", `{"type":"agent_user_message","client_id":"pending","text":"next"}`, newUUID(), "{}"); err != nil {
		t.Fatal(err)
	}
	token := newUUID()
	var l protocol.SupervisorLaunch
	if err = db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
		r := f.api.hub.localWorkspaceControl(ctx, f.pool, dc, protocol.LocalWorkspaceControl{RequestID: newUUID(), SessionID: f.sess, Operation: "launch", Token: token, Manifest: &m})
		l = r.Launch
		if !r.OK {
			t.Fatal("launch")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: l.RuntimeGeneration}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Inode: 7}}
	if err = db.BindWorkspaceRuntime(t.Context(), f.pool, f.sess, b.WorkspaceRuntimeRef, b); err != nil {
		t.Fatal(err)
	}
	final := protocol.WorkspaceFinalization{Binding: b, Message: protocol.SessionEnded{Type: "session_ended", SessionID: f.sess}, Results: []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: m.LaunchGeneration, MemberID: "one"}, Branch: "main"}}}
	// Commit directly at the transaction boundary; no raw terminal event, handler,
	// completion callback, or in-memory owner survives this modeled process death.
	if _, err = db.CommitLocalWorkspaceFinal(t.Context(), f.pool, f.sess, token, final); err != nil {
		t.Fatal(err)
	}
	if got := liveBoardTokens(t, f.pool, f.sess); got != 0 {
		t.Fatal("receipt commit retained credentials", got)
	}
	fresh := NewHub()
	var completions atomic.Int32
	fresh.SetCompletionNotifier(func(string) { completions.Add(1) })
	api := &API{dbPool: f.pool, hub: fresh}
	api.reconcileLocalWorkspaceFinals(t.Context())
	api.reconcileLocalWorkspaceFinals(t.Context())
	message, err := db.GetMessage(t.Context(), f.pool, ask)
	if err != nil || message.Answer == nil {
		t.Fatal("RECEIPT_OUTBOX: open question survived completion", err)
	}
	pending, err := db.PendingSessionInputs(t.Context(), f.pool, f.sess)
	if err != nil || len(pending) != 0 {
		t.Fatal("RECEIPT_OUTBOX: pending input survived completion", err)
	}
	if completions.Load() != 1 {
		t.Fatal("RECEIPT_OUTBOX: duplicate/missing completion", completions.Load())
	}
	var done bool
	if err = f.pool.QueryRow(t.Context(), `SELECT effects_done FROM local_workspace_authority WHERE session_id=$1 AND token=$2`, f.sess, token).Scan(&done); err != nil || !done {
		t.Fatal("outbox not durably acknowledged", err)
	}
}
