//go:build linux

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Start the shipped daemon entry point, never a Config with hand-stamped
// capabilities. HOME/state, sources and all process lifetime belong to this test.
func TestP6EnabledProductionStack(t *testing.T) {
	p6ProductionStack(t, os.Getenv("BLERG_HOST_HELPER_TEST_BINARY"))
}
func TestLegacyProductionRecovery(t *testing.T) {
	p6ProductionStack(t, os.Getenv("BLERG_HOST_HELPER_TEST_BINARY"), true)
}
func p6ProductionStack(t *testing.T, binary string, legacy ...bool) {
	legacyOnly := len(legacy) > 0 && legacy[0]
	if binary == "" {
		t.Skip("build the production daemon and supply BLERG_HOST_HELPER_TEST_BINARY")
	}
	if !protocol.ClusterWorkspaceEnabled || protocol.MacOSHostWorkspaceEnabled {
		t.Fatal("cluster must ship enabled; macOS host must remain disabled")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := newPrivFixture(t)
	id := newUUID()
	token := "bdt_" + strings.Repeat("e", 64)
	binding := desktopauth.Binding{DesktopID: id, OwnerID: privAcctA, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Key") != "fixture-internal-key" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/internal/desktop-tokens/validate":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["token"] != token {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(binding)
		case "/internal/shell/authorize":
			_ = json.NewEncoder(w).Encode(protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
		case "/internal/shell/control":
			w.Header().Set("Content-Type", "application/x-ndjson")
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				_ = json.NewEncoder(w).Encode(map[string]any{"policy": protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}})
				w.(http.Flusher).Flush()
				select {
				case <-ctx.Done():
					return
				case <-r.Context().Done():
					return
				case <-ticker.C:
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer func() { cancel(); core.Close() }()
	f.api.coreURL, f.api.coreInternalKey = core.URL, "fixture-internal-key"
	if err := f.api.StartRuntimeShellChannel(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", f.hub.ServeDaemon(spawnTokenDaemonTok, f.pool))
	f.api.RegisterWorkspaceDiscoveryRoutes(mux)
	f.api.RegisterWorkspaceFileRoutes(mux)
	f.api.RegisterShellRoutes(mux)
	mux.HandleFunc("POST /api/sessions", f.api.HandlePostSessions)
	mux.HandleFunc("DELETE /api/sessions/{id}", f.api.HandleDeleteSession)
	mux.HandleFunc("POST /api/runner/sessions/{id}/message", f.api.HandleRunnerMessage)
	server := httptest.NewServer(mux)
	defer server.Close()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	repos := filepath.Join(root, "repos")
	for _, path := range []string{home, repos, filepath.Join(root, "tmux")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	engineDir := filepath.Join(root, "engines")
	if err := os.Mkdir(engineDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engineDir, "claude"), []byte(p6EnabledEngine), 0700); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"one", "two"} {
		path := filepath.Join(repos, "project", member)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Example"}, {"config", "user.email", "developer@example.com"}} {
			p6EnabledGit(t, path, args...)
		}
		if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("real "+member+" member"), 0600); err != nil {
			t.Fatal(err)
		}
		p6EnabledGit(t, path, "add", "README.md")
		p6EnabledGit(t, path, "commit", "-qm", "initial")
	}

	// A private v1 recovery record and scalar DB row are exactly what an older
	// daemon leaves behind. No workspace manifest/capabilities are in this record.
	legacyID := newUUID()
	if err := db.WithDesktopFence(ctx, f.pool, id, func(ctx context.Context) error {
		return db.BindDesktopConnection(ctx, f.pool, binding, "enablement-desktop", "local", repos, newUUID())
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSessionAs(ctx, f.pool, legacyID, id, "idle", filepath.Join(repos, "project", "one"), "project/one", "Legacy session", "", db.SessionOrigin{SpawningAccount: privAcctA}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET runtime='daemon',kind='agent',engine='claude' WHERE id=$1`, legacyID); err != nil {
		t.Fatal(err)
	}
	records := daemon.AgentRecordsDir(filepath.Join(root, "state"))
	if err := os.MkdirAll(records, 0700); err != nil {
		t.Fatal(err)
	}
	legacyRecord, err := json.Marshal(daemon.AgentRecord{Version: 1, SessionID: legacyID, Repo: "project/one", WorkDir: filepath.Join(repos, "project", "one"), Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(records, legacyID+".json"), legacyRecord, 0600); err != nil {
		t.Fatal(err)
	}
	if err := daemon.StoreDesktopCredential(filepath.Join(root, "state"), daemon.DesktopCredential{DesktopID: id, Token: token}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = []string{"TMUX_TMPDIR=" + filepath.Join(root, "tmux"), "ANTHROPIC_API_KEY=fixture-api-key", "HOME=" + home, "PATH=" + engineDir + ":" + os.Getenv("PATH"), "LANG=C.UTF-8", "TMPDIR=" + os.Getenv("TMPDIR"), daemon.DaemonStateDirEnv + "=" + filepath.Join(root, "state"), "BLERG_RUNNER_SERVER_URL=ws" + strings.TrimPrefix(server.URL, "http") + "/ws/daemon", "BLERG_RUNNER_SERVER_HTTP=" + server.URL, "BLERG_RUNNER_DAEMON_TOKEN=" + token, "BLERG_RUNNER_DESKTOP_ID=" + id, "BLERG_RUNNER_REPOS_ROOT=" + repos, "BLERG_RUNNER_DAEMON_NAME=enablement-desktop", "BLERG_RUNNER_CLAUDE_USAGE=0", "BLERG_RUNNER_SANDBOX_NETWORK=", "BLERG_RUNNER_SANDBOX_IMAGE=" + os.Getenv("BLERG_RUNNER_SANDBOX_IMAGE")}
	log, err := os.Create(filepath.Join(root, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cancel()
			<-done
		}
		rawLog, _ := os.ReadFile(filepath.Join(root, "daemon.log"))
		if strings.Contains(string(rawLog), "WARNING: DATA RACE") {
			t.Error("race in actual daemon process", string(rawLog))
		}
		if t.Failed() {
			raw, _ := os.ReadFile(filepath.Join(root, "daemon.log"))
			t.Logf("owned daemon log: %s", raw)
		}
	}()
	deadline := time.After(40 * time.Second)
	for {
		dc := f.hub.GetDaemon(id)
		if dc != nil {
			if !dc.WorkspaceCapabilities.Supports("host", true) || (!legacyOnly && !dc.WorkspaceCapabilities.Supports("sandbox", true)) {
				raw, _ := json.Marshal(dc.WorkspaceCapabilities)
				t.Fatalf("PRODUCTION_HELLO: actual daemon advertised %s, expected verified workspace and shell", raw)
			}
			break
		}
		select {
		case <-deadline:
			raw, _ := os.ReadFile(filepath.Join(root, "daemon.log"))
			t.Fatalf("actual daemon did not register: %s", raw)
		case <-time.After(10 * time.Millisecond):
		}
	}
	request := func(t *testing.T, method, path string, body any, want int) []byte {
		var input io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			input = bytes.NewReader(raw)
		}
		r, err := http.NewRequestWithContext(ctx, method, server.URL+path, input)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctA, "session.start", "account.manage"))
		r.Header.Set("Origin", server.URL)
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode == http.StatusConflict && want == http.StatusOK && method == http.MethodGet && strings.Contains(path, "/workspace?") {
			return raw // lifecycle or directory-change refusal; caller retries under its bounded wait
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s: HTTP %d want %d: %s", method, path, response.StatusCode, want, raw)
		}
		return raw
	}
	var sessions []string
	defer func() {
		for _, sid := range sessions {
			call, stop := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			_ = f.api.stopSessionWithDelivery(call, sid, systemEnd(db.EndReasonStoppedByUser), false)
			stop()
		}
	}()

	t.Run("legacy", func(t *testing.T) {
		sessions = append(sessions, legacyID)
		p6EnabledWait(t, 30*time.Second, "legacy recovery binding", func() bool {
			_, binding, e := db.GetWorkspaceRuntime(ctx, f.pool, legacyID)
			return e == nil && binding != nil
		})
		for _, op := range []string{"list", "search", "status"} {
			p6EnabledWait(t, 10*time.Second, "legacy "+op, func() bool {
				raw := request(t, "GET", "/api/sessions/"+legacyID+"/workspace?op="+op+"&filter=README", nil, 200)
				var reply struct {
					Result protocol.WorkspaceExplorerResult
				}
				return json.Unmarshal(raw, &reply) == nil && reply.Result.OK && (op != "search" || len(reply.Result.Entries) == 1)
			})
		}
		for attempt := 0; attempt < 2; attempt++ {
			if attempt > 0 {
				previous := f.hub.GetDaemon(id)
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("owned daemon did not stop")
				}
				environment := cmd.Env
				cmd = exec.CommandContext(ctx, binary)
				cmd.Env, cmd.Stdout, cmd.Stderr = environment, log, log
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				done = make(chan error, 1)
				restarted := cmd
				go func() { done <- restarted.Wait() }()
				p6EnabledWait(t, 40*time.Second, "legacy recovery after daemon restart", func() bool {
					current := f.hub.GetDaemon(id)
					row, e := db.GetSession(ctx, f.pool, legacyID)
					return e == nil && row != nil && row.Status != "disconnected" && row.Status != "starting" && current != nil && current != previous && f.hub.FindDaemonForSession(legacyID) == current
				})
			}
			raw := request(t, "GET", "/api/sessions/"+legacyID+"/shell-capability", nil, 200)
			var capability struct{ Available bool }
			if json.Unmarshal(raw, &capability) != nil || !capability.Available {
				t.Fatalf("legacy shell unavailable: %s", raw)
			}
			request(t, "POST", "/api/sessions/"+legacyID+"/shells", nil, http.StatusPreconditionRequired)
			raw = request(t, "POST", "/api/sessions/"+legacyID+"/shell-consent/challenge", nil, 201)
			var challenge protocol.HostShellConsent
			if err := json.Unmarshal(raw, &challenge); err != nil {
				t.Fatal(err)
			}
			if challenge.Cwd != filepath.Join(repos, "project", "one") {
				t.Fatal("legacy shell escaped project root")
			}
			request(t, "POST", "/api/sessions/"+legacyID+"/shell-consent/accept", map[string]any{"challenge_id": challenge.ChallengeID, "accept": true}, 200)
			raw = request(t, "POST", "/api/sessions/"+legacyID+"/shells", nil, 201)
			var shell protocol.OwnerShellInfo
			if err := json.Unmarshal(raw, &shell); err != nil || shell.Handle == "" {
				t.Fatalf("legacy shell open: %s %v", raw, err)
			}
			if attempt == 0 {
				actor := ownershell.Actor{Kind: "human", Owner: privAcctA, Login: testSID}
				description, e := f.api.shellRuntime.describe(ctx, legacyID)
				if e != nil {
					t.Fatal(e)
				}
				attachment, e := f.api.shells.Attach(ctx, actor, shell.Handle, false)
				if e != nil {
					t.Fatal(e)
				}
				if e = f.api.shells.InputFrame(ctx, actor, shell.Handle, attachment.ID, 1, []byte("exit\n"), true); e != nil {
					t.Fatal(e)
				}
				p6EnabledWait(t, 10*time.Second, "host identity rotation after shell exit", func() bool {
					next, e := f.api.shellRuntime.describe(ctx, legacyID)
					return e == nil && next.Binding.Connection != description.Binding.Connection
				})
				f.api.shells.Tick(ctx)
				raw = request(t, "POST", "/api/sessions/"+legacyID+"/shell-consent/challenge", nil, 201)
				if err := json.Unmarshal(raw, &challenge); err != nil {
					t.Fatal(err)
				}
				request(t, "POST", "/api/sessions/"+legacyID+"/shell-consent/accept", map[string]any{"challenge_id": challenge.ChallengeID, "accept": true}, 200)
				raw = request(t, "POST", "/api/sessions/"+legacyID+"/shells", nil, 201)
				if err := json.Unmarshal(raw, &shell); err != nil || shell.Handle == "" {
					t.Fatal("fresh consent did not reopen host shell")
				}
			}
			request(t, "DELETE", "/api/shells/"+shell.Handle, nil, 204)
		}
		request(t, "DELETE", "/api/sessions/"+legacyID, nil, 204)
		p6EnabledWait(t, 10*time.Second, "legacy session stop", func() bool {
			row, e := db.GetSession(ctx, f.pool, legacyID)
			return e == nil && row != nil && row.EndedAt != nil
		})
	})
	if legacyOnly {
		return
	}
	for _, runtime := range []string{"daemon", "docker"} {
		t.Run(runtime, func(t *testing.T) {
			dc := f.hub.GetDaemon(id)
			raw := request(t, "GET", "/api/workspace/discovery?daemon_id="+id+"&path=project", nil, 200)
			var listed struct{ Page protocol.DiscoveryPage }
			if err := json.Unmarshal(raw, &listed); err != nil {
				t.Fatal(err)
			}
			selection := protocol.WorkspaceSelectionRequest{DaemonID: id, OwnershipRevision: 1, ConnectionGeneration: dc.DesktopGeneration, RootIdentity: listed.Page.RootIdentity, Folder: &protocol.SelectionRef{Path: listed.Page.Root.Path, SourceIdentity: listed.Page.Root.SourceIdentity}}
			runtimeName := "host"
			if runtime == "docker" {
				runtimeName = "sandbox"
			}
			raw = request(t, "POST", "/api/workspace/preview", map[string]any{"runtime": runtimeName, "selection": selection, "expand": true}, 200)
			var expanded struct {
				Selection protocol.WorkspaceSelectionRequest
			}
			if err := json.Unmarshal(raw, &expanded); err != nil {
				t.Fatal(err)
			}
			if len(expanded.Selection.Members) != 2 || expanded.Selection.Folder == nil {
				t.Fatalf("not an expanded two-member folder: %s", raw)
			}
			baseline := map[string]string{}
			for _, member := range []string{"one", "two"} {
				baseline[member] = p6EnabledGit(t, filepath.Join(repos, "project", member), "rev-parse", "HEAD")
			}
			raw = request(t, "POST", "/api/sessions", map[string]any{"selection": expanded.Selection, "runtime": runtime, "kind": "agent", "engine": "claude", "title": "Enabled folder"}, 202)
			var launch struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(raw, &launch); err != nil {
				t.Fatal(err)
			}
			if launch.SessionID == "" {
				t.Fatalf("no session id: %s", raw)
			}
			sid := launch.SessionID
			sessions = append(sessions, sid)
			p6EnabledWait(t, 60*time.Second, "prepared runtime", func() bool {
				_, binding, e := db.GetWorkspaceRuntime(ctx, f.pool, sid)
				row, rowErr := db.GetSession(ctx, f.pool, sid)
				return e == nil && binding != nil && rowErr == nil && row != nil && row.Status != "starting" && f.hub.FindDaemonForSession(sid) == dc
			})
			ws, err := db.GetSessionWorkspace(ctx, f.pool, sid)
			if err != nil || ws == nil || ws.Manifest == nil || len(ws.Manifest.Members) != 2 || ws.Manifest.Kind != "folder" {
				t.Fatal("persisted folder missing", err)
			}
			for _, op := range []string{"list", "search", "status"} {
				query := "?op=" + op
				if op == "search" {
					query += "&filter=README.md"
				}
				if op == "status" {
					query += "&path=" + ws.Manifest.Members[0].Path + "&member=" + ws.Manifest.Members[0].ID
				}
				var response struct {
					Result protocol.WorkspaceExplorerResult
				}
				// Startup may still create engine support files. Refusal of a
				// changing directory is correct; wait for a stable live snapshot.
				p6EnabledWait(t, 10*time.Second, op+" stable explorer response", func() bool {
					raw = request(t, "GET", "/api/sessions/"+sid+"/workspace"+query, nil, 200)
					if err := json.Unmarshal(raw, &response); err != nil {
						t.Fatal(err)
					}
					return response.Result.OK
				})
				if op == "search" && !strings.Contains(string(raw), "README.md") {
					t.Fatal("real member search missing", string(raw))
				}
			}
			if runtime == "daemon" {
				raw = request(t, "POST", "/api/sessions/"+sid+"/shell-consent/challenge", nil, 201)
				var challenge protocol.HostShellConsent
				if err := json.Unmarshal(raw, &challenge); err != nil {
					t.Fatal(err)
				}
				request(t, "POST", "/api/sessions/"+sid+"/shell-consent/accept", map[string]any{"challenge_id": challenge.ChallengeID, "accept": true}, 200)
			}
			raw = request(t, "POST", "/api/sessions/"+sid+"/shells", nil, 201)
			var shell protocol.OwnerShellInfo
			if err := json.Unmarshal(raw, &shell); err != nil || shell.Handle == "" {
				t.Fatalf("no real P5 shell: %s %v", raw, err)
			}
			request(t, "DELETE", "/api/shells/"+shell.Handle, nil, 204)
			request(t, "POST", "/api/runner/sessions/"+sid+"/message", map[string]string{"text": "commit both members", "client_id": newUUID()}, 202)
			p6EnabledWait(t, 90*time.Second, "turn sync and member outcomes", func() bool {
				w, e := db.GetSessionWorkspace(ctx, f.pool, sid)
				if e != nil || w == nil || len(w.Results) != 2 {
					return false
				}
				for i, m := range w.Results {
					if m.Head == "" || m.Pending || m.SyncError != "" || m.Head == baseline[ws.Manifest.Members[i].Path] {
						return false
					}
				}
				return true
			})
			for _, member := range []string{"one", "two"} {
				ref := "HEAD"
				if runtime == "docker" {
					ref = "wip/" + sid
				}
				if !strings.Contains(p6EnabledGit(t, filepath.Join(repos, "project", member), "show", ref+":synced.txt"), "enabled sync") {
					t.Fatal("member sync omitted committed work")
				}
			}
			request(t, "DELETE", "/api/sessions/"+sid, nil, 204)
			p6EnabledWait(t, 90*time.Second, "durable stop receipt", func() bool {
				row, e := db.GetSession(ctx, f.pool, sid)
				if e != nil || row == nil || row.EndedAt == nil {
					return false
				}
				var n int
				return f.pool.QueryRow(ctx, `SELECT count(*) FROM local_workspace_authority WHERE session_id=$1 AND final_digest IS NOT NULL`, sid).Scan(&n) == nil && n == 1
			})
		})
	}

}

const p6EnabledEngine = `#!/bin/sh
set -e
if [ "$1" = "--version" ]; then echo '2.1.47 (Claude Code)';exit;fi
if [ "$1" = "--help" ];then echo '--include-partial-messages --forward-subagent-text';exit;fi
base="$PWD"
if [ -d "$base/../two/.git" ];then base="$base/..";fi
for member in one two;do
 cd "$base/$member"
 printf 'enabled sync\n' >> synced.txt
 git add synced.txt
 git -c user.name=Example -c user.email=developer@example.com commit -qm enabled
 done
cat <<'JSON'
{"type":"system","subtype":"init","session_id":"enabled-fixture"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Committed both members."}]}}
{"type":"result","subtype":"success","is_error":false,"session_id":"enabled-fixture","result":"Committed both members."}
JSON
`

func p6EnabledGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s %v", args[0], out, err)
	}
	return strings.TrimSpace(string(out))
}
func p6EnabledWait(t *testing.T, timeout time.Duration, what string, ready func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	for !ready() {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
