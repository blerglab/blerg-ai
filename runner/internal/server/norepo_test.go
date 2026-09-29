package server

// "No repository" sessions: an explicit no_repo flag on POST /api/sessions and
// the v1 start. Without the flag nothing changes — a blank repo is refused
// exactly as it always was.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/scratch"
)

// errorText decodes a writeError body.
func errorText(t *testing.T, body []byte) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return m["error"]
}

// Every refusal on POST /api/sessions, old-shaped requests included: a blank
// repo without no_repo (or with no_repo:false) still gets today's exact 422,
// and no_repo refuses anything that also names a repository.
func TestPostSessionsNoRepoValidation(t *testing.T) {
	const combo = "no_repo cannot be combined with repo, git_url, provider, clone or new_repo"
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		// Old callers, unchanged.
		{"daemon, blank repo", map[string]any{"daemon_id": "d1", "repo": "", "runtime": "docker", "kind": "agent"}, "daemon_id and repo are required"},
		{"daemon, repo omitted", map[string]any{"daemon_id": "d1", "runtime": "daemon", "kind": "agent"}, "daemon_id and repo are required"},
		{"daemon, no_repo false", map[string]any{"daemon_id": "d1", "repo": "", "no_repo": false, "runtime": "docker", "kind": "agent"}, "daemon_id and repo are required"},
		{"cluster, blank repo", map[string]any{"repo": "", "runtime": "cluster", "kind": "agent"}, "repo is required"},
		{"cluster, no_repo false", map[string]any{"no_repo": false, "runtime": "cluster", "kind": "agent"}, "repo is required"},
		// no_repo naming a repository anyway.
		{"no_repo + repo", map[string]any{"daemon_id": "d1", "no_repo": true, "repo": "my-app", "runtime": "docker", "kind": "agent"}, combo},
		{"no_repo + git_url", map[string]any{"daemon_id": "d1", "no_repo": true, "git_url": "https://github.com/o/r.git", "runtime": "docker", "kind": "agent"}, combo},
		{"no_repo + provider", map[string]any{"daemon_id": "d1", "no_repo": true, "provider": "github", "runtime": "docker", "kind": "agent"}, combo},
		{"no_repo + clone", map[string]any{"daemon_id": "d1", "no_repo": true, "clone": true, "runtime": "docker", "kind": "agent"}, combo},
		{"no_repo + new_repo", map[string]any{"daemon_id": "d1", "no_repo": true, "new_repo": true, "runtime": "docker", "kind": "agent"}, combo},
		{"cluster no_repo + repo", map[string]any{"no_repo": true, "repo": "acme/widget", "runtime": "cluster", "kind": "agent"}, combo},
		// scratch_folder rules.
		{"scratch_folder without no_repo", map[string]any{"daemon_id": "d1", "repo": "my-app", "scratch_folder": ".scratch-x", "runtime": "docker", "kind": "agent"}, "scratch_folder needs no_repo"},
		{"scratch_folder on cluster", map[string]any{"no_repo": true, "scratch_folder": ".scratch-x", "runtime": "cluster", "kind": "agent"}, "scratch_folder does not apply to cluster sessions: they have no folder"},
		{"scratch_folder not a scratch name", map[string]any{"daemon_id": "d1", "no_repo": true, "scratch_folder": "my-app", "runtime": "docker", "kind": "agent"}, `scratch_folder must be ".scratch-" followed by up to 64 letters, digits, '-' or '_'`},
		{"scratch_folder traversal", map[string]any{"daemon_id": "d1", "no_repo": true, "scratch_folder": ".scratch-../x", "runtime": "docker", "kind": "agent"}, `scratch_folder must be ".scratch-" followed by up to 64 letters, digits, '-' or '_'`},
		// A daemon runtime still needs a daemon.
		{"no_repo without daemon", map[string]any{"no_repo": true, "runtime": "docker", "kind": "agent"}, "daemon_id is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, dc := runtimeTestAPI(t)
			f := &fakeK8s{}
			jm := newTestJobManager(t, f)
			jm.GitHubOrg = "acme"
			api.hub.SetJobManager(jm)
			rec := postSpawn(t, api, tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422: %s", rec.Code, rec.Body.String())
			}
			if got := errorText(t, rec.Body.Bytes()); got != tc.want {
				t.Errorf("error = %q, want %q", got, tc.want)
			}
			select {
			case m := <-dc.send:
				t.Errorf("message reached the daemon: %s", m)
			default:
			}
			if len(f.created) != 0 {
				t.Error("no Job should have been created")
			}
		})
	}
}

