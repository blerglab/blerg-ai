package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReviewDiscoveryOverActualDesktopTransport(t *testing.T) {
	f := newPrivFixture(t)
	id := newUUID()
	token := "bdt_" + strings.Repeat("a", 64)
	desktopFakeCore(t, f.api, map[string]desktopauth.Binding{token: {DesktopID: id, OwnerID: privAcctA, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}})
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", f.hub.ServeDaemon(spawnTokenDaemonTok, f.pool))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := desktopConnect(t, srv.URL, token, id, "review-desktop", true)
	dc := desktopWait(t, f.hub, id)
	dc.DiscoveryVersion = 1
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	makeRepo := func(name string) {
		for _, dir := range []string{"objects", "refs"} {
			if err := os.MkdirAll(filepath.Join(root, name, ".git", dir), 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, name, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	makeRepo("one")
	makeRepo("two")
	dc.SetReposRoot(root)
	dc.WorkspaceCapabilities.ManifestVersion = 2
	dc.WorkspaceCapabilities.LinuxHost = true
	dc.WorkspaceCapabilities.Sandbox = true
	dc.WorkspaceCapabilities.Cluster = true
	dc.RecoveryReadyCapable = true
	dc.SetAvailableEngines([]string{"claude", "codex"})
	dc.SetLocalEngines([]string{"codex"})
	service := discovery.NewOwned(func() string { return root }, id)
	spawned := make(chan protocol.SpawnSession, 4)
	got := make(chan struct{}, 16)
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
				got <- struct{}{}
				_ = c.WriteJSON(service.Handle(t.Context(), req))
			}
			if header.Type == "spawn_session" {
				var spawn protocol.SpawnSession
				_ = json.Unmarshal(raw, &spawn)
				spawned <- spawn
			}
		}
	}()

	browser := f.tok(t, "human", privAcctA, "session.start")
	r := httptest.NewRequest("GET", "/api/workspace/discovery?daemon_id="+id+"&path=.", nil)
	r.Header.Set("Authorization", "Bearer "+browser)
	ctx, cancel := context.WithTimeout(r.Context(), 7*time.Second)
	defer cancel()
	w := &reviewDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	start := time.Now()
	f.api.HandleWorkspaceDiscovery(w, r.WithContext(ctx))
	received := false
	select {
	case <-got:
		received = true
	default:
	}
	t.Logf("actual socket: HTTP=%d elapsed=%v request_reached_daemon_before_HTTP_return=%v body=%s", w.Code, time.Since(start), received, w.Body.String())
	if w.Code != 200 {
		t.Fatalf("owner discovery should succeed through real transport; got %d", w.Code)
	}
	var listed struct {
		Page protocol.DiscoveryPage `json:"page"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	selection := protocol.WorkspaceSelectionRequest{DaemonID: id, OwnershipRevision: 1, ConnectionGeneration: dc.DesktopGeneration, RootIdentity: listed.Page.RootIdentity}
	for _, entry := range listed.Page.Entries {
		selection.Members = append(selection.Members, protocol.SelectionRef{Path: entry.Path, SourceIdentity: entry.SourceIdentity})
	}
	post := func(url string, body any, handler http.HandlerFunc) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", url, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+browser)
		w := &reviewDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		handler(w, r)
		return w.ResponseRecorder
	}
	preview := post("/api/workspace/preview", map[string]any{"runtime": "host", "selection": selection}, f.api.HandleWorkspacePreview)
	if preview.Code != 200 {
		t.Fatalf("real preview: %d %s", preview.Code, preview.Body.String())
	}
	launch := post("/api/sessions", map[string]any{"runtime": "daemon", "selection": selection, "kind": "agent", "initial_prompt": "Review"}, f.api.HandlePostSessions)
	if launch.Code != 202 {
		t.Fatalf("real launch: %d %s", launch.Code, launch.Body.String())
	}
	var parentSpawn protocol.SpawnSession
	select {
	case parentSpawn = <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("real spawn did not reach desktop")
	}
	parent, err := db.GetSession(t.Context(), f.pool, parentSpawn.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	child, problem := f.api.startChild(t.Context(), parent, childStart{Repos: []string{parentSpawn.Workspace.Members[0].ID}, Engine: "codex", Prompt: "Review member"}, "request:review", "real-child-key")
	if problem != nil || child.SessionID == "" {
		t.Fatalf("real child: %v", problem)
	}
	select {
	case msg := <-spawned:
		if msg.Workspace == nil || len(msg.Workspace.Members) != 1 {
			t.Fatal("child lost immutable subset")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("real child spawn did not reach desktop")
	}
	if err := os.Rename(filepath.Join(root, "one"), filepath.Join(root, "old-one")); err != nil {
		t.Fatal(err)
	}
	makeRepo("one")
	if stale := post("/api/sessions", map[string]any{"runtime": "daemon", "selection": selection, "kind": "agent"}, f.api.HandlePostSessions); stale.Code != 409 {
		t.Fatal("same-name replacement launched")
	}

}

type reviewOwnershipWriter struct {
	*httptest.ResponseRecorder
	before func()
}

// The positive snapshot probe models a socket with deadline support.
func (w *reviewOwnershipWriter) SetWriteDeadline(time.Time) error { return nil }

func (w *reviewOwnershipWriter) WriteHeader(code int) {
	if w.before != nil {
		f := w.before
		w.before = nil
		f()
	}
	w.ResponseRecorder.WriteHeader(code)
}
func TestReviewHTTPResultRechecksOwnershipThroughWrite(t *testing.T) {
	f := newPrivFixture(t)
	token := f.tok(t, "human", privAcctA, "session.start")
	req := httptest.NewRequest("GET", "/api/runner/sessions/"+f.pubSession+"/result", nil)
	req.SetPathValue("id", f.pubSession)
	req.Header.Set("Authorization", "Bearer "+token)
	probed := false
	w := &reviewOwnershipWriter{ResponseRecorder: httptest.NewRecorder()}
	w.before = func() {
		tx, err := f.pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(t.Context()) }()
		var acquired bool
		if err := tx.QueryRow(t.Context(), `SELECT pg_try_advisory_xact_lock(hashtextextended($1::uuid::text,876434))`, f.dc.ID).Scan(&acquired); err != nil {
			t.Fatal(err)
		}
		if acquired {
			t.Error("result response write escaped persisted ownership fence")
		}
		probed = true
	}
	f.api.HandleRunnerResult(w, req)
	if !probed || w.Code != 200 {
		t.Fatalf("result response: %d", w.Code)
	}

}

func TestReviewLegacyLaunchStillAcceptsBareName(t *testing.T) {
	f := newPrivFixture(t)
	f.dc.DiscoveryVersion = 1
	token := f.tok(t, "human", privAcctA, "session.start")
	req := httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{"daemon_id":"`+f.dc.ID+`","repo":"unverified-repo","runtime":"daemon","kind":"agent"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.api.HandlePostSessions(w, req)
	t.Logf("legacy bare launch HTTP=%d body=%s", w.Code, w.Body.String())
	if w.Code == 202 {
		t.Fatal("bare name launch accepted without source identity or discovery")
	}
}

func TestReviewPublicHistoryMustNotBorrowDesktopOrigin(t *testing.T) {
	f := newTopicFx(t)
	f.post(f.sess, f.sessTok, topicStart("Review work"))
	f.post(f.sess, f.sessTok, map[string]any{"action": "done", "commits": []map[string]string{{"sha": "1a2b3c4"}}})
	dc := &DaemonConn{ID: topicDaemonID}
	dc.SetRepoOrigins(map[string]protocol.RepoOrigin{"app": {Provider: "github", FullName: "private-person-a/private-desktop-repo"}}, nil)
	f.api.hub.Register(dc)
	foreign := f.get(f.sess, f.token(topicOther, "human"), "")
	t.Logf("foreign history: HTTP=%d ETag=%s body=%s", foreign.Code, foreign.Header().Get("ETag"), foreign.Body.String())
	dc.SetRepoOrigins(map[string]protocol.RepoOrigin{"app": {Provider: "github", FullName: "private-person-a/changed-desktop-repo"}}, nil)
	second := f.get(f.sess, f.token(topicOther, "human"), "")
	t.Logf("foreign ETag changed with private origin: %v", foreign.Header().Get("ETag") != second.Header().Get("ETag"))
	if strings.Contains(foreign.Body.String(), "private-person-a") {
		t.Fatal("another person's desktop origin exposed through automatic commit URL")
	}
}

func TestReviewCommitDiffMustCheckExactDesktopOwner(t *testing.T) {
	for _, mode := range []string{"foreign-admin", "unknown-owner"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommitFx(t)
			token := f.token(topicOther, "human", "account.manage")
			if mode == "unknown-owner" {
				if _, err := f.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=NULL WHERE id=$1`, f.dc.ID); err != nil {
					t.Fatal(err)
				}
				token = f.token(topicOwner, "human")
			}
			for _, suffix := range []string{"", "/diff"} {
				w := f.refused("/api/sessions/"+f.sess+"/commits/0123456"+suffix, token)
				if w.Code == 200 {
					t.Fatal("foreign or unknown desktop request dispatched")
				}
			}
		})
	}
}

