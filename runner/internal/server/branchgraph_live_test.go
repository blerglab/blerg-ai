//go:build linux || darwin

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

type graphSocketSender struct{ conn *websocket.Conn }

func (s graphSocketSender) Send(message any) error { return s.conn.WriteJSON(message) }

// A live HTTP request crosses the JSON websocket boundary into the production
// daemon reader and real Git. No canned graph or direct internal read shortcut.
func TestBranchGraphLiveDaemonEndpoint(t *testing.T) {
	liveBranchGraph(t, func(generation int64) string {
		return "member_id=app&launch_generation=launch&generation=" + strconv.FormatInt(generation, 10)
	})
}

// The graph tab knows the member, not the runtime generation. The server owns
// that binding, so the request the app really sends must reach the live reader.
func TestBranchGraphLiveDaemonEndpointFromTheGraphTab(t *testing.T) {
	liveBranchGraph(t, func(int64) string { return "member_id=app&depth=50" })
}

func liveBranchGraph(t *testing.T, query func(int64) string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	fx := newCommitFx(t)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "app")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "--initial-branch=main"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "Live graph"}, {"tag", "v0.6.0"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "host", OwnerID: topicOwner, LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "app", Name: "app", Path: "app", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
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
	st := info.Sys().(*syscall.Stat_t)
	device := uint64(st.Dev) //nolint:unconvert // Darwin st_dev is signed.
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: generation}, ManifestDigest: "digest", Root: protocol.WorkspaceRoot{Device: device, Inode: st.Ino}}
	if err := db.BindWorkspaceRuntime(t.Context(), fx.pool, fx.sess, binding.WorkspaceRuntimeRef, binding); err != nil {
		t.Fatal(err)
	}
	fx.dc.AuthorizeWorkspaceRuntime(fx.sess, binding.WorkspaceRuntimeRef)
	reader, err := daemon.NewLocalExplorer(root, manifest, binding, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	peers := make(chan *websocket.Conn, 1)
	fx.mux.HandleFunc("GET /fixture-daemon", func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		peer, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			peers <- peer
		}
	})
	server := httptest.NewServer(fx.mux)
	defer server.Close()
	socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/fixture-daemon", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	_ = socket.SetReadDeadline(time.Now().Add(15 * time.Second))
	peer := <-peers
	defer peer.Close()
	_ = peer.SetReadDeadline(time.Now().Add(15 * time.Second))
	host := daemon.NewAgentHost(graphSocketSender{socket}, daemon.AgentHostConfig{HomeDir: t.TempDir()})
	host.BindWorkspaceExplorer(fx.sess, reader)
	done := make(chan error, 1)
	go func() {
		var raw []byte
		select {
		case raw = <-fx.dc.send:
		case <-ctx.Done():
			done <- ctx.Err()
			return
		}
		if err := peer.WriteMessage(websocket.TextMessage, raw); err != nil {
			done <- err
			return
		}
		var request protocol.ReadCommit
		if err := socket.ReadJSON(&request); err != nil {
			done <- err
			return
		}
		host.ReadCommit(ctx, request)
		var result protocol.CommitResult
		if err := peer.ReadJSON(&result); err != nil {
			done <- err
			return
		}
		fx.api.hub.deliverCommitResult(fx.dc, result)
		done <- nil
	}()
	request, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/sessions/"+fx.sess+"/git/graph?"+query(generation), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("live graph HTTP %d: %s", response.StatusCode, raw)
	}
	var graph protocol.BranchGraph
	if err := json.Unmarshal(raw, &graph); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || len(graph.Tags) != 1 || graph.Tags[0].Name != "v0.6.0" || !graph.Tags[0].Complete || len(graph.Tags[0].Contains) != 1 || graph.Commits[0].Date == "" || len(graph.Commits) != 1 || graph.Commits[0].Subject != "Live graph" || graph.MemberID != "app" || graph.Generation != generation {
		t.Fatalf("live graph HTTP %d: %+v", response.StatusCode, graph)
	}
	t.Logf("live HTTP -> websocket -> daemon -> Git -> JSON: status=%d commits=%d member=%s", response.StatusCode, len(graph.Commits), graph.MemberID)
}