// no_repo on a daemon runtime (This machine or Local sandbox): the daemon is
// told to create a scratch folder — a generated one, or the name the caller
// chose — and nothing about cloning reaches it.
func TestPostSessionsNoRepoOnDaemonSendsScratchFolder(t *testing.T) {
	for _, rt := range []string{"docker", "daemon"} {
		t.Run(rt, func(t *testing.T) {
			api, dc := runtimeTestAPI(t)
			rec := postSpawn(t, api, map[string]any{"daemon_id": "d1", "no_repo": true, "runtime": rt, "kind": "agent"})
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
			}
			msg := recvSpawn(t, dc)
			if !msg.NoRepo || !msg.NewRepo {
				t.Errorf("NoRepo=%v NewRepo=%v, want both true", msg.NoRepo, msg.NewRepo)
			}
			if !scratch.Valid(msg.Repo) {
				t.Errorf("Repo = %q, want a generated scratch folder name", msg.Repo)
			}
			if msg.CloneFrom != "" || msg.Provider != "" || msg.GitToken != "" {
				t.Errorf("clone fields reached the daemon: from=%q provider=%q token set=%v", msg.CloneFrom, msg.Provider, msg.GitToken != "")
			}
			if msg.Sandbox != (rt == "docker") {
				t.Errorf("Sandbox = %v for runtime %s", msg.Sandbox, rt)
			}
		})
	}

	api, dc := runtimeTestAPI(t)
	rec := postSpawn(t, api, map[string]any{
		"daemon_id": "d1", "no_repo": true, "scratch_folder": ".scratch-granite-3f9a", "runtime": "docker", "kind": "agent",
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if msg := recvSpawn(t, dc); msg.Repo != ".scratch-granite-3f9a" {
		t.Errorf("Repo = %q, want the caller's scratch_folder", msg.Repo)
	}
}

// jobEnv returns a created Job's container env as name -> entry.
func jobEnv(t *testing.T, job map[string]any) map[string]map[string]any {
	t.Helper()
	raw, _ := json.Marshal(job)
	var j struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Env []map[string]any `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &j); err != nil || len(j.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("decode job: %v", err)
	}
	out := map[string]map[string]any{}
	for _, e := range j.Spec.Template.Spec.Containers[0].Env {
		out[e["name"].(string)] = e
	}
	return out
}

// assertNoRepoJob checks a Job is a no-repo pod: told so, nothing to clone,
// and no git token of any kind in its env.
func assertNoRepoJob(t *testing.T, job map[string]any) {
	t.Helper()
	env := jobEnv(t, job)
	if v, _ := env["BLERG_RUNNER_NO_REPO"]["value"].(string); v != "1" {
		t.Errorf("BLERG_RUNNER_NO_REPO = %v, want 1", env["BLERG_RUNNER_NO_REPO"])
	}
	if v, _ := env["BLERG_RUNNER_GIT_URL"]["value"].(string); v != "" {
		t.Errorf("BLERG_RUNNER_GIT_URL = %q, want empty", v)
	}
	if v, _ := env["BLERG_RUNNER_REPO"]["value"].(string); v != "" {
		t.Errorf("BLERG_RUNNER_REPO = %q, want empty", v)
	}
	if e, ok := env[gitTokenKey]; ok {
		t.Errorf("a no-repo pod got a git token entry: %v", e)
	}
}

// no_repo on the cluster: an empty-workspace pod, accepted even where a bare
// repo name could not be resolved (no org, no git base) — there is nothing to
// resolve.
func TestPostSessionsNoRepoOnCluster(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.GitHubOrg = ""
	hub.SetJobManager(jm)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")

	rec := postSpawn(t, api, map[string]any{"no_repo": true, "runtime": "cluster", "kind": "agent", "initial_prompt": "hi"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if len(f.created) != 1 {
		t.Fatalf("created = %d Jobs, want 1", len(f.created))
	}
	assertNoRepoJob(t, f.created[0])
}

// A Job for a repository never carries BLERG_RUNNER_NO_REPO.
func TestCreateSessionJobWithRepoHasNoNoRepoEnv(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s-repo", Repo: "proj"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := jobEnv(t, f.created[0])["BLERG_RUNNER_NO_REPO"]; ok {
		t.Error("a repository session's Job has BLERG_RUNNER_NO_REPO")
	}
}

// The cluster start plan says what a no-repo pod actually does.
func TestClusterStartPlanNoRepo(t *testing.T) {
	for _, st := range clusterStartPlan("", nil) {
		if st.ID == protocol.StageClone {
			if st.Label != "Preparing workspace" || !strings.Contains(st.Detail, "no repository") {
				t.Errorf("no-repo clone stage = %+v", st)
			}
		}
	}
	for _, st := range clusterStartPlan("acme/widget", nil) {
		if st.ID == protocol.StageClone && (st.Label != "Cloning repo" || st.Detail != "acme/widget") {
			t.Errorf("repo clone stage = %+v", st)
		}
	}
}

// v1 start (and MCP start_session, which calls the same function): without
// no_repo a blank repo is "repo is required" exactly as before; with it,
// naming a repository anyway is refused.
func TestStartSessionNoRepoValidation(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{}
	hub.SetJobManager(newTestJobManager(t, f))
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	const combo = "no_repo cannot be combined with repo, git_url, provider, clone or new_repo"
	for _, tc := range []struct {
		name string
		req  runnerStartRequest
		want string
	}{
		{"blank repo", runnerStartRequest{Prompt: "go"}, "repo is required"},
		{"blank repo, no_repo false", runnerStartRequest{Prompt: "go", NoRepo: false}, "repo is required"},
		{"no_repo + repo", runnerStartRequest{Prompt: "go", NoRepo: true, Repo: "org/proj"}, combo},
		{"no_repo + git_url", runnerStartRequest{Prompt: "go", NoRepo: true, GitURL: "https://github.com/o/r.git"}, combo},
		{"no_repo + provider", runnerStartRequest{Prompt: "go", NoRepo: true, Provider: "gitlab"}, combo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, apiErr := api.StartSession(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, tc.req, "")
			if apiErr == nil {
				t.Fatal("start accepted, want 422")
			}
			if apiErr.Status != http.StatusUnprocessableEntity || apiErr.Message != tc.want {
				t.Errorf("error = %d %q, want 422 %q", apiErr.Status, apiErr.Message, tc.want)
			}
		})
	}
	if len(f.created) != 0 {
		t.Error("no Job should have been created")
	}
}

// A v1 no_repo start on the cluster creates a no-repo pod.
func TestStartSessionNoRepoOnCluster(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{}
	hub.SetJobManager(newTestJobManager(t, f))
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	resp, apiErr := api.StartSession(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind},
		runnerStartRequest{Prompt: "go", NoRepo: true}, "")
	if apiErr != nil {
		t.Fatalf("StartSession: %v", apiErr.Message)
	}
	if resp.SessionID == "" || len(f.created) != 1 {
		t.Fatalf("session %q, %d Jobs", resp.SessionID, len(f.created))
	}
	assertNoRepoJob(t, f.created[0])
}

// A v1 no_repo start on a daemon picks any connected daemon — preferring one
// with the sandbox image unless the bare host was asked for — and sends it a
// generated scratch folder to create.
func TestStartSessionNoRepoOnDaemon(t *testing.T) {
	api, hub := desktopRunnerAPI(t)
	plain := registerDaemon(t, api, hub, "00000000-0000-4000-8000-0000000000a1", "plain")
	boxed := registerDaemon(t, api, hub, "00000000-0000-4000-8000-0000000000a2", "boxed")
	boxed.SetSandboxAvailable(true)

	rec := runnerReq(t, api, http.MethodPost, "/api/runner/start", map[string]any{"no_repo": true, "prompt": "go"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d: %s", rec.Code, rec.Body.String())
	}
	msg := recvSpawn(t, boxed)
	if !msg.NoRepo || !msg.NewRepo || !scratch.Valid(msg.Repo) || !msg.Sandbox {
		t.Errorf("spawn = NoRepo %v NewRepo %v Repo %q Sandbox %v, want a sandboxed scratch spawn", msg.NoRepo, msg.NewRepo, msg.Repo, msg.Sandbox)
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	row, err := db.GetSession(context.Background(), api.dbPool, out["session_id"])
	if err != nil || row == nil {
		t.Fatalf("session row: %v", err)
	}
	if row.Repo != msg.Repo || !strings.HasSuffix(row.ProjectPath, "/"+msg.Repo) {
		t.Errorf("row repo %q path %q, want the scratch folder %q", row.Repo, row.ProjectPath, msg.Repo)
	}

	rec = runnerReq(t, api, http.MethodPost, "/api/runner/start", map[string]any{"no_repo": true, "prompt": "go", "runtime": "daemon"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start (daemon) = %d: %s", rec.Code, rec.Body.String())
	}
	if msg := recvSpawn(t, plain); !msg.NoRepo || msg.Sandbox {
		t.Errorf("runtime daemon: spawn NoRepo %v Sandbox %v, want an unsandboxed scratch spawn on the first daemon", msg.NoRepo, msg.Sandbox)
	}
}

// The server pre-creates a daemon session's row with the scratch folder it
// asked for; when the daemon had to create a different one (the name was
// taken), its session_started corrects the row — and an empty repo or path
// in a later session_started never erases a known one.
func TestSessionStartedRecordsTheScratchFolderActuallyCreated(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	daemonID := "00000000-0000-0000-0000-0000000000c1"
	sessionID := "00000000-0000-0000-0000-0000000000c2"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "starting", "/repos/.scratch-taken", ".scratch-taken", "T", ""); err != nil {
		t.Fatal(err)
	}
	const actual = ".scratch-taken-0123456789abcdef"
	HandleSessionStarted(ctx, NewHub(), pool, daemonID, protocol.SessionStarted{
		Type: "session_started", SessionID: sessionID, ProjectPath: "/repos/" + actual, Repo: actual, Title: "T",
	})
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.Repo != actual || row.ProjectPath != "/repos/"+actual {
		t.Errorf("row repo %q path %q, want the folder the daemon created", row.Repo, row.ProjectPath)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "", "", "T", ""); err != nil {
		t.Fatal(err)
	}
	if row, _ = db.GetSession(ctx, pool, sessionID); row.Repo != actual || row.ProjectPath != "/repos/"+actual {
		t.Errorf("an empty session_started erased repo/path: %q %q", row.Repo, row.ProjectPath)
	}
}

// A no-repo cluster session resumes as one: an empty workspace, nothing
// cloned, no git token.
func TestResumeNoRepoClusterSession(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	daemonID := "00000000-0000-0000-0000-0000000000b1"
	sessionID := "00000000-0000-0000-0000-0000000000b2"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "disconnected", clusterNoRepoWorkdir, "", "T", ""); err != nil {
		t.Fatal(err)
	}
	f := &fakeK8s{}
	hub := NewHub()
	hub.SetJobManager(newTestJobManager(t, f))
	resumeClusterSession(ctx, hub, pool, sessionID, "continue", "", testSID)
	if len(f.created) != 1 {
		t.Fatalf("created = %d Jobs, want 1", len(f.created))
	}
	assertNoRepoJob(t, f.created[0])
	if v, _ := jobEnv(t, f.created[0])["BLERG_RUNNER_RESUME"]["value"].(string); v != "1" {
		t.Error("resume Job is not marked BLERG_RUNNER_RESUME=1")
	}
}
