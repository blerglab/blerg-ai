//go:build linux || darwin

package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Exercise the exact two aggregate searches used by workspaceProvider followed
// by its shared viewer route, with real traversal rather than invented entries.
func TestWorkspaceQuickOpenRealSearchAndViewer(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterWorkspaceFileRoutes(fx.mux)
	root := t.TempDir()
	member := filepath.Join(root, "member")
	if err := os.Mkdir(member, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"README.md": "real quick-open content", ".env": "hidden"} {
		if err := os.WriteFile(filepath.Join(member, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: topicOwner, Runtime: "host", LaunchGeneration: "real-search", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "member", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "example-org/one"}}}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSessionWorkspace(t.Context(), fx.pool, fx.sess, manifest); err != nil {
		t.Fatal(err)
	}
	generation, err := db.BeginWorkspaceRuntime(t.Context(), fx.pool, fx.sess, manifest.LaunchGeneration, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("native root identity unavailable")
	}
	device := uint64(stat.Dev) //nolint:unconvert // Darwin st_dev is signed; Linux already uses uint64.
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: generation}, ManifestDigest: manifest.Digest, Root: protocol.WorkspaceRoot{Device: device, Inode: stat.Ino}}
	reader, err := daemon.NewLocalExplorer(root, manifest, binding, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	dc := workspaceOwnedDesktop(t, fx.api.hub, fx.pool, topicOwner, "desktop")
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2 WHERE id=$1`, fx.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	dc.SetSandboxClones(true)
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	if !dc.AuthorizeWorkspaceRuntime(fx.sess, binding.WorkspaceRuntimeRef) {
		t.Fatal("runtime authorization refused")
	}
	fx.api.hub.bindPreparedWorkspaceRuntime(t.Context(), fx.pool, dc, protocol.WorkspaceRuntimePrepared{Type: "workspace_runtime_prepared", SessionID: fx.sess, Binding: binding})
	for _, endpoint := range []string{"/workspace?op=search&path=&filter=README.md", "/workspace?op=search&path=&filter=R", "/workspace-file?path=member%2FREADME.md"} {
		request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+endpoint, nil)
		request.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
		response := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			fx.mux.ServeHTTP(&reviewDeadlineWriter{ResponseRecorder: response}, request)
		}()
		select {
		case raw := <-dc.send:
			if strings.HasPrefix(endpoint, "/workspace?") {
				var req protocol.WorkspaceExplorerRequest
				if json.Unmarshal(raw, &req) != nil {
					t.Fatal("invalid search request")
				}
				if req.Operation != "search" || req.Path != "" || req.WorkspaceRuntimeRef != binding.WorkspaceRuntimeRef {
					t.Fatalf("provider request changed scope: %+v", req)
				}
				fx.api.hub.deliverWorkspaceExplorerResult(dc, reader.Explore(t.Context(), req))
			} else {
				var req protocol.ReadWorkspaceFile
				if json.Unmarshal(raw, &req) != nil {
					t.Fatal("invalid viewer request")
				}
				fx.api.hub.deliverWorkspaceFileResult(dc, reader.ReadFile(t.Context(), req))
			}
		case <-time.After(5 * time.Second):
			t.Fatal("runtime request not dispatched")
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("HTTP reply did not complete")
		}
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), ".env") {
			t.Fatalf("real search/viewer HTTP: %d %s", response.Code, response.Body)
		}
		if strings.HasPrefix(endpoint, "/workspace?") {
			if !strings.Contains(response.Body.String(), "member/README.md") {
				t.Fatal("real filename missing from quick-open candidates")
			}
			<-dc.send // scoped completion cancellation
		} else if !strings.Contains(response.Body.String(), base64.StdEncoding.EncodeToString([]byte("real quick-open content"))) {
			t.Fatal("viewer did not return runtime bytes")
		}
	}
}
