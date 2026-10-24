package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// Exercise the actual desktop read pump, Manager dispatch and reply writer over
// a real socket. The selected sources have unrelated physical parents, so cwd
// cannot accidentally stand in for member selection.
func TestP6GitMembersOverDesktopTransport(t *testing.T) {
	one, _ := commitFixture(t)
	two, _ := commitFixture(t)
	headOne := commitFile(t, one, "one.txt", "first member\n", "First")
	baseTwo := commitFile(t, two, "two.txt", "second member base\n", "Second base")
	child := "12345678-1234-4234-8234-123456789abc"
	originGit(t, two, "checkout", "-qb", "wip/"+child)
	headTwo := commitFile(t, two, "two.txt", "second member changed\n", "Second changed")
	originGit(t, two, "checkout", "main")
	originGit(t, two, "merge", "--ff-only", "wip/"+child)
	namespace := t.TempDir()
	info, _ := os.Stat(namespace)
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "host", OwnerID: "owner", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{
		{ID: "one", Name: "one", Path: "members/one", SourceIdentity: "one-source", CommonDirectoryIdentity: "one-common", Destination: "one"},
		{ID: "two", Name: "two", Path: "members/two", SourceIdentity: "two-source", CommonDirectoryIdentity: "two-common", Destination: "two"},
	}}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 7}, ManifestDigest: "digest", Root: workspaceRootIdentity(info)}
	a, _ := os.Stat(one)
	b, _ := os.Stat(two)
	virtual, err := NewVirtualHostExplorer(namespace, manifest, binding, map[string]HostWorkspaceSource{"one": {Path: one, Root: workspaceRootIdentity(a)}, "two": {Path: two, Root: workspaceRootIdentity(b)}}, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	authority := &hostExplorerAuthority{testWorkspaceAuthority: testWorkspaceAuthority{bound: &binding}}
	wrapper := &authorizedHostExplorer{reader: virtual, authority: authority, launch: protocol.SupervisorLaunch{SessionID: "session", LaunchGeneration: "launch", RuntimeGeneration: 7, Runtime: "host"}, binding: binding}
	peers := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			peers <- peer
		}
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-peers
	defer peer.Close()
	client := NewWSClient(Config{})
	client.conn = conn
	manager := newManagerWithSender(client, ManagerConfig{})
	manager.BindWorkspaceExplorer("session", wrapper)
	// Leave a real legacy scalar binding pointing at the first source: a removed
	// v2 dispatch guard would read it and leak the ambiguous first member.
	manager.sessions["session"] = &Session{workspace: one, workspaceInfo: a, done: make(chan struct{})}
	manager.sessions["legacy"] = &Session{workspace: one, workspaceInfo: a, done: make(chan struct{})}
	client.OnMessage("read_commit", manager.handleReadCommit)
	client.OnMessage("read_session_origin", manager.handleReadSessionOrigin)
	pump := make(chan struct{})
	go func() { defer close(pump); _ = client.readLoop(conn, func([]byte) {}) }()
	defer func() { _ = conn.Close(); <-pump; manager.explorer.Close() }()
	call := func(req protocol.ReadCommit, origin bool) protocol.CommitResult {
		t.Helper()
		req.Type = "read_commit"
		if req.RequestID == "" {
			req.RequestID = "request"
		}
		if req.SessionID == "" {
			req.SessionID = "session"
		}
		var message any = req
		if origin {
			message = protocol.ReadSessionOrigin{Type: "read_session_origin", SessionID: req.SessionID, RequestID: req.RequestID, WorkspaceGitRef: req.WorkspaceGitRef}
		}
		if err := peer.WriteJSON(message); err != nil {
			t.Fatal(err)
		}
		_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, raw, err := peer.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if origin {
			var res protocol.SessionOriginResult
			if err := json.Unmarshal(raw, &res); err != nil {
				t.Fatal(err)
			}
			if res.WorkspaceGitRef != req.WorkspaceGitRef {
				t.Fatal("origin provenance lost")
			}
			return protocol.CommitResult{OK: res.OK, Commit: &protocol.CommitInfo{SHA: res.Commit}}
		}
		var res protocol.CommitResult
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatal(err)
		}
		if res.WorkspaceGitRef != req.WorkspaceGitRef {
			t.Fatal("commit provenance lost")
		}
		return res
	}
	ref := func(id string) protocol.WorkspaceGitRef {
		return protocol.WorkspaceGitRef{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, MemberID: id}
	}
	for _, test := range []struct{ id, head, text string }{{"one", headOne, "first member"}, {"two", headTwo, "second member changed"}} {
		req := protocol.ReadCommit{WorkspaceGitRef: ref(test.id), SHA: test.head}
		res := call(req, false)
		if !res.OK || res.Commit == nil || res.Commit.SHA != test.head || res.Commit.WorkspaceGitRef != req.WorkspaceGitRef {
			t.Fatalf("member commit: %+v", res)
		}
		req.Patch = true
		res = call(req, false)
		patch, _ := base64.StdEncoding.DecodeString(res.PatchB64)
		if !res.OK || !strings.Contains(string(patch), test.text) {
			t.Fatalf("member patch: %+v", res)
		}
		res = call(req, true)
		if !res.OK || res.Commit.SHA != test.head {
			t.Fatal("origin selected cwd instead of member")
		}
	}
	for _, req := range []protocol.ReadCommit{{SHA: headOne}, {SHA: headOne, WorkspaceGitRef: ref("unknown")}, {SHA: headOne, WorkspaceGitRef: protocol.WorkspaceGitRef{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 6}, MemberID: "one"}}, {SHA: headOne, WorkspaceGitRef: protocol.WorkspaceGitRef{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "old", Generation: 7}, MemberID: "one"}}} {
		if res := call(req, false); res.OK {
			t.Fatal("unqualified/stale commit accepted")
		}
		if res := call(req, true); res.OK {
			t.Fatal("unqualified/stale origin accepted")
		}
	}
	merged := call(protocol.ReadCommit{WorkspaceGitRef: ref("two"), MergedChild: child, ChildLaunchGeneration: "child-launch", ChildBase: baseTwo}, false)
	if !merged.OK || !merged.Merged {
		t.Fatalf("qualified child merge: %+v", merged)
	}
	if res := call(protocol.ReadCommit{SessionID: "legacy", SHA: headOne}, false); !res.OK {
		t.Fatal("legacy single repo changed")
	}
	if res := call(protocol.ReadCommit{SessionID: "legacy"}, true); !res.OK || res.Commit.SHA != headOne {
		t.Fatal("legacy origin changed")
	}
	if err := manager.explorer.Suspend(context.Background(), "session"); err != nil {
		t.Fatal(err)
	}
	if res := call(protocol.ReadCommit{WorkspaceGitRef: ref("one"), SHA: headOne}, false); res.OK {
		t.Fatal("suspended reader accepted")
	}
	if err := manager.explorer.Resume("session"); err != nil {
		t.Fatal(err)
	}
	// Replacing the current authority fences reads without changing the socket.
	wrapper.mu.Lock()
	authority.stale = true
	wrapper.mu.Unlock()
	if res := call(protocol.ReadCommit{WorkspaceGitRef: ref("one"), SHA: headOne}, false); res.OK {
		t.Fatal("revoked reader accepted")
	}
	manager.explorer.Unbind("session")
	if res := call(protocol.ReadCommit{SHA: headOne}, false); res.OK {
		t.Fatal("retired v2 reader downgraded to scalar")
	}
}

