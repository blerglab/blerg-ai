//go:build linux || darwin

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReviewBResultCarriesMemberOutcomes(t *testing.T) {
	f := newTopicFx(t)
	_, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster',repo='',project_path='/workspace/projects' WHERE id=$1`, f.sess)
	if err != nil {
		t.Fatal(err)
	}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "cluster", OwnerID: topicOwner, LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{
		{ID: "one", Name: "same", Path: "one", SourceIdentity: "one-source", CommonDirectoryIdentity: "one-common", Destination: "org/one", Provider: "github"},
		{ID: "two", Name: "same", Path: "two", SourceIdentity: "two-source", CommonDirectoryIdentity: "two-common", Destination: "org/two", Provider: "github"},
	}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.sess, m); err != nil {
		t.Fatal(err)
	}
	results := []protocol.WorkspaceMemberResult{
		{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "one", LaunchGeneration: "launch"}, Head: "saved-one", Branch: "wip/one"},
		{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "two", LaunchGeneration: "launch"}, Head: "saved-two", Branch: "wip/two", Pending: true, PushError: "rejected"},
	}
	if err := db.SetWorkspaceResults(t.Context(), f.pool, f.sess, "launch", results); err != nil {
		t.Fatal(err)
	}
	res, err := f.api.buildSessionResult(t.Context(), f.sess, topicOwner)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var got []protocol.WorkspaceMemberResult
	if encoded, err := json.Marshal(body["workspace_results"]); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].MemberID != "one" || got[1].MemberID != "two" || got[1].PushError != "rejected" || !got[1].Pending || got[1].LaunchGeneration != "launch" || res.Repo != "" || res.Branch != "" || res.GitURL != "" {
		t.Fatalf("member outcomes lost: %s", raw)
	}
	if _, ok := body["workspace_results"]; !ok {
		t.Fatalf("result omits all immutable member outcomes; branch=%q", res.Branch)
	}
}

func TestReviewBInventoryWriteReleasesDesktopFence(t *testing.T) {
	for _, which := range []string{"daemons", "repos"} {
		t.Run(which, func(t *testing.T) {
			f := newPrivFixture(t)
			dc := workspaceOwnedDesktop(t, f.hub, f.pool, privAcctA, "desktop")
			dc.SetCheckedOutRepos([]string{"repo"})
			r := httptest.NewRequest("GET", "/api/"+which, nil)
			r.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctA, "session.start"))
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			r = r.WithContext(ctx)
			socket, peer := net.Pipe()
			defer socket.Close()
			defer peer.Close()
			w := &round2StalledWriter{header: make(http.Header), conn: socket, entered: make(chan struct{})}
			done := make(chan struct{})
			go func() {
				defer close(done)
				if which == "repos" {
					f.api.HandleGetRepos(w, r)
				} else {
					f.api.HandleGetDaemons(w, r)
				}
			}()
			select {
			case <-w.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("no response")
			}
			<-ctx.Done()
			select {
			case <-done:
			case <-time.After(1200 * time.Millisecond):
				tx, err := f.pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				var acquired bool
				err = tx.QueryRow(t.Context(), `SELECT pg_try_advisory_xact_lock(hashtextextended($1::uuid::text,876434))`, dc.ID).Scan(&acquired)
				_ = tx.Rollback(t.Context())
				_ = peer.Close()
				<-done
				if err != nil {
					t.Fatal(err)
				}
				t.Fatalf("inventory write ignored request cancellation and held desktop fence: held=%v", !acquired)
			}
		})
	}
}

