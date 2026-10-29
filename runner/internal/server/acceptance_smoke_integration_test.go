//go:build blerg_workspace_integration && linux

package server

// Controller-only smoke harness. This runs inside its owned namespace, using
// the production selection/Job/runtime/reader/shell/terminal code. Core identity
// and the engine are deterministic fixtures; it is not a Core/provider test.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const smokeProvider = "acceptance-fixture"
const smokeGitHost = "acceptance-smoke"

func TestAcceptanceClusterSmoke(t *testing.T) {
	if os.Getenv("P6_ACCEPTANCE_SMOKE") != "1" {
		t.Skip("controller-only real pod smoke")
	}
	ns := os.Getenv("BLERG_RUNNER_AGENT_NAMESPACE")
	if err := podAcceptanceSettings(ns, os.Getenv("BLERG_RUNNER_POD_EXCLUDE_NODES"), os.Getenv("BLERG_RUNNER_AGENT_IMAGE")); err != nil {
		t.Fatal(err)
	}
	if !protocol.ClusterWorkspaceEnabled || protocol.MacOSHostWorkspaceEnabled {
		t.Fatal("cluster must ship enabled; macOS host must remain disabled")
	}
	var sources, targets []string
	if json.Unmarshal([]byte(os.Getenv("P6_PUBLIC_REPOS")), &sources) != nil || json.Unmarshal([]byte(os.Getenv("P6_PUSH_TARGETS")), &targets) != nil {
		t.Fatal("invalid smoke repository parameters")
	}
	if err := smokeURLs(sources, targets); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	root := t.TempDir()
	baselines := map[string]string{}
	for i, source := range sources {
		project := fmt.Sprintf("smoke/member%d", i+1)
		dest := filepath.Join(root, project+".git")
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		// Public upstream clone receives no credential, inherited Git config or helper.
		smokeGit(t, root, "clone", "--bare", "--single-branch", "--", source, dest)
		smokeGit(t, dest, "config", "http.receivepack", "true")
		baselines[project] = smokeGit(t, dest, "rev-parse", "HEAD")
	}
	fixtureToken := newUUID()
	backend := &cgi.Handler{Path: "/usr/lib/git-core/git-http-backend", Dir: root,
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=acceptance", "HOME=/nonexistent", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}}
	gitMux := http.NewServeMux()
	gitMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			_, token, ok := r.BasicAuth()
			if !ok || token != fixtureToken {
				w.WriteHeader(401)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		backend.ServeHTTP(w, r)
	})
	gitServer := &http.Server{Addr: ":8443", Handler: gitMux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 90 * time.Second}
	listener, err := net.Listen("tcp", ":8443")
	if err != nil {
		t.Fatal(err)
	}
	defer gitServer.Close()
	go func() { _ = gitServer.ServeTLS(listener, "/acceptance/tls/cert.pem", "/acceptance/tls/key.pem") }()
	if err := gitprovider.Default.Register(gitprovider.NewGitHubAt(smokeProvider, smokeGitHost, "https://"+smokeGitHost, nil)); err != nil {
		t.Fatal(err)
	}
	f := newPrivFixture(t)
	var sid string
	// Register before launch: synchronous HTTP failures must also leave evidence.
	defer func() {
		diagnosticCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		rows, err := f.pool.Query(diagnosticCtx, `SELECT id::text FROM sessions WHERE runtime='cluster' ORDER BY id`)
		if err != nil {
			t.Log("ACCEPTANCE_LAUNCH_DIAGNOSTICS []")
			return
		}
		var ids []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		var diagnostics []clusterLaunchDiagnostic
		for _, id := range ids {
			stages, _ := clusterLaunchSnapshot(diagnosticCtx, f.pool, id)
			diagnostics = append(diagnostics, stages...)
		}
		raw, _ := json.Marshal(diagnostics)
		t.Logf("ACCEPTANCE_LAUNCH_DIAGNOSTICS %s", raw)
	}()
	// Only fixture-owned credentials are minted. No owner/provider/engine secrets.
	fake := &fakeK8s{credentialKinds: []string{smokeProvider, "claude"}, credentialFetchResponses: map[string][]byte{
		privAcctA + ":" + smokeProvider: []byte(fixtureToken), privAcctA + ":claude": []byte("sk-ant-acceptance-scripted")}}
	cred := fake.credentialHandler()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/shell/") {
			if r.Header.Get("X-Internal-Key") != testInternalKey {
				w.WriteHeader(401)
				return
			}
			if r.URL.Path == "/internal/shell/control" {
				w.Header().Set("Content-Type", "application/x-ndjson")
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				for {
					_ = json.NewEncoder(w).Encode(map[string]any{"policy": protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}})
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
						return
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			}
			_ = json.NewEncoder(w).Encode(protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
			return
		}
		cred(w, r)
	}))
	defer core.Close()
	defer cancel()
	f.api.coreURL, f.api.coreInternalKey = core.URL, testInternalKey
	j := NewJobManagerFromEnv("")
	if j == nil {
		t.Fatal("in-cluster launcher configuration missing")
	}
	j.historyPool = f.pool
	j.CoreURL, j.CoreInternalKey, j.CredentialClient = core.URL, testInternalKey, core.Client()
	life := newClusterWatchLifetime(ctx)
	defer life.close()
	j.startWatches = life
	f.hub.SetJobManager(j)
	f.api.ConfigureClusterWorkspaces()
	catalog := NewRepoLister("", "", f.hub)
	catalog.cachedAt = time.Now()
	selection := protocol.WorkspaceSelectionRequest{}
	for i := range sources {
		project := fmt.Sprintf("smoke/member%d", i+1)
		catalog.cached = append(catalog.cached, RepoInfo{Name: fmt.Sprintf("member%d", i+1), FullName: project, Provider: smokeProvider})
		selection.Hosted = append(selection.Hosted, protocol.HostedSelectionRef{Provider: smokeProvider, FullName: project})
	}
	f.api.repos = catalog
	if err := f.api.StartRuntimeShellChannel(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", f.hub.ServeDaemon(spawnTokenDaemonTok, f.pool))
	f.api.RegisterWorkspaceFileRoutes(mux)
	f.api.RegisterShellRoutes(mux)
	mux.HandleFunc("POST /api/sessions", f.api.HandlePostSessions)
	mux.HandleFunc("DELETE /api/sessions/{id}", f.api.HandleDeleteSession)
	mux.HandleFunc("POST /api/runner/sessions/{id}/message", f.api.HandleRunnerMessage)
	apiServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	apiListener, err := net.Listen("tcp", ":8080")
	if err != nil {
		t.Fatal(err)
	}
	defer apiServer.Close()
	go func() { _ = apiServer.Serve(apiListener) }()
	client := &http.Client{Timeout: 30 * time.Second}
	request := func(method, path string, body any, want int) []byte {
		t.Helper()
		var input io.Reader
		if body != nil {
			raw, e := json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
			input = bytes.NewReader(raw)
		}
		r, e := http.NewRequestWithContext(ctx, method, "http://localhost:8080"+path, input)
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctA, "session.start", "account.manage"))
		r.Header.Set("Origin", "http://localhost:8080")
		res, e := client.Do(r)
		if e != nil {
			t.Fatal("smoke HTTP transport failed")
		}
		defer res.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(res.Body, 2<<20))
		if e == nil && res.StatusCode == http.StatusConflict && method == http.MethodGet && strings.Contains(path, "/workspace?") {
			return nil
		}
		if e != nil || res.StatusCode != want {
			if method == http.MethodPost && strings.HasSuffix(path, "/shells") {
				var refusal struct {
					Code string `json:"code"`
				}
				_ = json.Unmarshal(raw, &refusal)
				if !ownershell.SafeRefusalReason(refusal.Code) {
					refusal.Code = string(ownershell.ReasonUnavailable)
				}
				advanceStart(ctx, f.hub, f.pool, sid, true, stage("shell_open", "Owner shell", protocol.StageStateFailed, "Owner shell refused: "+refusal.Code))
				t.Fatalf("cluster smoke failed at stage shell_open: HTTP %d, reason %s", res.StatusCode, refusal.Code)
			}
			t.Fatalf("smoke HTTP %s: %d, expected %d", method, res.StatusCode, want)
		}
		return raw
	}
	raw := request("POST", "/api/sessions", map[string]any{"runtime": "cluster", "kind": "agent", "engine": "claude", "selection": selection, "title": "Acceptance public multi-repo smoke"}, 202)
	var launch struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(raw, &launch) != nil || launch.SessionID == "" {
		t.Fatal("no cluster session")
	}
	sid = launch.SessionID
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
		defer done()
		_ = f.api.stopSessionWithDelivery(cleanup, sid, systemEnd(db.EndReasonStoppedByUser), false)
	}()
	noActiveProgress := time.Now()
	p6EnabledWait(t, 4*time.Minute, "actual pod workspace and negotiated shell", func() bool {
		stages, err := clusterLaunchSnapshot(ctx, f.pool, sid)
		if err != nil {
			t.Fatal("cluster launch diagnostics unavailable")
		}
		if problem := clusterLaunchProblem(stages, time.Now(), 60*time.Second); problem != "" {
			if strings.Contains(problem, "stalled") {
				for _, diagnostic := range stages {
					if diagnostic.Outcome == protocol.StageStateActive {
						advanceStart(ctx, f.hub, f.pool, sid, true, stage(diagnostic.Stage, "", protocol.StageStateFailed, "Launch stage exceeded the smoke progress deadline"))
						break
					}
				}
			}
			t.Fatal(problem)
		}
		row, err := db.GetSession(ctx, f.pool, sid)
		if err == nil && row != nil && (row.Status == "error" || row.Status == "ended" || row.Status == "stopped") {
			t.Fatal("cluster launch ended before workspace negotiation; inspect launch diagnostics")
		}
		_, b, e := db.GetWorkspaceRuntime(ctx, f.pool, sid)
		dc := f.hub.FindDaemonForSession(sid)
		if dc != nil && (dc.WorkspaceCapabilities.ManifestVersion != 2 || dc.WorkspaceCapabilities.ShellVersion != 1) {
			t.Fatal("cluster launch failed at stage negotiation: workspace and owner shell capability required")
		}
		current, readErr := db.GetSessionWorkspace(ctx, f.pool, sid)
		ready := smokeSessionReady(row, stages) && e == nil && b != nil && dc != nil && dc.WorkspaceCapabilities.ManifestVersion == 2 && dc.WorkspaceCapabilities.ShellVersion == 1 && readErr == nil && current != nil && len(current.Results) == len(sources)
		for _, diagnostic := range stages {
			if diagnostic.Outcome == protocol.StageStateActive {
				noActiveProgress = time.Now()
			}
		}
		if !ready && time.Since(noActiveProgress) >= 60*time.Second {
			advanceStart(ctx, f.hub, f.pool, sid, true, stage("workspace_prepare", "", protocol.StageStateFailed, "Launch stage exceeded the smoke progress deadline"))
			t.Fatal("cluster launch stalled at stage workspace_prepare: no complete prepared workspace receipt")
		}
		return ready
	})
	ws, err := db.GetSessionWorkspace(ctx, f.pool, sid)
	if err != nil || ws == nil || ws.Manifest == nil || len(ws.Manifest.Members) != len(sources) {
		t.Fatal("multi-repo manifest missing")
	}
	seen := map[string]bool{}
	for _, m := range ws.Manifest.Members {
		if m.Path != m.ID || seen[m.Path] {
			t.Fatal("members do not have distinct paths")
		}
		seen[m.Path] = true
		found := false
		for _, r := range ws.Results {
			if r.MemberID == m.ID && r.Head == baselines[m.Project] {
				found = true
			}
		}
		if !found {
			t.Fatal("prepared clone HEAD differs from public upstream")
		}
		// Status travels through the actual pod outbound socket and protected reader.
		var status struct {
			Result protocol.WorkspaceExplorerResult
		}
		p6EnabledWait(t, 20*time.Second, "member status", func() bool {
			raw = request("GET", "/api/sessions/"+sid+"/workspace?op=status&path="+url.QueryEscape(m.Path)+"&member="+m.ID, nil, 200)
			return json.Unmarshal(raw, &status) == nil && status.Result.OK && status.Result.Status != nil && status.Result.Status.Branch == "wip/"+sid
		})
	}
	advanceStart(ctx, f.hub, f.pool, sid, true, stage("shell_open", "Owner shell", protocol.StageStateActive, "Opening ready owner's shell"))
	raw = request("POST", "/api/sessions/"+sid+"/shells", nil, 201)
	var shell protocol.OwnerShellInfo
	if json.Unmarshal(raw, &shell) != nil || shell.Handle == "" {
		t.Fatal("shell did not open")
	}
	request("DELETE", "/api/shells/"+shell.Handle, nil, 204)
	advanceStart(ctx, f.hub, f.pool, sid, true, stage("shell_open", "Owner shell", protocol.StageStateDone, "Stage completed"))
	request("POST", "/api/runner/sessions/"+sid+"/message", map[string]string{"text": "commit smoke marker in every member", "client_id": newUUID()}, 202)
	p6EnabledWait(t, 3*time.Minute, "scripted commits and sync push receipts", func() bool {
		current, e := db.GetSessionWorkspace(ctx, f.pool, sid)
		if e != nil || current == nil || len(current.Results) != len(sources) {
			return false
		}
		for _, r := range current.Results {
			original := ""
			for _, m := range ws.Manifest.Members {
				if m.ID == r.MemberID {
					original = baselines[m.Project]
				}
			}
			if original == "" || r.Head == original || r.Head == "" || r.Pending || r.SyncError != "" || r.PushError != "" {
				return false
			}
		}
		return true
	})
	for _, m := range ws.Manifest.Members {
		bare := filepath.Join(root, m.Project+".git")
		head := smokeGit(t, bare, "rev-parse", "refs/heads/wip/"+sid)
		if head == baselines[m.Project] || !strings.Contains(smokeGit(t, bare, "show", head+":acceptance-smoke.txt"), "acceptance smoke") {
			t.Fatal("sync did not push new committed member work to fixture")
		}
	}
	// Optional disposable upstreams receive only the exact wip/session branch.
	// Local fixture remains the credential-bound runtime destination; this relay
	// step verifies forwarding without granting any public source push authority.
	for i, target := range targets {
		bare := filepath.Join(root, fmt.Sprintf("smoke/member%d.git", i+1))
		ref := "refs/heads/wip/" + sid
		smokeGit(t, bare, "push", "--", target, ref+":"+ref)
		remote := smokeGit(t, bare, "ls-remote", "--", target, ref)
		if !strings.HasPrefix(remote, smokeGit(t, bare, "rev-parse", ref)+"\t") {
			t.Fatal("disposable target receipt differs")
		}
	}
	for _, op := range []string{"list", "search", "status"} {
		query := "?op=" + op
		if op == "search" {
			query += "&filter=acceptance-smoke.txt"
		}
		if op == "status" {
			m := ws.Manifest.Members[0]
			query += "&path=" + url.QueryEscape(m.Path) + "&member=" + m.ID
		}
		var reply struct {
			Result protocol.WorkspaceExplorerResult
		}
		p6EnabledWait(t, 20*time.Second, "explorer "+op, func() bool {
			raw = request("GET", "/api/sessions/"+sid+"/workspace"+query, nil, 200)
			return json.Unmarshal(raw, &reply) == nil && reply.Result.OK
		})
		if op == "list" {
			for path := range seen {
				if !bytes.Contains(raw, []byte(path)) {
					t.Fatal("explorer list omitted a member")
				}
			}
		}
		if op == "search" {
			for member := range seen {
				found := false
				for _, entry := range reply.Result.Entries {
					if entry.Path == member+"/acceptance-smoke.txt" {
						found = true
					}
				}
				if !found {
					t.Fatal("real search omitted a committed member marker")
				}
			}
		}
	}
	var liveTokens int
	if f.pool.QueryRow(ctx, `SELECT count(*) FROM board_tokens WHERE session_id=$1 AND revoked_at IS NULL`, sid).Scan(&liveTokens) != nil || liveTokens < 2 {
		t.Fatal("runtime/messaging tokens not proven live before stop")
	}
	request("DELETE", "/api/sessions/"+sid, nil, 204)
	p6EnabledWait(t, 2*time.Minute, "Job/Pod/Secret/token terminal cleanup", func() bool {
		row, e := db.GetSession(ctx, f.pool, sid)
		if e != nil || row == nil || row.EndedAt == nil {
			return false
		}
		var active int
		if f.pool.QueryRow(ctx, `SELECT count(*) FROM board_tokens WHERE session_id=$1 AND revoked_at IS NULL`, sid).Scan(&active) != nil || active != 0 {
			return false
		}
		for _, path := range []string{"/apis/batch/v1/namespaces/" + ns + "/jobs/" + j.jobName(sid), "/api/v1/namespaces/" + ns + "/secrets/" + sessionSecretName(sid)} {
			resp, e := j.doWithContentType(ctx, "GET", path, nil, "application/json")
			if e != nil {
				return false
			}
			_ = resp.Body.Close()
			if resp.StatusCode != 404 {
				return false
			}
		}
		return j.fenceNativeHistoryWriter(ctx, SessionJobSpec{SessionID: sid}) == nil
	})
	// A machine-readable receipt is emitted only after every real assertion. The
	// controller also verifies namespace UID, scheduling and effective RBAC.
	t.Log("ACCEPTANCE_SMOKE_PASS clones=distinct explorer=list,search,status shell=open,close sync=fixture-push stop=jobs,secrets,tokens quotas=NOT_ENFORCED")
}

