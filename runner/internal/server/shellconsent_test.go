package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

func TestHostShellChallengeUsesAuthenticatedIdentity(t *testing.T) {
	fx := newTopicFx(t)
	ctx := t.Context()
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: topicOwner, Runtime: "host", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "member", Name: "project", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if err := db.InsertSessionWorkspace(ctx, fx.pool, fx.sess, manifest); err != nil {
		t.Fatal(err)
	}
	generation, err := db.BeginWorkspaceRuntime(ctx, fx.pool, fx.sess, manifest.LaunchGeneration, 0)
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: generation}
	b := shellTestBinding()
	b.Session = fx.sess
	b.Owner = topicOwner
	b.Runtime = "host"
	if err = db.BindWorkspaceRuntime(ctx, fx.pool, fx.sess, ref, protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: ref, Root: b.Root, ManifestDigest: manifest.Digest}); err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{WorkspaceCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true, Sandbox: true, Cluster: true}, ID: topicDaemonID, send: make(chan []byte, 8), shellControl: make(chan []byte, 32)}
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	var mu sync.Mutex
	i := shellruntime.Identity{Machine: "authenticated-machine", Account: "actual-account", UID: 1000, Shell: "/bin/bash", Cwd: "/prepared/project"}
	done := make(chan struct{})
	run, cancel := context.WithCancel(ctx)
	go func() {
		defer close(done)
		for {
			select {
			case raw := <-dc.shellControl:
				var request shellruntime.RPC
				if json.Unmarshal(raw, &request) != nil {
					return
				}
				mu.Lock()
				actual := i
				mu.Unlock()
				fx.api.hub.deliverShell(dc, shellruntime.RPC{Version: 1, Type: "owner_shell_result", SessionID: request.SessionID, RequestID: request.RequestID, Op: request.Op, Binding: b, Identity: actual, OK: true})
			case <-run.Done():
				return
			}
		}
	}()
	defer func() { cancel(); <-done }()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Owner, Login string }
		var values map[string]string
		_ = json.NewDecoder(r.Body).Decode(&values)
		body.Owner = values["owner_id"]
		body.Login = values["login_id"]
		if r.URL.Path != "/internal/shell/authorize" || r.Header.Get("X-Internal-Key") != "core-key" || body.Owner != topicOwner || body.Login != testSID {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	}))
	defer core.Close()
	fx.api.coreURL = core.URL
	fx.api.coreInternalKey = "core-key"
	fx.api.shellRuntime = newRemoteShell(fx.api)
	fx.api.RegisterShellRoutes(fx.mux)
	call := func(action, token, origin, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "http://shell.example/api/sessions/"+fx.sess+"/shell-consent/"+action, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, req)
		return rec
	}
	for _, token := range []string{fx.token(topicOther, "human", "account.manage"), fx.token(topicOwner, "agent"), ""} {
		if rec := call("challenge", token, "http://shell.example", ""); rec.Code == 201 {
			t.Fatal("non-owner human challenge")
		}
	}
	owner := fx.token(topicOwner, "human")
	if rec := call("challenge", owner, "https://foreign.example", ""); rec.Code != 403 {
		t.Fatal("foreign origin")
	}
	// An adapter that has not completed startup cannot offer host shell consent.
	if rec := call("challenge", owner, "http://shell.example", ""); rec.Code != 403 {
		t.Fatal("unready shell adapter offered consent", rec.Code)
	}
	// This fixture installs remoteShell directly rather than running startup.
	fx.api.hub.runtimeShellReady.Store(true)
	rec := call("challenge", owner, "http://shell.example", "")
	if rec.Code != 201 {
		t.Fatal("challenge", rec.Code)
	}
	var challenge protocol.HostShellConsent
	if json.Unmarshal(rec.Body.Bytes(), &challenge) != nil {
		t.Fatal("challenge JSON")
	}
	if challenge.MachineID != i.Machine || challenge.Account != i.Account || challenge.UID != i.UID || challenge.Shell != i.Shell || challenge.Cwd != i.Cwd {
		t.Fatal("authenticated identity not named")
	}
	body := `{"challenge_id":"` + challenge.ChallengeID + `","accept":true}`
	if rec = call("accept", owner, "http://shell.example", strings.TrimSuffix(body, "}")+`,"machine_id":"spoof"}`); rec.Code != 400 {
		t.Fatal("browser identity override accepted")
	}
	mu.Lock()
	i.UID++
	mu.Unlock()
	if rec = call("accept", owner, "http://shell.example", body); rec.Code != 403 {
		t.Fatal("changed account accepted prior challenge")
	}
	mu.Lock()
	i.UID--
	mu.Unlock()
	if rec = call("accept", owner, "http://shell.example", body); rec.Code != 403 {
		t.Fatal("old challenge revived after identity labels returned", rec.Code)
	}
	rec = call("challenge", owner, "http://shell.example", "")
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &challenge) != nil {
		t.Fatal("fresh identity challenge unavailable")
	}
	body = `{"challenge_id":"` + challenge.ChallengeID + `","accept":true}`
	if rec = call("accept", owner, "http://shell.example", body); rec.Code != 200 {
		t.Fatal("fresh affirmation", rec.Code)
	}
	resolved, err := fx.api.shellRuntime.Resolve(ctx, fx.sess)
	if err != nil || !resolved.HostConsentValid {
		t.Fatal("fresh acceptance unavailable", err)
	}
	mu.Lock()
	i.UID++
	mu.Unlock()
	if resolved, err = fx.api.shellRuntime.Resolve(ctx, fx.sess); err != nil || resolved.HostConsentValid {
		t.Fatal("different identity retained acceptance", err)
	}
	mu.Lock()
	i.UID--
	mu.Unlock()
	if resolved, err = fx.api.shellRuntime.Resolve(ctx, fx.sess); err != nil || resolved.HostConsentValid {
		t.Fatal("accepted consent revived after A to B to A", err)
	}
	if rec = call("accept", owner, "http://shell.example", body); rec.Code != 403 {
		t.Fatal("challenge replay")
	}
	// A different launch/physical incarnation cannot reuse accepted consent.
	if _, err = fx.pool.Exec(ctx, `UPDATE session_workspaces SET runtime_generation=runtime_generation+1,runtime_binding=NULL WHERE session_id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if rec = call("accept", owner, "http://shell.example", body); rec.Code != 403 {
		t.Fatal("relaunch accepted old consent")
	}
}
