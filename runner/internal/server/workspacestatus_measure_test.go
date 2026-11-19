//go:build linux || darwin

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type statusMeasureSender struct {
	hub *Hub
	dc  *DaemonConn
}

func (s statusMeasureSender) Send(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var res protocol.WorkspaceExplorerResult
	if err = json.Unmarshal(raw, &res); err != nil {
		return err
	}
	s.hub.deliverWorkspaceExplorerResult(s.dc, res)
	return nil
}

// Opt-in measurement through the HTTP handler and production daemon router/reader.
// The existing checkout is read only; the database fixture is isolated.
func TestWorkspaceStatusMeasureRealEndpoint(t *testing.T) {
	root := os.Getenv("BLERG_STATUS_TEST_REPOSITORY")
	if root == "" {
		t.Skip("set BLERG_STATUS_TEST_REPOSITORY to an existing large checkout")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	fx := newTopicFx(t)
	fx.api.RegisterWorkspaceFileRoutes(fx.mux)
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: topicOwner, Runtime: "host", LaunchGeneration: "measure", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "example-org/one"}}}
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
	stat := info.Sys().(*syscall.Stat_t)
	device := uint64(stat.Dev) //nolint:unconvert // Darwin's device is signed.
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: generation}, ManifestDigest: manifest.Digest, Root: protocol.WorkspaceRoot{Device: device, Inode: stat.Ino}}
	reader, err := daemon.NewLocalExplorer(root, manifest, binding, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	router := &daemon.WorkspaceExplorerRouter{}
	router.Bind(fx.sess, reader)
	defer router.Unbind(fx.sess)
	dc := workspaceOwnedDesktop(t, fx.api.hub, fx.pool, topicOwner, "desktop")
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2 WHERE id=$1`, fx.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	dc.SetSandboxClones(true)
	dc.SetWorkBranches(true)
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	if !dc.AuthorizeWorkspaceRuntime(fx.sess, binding.WorkspaceRuntimeRef) {
		t.Fatal("authorization refused")
	}
	fx.api.hub.bindPreparedWorkspaceRuntime(t.Context(), fx.pool, dc, protocol.WorkspaceRuntimePrepared{Type: "workspace_runtime_prepared", SessionID: fx.sess, Binding: binding})
	done := make(chan struct{})
	stopped := make(chan struct{})
	defer func() { close(done); <-stopped }()
	go func() {
		defer close(stopped)
		for {
			select {
			case raw := <-dc.send:
				router.Handle(t.Context(), statusMeasureSender{fx.api.hub, dc}, raw)
			case <-done:
				return
			}
		}
	}()
	token := fx.token(topicOwner, "human")
	read := func(op string) (string, time.Duration) {
		request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+"/workspace?op="+op, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		started := time.Now()
		fx.mux.ServeHTTP(&reviewDeadlineWriter{ResponseRecorder: response}, request)
		var body struct {
			Code   string                           `json:"code"`
			Result protocol.WorkspaceExplorerResult `json:"result"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			return "invalid_json", time.Since(started)
		}
		label := body.Code
		if response.Code == 200 {
			label = "result." + body.Result.ErrorCode
			if body.Result.StatusReason != "" {
				label += ":" + body.Result.StatusReason
			}
			if body.Result.OK {
				label = "ok"
				if body.Result.Status != nil && body.Result.Status.Partial {
					label = "ok_partial"
				}
			}
		}
		return label, time.Since(started)
	}
	for _, concurrent := range []bool{false, true} {
		counts := map[string]int{}
		var total time.Duration
		for range 30 {
			var wg sync.WaitGroup
			if concurrent {
				wg.Go(func() { read("list") })
			}
			label, elapsed := read("status")
			counts[label]++
			total += elapsed
			wg.Wait()
		}
		t.Logf("concurrent_list=%v status_samples=30 counts=%v mean=%v", concurrent, counts, total/30)
	}
}
