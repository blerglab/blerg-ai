package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
	"github.com/gorilla/websocket"
)

func approvedCloneFixture(t *testing.T) (*delegFx, *db.MembershipOperation) {
	t.Helper()
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.dc.DiscoveryVersion = 1
	f.dc.RepositoryCloneVersion = protocol.RepositoryCloneVersion
	f.dc.WorkspaceCapabilities = protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, Sandbox: true, Membership: protocol.WorkspaceMembershipVersion}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner-1", Runtime: "sandbox", LaunchGeneration: "parent-launch", Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}, Members: []protocol.WorkspaceMember{{ID: "api", Name: "API", Path: "api", SourceIdentity: "api-source", CommonDirectoryIdentity: "api-common", Destination: "api-source"}}}
	m.Digest = workspaceselection.Digest(m)
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.parent, m); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionLaunchAuthSession(t.Context(), f.pool, f.parent, "fixture-launch"); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(t.Context(), f.pool, f.parent)
	if err != nil {
		t.Fatal(err)
	}
	f.api.userRepos = NewUserRepoLister(gitprovider.Default, func(context.Context, string) ([]string, bool) { return []string{"github"}, true }, nil)
	op, issue := f.api.buildMembership(t.Context(), &membershipContext{caller: delegationCaller{SessionID: f.parent}, row: row, m: m}, membershipChangeBody{OperationID: newUUID(), Kind: "add", Repository: "https://github.com/example-org/example-repo.git"})
	if issue != nil {
		t.Fatal(issue)
	}
	if _, _, err := db.CreateMembershipOperation(t.Context(), f.pool, *op); err != nil {
		t.Fatal(err)
	}
	if err := db.DecideMembershipOperation(t.Context(), f.pool, op.ID, "owner-1", op.Proposed.Digest, true); err != nil {
		t.Fatal(err)
	}
	f.api.userRepos = nil
	return f, op
}

func cloneCoreFixture(t *testing.T, f *delegFx, status *int, fetchStatus int) *int {
	t.Helper()
	calls := new(int)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.URL.Path == "/internal/credentials/fetch" {
			w.WriteHeader(fetchStatus)
			if fetchStatus == 200 {
				_, _ = w.Write([]byte(`{"plaintext_base64":"Zml4dHVyZS10b2tlbg=="}`))
			}
			return
		}
		w.WriteHeader(*status)
		if *status == 200 {
			_, _ = w.Write([]byte(`{"engines":["github"]}`))
		}
	}))
	t.Cleanup(core.Close)
	f.api.coreURL, f.api.coreInternalKey = core.URL, "fixture-key"
	f.api.repoVisibility = func(context.Context, gitprovider.Provider, string, gitprovider.Ref) (bool, bool) { return false, true }
	return calls
}

func TestMembershipClonePermanentProofRefusalNotifiesAndUnblocks(t *testing.T) {
	for _, status := range []int{401, 403, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f, op := approvedCloneFixture(t)
			cloneCoreFixture(t, f, &status, 200)
			var notice string
			f.hub.SetMembershipNoticeHook(func(_ string, text string, _ string) { notice = text })
			f.api.resolveMembershipClone(t.Context(), f.parent)
			stored, err := db.LatestMembershipOperation(t.Context(), f.pool, f.parent)
			if err != nil || stored.Phase != db.MembershipRefused || !strings.Contains(stored.Refusal, "sign-in") || notice == "" {
				t.Fatalf("refusal=%+v error=%v notice=%q", stored, err, notice)
			}
			if open, err := db.OpenMembershipOperation(t.Context(), f.pool, f.parent); err != nil || open != nil {
				t.Fatal("refusal still blocks membership", open, err)
			}
			if stored.ID != op.ID {
				t.Fatal("wrong operation refused")
			}
		})
	}
}

