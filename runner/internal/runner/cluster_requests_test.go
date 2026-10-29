package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type requestGit struct{}

func (requestGit) Prepare(context.Context, clusterworkspace.Member, string, bool) (string, string, error) {
	return "base", "head", nil
}
func (requestGit) Head(context.Context, clusterworkspace.Member) (string, error)       { return "head", nil }
func (requestGit) Push(context.Context, clusterworkspace.Member, string, string) error { return nil }

type requestLink struct {
	handlers map[string]func([]byte)
	replies  chan []byte
}

func (l *requestLink) OnMessage(kind string, fn func([]byte)) { l.handlers[kind] = fn }
func (l *requestLink) SendBounded(ctx context.Context, v any) error {
	raw, _ := json.Marshal(v)
	select {
	case l.replies <- raw:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type requestServices struct {
	started  chan string
	release  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (s *requestServices) Explorer(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	s.started <- "explorer"
	select {
	case <-s.release:
		return json.RawMessage(`{"ok":true,"entries":[],"session_id":"forged"}`), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s *requestServices) Shell(ctx context.Context, _ json.RawMessage, emit func(json.RawMessage) error) error {
	s.started <- "shell"
	return emit(json.RawMessage(`{"ok":true,"data":"cHR5"}`))
}
func (s *requestServices) Cancel()                     { s.once.Do(func() { close(s.canceled) }) }
func (s *requestServices) Close(context.Context) error { return nil }
func requestFixture(t *testing.T) (*clusterRequests, *requestLink, *requestServices) {
	t.Helper()
	p := clusterworkspace.Plan{SessionID: "session", Manifest: protocol.WorkspaceManifest{Version: 2, Layout: protocol.WorkspaceLayoutNames, Kind: "repo", OwnerID: "owner", Runtime: "cluster", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Provider: "github", Destination: "org/one"}}}, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, Members: []clusterworkspace.Member{{ID: "one", Provider: "github", CloneURL: "https://github.com/org/one.git", PushURL: "https://github.com/org/one.git", CredentialHost: "github.com", DestinationIdentity: "org/one"}}}
	rt, err := clusterworkspace.New(p, requestGit{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Prepare(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	l := &requestLink{handlers: map[string]func([]byte){}, replies: make(chan []byte, 10)}
	s := &requestServices{started: make(chan string, 10), release: make(chan struct{}), canceled: make(chan struct{})}
	r := newClusterRequests(context.Background(), p.SessionID, p.Runtime, rt, s, l)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := r.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return r, l, s
}
func requestRaw(kind, id string, gen int64) []byte {
	raw, _ := json.Marshal(struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		SessionID string `json:"session_id"`
		protocol.WorkspaceRuntimeRef
	}{kind, id, "session", protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: gen}})
	return raw
}
func requestReceive(t *testing.T, c <-chan []byte) map[string]any {
	t.Helper()
	select {
	case raw := <-c:
		var fields map[string]any
		if json.Unmarshal(raw, &fields) != nil {
			t.Fatal("bad reply")
		}
		return fields
	case <-time.After(time.Second):
		t.Fatal("no runtime reply")
		return nil
	}
}
func TestClusterRequestsOutboundIsolationAndBudgets(t *testing.T) {
	r, l, s := requestFixture(t)
	l.handlers["workspace_explorer"](requestRaw("workspace_explorer", "old", 2))
	r.mu.Lock()
	active := len(r.active)
	r.mu.Unlock()
	if active != 0 {
		t.Fatal("stale request reached backend")
	}
	l.handlers["workspace_explorer"](requestRaw("workspace_explorer", "first", 1))
	<-s.started
	l.handlers["workspace_explorer"](requestRaw("workspace_explorer", "busy", 1))
	if got := requestReceive(t, l.replies); got["error_code"] != "busy" {
		t.Fatal(got)
	}
	// A blocked explorer does not block the independent PTY request lane.
	l.handlers["owner_shell"](requestRaw("owner_shell", "pty", 1))
	<-s.started
	if got := requestReceive(t, l.replies); got["type"] != "owner_shell_result" || got["request_id"] != "pty" {
		t.Fatal(got)
	}
	close(s.release)
	if got := requestReceive(t, l.replies); got["session_id"] != "session" || got["type"] != "workspace_explorer_result" || got["generation"] != float64(1) {
		t.Fatal(got)
	}
	r.mu.Lock()
	epoch := r.epoch
	r.mu.Unlock()
	if err := r.reply(context.Background(), clusterRequest{Type: "workspace_explorer", RequestID: "huge"}, json.RawMessage(`{"entries":"`+string(make([]byte, 1<<20))+`"}`), "", epoch); err != nil {
		t.Fatal(err)
	}
	if got := requestReceive(t, l.replies); got["error_code"] != "unavailable" {
		t.Fatal(got)
	}
}
func TestClusterRequestsDisconnectJoinsAndFencesDelivery(t *testing.T) {
	r, l, s := requestFixture(t)
	l.handlers["workspace_explorer"](requestRaw("workspace_explorer", "first", 1))
	<-s.started
	r.Disconnect()
	<-s.canceled
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(l.replies) != 0 {
		t.Fatal("canceled reply reached replacement link")
	}
	l.handlers["owner_shell"](requestRaw("owner_shell", "late", 1))
	if len(s.started) != 0 {
		t.Fatal("late admission after close")
	}
}
func TestClusterRequestsUnboundFailsClosed(t *testing.T) {
	r, l, _ := requestFixture(t)
	r.mu.Lock()
	r.services = nil
	r.mu.Unlock()
	l.handlers["owner_shell"](requestRaw("owner_shell", "pty", 1))
	if got := requestReceive(t, l.replies); got["error_code"] != "unavailable" {
		t.Fatal(got)
	}
}

func TestClusterRunnerRefusesMissingContainmentBeforeAllocation(t *testing.T) {
	home := t.TempDir()
	_, _, err := prepareClusterDisk(Config{Home: home, SessionID: "session", Workspace: &clusterworkspace.Plan{SessionID: "session"}})
	if err == nil {
		t.Fatal("uncontained pod agent was admitted")
	}
	entries, readErr := os.ReadDir(home)
	if readErr != nil || len(entries) != 0 {
		t.Fatal("allocated storage before containment was available", readErr)
	}
}

func TestClusterRunnerRefusesAgentWritableSnapshotLocation(t *testing.T) {
	home := t.TempDir()
	// Even an injected containment adapter cannot authorize a snapshot cache in
	// the agent's own HOME. P6 must explicitly supply its protected storage.
	for _, private := range []string{"", filepath.Join(home, "private")} {
		_, _, err := prepareClusterDisk(Config{Home: home, SessionID: "session", ClusterPrivateRoot: private, ClusterWriters: requestWriters{}, Workspace: &clusterworkspace.Plan{SessionID: "session"}})
		if err == nil {
			t.Fatal("accepted writable snapshot location")
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("allocated unsafe snapshot state", err)
	}
}

type requestWriters struct{}

func (requestWriters) Freeze(context.Context) error { return nil }
func (requestWriters) Thaw(context.Context) error   { return nil }
func (requestWriters) Stop(context.Context) error   { return nil }

func TestPodCommandLauncherRejectsFencedPreparation(t *testing.T) {
	requests, _, _ := requestFixture(t)
	requests.runtime.FenceAdmission()
	called := false
	start := workspaceCommandLauncher(t.Context(), requests.runtime, func(*exec.Cmd) error { called = true; return nil })
	if start(exec.Command("true")) == nil || called {
		t.Fatal("engine command bypassed runtime coordinator")
	}
}
