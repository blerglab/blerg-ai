package server

import (
	"context"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMembershipCloneChannelIndependentOfDiscovery(t *testing.T) {
	h := NewHub()
	clones := h.workspaceRequestChannel("clone_addition")
	discovery := h.workspaceRequestChannel("list")
	if clones == discovery {
		t.Fatal("clone occupies discovery capacity")
	}
	dc := &DaemonConn{}
	for _, operation := range []string{"bind_child", "resolve", "list", "addition", "bind"} {
		if h.workspaceRequestChannel(operation) != discovery {
			t.Fatal(operation)
		}
	}
	clones.pending = map[string]discoveryWaiter{"clone": {dc: dc, ch: make(chan protocol.WorkspaceDiscoveryResult, 1)}}
	discovery.pending = map[string]discoveryWaiter{"list": {dc: dc, ch: make(chan protocol.WorkspaceDiscoveryResult, 1)}}
	ch := clones.pending["clone"].ch
	h.deliverWorkspaceDiscovery(dc, protocol.WorkspaceDiscoveryResult{RequestID: "clone", OK: true})
	if len(discovery.pending) != 1 {
		t.Fatal("clone disturbed in-flight list")
	}
	select {
	case <-ch:
	default:
		t.Fatal("clone reply was lost")
	}
	h.abandonWorkspaceDiscoveryRequests(dc)
	if len(discovery.pending) != 0 || len(clones.pending) != 0 {
		t.Fatal("connection cleanup leaked requests")
	}
}

func TestMembershipCloneRetriesUnknownConnectionAndNotifiesRealAbsence(t *testing.T) {
	f := newLiveFx(t)
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET runtime='daemon' WHERE id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	member := protocol.WorkspaceMember{ID: "api", Name: "API", Path: "api", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: topicOwner, Runtime: "host", LaunchGeneration: "launch", Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}, Members: []protocol.WorkspaceMember{member}}
	m.Digest = workspaceselection.Digest(m)
	if err := db.InsertSessionWorkspace(ctx, f.pool, f.sess, m); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(ctx, f.pool, f.sess)
	if err != nil {
		t.Fatal(err)
	}
	f.api.userRepos = NewUserRepoLister(gitprovider.Default, func(context.Context, string) ([]string, bool) { return []string{"github"}, true }, nil)
	op, issue := f.api.buildMembership(ctx, &membershipContext{caller: delegationCaller{SessionID: f.sess}, row: row, m: m}, membershipChangeBody{OperationID: newUUID(), Kind: "add", Repository: "https://github.com/example-org/example-repo.git"})
	if issue != nil {
		t.Fatal(issue)
	}
	if _, _, err := db.CreateMembershipOperation(ctx, f.pool, *op); err != nil {
		t.Fatal(err)
	}
	if err := db.DecideMembershipOperation(ctx, f.pool, op.ID, topicOwner, op.Proposed.Digest, true); err != nil {
		t.Fatal(err)
	}
	status := http.StatusServiceUnavailable
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"engines":[]}`))
		}
	}))
	t.Cleanup(core.Close)
	f.api.coreURL, f.api.coreInternalKey = core.URL, "fixture-key"
	if err := db.SetSessionLaunchAuthSession(ctx, f.pool, f.sess, "fixture-launch"); err != nil {
		t.Fatal(err)
	}
	f.api.resolveMembershipClone(ctx, f.sess)
	stored, err := db.OpenMembershipOperation(ctx, f.pool, f.sess)
	if err != nil || stored == nil || stored.Phase != db.MembershipAuthorized {
		t.Fatalf("transient lookup settled approval: %v %v", stored, err)
	}
	var notice string
	f.api.hub.SetMembershipNoticeHook(func(_ string, text string, _ string) { notice = text })
	status = http.StatusOK
	f.api.resolveMembershipClone(ctx, f.sess)
	stored, err = db.LatestMembershipOperation(ctx, f.pool, f.sess)
	if err != nil || stored.Phase != db.MembershipRefused || notice == "" {
		t.Fatalf("real absence: %v %v notice=%q", stored, err, notice)
	}
	if repositoryRequest(stored) != nil {
		t.Fatal("settled operation still asks for approval")
	}
	if got := decodeLive(t, f.getLive(f.sess, f.token(topicOwner, "human"), "")).Counts.NeedsYou; got != 0 {
		t.Fatalf("settled refusal still needs you: %d", got)
	}
}

func TestMembershipNoticeReachesMain(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.api.membershipNotify(f.parent, "Git could not complete the approved clone.", "membership-"+newUUID())
	received := f.parentNote(f.parent, time.Second)
	if received.Source != membershipSource || received.Text != "Git could not complete the approved clone." {
		t.Fatalf("wrong relay: %+v", received)
	}
}

func TestMembershipCloneFailureReasonsMatchTheStage(t *testing.T) {
	for _, tc := range []struct{ code, contains string }{
		{"destination_unavailable", "No repository was cloned"},
		{"destination_exists", "different repository or folder"},
		{"clone_failed", "Git could not complete"},
		{"local_storage", "prepare or save"},
		{"stale", "before the cloned repository could be added"},
		{"invalid_selection", "request as invalid"},
		{"unavailable", "resolve the cloned repository"},
	} {
		if got := membershipCloneRefusal(tc.code); !strings.Contains(got, tc.contains) {
			t.Errorf("%s: %s", tc.code, got)
		}
	}
}

func TestMembershipCloneChannelFailureDistinguishesTimeoutFromRetry(t *testing.T) {
	for _, err := range []error{errDiscoveryUnavailable, context.Canceled, context.DeadlineExceeded} {
		if got := membershipCloneChannelRefusal(err); got != "" {
			t.Fatalf("transient channel error refused: %s", got)
		}
	}
	if got := membershipCloneChannelRefusal(fmt.Errorf("wrapped: %w", errMembershipCloneTimeout)); !strings.Contains(got, "did not confirm clone completion") {
		t.Fatal(got)
	}
}
