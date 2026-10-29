package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// #242: a host folder with no Git repository is a valid zero-member selection, and the
// preview must say so with arrays. It sent "members": null, which the picker could not read.
func TestWorkspacePreviewOfFolderWithoutRepositoriesSendsEmptyArrays(t *testing.T) {
	f := newPrivFixture(t)
	id := newUUID()
	token := "bdt_" + strings.Repeat("e", 64)
	desktopFakeCore(t, f.api, map[string]desktopauth.Binding{token: {DesktopID: id, OwnerID: privAcctA, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}})
	srv := httptest.NewServer(f.hub.ServeDaemon(spawnTokenDaemonTok, f.pool))
	defer srv.Close()
	c := desktopConnect(t, srv.URL, token, id, "empty-folder-desktop", true)
	dc := desktopWait(t, f.hub, id)
	dc.DiscoveryVersion = 1
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "notes", "todo.txt"), []byte("plain folder\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dc.SetReposRoot(root)
	dc.WorkspaceCapabilities.ManifestVersion = 2
	dc.WorkspaceCapabilities.LinuxHost = true
	dc.WorkspaceCapabilities.Sandbox = true
	service := discovery.NewOwned(func() string { return root }, id)
	go func() {
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			var header struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &header)
			if header.Type == "discover_workspace" {
				var req protocol.DiscoverWorkspace
				_ = json.Unmarshal(raw, &req)
				_ = c.WriteJSON(service.Handle(context.Background(), req))
			}
		}
	}()
	browser := f.tok(t, "human", privAcctA, "session.start")
	r := httptest.NewRequest("GET", "/api/workspace/discovery?daemon_id="+id+"&path=.", nil)
	r.Header.Set("Authorization", "Bearer "+browser)
	w := &reviewDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	f.api.HandleWorkspaceDiscovery(w, r)
	if w.Code != 200 {
		t.Fatalf("discovery: %d %s", w.Code, w.Body.String())
	}
	var listed struct {
		Page protocol.DiscoveryPage `json:"page"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	var folder *protocol.SelectionRef
	for _, entry := range listed.Page.Entries {
		if entry.Path == "notes" {
			folder = &protocol.SelectionRef{Path: entry.Path, SourceIdentity: entry.SourceIdentity}
		}
	}
	if folder == nil {
		t.Fatalf("folder not listed: %s", w.Body.String())
	}
	selection := protocol.WorkspaceSelectionRequest{DaemonID: id, OwnershipRevision: 1, ConnectionGeneration: dc.DesktopGeneration, RootIdentity: listed.Page.RootIdentity, Folder: folder, Members: []protocol.SelectionRef{}}
	raw, _ := json.Marshal(map[string]any{"runtime": "host", "selection": selection, "expand": true})
	r = httptest.NewRequest("POST", "/api/workspace/preview", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+browser)
	w = &reviewDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	f.api.HandleWorkspacePreview(w, r)
	if w.Code != 200 {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		OK        bool `json:"ok"`
		Selection struct {
			Members *[]json.RawMessage `json:"members"`
			Folder  *protocol.SelectionRef
		} `json:"selection"`
		Members *[]json.RawMessage `json:"members"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Selection.Folder == nil || body.Selection.Folder.Path != "notes" {
		t.Fatalf("empty folder not resolved: %s", w.Body.String())
	}
	if body.Selection.Members == nil || len(*body.Selection.Members) != 0 || body.Members == nil || len(*body.Members) != 0 {
		t.Fatalf("members must be empty arrays, never null: %s", w.Body.String())
	}
}
