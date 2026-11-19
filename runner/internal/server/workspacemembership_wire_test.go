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
	"github.com/blerglab/blerg-ai/contracts/shellpolicy"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/keepertest"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

// membershipStack is the real path: the HTTP routes, the real server over a
// websocket and the shipped daemon binary with its real containment and journal.
type membershipStack struct {
	t      *testing.T
	ctx    context.Context
	f      *privFixture
	server *httptest.Server
	id     string
	repos  string
	binary string
	env    []string
	log    *os.File
	cmd    *exec.Cmd
	done   chan error
}

func newMembershipStack(t *testing.T, sandboxImage string, realEngine ...string) *membershipStack {
	t.Helper()
	return newMembershipStackWithKeeper(t, sandboxImage, false, realEngine...)
}

func newMembershipStackWithKeeper(t *testing.T, sandboxImage string, withKeeper bool, realEngine ...string) *membershipStack {
	t.Helper()
	binary := os.Getenv("BLERG_HOST_HELPER_TEST_BINARY")
	if binary == "" {
		t.Skip("build the production daemon and supply BLERG_HOST_HELPER_TEST_BINARY")
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	f := newPrivFixture(t)
	id := newUUID()
	token := "bdt_" + strings.Repeat("e", 64)
	binding := desktopauth.Binding{DesktopID: id, OwnerID: privAcctA, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/desktop-tokens/validate":
			_ = json.NewEncoder(w).Encode(binding)
		case "/internal/shell/authorize":
			// The shipped daemon uses persistent consent, which requires a
			// fresh, exact-login authorization rather than a legacy policy.
			var request shellpolicy.AuthorizationRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error("decode shell authorization request:", err)
				http.Error(w, "invalid authorization request", http.StatusBadRequest)
				return
			}
			if r.Header.Get("X-Internal-Key") != "fixture-internal-key" || request.Owner != privAcctA || request.Login != testSID || request.Nonce == "" {
				t.Error("shell authorization request lacks expected authority")
				http.Error(w, "invalid authorization authority", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(shellpolicy.AuthorizationAllowed{
				ShellPolicy:          shellpolicy.ShellPolicy{Version: 1, Revision: 1, Enabled: true},
				AuthorizationVersion: 2, Outcome: "allow", Owner: request.Owner,
				Login: request.Login, Nonce: request.Nonce, LoginExpiresAt: binding.ExpiresAt.Unix(),
			})
		case "/internal/shell/control":
			w.Header().Set("Content-Type", "application/x-ndjson")
			// A live Core stream supplies fresh snapshots; silence correctly
			// makes the shell manager unavailable after three seconds.
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				if err := json.NewEncoder(w).Encode(map[string]any{"policy": protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}}); err != nil {
					t.Log("write shell control snapshot:", err)
					return
				}
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
	t.Cleanup(func() { cancel(); core.Close() })
	f.api.coreURL, f.api.coreInternalKey = core.URL, "fixture-internal-key"
	if err := f.api.StartRuntimeShellChannel(ctx); err != nil {
		t.Fatal(err)
	}
	f.api.membershipSweepEvery = 300 * time.Millisecond
	go f.api.RunWorkspaceMembership(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", f.hub.ServeDaemon(spawnTokenDaemonTok, f.pool))
	f.api.RegisterWorkspaceDiscoveryRoutes(mux)
	f.api.RegisterWorkspaceFileRoutes(mux)
	f.api.RegisterWorkspaceMembershipRoutes(mux)
	f.api.RegisterShellRoutes(mux)
	mux.HandleFunc("POST /api/sessions", f.api.HandlePostSessions)
	mux.HandleFunc("DELETE /api/sessions/{id}", f.api.HandleDeleteSession)
	mux.HandleFunc("GET /api/runner/sessions/{id}", f.api.HandleRunnerStatus)
	mux.HandleFunc("GET /api/runner/sessions/{id}/events", f.api.HandleRunnerEvents)
	mux.HandleFunc("POST /api/runner/sessions/{id}/message", f.api.HandleRunnerMessage)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	root := t.TempDir()
	home, repos := filepath.Join(root, "home"), filepath.Join(root, "repos")
	engines := filepath.Join(root, "engines")
	for _, path := range []string{home, repos, filepath.Join(root, "tmux"), engines} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if len(realEngine) > 0 {
		if err := os.Symlink(realEngine[0], filepath.Join(engines, "claude")); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(filepath.Join(engines, "claude"), []byte(p6EnabledEngine), 0700); err != nil {
		t.Fatal(err)
	}
	if err := db.WithDesktopFence(ctx, f.pool, id, func(ctx context.Context) error {
		return db.BindDesktopConnection(ctx, f.pool, binding, "membership-desktop", "local", repos, newUUID())
	}); err != nil {
		t.Fatal(err)
	}
	if err := daemon.StoreDesktopCredential(filepath.Join(root, "state"), daemon.DesktopCredential{DesktopID: id, Token: token}); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(root, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	s := &membershipStack{t: t, ctx: ctx, f: f, server: server, id: id, repos: repos, binary: binary, log: log}
	s.env = []string{"TMUX_TMPDIR=" + filepath.Join(root, "tmux"), "ANTHROPIC_API_KEY=fixture-api-key", "HOME=" + home, "PATH=" + engines + ":" + os.Getenv("PATH"), "LANG=C.UTF-8", "TMPDIR=" + os.Getenv("TMPDIR"), daemon.DaemonStateDirEnv + "=" + filepath.Join(root, "state"), "BLERG_RUNNER_SERVER_URL=ws" + strings.TrimPrefix(server.URL, "http") + "/ws/daemon", "BLERG_RUNNER_SERVER_HTTP=" + server.URL, "BLERG_RUNNER_DAEMON_TOKEN=" + token, "BLERG_RUNNER_DESKTOP_ID=" + id, "BLERG_RUNNER_REPOS_ROOT=" + repos, "BLERG_RUNNER_DAEMON_NAME=membership-desktop", "BLERG_RUNNER_CLAUDE_USAGE=0", "BLERG_RUNNER_SANDBOX_NETWORK=", "BLERG_RUNNER_SANDBOX_IMAGE=" + sandboxImage}
	if len(realEngine) > 0 {
		s.env[1] = "ANTHROPIC_API_KEY=" + os.Getenv("ANTHROPIC_API_KEY")
	}
	t.Cleanup(func() {
		s.stop()
		raw, _ := os.ReadFile(log.Name())
		if strings.Contains(string(raw), "WARNING: DATA RACE") {
			t.Error("race in actual daemon process", string(raw))
		}
		if t.Failed() {
			t.Logf("owned daemon log: %s", raw)
		}
		log.Close()
	})
	if withKeeper {
		if err := keepertest.Run(t, binary, filepath.Join(root, "state"), "shell-keeper", "init"); err != nil {
			t.Fatal("initialize membership shell keeper:", err)
		}
		keeper := keepertest.Start(t, binary, filepath.Join(root, "state"))
		t.Cleanup(func() {
			if t.Failed() {
				if log, ok := keeper.Cmd.Stderr.(*os.File); ok {
					raw, err := os.ReadFile(log.Name())
					if err != nil {
						t.Log("read owned keeper log:", err)
					} else {
						t.Logf("owned keeper log: %s", raw)
					}
				}
			}
		})
	}
	s.start()
	return s
}

func (s *membershipStack) start() {
	s.t.Helper()
	previous := s.f.hub.GetDaemon(s.id)
	s.cmd = exec.CommandContext(s.ctx, s.binary)
	s.cmd.Env, s.cmd.Stdout, s.cmd.Stderr = s.env, s.log, s.log
	if err := s.cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	s.done = make(chan error, 1)
	cmd, done := s.cmd, s.done
	go func() { done <- cmd.Wait() }()
	p6EnabledWait(s.t, 40*time.Second, "desktop connection", func() bool {
		dc := s.f.hub.GetDaemon(s.id)
		return dc != nil && dc != previous
	})
}

func (s *membershipStack) stop() {
	if s.cmd == nil {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(15 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
	s.cmd = nil
}

func (s *membershipStack) request(method, path string, body any, want int) []byte {
	s.t.Helper()
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		input = bytes.NewReader(raw)
	}
	r, err := http.NewRequestWithContext(s.ctx, method, s.server.URL+path, input)
	if err != nil {
		s.t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+s.f.tok(s.t, "human", privAcctA, "session.start", "account.manage"))
	r.Header.Set("Origin", s.server.URL)
	response, err := s.server.Client().Do(r)
	if err != nil {
		s.t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	if want != 0 && response.StatusCode != want {
		s.t.Fatalf("%s %s: HTTP %d want %d: %s", method, path, response.StatusCode, want, raw)
	}
	return raw
}

func (s *membershipStack) repository(parts ...string) protocol.SelectionRef {
	s.t.Helper()
	dir := filepath.Join(append([]string{s.repos}, parts...)...)
	if err := os.MkdirAll(dir, 0700); err != nil {
		s.t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Example"}, {"config", "user.email", "developer@example.com"}} {
		p6EnabledGit(s.t, dir, args...)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("real "+parts[len(parts)-1]+" member"), 0600); err != nil {
		s.t.Fatal(err)
	}
	p6EnabledGit(s.t, dir, "add", "README.md")
	p6EnabledGit(s.t, dir, "commit", "-qm", "initial")
	return s.reference(parts...)
}

func (s *membershipStack) reference(parts ...string) protocol.SelectionRef {
	s.t.Helper()
	parent := strings.Join(parts[:len(parts)-1], "/")
	raw := s.request("GET", "/api/workspace/discovery?daemon_id="+s.id+"&path="+parent, nil, 200)
	var listed struct{ Page protocol.DiscoveryPage }
	if err := json.Unmarshal(raw, &listed); err != nil {
		s.t.Fatal(err)
	}
	name := parts[len(parts)-1]
	for _, entry := range listed.Page.Entries {
		if entry.Name == name {
			return protocol.SelectionRef{Path: entry.Path, SourceIdentity: entry.SourceIdentity}
		}
	}
	s.t.Fatalf("no %s in %s", name, raw)
	return protocol.SelectionRef{}
}

func (s *membershipStack) selection(refs ...protocol.SelectionRef) protocol.WorkspaceSelectionRequest {
	s.t.Helper()
	raw := s.request("GET", "/api/workspace/discovery?daemon_id="+s.id, nil, 200)
	var listed struct{ Page protocol.DiscoveryPage }
	if err := json.Unmarshal(raw, &listed); err != nil {
		s.t.Fatal(err)
	}
	dc := s.f.hub.GetDaemon(s.id)
	return protocol.WorkspaceSelectionRequest{DaemonID: s.id, OwnershipRevision: 1, ConnectionGeneration: dc.DesktopGeneration, RootIdentity: listed.Page.RootIdentity, Members: refs}
}

func (s *membershipStack) launch(runtimeName string, refs ...protocol.SelectionRef) string {
	s.t.Helper()
	runtime := map[string]string{"daemon": "host", "docker": "sandbox"}[runtimeName]
	raw := s.request("POST", "/api/workspace/preview", map[string]any{"runtime": runtime, "selection": s.selection(refs...)}, 200)
	var preview struct {
		Selection protocol.WorkspaceSelectionRequest
	}
	if err := json.Unmarshal(raw, &preview); err != nil {
		s.t.Fatal(err)
	}
	raw = s.request("POST", "/api/sessions", map[string]any{"selection": preview.Selection, "runtime": runtimeName, "kind": "agent", "engine": "claude", "title": "Live membership"}, 202)
	var launch struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &launch); err != nil || launch.SessionID == "" {
		s.t.Fatalf("no session: %s", raw)
	}
	s.waitIdle(launch.SessionID)
	return launch.SessionID
}

func (s *membershipStack) waitIdle(sid string) {
	s.t.Helper()
	status := ""
	defer func() {
		if s.t.Failed() {
			s.t.Logf("session status %q", status)
			rows, err := s.f.pool.Query(context.Background(), `SELECT kind,to_char(ts,'HH24:MI:SS.MS')||' '||left(payload::text,120) FROM agent_events WHERE session_id=$1 AND kind NOT IN ('assistant_text','mcp_status','start_stage') ORDER BY seq DESC LIMIT 30`, sid)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var kind, payload string
					if rows.Scan(&kind, &payload) == nil {
						s.t.Logf("event %s %s", kind, payload)
					}
				}
			} else {
				s.t.Log(err)
			}
		}
	}()
	p6EnabledWait(s.t, 90*time.Second, "idle runtime", func() bool {
		_, binding, e := db.GetWorkspaceRuntime(s.ctx, s.f.pool, sid)
		row, rowErr := db.GetSession(s.ctx, s.f.pool, sid)
		if row != nil {
			status = row.Status
		}
		return e == nil && binding != nil && rowErr == nil && row != nil && row.Status == "idle"
	})
}

func (s *membershipStack) view(sid string) membershipView {
	s.t.Helper()
	var view membershipView
	if err := json.Unmarshal(s.request("GET", "/api/sessions/"+sid+"/workspace/repositories", nil, 200), &view); err != nil {
		s.t.Fatal(err)
	}
	return view
}

func (s *membershipStack) add(sid string, revision int64, ref protocol.SelectionRef) string {
	s.t.Helper()
	op := newUUID()
	s.request("POST", "/api/sessions/"+sid+"/workspace/repositories", map[string]any{"operation_id": op, "kind": "add", "expected_revision": revision, "selection": s.selection(ref)}, 202)
	return op
}

func (s *membershipStack) waitPhase(op, phase string) *db.MembershipOperation {
	s.t.Helper()
	var last *db.MembershipOperation
	defer func() {
		if s.t.Failed() && last != nil {
			s.t.Logf("operation phase=%s waiting=%q refusal=%q", last.Phase, last.Waiting, last.Refusal)
		}
	}()
	p6EnabledWait(s.t, 60*time.Second, "membership "+phase, func() bool {
		var err error
		last, err = db.GetMembershipOperation(s.ctx, s.f.pool, op)
		if err != nil {
			s.t.Fatal(err)
		}
		if last != nil && last.Phase == db.MembershipRefused && phase != db.MembershipRefused {
			s.t.Fatalf("refused: %s", last.Refusal)
		}
		return last != nil && last.Phase == phase
	})
	return last
}

func (s *membershipStack) explorer(sid, query string) protocol.WorkspaceExplorerResult {
	s.t.Helper()
	var reply struct {
		Result protocol.WorkspaceExplorerResult
	}
	var last []byte
	defer func() {
		if s.t.Failed() {
			s.t.Logf("last explorer reply: %s", last)
		}
	}()
	p6EnabledWait(s.t, 45*time.Second, "explorer "+query, func() bool {
		last = s.request("GET", "/api/sessions/"+sid+"/workspace?"+query, nil, 0)
		return json.Unmarshal(last, &reply) == nil && reply.Result.OK
	})
	return reply.Result
}

func TestMembershipHostAddRealWire(t *testing.T) {
	s := newMembershipStack(t, "")
	one, two := s.repository("project", "one"), s.repository("project", "two")
	three := s.repository("elsewhere", "three")
	sid := s.launch("daemon", one, two)
	launch, err := db.GetSessionLaunchManifest(s.ctx, s.f.pool, sid)
	if err != nil {
		t.Fatal(err)
	}
	view := s.view(sid)
	if view.Revision != 0 || view.Unsupported != "" || view.AddBlocked != "" {
		t.Fatalf("view %+v", view)
	}

	op := s.add(sid, 0, three)
	s.waitPhase(op, db.MembershipInstalled)
	w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil || w.Manifest == nil || len(w.Manifest.Members) != 3 || w.Manifest.Revision != 1 || w.Manifest.Members[2].Path != "three" {
		t.Fatalf("manifest %+v %v", w, err)
	}
	for i, member := range launch.Members {
		if w.Manifest.Members[i] != member {
			t.Fatalf("existing member changed: %+v", w.Manifest.Members[i])
		}
	}
	unchanged, err := db.GetSessionLaunchManifest(s.ctx, s.f.pool, sid)
	if err != nil || unchanged.Digest != launch.Digest {
		t.Fatal("launch manifest changed", err)
	}
	_, binding, err := db.GetWorkspaceRuntime(s.ctx, s.f.pool, sid)
	if err != nil || binding.ManifestDigest != w.Manifest.Digest {
		t.Fatalf("binding %+v %v", binding, err)
	}

	// The picker's named folder is listed and searched like any member.
	listing := s.explorer(sid, "op=list")
	found := false
	for _, entry := range listing.Entries {
		found = found || entry.Name == "three" && entry.Repository
	}
	if !found {
		t.Fatalf("explorer lacks the new member: %+v", listing)
	}
	if search := s.explorer(sid, "op=search&filter=README.md"); !strings.Contains(entriesText(search), "three/README.md") {
		t.Fatalf("search lacks the new member: %+v", search)
	}

	// A duplicate is refused before it is journaled.
	if raw := s.request("POST", "/api/sessions/"+sid+"/workspace/repositories", map[string]any{"operation_id": newUUID(), "kind": "add", "expected_revision": 1, "selection": s.selection(three)}, http.StatusConflict); !strings.Contains(string(raw), "already in the workspace") {
		t.Fatalf("duplicate: %s", raw)
	}

	// Turn-end delivery reports the new member through the same binding.
	s.waitIdle(sid)
	s.request("POST", "/api/runner/sessions/"+sid+"/message", map[string]string{"text": "commit both members", "client_id": newUUID()}, 202)
	p6EnabledWait(t, 60*time.Second, "three member outcomes", func() bool {
		current, e := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
		return e == nil && len(current.Results) == 3
	})

	// A restart must recover the revision, not the launch selection.
	s.waitIdle(sid)
	s.stop()
	s.start()
	p6EnabledWait(t, 60*time.Second, "resumed revision", func() bool {
		_, b, e := db.GetWorkspaceRuntime(s.ctx, s.f.pool, sid)
		row, rowErr := db.GetSession(s.ctx, s.f.pool, sid)
		return e == nil && b != nil && b.ManifestDigest == w.Manifest.Digest && rowErr == nil && row.Status == "idle"
	})
	if listing := s.explorer(sid, "op=list"); !strings.Contains(entriesText(listing), "three") {
		t.Fatalf("restart lost the member: %+v", listing)
	}
}

func (s *membershipStack) remove(sid string, revision int64, member string) string {
	s.t.Helper()
	op := newUUID()
	s.request("POST", "/api/sessions/"+sid+"/workspace/repositories", map[string]any{"operation_id": op, "kind": "remove", "expected_revision": revision, "member_id": member}, 202)
	return op
}

func TestMembershipHostRemoveRealWire(t *testing.T) {
	s := newMembershipStack(t, "")
	one, two := s.repository("project", "one"), s.repository("project", "two")
	three := s.repository("elsewhere", "three")
	sid := s.launch("daemon", one, two, three)
	w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.repos, "elsewhere", "three")
	head := p6EnabledGit(t, dir, "rev-parse", "HEAD")
	p6EnabledGit(t, dir, "branch", "keep-me")

	// Ruling 1: unfinished work refuses and says why; the member stays.
	if err := os.WriteFile(filepath.Join(dir, "draft.txt"), []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	refused := s.waitPhase(s.remove(sid, 0, w.Manifest.Members[2].ID), db.MembershipRefused)
	if !strings.Contains(refused.Refusal, "unfinished") {
		t.Fatalf("refusal: %s", refused.Refusal)
	}
	if view := s.view(sid); view.Last == nil || !strings.Contains(view.Last.Refusal, "unfinished") {
		t.Fatalf("the row has no reason: %+v", view)
	}
	if err := os.Remove(filepath.Join(dir, "draft.txt")); err != nil {
		t.Fatal(err)
	}

	// Ruling 4: Main works in the first repository of a separately picked workspace.
	anchored := s.waitPhase(s.remove(sid, 0, w.Manifest.Members[0].ID), db.MembershipRefused)
	if !strings.Contains(anchored.Refusal, "working in this repository") {
		t.Fatalf("refusal: %s", anchored.Refusal)
	}

	s.waitIdle(sid)
	s.waitPhase(s.remove(sid, 0, w.Manifest.Members[2].ID), db.MembershipInstalled)
	current, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil || len(current.Manifest.Members) != 2 || current.Manifest.Revision != 1 {
		t.Fatalf("manifest %+v %v", current, err)
	}
	if listing := s.explorer(sid, "op=list"); strings.Contains(entriesText(listing), "three") {
		t.Fatalf("explorer still has the member: %+v", listing)
	}
	// Nothing of the person's is touched.
	if got := p6EnabledGit(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("head moved: %s", got)
	}
	p6EnabledGit(t, dir, "rev-parse", "--verify", "keep-me")

	// The repository can come back, as a new revision.
	s.waitIdle(sid)
	s.waitPhase(s.add(sid, 1, s.reference("elsewhere", "three")), db.MembershipInstalled)
	if listing := s.explorer(sid, "op=list"); !strings.Contains(entriesText(listing), "three") {
		t.Fatalf("explorer lacks the returned member: %+v", listing)
	}
}

func TestMembershipSandboxRemoveRealWire(t *testing.T) {
	if os.Getenv("BLERG_HOST_HELPER_TEST_BINARY") == "" {
		t.Skip("build the production daemon and supply BLERG_HOST_HELPER_TEST_BINARY")
	}
	s := newMembershipStack(t, p6SandboxImage(t, "blerg-runner-sandbox:latest"))
	one, two := s.repository("project", "one"), s.repository("project", "two")
	three := s.repository("elsewhere", "three")
	sid := s.launch("docker", one, two, three)
	defer func() {
		call, stop := context.WithTimeout(context.WithoutCancel(s.ctx), 30*time.Second)
		defer stop()
		_ = s.f.api.stopSessionWithDelivery(call, sid, systemEnd(db.EndReasonStoppedByUser), false)
	}()
	w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil {
		t.Fatal(err)
	}
	base := p6EnabledGit(t, filepath.Join(s.repos, "project", "one"), "rev-parse", "HEAD")
	s.request("POST", "/api/runner/sessions/"+sid+"/message", map[string]string{"text": "commit both members", "client_id": newUUID()}, 202)
	p6EnabledWait(t, 90*time.Second, "delivered results", func() bool {
		current, e := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
		if e != nil || len(current.Results) != 3 {
			return false
		}
		for _, result := range current.Results {
			if result.Pending || result.SyncError != "" || result.MemberID == w.Manifest.Members[0].ID && result.Head == base {
				return false
			}
		}
		return true
	})
	s.waitIdle(sid)
	container := strings.TrimSpace(string(mustOutput(t, "docker", "ps", "-q", "--filter", "label=blerg.runner.workspace-session="+sid)))
	path := "/workspace/" + w.Manifest.Members[2].Path
	mustOutput(t, "docker", "exec", container, "sh", "-c", "touch "+path+"/draft.txt")
	refused := s.waitPhase(s.remove(sid, 0, w.Manifest.Members[2].ID), db.MembershipRefused)
	if !strings.Contains(refused.Refusal, "unfinished") {
		t.Fatalf("refusal: %s", refused.Refusal)
	}
	mustOutput(t, "docker", "exec", container, "rm", path+"/draft.txt")
	s.waitIdle(sid)
	s.waitPhase(s.remove(sid, 0, w.Manifest.Members[2].ID), db.MembershipInstalled)
	for _, entry := range s.explorer(sid, "op=list").Entries {
		if entry.Name == "three" && entry.Repository {
			t.Fatalf("explorer still has the member: %+v", entry)
		}
	}
	// Its checkout stays until someone explicitly disposes of it.
	mustOutput(t, "docker", "exec", container, "test", "-d", path+"/.git")
	// The rest of the workspace still delivers.
	s.waitIdle(sid)
	if raw := s.request("POST", "/api/sessions/"+sid+"/workspace/repositories", map[string]any{"operation_id": newUUID(), "kind": "remove", "expected_revision": 1, "member_id": w.Manifest.Members[0].ID}, http.StatusConflict); !strings.Contains(string(raw), "working in this repository") {
		t.Fatalf("anchor: %s", raw)
	}
}

func (s *membershipStack) sessionRequest(sid, method, path string, body any, want int) []byte {
	s.t.Helper()
	token, err := db.MintSessionToken(s.ctx, s.f.pool, sid, []string{"message"}, time.Hour)
	if err != nil {
		s.t.Fatal(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		s.t.Fatal(err)
	}
	r, err := http.NewRequestWithContext(s.ctx, method, s.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		s.t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	response, err := s.server.Client().Do(r)
	if err != nil {
		s.t.Fatal(err)
	}
	defer response.Body.Close()
	out, err := io.ReadAll(response.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	if response.StatusCode != want {
		s.t.Fatalf("%s %s: HTTP %d want %d: %s", method, path, response.StatusCode, want, out)
	}
	return out
}

// Main asks; only the person's approval of that exact proposal applies it.
func TestMembershipMainRequestRealWire(t *testing.T) {
	s := newMembershipStack(t, "")
	one, two := s.repository("project", "one"), s.repository("project", "two")
	s.repository("elsewhere", "three")
	s.repository("elsewhere", "four")
	sid := s.launch("daemon", one, two)
	route := "/api/sessions/" + sid + "/workspace/repositories"

	request := func(repository string) membershipOperationView {
		var view membershipOperationView
		raw := s.sessionRequest(sid, "POST", route, map[string]any{"operation_id": newUUID(), "kind": "add", "expected_revision": s.view(sid).Revision, "repository": repository}, http.StatusAccepted)
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	denied := request("elsewhere/four")
	if denied.Phase != db.MembershipRequested || denied.RequestedBy != "main" || denied.MemberPath != "four" {
		t.Fatalf("request %+v", denied)
	}
	// Main cannot approve its own request, nor a person another proposal.
	s.sessionRequest(sid, "POST", route+"/"+denied.ID+"/decide", map[string]any{"allow": true, "proposed_digest": denied.Digest}, http.StatusForbidden)
	s.request("POST", route+"/"+denied.ID+"/decide", map[string]any{"allow": true, "proposed_digest": "other"}, http.StatusConflict)
	s.request("POST", route+"/"+denied.ID+"/decide", map[string]any{"allow": false, "proposed_digest": denied.Digest}, 200)
	if w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid); err != nil || len(w.Manifest.Members) != 2 {
		t.Fatal("denied change applied", err)
	}

	approved := request("elsewhere/three")
	s.request("POST", route+"/"+approved.ID+"/decide", map[string]any{"allow": true, "proposed_digest": approved.Digest}, 200)
	done := s.waitPhase(approved.ID, db.MembershipInstalled)
	if done.ApprovedBy != privAcctA || done.RequestedBy != "main" {
		t.Fatalf("approval not recorded: %+v", done)
	}

	// Someone else's session and a person who is not the owner see nothing.
	other := s.f.tok(t, "human", "stranger", "session.start")
	r, err := http.NewRequestWithContext(s.ctx, "GET", s.server.URL+route, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+other)
	response, err := s.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("stranger read: HTTP %d", response.StatusCode)
	}
}

// An open shell holds the change as pending; closing it lets the change through.
// An old peer has no affirmative support and the change waits for an update.
func TestMembershipWaitsForShellAndOldPeerRealWire(t *testing.T) {
	s := newMembershipStackWithKeeper(t, "", true)
	// The keeper negotiates its own authority after the agent websocket opens.
	// Do not launch a workspace against the provisional startup link.
	p6EnabledWait(t, 20*time.Second, "keeper authority refresh", func() bool {
		dc := s.f.hub.GetDaemon(s.id)
		return dc != nil && dc.refreshedShellLink.Load() != nil
	})
	// The first hello can precede the keeper's native capability probe. The
	// refreshed link proves that probe completed; reconnect to negotiate its
	// verified capabilities before this membership test creates a workspace.
	previous := s.f.hub.GetDaemon(s.id)
	if !previous.WorkspaceCapabilities.SupportsPersistence("host") {
		previous.desktopClose()
		p6EnabledWait(t, 20*time.Second, "persistent host capability negotiation", func() bool {
			dc := s.f.hub.GetDaemon(s.id)
			return dc != nil && dc != previous && dc.WorkspaceCapabilities.SupportsPersistence("host")
		})
	}
	one, two := s.repository("project", "one"), s.repository("project", "two")
	three := s.repository("elsewhere", "three")
	sid := s.launch("daemon", one, two)
	route := "/api/sessions/" + sid + "/workspace/repositories"

	lastReply, lastErr := s.f.api.shellRuntime.describe(s.ctx, sid)
	t.Cleanup(func() {
		if t.Failed() {
			reply, err := lastReply, lastErr
			t.Logf("shell readiness: error=%v code=%s live=%v version=%d server=%+v runtime=%+v", err, reply.ErrorCode, reply.Binding.Live, reply.Binding.ShellLink.Version, reply.Binding.ServerCapabilities, reply.Binding.RuntimeCapabilities)
		}
	})
	// Agent admission does not imply the independent keeper handshake and
	// workspace registration have finished. Await actual shell resolution.
	p6EnabledWait(t, 20*time.Second, "host shell keeper registration", func() bool {
		lastReply, lastErr = s.f.api.shellRuntime.describe(s.ctx, sid)
		binding, err := (shellResolver{s.f.api, s.f.api.shellRuntime}).Resolve(s.ctx, sid)
		return err == nil && binding.Live && binding.ShellLink.Version == 2
	})

	raw := s.request("POST", "/api/sessions/"+sid+"/shell-consent/challenge", nil, 201)
	var challenge protocol.HostShellConsent
	if err := json.Unmarshal(raw, &challenge); err != nil {
		t.Fatal(err)
	}
	s.request("POST", "/api/sessions/"+sid+"/shell-consent/accept", map[string]any{"version": 2, "challenge_id": challenge.ChallengeID, "accept": true}, 200)
	var shell protocol.OwnerShellInfo
	if err := json.Unmarshal(s.request("POST", "/api/sessions/"+sid+"/shells", nil, 201), &shell); err != nil {
		t.Fatal(err)
	}
	op := s.add(sid, 0, three)
	p6EnabledWait(t, 20*time.Second, "pending behind the shell", func() bool {
		current, e := db.GetMembershipOperation(s.ctx, s.f.pool, op)
		return e == nil && current.Phase == db.MembershipAuthorized && strings.Contains(current.Waiting, "open shell")
	})
	s.request("DELETE", "/api/shells/"+shell.Handle, nil, 204)
	s.waitPhase(op, db.MembershipInstalled)

	dc := s.f.hub.GetDaemon(s.id)
	dc.WorkspaceCapabilities.Membership = 0
	raw = s.request("POST", route, map[string]any{"operation_id": newUUID(), "kind": "remove", "expected_revision": 1, "member_id": "unknown"}, http.StatusConflict)
	if !strings.Contains(string(raw), "changed") {
		t.Fatalf("stale member: %s", raw)
	}
	w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil {
		t.Fatal(err)
	}
	raw = s.request("POST", route, map[string]any{"operation_id": newUUID(), "kind": "remove", "expected_revision": 1, "member_id": w.Manifest.Members[2].ID}, http.StatusConflict)
	if !strings.Contains(string(raw), "Update Blerg") {
		t.Fatalf("old peer: %s", raw)
	}
}

// BLG-347: zero-member folder -> initialize and commit -> authorized adoption ->
// a child can select it, across a restart, and discovery alone grants nothing.
func TestMembershipEmptyFolderAdoptionRealWire(t *testing.T) {
	s := newMembershipStack(t, "")
	folder := filepath.Join(s.repos, "fresh")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	raw := s.request("GET", "/api/workspace/discovery?daemon_id="+s.id, nil, 200)
	var listed struct{ Page protocol.DiscoveryPage }
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	var ref protocol.SelectionRef
	for _, entry := range listed.Page.Entries {
		if entry.Name == "fresh" {
			ref = protocol.SelectionRef{Path: entry.Path, SourceIdentity: entry.SourceIdentity}
		}
	}
	selection := s.selection()
	selection.Members = []protocol.SelectionRef{}
	selection.Folder = &ref
	raw = s.request("POST", "/api/workspace/preview", map[string]any{"runtime": "host", "selection": selection, "expand": true}, 200)
	var preview struct {
		Selection protocol.WorkspaceSelectionRequest
	}
	if err := json.Unmarshal(raw, &preview); err != nil {
		t.Fatal(err)
	}
	raw = s.request("POST", "/api/sessions", map[string]any{"selection": preview.Selection, "runtime": "daemon", "kind": "agent", "engine": "claude", "title": "Empty folder"}, 202)
	var launch struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &launch); err != nil || launch.SessionID == "" {
		t.Fatalf("no session: %s", raw)
	}
	sid := launch.SessionID
	s.waitIdle(sid)
	if w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid); err != nil || len(w.Manifest.Members) != 0 {
		t.Fatal("not a zero-member folder", err)
	}

	// Git appears in the folder, as an agent or a person would make it.
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Example"}, {"config", "user.email", "developer@example.com"}} {
		p6EnabledGit(t, folder, args...)
	}
	if err := os.WriteFile(filepath.Join(folder, "README.md"), []byte("adopted"), 0600); err != nil {
		t.Fatal(err)
	}
	p6EnabledGit(t, folder, "add", "README.md")
	p6EnabledGit(t, folder, "commit", "-qm", "initial")
	if w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid); err != nil || len(w.Manifest.Members) != 0 {
		t.Fatal("discovery granted membership", err)
	}
	head := p6EnabledGit(t, folder, "rev-parse", "HEAD")
	s.waitPhase(s.add(sid, 0, s.reference("fresh")), db.MembershipInstalled)
	w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil || len(w.Manifest.Members) != 1 || w.Manifest.Members[0].Path != "." {
		t.Fatalf("manifest %+v %v", w, err)
	}
	if got := p6EnabledGit(t, folder, "rev-parse", "HEAD"); got != head {
		t.Fatal("adoption moved the branch")
	}
	child := func() {
		t.Helper()
		next := *w.Manifest
		next.LaunchGeneration = newUUID()
		next.Revision = 0
		next.Digest = workspaceselection.Digest(next)
		res, e := s.f.hub.requestWorkspaceDiscovery(s.ctx, privAcctA, s.f.hub.GetDaemon(s.id), protocol.DiscoverWorkspace{Operation: "bind_child", ParentLaunchGeneration: w.Manifest.LaunchGeneration, Manifest: &next})
		if e != nil || !res.OK {
			t.Fatalf("child selection refused: %+v %v", res, e)
		}
	}
	child()
	s.waitIdle(sid)
	s.stop()
	s.start()
	p6EnabledWait(t, 60*time.Second, "resumed adoption", func() bool {
		_, b, e := db.GetWorkspaceRuntime(s.ctx, s.f.pool, sid)
		row, rowErr := db.GetSession(s.ctx, s.f.pool, sid)
		return e == nil && b != nil && b.ManifestDigest == w.Manifest.Digest && rowErr == nil && row.Status == "idle"
	})
	if search := s.explorer(sid, "op=search&filter=README.md"); !strings.Contains(entriesText(search), "README.md") {
		t.Fatalf("search lacks the adopted file: %+v", search)
	}
	child()

	// An adopted folder holds the root, so another repository takes the reserved
	// folder, and workers can select both from the new revision.
	s.waitPhase(s.add(sid, w.Manifest.Revision, s.repository("elsewhere", "second")), db.MembershipInstalled)
	if w, err = db.GetSessionWorkspace(s.ctx, s.f.pool, sid); err != nil || len(w.Manifest.Members) != 2 || w.Manifest.Members[0].Path != "." || w.Manifest.Members[1].Path != ".workspace/second" {
		t.Fatalf("manifest %+v %v", w, err)
	}
	child()
}

func entriesText(result protocol.WorkspaceExplorerResult) string {
	var names []string
	for _, entry := range result.Entries {
		names = append(names, entry.Path)
	}
	return strings.Join(names, " ")
}

func TestMembershipSandboxAddRealWire(t *testing.T) {
	if os.Getenv("BLERG_HOST_HELPER_TEST_BINARY") == "" {
		t.Skip("build the production daemon and supply BLERG_HOST_HELPER_TEST_BINARY")
	}
	s := newMembershipStack(t, p6SandboxImage(t, "blerg-runner-sandbox:latest"))
	one, two := s.repository("project", "one"), s.repository("project", "two")
	three := s.repository("elsewhere", "three")
	four := s.repository("elsewhere", "four")
	if dc := s.f.hub.GetDaemon(s.id); !dc.WorkspaceCapabilities.Supports("sandbox", true) || dc.WorkspaceCapabilities.Membership != protocol.WorkspaceMembershipVersion {
		t.Fatalf("capabilities %+v", dc.WorkspaceCapabilities)
	}
	sid := s.launch("docker", one, two)
	defer func() {
		call, stop := context.WithTimeout(context.WithoutCancel(s.ctx), 30*time.Second)
		defer stop()
		_ = s.f.api.stopSessionWithDelivery(call, sid, systemEnd(db.EndReasonStoppedByUser), false)
	}()

	s.waitPhase(s.add(sid, 0, three), db.MembershipInstalled)
	w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil || len(w.Manifest.Members) != 3 || w.Manifest.Members[2].Path != "three" {
		t.Fatalf("manifest %+v %v", w, err)
	}
	if listing := s.explorer(sid, "op=list"); !strings.Contains(entriesText(listing), "three") {
		t.Fatalf("explorer lacks the new member: %+v", listing)
	}
	if search := s.explorer(sid, "op=search&filter=README.md"); !strings.Contains(entriesText(search), "three/README.md") {
		t.Fatalf("search lacks the new member: %+v", search)
	}

	// The turn-end sync copies the new member like the others, to its own branch.
	s.waitIdle(sid)
	s.request("POST", "/api/runner/sessions/"+sid+"/message", map[string]string{"text": "commit both members", "client_id": newUUID()}, 202)
	p6EnabledWait(t, 90*time.Second, "new member delivery", func() bool {
		current, e := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
		if e != nil || len(current.Results) != 3 {
			return false
		}
		for _, result := range current.Results {
			if result.Pending || result.SyncError != "" {
				return false
			}
		}
		return true
	})
	s.waitIdle(sid)

	// A folder the agent made is its work: the change is refused and nothing is replaced.
	container := strings.TrimSpace(string(mustOutput(t, "docker", "ps", "-q", "--filter", "label=blerg.runner.workspace-session="+sid)))
	if container == "" {
		t.Fatal("no labeled container to reach")
	}
	mustOutput(t, "docker", "exec", container, "sh", "-c", "mkdir /workspace/four && touch /workspace/four/keep")
	refused := s.waitPhase(s.add(sid, 1, four), db.MembershipRefused)
	if !strings.Contains(refused.Refusal, "already exists") {
		t.Fatalf("refusal: %s", refused.Refusal)
	}
	mustOutput(t, "docker", "exec", container, "test", "-f", "/workspace/four/keep")
	if current, e := db.GetSessionWorkspace(s.ctx, s.f.pool, sid); e != nil || current.Manifest.Revision != 1 {
		t.Fatal("refused change moved the revision", e)
	}

	// A daemon restart ends a sandbox session; it must settle every member of the
	// revision, including the one that joined after launch.
	s.stop()
	s.start()
	p6EnabledWait(t, 60*time.Second, "sandbox recovery", func() bool {
		row, e := db.GetSession(s.ctx, s.f.pool, sid)
		return e == nil && row != nil && row.EndedAt != nil
	})
	final, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil || len(final.Results) != 3 {
		t.Fatalf("final results %+v %v", final, err)
	}
}

func mustOutput(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
	return out
}

// TestResolveBaseWhenMemberPathIsRootRealWire reproduces the live failure a desktop Main
// hit after BLG-501's merge: a single-repo install whose configured repos root is the
// checkout itself (BLERG_RUNNER_REPOS_ROOT points directly at the repo, member Path "."),
// not a multi-repo container with the repo as a named subdirectory. Every other
// resolve_base test in internal/discovery used a subdirectory member; this is the first to
// go through the real daemon binary, the real websocket transport and a repos root that is
// itself the repository.
func TestResolveBaseWhenMemberPathIsRootRealWire(t *testing.T) {
	s := newMembershipStack(t, "")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Example"}, {"config", "user.email", "developer@example.com"}} {
		p6EnabledGit(t, s.repos, args...)
	}
	if err := os.WriteFile(filepath.Join(s.repos, "README.md"), []byte("real root member"), 0600); err != nil {
		t.Fatal(err)
	}
	p6EnabledGit(t, s.repos, "add", "README.md")
	p6EnabledGit(t, s.repos, "commit", "-qm", "initial")
	want := p6EnabledGit(t, s.repos, "rev-parse", "main")

	raw := s.request("GET", "/api/workspace/discovery?daemon_id="+s.id, nil, 200)
	var listed struct{ Page protocol.DiscoveryPage }
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Page.Root.Path != "." || listed.Page.Root.Kind != "repository" {
		t.Fatalf("expected the repos root itself to be a repository, got %+v", listed.Page.Root)
	}
	folder := protocol.SelectionRef{Path: listed.Page.Root.Path, SourceIdentity: listed.Page.Root.SourceIdentity}

	// resolve_base reads this member's real on-disk path from the daemon's own bound
	// selection (design §13), never from this collapsed, public manifest Path — so a real
	// bound launch is required, the same as cluster_host_workspace.go always has in hand
	// (the parent host workspace it's resolving --base for).
	sid := s.launch("daemon", folder)
	w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil || len(w.Manifest.Members) != 1 {
		t.Fatalf("manifest %+v %v", w, err)
	}

	dc := s.f.hub.GetDaemon(s.id)
	if dc == nil {
		t.Fatal("no live daemon connection")
	}
	res, err := s.f.hub.requestWorkspaceDiscovery(s.ctx, privAcctA, dc, protocol.DiscoverWorkspace{
		Operation:              "resolve_base",
		ParentLaunchGeneration: w.Manifest.LaunchGeneration,
		Folder:                 &folder,
		Base:                   "main",
	})
	if err != nil {
		t.Fatalf("resolve_base request failed: %v", err)
	}
	if !res.OK {
		t.Fatalf("resolve_base refused: %s (cause=%s)", res.ErrorCode, res.Cause)
	}
	if res.BaseSHA != want {
		t.Fatalf("got BaseSHA %q, want %q", res.BaseSHA, want)
	}
}

// TestResolveBaseRefusalIsLoggedRealWire closes the "journalctl logged nothing" gap: a
// resolve_base refusal (here, a SourceIdentity that doesn't match any member of the real
// bound selection — the same shape a real desktop would see if its workspace binding ever
// drifted from what the daemon currently computes) must leave a real log line with its own
// distinct, fixed cause (design §13 — never the shared, ambiguous "invalid_selection" every
// refusal used to collapse to), in the real daemon process's own log output.
func TestResolveBaseRefusalIsLoggedRealWire(t *testing.T) {
	s := newMembershipStack(t, "")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Example"}, {"config", "user.email", "developer@example.com"}} {
		p6EnabledGit(t, s.repos, args...)
	}
	p6EnabledGit(t, s.repos, "commit", "-qm", "initial", "--allow-empty")

	raw := s.request("GET", "/api/workspace/discovery?daemon_id="+s.id, nil, 200)
	var listed struct{ Page protocol.DiscoveryPage }
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	bound := protocol.SelectionRef{Path: listed.Page.Root.Path, SourceIdentity: listed.Page.Root.SourceIdentity}
	sid := s.launch("daemon", bound)
	w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil || len(w.Manifest.Members) != 1 {
		t.Fatalf("manifest %+v %v", w, err)
	}

	folder := protocol.SelectionRef{Path: ".", SourceIdentity: "fixture-wrong-identity"}
	dc := s.f.hub.GetDaemon(s.id)
	if dc == nil {
		t.Fatal("no live daemon connection")
	}
	res, err := s.f.hub.requestWorkspaceDiscovery(s.ctx, privAcctA, dc, protocol.DiscoverWorkspace{
		Operation:              "resolve_base",
		ParentLaunchGeneration: w.Manifest.LaunchGeneration,
		Folder:                 &folder,
		Base:                   "main",
	})
	if err != nil {
		t.Fatalf("resolve_base request failed: %v", err)
	}
	if res.OK {
		t.Fatal("expected a refusal for an unrecognized member SourceIdentity")
	}
	if res.Cause != "resolve_base:unknown_member" {
		t.Fatalf("got cause %q, want %q", res.Cause, "resolve_base:unknown_member")
	}

	var logged []byte
	p6EnabledWait(t, 5*time.Second, "daemon log carries the resolve_base refusal with its distinct cause", func() bool {
		logged, err = os.ReadFile(s.log.Name())
		return err == nil && strings.Contains(string(logged), `"operation":"resolve_base"`) && strings.Contains(string(logged), `"cause":"`+res.Cause+`"`)
	})
}