func TestReviewBExplorerCannotReadForeignDesktopAsAdmin(t *testing.T) {
	for _, op := range []string{"list", "search", "status"} {
		for _, scenario := range []string{"foreign-admin", "unknown", "owner", "reassigned", "recipient-changed", "cluster-admin"} {
			t.Run(op+"/"+scenario, func(t *testing.T) {
				fx := newTopicFx(t)
				fx.api.RegisterWorkspaceFileRoutes(fx.mux)
				root := t.TempDir()
				if err := os.Mkdir(filepath.Join(root, "member"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "member", "private-owner-file.txt"), []byte("private"), 0600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("git", "-C", filepath.Join(root, "member"), "init", "-q")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git init: %v %s", err, out)
				}
				dc := workspaceOwnedDesktop(t, fx.api.hub, fx.pool, topicOwner, "owned-desktop")
				if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='daemon' WHERE id=$1`, fx.sess, dc.ID); err != nil {
					t.Fatal(err)
				}
				fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
				m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: topicOwner, Runtime: "host", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "private-owner-repo", Path: "member", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
				if scenario == "cluster-admin" {
					m.Runtime = "cluster"
					dc.DesktopBinding = nil
					dc.BoundSessionID = fx.sess
					if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster' WHERE id=$1`, fx.sess); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "unknown" {
					if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=NULL WHERE id=$1`, dc.ID); err != nil {
						t.Fatal(err)
					}
				}
				if err := db.InsertSessionWorkspace(t.Context(), fx.pool, fx.sess, m); err != nil {
					t.Fatal(err)
				}
				gen, err := db.BeginWorkspaceRuntime(t.Context(), fx.pool, fx.sess, m.LaunchGeneration, 0)
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(root)
				if err != nil {
					t.Fatal(err)
				}
				st := info.Sys().(*syscall.Stat_t)
				device := uint64(st.Dev) //nolint:unconvert // Darwin st_dev is signed; Linux already uses uint64.
				b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: gen}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Device: device, Inode: st.Ino}}
				if err := db.BindWorkspaceRuntime(t.Context(), fx.pool, fx.sess, b.WorkspaceRuntimeRef, b); err != nil {
					t.Fatal(err)
				}
				dc.AuthorizeWorkspaceRuntime(fx.sess, b.WorkspaceRuntimeRef)
				reader, err := daemon.NewLocalExplorer(root, m, b, func() bool { return true })
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				req := httptest.NewRequest("GET", "/api/sessions/"+fx.sess+"/workspace?op="+op+"&path=member&member=one&filter=private", nil)
				viewer := topicOwner
				if scenario == "foreign-admin" || scenario == "cluster-admin" {
					viewer = topicOther
				}
				req.Header.Set("Authorization", "Bearer "+fx.token(viewer, "human", "account.manage"))
				rec := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { defer close(done); fx.mux.ServeHTTP(&reviewDeadlineWriter{ResponseRecorder: rec}, req) }()
				dispatched := false
				select {
				case raw := <-dc.send:
					dispatched = true
					var q protocol.WorkspaceExplorerRequest
					if err := json.Unmarshal(raw, &q); err != nil {
						t.Fatal(err)
					}
					if scenario == "reassigned" {
						changed := make(chan error, 1)
						go func() {
							changed <- db.WithDesktopFence(t.Context(), fx.pool, dc.ID, func(ctx context.Context) error {
								_, err := db.AssignDesktop(ctx, fx.pool, dc.ID, topicOther, 1)
								return err
							})
						}()
						select {
						case err := <-changed:
							if err != nil {
								t.Fatal(err)
							}
						case <-time.After(time.Second):
							t.Fatal("RPC wait held desktop fence")
						}
					}
					if scenario == "recipient-changed" {
						if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, fx.sess, topicOther); err != nil {
							t.Fatal(err)
						}
					}
					fx.api.hub.deliverWorkspaceExplorerResult(dc, reader.Explore(t.Context(), q))
					<-done
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("handler stuck")
				}
				if scenario == "owner" || scenario == "cluster-admin" {
					if rec.Code != 200 || !dispatched {
						t.Fatalf("authorized read refused: %d %s", rec.Code, rec.Body)
					}
					return
				}
				if scenario == "reassigned" || scenario == "recipient-changed" {
					if rec.Code == 200 || strings.Contains(rec.Body.String(), "private-owner") {
						t.Fatalf("stale delivery: %d %s", rec.Code, rec.Body)
					}
					return
				}
				if rec.Code == 200 || dispatched {
					t.Fatalf("foreign administrator received desktop explorer: HTTP %d dispatched=%v body=%s", rec.Code, dispatched, rec.Body.String())
				}
			})
		}
	}
}

// A 304 still has bytes to flush. Its headers must leave under the same short
// authority fence as an ordinary inventory response.
type inventoryFlushWriter struct{ round2StalledWriter }

func (w *inventoryFlushWriter) FlushError() error { _, err := w.Write([]byte("headers")); return err }
func TestP6Inventory304AndUnsupportedWriter(t *testing.T) {
	f := newPrivFixture(t)
	dc := workspaceOwnedDesktop(t, f.hub, f.pool, privAcctA, "desktop")
	dc.SetCheckedOutRepos([]string{"repo"})
	for _, which := range []string{"repos", "daemons"} {
		t.Run(which, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/"+which, nil)
			r.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctA, "session.start"))
			handler := f.api.HandleGetRepos
			if which == "daemons" {
				handler = f.api.HandleGetDaemons
			}
			unsupported := httptest.NewRecorder()
			handler(unsupported, r)
			if unsupported.Code != 409 || strings.Contains(unsupported.Body.String(), dc.ID) || strings.Contains(unsupported.Body.String(), "checked_out") {
				t.Fatal("unsupported writer received inventory")
			}
			r.Header.Set("If-None-Match", "*")
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			socket, peer := net.Pipe()
			defer socket.Close()
			defer peer.Close()
			w := &inventoryFlushWriter{round2StalledWriter: round2StalledWriter{header: make(http.Header), conn: socket, entered: make(chan struct{})}}
			done := make(chan struct{})
			go func() { handler(w, r.WithContext(ctx)); close(done) }()
			select {
			case <-done:
			case <-time.After(1500 * time.Millisecond):
				t.Fatal("inventory flush held fence")
			}
			if w.writes != 1 {
				t.Fatalf("partial response retried: %d", w.writes)
			}
			if err := db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestP6MemberResultsRecipientAndDelivery(t *testing.T) {
	for _, endpoint := range []string{"result", "mcp-builder", "polling-sse", "live-sse", "callback", "queued"} {
		for _, state := range []string{"owner", "foreign", "unknown", "reassigned", "runtime-changed"} {
			t.Run(endpoint+"/"+state, func(t *testing.T) {
				f := newPrivFixture(t)
				sid := f.pubSession
				dc := f.dc
				if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='docker' WHERE id=$1`, sid); err != nil {
					t.Fatal(err)
				}
				m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "sandbox", OwnerID: privAcctA, LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "private-member", Path: "one", SourceIdentity: "private-source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
				if err := db.InsertSessionWorkspace(t.Context(), f.pool, sid, m); err != nil {
					t.Fatal(err)
				}
				results := []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "one", LaunchGeneration: "launch"}, Head: "private-head", Pending: true, PushError: "private-rejection", RecoveryLocation: "/private-recovery"}}
				if err := db.SetWorkspaceResults(t.Context(), f.pool, sid, "launch", results); err != nil {
					t.Fatal(err)
				}
				now := time.Now()
				if err := db.EndSessionStatus(t.Context(), f.pool, sid, "stopped", &now, systemEnd(db.EndReasonProcessExited)); err != nil {
					t.Fatal(err)
				}
				before, err := f.api.buildSessionResult(t.Context(), sid, privAcctA)
				if err != nil {
					t.Fatal(err)
				}
				snapshot, _ := json.Marshal(before)
				viewer := privAcctA
				if state == "foreign" {
					viewer = privAcctB
				}
				if state == "unknown" {
					if _, err := f.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=NULL WHERE id=$1`, dc.ID); err != nil {
						t.Fatal(err)
					}
				}
				if state == "reassigned" {
					if err := db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
						_, err := db.AssignDesktop(ctx, f.pool, dc.ID, privAcctB, 1)
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				if state == "runtime-changed" {
					if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster' WHERE id=$1`, sid); err != nil {
						t.Fatal(err)
					}
				}
				var raw []byte
				switch endpoint {
				case "mcp-builder":
					res, err := f.api.Result(t.Context(), RunnerPrincipal{Kind: "human", Sub: viewer}, sid)
					if err != nil {
						t.Fatal(err)
					}
					raw, _ = json.Marshal(res)
				case "queued":
					raw, _ = f.hub.desktopPayloadFor(t.Context(), viewer, snapshot)
				case "callback":
					client := &http.Client{Transport: p6CaptureTransport{capture: func(b []byte) { raw = b }}}
					if _, err := f.api.postCompletionSnapshot(t.Context(), client, "https://callback.example.test", sid, "delivery", "", snapshot, viewer); err != nil {
						t.Fatal(err)
					}
				default:
					handler := f.api.HandleRunnerResult
					if endpoint == "polling-sse" {
						handler = f.api.HandleRunnerEventStream
					}
					if endpoint == "live-sse" {
						handler = f.api.HandleRunnerEventsLive
					}
					response := privDo(handler, "GET", "/", sid, f.tok(t, "human", viewer, "session.start"), "")
					if response.Code != 200 {
						t.Fatalf("response %d %s", response.Code, response.Body)
					}
					raw = response.Body.Bytes()
				}
				if state == "owner" {
					if !strings.Contains(string(raw), "private-rejection") || !strings.Contains(string(raw), "private-recovery") {
						t.Fatalf("outcomes missing: %s", raw)
					}
				} else if strings.Contains(string(raw), "private-") {
					t.Fatalf("member metadata leaked: %s", raw)
				}
				if !strings.Contains(string(raw), `"workspace_results":[`) {
					t.Fatalf("delivery lost shared result schema: %s", raw)
				}
			})
		}
	}
}

