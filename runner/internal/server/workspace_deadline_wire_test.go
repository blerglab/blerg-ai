//go:build linux || darwin

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

// Exercise a request deadline using the shipped server, daemon client and
// explorer router over TCP, then list a large repository on the same socket.
func TestWorkspaceListDeadlineWireKeepsConnection(t *testing.T) {
	f := newTopicFx(t)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "repo", Runtime: "host", OwnerID: topicOwner, LaunchGeneration: newUUID(), Digest: "fixture", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.sess, m); err != nil {
		t.Fatal(err)
	}
	token := "bdt_" + strings.Repeat("a", 64)
	b := desktopauth.Binding{DesktopID: topicDaemonID, OwnerID: topicOwner, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
	desktopFakeCore(t, f.api, map[string]desktopauth.Binding{token: b})
	srv := httptest.NewServer(f.api.hub.ServeDaemon(topicDaemonToken, f.pool))
	defer srv.Close()
	client := daemon.NewWSClient(daemon.Config{ServerURL: "ws" + strings.TrimPrefix(srv.URL, "http"), DaemonToken: token, DesktopID: topicDaemonID, DaemonMode: "local", DaemonName: "wire-host", HeartbeatInterval: time.Hour, ReconnectInitial: time.Millisecond, ReconnectMax: time.Millisecond})
	authority := daemon.NewWorkspaceAuthority(client)
	connected := make(chan struct{}, 4)
	client.SetOnConnect(func() { connected <- struct{}{} })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); client.Run(ctx, func() []string { return []string{f.sess} }) }()
	defer func() {
		cancel()
		<-done
	}()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("connect")
	}
	launch, err := authority.Launch(t.Context(), f.sess, m, newUUID())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := exec.CommandContext(t.Context(), "git", "init", "-q", root).Run(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "README.md"), []byte("retained host bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := shellruntime.ResolvePersistentIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: launch.RuntimeGeneration}, ManifestDigest: m.Digest, Root: identity.MemberRoot}
	if err = authority.Bind(t.Context(), launch, binding); err != nil {
		t.Fatal(err)
	}
	reader, err := daemon.NewLocalExplorer(root, m, binding, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	// Hold the first reader until cancellation, modelling CPU starvation.
	for i := range 12000 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("entry-%05d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var router daemon.WorkspaceExplorerRouter
	defer router.CancelAll()
	slow := &deadlineWireExplorer{reader: reader, cancelled: make(chan struct{})}
	router.Bind(f.sess, slow)
	client.OnMessage("workspace_explorer", func(raw []byte) { router.Handle(ctx, client, raw) })
	client.OnMessage("cancel_workspace_explorer", func(raw []byte) { router.Handle(ctx, client, raw) })
	dc := desktopWait(t, f.api.hub, topicDaemonID)
	call, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	_, err = f.api.hub.requestWorkspaceExplorer(call, dc, protocol.WorkspaceExplorerRequest{SessionID: f.sess, Operation: "list", WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	select {
	case <-slow.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel not delivered")
	}
	router.Bind(f.sess, reader)
	probe, stopProbe := context.WithTimeout(ctx, 5*time.Second)
	defer stopProbe()
	res, err := f.api.hub.requestWorkspaceExplorer(probe, dc, protocol.WorkspaceExplorerRequest{SessionID: f.sess, Operation: "list", WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef})
	if err != nil || !res.OK {
		t.Fatalf("probe: %v, code=%s", err, res.ErrorCode)
	}
	if f.api.hub.GetDaemon(topicDaemonID) != dc {
		t.Fatal("deadline replaced connection")
	}
	if err := authority.Current(probe, launch, binding); err != nil {
		t.Fatal(err)
	}
	t.Logf("deadline isolated; subsequent list returned %d entries on original socket", len(res.Entries))
}

type deadlineWireExplorer struct {
	reader    *daemon.LocalExplorer
	cancelled chan struct{}
}

func (e *deadlineWireExplorer) Explore(ctx context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	<-ctx.Done()
	defer close(e.cancelled)
	return e.reader.Explore(ctx, req)
}