func TestMembershipCloneTransientDeadlineSurvivesRestart(t *testing.T) {
	for _, credentialFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "core", true: "credential"}[credentialFailure], func(t *testing.T) {
			f, _ := approvedCloneFixture(t)
			status := 503
			if credentialFailure {
				status = 200
			}
			cloneCoreFixture(t, f, &status, 503)
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			f.api.membershipCloneNow = func() time.Time { return now }
			f.api.resolveMembershipClone(t.Context(), f.parent)
			first, err := db.OpenMembershipOperation(t.Context(), f.pool, f.parent)
			if err != nil || first == nil || first.CloneRetryStartedAt == nil || !first.CloneRetryStartedAt.Equal(now) {
				t.Fatalf("first retry: %+v %v", first, err)
			}
			// A new API instance reads the persisted deadline; no in-memory timer is retained.
			resumed := NewAPI(f.hub, f.pool, delegDaemonToken, nil, "")
			resumed.coreURL, resumed.coreInternalKey = f.api.coreURL, f.api.coreInternalKey
			resumed.membershipCloneNow = func() time.Time { return now }
			f.api = resumed
			now = now.Add(14*time.Minute + 59*time.Second)
			// A different transient cause must not restart the recovery window.
			status = http.StatusOK
			f.api.resolveMembershipClone(t.Context(), f.parent)
			active, err := db.OpenMembershipOperation(t.Context(), f.pool, f.parent)
			if err != nil || active == nil || !active.CloneRetryStartedAt.Equal(*first.CloneRetryStartedAt) {
				t.Fatal("retry extended or prematurely settled", active, err)
			}
			var notice string
			f.hub.SetMembershipNoticeHook(func(_ string, text string, _ string) { notice = text })
			now = now.Add(time.Second)
			f.api.resolveMembershipClone(t.Context(), f.parent)
			last, err := db.LatestMembershipOperation(t.Context(), f.pool, f.parent)
			if err != nil || last.Phase != db.MembershipRefused || !strings.Contains(last.Refusal, "15 minutes") || !strings.Contains(last.Refusal, active.Waiting) || notice == "" {
				t.Fatalf("deadline: %+v %v notice=%q", last, err, notice)
			}
		})
	}
}

func TestMembershipCloneOldDaemonRefusesBeforeCoreOrDispatch(t *testing.T) {
	f, _ := approvedCloneFixture(t)
	f.dc.RepositoryCloneVersion = 0
	status := 503
	calls := cloneCoreFixture(t, f, &status, 503)
	var notice string
	f.hub.SetMembershipNoticeHook(func(_ string, text string, _ string) { notice = text })
	f.api.resolveMembershipClone(t.Context(), f.parent)
	op, err := db.LatestMembershipOperation(t.Context(), f.pool, f.parent)
	if err != nil || op.Phase != db.MembershipRefused || !strings.Contains(op.Refusal, "Update Blerg") || notice == "" || *calls != 0 {
		t.Fatalf("legacy: %+v %v calls=%d notice=%q", op, err, *calls, notice)
	}
	f.hub.cloneReqs.Lock()
	pending := len(f.hub.cloneReqs.pending)
	f.hub.cloneReqs.Unlock()
	if pending != 0 {
		t.Fatal("legacy daemon occupied clone slot")
	}
}

