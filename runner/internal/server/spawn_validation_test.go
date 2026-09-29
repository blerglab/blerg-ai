package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// runtimeName is what the session row's posture claim is built from, so every
// runtime must map to itself — a value silently recorded as "daemon" would
// label a session as running unsandboxed on the host when it does not.
func TestRuntimeNameMapsEveryRuntimeToItself(t *testing.T) {
	for rt, want := range map[string]string{
		"docker":  "docker",
		"cluster": "cluster",
		"daemon":  "daemon",
		"":        "daemon", // callers older than the explicit Run column
	} {
		if got := runtimeName(rt); got != want {
			t.Errorf("runtimeName(%q) = %q, want %q", rt, got, want)
		}
	}
}

// A repo that is not a plain directory name must be refused before anything is
// sent to the daemon, on both the REST and the browser-WS spawn paths.
func TestPostSessionsRejectsTraversalRepo(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "d1", Name: "laptop", ReposRoot: "/home/dev/repos", send: make(chan []byte, 8)}
	hub.Register(dc)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	mint := enableBrowserAuth(t, api)
	tok := mint("session.start")

	for _, repo := range []string{"../..", "../../..", "a/b/c", "org/../x", "a\\b", ".hidden", ".hidden/x", "org/.hidden", "..", "/etc", "/abs", "app/", "/org/name"} {
		body, _ := json.Marshal(map[string]any{"daemon_id": "d1", "repo": repo, "runtime": "docker"})
		req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		api.HandlePostSessions(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("repo %q: status %d, want 422", repo, rec.Code)
		}
		select {
		case m := <-dc.send:
			t.Errorf("repo %q: message reached the daemon: %s", repo, m)
		default:
		}
	}
}

