package server

import (
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"testing"
)

func TestSessionEndedPersistsWorkspaceMemberResults(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := t.Context()
	const daemon = "00000000-0000-0000-0000-0000000000d1"
	const session = "00000000-0000-0000-0000-0000000000d2"
	if err := db.UpsertDaemon(ctx, pool, daemon, "workspace-test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/workspace", "", "Workspace", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET spawning_account_id='owner',runtime='docker' WHERE id=$1`, session); err != nil {
		t.Fatal(err)
	}
	launch := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "sandbox", LaunchGeneration: "launch", Root: protocol.WorkspaceRoot{Inode: 1}, Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if err := db.InsertSessionWorkspace(ctx, pool, session, launch); err != nil {
		t.Fatal(err)
	}
	results := []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "one", LaunchGeneration: "launch"}, Base: "base", Head: "head", Branch: "wip/session", Pending: true, RecoveryLocation: "/private/recovery"}}
	HandleSessionEnded(ctx, NewHub(), pool, protocol.SessionEnded{SessionID: session, WorkspaceResults: results})
	persisted, err := db.GetSessionWorkspace(ctx, pool, session)
	if err != nil || len(persisted.Results) != 1 || persisted.Results[0].Head != "head" {
		t.Fatalf("member results not persisted: %+v %v", persisted, err)
	}
	results[0].LaunchGeneration = "stale"
	results[0].Head = "forged"
	HandleSessionEnded(ctx, NewHub(), pool, protocol.SessionEnded{SessionID: session, WorkspaceResults: results})
	persisted, err = db.GetSessionWorkspace(ctx, pool, session)
	if err != nil || persisted.Results[0].Head != "head" {
		t.Fatal("stale member result replaced trusted result")
	}
}

func TestHostWorkspaceChildResultBranch(t *testing.T) {
	f := newPrivFixture(t)
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET runtime='daemon',parent_session_id=$2 WHERE id=$1`, f.pubSession, f.privSession); err != nil {
		t.Fatal(err)
	}
	launch := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: privAcctA, Runtime: "host", LaunchGeneration: "host-child-launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "app", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if err := db.InsertSessionWorkspace(ctx, f.pool, f.pubSession, launch); err != nil {
		t.Fatal(err)
	}
	result, err := f.api.buildSessionResult(ctx, f.pubSession, privAcctA)
	if err != nil || result.Branch != "wip/"+f.pubSession {
		t.Fatalf("host child branch: %+v %v", result, err)
	}
	other, err := f.api.buildSessionResult(ctx, f.pubSession, privAcctB)
	if err != nil || other.Branch != "" {
		t.Fatalf("desktop branch exposed to another viewer: %+v %v", other, err)
	}
}
