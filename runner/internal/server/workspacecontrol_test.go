package server

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestLocalWorkspaceAuthenticatedCompletionAndRecovery(t *testing.T) {
	f := newTopicFx(t)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "workspace-desktop")
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='docker' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	f.api.hub.SetSessionOwner(f.sess, dc.ID)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "sandbox", OwnerID: topicOwner, LaunchGeneration: newUUID(), Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
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
	reply := call(dc, request)
	if !reply.OK || reply.Receipt.Digest != final.Digest() {
		t.Fatal("no durable completion receipt")
	}
	row, _ := db.GetSession(t.Context(), f.pool, f.sess)
	if row.EndedAt == nil || row.Status != "stopped" {
		t.Fatal("receipt preceded terminal commit")
	}
	ended := *row.EndedAt
	dc.DesktopGeneration = newUUID()
	if err := db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
		return db.BindDesktopConnection(ctx, f.pool, *dc.DesktopBinding, dc.Name, dc.Mode, dc.ReposRoot, dc.DesktopGeneration)
	}); err != nil {
		t.Fatal(err)
	}
	if !call(dc, request).OK {
		t.Fatal("new authenticated connection could not reconcile lost receipt after end")
	}
	if call(dc, protocol.LocalWorkspaceControl{Operation: "current", Launch: launch.Launch, Binding: binding}).OK {
		t.Fatal("ordinary Current accepted ended session")
	}
	if call(dc, protocol.LocalWorkspaceControl{Operation: "results", Launch: launch.Launch, Binding: binding, Results: results}).OK {
		t.Fatal("ordinary Results accepted ended session")
	}
	final.Message.ExitCode = 1
	if call(dc, request).OK {
		t.Fatal("conflicting outcome replaced durable completion")
	}
	final.Message.ExitCode = 0
	ctx, err := localWorkspaceTerminalContext(t.Context(), f.pool, f.sess, message)
	if err != nil {
		t.Fatal(err)
	}
	HandleSessionEnded(ctx, f.api.hub, f.pool, message)
	row, _ = db.GetSession(t.Context(), f.pool, f.sess)
	saved, _ := db.GetSessionWorkspace(t.Context(), f.pool, f.sess)
	if !row.EndedAt.Equal(ended) || !reflect.DeepEqual(saved.Results, results) {
		t.Fatal("post-commit notification rewrote durable evidence")
	}
	foreign := workspaceOwnedDesktop(t, f.api.hub, f.pool, privAcctB, "other-desktop")
	if call(foreign, request).OK {
		t.Fatal("foreign owner reconciled completion")
	}
	legacy := DaemonConn{ID: dc.ID}
	if call(&legacy, request).OK {
		t.Fatal("unknown ownership accepted")
	}
	// Stored JSON round trips retain the exact digest across journal/server types.
	raw, _ := json.Marshal(final)
	var round protocol.WorkspaceFinalization
	if json.Unmarshal(raw, &round) != nil || round.Digest() != reply.Receipt.Digest {
		t.Fatal("completion encoding changed")
	}
}

// Current says definitively when a launch is over (the daemon then keeps its writers stopped) and
// leaves every other failure unmarked, so the daemon retries instead of wedging the session.
func TestLocalWorkspaceCurrentRefusesOnlyDefinitively(t *testing.T) {
	f := newTopicFx(t)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "refusal-desktop")
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='docker' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	f.api.hub.SetSessionOwner(f.sess, dc.ID)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "sandbox", OwnerID: topicOwner, LaunchGeneration: newUUID(), Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.sess, m); err != nil {
		t.Fatal(err)
	}
	call := func(req protocol.LocalWorkspaceControl) protocol.LocalWorkspaceControlResult {
		req.SessionID, req.RequestID = f.sess, newUUID()
		var reply protocol.LocalWorkspaceControlResult
		if err := db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
			reply = f.api.hub.localWorkspaceControl(ctx, f.pool, dc, req)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	launch := call(protocol.LocalWorkspaceControl{Operation: "launch", Token: newUUID(), Manifest: &m})
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: launch.Launch.RuntimeGeneration}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Inode: 7}}
	if !launch.OK || !call(protocol.LocalWorkspaceControl{Operation: "bind", Launch: launch.Launch, Binding: binding}).OK {
		t.Fatal("launch or bind refused")
	}
	current := func(l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) protocol.LocalWorkspaceControlResult {
		return call(protocol.LocalWorkspaceControl{Operation: "current", Launch: l, Binding: b})
	}
	if r := current(launch.Launch, binding); !r.OK || r.Refused != "" {
		t.Fatalf("current launch: %+v", r)
	}
	stale := launch.Launch
	stale.RuntimeGeneration++
	if r := current(stale, binding); r.OK || r.Refused == "" {
		t.Fatalf("a replaced launch must be refused definitively: %+v", r)
	}
	other := binding
	other.Root.Inode++
	if r := current(launch.Launch, other); r.OK || r.Refused == "" {
		t.Fatalf("a replaced runtime binding must be refused definitively: %+v", r)
	}
	// A reconnect the daemon has not yet recovered is not a verdict.
	dc.DesktopGeneration = newUUID()
	if err := db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
		return db.BindDesktopConnection(ctx, f.pool, *dc.DesktopBinding, dc.Name, dc.Mode, dc.ReposRoot, dc.DesktopGeneration)
	}); err != nil {
		t.Fatal(err)
	}
	if r := current(launch.Launch, binding); r.OK || r.Refused != "" {
		t.Fatalf("an unrecovered reconnect must fail without a definitive refusal: %+v", r)
	}
	recovered := call(protocol.LocalWorkspaceControl{Operation: "recover", Launch: launch.Launch, Binding: binding})
	if !recovered.OK {
		t.Fatalf("recover: %+v", recovered)
	}
	if r := current(recovered.Launch, binding); !r.OK {
		t.Fatalf("current after recovery: %+v", r)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET ended_at=now(),status='stopped' WHERE id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	if r := current(recovered.Launch, binding); r.OK || r.Refused == "" {
		t.Fatalf("an ended session must be refused definitively: %+v", r)
	}
}