func TestPostSessionsAcceptsPlainRepoName(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "d1", Name: "laptop", send: make(chan []byte, 8)}
	hub.Register(dc)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	tok := enableBrowserAuth(t, api)("session.start")
	body, _ := json.Marshal(map[string]any{"daemon_id": "d1", "repo": "my-app", "runtime": "docker"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostSessions(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	select {
	case <-dc.send:
	default:
		t.Fatal("no spawn_session reached the daemon")
	}
}

// The browser-WS spawn_session path must apply the same validation. The
// "spawn_session" case in browser_conn.go's ServeBrowser read pump is not
// callable in isolation (it's inline in a network read loop), so the
// forwarding logic is extracted into Hub.forwardBrowserSpawn and tested
// directly here.
func TestForwardBrowserSpawnRejectsTraversalRepo(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "d1", Name: "laptop", send: make(chan []byte, 8)}
	hub.Register(dc)
	bc := &BrowserConn{ID: "b1"}

	for _, repo := range []string{"../..", "../../..", "a/b/c", "org/../x", "a\\b", ".hidden", ".hidden/x", "org/.hidden", "..", "/etc", "/abs", "app/", "/org/name"} {
		msg := protocol.BrowserSpawnSession{Type: "spawn_session", DaemonID: "d1", Repo: repo}
		_, err := hub.forwardBrowserSpawn(bc, msg, nil)
		if err == nil {
			t.Errorf("repo %q: expected error, got nil", repo)
		}
		select {
		case m := <-dc.send:
			t.Errorf("repo %q: message reached the daemon: %s", repo, m)
		default:
		}
	}
}

func TestForwardBrowserSpawnAcceptsPlainRepoName(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "d1", Name: "laptop", send: make(chan []byte, 8)}
	hub.Register(dc)
	bc := &BrowserConn{ID: "b1"}

	sessionID, err := hub.forwardBrowserSpawn(bc, protocol.BrowserSpawnSession{Type: "spawn_session", DaemonID: "d1", Repo: "my-app"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected a session id")
	}
	select {
	case <-dc.send:
	default:
		t.Fatal("no spawn_session reached the daemon")
	}
}

// Org-qualified repos ("org/name") are first-class across the stack — boards
// store them and runner.JoinRepoURL resolves them against the git base's host —
// so validateRepoName must accept exactly one interior slash while still
// refusing anything that could climb out of a repos root.
func TestValidateRepoNameAllowsOneOrgSegment(t *testing.T) {
	for _, repo := range []string{"app", "my-app", "org/name", "Some.Org/my.app"} {
		if err := validateRepoName(repo); err != nil {
			t.Errorf("validateRepoName(%q) = %v, want accepted", repo, err)
		}
	}
	for _, repo := range []string{
		"", "..", "../x", "org/../x", "a/b/c", "/abs", "/org/name", "app/",
		".hidden", ".hidden/x", "org/.hidden", "a\\b", "org/na\nme", "org/na\x00me",
	} {
		if err := validateRepoName(repo); err == nil {
			t.Errorf("validateRepoName(%q) = nil, want rejected", repo)
		}
	}
}

// The REST spawn path must let an org-qualified repo through to the daemon.
func TestPostSessionsAcceptsOrgQualifiedRepo(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "d1", Name: "laptop", send: make(chan []byte, 8)}
	hub.Register(dc)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	tok := enableBrowserAuth(t, api)("session.start")
	body, _ := json.Marshal(map[string]any{"daemon_id": "d1", "repo": "blerglab/blerg", "runtime": "docker"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostSessions(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	select {
	case <-dc.send:
	default:
		t.Fatal("no spawn_session reached the daemon")
	}
}

// Cluster sessions clone from a git base that, with no BLERG_RUNNER_GITHUB_ORG
// configured, is bare github.com — so a one-segment repo name has nothing to
// resolve against and must be refused with a message that says what to do.
func TestPostSessionsClusterRequiresOrgQualifiedRepoWithoutOrg(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.GitHubOrg = ""
	hub.SetJobManager(jm)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	tok := enableBrowserAuth(t, api)("session.start")

	body, _ := json.Marshal(map[string]any{"repo": "widget", "runtime": "cluster", "kind": "agent"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostSessions(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "repo must be given as org/name for cluster sessions (or set BLERG_RUNNER_GITHUB_ORG)") {
		t.Errorf("unexpected message: %s", rec.Body.String())
	}
	if len(f.created) != 0 {
		t.Error("no Job should have been created")
	}
}

// With an org configured, a bare repo name still resolves against it.
func TestPostSessionsClusterAcceptsBareRepoWithOrg(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.GitHubOrg = "acme"
	hub.SetJobManager(jm)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	tok := enableBrowserAuth(t, api)("session.start")

	body, _ := json.Marshal(map[string]any{"repo": "widget", "runtime": "cluster", "kind": "agent"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostSessions(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
}

// An explicit BLERG_RUNNER_AGENT_GIT_BASE is a complete answer on its own: it
// may point at a self-hosted host whose paths are single-segment, so a bare
// repo name resolves fine and must not be refused just because no GitHub org
// is configured.
func TestPostSessionsClusterAcceptsBareRepoWithExplicitGitBase(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.GitHubOrg = ""
	jm.ExplicitGitURLBase = "https://git.example.test/team"
	jm.GitURLBase = "https://git.example.test/team"
	hub.SetJobManager(jm)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	tok := enableBrowserAuth(t, api)("session.start")

	body, _ := json.Marshal(map[string]any{"repo": "widget", "runtime": "cluster", "kind": "agent"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostSessions(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if len(f.created) != 1 {
		t.Fatalf("created = %d, want 1 Job", len(f.created))
	}
	raw, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(raw), `"BLERG_RUNNER_GIT_URL","value":"https://git.example.test/team/widget.git"`) {
		t.Errorf("clone URL should resolve against the explicit base: %s", raw)
	}
}

// An org/name repo is always passed through, org configured or not.
func TestPostSessionsClusterAcceptsOrgQualifiedRepoWithoutOrg(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.GitHubOrg = ""
	hub.SetJobManager(jm)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	tok := enableBrowserAuth(t, api)("session.start")

	body, _ := json.Marshal(map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": "agent"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostSessions(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if len(f.created) != 1 {
		t.Fatalf("created = %d, want 1 Job", len(f.created))
	}
}

// ─── Explicit runtime (spec §2: the runtime × kind table) ────────────────────

// postSpawn POSTs a spawn body with a session.start token and returns the
// recorder, so the runtime rules can be asserted on status and body.
func postSpawn(t *testing.T, api *API, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	tok := enableBrowserAuth(t, api)("session.start")
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostSessions(rec, req)
	return rec
}

func runtimeTestAPI(t *testing.T) (*API, *DaemonConn) {
	t.Helper()
	hub := NewHub()
	dc := &DaemonConn{ID: "d1", Name: "laptop", ReposRoot: "/repos", send: make(chan []byte, 8)}
	hub.Register(dc)
	return NewAPI(hub, nil, "daemon-tok-1234567890", nil, ""), dc
}

// runtime is a closed set: anything outside cluster|docker|daemon|"" is a
// caller mistake, not a silent fallback to the least-sandboxed option.
func TestPostSessionsRejectsUnknownRuntime(t *testing.T) {
	api, dc := runtimeTestAPI(t)
	for _, rt := range []string{"vm", "DOCKER", "host", "sandbox", "kubernetes"} {
		rec := postSpawn(t, api, map[string]any{"daemon_id": "d1", "repo": "my-app", "runtime": rt, "kind": "agent"})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("runtime %q: status %d, want 422", rt, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "invalid runtime") {
			t.Errorf("runtime %q: body %s, want \"invalid runtime\"", rt, rec.Body.String())
		}
		select {
		case m := <-dc.send:
			t.Errorf("runtime %q: message reached the daemon: %s", rt, m)
		default:
		}
	}
}

// Terminal sessions cannot run in a cluster pod — there is no PTY to attach
// to. The message is the one the launch sheet shows verbatim.
func TestPostSessionsRejectsTerminalOnCluster(t *testing.T) {
	for _, kind := range []string{"", "tmux"} {
		hub := NewHub()
		f := &fakeK8s{}
		jm := newTestJobManager(t, f)
		jm.GitHubOrg = "acme"
		hub.SetJobManager(jm)
		api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")

		rec := postSpawn(t, api, map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": kind})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("kind %q: status %d, want 422: %s", kind, rec.Code, rec.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["error"] != "terminal sessions run on a daemon" {
			t.Errorf("kind %q: error = %q, want %q", kind, body["error"], "terminal sessions run on a daemon")
		}
		if len(f.created) != 0 {
			t.Errorf("kind %q: no Job should have been created", kind)
		}
	}
}

// Agent + docker is the new case: the daemon is told to sandbox the engine.
func TestPostSessionsSandboxesAgentOnDocker(t *testing.T) {
	api, dc := runtimeTestAPI(t)
	rec := postSpawn(t, api, map[string]any{"daemon_id": "d1", "repo": "my-app", "runtime": "docker", "kind": "agent"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	msg := recvSpawn(t, dc)
	if !msg.Sandbox {
		t.Error("Sandbox = false, want true for agent + docker")
	}
	if msg.Kind != "agent" {
		t.Errorf("Kind = %q, want agent", msg.Kind)
	}
}

// Agent + daemon stays unsandboxed (the explicitly acknowledged choice), and
// an older caller that sends no runtime at all still means the daemon host.
func TestPostSessionsAgentOnDaemonIsUnsandboxed(t *testing.T) {
	api, dc := runtimeTestAPI(t)
	for _, rt := range []string{"daemon", ""} {
		rec := postSpawn(t, api, map[string]any{"daemon_id": "d1", "repo": "my-app", "runtime": rt, "kind": "agent"})
		if rec.Code != http.StatusAccepted {
			t.Fatalf("runtime %q: status %d, want 202: %s", rt, rec.Code, rec.Body.String())
		}
		if msg := recvSpawn(t, dc); msg.Sandbox {
			t.Errorf("runtime %q: Sandbox = true, want false", rt)
		}
	}
}

// tmux + docker is unchanged: the sandbox terminal, permission bypass allowed.
func TestPostSessionsTerminalOnDockerUnchanged(t *testing.T) {
	api, dc := runtimeTestAPI(t)
	rec := postSpawn(t, api, map[string]any{
		"daemon_id": "d1", "repo": "my-app", "runtime": "docker",
		"dangerously_skip_permissions": true,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	msg := recvSpawn(t, dc)
	if !msg.Sandbox || !msg.DangerouslySkipPermissions || msg.Kind != "" {
		t.Errorf("spawn = %+v, want sandboxed tmux with skip permissions", msg)
	}
}

// The permission-bypass rule is unchanged by the runtime validation: only the
// Docker sandbox may skip prompts, on either kind.
func TestPostSessionsSkipPermissionsStillDockerOnly(t *testing.T) {
	api, dc := runtimeTestAPI(t)
	for _, rt := range []string{"daemon", ""} {
		rec := postSpawn(t, api, map[string]any{
			"daemon_id": "d1", "repo": "my-app", "runtime": rt, "kind": "agent",
			"dangerously_skip_permissions": true,
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("runtime %q: status %d, want 422", rt, rec.Code)
		}
		select {
		case m := <-dc.send:
			t.Errorf("runtime %q: message reached the daemon: %s", rt, m)
		default:
		}
	}
}
