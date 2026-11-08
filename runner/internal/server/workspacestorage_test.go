package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceStorageOnlyHumanOwner(t *testing.T) {
	f := newTopicFx(t)
	f.api.RegisterWorkspaceStorageRoutes(f.mux)
	for _, method := range []string{"GET", "POST"} {
		for _, token := range []string{f.sessTok, f.otherTok, f.token(topicOther, "human")} {
			res := pluginReq(f, method, "/api/sessions/"+f.sess+"/workspace/storage", token, map[string]string{"confirm": strings.Repeat("a", 64)})
			if res.Code != 401 && res.Code != 403 && res.Code != 404 {
				t.Fatalf("nonowner or agent admitted: %d", res.Code)
			}
		}
	}
}
func TestWorkspaceStorageRefusalLogsCause(t *testing.T) {
	f := newTopicFx(t)
	f.api.RegisterWorkspaceStorageRoutes(f.mux)
	var logs bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer func() { failurelog.Default = old }()
	res := pluginReq(f, "POST", "/api/sessions/"+f.sess+"/workspace/storage", f.token(topicOwner, "human"), map[string]string{"confirm": "canary-private"})
	if res.Code == 200 {
		t.Fatal("live session admitted")
	}
	if !strings.Contains(logs.String(), "unsupported_or_live_session") || strings.Contains(logs.String(), "canary-private") {
		t.Fatal("missing cause or private body logged")
	}
}

func TestWorkspaceStorageWireSurvivesEndedSessionOwnershipRemoval(t *testing.T) {
	f := newTopicFx(t)
	f.api.RegisterWorkspaceStorageRoutes(f.mux)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "storage-desktop")
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='docker',status='stopped',ended_at=now() WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "sandbox", OwnerID: topicOwner, LaunchGeneration: newUUID(), Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.sess, manifest); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	operations := make(chan protocol.WorkspaceStorageRequest, 3)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 3 {
			select {
			case raw := <-dc.send:
				var req protocol.WorkspaceStorageRequest
				if json.Unmarshal(raw, &req) != nil {
					return
				}
				operations <- req
				preview := &protocol.WorkspaceStoragePreview{Digest: digest, Files: []string{"one/local.txt"}, Commits: []string{}}
				if req.Operation != "preview" {
					preview.Released = true
				}
				reply, _ := json.Marshal(protocol.WorkspaceStorageResult{Type: "workspace_storage_result", RequestID: req.RequestID, SessionID: f.sess, Preview: preview})
				// Another daemon and a reply for another session cannot consume this request.
				f.api.hub.deliverExtResult(&DaemonConn{ID: "foreign"}, "workspace_storage_result", reply)
				f.api.hub.deliverExtResult(dc, "workspace_storage_result", reply)
			case <-t.Context().Done():
				return
			}
		}
	}()
	owner := f.token(topicOwner, "human")
	read := pluginReq(f, "GET", "/api/sessions/"+f.sess+"/workspace/storage", owner, nil)
	if read.Code != 200 || !strings.Contains(read.Body.String(), "one/local.txt") {
		t.Fatalf("preview: %d %s", read.Code, read.Body.String())
	}
	released := pluginReq(f, "POST", "/api/sessions/"+f.sess+"/workspace/storage", owner, map[string]string{"confirm": digest})
	if released.Code != 200 || !strings.Contains(released.Body.String(), `"released":true`) {
		t.Fatalf("release: %d %s", released.Code, released.Body.String())
	}
	<-done
	for _, op := range []string{"preview", "release", "acknowledge"} {
		req := <-operations
		if req.Operation != op || req.OwnerID != topicOwner || (op == "release" && req.Confirm != digest) {
			t.Fatalf("wrong control: %+v", req)
		}
	}
}

func TestWorkspaceStorageOversizedFrameLogsCause(t *testing.T) {
	var logs bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer func() { failurelog.Default = old }()
	raw := []byte(strings.Repeat("private-canary", 21000))
	handleDaemonFrame(t.Context(), NewHub(), &DaemonConn{}, nil, raw, envelope{Type: "workspace_storage_result"})
	if !strings.Contains(logs.String(), "result_frame_too_large") || strings.Contains(logs.String(), "private-canary") {
		t.Fatal("missing safe oversized-frame cause")
	}
}
