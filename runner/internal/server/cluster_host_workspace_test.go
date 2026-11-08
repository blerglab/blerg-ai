package server

import (
	"context"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func hostClusterParentManifest(account string) protocol.WorkspaceManifest {
	return protocol.WorkspaceManifest{
		Version: protocol.WorkspaceVersion, Layout: protocol.WorkspaceLayoutNames, Kind: "folder", OwnerID: account, Runtime: "host", LaunchGeneration: "launch-host-1",
		Members: []protocol.WorkspaceMember{
			{ID: "m1", Name: "app", Path: "app", SourceIdentity: "src-1", CommonDirectoryIdentity: "cd-1", Destination: "dest-1", Provider: "github", Project: "example-org/app"},
			{ID: "m2", Name: "local-only", Path: "local-only", SourceIdentity: "src-2", CommonDirectoryIdentity: "cd-2", Destination: "dest-2"},
		},
	}
}

func hostClusterReq(account, base string, repos ...string) runnerStartRequest {
	return runnerStartRequest{WorkspaceRepos: repos, WorkspaceLaunch: "launch-host-1", Base: base, Runtime: "cluster"}
}

// The core relaxation this card builds: a host Main's cluster worker reaches a real manifest
// and real members, with a stored read-only token, a provider that reports --base's current
// SHA, and a stale desktop branch that is already reachable at the provider. No PushAuthorized anywhere in
// the result (§1's failure rule); BaseSHA is what the pod actually checks out, not BaseRef's
// mutable name (Codex review of 1524a5d8).
func TestHostClusterChildWorkspaceBuildsReadOnlyMembers(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	jm := f.api.hub.JobManager()
	sha := strings.Repeat("a", 40)
	jm.baseBranchRemote = func(_ context.Context, _, _, _ string) (string, credentialRefusal, error) {
		return sha, credentialOK, nil
	}
	f.api.resolveBaseRef = func(_ context.Context, _ string, _ *DaemonConn, _, _, _, _ string) (string, error) {
		return strings.Repeat("b", 40), nil
	}
	f.api.baseSHAPushed = func(_ context.Context, _, _, _, got, _ string) (bool, int, bool, error) {
		if got != strings.Repeat("b", 40) {
			t.Fatalf("expected the daemon-resolved local SHA passed through, got %q want %q", got, strings.Repeat("b", 40))
		}
		return true, 0, false, nil
	}
	f.fake.credentialFetchResponses = map[string][]byte{spec.SpawningAccountID + ":" + readOnlyGitCredentialKind("github", "example-org"): []byte("fixture-readonly-token")}
	parent, err := db.GetSession(ctx, f.pool, spec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := hostClusterParentManifest(spec.SpawningAccountID)
	req := hostClusterReq(spec.SpawningAccountID, "alpha/0.6.0", "m1")
	child, members, apiErr := f.api.hostClusterChildWorkspace(ctx, parent, req, "child-1", manifest, coreProof{SessionID: "fixture-sid"})
	if apiErr != nil {
		t.Fatalf("refused: %v", apiErr)
	}
	if child == nil || child.Runtime != "cluster" || child.Kind != "repo" || len(child.Members) != 1 || child.LaunchGeneration == manifest.LaunchGeneration {
		t.Fatalf("child manifest: %+v", child)
	}
	if len(members) != 1 || members[0].PushAuthorized || members[0].ID != "m1" || members[0].CloneURL == "" {
		t.Fatalf("members: %+v", members)
	}
	if members[0].BaseRef != "alpha/0.6.0" {
		t.Fatalf("expected BaseRef to carry --base through to the pod's own clone, got %q", members[0].BaseRef)
	}
	if members[0].BaseSHA != sha {
		t.Fatalf("expected BaseSHA to be the verified exact commit, got %q want %q", members[0].BaseSHA, sha)
	}
	// The result must actually be a valid launch plan downstream, not just shaped right
	// here: this is what caught a real bug (DestinationIdentity must be the clone path
	// itself, never protocol.WorkspaceMember's own internal Destination field).
	plan := clusterworkspace.Plan{SessionID: "child-1", Manifest: *child, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: child.LaunchGeneration, Generation: 1}, Members: members}
	if err := plan.Validate(); err != nil {
		t.Fatalf("plan.Validate: %v", err)
	}
}

// Row 2: a member with no resolvable provider remote is refused by name; the launch never
// partially starts on the other, resolvable member.
func TestHostClusterChildWorkspaceRefusesUnresolvableMember(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	jm := f.api.hub.JobManager()
	// m1 (ahead of m2 in parent order) must never reach the network: m2's missing Project is
	// refused before any base-branch check, for either member.
	jm.baseBranchRemote = func(context.Context, string, string, string) (string, credentialRefusal, error) {
		t.Fatal("m2's missing remote must refuse before any base-branch network check")
		return "", credentialOK, nil
	}
	parent, err := db.GetSession(ctx, f.pool, spec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := hostClusterParentManifest(spec.SpawningAccountID)
	req := hostClusterReq(spec.SpawningAccountID, "alpha/0.6.0", "m1", "m2") // m2 has no Project
	_, _, apiErr := f.api.hostClusterChildWorkspace(ctx, parent, req, "child-1", manifest, coreProof{SessionID: "fixture-sid"})
	if apiErr == nil {
		t.Fatal("expected a refusal naming the unresolvable member")
	}
	if apiErr.Error() == "" || !strings.Contains(apiErr.Error(), "m2") {
		t.Fatalf("refusal does not name the member: %v", apiErr)
	}
}

// Row 3a: the branch has never been pushed to this repository at all — the provider's
// advertisement simply doesn't name it.
func TestHostClusterChildWorkspaceRefusesNeverPushedBase(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	jm := f.api.hub.JobManager()
	jm.baseBranchRemote = func(_ context.Context, _, _, _ string) (string, credentialRefusal, error) {
		return "", credentialOK, nil
	}
	f.api.resolveBaseRef = func(context.Context, string, *DaemonConn, string, string, string, string) (string, error) {
		t.Fatal("an absent remote branch must refuse before any daemon round trip")
		return "", nil
	}
	f.fake.credentialFetchResponses = map[string][]byte{spec.SpawningAccountID + ":" + readOnlyGitCredentialKind("github", "example-org"): []byte("fixture-readonly-token")}
	parent, err := db.GetSession(ctx, f.pool, spec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := hostClusterParentManifest(spec.SpawningAccountID)
	req := hostClusterReq(spec.SpawningAccountID, "alpha/0.6.0", "m1")
	_, _, apiErr := f.api.hostClusterChildWorkspace(ctx, parent, req, "child-1", manifest, coreProof{SessionID: "fixture-sid"})
	if apiErr == nil || !strings.Contains(apiErr.Error(), "not yet pushed") {
		t.Fatalf("expected a not-yet-pushed refusal, got %v", apiErr)
	}
}

// Row 3b (the Codex-review fix): the branch exists upstream, and Main's own local commit for
// it resolves fine, but the provider's compare API says that commit is ahead of the branch
// (not pushed) — refused by name, with the ahead count.
func TestHostClusterChildWorkspaceRefusesLocalAheadOfOrigin(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	jm := f.api.hub.JobManager()
	remoteSHA := strings.Repeat("a", 40)
	localSHA := strings.Repeat("b", 40)
	jm.baseBranchRemote = func(_ context.Context, _, _, _ string) (string, credentialRefusal, error) {
		return remoteSHA, credentialOK, nil
	}
	f.api.resolveBaseRef = func(_ context.Context, _ string, _ *DaemonConn, _, _, _, _ string) (string, error) {
		return localSHA, nil
	}
	f.api.baseSHAPushed = func(_ context.Context, _, _, _, got, _ string) (bool, int, bool, error) {
		if got != localSHA {
			t.Fatalf("expected the daemon-resolved local SHA passed through, got %q want %q", got, localSHA)
		}
		return false, 3, false, nil
	}
	f.fake.credentialFetchResponses = map[string][]byte{spec.SpawningAccountID + ":" + readOnlyGitCredentialKind("github", "example-org"): []byte("fixture-readonly-token")}
	parent, err := db.GetSession(ctx, f.pool, spec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := hostClusterParentManifest(spec.SpawningAccountID)
	req := hostClusterReq(spec.SpawningAccountID, "alpha/0.6.0", "m1")
	_, _, apiErr := f.api.hostClusterChildWorkspace(ctx, parent, req, "child-1", manifest, coreProof{SessionID: "fixture-sid"})
	if apiErr == nil || !strings.Contains(apiErr.Error(), "not yet pushed") || !strings.Contains(apiErr.Error(), "3 commit") {
		t.Fatalf("expected a named ahead-count refusal, got %v", apiErr)
	}
}

// Row 3c: the provider's compare API reports a diverged history (e.g. a force-push) —
// refused by name without a guessed count.
func TestHostClusterChildWorkspaceRefusesDivergedBase(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	jm := f.api.hub.JobManager()
	jm.baseBranchRemote = func(_ context.Context, _, _, _ string) (string, credentialRefusal, error) {
		return strings.Repeat("a", 40), credentialOK, nil
	}
	f.api.resolveBaseRef = func(_ context.Context, _ string, _ *DaemonConn, _, _, _, _ string) (string, error) {
		return strings.Repeat("b", 40), nil
	}
	f.api.baseSHAPushed = func(context.Context, string, string, string, string, string) (bool, int, bool, error) {
		return false, 0, true, nil
	}
	f.fake.credentialFetchResponses = map[string][]byte{spec.SpawningAccountID + ":" + readOnlyGitCredentialKind("github", "example-org"): []byte("fixture-readonly-token")}
	parent, err := db.GetSession(ctx, f.pool, spec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := hostClusterParentManifest(spec.SpawningAccountID)
	req := hostClusterReq(spec.SpawningAccountID, "alpha/0.6.0", "m1")
	_, _, apiErr := f.api.hostClusterChildWorkspace(ctx, parent, req, "child-1", manifest, coreProof{SessionID: "fixture-sid"})
	if apiErr == nil || !strings.Contains(apiErr.Error(), "not yet pushed") || !strings.Contains(apiErr.Error(), "diverged") {
		t.Fatalf("expected a named diverged-history refusal, got %v", apiErr)
	}
}

// Row 4: missing owner token — nothing stored, and the provider (per the seam) demands a
// credential we don't have.
func TestHostClusterChildWorkspaceRefusesMissingOwnerToken(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	jm := f.api.hub.JobManager()
	jm.baseBranchRemote = func(_ context.Context, _, _, token string) (string, credentialRefusal, error) {
		if token != "" {
			t.Fatal("no token was stored; nothing should have been offered")
		}
		return "", credentialMissing, nil
	}
	parent, err := db.GetSession(ctx, f.pool, spec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := hostClusterParentManifest(spec.SpawningAccountID)
	req := hostClusterReq(spec.SpawningAccountID, "alpha/0.6.0", "m1")
	_, _, apiErr := f.api.hostClusterChildWorkspace(ctx, parent, req, "child-1", manifest, coreProof{SessionID: "fixture-sid"})
	if apiErr == nil || !strings.Contains(apiErr.Error(), "m1") || !strings.Contains(apiErr.Error(), "example-org") {
		t.Fatalf("expected a named missing-token refusal, got %v", apiErr)
	}
}

// Row 5: a stored token exists but the provider rejects it (expired or otherwise invalid) —
// never folded into a "not pushed" message.
func TestHostClusterChildWorkspaceRefusesRejectedToken(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	jm := f.api.hub.JobManager()
	jm.baseBranchRemote = func(_ context.Context, _, _, token string) (string, credentialRefusal, error) {
		if token != "fixture-readonly-token" {
			t.Fatalf("expected the stored token to be offered, got %q", token)
		}
		return "", credentialRejected, nil
	}
	f.fake.credentialFetchResponses = map[string][]byte{spec.SpawningAccountID + ":" + readOnlyGitCredentialKind("github", "example-org"): []byte("fixture-readonly-token")}
	parent, err := db.GetSession(ctx, f.pool, spec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := hostClusterParentManifest(spec.SpawningAccountID)
	req := hostClusterReq(spec.SpawningAccountID, "alpha/0.6.0", "m1")
	_, _, apiErr := f.api.hostClusterChildWorkspace(ctx, parent, req, "child-1", manifest, coreProof{SessionID: "fixture-sid"})
	if apiErr == nil || !strings.Contains(apiErr.Error(), "example-org") || !strings.Contains(apiErr.Error(), "rejected") || strings.Contains(apiErr.Error(), "not yet pushed") {
		t.Fatalf("expected a named token-rejected refusal, distinct from 'not yet pushed', got %v", apiErr)
	}
}

// A stale or forged workspace_launch must refuse, exactly like clusterChildWorkspace's own
// freshness check.
func TestHostClusterChildWorkspaceRefusesStaleLaunch(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	parent, err := db.GetSession(ctx, f.pool, spec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := hostClusterParentManifest(spec.SpawningAccountID)
	req := hostClusterReq(spec.SpawningAccountID, "alpha/0.6.0", "m1")
	req.WorkspaceLaunch = "forged"
	_, _, apiErr := f.api.hostClusterChildWorkspace(ctx, parent, req, "child-1", manifest, coreProof{SessionID: "fixture-sid"})
	if apiErr == nil {
		t.Fatal("expected a refusal for a stale launch generation")
	}
}
