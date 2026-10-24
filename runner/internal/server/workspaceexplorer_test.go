package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceExplorerSlotsReserveViewerProgress(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon"}
	other := &DaemonConn{ID: "other"}
	third := &DaemonConn{ID: "third"}
	release, ok := h.acquireWorkspaceExplorer(dc, "owner", "session", true)
	if !ok {
		t.Fatal("first explorer refused")
	}
	defer release()
	if _, ok := h.acquireWorkspaceExplorer(dc, "other-owner", "different", false); ok {
		t.Fatal("per-daemon limit bypassed")
	}
	if _, ok := h.acquireWorkspaceExplorer(other, "owner", "different", false); ok {
		t.Fatal("per-person limit bypassed")
	}
	second, ok := h.acquireWorkspaceExplorer(other, "other-owner", "other-session", false)
	if !ok {
		t.Fatal("second explorer refused")
	}
	defer second()
	if _, ok := h.acquireWorkspaceExplorer(third, "third-owner", "third-session", false); ok {
		t.Fatal("global explorer limit bypassed")
	}
	viewer, ok := h.acquireWorkspaceFileRead(dc, "owner")
	if !ok {
		t.Fatal("explorer starved same-person viewer")
	}
	viewer()
	viewer, ok = h.acquireWorkspaceFileRead(third, "third-owner")
	if !ok {
		t.Fatal("explorer starved another viewer")
	}
	viewer()
}
func TestWorkspaceExplorerRejectsForgedReplies(t *testing.T) {
	req := protocol.WorkspaceExplorerRequest{Operation: "list", WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}}
	valid := protocol.WorkspaceExplorerResult{Operation: "list", WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, OK: true, Entries: []protocol.WorkspaceEntry{{Name: "file", Path: "file"}}}
	if !validExplorerResult(req, valid) {
		t.Fatal("valid result refused")
	}
	for _, mutate := range []func(*protocol.WorkspaceExplorerResult){
		func(r *protocol.WorkspaceExplorerResult) { r.Generation = 2 },
		func(r *protocol.WorkspaceExplorerResult) { r.LaunchGeneration = "old" },
		func(r *protocol.WorkspaceExplorerResult) { r.Entries[0].Path = "../file" },
		func(r *protocol.WorkspaceExplorerResult) { r.Entries[0].Path = "nested/file" },
		func(r *protocol.WorkspaceExplorerResult) { r.Entries = append(r.Entries, r.Entries[0]) },
	} {
		bad := valid
		bad.Entries = append([]protocol.WorkspaceEntry(nil), valid.Entries...)
		mutate(&bad)
		if validExplorerResult(req, bad) {
			t.Fatalf("forged reply accepted: %+v", bad)
		}
	}
}
func TestWorkspaceExplorerCancelOnNavigation(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	dc.SetSandboxClones(true)
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	dc.AuthorizeWorkspaceRuntime("session", protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.requestWorkspaceExplorer(ctx, dc, protocol.WorkspaceExplorerRequest{SessionID: "session", Operation: "list", WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}})
		done <- err
	}()
	var raw []byte
	select {
	case raw = <-dc.send:
	case <-time.After(time.Second):
		t.Fatal("no request")
	}
	var req protocol.WorkspaceExplorerRequest
	if json.Unmarshal(raw, &req) != nil {
		t.Fatal("invalid request")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("navigation did not cancel request")
	}
	select {
	case raw = <-dc.send:
	case <-time.After(time.Second):
		t.Fatal("no runtime cancellation")
	}
	var cancelled protocol.WorkspaceExplorerRequest
	_ = json.Unmarshal(raw, &cancelled)
	if cancelled.Type != "cancel_workspace_explorer" || cancelled.RequestID != req.RequestID || cancelled.SessionID != req.SessionID {
		t.Fatal("cancellation not scoped to original request")
	}
}

