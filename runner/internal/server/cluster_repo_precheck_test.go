package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClusterRepositoryDeniedBeforeJob(t *testing.T) {
	var reads atomic.Int64
	git := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("private fixture must never reach an error"))
	}))
	defer git.Close()
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.repositoryRead = nil // exercise the real HTTP read check
	jm.CoreURL, jm.CoreInternalKey = "", ""
	jm.GitURLBase = git.URL + "/repos"
	jm.ExplicitGitURLBase = jm.GitURLBase
	err := jm.CreateSessionJob(SessionJobSpec{SessionID: "repo-check", Repo: "private", Engine: "claude"})
	if err == nil {
		t.Fatal("unreadable repository was accepted")
	}
	if strings.Contains(err.Error(), "private fixture") || !strings.Contains(err.Error(), "repository") {
		t.Fatal("repository refusal must be actionable and omit the upstream body")
	}
	if reads.Load() != 1 {
		t.Fatal("did not check the repository read endpoint")
	}
	if len(f.created) != 0 {
		t.Fatal("a Job was created for an unreadable repository")
	}
}

// Without a named desktop, no connected desktop's folder decides what a cluster start clones.
func TestClusterLaunchDoesNotResolveDesktopFolderWithoutDaemonHint(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "desktop-a", Name: "Desktop", send: make(chan []byte, 8)}
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	dc.SetRepoRemotes(map[string]string{"app": "example-org/app"})
	hub.Register(dc)
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.GitHubOrg = ""
	hub.SetJobManager(jm)
	api := NewAPI(hub, nil, "fixture-daemon-key", nil, "")
	token := enableBrowserAuth(t, api)("session.start")
	r := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"repo":"app","runtime":"cluster","kind":"agent"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	api.HandlePostSessions(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("hint-less start: %d %s, want 422", w.Code, w.Body.String())
	}
	if len(f.created) != 0 {
		t.Fatal("a Job was created from another desktop's folder")
	}
}

func TestClusterChildUsesParentDesktopRemote(t *testing.T) {
	cx := newDesktopClusterFx(t)
	cx.hub.GetHostDaemons()[0].SetRepoRemotes(map[string]string{"app": "example-org/app"})
	cx.clusterRule("claude")
	code, out, _ := cx.postChild(cx.parent, cx.parentTok, clusterBody(map[string]any{"repo": "app"}), newUUID())
	if code != http.StatusCreated {
		t.Fatalf("resolved child: %d %v", code, out)
	}
	child, _ := out["child"].(map[string]any)
	if child["repo"] != "example-org/app" {
		t.Fatal("child did not retain the resolved repository")
	}
	raw, _ := json.Marshal(cx.k8s.created)
	if !strings.Contains(string(raw), "https://github.com/example-org/app.git") {
		t.Fatal("child cloned the wrong repository")
	}
}

// The board and cron contract names no desktop for a cluster start, so a desktop's folder never
// decides the repository: with no default org a bare name is refused, and with one the org wins.
func TestRunnerContractDoesNotResolveDesktopFolder(t *testing.T) {
	api, _ := runnerTestAPI(t)
	dc := &DaemonConn{ID: "desktop-a", Name: "Desktop", send: make(chan []byte, 8)}
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	dc.SetRepoRemotes(map[string]string{"app": "example-org/app"})
	api.hub.Register(dc)
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.GitHubOrg = ""
	api.hub.SetJobManager(jm)
	_, problem := api.StartSession(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, runnerStartRequest{Repo: "app", Runtime: "cluster", Engine: "claude"}, "")
	if problem == nil || problem.Status != http.StatusUnprocessableEntity || len(f.created) != 0 {
		t.Fatalf("bare name resolved from a desktop: %v", problem)
	}
}

// A configured default org is the install's answer for a bare name. Desktops that report the same
// folder name with other (and conflicting) origins neither redirect the clone nor break the start.
func TestRunnerContractDefaultOrgWinsOverDesktopOrigins(t *testing.T) {
	api, _ := runnerTestAPI(t)
	for i, origin := range []string{"someone/app", "org/app"} {
		dc := &DaemonConn{ID: "desktop-" + string(rune('a'+i)), Name: "Desktop", send: make(chan []byte, 8)}
		dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
		dc.SetRepoRemotes(map[string]string{"app": origin})
		api.hub.Register(dc)
	}
	f := &fakeK8s{}
	jm := newTestJobManager(t, f) // default org "org", base https://github.com/org
	api.hub.SetJobManager(jm)
	if _, problem := api.StartSession(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, runnerStartRequest{Repo: "app", Runtime: "cluster", Engine: "claude"}, ""); problem != nil {
		t.Fatalf("start: %v", problem)
	}
	raw, _ := json.Marshal(f.created)
	if !strings.Contains(string(raw), "https://github.com/org/app.git") || strings.Contains(string(raw), "someone/app") {
		t.Fatal("the default org did not decide the repository")
	}
}

func TestClusterChildRetainsGitLabOrigin(t *testing.T) {
	cx := newDesktopClusterFx(t)
	cx.hub.GetHostDaemons()[0].SetRepoOrigins(map[string]protocol.RepoOrigin{"app": {Provider: "gitlab", FullName: "example-group/app"}}, nil)
	cx.clusterRule("claude")
	code, out, _ := cx.postChild(cx.parent, cx.parentTok, clusterBody(map[string]any{"repo": "app"}), newUUID())
	if code != http.StatusCreated {
		t.Fatalf("GitLab child: %d %v", code, out)
	}
	raw, _ := json.Marshal(cx.k8s.created)
	if !strings.Contains(string(raw), "https://gitlab.com/example-group/app.git") {
		t.Fatal("cluster child changed the reported git provider")
	}
}

// A bare folder name resolved from the parent's desktop is stored with its provider; deciding the
// request must start the child, not refuse the stored provider as a caller-supplied field.
func TestClusterChildBareNameAskThenDecideStarts(t *testing.T) {
	cx := newDesktopClusterFx(t)
	cx.hub.GetHostDaemons()[0].SetRepoRemotes(map[string]string{"app": "example-org/app"})
	id := cx.ask(cx.parent, cx.parentTok, clusterBody(map[string]any{"repo": "app"}))
	code, out := cx.decide(id, cx.human("owner-1"), choice("once"))
	child := asString(out["child_session_id"])
	if code != http.StatusOK || child == "" || asString(out["start_error"]) != "" {
		t.Fatalf("decide = %d %v", code, out)
	}
	raw, _ := json.Marshal(cx.job(child))
	if !strings.Contains(string(raw), "https://github.com/example-org/app.git") {
		t.Fatal("the decided child cloned the wrong repository")
	}
}
