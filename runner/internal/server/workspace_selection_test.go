package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
	"github.com/jackc/pgx/v5/pgxpool"
)

func workspaceOwnedDesktop(t *testing.T, h *Hub, pool *pgxpool.Pool, owner, name string) *DaemonConn {
	t.Helper()
	b := desktopauth.Binding{DesktopID: newUUID(), OwnerID: owner, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
	dc := &DaemonConn{ID: b.DesktopID, Name: name, Mode: "local", ReposRoot: "/private-root-" + owner, DesktopBinding: &b, DesktopGeneration: newUUID(), DiscoveryVersion: 1, send: make(chan []byte, 64)}
	err := db.WithDesktopFence(t.Context(), pool, dc.ID, func(ctx context.Context) error {
		return db.BindDesktopConnection(ctx, pool, b, name, "local", dc.ReposRoot, dc.DesktopGeneration)
	})
	if err != nil {
		t.Fatal(err)
	}
	h.Register(dc)
	return dc
}
func TestWorkspaceDesktopPrivacyAcrossHTTPPayloadsAndETags(t *testing.T) {
	f := newPrivFixture(t)
	a := workspaceOwnedDesktop(t, f.hub, f.pool, privAcctA, "same-desktop")
	b := workspaceOwnedDesktop(t, f.hub, f.pool, privAcctB, "same-desktop")
	a.SetCheckedOutRepos([]string{"same-repo"})
	a.SetRepoOrigins(map[string]protocol.RepoOrigin{"same-repo": {Provider: "github", FullName: "person-a/private-repo"}}, nil)
	b.SetCheckedOutRepos([]string{"same-repo"})
	// Use distinguishable folder names as well as a common name: a same-name
	// merge must never turn the other person's remote into launch authority.
	b.SetCheckedOutRepos([]string{"same-repo", "private-B"})
	b.SetRepoOrigins(map[string]protocol.RepoOrigin{"same-repo": {Provider: "github", FullName: "person-b/private-repo"}}, nil)
	unknown := &DaemonConn{ID: newUUID(), Name: "unknown-private", ReposRoot: "/unknown-private", send: make(chan []byte, 8)}
	if err := db.UpsertDaemon(t.Context(), f.pool, unknown.ID, unknown.Name, "local", unknown.ReposRoot); err != nil {
		t.Fatal(err)
	}
	f.hub.Register(unknown)
	owned := f.hub.ownedHostDaemons(t.Context(), privAcctA)
	foundOwn := false
	for _, dc := range owned {
		if dc == a {
			foundOwn = true
		}
		if dc.DesktopBinding == nil || dc.DesktopBinding.OwnerID != privAcctA {
			t.Fatal("foreign desktop reached collection")
		}
	}
	if !foundOwn {
		t.Fatal("owned desktop missing from collection")
	}
	catalog := NewRepoLister("org", "", f.hub)
	catalog.cachedAt = time.Now()
	catalog.cached = []RepoInfo{{Name: "same-repo", FullName: "org/same-repo", Provider: "github"}}
	f.api.repos = catalog
	request := func(handler http.HandlerFunc, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler(&reviewDeadlineWriter{ResponseRecorder: w}, r)
		return w
	}
	tokenA := f.tok(t, "human", privAcctA, "session.start", "account.manage")
	for _, handler := range []http.HandlerFunc{f.api.HandleGetDaemons, f.api.HandleGetRepos} {
		w := request(handler, tokenA)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		for _, secret := range []string{b.ID, b.ReposRoot, "private-B", "unknown-private", "person-b/private-repo"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("foreign desktop metadata %q in %s", secret, w.Body.String())
			}
		}
	}
	if own := request(f.api.HandleGetDaemons, tokenA); !strings.Contains(own.Body.String(), a.ID) {
		t.Fatal("owner inventory was discarded")
	}
	first := request(f.api.HandleGetRepos, tokenA)
	b.SetCheckedOutRepos([]string{"changed-private-B"})
	second := request(f.api.HandleGetRepos, tokenA)
	if first.Header().Get("ETag") == "" || first.Header().Get("ETag") != second.Header().Get("ETag") {
		t.Fatal("foreign inventory influenced owner's ETag")
	}
	for _, typ := range []string{"initial_state", "daemon_connected", "daemon_limits_changed"} {
		raw, _ := json.Marshal(map[string]any{"type": typ, "daemon": map[string]any{"id": b.ID, "repos_root": b.ReposRoot}, "daemon_id": b.ID, "daemons": []map[string]any{{"id": a.ID}, {"id": b.ID}, {"id": unknown.ID}}})
		filtered, ok := f.hub.desktopPayloadFor(t.Context(), privAcctA, raw)
		if ok && (bytes.Contains(filtered, []byte(b.ID)) || bytes.Contains(filtered, []byte(b.ReposRoot)) || bytes.Contains(filtered, []byte(unknown.ID))) {
			t.Fatalf("%s leaked: %s", typ, filtered)
		}
	}
	// Administrator status did not expand the scope above. Assignment fences
	// an already prepared and queued payload at its final delivery check.
	raw, _ := json.Marshal(map[string]any{"type": "daemon_connected", "daemon": map[string]any{"id": a.ID, "repos_root": a.ReposRoot, "ownership_revision": 1, "connection_generation": a.DesktopGeneration}})
	_, err := db.AssignDesktop(t.Context(), f.pool, a.ID, privAcctB, 1)
	if err != nil {
		t.Fatal(err)
	}
	if filtered, ok := f.hub.desktopPayloadFor(t.Context(), privAcctA, raw); ok && bytes.Contains(filtered, []byte(a.ReposRoot)) {
		t.Fatal("queued metadata survived reassignment")
	}
}
func TestWorkspaceDiscoveryRejectsForeignAndStaleSelectionBeforeDispatch(t *testing.T) {
	f := newPrivFixture(t)
	a := workspaceOwnedDesktop(t, f.hub, f.pool, privAcctA, "desktop")
	b := workspaceOwnedDesktop(t, f.hub, f.pool, privAcctB, "desktop")
	token := f.tok(t, "human", privAcctA, "session.start")
	for _, dc := range []*DaemonConn{a, b} {
		selection := protocol.WorkspaceSelectionRequest{DaemonID: dc.ID, OwnershipRevision: 1, ConnectionGeneration: dc.DesktopGeneration, RootIdentity: "root", Members: []protocol.SelectionRef{{Path: "repo", SourceIdentity: "source"}}}
		if dc == a {
			selection.ConnectionGeneration = "stale"
		}
		body, _ := json.Marshal(map[string]any{"runtime": "host", "selection": selection})
		req := httptest.NewRequest("POST", "/api/workspace/preview", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.api.HandleWorkspacePreview(w, req)
		if w.Code != 409 {
			t.Fatalf("forged scope accepted: %d %s", w.Code, w.Body.String())
		}
		select {
		case msg := <-dc.send:
			t.Fatalf("unauthorized discovery dispatched: %s", msg)
		default:
		}
	}
}
func TestWorkspaceChildExactSubsetAndRuntimePaths(t *testing.T) {
	parent := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "sandbox", LaunchGeneration: "parent", Root: protocol.WorkspaceRoot{Inode: 1}, Members: []protocol.WorkspaceMember{{ID: "a", Name: "same", Path: "members/a", SourceIdentity: "s-a", CommonDirectoryIdentity: "c-a", Destination: "s-a"}, {ID: "b", Name: "same", Path: "members/b", SourceIdentity: "s-b", CommonDirectoryIdentity: "c-b", Destination: "s-b"}}}
	parent.Digest = workspaceselection.Digest(parent)
	for _, cs := range []childStart{{Repos: []string{"a"}}, {AllRepos: true}} {
		if err := selectWorkspaceChild(&cs, parent); err != nil {
			t.Fatal(err)
		}
		if cs.Workspace.Members[0].Path != "members/a" || cs.Runtime != "docker" || protocol.ValidateWorkspaceChild(parent, *cs.Workspace) != nil {
			t.Fatal("child changed parent authority or paths")
		}
	}
	for _, cs := range []childStart{{Repos: []string{"same"}}, {Repos: []string{"outside"}}, {Repos: []string{"a", "a"}}, {Repos: []string{"a"}, Runtime: "daemon"}, {AllRepos: true, Repo: "a"}, {NoRepo: true}} {
		if selectWorkspaceChild(&cs, parent) == nil {
			t.Fatalf("invalid child accepted: %+v", cs)
		}
	}
}