func TestP6SandboxGitUsesBoundMemberSource(t *testing.T) {
	w, r, _, _ := localWorkspaceFixture(t)
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: w.state.Launch.LaunchGeneration, Generation: 7}, ManifestDigest: w.state.Launch.Digest, Root: protocol.WorkspaceRoot{Inode: 1}}
	authority := &testWorkspaceAuthority{bound: &b}
	w.state.Binding = &b
	w.authorize = func(ctx context.Context) error { return authority.Current(ctx, protocol.SupervisorLaunch{}, b) }
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	reader := &boundWorkspaceExplorer{workspace: w}
	for _, member := range w.state.Members {
		sha := originGit(t, member.Source.Repo, "rev-parse", "HEAD")
		req := protocol.ReadCommit{SessionID: w.state.Session, SHA: sha, WorkspaceGitRef: protocol.WorkspaceGitRef{WorkspaceRuntimeRef: b.WorkspaceRuntimeRef, MemberID: member.Member.ID}}
		res := reader.readGit(t.Context(), req, false)
		if !res.OK || res.Commit == nil || res.Commit.SHA != sha {
			t.Fatalf("bound source commit: %+v", res)
		}
		req.Patch = true
		if res := reader.readGit(t.Context(), req, false); !res.OK || res.PatchB64 == "" {
			t.Fatalf("bound source patch: %+v", res)
		}
		req.Generation--
		if res := reader.readGit(t.Context(), req, false); res.OK {
			t.Fatal("stale sandbox source accepted")
		}
	}
	if r.heads != 0 {
		t.Fatal("commit read entered clone instead of trusted source")
	}

	for _, member := range w.state.Members {
		req := protocol.ReadCommit{SessionID: w.state.Session, WorkspaceGitRef: protocol.WorkspaceGitRef{WorkspaceRuntimeRef: b.WorkspaceRuntimeRef, MemberID: member.Member.ID}}
		res := reader.readGit(t.Context(), req, true)
		want, _ := readCloneHead(member.Clone)
		if !res.OK || res.Commit == nil || res.Commit.SHA != want {
			t.Fatalf("bound runtime origin: %+v", res)
		}
	}
	authority.stale = true
	member := w.state.Members[0]
	if res := reader.readGit(t.Context(), protocol.ReadCommit{SessionID: w.state.Session, SHA: originGit(t, member.Source.Repo, "rev-parse", "HEAD"), WorkspaceGitRef: protocol.WorkspaceGitRef{WorkspaceRuntimeRef: b.WorkspaceRuntimeRef, MemberID: member.Member.ID}}, false); res.OK {
		t.Fatal("stale authority returned commit")
	}
}
