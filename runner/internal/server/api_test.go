package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/coreauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// mintTestToken signs claims into a compact "header.payload.sig" token for testing.
func mintTestToken(t *testing.T, priv ed25519.PrivateKey, kid string, c identity.Claims) string {
	t.Helper()
	hb, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kid})
	pb, _ := json.Marshal(c)
	si := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	sig := ed25519.Sign(priv, []byte(si))
	return si + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// newTestCoreServer creates a mock blerg-core server for testing.
func newTestCoreServer(t *testing.T, pub ed25519.PublicKey) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		jwks := map[string]string{"k1": base64.RawURLEncoding.EncodeToString(pub)}
		json.NewEncoder(w).Encode(jwks)
	})
	mux.HandleFunc("/revocations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]interface{}{})
	})
	return httptest.NewServer(mux)
}

// TestHandleGetReposListsLocalFoldersWithoutGitHubConfigured is a regression
// test for a real bug: with no GitHub org configured (a.repos == nil — the
// normal state for a plain desktop install), HandleGetRepos used to return an
// empty list unconditionally, before ever reaching the "local-only folders"
// merge below, even though that merge doesn't touch a.repos at all. A desktop
// install with real repos checked out locally, and no GitHub integration,
// always saw an empty picker as a result — reported live: a daemon correctly
// showed the right repos_root and was connected, but /api/repos was always
// {"repos":[]}.
func TestHandleGetReposListsLocalFoldersWithoutGitHubConfigured(t *testing.T) {
	// Set up test core server for auth
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	coreSrv := newTestCoreServer(t, pub)
	defer coreSrv.Close()

	coreAuth := coreauth.New(coreSrv.URL)

	hub := NewHub()
	dc := &DaemonConn{
		ID:        "d1",
		Name:      "dev-laptop",
		ReposRoot: "/home/dev/repositories",
	}
	dc.SetCheckedOutRepos([]string{"blerg", "clarity", "example"})
	hub.Register(dc)

	api := NewAPI(hub, nil, "", nil /* no GitHub org configured */, "")
	api.SetCoreAuth(coreAuth)

	// Create a valid token with session.start capability
	now := time.Now().Unix()
	token := mintTestToken(t, priv, "k1", identity.Claims{
		Sub: "human:test-user", Aud: "blerg-runner", Kind: "human",
		Caps: []string{"session.start"}, ExpiresAt: now + 60,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	api.HandleGetRepos(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp reposResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(resp.Repos) != 3 {
		t.Fatalf("got %d repos, want 3 local-only folders: %+v", len(resp.Repos), resp.Repos)
	}
	byName := make(map[string]RepoInfo, len(resp.Repos))
	for _, r := range resp.Repos {
		byName[r.Name] = r
	}
	for _, name := range []string{"blerg", "clarity", "example"} {
		r, ok := byName[name]
		if !ok {
			t.Errorf("expected local folder %q in response, got %+v", name, resp.Repos)
			continue
		}
		if !r.IsLocal {
			t.Errorf("repo %q: IsLocal = false, want true", name)
		}
		if len(r.CheckedOut) != 1 || r.CheckedOut[0] != "dev-laptop" {
			t.Errorf("repo %q: CheckedOut = %v, want [dev-laptop]", name, r.CheckedOut)
		}
	}
}

// TestBrowserEndpointsRequireAuth verifies that all 7 browser-facing endpoints
// return 401 Unauthorized when called without a valid credential.
func TestBrowserEndpointsRequireAuth(t *testing.T) {
	hub := NewHub()
	api := NewAPI(hub, nil, "daemon-tok", nil, "")

	// Test each endpoint returns 401 without credential
	req := httptest.NewRequest("GET", "/api/daemons", nil)
	w := httptest.NewRecorder()
	api.HandleGetDaemons(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/daemons: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest("GET", "/api/cluster/status", nil)
	w = httptest.NewRecorder()
	api.HandleGetClusterStatus(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/cluster/status: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest("GET", "/api/sessions", nil)
	w = httptest.NewRecorder()
	api.HandleGetSessions(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/sessions: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest("POST", "/api/sessions", bytes.NewBuffer([]byte(`{"daemon_id":"d1","repo":"test"}`)))
	w = httptest.NewRecorder()
	api.HandlePostSessions(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/sessions: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest("DELETE", "/api/sessions/test-id", nil)
	req.SetPathValue("id", "test-id")
	w = httptest.NewRecorder()
	api.HandleDeleteSession(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE /api/sessions/{id}: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest("PATCH", "/api/sessions/test-id", bytes.NewBuffer([]byte(`{"title":"test"}`)))
	req.SetPathValue("id", "test-id")
	w = httptest.NewRecorder()
	api.HandlePatchSession(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("PATCH /api/sessions/{id}: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest("GET", "/api/repos", nil)
	w = httptest.NewRecorder()
	api.HandleGetRepos(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/repos: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// TestBrowserEndpointsSucceedWithValidAuth verifies that at least one browser
// endpoint succeeds when given a valid core-issued token with the required
// session.start capability.
func TestBrowserEndpointsSucceedWithValidAuth(t *testing.T) {
	// Set up test core server
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	coreSrv := newTestCoreServer(t, pub)
	defer coreSrv.Close()

	coreAuth := coreauth.New(coreSrv.URL)

	hub := NewHub()
	api := NewAPI(hub, nil, "daemon-tok", nil, "")
	api.SetCoreAuth(coreAuth)

	// Create a valid token with session.start capability
	now := time.Now().Unix()
	token := mintTestToken(t, priv, "k1", identity.Claims{
		Sub: "human:test-user", Aud: "blerg-runner", Kind: "human",
		Caps: []string{"session.start"}, ExpiresAt: now + 60,
	})

	// Test HandleGetDaemons with valid token
	req := httptest.NewRequest("GET", "/api/daemons", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	api.HandleGetDaemons(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("GET /api/daemons with valid token: got status %d, want %d", w.Code, http.StatusOK)
	}

	// Test HandleGetClusterStatus with valid token
	req = httptest.NewRequest("GET", "/api/cluster/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	api.HandleGetClusterStatus(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("GET /api/cluster/status with valid token: got status %d, want %d", w.Code, http.StatusOK)
	}
}

// TestValidateRepoNameRejectsTraversal verifies that validateRepoName rejects
// path traversal and unsafe repo names.
func TestValidateRepoNameRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"../other/private-repo", "/etc/passwd", "..\\x", ".hidden"} {
		if err := validateRepoName(bad); err == nil {
			t.Errorf("validateRepoName(%q) = nil, want error", bad)
		}
	}
	if err := validateRepoName("myorg-repo123"); err != nil {
		t.Errorf("validateRepoName(good) = %v, want nil", err)
	}
}

// ─── GET /api/me/credentials ──────────────────────────────────────────────────

// newCredentialsAPI builds an API wired to an auth-capable stub core, plus a
// valid browser token for account "acct-1". credentialsHandler (may be nil)
// serves POST /internal/credentials/list on that same stub core.
func newCredentialsAPI(t *testing.T, credentialsHandler http.HandlerFunc) (*API, string, *httptest.Server) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"k1": base64.RawURLEncoding.EncodeToString(pub)})
	})
	mux.HandleFunc("/revocations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]interface{}{})
	})
	if credentialsHandler != nil {
		mux.HandleFunc("/internal/credentials/list", credentialsHandler)
	}
	coreSrv := httptest.NewServer(mux)

	api := NewAPI(NewHub(), nil, "daemon-tok", nil, "")
	api.SetCoreAuth(coreauth.New(coreSrv.URL))
	api.SetCoreCredentials(coreSrv.URL, "internal-key")

	token := mintTestToken(t, priv, "k1", identity.Claims{
		Sub: "acct-1", Aud: "blerg-runner", Kind: "human", Sid: testSID,
		Caps: []string{"session.start"}, ExpiresAt: time.Now().Unix() + 60,
	})
	return api, token, coreSrv
}

// callMyCredentials runs the handler with the given bearer token and returns
// the recorder plus the decoded body.
func callMyCredentials(t *testing.T, api *API, token string) (*httptest.ResponseRecorder, protocol.MyCredentials) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/me/credentials", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	api.HandleGetMyCredentials(w, req)
	var out protocol.MyCredentials
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode body %q: %v", w.Body.String(), err)
		}
	}
	return w, out
}

func TestMyCredentialsRequiresAuth(t *testing.T) {
	api, _, coreSrv := newCredentialsAPI(t, nil)
	defer coreSrv.Close()

	w, _ := callMyCredentials(t, api, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/me/credentials unauthenticated: got %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestMyCredentialsSplitsGitFromEngines(t *testing.T) {
	var gotKey, gotAccount string
	api, token, coreSrv := newCredentialsAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Internal-Key")
		var body struct {
			AccountID string `json:"account_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		gotAccount = body.AccountID
		json.NewEncoder(w).Encode(map[string][]string{"engines": {"claude", "github"}})
	})
	defer coreSrv.Close()

	w, out := callMyCredentials(t, api, token)
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if gotKey != "internal-key" {
		t.Errorf("X-Internal-Key = %q, want %q", gotKey, "internal-key")
	}
	if gotAccount != "acct-1" {
		t.Errorf("account_id = %q, want %q", gotAccount, "acct-1")
	}
	if len(out.Engines) != 1 || out.Engines[0] != "claude" {
		t.Errorf("Engines = %v, want [claude] (github must be filtered out)", out.Engines)
	}
	if !out.Git {
		t.Error("Git = false, want true (github was listed)")
	}
	if out.Unavailable {
		t.Error("Unavailable = true, want false")
	}
}

// TestMyCredentialsNotFoundIsUnavailable covers I2: core's list endpoint 404s
// when the account has no LIVE SESSION — the same anti-enumeration answer it
// gives for a missing credential — so a 404 says nothing about whether the
// account has credentials stored. Reporting it as a definite empty set would
// tell a user with several credentials that they have none; it belongs in the
// "couldn't tell" bucket alongside a down or unconfigured core.
func TestMyCredentialsNotFoundIsUnavailable(t *testing.T) {
	api, token, coreSrv := newCredentialsAPI(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	defer coreSrv.Close()

	w, out := callMyCredentials(t, api, token)
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", w.Code)
	}
	if len(out.Engines) != 0 || out.Git || !out.Unavailable {
		t.Errorf("got %+v, want empty engines, git=false, unavailable=true", out)
	}
	// Empty engines must serialise as [], never null.
	if !bytes.Contains(w.Body.Bytes(), []byte(`"engines":[]`)) {
		t.Errorf("body %q: want engines serialised as []", w.Body.String())
	}
}

func TestMyCredentialsCoreDownIsUnavailable(t *testing.T) {
	api, token, coreSrv := newCredentialsAPI(t, nil)
	defer coreSrv.Close()
	api.SetCoreCredentials("http://127.0.0.1:1", "internal-key") // nothing listening

	w, out := callMyCredentials(t, api, token)
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", w.Code)
	}
	if !out.Unavailable || len(out.Engines) != 0 || out.Git {
		t.Errorf("got %+v, want unavailable=true with empty engines", out)
	}
}

func TestMyCredentialsCoreErrorIsUnavailable(t *testing.T) {
	api, token, coreSrv := newCredentialsAPI(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom: core detail the browser must not see", http.StatusServiceUnavailable)
	})
	defer coreSrv.Close()

	w, out := callMyCredentials(t, api, token)
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", w.Code)
	}
	if !out.Unavailable {
		t.Errorf("got %+v, want unavailable=true", out)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("boom")) {
		t.Errorf("body %q: core's body must never be forwarded", w.Body.String())
	}
}

// ─── Recorded runtime + listed posture (spec §2) ─────────────────────────────

// Every daemon-routed spawn records exactly one of docker|daemon on the
// session row, for both kinds — the UI labels sessions from this column, and
// the reconciler selects cluster rows on it, so it must never be empty or
// spelled differently from the wire value.
//
// Requires TEST_DATABASE_URL; skips otherwise.
func TestPostSessionsRecordsRuntimeAndKind(t *testing.T) {
	cases := []struct {
		name        string
		runtime     string
		kind        string
		wantRuntime string
		wantKind    string
	}{
		{"agent in the sandbox", "docker", "agent", "docker", "agent"},
		{"agent on the host", "daemon", "agent", "daemon", "agent"},
		{"terminal in the sandbox", "docker", "", "docker", "tmux"},
		{"legacy caller, no runtime", "", "", "daemon", "tmux"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, _, dc, pool := spawnTokenFixture(t)
			rec := postSpawn(t, api, map[string]any{
				"daemon_id": dc.ID, "repo": "my-app", "runtime": tc.runtime, "kind": tc.kind,
			})
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
			}
			var resp map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			row, err := db.GetSession(context.Background(), pool, resp["session_id"])
			if err != nil || row == nil {
				t.Fatalf("GetSession: %v (row %v)", err, row)
			}
			if row.Runtime == nil || *row.Runtime != tc.wantRuntime {
				t.Errorf("runtime = %v, want %q", row.Runtime, tc.wantRuntime)
			}
			if row.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", row.Kind, tc.wantKind)
			}
		})
	}
}

// GET /api/sessions must tell the browser where each session runs and what
// kind it is, for a cluster session, a sandboxed one and a host one alike.
//
// Requires TEST_DATABASE_URL; skips otherwise.
func TestGetSessionsReportsRuntimeAndKind(t *testing.T) {
	api, _, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()

	sandboxed := postSpawnSessionID(t, api, dc.ID, "docker", "agent")
	host := postSpawnSessionID(t, api, dc.ID, "daemon", "")

	clusterID := "00000000-0000-4000-8000-00000000c1c1"
	if err := db.UpsertDaemon(ctx, pool, clusterDaemonID, "cluster", "runner", ""); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertClusterSession(ctx, pool, clusterID, clusterDaemonID, "starting",
		"/workspace/acme/widget", "acme/widget", "Cluster one", ""); err != nil {
		t.Fatalf("InsertClusterSession: %v", err)
	}
	if err := db.SetSessionKind(ctx, pool, clusterID, "agent"); err != nil {
		t.Fatalf("SetSessionKind: %v", err)
	}

	tok := enableBrowserAuth(t, api)("session.start")
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandleGetSessions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var list []protocol.SessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	byID := map[string]protocol.SessionInfo{}
	for _, s := range list {
		byID[s.ID] = s
	}
	for _, want := range []struct{ id, runtime, kind string }{
		{sandboxed, "docker", "agent"},
		{host, "daemon", "tmux"},
		{clusterID, "cluster", "agent"},
	} {
		got, ok := byID[want.id]
		if !ok {
			t.Fatalf("session %s missing from the list", want.id)
		}
		if got.Runtime != want.runtime || got.Kind != want.kind {
			t.Errorf("session %s: runtime=%q kind=%q, want %q/%q", want.id, got.Runtime, got.Kind, want.runtime, want.kind)
		}
	}
	// The wire shape itself, not just the decoded struct: the browser reads
	// these two keys by name.
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"runtime":"docker"`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"kind":"agent"`)) {
		t.Errorf("sessions JSON lacks runtime/kind keys: %s", rec.Body.String())
	}
}

// The session_started the browser receives is the first thing it knows about a
// session, so it must already carry the same labels the list shows — the
// runtime lives on the row (the server chose it at spawn), not in the daemon's
// message.
//
// Requires TEST_DATABASE_URL; skips otherwise.
func TestSessionStartedBroadcastCarriesRuntimeAndKind(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	sessionID := postSpawnSessionID(t, api, dc.ID, "docker", "agent")

	bc := &BrowserConn{ID: "b1", send: make(chan []byte, 8)}
	hub.RegisterBrowser(bc)
	HandleSessionStarted(context.Background(), hub, pool, dc.ID, protocol.SessionStarted{
		Type: "session_started", SessionID: sessionID, Repo: "my-app",
		ProjectPath: "/repos/my-app", Kind: "agent",
	})

	var started protocol.BrowserSessionStarted
	for {
		select {
		case raw := <-bc.send:
			if err := json.Unmarshal(raw, &started); err != nil || started.Type != "session_started" {
				continue
			}
			if started.Session.Runtime != "docker" || started.Session.Kind != "agent" {
				t.Fatalf("session_started = runtime %q kind %q, want docker/agent",
					started.Session.Runtime, started.Session.Kind)
			}
			return
		default:
			t.Fatal("no session_started reached the browser")
		}
	}
}

// session_started also carries the session's engine and launch effort (from
// the row), so the in-session model switcher asks for the right engine's list
// from the first moment. Requires TEST_DATABASE_URL.
func TestSessionStartedBroadcastCarriesEngineAndEffort(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	rec := postSpawn(t, api, map[string]any{"daemon_id": dc.ID, "repo": "my-app", "runtime": "docker",
		"kind": "agent", "engine": "codex", "model": "gpt-5-codex"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	// A Claude session with an effort, for the effort half.
	rec2 := postSpawn(t, api, map[string]any{"daemon_id": dc.ID, "repo": "my-app", "runtime": "docker",
		"kind": "agent", "model": "claude-opus-5-5", "effort": "xhigh"})
	var resp2 map[string]string
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)

	bc := &BrowserConn{ID: "b1", send: make(chan []byte, 16)}
	hub.RegisterBrowser(bc)
	for _, id := range []string{resp["session_id"], resp2["session_id"]} {
		HandleSessionStarted(context.Background(), hub, pool, dc.ID, protocol.SessionStarted{
			Type: "session_started", SessionID: id, Repo: "my-app", ProjectPath: "/repos/my-app", Kind: "agent",
		})
	}
	got := map[string]protocol.SessionInfo{}
	for len(bc.send) > 0 {
		var started protocol.BrowserSessionStarted
		if err := json.Unmarshal(<-bc.send, &started); err == nil && started.Type == "session_started" {
			got[started.Session.ID] = started.Session
		}
	}
	if s := got[resp["session_id"]]; s.Engine != "codex" {
		t.Errorf("codex session_started engine = %q", s.Engine)
	}
	if s := got[resp2["session_id"]]; s.Effort != "xhigh" || s.Engine != "" {
		t.Errorf("claude session_started effort = %q engine = %q", s.Effort, s.Engine)
	}
}

func postSpawnSessionID(t *testing.T, api *API, daemonID, runtime, kind string) string {
	t.Helper()
	rec := postSpawn(t, api, map[string]any{"daemon_id": daemonID, "repo": "my-app", "runtime": runtime, "kind": kind})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp["session_id"]
}

func TestMyCredentialsUnconfiguredIsUnavailable(t *testing.T) {
	api, token, coreSrv := newCredentialsAPI(t, nil)
	defer coreSrv.Close()
	api.SetCoreCredentials(coreSrv.URL, "") // internal key unset

	w, out := callMyCredentials(t, api, token)
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", w.Code)
	}
	if !out.Unavailable {
		t.Errorf("got %+v, want unavailable=true when the internal key is unset", out)
	}
}
