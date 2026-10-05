package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type newRepoFx struct {
	api     *API
	k8s     *fakeK8s
	created []string // "provider owner/name private"
	token   string   // "" = the account has no token
	exists  bool     // the visibility check answers "exists"
	err     error    // CreateRepo's answer
}

func newNewRepoFx(t *testing.T) *newRepoFx {
	t.Helper()
	fx := &newRepoFx{k8s: &fakeK8s{}, token: "tok-gh"}
	hub := NewHub()
	jm := newTestJobManager(t, fx.k8s)
	jm.GitHubOrg = ""
	jm.GitURLBase = "https://github.com"
	hub.SetJobManager(jm)
	fx.api = NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	fx.api.fetchGitToken = func(_ context.Context, accountID, kind string) ([]byte, bool, error) {
		if accountID != "user-1" {
			t.Errorf("token asked for %q", accountID)
		}
		if fx.token == "" {
			return nil, false, nil
		}
		return []byte(fx.token + "-" + kind), true, nil
	}
	fx.api.repoVisibility = func(context.Context, gitprovider.Provider, string, gitprovider.Ref) (bool, bool) {
		return true, fx.exists
	}
	fx.api.createRepo = func(_ context.Context, p gitprovider.Provider, token string, ref gitprovider.Ref, private bool) error {
		if !strings.HasPrefix(token, "tok-gh-") {
			t.Errorf("create used token %q", token)
		}
		fx.created = append(fx.created, p.ID()+" "+ref.FullName()+" "+map[bool]string{true: "private", false: "public"}[private])
		return fx.err
	}
	return fx
}

func (fx *newRepoFx) post(t *testing.T, body map[string]any) (int, string, []protocol.StartStage) {
	t.Helper()
	rec := postSpawn(t, fx.api, body)
	var out struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	var stages []protocol.StartStage
	if att := fx.api.hub.starts.get(out.SessionID); att != nil && out.SessionID != "" {
		stages = att.stages
	}
	return rec.Code, rec.Body.String(), stages
}

func stageByID(stages []protocol.StartStage, id string) *protocol.StartStage {
	for i := range stages {
		if stages[i].ID == id {
			return &stages[i]
		}
	}
	return nil
}