type p6CaptureTransport struct{ capture func([]byte) }

func (t p6CaptureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	t.capture(body)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
}

func TestP6CommitAndOriginBrokerProvenance(t *testing.T) {
	f := newTopicFx(t)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "desktop")
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='daemon' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	f.api.hub.SetSessionOwner(f.sess, dc.ID)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "host", OwnerID: topicOwner, LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{
		{ID: "one", Name: "one", Path: "one", SourceIdentity: "source-one", CommonDirectoryIdentity: "common-one", Destination: "destination-one"},
		{ID: "two", Name: "two", Path: "two", SourceIdentity: "source-two", CommonDirectoryIdentity: "common-two", Destination: "destination-two"},
	}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.sess, m); err != nil {
		t.Fatal(err)
	}
	gen, err := db.BeginWorkspaceRuntime(t.Context(), f.pool, f.sess, "launch", 0)
	if err != nil {
		t.Fatal(err)
	}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: gen}, ManifestDigest: "digest", Root: protocol.WorkspaceRoot{Inode: 1}}
	if err := db.BindWorkspaceRuntime(t.Context(), f.pool, f.sess, b.WorkspaceRuntimeRef, b); err != nil {
		t.Fatal(err)
	}
	dc.AuthorizeWorkspaceRuntime(f.sess, b.WorkspaceRuntimeRef)
	for _, origin := range []bool{false, true} {
		if origin {
			if _, err := f.api.hub.requestSessionOrigin(t.Context(), dc, f.sess); err == nil {
				t.Fatal("unqualified folder origin accepted")
			}
		} else {
			if _, err := f.api.hub.requestCommit(t.Context(), dc, topicOwner, f.sess, fixtureSHA, false, time.Second); err == nil {
				t.Fatal("unqualified folder commit accepted")
			}
		}
		select {
		case <-dc.send:
			t.Fatal("unqualified read dispatched")
		default:
		}
		ref := protocol.WorkspaceGitRef{WorkspaceRuntimeRef: b.WorkspaceRuntimeRef, MemberID: "two"}
		done := make(chan error, 1)
		if origin {
			go func() {
				res, err := f.api.hub.requestSessionOrigin(t.Context(), dc, f.sess, ref)
				if err == nil && res.WorkspaceGitRef != ref {
					err = errors.New("wrong origin provenance")
				}
				done <- err
			}()
		} else {
			go func() {
				res, err := f.api.hub.requestCommitRead(t.Context(), dc, topicOwner, protocol.ReadCommit{SessionID: f.sess, SHA: fixtureSHA, WorkspaceGitRef: ref}, time.Second)
				if err == nil && res.WorkspaceGitRef != ref {
					err = errors.New("wrong commit provenance")
				}
				done <- err
			}()
		}
		raw := <-dc.send
		if origin {
			var req protocol.ReadSessionOrigin
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatal(err)
			}
			if req.WorkspaceGitRef != ref {
				t.Fatal("origin request lost member")
			}
			res := protocol.SessionOriginResult{RequestID: req.RequestID, SessionID: f.sess, WorkspaceGitRef: ref, OK: true, Commit: fixtureSHA}
			res.MemberID = "one"
			f.api.hub.deliverSessionOriginResult(dc, res)
			select {
			case <-done:
				t.Fatal("wrong member origin delivered")
			default:
			}
			res.MemberID = "two"
			f.api.hub.deliverSessionOriginResult(dc, res)
		} else {
			var req protocol.ReadCommit
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatal(err)
			}
			if req.WorkspaceGitRef != ref {
				t.Fatal("commit request lost member")
			}
			res := protocol.CommitResult{RequestID: req.RequestID, SessionID: f.sess, WorkspaceGitRef: ref, OK: true}
			res.Generation--
			f.api.hub.deliverCommitResult(dc, res)
			select {
			case <-done:
				t.Fatal("wrong generation commit delivered")
			default:
			}
			res.Generation = gen
			f.api.hub.deliverCommitResult(dc, res)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}

	f.api.RegisterCommitRoutes(f.mux)
	for _, member := range []string{"one", "two"} {
		for _, patch := range []bool{false, true} {
			path := "/api/sessions/" + f.sess + "/commits/" + fixtureSHA
			if patch {
				path += "/diff"
			}
			path += "?member_id=" + member + "&launch_generation=launch&generation=1"
			request := httptest.NewRequest("GET", path, nil)
			request.Header.Set("Authorization", "Bearer "+f.token(topicOwner, "human"))
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { f.mux.ServeHTTP(response, request); close(done) }()
			var req protocol.ReadCommit
			select {
			case raw := <-dc.send:
				if err := json.Unmarshal(raw, &req); err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("qualified HTTP did not dispatch")
			}
			if req.MemberID != member || req.LaunchGeneration != "launch" || req.Generation != gen || req.Patch != patch {
				t.Fatalf("HTTP provenance lost: %+v", req)
			}
			res := protocol.CommitResult{SessionID: f.sess, RequestID: req.RequestID, WorkspaceGitRef: req.WorkspaceGitRef, OK: true}
			if patch {
				res.PatchB64 = base64.StdEncoding.EncodeToString([]byte("patch-" + member))
			} else {
				res.Commit = &protocol.CommitInfo{SHA: fixtureSHA, Short: fixtureSHA[:7], Subject: "member-" + member}
			}
			f.api.hub.deliverCommitResult(dc, res)
			<-done
			if response.Code != 200 {
				t.Fatalf("qualified HTTP refused: %d %s", response.Code, response.Body)
			}
			if patch {
				if response.Body.String() != "patch-"+member || response.Header().Get("X-Blerg-Member-ID") != member || response.Header().Get("X-Blerg-Generation") != "1" {
					t.Fatal("patch provenance lost")
				}
			} else if !strings.Contains(response.Body.String(), `"member_id":"`+member+`"`) {
				t.Fatal("summary provenance lost")
			}
		}
	}

	t.Setenv("BLERG_RUNNER_SELF_URL", "https://runner.example.test")
	row, err := db.GetSession(t.Context(), f.pool, f.sess)
	if err != nil {
		t.Fatal(err)
	}
	originDone := make(chan boardfiling.Origin, 1)
	go func() {
		origin, err := f.api.filingOrigin(t.Context(), row, protocol.WorkspaceGitRef{WorkspaceRuntimeRef: b.WorkspaceRuntimeRef, MemberID: "two"})
		if err != nil {
			originDone <- boardfiling.Origin{}
			return
		}
		originDone <- origin
	}()
	var originReq protocol.ReadSessionOrigin
	select {
	case raw := <-dc.send:
		if err := json.Unmarshal(raw, &originReq); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("qualified filing origin did not dispatch")
	}
	f.api.hub.deliverSessionOriginResult(dc, protocol.SessionOriginResult{RequestID: originReq.RequestID, SessionID: f.sess, WorkspaceGitRef: originReq.WorkspaceGitRef, OK: true, Commit: fixtureSHA})
	origin := <-originDone
	if origin.MemberID != "two" || origin.LaunchGeneration != "launch" || origin.Generation != gen || origin.Repo != "destination-two" || origin.Commit != fixtureSHA {
		t.Fatalf("filing lost member origin: %+v", origin)
	}
	child := newUUID()
	if err := db.InsertSession(t.Context(), f.pool, child, dc.ID, "running", "/workspace", "", "child", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon',spawning_account_id=$2,parent_session_id=$3 WHERE id=$1`, child, topicOwner, f.sess); err != nil {
		t.Fatal(err)
	}
	childManifest := m
	childManifest.LaunchGeneration = "child-launch"
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, child, childManifest); err != nil {
		t.Fatal(err)
	}
	results := []protocol.WorkspaceMemberResult{}
	for _, member := range childManifest.Members {
		results = append(results, protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: "child-launch", MemberID: member.ID}, Base: fixtureSHA})
	}
	if err := db.SetWorkspaceResults(t.Context(), f.pool, child, "child-launch", results); err != nil {
		t.Fatal(err)
	}
	for _, allMerged := range []bool{true, false} {
		done := make(chan bool, 1)
		go func() { merged, _ := f.api.planChildMergedCheck(t.Context(), row, child); done <- merged }()
		for _, member := range childManifest.Members {
			var req protocol.ReadCommit
			select {
			case raw := <-dc.send:
				if err := json.Unmarshal(raw, &req); err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("member merge did not dispatch")
			}
			if req.MemberID != member.ID || req.ChildLaunchGeneration != "child-launch" || req.ChildBase != fixtureSHA || req.LaunchGeneration != "launch" || req.Generation != gen {
				t.Fatalf("child merge lost provenance: %+v", req)
			}
			f.api.hub.deliverCommitResult(dc, protocol.CommitResult{SessionID: f.sess, RequestID: req.RequestID, WorkspaceGitRef: req.WorkspaceGitRef, OK: true, Merged: allMerged || member.ID == "one"})
		}
		if merged := <-done; merged != allMerged {
			t.Fatal("child settlement selected a scalar member")
		}
	}
	// Replacement invalidates both readers before collection and after a reply.
	old := protocol.WorkspaceGitRef{WorkspaceRuntimeRef: b.WorkspaceRuntimeRef, MemberID: "two"}
	next, err := db.BeginWorkspaceRuntime(t.Context(), f.pool, f.sess, "launch", gen)
	if err != nil {
		t.Fatal(err)
	}
	b.Generation = next
	if err := db.BindWorkspaceRuntime(t.Context(), f.pool, f.sess, b.WorkspaceRuntimeRef, b); err != nil {
		t.Fatal(err)
	}
	dc.AuthorizeWorkspaceRuntime(f.sess, b.WorkspaceRuntimeRef)
	if f.api.hub.currentWorkspaceGit(t.Context(), f.sess, old) {
		t.Fatal("old delivery survived runtime replacement")
	}
	if _, err := f.api.hub.requestSessionOrigin(t.Context(), dc, f.sess, old); err == nil {
		t.Fatal("stale origin dispatched")
	}
	if _, err := f.api.hub.requestCommitRead(t.Context(), dc, topicOwner, protocol.ReadCommit{SessionID: f.sess, SHA: fixtureSHA, WorkspaceGitRef: old}, time.Second); err == nil {
		t.Fatal("stale commit dispatched")
	}
}

func TestP6LegacyPatchBroker(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "desktop", send: make(chan []byte, 2)}
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	done := make(chan error, 1)
	go func() {
		_, err := h.requestCommit(t.Context(), dc, "owner", "session", fixtureSHA, true, time.Second)
		done <- err
	}()
	var req protocol.ReadCommit
	if err := json.Unmarshal(<-dc.send, &req); err != nil {
		t.Fatal(err)
	}
	if !req.Patch {
		t.Fatal("legacy diff lost patch mode")
	}
	h.deliverCommitResult(dc, protocol.CommitResult{RequestID: req.RequestID, SessionID: req.SessionID, OK: true})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