func TestMembershipCloneStoppedWithoutEndedAtRefuses(t *testing.T) {
	f, _ := approvedCloneFixture(t)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET status='stopped',ended_at=NULL WHERE id=$1`, f.parent); err != nil {
		t.Fatal(err)
	}
	status := 503
	calls := cloneCoreFixture(t, f, &status, 200)
	var notice string
	f.hub.SetMembershipNoticeHook(func(_ string, text string, _ string) { notice = text })
	f.api.resolveMembershipClone(t.Context(), f.parent)
	op, err := db.LatestMembershipOperation(t.Context(), f.pool, f.parent)
	if err != nil || op.Phase != db.MembershipRefused || !strings.Contains(op.Refusal, "ended") || *calls != 0 || notice == "" {
		t.Fatalf("stopped: %+v %v calls=%d", op, err, *calls)
	}
}

func TestMembershipCloneResumeRetryAndColonSource(t *testing.T) {
	f, _ := approvedCloneFixture(t)
	status := 200
	cloneCoreFixture(t, f, &status, 200)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	replies := 0
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case raw := <-f.dc.send:
				var req protocol.DiscoverWorkspace
				if json.Unmarshal(raw, &req) != nil || req.Type != "clone_repository" {
					continue
				}
				replies++
				result := protocol.WorkspaceDiscoveryResult{RequestID: req.RequestID, ErrorCode: "binding_pending"}
				if replies > 1 {
					result.OK = true
					result.ErrorCode = ""
					result.Addition = &protocol.DiscoveryAddition{PhysicalPath: req.Path, Source: protocol.DiscoverySource{ID: "cloned", Name: req.Path, Path: "work:2026/" + req.Path, SourceIdentity: "new-source", CommonDirectoryIdentity: "new-common", Origin: &protocol.RepoOrigin{Provider: "github", FullName: "example-org/example-repo"}}}
				}
				f.hub.deliverWorkspaceDiscovery(f.dc, result)
			}
		}
	}()
	f.api.resolveMembershipClone(t.Context(), f.parent)
	waiting, err := db.OpenMembershipOperation(t.Context(), f.pool, f.parent)
	if err != nil || waiting == nil || waiting.CloneRetryStartedAt == nil || !strings.Contains(waiting.Waiting, "resume") {
		t.Fatalf("resume wait: %+v %v", waiting, err)
	}
	f.api.resolveMembershipClone(t.Context(), f.parent)
	resolved, err := db.OpenMembershipOperation(t.Context(), f.pool, f.parent)
	if err != nil || resolved == nil || resolved.Source.GitURL != "" || !strings.HasPrefix(resolved.Source.Path, "work:2026/") {
		t.Fatalf("resolved colon source: %+v %v", resolved, err)
	}
}

func TestMembershipCloneCapacityRetryHasDeadline(t *testing.T) {
	f, _ := approvedCloneFixture(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.api.membershipCloneNow = func() time.Time { return now }
	for i := 0; i < cap(membershipCloneSlots); i++ {
		membershipCloneSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(membershipCloneSlots); i++ {
			<-membershipCloneSlots
		}
	}()
	f.api.resolveMembershipClone(t.Context(), f.parent)
	op, err := db.OpenMembershipOperation(t.Context(), f.pool, f.parent)
	if err != nil || op == nil || op.CloneRetryStartedAt == nil || !strings.Contains(op.Waiting, "another repository") {
		t.Fatalf("capacity wait: %+v %v", op, err)
	}
	now = now.Add(15 * time.Minute)
	f.api.resolveMembershipClone(t.Context(), f.parent)
	op, err = db.LatestMembershipOperation(t.Context(), f.pool, f.parent)
	if err != nil || op.Phase != db.MembershipRefused {
		t.Fatalf("capacity bypassed deadline: %+v %v", op, err)
	}
}

func TestRepositoryCloneCapabilitySurvivesDaemonHello(t *testing.T) {
	for _, version := range []int{0, protocol.RepositoryCloneVersion} {
		hub := NewHub()
		conn := dialDaemon(t, hub)
		if err := conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: "clone-capability", Token: "tok-1234567890", ProtocolVersion: "1", Welcome: true, RepositoryCloneVersion: version}); err != nil {
			t.Fatal(err)
		}
		var welcome protocol.DaemonWelcome
		if err := json.Unmarshal(readFirstMessage(t, conn), &welcome); err != nil {
			t.Fatal(err)
		}
		// A pong is processed by the read pump, which starts after registration.
		pong, done := make(chan struct{}, 1), make(chan struct{})
		conn.SetPongHandler(func(string) error { pong <- struct{}{}; return nil })
		go func() {
			defer close(done)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		if err := conn.WriteControl(websocket.PingMessage, []byte("clone-capability"), time.Now().Add(5*time.Second)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-pong:
		case <-time.After(5 * time.Second):
			t.Fatal("daemon read pump did not start")
		}
		dc := hub.GetDaemon(welcome.DaemonID)
		if dc == nil {
			t.Fatal("daemon was not registered before read pump")
		}
		if dc.RepositoryCloneVersion != version {
			t.Fatal("clone capability lost in hello", dc.RepositoryCloneVersion, version)
		}
		_ = conn.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("socket reader did not stop")
		}
	}
}
