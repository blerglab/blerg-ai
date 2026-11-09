package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	membershipDaemon  = "00000000-0000-0000-0000-000000000011"
	membershipSession = "00000000-0000-0000-0000-000000000012"
	membershipOp      = "00000000-0000-0000-0000-000000000013"
)

func membershipMember(id string) protocol.WorkspaceMember {
	return protocol.WorkspaceMember{ID: id, Name: id, Path: id, SourceIdentity: "source-" + id, CommonDirectoryIdentity: "common-" + id, Destination: "source-" + id}
}

func membershipFixture(t *testing.T) (*pgxpool.Pool, protocol.WorkspaceManifest, protocol.WorkspaceRuntimeBinding) {
	t.Helper()
	pool := connect(t)
	ctx := context.Background()
	if err := db.UpsertDaemon(ctx, pool, membershipDaemon, "test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, membershipSession, membershipDaemon, "running", "/repos/one", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET spawning_account_id='owner',runtime='daemon' WHERE id=$1`, membershipSession); err != nil {
		t.Fatal(err)
	}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "host", LaunchGeneration: "launch", Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}, Members: []protocol.WorkspaceMember{membershipMember("one"), membershipMember("two")}}
	m.Digest = workspaceselection.Digest(m)
	if err := db.InsertSessionWorkspace(ctx, pool, membershipSession, m); err != nil {
		t.Fatal(err)
	}
	generation, err := db.BeginWorkspaceRuntime(ctx, pool, membershipSession, m.LaunchGeneration, 0)
	if err != nil {
		t.Fatal(err)
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: generation}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}}
	if err := db.BindWorkspaceRuntime(ctx, pool, membershipSession, binding.WorkspaceRuntimeRef, binding); err != nil {
		t.Fatal(err)
	}
	return pool, m, binding
}

func proposedAdd(t *testing.T, m protocol.WorkspaceManifest, revision int64) protocol.WorkspaceManifest {
	t.Helper()
	next := m
	next.Revision = revision
	next.Members = append(append([]protocol.WorkspaceMember(nil), m.Members...), membershipMember("three"))
	next.Digest = workspaceselection.Digest(next)
	return next
}

func addOperation(m, proposed protocol.WorkspaceManifest) db.MembershipOperation {
	return db.MembershipOperation{ID: membershipOp, SessionID: membershipSession, Kind: "add", Phase: db.MembershipAuthorized, RequestedBy: "person", Principal: "owner", ApprovedBy: "owner", ExpectedRevision: 0, OldDigest: m.Digest, Proposed: proposed, MemberID: "three", MemberName: "three", Source: protocol.SelectionRef{Path: "three", SourceIdentity: "source-three"}}
}