func smokeURLs(sources, targets []string) error {
	if len(sources) < 2 || len(sources) > 3 || len(targets) != 0 && len(targets) != len(sources) {
		return fmt.Errorf("two or three public sources and zero or matching disposable targets required")
	}
	seen := map[string]bool{}
	for _, raw := range append(append([]string{}, sources...), targets...) {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || strings.ContainsAny(raw, "\r\n\x00") {
			return fmt.Errorf("credential-free HTTPS repository URL required")
		}

		if strings.Contains(u.Path, "%") || u.RawPath != "" {
			return fmt.Errorf("unambiguous repository path required")
		}
		for _, part := range strings.Split(u.Path, "/") {
			if part == "." || part == ".." {
				return fmt.Errorf("unambiguous repository path required")
			}
		}
		port := u.Port()
		if port == "" {
			port = "443"
		}
		key := strings.ToLower(u.Hostname() + ":" + port + strings.TrimSuffix(strings.TrimRight(strings.ToLower(u.Path), "/"), ".git"))
		if seen[key] {
			return fmt.Errorf("duplicate repository or source push target")
		}
		seen[key] = true
	}
	return nil
}

func smokeGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "credential.helper=", "-c", "http.followRedirects=false", "-c", "http.proxy="}, args...)...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/nonexistent", "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("anonymous fixture Git operation failed (URLs/output withheld)")
	}
	return strings.TrimSpace(string(out))
}