func fixtureOwnDesktop(t *testing.T, pool *pgxpool.Pool, dc *DaemonConn, owner string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2 WHERE id=$1`, dc.ID, owner); err != nil {
		t.Fatal(err)
	}
	b := desktopauth.Binding{DesktopID: dc.ID, OwnerID: owner, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
	dc.DesktopBinding = &b
	dc.DesktopGeneration = newUUID()
	if err := db.BindDesktopConnection(t.Context(), pool, b, dc.Name, "local", dc.CurrentReposRoot(), dc.DesktopGeneration); err != nil {
		t.Fatal(err)
	}
}

func workspaceBoardProof(t *testing.T, api *API, owner string) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	return mintRunnerToken(t, priv, "core-1", identity.Claims{Sub: owner, Kind: "human", Sid: testSID, Aud: "blerg-board", Caps: []string{"session.start"}, ExpiresAt: time.Now().Add(time.Hour).Unix()})
}

// Existing feature fixtures now declare the desktop's person explicitly.
func ownedInventoryAPI(t *testing.T, h *Hub, token string, repos *RepoLister, vapid string) *API {
	t.Helper()
	pool := connectSrvTestDB(t)
	a := NewAPI(h, pool, token, repos, vapid)
	for _, dc := range h.GetHostDaemons() {
		fixtureOwnDesktop(t, pool, dc, "user-1")
	}
	return a
}
func workspaceFixturePrincipal() runnerPrincipal {
	return runnerPrincipal{Kind: "human", Sub: "user-1", Sid: testSID, Caps: []string{"session.start"}}
}

func TestWorkspaceLaunchBuildsAndPersistsTrustedManifestWithFakeRuntime(t *testing.T) {
	f := newPrivFixture(t)
	dc := workspaceOwnedDesktop(t, f.hub, f.pool, privAcctA, "desktop")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		for _, directory := range []string{"objects", "refs"} {
			if err := os.MkdirAll(filepath.Join(root, name, ".git", directory), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, name, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dc.SetReposRoot(root)
	dc.WorkspaceCapabilities.ManifestVersion = 2
	service := discovery.NewOwned(func() string { return root }, dc.ID)
	page := service.Handle(t.Context(), protocol.DiscoverWorkspace{Type: "discover_workspace", RequestID: "fixture", Operation: "list", Path: "."})
	if !page.OK || len(page.Page.Entries) != 2 {
		t.Fatalf("fixture discovery: %+v", page)
	}
	selection := protocol.WorkspaceSelectionRequest{DaemonID: dc.ID, OwnershipRevision: 1, ConnectionGeneration: dc.DesktopGeneration, RootIdentity: page.Page.RootIdentity}
	for _, e := range page.Page.Entries {
		selection.Members = append(selection.Members, protocol.SelectionRef{Path: e.Path, SourceIdentity: e.SourceIdentity})
	}
	spawned := make(chan protocol.SpawnSession, 2)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case raw := <-dc.send:
				var header struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(raw, &header)
				if header.Type == "discover_workspace" {
					var req protocol.DiscoverWorkspace
					_ = json.Unmarshal(raw, &req)
					res := service.Handle(t.Context(), req)
					f.hub.deliverWorkspaceDiscovery(dc, res)
				}
				if header.Type == "spawn_session" {
					var msg protocol.SpawnSession
					_ = json.Unmarshal(raw, &msg)
					spawned <- msg
				}
			}
		}
	}()
	token := f.tok(t, "human", privAcctA, "session.start")
	post := func(body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/sessions", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.api.HandlePostSessions(w, req)
		return w
	}
	w := post(map[string]any{"selection": selection, "runtime": "daemon", "kind": "agent", "title": "Selected workspace", "initial_prompt": "Review the selected repositories"})
	if w.Code != 202 {
		t.Fatalf("launch %d %s", w.Code, w.Body.String())
	}
	var msg protocol.SpawnSession
	select {
	case msg = <-spawned:
	case <-time.After(time.Second):
		t.Fatal("authorized workspace not dispatched")
	}
	if msg.Workspace == nil || msg.Workspace.OwnerID != privAcctA || msg.Workspace.Kind != "folder" || len(msg.Workspace.Members) != 2 || msg.Workspace.Digest != workspaceselection.Digest(*msg.Workspace) {
		t.Fatal("server did not construct the immutable manifest")
	}
	if msg.WorkspaceCapabilities != dc.WorkspaceCapabilities {
		t.Fatal("WORKSPACE_LAUNCH_CAPABILITIES: selected launch lost negotiated capabilities", msg.WorkspaceCapabilities)
	}
	raw, _ := json.Marshal(msg.Workspace)
	if bytes.Contains(raw, []byte(root)) {
		t.Fatal("private source path entered manifest")
	}
	for _, m := range msg.Workspace.Members {
		if m.Path != "members/"+m.ID || m.Project != "" {
			t.Fatal("member paths or project authority were guessed")
		}
	}
	stored, err := db.GetSessionWorkspace(t.Context(), f.pool, msg.SessionID)
	if err != nil || stored == nil || stored.Manifest.Digest != msg.Workspace.Digest {
		t.Fatal("manifest was not persisted before dispatch")
	}
	if !strings.Contains(msg.InitialPrompt, "commit each changed repository") && !strings.Contains(msg.InitialPrompt, "Commit each changed repository") {
		t.Fatal("member commit guidance missing")
	}
	if w := post(map[string]any{"selection": selection, "runtime": "daemon", "kind": "agent", "workspace": msg.Workspace}); w.Code != 400 {
		t.Fatal("caller manifest was accepted")
	}
	// A peer with discovery but no v2 runtime can still start one top-level
	// repository through the existing legacy behavior. Multi-selection refuses.
	dc.WorkspaceCapabilities.ManifestVersion = 0
	if w := post(map[string]any{"selection": selection, "runtime": "daemon", "kind": "agent"}); w.Code != 409 {
		t.Fatalf("old runtime accepted multi-selection: %d", w.Code)
	}
	selection.Members = selection.Members[:1]
	if w := post(map[string]any{"selection": selection, "runtime": "daemon", "kind": "agent"}); w.Code != 409 {
		t.Fatal("new discovery peer downgraded an immutable selection to a bare name")
	}

}

func TestWorkspaceConfidentialMetadataIsFilteredFromEveryReplayAndResult(t *testing.T) {
	f := newPrivFixture(t)
	sid := f.pubSession
	secret := "private-desktop-location"
	raw, _ := json.Marshal(protocol.StartStagePayload{Plan: true, Runtime: "docker", Stages: []protocol.StartStage{{ID: "daemon", Label: "Waiting for daemon", State: "active", Detail: secret, Hint: secret}}})
	if _, _, err := db.AppendAgentEvent(t.Context(), f.pool, sid, newUUID(), "start_stage", string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEvent(t.Context(), f.pool, sid, newUUID(), "assistant_text", `{"text":"PERSON CHOSE TO SHARE","done":true}`); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionError(t.Context(), f.pool, sid, secret); err != nil {
		t.Fatal(err)
	}
	principal := runnerPrincipal{Kind: "human", Sub: privAcctB, Caps: []string{"session.start"}, Sid: testSID}
	for _, before := range []bool{false, true} {
		var body map[string]any
		var problem *APIError
		if before {
			body, problem = f.api.EventsBefore(t.Context(), principal, sid, 1000, 200)
		} else {
			body, problem = f.api.Events(t.Context(), principal, sid, 0, 200)
		}
		raw, _ := json.Marshal(body)
		if problem != nil || bytes.Contains(raw, []byte(secret)) || !bytes.Contains(raw, []byte("PERSON CHOSE TO SHARE")) {
			t.Fatalf("replay privacy=%s %v", raw, problem)
		}
	}
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), f.pool, sid, "ended", &now, systemEnd(db.EndReasonProcessExited)); err != nil {
		t.Fatal(err)
	}
	for _, handler := range []http.HandlerFunc{f.api.HandleRunnerEventStream, f.api.HandleRunnerEventsLive} {
		for _, scope := range []string{"person-b", "board-background"} {
			token := f.tok(t, "human", privAcctB, "session.start")
			req := httptest.NewRequest("GET", "/api/runner/sessions/"+sid+"/events", nil)
			if scope == "board-background" {
				token = f.tok(t, "human", privAcctA, "session.start")
				req.Header.Set(desktopViewerHeader, "none")
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.SetPathValue("id", sid)
			w := httptest.NewRecorder()
			handler(w, req)
			if w.Code != 200 || strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), "PERSON CHOSE TO SHARE") {
				t.Fatalf("stream %s: %d %s", scope, w.Code, w.Body.String())
			}
		}
	}
	// A separate direct owner read keeps the actual stage detail.
	stage, _ := json.Marshal(protocol.StartStagePayload{Stages: []protocol.StartStage{{ID: "daemon", Detail: secret}}})
	own := f.hub.desktopEventPayload(t.Context(), privAcctA, sid, "start_stage", string(stage))
	if !bytes.Contains(own, []byte(secret)) {
		t.Fatal("owner lost desktop stage detail")
	}
}

type workspaceClusterFake struct {
	manifest *protocol.WorkspaceManifest
	prepared bool
}

func (*workspaceClusterFake) Capabilities() protocol.WorkspaceCapabilities {
	return protocol.WorkspaceCapabilities{ManifestVersion: 2}
}
func (f *workspaceClusterFake) Prepare(_ context.Context, m protocol.WorkspaceManifest) (protocol.WorkspaceRoot, error) {
	f.prepared = true
	return protocol.WorkspaceRoot{Device: 7, Inode: 8}, nil
}
func (f *workspaceClusterFake) Start(_ context.Context, m protocol.WorkspaceManifest, _ WorkspaceLaunchRequest) (string, error) {
	f.manifest = &m
	return newUUID(), nil
}
func TestWorkspaceHostedSelectionUsesAuthorizedSourcesAndFakeClusterConsumer(t *testing.T) {
	f := newPrivFixture(t)
	catalog := NewRepoLister("org", "", f.hub)
	catalog.cachedAt = time.Now()
	catalog.cached = []RepoInfo{{Name: "api", FullName: "org/api", Provider: "github"}, {Name: "web", FullName: "org/web", Provider: "gitlab"}}
	f.api.repos = catalog
	consumer := &workspaceClusterFake{}
	f.api.SetWorkspaceClusterConsumer(consumer)
	token := f.tok(t, "human", privAcctA, "session.start")
	selection := protocol.WorkspaceSelectionRequest{Members: []protocol.SelectionRef{}, Hosted: []protocol.HostedSelectionRef{{Provider: "github", FullName: "org/api"}, {Provider: "gitlab", FullName: "org/web"}}}
	post := func(s protocol.WorkspaceSelectionRequest) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"runtime": "cluster", "kind": "agent", "selection": s})
		req := httptest.NewRequest("POST", "/api/sessions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.api.HandlePostSessions(w, req)
		return w
	}
	if w := post(selection); w.Code != 202 {
		t.Fatalf("hosted selection: %d %s", w.Code, w.Body.String())
	}
	m := consumer.manifest
	if !consumer.prepared || m == nil || m.Validate() != nil || m.OwnerID != privAcctA || m.Runtime != "cluster" || len(m.Members) != 2 {
		t.Fatal("authorized immutable cluster selection was not emitted")
	}
	for _, member := range m.Members {
		if member.Path != member.ID || member.Project == "" || member.Provider == "" {
			t.Fatal("cluster member was not provider-qualified")
		}
	}
	consumer.manifest = nil
	forged := selection
	forged.Hosted = append([]protocol.HostedSelectionRef(nil), selection.Hosted...)
	forged.Hosted[0].FullName = "other-person/private"
	if w := post(forged); w.Code != 409 || consumer.manifest != nil {
		t.Fatal("unlisted hosted source reached consumer")
	}
	forged = selection
	forged.DaemonID = f.dc.ID
	if w := post(forged); w.Code != 409 {
		t.Fatal("desktop source borrowed cluster authority")
	}
	f.api.SetWorkspaceClusterConsumer(nil)
	if w := post(selection); w.Code != 409 {
		t.Fatal("legacy cluster silently reduced a multi-selection")
	}
}

func TestWorkspaceDeliveryHoldsOwnershipFenceThroughTransportWrite(t *testing.T) {
	f := newPrivFixture(t)
	dc := workspaceOwnedDesktop(t, f.hub, f.pool, privAcctA, "desktop")
	raw, _ := json.Marshal(map[string]any{"type": "daemon_connected", "daemon": map[string]any{"id": dc.ID, "repos_root": dc.ReposRoot, "ownership_revision": 1, "connection_generation": dc.DesktopGeneration}})
	wrote := false
	err := f.hub.deliverDesktopPayload(t.Context(), privAcctA, raw, func(payload []byte) error {
		tx, e := f.pool.Begin(t.Context())
		if e != nil {
			return e
		}
		defer func() { _ = tx.Rollback(t.Context()) }()
		var acquired bool
		if e = tx.QueryRow(t.Context(), `SELECT pg_try_advisory_xact_lock(hashtextextended($1::uuid::text,876434))`, dc.ID).Scan(&acquired); e != nil {
			return e
		}
		if acquired {
			t.Error("transport write was outside the persisted ownership fence")
		}
		wrote = bytes.Contains(payload, []byte(dc.ReposRoot))
		return nil
	})
	if err != nil || !wrote {
		t.Fatalf("owner delivery: %v, wrote=%v", err, wrote)
	}
	// A same-owner reconnect replaces the generation, even without reassignment.
	dc.DesktopGeneration = newUUID()
	if err := db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
		return db.BindDesktopConnection(ctx, f.pool, *dc.DesktopBinding, dc.Name, "local", dc.ReposRoot, dc.DesktopGeneration)
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.hub.deliverDesktopPayload(t.Context(), privAcctA, raw, func([]byte) error { t.Error("obsolete connection inventory reached the transport"); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AssignDesktop(t.Context(), f.pool, dc.ID, privAcctB, 1); err != nil {
		t.Fatal(err)
	}
	if err := f.hub.deliverDesktopPayload(t.Context(), privAcctA, raw, func([]byte) error { t.Error("reassigned desktop reached the old owner's transport"); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceHTTPModelSnapshotIsFencedAfterCollection(t *testing.T) {
	f := newPrivFixture(t)
	dc := workspaceOwnedDesktop(t, f.hub, f.pool, privAcctA, "desktop")
	sources := models.NewRegistry()
	sources.Register("codex", models.SourceFunc(func(ctx context.Context, _ string) (models.List, error) {
		_, err := db.AssignDesktop(ctx, f.pool, dc.ID, privAcctB, 1)
		return models.List{Source: models.SourceDaemon, Models: []models.Model{{ID: "private-model", Name: "Private model"}}}, err
	}))
	f.api.SetModelSources(sources)
	req := httptest.NewRequest("GET", "/api/models/codex?daemon_id="+dc.ID, nil)
	req.SetPathValue("engine", "codex")
	req.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctA, "session.start"))
	w := httptest.NewRecorder()
	f.api.HandleGetModels(w, req)
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "private-model") {
		t.Fatalf("metadata collected before assignment reached HTTP: %d %s", w.Code, w.Body.String())
	}
}

func TestWorkspaceHTTPCollectionCannotAdoptUnfencedNewConnection(t *testing.T) {
	f := newPrivFixture(t)
	original := workspaceOwnedDesktop(t, f.hub, f.pool, privAcctA, "first")
	original.SetEngineModels(map[string]models.Report{"codex": {Models: []models.Model{{ID: "original", Name: "Original"}}, FetchedAt: time.Now()}})
	sources := models.NewRegistry()
	sources.Register("codex", models.SourceFunc(func(ctx context.Context, id string) (models.List, error) {
		late := workspaceOwnedDesktop(t, f.hub, f.pool, privAcctA, "new")
		late.SetEngineModels(map[string]models.Report{"codex": {Models: []models.Model{{ID: "unfenced", Name: "Unfenced"}}, FetchedAt: time.Now()}})
		return NewModelSources(nil, f.hub).Models(ctx, "codex", id)
	}))
	f.api.SetModelSources(sources)
	req := httptest.NewRequest("GET", "/api/models/codex", nil)
	req.SetPathValue("engine", "codex")
	req.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctA, "session.start"))
	w := httptest.NewRecorder()
	f.api.HandleGetModels(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "original") || strings.Contains(w.Body.String(), "unfenced") {
		t.Fatalf("response adopted an unfenced connection: %d %s", w.Code, w.Body.String())
	}
}

func (f *workspaceClusterFake) StartChild(_ context.Context, _ protocol.WorkspaceManifest, m protocol.WorkspaceManifest, spec SessionJobSpec) error {
	f.prepared = true
	f.manifest = &m
	if spec.SessionID == "" || spec.ExtraEnv[sessionTokenEnv] == "" || !spec.RequireRecoveryReady || !spec.PluginsResolved {
		return fmt.Errorf("missing normal child policy")
	}
	return nil
}
