//go:build linux && persistent_shell_test

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
)

func TestPersistentWireHelperProcess(t *testing.T) {
	for index, arg := range os.Args {
		if arg != "persistent-wire-helper" {
			continue
		}
		if index+1 >= len(os.Args) {
			os.Exit(1)
		}
		shellruntime.SetPersistentWireHelperClock(os.Args[index+1])
		var err error
		switch os.Args[len(os.Args)-1] {
		case "--probe":
			err = shellruntime.Probe(os.Stdout)
		case "--probe-persistence":
			err = shellruntime.ProbePersistent(os.Stdin, os.Stdout)
		default:
			err = shellruntime.ServeHelper(os.Stdin, os.Stdout)
		}
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

// No calls to Hub.deliverShell, Service.execute or a fake supervisor: every RPC
// crosses ServeDaemon/handleDaemonFrame and the production WSClient dispatcher.
func TestPersistentShellRealWireReconnectAndWithdrawal(t *testing.T) {
	fx := newTopicFx(t)
	ctx := t.Context()
	grace := daemonLostGrace.Swap(0)
	defer daemonLostGrace.Store(grace)
	clockFile := filepath.Join(t.TempDir(), "elapsed")
	advance := func(d time.Duration) {
		t.Helper()
		next := clockFile + ".next"
		if err := os.WriteFile(next, []byte(strconv.FormatInt(int64(d), 10)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(next, clockFile); err != nil {
			t.Fatal(err)
		}
	}
	advance(0)
	cwd := t.TempDir()
	identity, err := shellruntime.ResolvePersistentIdentity(cwd)
	if err != nil {
		t.Fatal(err)
	}
	caps := protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true, ShellPersistence: 2, PersistentHost: true}
	var online atomic.Bool
	online.Store(true)
	mux := http.NewServeMux()
	serveDaemon := fx.api.hub.ServeDaemon(topicDaemonToken, fx.pool)
	mux.HandleFunc("/ws/daemon", func(w http.ResponseWriter, r *http.Request) {
		if !online.Load() {
			http.Error(w, "desktop offline", http.StatusServiceUnavailable)
			return
		}
		serveDaemon(w, r)
	})
	fx.api.RegisterShellRoutes(mux)
	app := httptest.NewServer(mux)
	defer app.Close()
	client := daemon.NewWSClient(daemon.Config{ServerURL: "ws" + strings.TrimPrefix(app.URL, "http") + "/ws/daemon", DaemonToken: "bdt_" + strings.Repeat("f", 64), DesktopID: topicDaemonID, DaemonMode: "local", DaemonName: "wire-desktop", ReposRoot: cwd, WorkspaceCapabilities: caps, HeartbeatInterval: time.Hour, ReconnectInitial: 20 * time.Millisecond, ReconnectMax: 20 * time.Millisecond})
	service := client.OwnerShellRuntime()
	now := shellruntime.SetPersistentWireServiceClock(service, clockFile)
	enrollment := desktopauth.Binding{DesktopID: topicDaemonID, OwnerID: topicOwner, TokenID: newUUID(), Revision: 1, ExpiresAt: now().Add(8 * 24 * time.Hour)}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/desktop-tokens/validate":
			_ = json.NewEncoder(w).Encode(enrollment)
		case "/internal/shell/authorize":
			var request map[string]any
			_ = json.NewDecoder(r.Body).Decode(&request)
			_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "revision": 1, "enabled": true, "authorization_version": 2, "outcome": "allow", "owner_id": request["owner_id"], "login_id": request["login_id"], "nonce": request["nonce"], "login_expires_at": now().Add(8 * 24 * time.Hour).Unix()})
		default:
			http.NotFound(w, r)
		}
	}))
	defer core.Close()
	fx.api.coreURL = core.URL
	fx.api.coreInternalKey = "fixture-key"
	remote := newRemoteShell(fx.api)
	fx.api.shellRuntime = remote
	var runnerSkew atomic.Int64
	manager, err := ownershell.New(shellAuthority{core.URL, "fixture-key", core.Client()}, shellResolver{fx.api, remote}, remote, nil, func() time.Time { return now().Add(time.Duration(runnerSkew.Load())) })
	if err != nil {
		t.Fatal(err)
	}
	fx.api.shells = manager
	fx.api.hub.mu.Lock()
	fx.api.hub.shellCustodian = manager.Incarnation()
	fx.api.hub.mu.Unlock()
	fx.api.hub.runtimeShellReady.Store(true)
	fx.api.hub.shellCustodyReady.Store(true)
	policy := protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}
	manager.SetPolicy(ctx, policy)
	defer manager.Shutdown(context.Background())
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: topicOwner, Runtime: "host", LaunchGeneration: "wire-launch", Digest: "wire-manifest", Members: []protocol.WorkspaceMember{{ID: "member", Name: "project", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if _, err = fx.pool.Exec(ctx, `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if err = db.InsertSessionWorkspace(ctx, fx.pool, fx.sess, manifest); err != nil {
		t.Fatal(err)
	}
	generation, err := db.BeginWorkspaceRuntime(ctx, fx.pool, fx.sess, manifest.LaunchGeneration, 0)
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: generation}
	if err = db.BindWorkspaceRuntime(ctx, fx.pool, fx.sess, ref, protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: ref, Root: identity.MemberRoot, ManifestDigest: manifest.Digest}); err != nil {
		t.Fatal(err)
	}
	b := ownershell.Binding{RuntimeGeneration: generation, Root: identity.MemberRoot, ManifestDigest: manifest.Digest, Session: fx.sess, Owner: topicOwner, Launch: manifest.LaunchGeneration, Connection: "initial", Audience: ownershell.RuntimeAudience("initial", generation), Runtime: "host", Live: true, ServerCapabilities: caps, RuntimeCapabilities: caps}
	var starts atomic.Int32
	command := shellruntime.Command{Executable: "/usr/bin/env", Args: []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestPersistentWireHelperProcess$", "--", "persistent-wire-helper", clockFile}, Identity: identity, Root: identity.MemberRoot, PublicRoot: cwd, Policy: policy, OwnedStarted: func(int) error { starts.Add(1); return nil }}
	if err = service.Register(ctx, new(sync.Mutex), b, command, func(context.Context) error { return service.ProveRecordedShellsEmpty(fx.sess) }); err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); client.Run(run, func() []string { return []string{fx.sess} }) }()
	defer func() { cancel(); <-done }()
	wireWait(t, "registered and negotiated desktop", func() bool {
		dc := fx.api.hub.GetDaemon(topicDaemonID)
		binding, e := service.Binding(fx.sess)
		return dc != nil && dc.ShellLink != nil && e == nil && binding.ServerCapabilities.ShellVersion == 1
	})
	token := fx.token(topicOwner, "human")
	post := func(path string, value any) (int, []byte) {
		t.Helper()
		var body []byte
		if value != nil {
			body, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
		}
		request, e := http.NewRequestWithContext(ctx, "POST", app.URL+path, bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Origin", app.URL)
		request.Header.Set("Content-Type", "application/json")
		response, e := app.Client().Do(request)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		raw, e := io.ReadAll(response.Body)
		if e != nil {
			t.Fatal(e)
		}
		return response.StatusCode, raw
	}
	base := "/api/sessions/" + fx.sess
	status, raw := post(base+"/shell-consent/challenge", nil)
	if status != 201 {
		t.Fatalf("wire challenge: %d %s", status, raw)
	}
	var host protocol.HostShellConsent
	if err = json.Unmarshal(raw, &host); err != nil || host.ID == "" {
		t.Fatal("wire challenge missing", err)
	}
	status, raw = post(base+"/shell-consent/accept", map[string]any{"version": 2, "challenge_id": host.ChallengeID, "accept": true})
	if status != 200 {
		t.Fatalf("wire accept: %d %s", status, raw)
	}
	runnerSkew.Store(int64(10 * time.Second))
	status, raw = post(base+"/shells", nil)
	var refusal struct {
		Code string `json:"code"`
	}
	if err = json.Unmarshal(raw, &refusal); err != nil || status != http.StatusForbidden || refusal.Code != string(ownershell.ReasonOpenRuntime) || starts.Load() != 0 {
		t.Fatalf("clock refusal did not reach authenticated allocation boundary: %d starts=%d %s", status, starts.Load(), raw)
	}
	remote.mu.Lock()
	reusable := remote.consents[host.ID] != nil && !remote.consents[host.ID].consumed
	remote.mu.Unlock()
	if !reusable {
		t.Fatal("wire AllocationRefused did not release consent")
	}
	runnerSkew.Store(0)
	status, raw = post(base+"/shells", nil)
	if status != 201 {
		t.Fatalf("wire open retry: %d %s", status, raw)
	}
	var info protocol.OwnerShellInfo
	if err = json.Unmarshal(raw, &info); err != nil || !info.Persistent {
		t.Fatal("wire open missing custody", err)
	}
	browser := func() *websocket.Conn {
		t.Helper()
		dialer := websocket.Dialer{Subprotocols: []string{"bearer", token}}
		socket, response, e := dialer.DialContext(ctx, "ws"+strings.TrimPrefix(app.URL, "http")+"/api/shells/"+info.Handle+"/socket?replay=1", http.Header{"Origin": []string{app.URL}})
		if response != nil {
			_ = response.Body.Close()
		}
		if e != nil {
			t.Fatal("browser attach", e)
		}
		_ = socket.SetReadDeadline(time.Now().Add(8 * time.Second))
		var replay struct {
			Type  string `json:"type"`
			Bytes int    `json:"bytes"`
		}
		if e = socket.ReadJSON(&replay); e != nil || replay.Type != "replay" {
			t.Fatal("wire replay notice", e, replay.Type)
		}
		for remaining := replay.Bytes; remaining > 0; {
			kind, data, e := socket.ReadMessage()
			if e != nil {
				t.Fatal(e)
			}
			if kind == websocket.BinaryMessage {
				remaining -= len(data)
			}
		}
		return socket
	}
	readMarker := func(socket *websocket.Conn, marker string) {
		t.Helper()
		var data []byte
		for !bytes.Contains(data, []byte(marker)) {
			kind, chunk, e := socket.ReadMessage()
			if e != nil {
				t.Fatalf("wire shell output %s: %v", marker, e)
			}
			if kind == websocket.BinaryMessage {
				data = append(data, chunk...)
			}
		}
	}
	first := browser()
	if err = first.WriteJSON(protocol.OwnerShellClientFrame{Type: "input", Seq: 1, Human: true, Data: []byte("KEEP_WIRE=same-wire-process; printf '\\127\\111\\122\\105\\137\\122\\105\\101\\104\\131\\n'\n")}); err != nil {
		t.Fatal(err)
	}
	readMarker(first, "WIRE_READY")
	_ = first.Close()
	old := fx.api.hub.GetDaemon(topicDaemonID)
	if old == nil {
		t.Fatal("desktop disappeared")
	}
	online.Store(false)
	old.desktopClose()
	wireWait(t, "desktop disconnected", func() bool { return fx.api.hub.GetDaemon(topicDaemonID) == nil })
	wireWait(t, "daemon loss recorded", func() bool {
		var lost bool
		e := fx.pool.QueryRow(ctx, `SELECT daemon_lost_at IS NOT NULL FROM sessions WHERE id=$1`, fx.sess).Scan(&lost)
		return e == nil && lost
	})
	advance(31 * time.Minute)
	if _, err = fx.pool.Exec(ctx, `UPDATE sessions SET daemon_lost_at=now()-interval '31 minutes' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	manager.Tick(ctx)
	fx.api.finaliseLostDaemonSessions(ctx)
	row, err := db.GetSession(ctx, fx.pool, fx.sess)
	if err != nil || row.EndReason != nil {
		t.Fatal("real-wire retained session ended after 31 minutes", err)
	}
	online.Store(true)
	wireWait(t, "desktop reconnected", func() bool {
		dc := fx.api.hub.GetDaemon(topicDaemonID)
		return dc != nil && dc != old && dc.ShellLink != nil
	})
	second := browser()
	defer second.Close()
	if err = second.WriteJSON(protocol.OwnerShellClientFrame{Type: "input", Seq: 1, Human: true, Data: []byte("printf '%s\\n' \"$KEEP_WIRE\"\n")}); err != nil {
		t.Fatal(err)
	}
	readMarker(second, "same-wire-process")
	if starts.Load() != 1 {
		t.Fatalf("wire reconnect replaced helper: %d starts", starts.Load())
	}
	status, raw = post(base+"/shell-consent/withdraw", map[string]string{"consent_id": host.ID})
	if status != 200 {
		t.Fatalf("wire withdrawal: %d %s", status, raw)
	}
	var state struct {
		Withdrawn bool `json:"withdrawn"`
		Pending   bool `json:"cleanup_pending"`
	}
	if err = json.Unmarshal(raw, &state); err != nil || !state.Withdrawn || state.Pending {
		t.Fatal("wire withdrawal did not settle physical cleanup", err)
	}
	var audit ownershell.Audit
	var encoded []byte
	if err = fx.pool.QueryRow(ctx, `SELECT withdrawal_audit FROM shell_host_consents_v2 WHERE acceptance_id=$1`, host.ID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(encoded, &audit); err != nil || audit.Login != testSID || audit.Event != ownershell.ConsentWithdrawn || audit.CustodyKind != "allocation" || audit.Epoch < 2 {
		t.Fatal("wire withdrawal lost durable allocation audit", err)
	}
	t.Logf("same helper survived 31 virtual minutes; withdrawal settled; epoch=%d", audit.Epoch)
}

func wireWait(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