func newRepoBody(extra map[string]any) map[string]any {
	body := map[string]any{"repo": "me/new-proj", "runtime": "cluster", "kind": "agent", "new_repo": true}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestPostSessionsNewRepoCreatesAndStarts(t *testing.T) {
	fx := newNewRepoFx(t)
	code, body, stages := fx.post(t, newRepoBody(map[string]any{"visibility": "public"}))
	if code != http.StatusAccepted {
		t.Fatalf("status %d: %s", code, body)
	}
	if len(fx.created) != 1 || fx.created[0] != "github me/new-proj public" {
		t.Fatalf("created = %v", fx.created)
	}
	if got := planIDs(stages); !strings.HasPrefix(got, "repo,queued,") {
		t.Fatalf("plan = %s", got)
	}
	if st := stageByID(stages, protocol.StageRepo); st.State != protocol.StageStateDone || st.Detail != "github.com/me/new-proj · public" {
		t.Fatalf("repo stage = %+v", st)
	}
	if st := stageByID(stages, protocol.StageClone); st.Label != "Cloning new repository" {
		t.Fatalf("clone stage = %+v", st)
	}
	env := jobEnv(t, fx.k8s.created[0])
	if v, _ := env["BLERG_RUNNER_NEW_REPO"]["value"].(string); v != "1" {
		t.Fatalf("BLERG_RUNNER_NEW_REPO = %v", env["BLERG_RUNNER_NEW_REPO"])
	}
	if v, _ := env["BLERG_RUNNER_GIT_URL"]["value"].(string); v != "https://github.com/me/new-proj.git" {
		t.Fatalf("BLERG_RUNNER_GIT_URL = %v", env["BLERG_RUNNER_GIT_URL"])
	}
	raw, _ := json.Marshal(fx.k8s.created[0])
	if strings.Contains(string(raw), `"key":"BLERG_RUNNER_GIT_TOKEN","name":"blerg-runner-agent"`) {
		t.Fatalf("the operator's git token was offered to a new-repository pod: %s", raw)
	}
}

// Private is the default visibility.
func TestPostSessionsNewRepoDefaultsToPrivate(t *testing.T) {
	fx := newNewRepoFx(t)
	if code, body, _ := fx.post(t, newRepoBody(nil)); code != http.StatusAccepted {
		t.Fatalf("status %d: %s", code, body)
	}
	if len(fx.created) != 1 || fx.created[0] != "github me/new-proj private" {
		t.Fatalf("created = %v", fx.created)
	}
}

// An existing repository — seen by the check, or reported by the create — is
// a 409 and nothing starts: never a fallback into it.
func TestPostSessionsNewRepoRefusesAnExistingRepository(t *testing.T) {
	fx := newNewRepoFx(t)
	fx.exists = true
	if code, body, _ := fx.post(t, newRepoBody(nil)); code != http.StatusConflict || !strings.Contains(body, "already exists") {
		t.Fatalf("visible existing repo: %d %s", code, body)
	}
	fx = newNewRepoFx(t)
	fx.err = gitprovider.ErrExists
	if code, body, _ := fx.post(t, newRepoBody(nil)); code != http.StatusConflict || !strings.Contains(body, "already exists") {
		t.Fatalf("create said exists: %d %s", code, body)
	}
	if len(fx.k8s.created) != 0 {
		t.Fatal("a Job was created for a repository that already exists")
	}
}

// No token, or a token that may not create: the session starts anyway, with a
// warning stage saying why, the NEW_REPO flag, the recorded clone URL, and
// still no operator token.
func TestPostSessionsNewRepoFallsBackWhenItCannotCreate(t *testing.T) {
	for name, setup := range map[string]struct {
		set    func(*newRepoFx)
		reason string
	}{
		"no token":  {func(fx *newRepoFx) { fx.token = "" }, "no github token in Settings"},
		"forbidden": {func(fx *newRepoFx) { fx.err = &gitprovider.StatusError{Provider: "github", Status: 403} }, "your github token cannot create repositories under me"},
		"outage":    {func(fx *newRepoFx) { fx.err = &gitprovider.StatusError{Provider: "github", Status: 502} }, "github returned 502"},
		"network":   {func(fx *newRepoFx) { fx.err = errors.New("dial tcp: timeout") }, "github could not be reached"},
	} {
		fx := newNewRepoFx(t)
		setup.set(fx)
		code, body, stages := fx.post(t, newRepoBody(nil))
		if code != http.StatusAccepted {
			t.Fatalf("%s: status %d: %s", name, code, body)
		}
		st := stageByID(stages, protocol.StageRepo)
		if st == nil || st.State != protocol.StageStateWarning || st.Detail != "not created: "+setup.reason || !strings.Contains(st.Hint, "Create me/new-proj on github.com") {
			t.Fatalf("%s: repo stage = %+v", name, st)
		}
		env := jobEnv(t, fx.k8s.created[0])
		if v, _ := env["BLERG_RUNNER_NEW_REPO"]["value"].(string); v != "1" {
			t.Fatalf("%s: NEW_REPO not set", name)
		}
		if v, _ := env["BLERG_RUNNER_GIT_URL"]["value"].(string); v != "https://github.com/me/new-proj.git" {
			t.Fatalf("%s: GIT_URL = %v", name, env["BLERG_RUNNER_GIT_URL"])
		}
		raw, _ := json.Marshal(fx.k8s.created[0])
		if strings.Contains(string(raw), `"key":"BLERG_RUNNER_GIT_TOKEN","name":"blerg-runner-agent"`) {
			t.Fatalf("%s: operator git token offered: %s", name, raw)
		}
	}
}

func TestPostSessionsNewRepoValidation(t *testing.T) {
	fx := newNewRepoFx(t)
	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{newRepoBody(map[string]any{"repo": "bare"}), "owner/name"},
		{newRepoBody(map[string]any{"visibility": "internal"}), "visibility"},
		{newRepoBody(map[string]any{"git_url": "https://github.com/me/new-proj"}), "cannot be combined"},
		{newRepoBody(map[string]any{"clone": true, "provider": "github"}), "cannot be combined"},
		{newRepoBody(map[string]any{"no_repo": true}), "no_repo cannot be combined"},
		{newRepoBody(map[string]any{"repo": "grp/sub/x", "provider": "gitlab"}), "invalid repo name"},
	} {
		code, body, _ := fx.post(t, c.body)
		if code != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) {
			t.Errorf("%v: %d %s (want 422 containing %q)", c.body, code, body, c.want)
		}
	}
	if len(fx.created) != 0 || len(fx.k8s.created) != 0 {
		t.Fatal("an invalid request created something")
	}
}