func TestAcceptanceSmokeParameters(t *testing.T) {
	sources := []string{"https://example.invalid/a/one.git", "https://example.invalid/b/two.git"}
	if smokeURLs(sources, nil) != nil {
		t.Fatal("valid public smoke refused")
	}
	for _, bad := range [][]string{sources[:1], {sources[0], sources[0]}, {"https://token@example.invalid/a.git", sources[1]}, {"http://example.invalid/a.git", sources[1]}} {
		if smokeURLs(bad, nil) == nil {
			t.Fatal("unsafe smoke input admitted")
		}
	}
	if smokeURLs(sources, sources) == nil || smokeURLs(sources, []string{sources[0] + "/", sources[1] + "/"}) == nil || smokeURLs(sources, []string{"https://example.invalid/target.git"}) == nil {
		t.Fatal("unsafe push targets admitted")
	}
}

// Workspace receipts can precede the engine's first live state change.
func smokeSessionReady(row *db.SessionRow, stages []clusterLaunchDiagnostic) bool {
	if row == nil || !isLiveStatus(row.Status) {
		return false
	}
	for _, s := range stages {
		if s.Stage == protocol.StageReady && s.Outcome == protocol.StageStateDone {
			return true
		}
	}
	return false
}

func TestAcceptanceShellWaitsForEngineReady(t *testing.T) {
	if !protocol.EnabledWorkspaceCapabilities().Supports("cluster", true) {
		t.Fatal("acceptance build lacks cluster shell capability")
	}
	row := &db.SessionRow{Status: "starting"}
	stages := []clusterLaunchDiagnostic{{Stage: "workspace_prepare", Outcome: "done"}, {Stage: "engine", Outcome: "active"}, {Stage: "ready", Outcome: "pending"}}
	if smokeSessionReady(row, stages) {
		t.Fatal("prepared workspace admitted shell before engine readiness")
	}
	row.Status = "idle"
	if smokeSessionReady(row, stages) {
		t.Fatal("shell admitted before persisted ready stage")
	}
	stages[2].Outcome = "done"
	if !smokeSessionReady(row, stages) {
		t.Fatal("ready live session refused")
	}
	row.Status = "stopped"
	if smokeSessionReady(row, stages) || smokeSessionReady(nil, stages) {
		t.Fatal("missing or stopped session admitted")
	}
}
