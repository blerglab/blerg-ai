//go:build linux || darwin

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestDatasetMergesSocketDaemonRoundTripAndMark(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
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
	for _, args := range [][]string{{"init", "-q", "--initial-branch=main"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "Base"}, {"tag", "v0.5.4"}} {
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

	git := func(args ...string) {
		t.Helper()
		fixed := []string{"-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com"}
		if output, err := exec.CommandContext(ctx, "git", append(fixed, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, output)
		}
	}
	merge := func(branch string) {
		git("checkout", "-qb", branch)
		git("commit", "--allow-empty", "-qm", "Feature")
		git("checkout", "-q", "main")
		git("merge", "--no-ff", branch, "-m", "Merge feature")
	}
	merge("feature-one")
	peers := make(chan *websocket.Conn, 1)
	fx.mux.HandleFunc("GET /fixture-daemon", func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		conn, err := up.Upgrade(w, r, nil)
		if err == nil {
			peers <- conn
		}
	})
	fx.mux.HandleFunc("/ws/browser", fx.api.hub.ServeBrowser(fx.pool, fx.api.AuthorizeBrowserWS))
	server := httptest.NewServer(fx.mux)
	defer server.Close()
	socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/fixture-daemon", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	peer := <-peers
	defer peer.Close()
	_ = socket.SetReadDeadline(time.Now().Add(25 * time.Second))
	_ = peer.SetReadDeadline(time.Now().Add(25 * time.Second))
	host := daemon.NewAgentHost(graphSocketSender{socket}, daemon.AgentHostConfig{HomeDir: t.TempDir()})
	host.BindWorkspaceExplorer(fx.sess, reader)
	done := make(chan error, 1)
	snapshots := make(chan *protocol.MergeHistory, 2)
	go func() {
		for i := 0; i < 2; i++ {
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
			if result.Merges != nil {
				copyHistory := *result.Merges
				copyHistory.WorkspaceGitRef = result.WorkspaceGitRef
				snapshots <- &copyHistory
			}
			fx.api.hub.deliverCommitResult(fx.dc, result)
		}
		done <- nil
	}()
	token := fx.token(topicOwner, "human")
	dialer := websocket.Dialer{Subprotocols: []string{"bearer", token}}
	browser, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/browser", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	readMessageWithDeadline(t, browser)
	q := protocol.DatasetSubscribe{Type: "subscribe_dataset", SessionID: fx.sess, SubscriptionID: newUUID(), Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.cards.merged", Parameters: map[string]string{"bucket": "day", "timezone": "UTC", "release": "0.6.0", "base": "v0.5.4", "member": "app"}}}
	if err := browser.WriteJSON(q); err != nil {
		t.Fatal(err)
	}
	next := func() protocol.DatasetDelta {
		t.Helper()
		for {
			var delta protocol.DatasetDelta
			if err := json.Unmarshal(readMessageWithDeadline(t, browser), &delta); err != nil {
				t.Fatal(err)
			}
			if delta.Type == "dataset_delta" {
				return delta
			}
		}
	}
	first := next()
	if first.Status != "ready" || !first.Snapshot || len(first.Upserts) != 1 || first.Upserts[0]["count"] != float64(1) {
		t.Fatalf("merge snapshot: %+v", first)
	}
	cached := <-snapshots
	rows, observed, cause := resolveDataset(ctx, fx.api.hub, fx.pool, topicOwner, q, &cached, false)
	if cause != "" || len(rows) != 1 || !observed.Equal(first.ObservedAt) {
		t.Fatalf("cached merge freshness: %s %s", cause, observed)
	}
	merge("feature-two")
	request := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/marks", strings.NewReader(`{"kind":"milestone","text":"Merged fixture work"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	fx.mux.ServeHTTP(response, request)
	if response.Code != 201 {
		t.Fatalf("mark: %d %s", response.Code, response.Body.String())
	}
	second := next()
	if second.Status != "ready" || second.Snapshot || second.BaseSequence != first.Sequence || len(second.Upserts) != 1 || second.Upserts[0]["count"] != float64(2) || !second.ObservedAt.After(first.ObservedAt) {
		t.Fatalf("merge delta after mark: %+v", second)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