// The Job spec alone (what a resume rebuilds from the row) withholds the
// operator token and sets the flag.
func TestCreateSessionJobNewRepoWithholdsOperatorGitToken(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s-new", Repo: "org/new", NewRepo: true, NoOperatorGitToken: true,
		GitURL: "https://github.com/org/new.git", Resume: true}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(raw), `"BLERG_RUNNER_NEW_REPO","value":"1"`) || !strings.Contains(string(raw), `"BLERG_RUNNER_RESUME","value":"1"`) {
		t.Fatalf("env: %s", raw)
	}
	if strings.Contains(string(raw), `"key":"BLERG_RUNNER_GIT_TOKEN"`) {
		t.Fatalf("operator git token offered on resume: %s", raw)
	}
	// And without the flag the operator token on its own base host is offered, as before.
	f2 := &fakeK8s{}
	jm2 := newTestJobManager(t, f2)
	if err := jm2.CreateSessionJob(SessionJobSpec{SessionID: "s-old", Repo: "org/old", GitURL: "https://github.com/org/old.git"}); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(f2.created[0])
	if !strings.Contains(string(raw), `"key":"BLERG_RUNNER_GIT_TOKEN"`) {
		t.Fatalf("operator git token no longer offered to an ordinary session: %s", raw)
	}
}

// resumeClusterSession, end to end through the sessions row: a "New repository" session resumes
// with the flag, the recorded clone URL and no operator git token.
func TestResumeClusterSessionCarriesNewRepo(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration test")
	}
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	daemonID := "00000000-0000-0000-0000-0000000000a1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	sessionID := "00000000-0000-0000-0000-0000000000c1"
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "disconnected", "/workspace/me/new-proj", "me/new-proj", "T", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionGitURL(ctx, pool, sessionID, "https://github.com/me/new-proj.git"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionNewRepo(ctx, pool, sessionID); err != nil {
		t.Fatal(err)
	}
	if !db.GetSessionNewRepo(ctx, pool, sessionID) {
		t.Fatal("new_repo not recorded")
	}
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	hub := NewHub()
	hub.SetJobManager(jm)
	resumeClusterSession(ctx, hub, pool, sessionID, "continue", "someone", testSID)
	if len(f.created) != 1 {
		t.Fatalf("created %d Jobs", len(f.created))
	}
	env := jobEnv(t, f.created[0])
	if v, _ := env["BLERG_RUNNER_NEW_REPO"]["value"].(string); v != "1" {
		t.Fatal("resume lost BLERG_RUNNER_NEW_REPO")
	}
	if v, _ := env["BLERG_RUNNER_GIT_URL"]["value"].(string); v != "https://github.com/me/new-proj.git" {
		t.Fatalf("resume clone URL = %q", v)
	}
	raw, _ := json.Marshal(f.created[0])
	if strings.Contains(string(raw), `"key":"BLERG_RUNNER_GIT_TOKEN"`) {
		t.Fatalf("operator git token offered on resume: %s", raw)
	}
}

func TestNewRepoHostProblem(t *testing.T) {
	if got := newRepoHostProblem("https://github.com/me/x.git", ""); got != "" {
		t.Errorf("same host: %q", got)
	}
	if got := newRepoHostProblem("https://git.internal.example/me/x.git", "github"); got == "" {
		t.Error("an operator base host must refuse a new repository")
	}
}