func TestReviewClusterChildUsesRegisteredWorkspaceConsumer(t *testing.T) {
	f := newPrivFixture(t)
	clusterID := newUUID()
	if err := db.UpsertDaemon(t.Context(), f.pool, clusterID, "review-cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster',daemon_id=$2 WHERE id=$1`, f.pubSession, clusterID); err != nil {
		t.Fatal(err)
	}
	f.hub.SetSessionOwner(f.pubSession, clusterID)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: privAcctA, Runtime: "cluster", LaunchGeneration: newUUID(), Root: protocol.WorkspaceRoot{Inode: 1}, Members: []protocol.WorkspaceMember{{ID: "api", Name: "api", Path: "projects/api", SourceIdentity: "s-api", CommonDirectoryIdentity: "c-api", Destination: "github:org/api"}, {ID: "web", Name: "web", Path: "projects/web", SourceIdentity: "s-web", CommonDirectoryIdentity: "c-web", Destination: "github:org/web"}}}
	m.Digest = workspaceselection.Digest(m)
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.pubSession, m); err != nil {
		t.Fatal(err)
	}
	parent, err := db.GetSession(t.Context(), f.pool, f.pubSession)
	if err != nil {
		t.Fatal(err)
	}
	consumer := &workspaceClusterFake{}
	f.api.SetWorkspaceClusterConsumer(consumer)
	for i, selection := range []childStart{{Repos: []string{"api"}}, {Repos: []string{"api", "web"}}, {AllRepos: true}} {
		selection.Prompt = "Review selected members"
		selection.Engine = "codex"
		selection.Runtime = "cluster"
		key := fmt.Sprintf("cluster-review-%d", i)
		child, problem := f.api.startChild(t.Context(), parent, selection, "request:review", key)
		if problem != nil || !consumer.prepared || consumer.manifest == nil {
			t.Fatalf("cluster subset handoff: %v", problem)
		}
		expected := 1
		if i > 0 {
			expected = 2
		}
		if len(consumer.manifest.Members) != expected {
			t.Fatal("consumer did not receive exact subset")
		}
		consumer.prepared = false
		replay, problem := f.api.startChild(t.Context(), parent, selection, "request:review", key)
		if problem != nil || !replay.Replayed || replay.SessionID != child.SessionID || consumer.prepared {
			t.Fatal("child idempotency did not preserve existing launch")
		}
	}
	if _, problem := f.api.startChild(t.Context(), parent, childStart{Repos: []string{"foreign"}, Prompt: "review", Engine: "codex", Runtime: "cluster"}, "request:review", "foreign-key"); problem == nil || consumer.prepared {
		t.Fatal("foreign member reached cluster consumer")
	}

}