func TestMembershipCommitPublishesRevisionAndKeepsLaunchImmutable(t *testing.T) {
	pool, m, binding := membershipFixture(t)
	ctx := context.Background()
	proposed := proposedAdd(t, m, 1)
	op, created, err := db.CreateMembershipOperation(ctx, pool, addOperation(m, proposed))
	if err != nil || !created {
		t.Fatal(created, err)
	}
	next, err := db.CommitMembershipOperation(ctx, pool, op.ID, binding, "receipt")
	if err != nil {
		t.Fatal(err)
	}
	if next.ManifestDigest != proposed.Digest || next.WorkspaceRuntimeRef != binding.WorkspaceRuntimeRef || next.Root != binding.Root {
		t.Fatalf("binding %+v", next)
	}
	w, err := db.GetSessionWorkspace(ctx, pool, membershipSession)
	if err != nil || w.Manifest.Digest != proposed.Digest || len(w.Manifest.Members) != 3 || w.Manifest.Revision != 1 {
		t.Fatal("effective manifest", w, err)
	}
	launch, err := db.GetSessionLaunchManifest(ctx, pool, membershipSession)
	if err != nil || launch.Digest != m.Digest || len(launch.Members) != 2 {
		t.Fatal("launch history changed", launch, err)
	}
	_, current, err := db.GetWorkspaceRuntime(ctx, pool, membershipSession)
	if err != nil || current == nil || *current != next {
		t.Fatal("binding not published", current, err)
	}
	if err := next.Validate(*w.Manifest); err != nil {
		t.Fatal(err)
	}
	// A lost acknowledgment retries the exact commit and receives the same binding.
	again, err := db.CommitMembershipOperation(ctx, pool, op.ID, binding, "receipt")
	if err != nil || again != next {
		t.Fatal("idempotent commit", again, err)
	}
	if _, err := db.CommitMembershipOperation(ctx, pool, op.ID, binding, "other-receipt"); !errors.Is(err, db.ErrMembershipConflict) {
		t.Fatal("different receipt accepted", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE session_workspace_revisions SET manifest='{}' WHERE session_id=$1`, membershipSession); err == nil {
		t.Fatal("revision history is append-only")
	}
}

func TestMembershipCloneCannotResolveBeforeApproval(t *testing.T) {
	pool, m, binding := membershipFixture(t)
	next := proposedAdd(t, m, 1)
	op := addOperation(m, next)
	op.Phase, op.RequestedBy, op.ApprovedBy = db.MembershipRequested, "main", ""
	op.Source = protocol.SelectionRef{GitURL: "https://github.com/example-org/example-repo.git"}
	if _, _, err := db.CreateMembershipOperation(t.Context(), pool, op); err != nil {
		t.Fatal(err)
	}
	resolved := addOperation(m, next)
	if err := db.ResolveMembershipClone(t.Context(), pool, op.ID, next.Digest, resolved); !errors.Is(err, db.ErrMembershipStale) {
		t.Fatalf("unapproved clone resolved: %v", err)
	}
	if err := db.DecideMembershipOperation(t.Context(), pool, op.ID, "owner", next.Digest, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CommitMembershipOperation(t.Context(), pool, op.ID, binding, "receipt"); !errors.Is(err, db.ErrMembershipConflict) {
		t.Fatalf("URL placeholder committed: %v", err)
	}
	if err := db.ResolveMembershipClone(t.Context(), pool, op.ID, "wrong-digest", resolved); !errors.Is(err, db.ErrMembershipStale) {
		t.Fatal(err)
	}
	if err := db.ResolveMembershipClone(t.Context(), pool, op.ID, next.Digest, resolved); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetMembershipOperation(t.Context(), pool, op.ID)
	if err != nil || stored.Source.GitURL != "" || stored.Source.Path != "three" {
		t.Fatal(stored, err)
	}
}

func TestMembershipRevisionCASAndRetries(t *testing.T) {
	pool, m, binding := membershipFixture(t)
	ctx := context.Background()
	op := addOperation(m, proposedAdd(t, m, 1))
	if _, created, err := db.CreateMembershipOperation(ctx, pool, op); err != nil || !created {
		t.Fatal(err)
	}
	if again, created, err := db.CreateMembershipOperation(ctx, pool, op); err != nil || created || again.ID != op.ID {
		t.Fatal("exact retry", created, err)
	}
	changed := op
	changed.Source.Path = "elsewhere"
	if _, _, err := db.CreateMembershipOperation(ctx, pool, changed); !errors.Is(err, db.ErrMembershipConflict) {
		t.Fatal("changed retry accepted", err)
	}
	second := op
	second.ID = "00000000-0000-0000-0000-000000000014"
	if _, _, err := db.CreateMembershipOperation(ctx, pool, second); !errors.Is(err, db.ErrMembershipBusy) {
		t.Fatal("second open operation accepted", err)
	}
	if _, err := db.CommitMembershipOperation(ctx, pool, op.ID, binding, "receipt"); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteMembershipOperation(ctx, pool, op.ID); err != nil {
		t.Fatal(err)
	}
	second.Proposed = proposedAdd(t, m, 1)
	if _, _, err := db.CreateMembershipOperation(ctx, pool, second); !errors.Is(err, db.ErrMembershipStale) {
		t.Fatal("stale expected revision accepted", err)
	}
}

func TestMembershipCommitNeedsCurrentRuntimeAndApproval(t *testing.T) {
	pool, m, binding := membershipFixture(t)
	ctx := context.Background()
	op := addOperation(m, proposedAdd(t, m, 1))
	op.Phase = db.MembershipRequested
	op.RequestedBy, op.ApprovedBy = "main", ""
	if _, _, err := db.CreateMembershipOperation(ctx, pool, op); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CommitMembershipOperation(ctx, pool, op.ID, binding, "receipt"); !errors.Is(err, db.ErrMembershipConflict) {
		t.Fatal("unapproved request committed", err)
	}
	if err := db.DecideMembershipOperation(ctx, pool, op.ID, "owner", "someone-else-digest", true); !errors.Is(err, db.ErrMembershipConflict) {
		t.Fatal("approval of another proposal accepted", err)
	}
	if err := db.DecideMembershipOperation(ctx, pool, op.ID, "owner", op.Proposed.Digest, true); err != nil {
		t.Fatal(err)
	}
	stale := binding
	stale.Generation++
	if _, err := db.CommitMembershipOperation(ctx, pool, op.ID, stale, "receipt"); !errors.Is(err, db.ErrMembershipStale) {
		t.Fatal("stale runtime committed", err)
	}
	if _, err := db.CommitMembershipOperation(ctx, pool, op.ID, binding, "receipt"); err != nil {
		t.Fatal(err)
	}
	if err := db.RefuseMembershipOperation(ctx, pool, op.ID, "too late"); !errors.Is(err, db.ErrMembershipConflict) {
		t.Fatal("committed operation refused", err)
	}
}

func TestMembershipRemovalKeepsHistoricalResults(t *testing.T) {
	pool, m, binding := membershipFixture(t)
	ctx := context.Background()
	results := []protocol.WorkspaceMemberResult{
		{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "one", LaunchGeneration: "launch"}, Head: "a"},
		{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "two", LaunchGeneration: "launch"}, Head: "b"},
	}
	if err := db.SetWorkspaceRuntimeResults(ctx, pool, membershipSession, binding.WorkspaceRuntimeRef, results); err != nil {
		t.Fatal(err)
	}
	next := m
	next.Revision = 1
	next.Members = []protocol.WorkspaceMember{m.Members[0]}
	next.Digest = workspaceselection.Digest(next)
	op := db.MembershipOperation{ID: membershipOp, SessionID: membershipSession, Kind: "remove", Phase: db.MembershipAuthorized, RequestedBy: "person", Principal: "owner", ApprovedBy: "owner", OldDigest: m.Digest, Proposed: next, MemberID: "two", MemberName: "two"}
	if _, _, err := db.CreateMembershipOperation(ctx, pool, op); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CommitMembershipOperation(ctx, pool, op.ID, binding, "receipt"); err != nil {
		t.Fatal(err)
	}
	w, err := db.GetSessionWorkspace(ctx, pool, membershipSession)
	if err != nil || len(w.Results) != 1 || w.Results[0].MemberID != "one" {
		t.Fatal("current results", w, err)
	}
	retired, err := db.MembershipRetiredResults(ctx, pool, membershipSession)
	if err != nil || len(retired) != 1 || retired[0].MemberID != "two" || retired[0].Head != "b" {
		t.Fatal("historical result lost", retired, err)
	}
}

func TestMembershipFinalCommitUsesCurrentRevision(t *testing.T) {
	pool, m, binding := membershipFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO local_workspace_authority(session_id,token,launch_generation,runtime_generation,connection_generation) VALUES($1,'token',$2,$3,'connection')`, membershipSession, m.LaunchGeneration, binding.Generation); err != nil {
		t.Fatal(err)
	}
	proposed := proposedAdd(t, m, 1)
	op := addOperation(m, proposed)
	if _, _, err := db.CreateMembershipOperation(ctx, pool, op); err != nil {
		t.Fatal(err)
	}
	next, err := db.CommitMembershipOperation(ctx, pool, op.ID, binding, "receipt")
	if err != nil {
		t.Fatal(err)
	}
	results := []protocol.WorkspaceMemberResult{}
	for _, member := range proposed.Members {
		results = append(results, protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: member.ID, LaunchGeneration: "launch"}})
	}
	final := protocol.WorkspaceFinalization{Binding: next, Message: protocol.SessionEnded{Type: "session_ended", SessionID: membershipSession}, Results: results}
	if _, err := db.CommitLocalWorkspaceFinal(ctx, pool, membershipSession, "token", final); err != nil {
		t.Fatal(err)
	}
}

