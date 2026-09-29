package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/gitremote"
)

// The daemon's remote report is input: only plain folder names mapping to a
// valid GitHub org/name survive, and the map is capped.
func TestSetRepoRemotesSanitizes(t *testing.T) {
	dc := &DaemonConn{}
	if _, ok := dc.RepoRemote("x"); ok {
		t.Fatal("zero-value DaemonConn must know no remotes")
	}
	dc.SetRepoRemotes(map[string]string{
		"entertainment": "example-org/example-repo",
		"widget":        "example-org/widget",
		"bare-value":    "example-repo",
		"three-segs":    "a/b/c",
		"dotted":        "example-org/.hidden",
		"traversal":     "../etc",
		"a/b":           "example-org/example-repo",
		"..":            "example-org/example-repo",
		".hidden":       "example-org/example-repo",
		"":              "example-org/example-repo",
		"newline":       "example-org/exa\nmple",
		"https-url":     "https://github.com/example-org/example-repo",
	})
	for name, want := range map[string]string{"entertainment": "example-org/example-repo", "widget": "example-org/widget"} {
		if got, ok := dc.RepoRemote(name); !ok || got != want {
			t.Errorf("RepoRemote(%q) = %q,%v; want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"bare-value", "three-segs", "dotted", "traversal", "a/b", "..", ".hidden", "", "newline", "https-url"} {
		if got, ok := dc.RepoRemote(name); ok {
			t.Errorf("RepoRemote(%q) = %q, want dropped", name, got)
		}
	}
	// A later report replaces, rather than merges with, the earlier one.
	dc.SetRepoRemotes(nil)
	if _, ok := dc.RepoRemote("entertainment"); ok {
		t.Error("nil report must clear known remotes")
	}

	big := make(map[string]string, gitremote.MaxRemotes+10)
	for i := 0; i < gitremote.MaxRemotes+10; i++ {
		big[fmt.Sprintf("r%d", i)] = fmt.Sprintf("example-org/r%d", i)
	}
	dc.SetRepoRemotes(big)
	dc.liveMu.RLock()
	n := len(dc.repoRemotes)
	dc.liveMu.RUnlock()
	if n != gitremote.MaxRemotes {
		t.Errorf("kept %d remotes, want cap %d", n, gitremote.MaxRemotes)
	}
}

func getRepos(t *testing.T, api *API) map[string]RepoInfo {
	t.Helper()
	tok := enableBrowserAuth(t, api)("session.start")
	req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandleGetRepos(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp reposResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]RepoInfo, len(resp.Repos))
	for _, r := range resp.Repos {
		out[r.Name] = r
	}
	return out
}

// A local-only folder takes its FullName from the daemon-reported remote
// (even when the folder's name differs from the repository's); an unknown one
// stays bare and is flagged not cloneable.
func TestHandleGetReposLocalFolderUsesReportedRemote(t *testing.T) {
	hub := NewHub()
	d1 := &DaemonConn{ID: "d1", Name: "desk"}
	d1.SetCheckedOutRepos([]string{"entertainment", "scratch", "shared", "split"})
	d1.SetRepoRemotes(map[string]string{
		"entertainment": "example-org/example-repo",
		"shared":        "example-org/shared",
		"split":         "example-org/split-a",
	})
	hub.Register(d1)
	d2 := &DaemonConn{ID: "d2", Name: "laptop"}
	d2.SetCheckedOutRepos([]string{"shared", "split"})
	d2.SetRepoRemotes(map[string]string{
		"shared": "example-org/shared",
		"split":  "example-org/split-b", // same folder name, different repo
	})
	hub.Register(d2)
	api := NewAPI(hub, nil, "", nil, "")

	repos := getRepos(t, api)

	e := repos["entertainment"]
	if e.FullName != "example-org/example-repo" || e.Remote != "example-org/example-repo" {
		t.Errorf("entertainment = %+v, want FullName/Remote example-org/example-repo", e)
	}
	if e.Cloneable == nil || !*e.Cloneable || !e.IsLocal {
		t.Errorf("entertainment should be local and cloneable: %+v", e)
	}

	s := repos["scratch"]
	if s.FullName != "scratch" || s.Remote != "" {
		t.Errorf("scratch = %+v, want bare FullName and no Remote", s)
	}
	if s.Cloneable == nil || *s.Cloneable {
		t.Errorf("scratch must be flagged cloneable=false: %+v", s)
	}

	if sh := repos["shared"]; sh.FullName != "example-org/shared" || sh.Cloneable == nil || !*sh.Cloneable {
		t.Errorf("shared (daemons agree) = %+v, want example-org/shared", sh)
	}
	if sp := repos["split"]; sp.FullName != "split" || sp.Cloneable == nil || *sp.Cloneable {
		t.Errorf("split (daemons disagree) = %+v, want bare + not cloneable", sp)
	}
}

// Old daemons send no repo_remotes: their folders keep the bare FullName they
// always had, now explicitly flagged cloneable=false.
func TestHandleGetReposWithoutRemoteReport(t *testing.T) {
	hub := NewHub()
	d := &DaemonConn{ID: "d1", Name: "desk"}
	d.SetCheckedOutRepos([]string{"example-repo"})
	hub.Register(d)
	api := NewAPI(hub, nil, "", nil, "")
	r := getRepos(t, api)["example-repo"]
	if r.FullName != "example-repo" || r.Cloneable == nil || *r.Cloneable {
		t.Errorf("got %+v, want bare FullName and cloneable=false", r)
	}
}

// Server backstop: a bare cluster repo with no org configured resolves via
// the explicitly requested daemon's reported remote.
func TestPostSessionsClusterResolvesBareRepoViaRequestedDaemon(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.GitHubOrg = ""
	jm.GitURLBase = "https://github.com"
	hub.SetJobManager(jm)
	d := &DaemonConn{ID: "d1", Name: "desk", send: make(chan []byte, 8)}
	d.SetCheckedOutRepos([]string{"entertainment"})
	d.SetRepoRemotes(map[string]string{"entertainment": "example-org/example-repo"})
	hub.Register(d)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")

	rec := postSpawn(t, api, map[string]any{"repo": "entertainment", "runtime": "cluster", "kind": "agent", "daemon_id": "d1"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if len(f.created) != 1 {
		t.Fatalf("created = %d, want 1 Job", len(f.created))
	}
	raw, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(raw), "example-org/example-repo") {
		t.Errorf("Job should clone the reported remote: %s", raw)
	}
}

// The backstop never guesses across daemons, and needs a daemon_id.
func TestPostSessionsClusterBackstopOnlyUsesRequestedDaemon(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.GitHubOrg = ""
	hub.SetJobManager(jm)
	other := &DaemonConn{ID: "d2", Name: "laptop", send: make(chan []byte, 8)}
	other.SetCheckedOutRepos([]string{"entertainment"})
	other.SetRepoRemotes(map[string]string{"entertainment": "example-org/example-repo"})
	hub.Register(other)
	asked := &DaemonConn{ID: "d1", Name: "desk", send: make(chan []byte, 8)}
	asked.SetCheckedOutRepos([]string{"entertainment"})
	hub.Register(asked)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")

	for _, body := range []map[string]any{
		{"repo": "entertainment", "runtime": "cluster", "kind": "agent", "daemon_id": "d1"},
		{"repo": "entertainment", "runtime": "cluster", "kind": "agent"},
		{"repo": "entertainment", "runtime": "cluster", "kind": "agent", "daemon_id": "nope"},
	} {
		rec := postSpawn(t, api, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%v: status %d, want 422: %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(f.created) != 0 {
		t.Error("no Job should have been created")
	}
}
