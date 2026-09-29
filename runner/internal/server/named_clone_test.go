package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A daemon that honours CloneFrom and every provider, with a token fetcher
// that records what it was asked for.
func namedCloneAPI(t *testing.T) (*API, *DaemonConn, *[]string) {
	t.Helper()
	api, dc := runtimeTestAPI(t)
	dc.SetGitProviders([]string{"github", "gitlab"})
	dc.SetCloneFrom(true)
	var asked []string
	api.fetchGitToken = func(_ context.Context, accountID, kind string) ([]byte, bool, error) {
		asked = append(asked, kind)
		return []byte("  tok-" + kind + "-s3cret\n"), true, nil
	}
	// Private unless a test says otherwise: never the network.
	api.repoVisibility = func(context.Context, gitprovider.Provider, string, gitprovider.Ref) (bool, bool) { return true, true }
	return api, dc, &asked
}

// Who may hold the caller's token: a private repository's token goes only
// with a Local sandbox session, or to a daemon whose owner opted in; a
// This machine launch of a private repository is refused with the reason; a
// public repository's clone never carries a token.
func TestPostSessionsNamedCloneTokenOnlyWhereItMayGo(t *testing.T) {
	body := func(runtime string) map[string]any {
		return map[string]any{"daemon_id": "d1", "git_url": "https://github.com/acme/secret", "kind": "agent", "runtime": runtime}
	}
	vis := func(private, known bool) func(context.Context, gitprovider.Provider, string, gitprovider.Ref) (bool, bool) {
		return func(context.Context, gitprovider.Provider, string, gitprovider.Ref) (bool, bool) {
			return private, known
		}
	}

	// Private, This machine, no opt-in: refused, nothing sent.
	api, dc, _ := namedCloneAPI(t)
	rec := postSpawn(t, api, body("daemon"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Local sandbox") ||
		!strings.Contains(rec.Body.String(), "BLERG_RUNNER_ALLOW_HOST_CREDENTIAL_CLONE") {
		t.Fatalf("private on This machine: status %d body %s, want 422 explaining", rec.Code, rec.Body.String())
	}
	noSpawn(t, dc, "private on This machine")

	// Private, Local sandbox: the token goes, for the in-sandbox clone.
	if rec := postSpawn(t, api, body("docker")); rec.Code != http.StatusAccepted {
		t.Fatalf("private in sandbox: status %d: %s", rec.Code, rec.Body.String())
	}
	if msg := recvNamedSpawn(t, dc); msg.GitToken != "tok-github-s3cret" || !msg.Sandbox {
		t.Errorf("private in sandbox: token %q sandbox %v", msg.GitToken, msg.Sandbox)
	}

	// Private, This machine, daemon opted in: the token goes.
	dc.SetAllowHostCredentialClone(true)
	if rec := postSpawn(t, api, body("daemon")); rec.Code != http.StatusAccepted {
		t.Fatalf("private on an opted-in daemon: status %d: %s", rec.Code, rec.Body.String())
	}
	if msg := recvNamedSpawn(t, dc); msg.GitToken != "tok-github-s3cret" {
		t.Errorf("opted-in daemon: token %q", msg.GitToken)
	}

	// Public: no token, on any runtime.
	for _, rt := range []string{"daemon", "docker"} {
		api, dc, _ := namedCloneAPI(t)
		api.repoVisibility = vis(false, true)
		if rec := postSpawn(t, api, body(rt)); rec.Code != http.StatusAccepted {
			t.Fatalf("public %s: status %d: %s", rt, rec.Code, rec.Body.String())
		}
		if msg := recvNamedSpawn(t, dc); msg.GitToken != "" {
			t.Errorf("public %s: a token went with a public clone", rt)
		}
	}

	// Unknown: sent where a token may go, left out elsewhere — never refused.
	for rt, want := range map[string]string{"daemon": "", "docker": "tok-github-s3cret"} {
		api, dc, _ := namedCloneAPI(t)
		api.repoVisibility = vis(false, false)
		if rec := postSpawn(t, api, body(rt)); rec.Code != http.StatusAccepted {
			t.Fatalf("unknown %s: status %d: %s", rt, rec.Code, rec.Body.String())
		}
		if msg := recvNamedSpawn(t, dc); msg.GitToken != want {
			t.Errorf("unknown %s: token %q, want %q", rt, msg.GitToken, want)
		}
	}
}

func recvNamedSpawn(t *testing.T, dc *DaemonConn) protocol.SpawnSession {
	t.Helper()
	select {
	case raw := <-dc.send:
		var msg protocol.SpawnSession
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatal(err)
		}
		return msg
	default:
		t.Fatal("no spawn_session reached the daemon")
	}
	return protocol.SpawnSession{}
}

func noSpawn(t *testing.T, dc *DaemonConn, what any) {
	t.Helper()
	select {
	case raw := <-dc.send:
		t.Errorf("%v: message reached the daemon: %s", what, raw)
	default:
	}
}

// provider + owner/name are required together for a clone; a URL must be on
// a registered provider and agree with whatever else was said; nothing half-
// valid ever reaches the daemon.
func TestPostSessionsNamedCloneValidation(t *testing.T) {
	api, dc, asked := namedCloneAPI(t)
	base := func(extra map[string]any) map[string]any {
		b := map[string]any{"daemon_id": "d1", "kind": "agent", "runtime": "daemon"}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	for _, body := range []map[string]any{
		base(map[string]any{"repo": "torvalds/linux", "clone": true}),               // no provider
		base(map[string]any{"repo": "linux", "provider": "github", "clone": true}),  // not owner/name
		base(map[string]any{"repo": "a/b", "provider": "bitbucket", "clone": true}), // unknown provider
		base(map[string]any{"repo": "a/b", "provider": "github", "clone": true, "new_repo": true}),
		base(map[string]any{"git_url": "https://example.com/a/b.git"}),                       // no provider there
		base(map[string]any{"git_url": "https://github.com/a/b/c"}),                          // not a repository URL
		base(map[string]any{"git_url": "https://github.com/a/b", "provider": "gitlab"}),      // host/provider mismatch
		base(map[string]any{"git_url": "git@gitlab.com:grp/tool.git", "provider": "github"}), // host/provider mismatch
		base(map[string]any{"git_url": "https://github.com/a/b", "repo": "a/other"}),         // two repositories
		base(map[string]any{"git_url": "https://gitlab.com/grp/sub/tool"}),                   // nested group
		base(map[string]any{"git_url": "https://x-access-token:leak@github.com/a/b?x=1"}),    // query: refused
	} {
		rec := postSpawn(t, api, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%v: status %d, want 422: %s", body, rec.Code, rec.Body.String())
		}
		noSpawn(t, dc, body)
	}
	if len(*asked) != 0 {
		t.Errorf("a refused request fetched tokens: %v", *asked)
	}
}

// Every URL form resolves to its provider and owner/name; the daemon gets a
// folder, the repository to clone into it, and the caller's own token for
// that provider — trimmed, and for that provider only.
func TestPostSessionsNamedCloneFromURL(t *testing.T) {
	cases := []struct {
		body                   map[string]any
		provider, full, folder string
	}{
		{map[string]any{"git_url": "https://github.com/torvalds/linux"}, "github", "torvalds/linux", "linux"},
		{map[string]any{"git_url": "https://github.com/torvalds/linux.git"}, "github", "torvalds/linux", "linux"},
		{map[string]any{"git_url": "git@github.com:octo/tiny.git"}, "github", "octo/tiny", "tiny"},
		{map[string]any{"git_url": "ssh://git@gitlab.com/grp/tool.git"}, "gitlab", "grp/tool", "tool"},
		{map[string]any{"git_url": "https://gitlab.com/grp/tool", "provider": "gitlab", "repo": "GRP/Tool"}, "gitlab", "grp/tool", "tool"},
		{map[string]any{"repo": "grp/tool", "provider": "gitlab", "clone": true}, "gitlab", "grp/tool", "tool"},
		{map[string]any{"repo": "octo/tiny", "provider": "github", "clone": true}, "github", "octo/tiny", "tiny"},
	}
	for _, c := range cases {
		api, dc, asked := namedCloneAPI(t)
		body := map[string]any{"daemon_id": "d1", "kind": "agent", "runtime": "docker"}
		for k, v := range c.body {
			body[k] = v
		}
		rec := postSpawn(t, api, body)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%v: status %d: %s", c.body, rec.Code, rec.Body.String())
		}
		msg := recvNamedSpawn(t, dc)
		if msg.Provider != c.provider || msg.CloneFrom != c.full || msg.Repo != c.folder || msg.NewRepo {
			t.Errorf("%v: spawn = provider %q clone_from %q repo %q new_repo %v, want %s %s %s",
				c.body, msg.Provider, msg.CloneFrom, msg.Repo, msg.NewRepo, c.provider, c.full, c.folder)
		}
		if want := "tok-" + c.provider + "-s3cret"; msg.GitToken != want {
			t.Errorf("%v: git token %q, want the caller's own %s token, trimmed", c.body, msg.GitToken, c.provider)
		}
		if len(*asked) != 1 || (*asked)[0] != c.provider {
			t.Errorf("%v: fetched tokens %v, want exactly [%s]", c.body, *asked, c.provider)
		}
	}
}

// A public repository needs no token: with none stored (or core not wired)
// the spawn still goes out, without one.
func TestPostSessionsNamedCloneWithoutToken(t *testing.T) {
	api, dc := runtimeTestAPI(t)
	dc.SetCloneFrom(true)
	api.fetchGitToken = func(context.Context, string, string) ([]byte, bool, error) { return nil, false, nil }
	rec := postSpawn(t, api, map[string]any{"daemon_id": "d1", "git_url": "https://github.com/torvalds/linux", "kind": "agent", "runtime": "daemon"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if msg := recvNamedSpawn(t, dc); msg.GitToken != "" || msg.CloneFrom != "torvalds/linux" {
		t.Errorf("spawn = clone_from %q token %q, want torvalds/linux and no token", msg.CloneFrom, msg.GitToken)
	}

	// And with nothing configured at all: the real fetcher degrades to none.
	api2, dc2 := runtimeTestAPI(t)
	dc2.SetCloneFrom(true)
	if rec := postSpawn(t, api2, map[string]any{"daemon_id": "d1", "git_url": "https://github.com/torvalds/linux", "kind": "agent", "runtime": "daemon"}); rec.Code != http.StatusAccepted {
		t.Fatalf("unconfigured core: status %d: %s", rec.Code, rec.Body.String())
	}
	if msg := recvNamedSpawn(t, dc2); msg.GitToken != "" {
		t.Errorf("unconfigured core produced a token: %q", msg.GitToken)
	}
}

// A daemon that predates CloneFrom would read the folder as a GitHub repo to
// clone (the wrong repository): it is refused and sent nothing — no token.
func TestPostSessionsNamedCloneRefusesAnOlderDaemon(t *testing.T) {
	api, dc, asked := namedCloneAPI(t)
	dc.SetCloneFrom(false)
	rec := postSpawn(t, api, map[string]any{"daemon_id": "d1", "git_url": "https://github.com/torvalds/linux", "kind": "agent", "runtime": "daemon"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "too old") {
		t.Fatalf("status %d body %s, want 422 too old", rec.Code, rec.Body.String())
	}
	noSpawn(t, dc, "older daemon")
	if len(*asked) != 0 {
		t.Errorf("fetched a token for a spawn that was refused: %v", *asked)
	}
}

// A caller that sends none of the new fields gets exactly today's spawn: no
// CloneFrom, no token, no token fetch.
func TestPostSessionsWithoutNamedCloneIsUnchanged(t *testing.T) {
	api, dc, asked := namedCloneAPI(t)
	for _, body := range []map[string]any{
		{"daemon_id": "d1", "repo": "my-app", "kind": "agent", "runtime": "daemon"},
		{"daemon_id": "d1", "repo": "fresh", "new_repo": true, "kind": "agent", "runtime": "daemon"},
		{"daemon_id": "d1", "repo": "grp/tool", "provider": "gitlab", "kind": "agent", "runtime": "daemon"},
	} {
		if rec := postSpawn(t, api, body); rec.Code != http.StatusAccepted {
			t.Fatalf("%v: status %d: %s", body, rec.Code, rec.Body.String())
		}
		msg := recvNamedSpawn(t, dc)
		if msg.CloneFrom != "" || msg.GitToken != "" || msg.Repo != body["repo"] {
			t.Errorf("%v: spawn = repo %q clone_from %q token %q", body, msg.Repo, msg.CloneFrom, msg.GitToken)
		}
	}
	if len(*asked) != 0 {
		t.Errorf("a legacy spawn fetched tokens: %v", *asked)
	}
}

// The folder rule, and the token only when a clone will happen.
func TestCloneFolderFor(t *testing.T) {
	ref := gitprovider.Ref{Provider: "github", Owner: "torvalds", Name: "linux"}
	cases := []struct {
		name    string
		folders []string
		origins map[string]protocol.RepoOrigin
		want    string
		present bool
		problem bool
	}{
		{"free", nil, nil, "linux", false, false},
		{"same repo already there", []string{"linux"}, map[string]protocol.RepoOrigin{"linux": {Provider: "github", FullName: "Torvalds/Linux"}}, "linux", true, false},
		{"name holds another repo", []string{"linux"}, map[string]protocol.RepoOrigin{"linux": {Provider: "github", FullName: "someone/linux"}}, "torvalds-linux", false, false},
		{"name holds another provider's", []string{"linux"}, map[string]protocol.RepoOrigin{"linux": {Provider: "gitlab", FullName: "torvalds/linux"}}, "torvalds-linux", false, false},
		{"name holds an unknown folder", []string{"linux"}, nil, "torvalds-linux", false, false},
		{"fallback already holds it", []string{"linux", "torvalds-linux"}, map[string]protocol.RepoOrigin{"torvalds-linux": {Provider: "github", FullName: "torvalds/linux"}}, "torvalds-linux", true, false},
		{"fallback holds it, name free again", []string{"torvalds-linux"}, map[string]protocol.RepoOrigin{"torvalds-linux": {Provider: "github", FullName: "torvalds/linux"}}, "torvalds-linux", true, false},
		{"both taken", []string{"linux", "torvalds-linux"}, nil, "", false, true},
	}
	for _, c := range cases {
		d := &DaemonConn{Name: "desk"}
		d.SetCheckedOutRepos(c.folders)
		d.SetRepoOrigins(c.origins, nil)
		got, present, problem := cloneFolderFor(d, ref)
		if got != c.want || present != c.present || (problem != "") != c.problem {
			t.Errorf("%s: = %q present=%v problem=%q, want %q present=%v problem=%v", c.name, got, present, problem, c.want, c.present, c.problem)
		}
	}

	// Already there: sent to that folder, and no token fetched or sent.
	api, dc, asked := namedCloneAPI(t)
	dc.SetCheckedOutRepos([]string{"linux"})
	dc.SetRepoOrigins(map[string]protocol.RepoOrigin{"linux": {Provider: "github", FullName: "torvalds/linux"}}, nil)
	if rec := postSpawn(t, api, map[string]any{"daemon_id": "d1", "git_url": "https://github.com/torvalds/linux", "kind": "agent", "runtime": "daemon"}); rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if msg := recvNamedSpawn(t, dc); msg.Repo != "linux" || msg.GitToken != "" {
		t.Errorf("spawn = repo %q token %q, want linux with no token", msg.Repo, msg.GitToken)
	}
	if len(*asked) != 0 {
		t.Errorf("fetched a token with nothing to clone: %v", *asked)
	}

	// Both taken: refused with a reason, nothing sent.
	dc.SetCheckedOutRepos([]string{"linux", "torvalds-linux"})
	dc.SetRepoOrigins(nil, nil)
	rec := postSpawn(t, api, map[string]any{"daemon_id": "d1", "git_url": "https://github.com/torvalds/linux", "kind": "agent", "runtime": "daemon"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "torvalds-linux") {
		t.Errorf("both taken: status %d body %s, want 409 naming the folders", rec.Code, rec.Body.String())
	}
	noSpawn(t, dc, "both taken")
}

// Cluster runtime takes the same URL: the pod clones that provider's URL.
func TestPostSessionsClusterGitURL(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.GitHubOrg = ""
	jm.GitURLBase = "https://github.com"
	hub.SetJobManager(jm)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	rec := postSpawn(t, api, map[string]any{"git_url": "git@gitlab.com:grp/tool.git", "runtime": "cluster", "kind": "agent"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	raw, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(raw), `"BLERG_RUNNER_GIT_URL","value":"https://gitlab.com/grp/tool.git"`) {
		t.Errorf("Job should clone the GitLab URL: %s", raw)
	}
}

// The token the server hands a daemon is never written to the server's log,
// on success or on a failed send.
func TestNamedCloneTokenIsNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	api, dc, _ := namedCloneAPI(t)
	if rec := postSpawn(t, api, map[string]any{"daemon_id": "d1", "git_url": "https://github.com/octo/tiny", "kind": "agent", "runtime": "docker"}); rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	_ = recvNamedSpawn(t, dc)
	// A full send buffer: the spawn fails, and still nothing is logged.
	full := &DaemonConn{ID: "d2", Name: "full", send: make(chan []byte)}
	full.SetCloneFrom(true)
	api.hub.Register(full)
	rec := postSpawn(t, api, map[string]any{"daemon_id": "d2", "git_url": "https://github.com/octo/tiny", "kind": "agent", "runtime": "docker"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("full buffer: status %d", rec.Code)
	}
	if strings.Contains(buf.String(), "s3cret") || strings.Contains(rec.Body.String(), "s3cret") {
		t.Errorf("the git token leaked into the log or response:\n%s\n%s", buf.String(), rec.Body.String())
	}
}