func TestMembershipRefusalKeepsLocalFilesAndDiscardBindsTheRetry(t *testing.T) {
	pool, m, binding := membershipFixture(t)
	ctx := context.Background()
	op := addOperation(m, proposedAdd(t, m, 1))
	if _, _, err := db.CreateMembershipOperation(ctx, pool, op); err != nil {
		t.Fatal(err)
	}
	local := protocol.WorkspaceLocalFiles{Names: []string{".env.local"}, Count: 3, Digest: "digest"}
	if err := db.RefuseMembershipRemoval(ctx, pool, op.ID, "refused", local); err != nil {
		t.Fatal(err)
	}
	refused, err := db.GetMembershipOperation(ctx, pool, op.ID)
	if err != nil || refused.LocalFiles.Digest != "digest" || refused.LocalFiles.Count != 3 || len(refused.LocalFiles.Names) != 1 {
		t.Fatalf("%+v %v", refused, err)
	}

	second := op
	second.ID, second.DiscardLocal = "00000000-0000-0000-0000-000000000015", "digest"
	created, _, err := db.CreateMembershipOperation(ctx, pool, second)
	if err != nil || created.DiscardLocal != "digest" {
		t.Fatalf("%+v %v", created, err)
	}
	changed := second
	changed.DiscardLocal = "other"
	if _, _, err := db.CreateMembershipOperation(ctx, pool, changed); !errors.Is(err, db.ErrMembershipConflict) {
		t.Fatal("a retry changed what it discards", err)
	}

	// A committed change can still explain why it hasn't finished.
	if _, err := db.CommitMembershipOperation(ctx, pool, second.ID, binding, "receipt"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMembershipWaiting(ctx, pool, second.ID, "reopening"); err != nil {
		t.Fatal(err)
	}
	if open, err := db.OpenMembershipOperation(ctx, pool, membershipSession); err != nil || open.Waiting != "reopening" {
		t.Fatalf("%+v %v", open, err)
	}
	if err := db.CompleteMembershipOperation(ctx, pool, second.ID); err != nil {
		t.Fatal(err)
	}
	if done, err := db.GetMembershipOperation(ctx, pool, second.ID); err != nil || done.Waiting != "" {
		t.Fatalf("%+v %v", done, err)
	}
}
