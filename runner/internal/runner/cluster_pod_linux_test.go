//go:build linux

package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"golang.org/x/sys/unix"
)

func podExplorerFixture(t *testing.T) *podServices {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "lost+found"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "lost+found"), 0700) })
	if err := os.Mkdir(filepath.Join(root, "repo"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repo", "readme.txt"), []byte("public fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(root, &st); err != nil {
		t.Fatal(err)
	}
	m := protocol.WorkspaceManifest{Version: 2, Layout: protocol.WorkspaceLayoutNames, Kind: "repo", OwnerID: "owner", Runtime: "cluster", LaunchGeneration: "selection", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "member", Name: "repo", Path: "repo", SourceIdentity: "source", CommonDirectoryIdentity: "common", Provider: "github", Destination: "org/repo"}}}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: 1}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Device: st.Dev, Inode: st.Ino}}
	s, err := newPodServices(root, m, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}
func TestPodServicesExplorerViewerPauseAndJoin(t *testing.T) {
	s := podExplorerFixture(t)
	q := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "request", SessionID: "session", WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "selection", Generation: 1}, Operation: "list", Path: ""}
	raw, _ := json.Marshal(q)
	data, err := s.Explorer(t.Context(), raw)
	var reply protocol.WorkspaceExplorerResult
	if err != nil || json.Unmarshal(data, &reply) != nil || !reply.OK || len(reply.Entries) != 1 || reply.Entries[0].Name != "repo" {
		t.Fatalf("real pod explorer: %s %v", data, err)
	}
	file := protocol.ReadWorkspaceFile{RequestID: "file", SessionID: q.SessionID, WorkspaceRuntimeRef: q.WorkspaceRuntimeRef, Path: "repo/readme.txt"}
	read := s.ReadFile(t.Context(), file)
	body, _ := base64.StdEncoding.DecodeString(read.ContentB64)
	if !read.OK || string(body) != "public fixture" {
		t.Fatal("registered in-pod viewer failed", read.ErrorCode)
	}
	file.Path = "../home-secret"
	if s.ReadFile(t.Context(), file).OK {
		t.Fatal("viewer escaped public root")
	}
	stale := q
	stale.Generation++
	if r := s.Explore(t.Context(), stale); r.OK {
		t.Fatal("stale root generation read")
	}
	ctx, done, err := s.reader(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	joined := make(chan error, 1)
	go func() { joined <- s.pause(t.Context()) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("freeze did not cancel active reader")
	}
	select {
	case <-joined:
		t.Fatal("freeze preceded reader join")
	case <-time.After(100 * time.Millisecond):
	}
	done()
	if err := <-joined; err != nil {
		t.Fatal(err)
	}
	if r := s.Explore(t.Context(), q); r.OK {
		t.Fatal("new read entered frozen runtime")
	}
	s.resume()
	if r := s.Explore(t.Context(), q); !r.OK {
		t.Fatal("reader did not recover after verified thaw")
	}
	var wait sync.WaitGroup
	for range 10 {
		wait.Add(1)
		go func() { defer wait.Done(); s.Cancel(); _ = s.Explore(t.Context(), q) }()
	}
	wait.Wait()
	if err = s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r := s.Explore(t.Context(), q); r.OK {
		t.Fatal("closed runtime read")
	}
}
func TestPodSupervisorLostRegistrationCannotInventCleanup(t *testing.T) {
	p := &podRuntime{readers: &podServices{}, launch: protocol.SupervisorLaunch{SessionID: "session", LaunchGeneration: "launch", RuntimeGeneration: 1, Runtime: "cluster", ConnectionID: "connection"}}
	// A once-bound registration may not turn a missing service into a fresh,
	// never-opened shell runtime's empty proof.
	if _, err := p.CloseLaunch(t.Context(), p.launch, "test"); err == nil {
		t.Fatal("lost service fabricated empty proof")
	}
}

func TestPodExplorerHostedUnsafeNameUsesIDFallback(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"opaque-id", "other"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "opaque-id", "readme.txt"), []byte("selected repository"), 0600); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(root, &st); err != nil {
		t.Fatal(err)
	}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "cluster", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{
		{ID: "opaque-id", Name: "secrets", Path: "opaque-id", SourceIdentity: "one", CommonDirectoryIdentity: "git-one", Destination: "org/secrets"},
		{ID: "other", Name: "other", Path: "other", SourceIdentity: "two", CommonDirectoryIdentity: "git-two", Destination: "org/other"},
	}}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: 1}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Device: st.Dev, Inode: st.Ino}}
	service, err := newPodServices(root, m, binding, nil)
	if err != nil {
		t.Fatal("safe ID fallback could not be explored", err)
	}
	defer func() { _ = service.Close(t.Context()) }()
	reply := service.ReadFile(t.Context(), protocol.ReadWorkspaceFile{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Path: "opaque-id/readme.txt"})
	if !reply.OK {
		t.Fatal("selected ID fallback file could not be read", reply)
	}
}