func TestReviewCommitRechecksConnectionAfterRPC(t *testing.T) {
	for _, change := range []string{"owner", "generation"} {
		t.Run(change, func(t *testing.T) {
			f := newCommitFx(t)
			w := f.answered("/api/sessions/"+f.sess+"/commits/0123456", f.token(topicOwner, "human"), func(req protocol.ReadCommit) protocol.CommitResult {
				if change == "owner" {
					if _, err := db.AssignDesktop(t.Context(), f.pool, f.dc.ID, topicOther, 1); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := f.pool.Exec(t.Context(), `UPDATE daemons SET connection_generation=gen_random_uuid() WHERE id=$1`, f.dc.ID); err != nil {
						t.Fatal(err)
					}
				}
				return okCommit(req)
			})
			if w.Code != 409 {
				t.Fatalf("obsolete commit delivered: %d", w.Code)
			}
		})
	}
}
func TestReviewHistoryAndChecklistOwnerScope(t *testing.T) {
	f := newWorkFx(t)
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 16)}
	f.api.hub.Register(dc)
	fixtureOwnDesktop(t, f.pool, dc, topicOwner)
	dc.SetRepoOrigins(map[string]protocol.RepoOrigin{"app": {Provider: "github", FullName: "private-origin/source"}}, nil)
	f.post(f.sess, f.sessTok, topicStart("Work"))
	f.post(f.sess, f.sessTok, map[string]any{"action": "done", "commits": []map[string]string{{"sha": "1234567"}}})
	f.postWork("checklist", f.sess, f.sessTok, map[string]any{"op": "set", "items": []string{"Work"}})
	f.postWork("checklist", f.sess, f.sessTok, map[string]any{"op": "tick", "index": 1, "commits": []string{"1234567"}})
	for _, get := range []func(string, string, string) *httptest.ResponseRecorder{f.get, f.getLive} {
		owner := get(f.sess, f.token(topicOwner, "human"), "")
		if owner.Code != 200 || !strings.Contains(owner.Body.String(), "private-origin") {
			t.Fatalf("owner mapping missing: %d %s", owner.Code, owner.Body.String())
		}
		for _, tok := range []string{f.token(topicOther, "human"), f.token(topicOther, "human", "account.manage")} {
			before := get(f.sess, tok, "")
			dc.SetRepoOrigins(map[string]protocol.RepoOrigin{"app": {Provider: "github", FullName: "private-origin/changed"}}, nil)
			after := get(f.sess, tok, "")
			if strings.Contains(before.Body.String(), "private-origin") || before.Header().Get("ETag") != after.Header().Get("ETag") {
				t.Fatal("foreign enrichment influenced payload or ETag")
			}
		}
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=NULL WHERE id=$1`, dc.ID); err != nil {
		t.Fatal(err)
	}
	for _, get := range []func(string, string, string) *httptest.ResponseRecorder{f.get, f.getLive} {
		if w := get(f.sess, f.token(topicOwner, "human", "account.manage"), ""); strings.Contains(w.Body.String(), "private-origin") {
			t.Fatal("unknown source enriched")
		}
	}
}
func TestReviewDesktopRemovalAcrossHubs(t *testing.T) {
	f := newPrivFixture(t)
	other := NewHub()
	_ = NewAPI(other, f.pool, "fixture", nil, "")
	other.Register(f.dc)
	bc := &BrowserConn{AccountID: privAcctA, send: make(chan []byte, 256), ctx: t.Context()}
	before, err := other.desktopScopeRows(t.Context(), privAcctA)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithDesktopFence(t.Context(), f.pool, f.dc.ID, func(ctx context.Context) error {
		_, err := db.AssignDesktop(ctx, f.pool, f.dc.ID, privAcctB, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ctx, done := context.WithCancel(t.Context())
	defer done()
	go other.watchDesktopScope(ctx, bc, f.pool, before)

	var hint map[string]any
	select {
	case raw := <-bc.send:
		_ = json.Unmarshal(raw, &hint)
	case <-time.After(5 * time.Second):
		t.Fatal("remote replica did not remove cached desktop")
	}
	if hint["type"] != "desktop_inventory_invalidated" || len(hint) != 1 {
		t.Fatal("missing metadata-free removal")
	}
	var snapshot map[string]any
	_ = json.Unmarshal(<-bc.send, &snapshot)
	raw, _ := json.Marshal(snapshot)
	if strings.Contains(string(raw), f.dc.ReposRoot) && f.dc.ReposRoot != "" {
		t.Fatal("removed metadata survived snapshot")
	}
}
func TestReviewOwnershipEventMatchesBrowserContract(t *testing.T) {
	raw, err := os.ReadFile("../../frontend/src/test/desktopOwnership.json")
	if err != nil {
		t.Fatal(err)
	}
	f := newPrivFixture(t)
	bc := &BrowserConn{ID: newUUID(), AccountID: privAcctA, send: make(chan []byte, 64), ctx: t.Context()}
	f.hub.RegisterBrowser(bc)
	defer f.hub.UnregisterBrowser(bc.ID)
	f.api.desktopOwnershipChanged(f.dc.ID, privAcctA, privAcctB, 2)
	var expected, actual map[string]any
	_ = json.Unmarshal(raw, &expected)
	expected["desktop_id"] = f.dc.ID
	select {
	case raw := <-bc.send:
		_ = json.Unmarshal(raw, &actual)
	case <-time.After(time.Second):
		t.Fatal("missing removal event")
	}
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("server and browser removal contracts differ: %v", actual)
	}
}

func TestReviewEveryHTTPDesktopSnapshotFencesItsWrite(t *testing.T) {
	f := newPrivFixture(t)
	for _, handler := range []http.HandlerFunc{f.api.HandleRunnerResult, f.api.HandleRunnerStatus, f.api.HandleRunnerEvents, f.api.HandleGetCapabilities, f.api.HandleGetAgentEvents, f.api.HandleGetTopics, f.api.HandleGetLive} {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetPathValue("id", f.pubSession)
		if reflect.ValueOf(handler).Pointer() == reflect.ValueOf(f.api.HandleGetAgentEvents).Pointer() {
			r.Header.Set("Authorization", "Bearer "+f.api.daemonToken)
		} else {
			r.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctA, "session.start"))
		}
		probed := false
		w := &reviewOwnershipWriter{ResponseRecorder: httptest.NewRecorder(), before: func() {
			tx, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			var acquired bool
			if err := tx.QueryRow(t.Context(), `SELECT pg_try_advisory_xact_lock(hashtextextended($1::uuid::text,876434))`, f.dc.ID).Scan(&acquired); err != nil {
				t.Fatal(err)
			}
			if acquired {
				t.Error("HTTP snapshot write was not fenced")
			}
			probed = true
		}}
		handler(w, r)
		if !probed || w.Code != 200 {
			t.Fatalf("snapshot: %d %s", w.Code, w.Body.String())
		}
	}
}
func TestReviewBareNamesRefusedAcrossLaunchEntrances(t *testing.T) {
	f := newPrivFixture(t)
	f.dc.DiscoveryVersion = 1
	f.dc.RecoveryReadyCapable = true
	f.dc.SetAvailableEngines([]string{"codex"})
	f.dc.SetLocalEngines([]string{"codex"})
	principal := runnerPrincipal{Kind: "human", Sub: privAcctA, Caps: []string{"session.start"}, Sid: testSID}
	// A contract start names its repository; the runner resolves it through the desktop's own
	// discovery into a bound selection (contract_workspace.go) and never launches the bare name.
	// This desktop never answers discovery, so the start is refused and nothing is spawned.
	if _, err := f.api.StartSession(t.Context(), principal, runnerStartRequest{Runtime: "daemon", DaemonID: f.dc.ID, Repo: "unverified", Engine: "codex", Prompt: "review"}, "bare-runner"); err == nil || err.Status < 400 {
		t.Fatal("runner accepted bare source")
	}
	for drained := false; !drained; {
		select {
		case raw := <-f.dc.send:
			if strings.Contains(string(raw), `"spawn_session"`) {
				t.Fatalf("runner spawned a bare source: %s", raw)
			}
		default:
			drained = true
		}
	}
	parent, err := db.GetSession(t.Context(), f.pool, f.pubSession)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.api.startChild(t.Context(), parent, childStart{Repo: "unverified", Runtime: "daemon", Engine: "codex", Prompt: "review"}, "request:review", "bare-child"); err == nil || err.Status != 409 || !strings.Contains(err.Message, "source references") {
		t.Fatal("child accepted bare source")
	}
}

type reviewFrameWriter struct {
	*httptest.ResponseRecorder
	probe func()
}

func (w *reviewFrameWriter) Write(b []byte) (int, error) {
	w.probe()
	return w.ResponseRecorder.Write(b)
}
func TestReviewPollingSSEFencesEveryEventAndTerminalResult(t *testing.T) {
	f := newPrivFixture(t)
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), f.pool, f.pubSession, "stopped", &now, systemEnd(db.EndReasonProcessExited)); err != nil {
		t.Fatal(err)
	}
	writes := 0
	w := &reviewFrameWriter{ResponseRecorder: httptest.NewRecorder(), probe: func() {
		writes++
		tx, err := f.pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(t.Context()) }()
		var acquired bool
		if err := tx.QueryRow(t.Context(), `SELECT pg_try_advisory_xact_lock(hashtextextended($1::uuid::text,876434))`, f.dc.ID).Scan(&acquired); err != nil {
			t.Fatal(err)
		}
		if acquired {
			t.Error("SSE desktop frame escaped ownership fence")
		}
	}}
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("id", f.pubSession)
	req.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctA, "session.start"))
	f.api.HandleRunnerEventStream(w, req)
	if writes < 2 || !strings.Contains(w.Body.String(), "event: end") {
		t.Fatal("event and terminal result were not delivered")
	}
}

type reviewDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *reviewDeadlineWriter) SetWriteDeadline(d time.Time) error { w.deadline = d; return nil }
func TestReviewCommitResponseCannotExtendTheFenceDeadline(t *testing.T) {
	f := newCommitFx(t)
	deadline := time.Now().Add(5 * time.Second)
	ctx, done := context.WithDeadline(t.Context(), deadline)
	defer done()
	req := httptest.NewRequest("GET", "/api/sessions/"+f.sess+"/commits/0123456", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+f.token(topicOwner, "human"))
	w := &reviewDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	finished := make(chan struct{})
	go func() { defer close(finished); f.mux.ServeHTTP(w, req) }()
	select {
	case raw := <-f.dc.send:
		var request protocol.ReadCommit
		_ = json.Unmarshal(raw, &request)
		reply := okCommit(request)
		reply.Type = "commit_result"
		reply.RequestID = request.RequestID
		reply.SessionID = f.sess
		f.api.hub.deliverCommitResult(f.dc, reply)
	case <-ctx.Done():
		t.Fatal("commit was not dispatched")
	}
	<-finished
	if w.Code != 200 || w.deadline.IsZero() || w.deadline.After(deadline) {
		t.Fatal("commit write extended its ownership fence deadline")
	}
}