func TestWorkspaceExplorerHTTPBindingAndAuthorization(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterWorkspaceFileRoutes(fx.mux)
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 16)}
	dc.SetSandboxClones(true)
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: topicOwner, Runtime: "host", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "member", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "example-org/one"}}}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSessionWorkspace(t.Context(), fx.pool, fx.sess, manifest); err != nil {
		t.Fatal(err)
	}
	generation, err := db.BeginWorkspaceRuntime(t.Context(), fx.pool, fx.sess, "launch", 0)
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: generation}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: ref, ManifestDigest: manifest.Digest, Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}}
	prepared := protocol.WorkspaceRuntimePrepared{Type: "workspace_runtime_prepared", SessionID: fx.sess, Binding: binding}
	fx.api.hub.bindPreparedWorkspaceRuntime(t.Context(), fx.pool, dc, prepared)
	_, root, err := db.GetWorkspaceRuntime(t.Context(), fx.pool, fx.sess)
	if err != nil || root != nil {
		t.Fatal("unapproved root report registered itself")
	}
	if !dc.AuthorizeWorkspaceRuntime(fx.sess, ref) {
		t.Fatal("trusted runtime authorization failed")
	}
	fx.api.hub.bindPreparedWorkspaceRuntime(t.Context(), fx.pool, dc, prepared)
	_, root, err = db.GetWorkspaceRuntime(t.Context(), fx.pool, fx.sess)
	if err != nil || root == nil {
		t.Fatal("authorized root not registered", err)
	}
	dc.DesktopBinding = &desktopauth.Binding{DesktopID: dc.ID, OwnerID: topicOwner, Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}
	dc.DesktopGeneration = "11111111-1111-4111-8111-111111111111"
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=1,connection_generation=$3 WHERE id=$1`, dc.ID, topicOwner, dc.DesktopGeneration); err != nil {
		t.Fatal(err)
	}
	// Desktop browsing requires the owner; cluster administrator policy is separate.
	for _, token := range []string{fx.token(topicOwner, "human")} {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+"/workspace", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { defer close(done); fx.mux.ServeHTTP(&reviewDeadlineWriter{ResponseRecorder: rec}, req) }()
		var raw []byte
		select {
		case raw = <-dc.send:
		case <-time.After(5 * time.Second):
			t.Fatal("no authorized request")
		}
		var request protocol.WorkspaceExplorerRequest
		if json.Unmarshal(raw, &request) != nil || request.WorkspaceRuntimeRef != ref {
			t.Fatal("request not bound")
		}
		result := protocol.WorkspaceExplorerResult{Type: "workspace_explorer_result", RequestID: request.RequestID, SessionID: fx.sess, WorkspaceRuntimeRef: ref, Operation: "list", OK: true, Entries: []protocol.WorkspaceEntry{{Name: "member", Path: "member", Directory: true, Repository: true, MemberID: "one"}}}
		fx.api.hub.deliverWorkspaceExplorerResult(dc, result)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("reply did not complete")
		}
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "member") || rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("viewer response: %d %s", rec.Code, rec.Body)
		}
		<-dc.send // scoped cancellation after completion
	}
	for _, test := range []struct {
		sid, token string
		status     int
	}{
		{fx.sess, fx.token(topicOther, "human"), 403},
		{fx.sess, fx.token(topicOther, "human", "account.manage"), 403},
		{fx.sess, fx.token(topicOwner, "agent"), 403},
		{fx.privSess, fx.token(topicOwner, "human", "account.manage"), 404},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+test.sid+"/workspace", nil)
		req.Header.Set("Authorization", "Bearer "+test.token)
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(&reviewDeadlineWriter{ResponseRecorder: rec}, req)
		if rec.Code != test.status {
			t.Fatalf("auth status: %d %s", rec.Code, rec.Body)
		}
		select {
		case <-dc.send:
			t.Fatal("refused explorer request dispatched")
		default:
		}
	}

	// Connection replacement fences a reply even while the old hub socket stays live.
	dc.DesktopBinding = &desktopauth.Binding{DesktopID: dc.ID, OwnerID: topicOwner, Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}
	dc.DesktopGeneration = "11111111-1111-4111-8111-111111111111"
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=1,connection_generation=$3 WHERE id=$1`, dc.ID, topicOwner, dc.DesktopGeneration); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkspaceDesktop(t.Context(), fx.pool, dc, fx.sess); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+"/workspace", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); fx.mux.ServeHTTP(&reviewDeadlineWriter{ResponseRecorder: rec}, req) }()
	var request protocol.WorkspaceExplorerRequest
	select {
	case raw := <-dc.send:
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no desktop request")
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET connection_generation='22222222-2222-4222-8222-222222222222' WHERE id=$1`, dc.ID); err != nil {
		t.Fatal(err)
	}
	fx.api.hub.deliverWorkspaceExplorerResult(dc, protocol.WorkspaceExplorerResult{Type: "workspace_explorer_result", RequestID: request.RequestID, SessionID: fx.sess, WorkspaceRuntimeRef: ref, Operation: "list", OK: true})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stale reply did not complete")
	}
	if rec.Code != 409 {
		t.Fatalf("replaced desktop delivered workspace: %d %s", rec.Code, rec.Body)
	}
	<-dc.send
	// Stale desktops cannot bind a prepared root after a generation replacement.
	next, err := db.BeginWorkspaceRuntime(t.Context(), fx.pool, fx.sess, "launch", generation)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Binding.Generation = next
	if !dc.AuthorizeWorkspaceRuntime(fx.sess, prepared.Binding.WorkspaceRuntimeRef) {
		t.Fatal("authorize replacement")
	}
	fx.api.hub.bindPreparedWorkspaceRuntime(t.Context(), fx.pool, dc, prepared)
	_, staleRoot, err := db.GetWorkspaceRuntime(t.Context(), fx.pool, fx.sess)
	if err != nil || staleRoot != nil {
		t.Fatal("stale desktop bound prepared root", err)
	}
	dc.DesktopGeneration = "22222222-2222-4222-8222-222222222222"
	fx.api.hub.bindPreparedWorkspaceRuntime(t.Context(), fx.pool, dc, prepared)
	generation = next

	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=2 WHERE id=$1`, dc.ID, topicOther); err != nil {
		t.Fatal(err)
	}
	called := false
	err = withWorkspaceDesktopFence(t.Context(), fx.pool, dc, fx.sess, func(context.Context) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("reassigned desktop retained workspace authority")
	}
	// Ownership/runtime changes invalidate the old manifest even while the old
	// authenticated connection remains alive.
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, fx.sess, topicOther); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadExplorerBinding(t.Context(), fx.pool, fx.sess); err == nil {
		t.Fatal("owner change retained old workspace authority")
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=$2,runtime='cluster' WHERE id=$1`, fx.sess, topicOwner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadExplorerBinding(t.Context(), fx.pool, fx.sess); err == nil {
		t.Fatal("runtime change retained old root")
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	// A new incarnation clears the binding; stale data cannot be served from the
	// immutable manifest's old root (even with a still-current daemon socket).
	if _, err := db.BeginWorkspaceRuntime(t.Context(), fx.pool, fx.sess, "launch", generation); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadExplorerBinding(t.Context(), fx.pool, fx.sess); err == nil {
		t.Fatal("pending replacement used legacy root")
	}
}

func TestWorkspaceFileFolderWithoutManifestRefusesLegacyFallback(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterWorkspaceFileRoutes(fx.mux)
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 4)}
	dc.SetSandboxClones(true)
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET workspace_kind='folder' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+"/workspace-file?path=README.md", nil)
	request.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	response := httptest.NewRecorder()
	fx.mux.ServeHTTP(response, request)
	if response.Code != 409 {
		t.Fatalf("folder used scalar file reader: %d", response.Code)
	}
	select {
	case <-dc.send:
		t.Fatal("folder request dispatched without manifest")
	default:
	}
}

func TestWorkspaceExplorerPreparedRootWithinRuntimeFence(t *testing.T) {
	fx := newTopicFx(t)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	raw, _, err := db.MintRuntimeLaunch(t.Context(), fx.pool, fx.sess, fx.sessTok, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.ValidateRuntimeToken(t.Context(), fx.pool, raw)
	if err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{ID: topicDaemonID, RuntimeTokenID: token.ID, BoundSessionID: fx.sess, send: make(chan []byte, 8)}
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: topicOwner, Runtime: "cluster", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "member", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "example-org/one"}}}
	if err := db.InsertSessionWorkspace(t.Context(), fx.pool, fx.sess, manifest); err != nil {
		t.Fatal(err)
	}
	generation, err := db.BeginWorkspaceRuntime(t.Context(), fx.pool, fx.sess, "launch", 0)
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: generation}
	if !dc.AuthorizeWorkspaceRuntime(fx.sess, ref) {
		t.Fatal("authorize")
	}
	prepared := protocol.WorkspaceRuntimePrepared{Type: "workspace_runtime_prepared", SessionID: fx.sess, Binding: protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: ref, ManifestDigest: "digest", Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}}}
	frame, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	// This is the same fence held by daemonReadPump before calling the handler.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err = db.WithRuntimeLaunchFence(ctx, fx.pool, fx.sess, func(fenced context.Context) error {
		if !handleDaemonFrame(fenced, fx.api.hub, dc, fx.pool, frame, envelope{Type: prepared.Type, SessionID: fx.sess}) {
			t.Fatal("frame refused")
		}
		_, binding, err := db.GetWorkspaceRuntime(fenced, fx.pool, fx.sess)
		if err != nil {
			return err
		}
		if binding == nil || *binding != prepared.Binding {
			t.Fatal("prepared root not bound under existing fence")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceExplorerProtectedManifestIsNeverSerialized(t *testing.T) {
	for _, component := range []string{".env", ".ssh", ".git", "credentials.private"} {
		t.Run(component, func(t *testing.T) {
			fx := newTopicFx(t)
			fx.api.RegisterWorkspaceFileRoutes(fx.mux)
			dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 4)}
			fx.api.hub.Register(dc)
			fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
			if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
				t.Fatal(err)
			}
			manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: topicOwner, Runtime: "host", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "private-id", Name: component, Path: "member/" + component, SourceIdentity: "private-source", CommonDirectoryIdentity: "private-common", Destination: "example-org/private-member"}}}
			if err := db.InsertSessionWorkspace(t.Context(), fx.pool, fx.sess, manifest); err != nil {
				t.Fatal(err)
			}
			gen, err := db.BeginWorkspaceRuntime(t.Context(), fx.pool, fx.sess, "launch", 0)
			if err != nil {
				t.Fatal(err)
			}
			ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: gen}
			if err := db.BindWorkspaceRuntime(t.Context(), fx.pool, fx.sess, ref, protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: ref, ManifestDigest: "digest", Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}}); err != nil {
				t.Fatal(err)
			}
			dc.AuthorizeWorkspaceRuntime(fx.sess, ref)
			req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+"/workspace", nil)
			req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
			rec := httptest.NewRecorder()
			fx.mux.ServeHTTP(&reviewDeadlineWriter{ResponseRecorder: rec}, req)
			if rec.Code != 409 {
				t.Fatalf("protected binding response: %d %s", rec.Code, rec.Body)
			}
			for _, private := range []string{component, "private-id", "private-source", "private-common", "example-org/private-member", "\"members\"", "\"unavailable_members\"", "\"changed\"", "\"untracked\""} {
				if strings.Contains(rec.Body.String(), private) {
					t.Fatalf("protected metadata serialized: %s", rec.Body)
				}
			}
			select {
			case <-dc.send:
				t.Fatal("protected manifest dispatched")
			default:
			}
		})
	}
}

func TestWorkspaceExplorerStatusCodesBoundToPaths(t *testing.T) {
	req := protocol.WorkspaceExplorerRequest{Operation: "status"}
	valid := protocol.WorkspaceExplorerResult{Operation: "status", OK: true, Status: &protocol.WorkspaceStatus{Changed: 1, Paths: []string{"safe.txt"}, Files: []protocol.WorkspacePathStatus{{Path: "safe.txt", Code: "M"}}}}
	if !validExplorerResult(req, valid) {
		t.Fatal("valid file decorations refused")
	}
	for _, file := range []protocol.WorkspacePathStatus{{Path: "../other", Code: "M"}, {Path: "safe.txt", Code: "evil"}, {Path: "safe.txt", Code: "M", Directory: true}, {Path: "safe.txt", Code: "U"}} {
		bad := valid
		status := *valid.Status
		status.Files = []protocol.WorkspacePathStatus{file}
		bad.Status = &status
		if validExplorerResult(req, bad) {
			t.Fatalf("forged status code accepted: %+v", file)
		}
	}
	old := valid
	status := *valid.Status
	status.Files = nil
	old.Status = &status
	if !validExplorerResult(req, old) {
		t.Fatal("older status reply refused")
	}
}
