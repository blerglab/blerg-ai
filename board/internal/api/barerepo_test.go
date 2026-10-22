package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
)

// bareRepoRunner lists a cluster that does not resolve bare names, and two desktops. Its
// repo-origin answer is deliberately over-generous: it reports BOTH desktops' remotes, so a board
// that read any desktop but the one the person chose would be caught.
type bareRepoRunner struct {
	identityRunner
	origins map[string]map[string]*runner.RepoOrigin // daemon → folder → origin (nil: no remote)
	asked   []string                                 // daemon ids repo-origin discovery named
	repos   []string                                 // repos plain discovery named
}

func (f *bareRepoRunner) targets(repo string) []runner.LaunchTarget {
	out := []runner.LaunchTarget{{Name: "Cluster", Runtimes: []string{"cluster"}}}
	for _, id := range []string{"desktop-a", "desktop-b"} {
		t := runner.LaunchTarget{DaemonID: id, Name: strings.TrimPrefix(id, "desktop-"), Runtimes: []string{"daemon", "docker"}}
		if repo != "" {
			origin, has := f.origins[id][repo]
			t.HasRepo = &has
			t.RepoOrigin = origin
		}
		out = append(out, t)
	}
	return out
}

func (f *bareRepoRunner) Discover(_ context.Context, _, repo string, _ bool) (runner.LaunchOptions, error) {
	f.repos = append(f.repos, repo)
	return runner.LaunchOptions{State: "known", Engines: []runner.LaunchEngine{}, Targets: f.targets(repo)}, nil
}

func (f *bareRepoRunner) DiscoverRepoOrigin(_ context.Context, _, repo, daemon string) (runner.LaunchOptions, error) {
	f.asked = append(f.asked, daemon)
	return runner.LaunchOptions{State: "known", Engines: []runner.LaunchEngine{}, Targets: f.targets(repo)}, nil
}

// bareRepoFixture: a board whose only repo is the bare folder "app", owned (automation) by the
// test person, with one card on it.
func bareRepoFixture(t *testing.T, origins map[string]map[string]*runner.RepoOrigin) (boardID, cardID string, fake *fakeRunner, driver *bareRepoRunner, do func(method, path string, body any) *http.Response) {
	t.Helper()
	srv, pool := testServer(t)
	ctx := context.Background()
	fake = &fakeRunner{lifecycle: "running"}
	driver = &bareRepoRunner{identityRunner: identityRunner{fake}, origins: origins}
	srvAPI.SetRunner(api.RunnerConfig{Driver: driver, PublicURL: "https://b.test", AgentURL: "http://b.svc"})
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetBoardAutomationToken(ctx, pool, board.ID, db.AutomationTokenUpdate{Token: runSessionsToken(t), AccountID: humanSub}); err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strPtr("build it"), Repos: &[]string{"app"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	human := testHumanToken(t, fullHumanCaps)
	do = func(method, path string, body any) *http.Response {
		return request(t, srv, method, path, human, nil, body)
	}
	return board.ID, res.Card.ID, fake, driver, do
}

func remoteQuery(boardID, repo, daemon string) string {
	return "/api/boards/" + boardID + "/repo-remote?" + url.Values{"repo": {repo}, "daemon_id": {daemon}}.Encode()
}

// A bare repo with a remote on the chosen desktop: the board shows app → owner/app, saves owner/name
// on the board and its cards when the person confirms, and a cluster card run then clones it.
func TestBareRepoResolvedOnceThenRunsOnCluster(t *testing.T) {
	boardID, cardID, fake, driver, do := bareRepoFixture(t, map[string]map[string]*runner.RepoOrigin{
		"desktop-a": {"app": {Provider: "github", FullName: "example-org/app"}},
		"desktop-b": {"app": {Provider: "github", FullName: "someone/app"}},
	})

	// Unresolved: the cluster is refused, with the reason, before anything starts.
	resp := do("POST", "/api/cards/"+cardID+"/spawn", map[string]any{"mode": "run", "runtime": "cluster"})
	if body := readAll(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "desktop folder name") {
		t.Fatalf("unresolved cluster run: %d %s", resp.StatusCode, body)
	}
	resp = do("PATCH", "/api/boards/"+boardID, map[string]any{"run_runtime": "cluster", "run_daemon_id": ""})
	if body := readAll(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "resolve it under Repos") {
		t.Fatalf("unresolved cluster target: %d %s", resp.StatusCode, body)
	}
	if fake.attemptCount() != 0 {
		t.Fatal("a refused cluster run reached the runner")
	}
	var targets struct {
		Unresolved []string `json:"unresolved_repos"`
		Targets    []struct {
			Runtime     string `json:"runtime"`
			Unavailable string `json:"unavailable"`
		} `json:"targets"`
	}
	decodeBody(t, do("GET", "/api/boards/"+boardID+"/run-targets", nil), &targets)
	if len(targets.Unresolved) != 1 || targets.Unresolved[0] != "app" || targets.Targets[0].Runtime != "cluster" || targets.Targets[0].Unavailable == "" {
		t.Fatalf("board run targets = %+v", targets)
	}

	var found struct {
		FullName    string `json:"full_name"`
		DesktopName string `json:"desktop_name"`
		Problem     string `json:"problem"`
	}
	decodeBody(t, do("GET", remoteQuery(boardID, "app", "desktop-a"), nil), &found)
	if found.FullName != "example-org/app" || found.Problem != "" || found.DesktopName != "a" {
		t.Fatalf("lookup = %+v", found)
	}
	if len(driver.asked) != 1 || driver.asked[0] != "desktop-a" {
		t.Fatalf("asked desktops %v, want only the chosen one", driver.asked)
	}

	// The confirmation must match what the desktop says now.
	resp = do("POST", "/api/boards/"+boardID+"/repos/resolve", map[string]any{"repo": "app", "daemon_id": "desktop-a", "full_name": "someone/app"})
	if body := readAll(t, resp); resp.StatusCode != http.StatusConflict {
		t.Fatalf("mismatched confirmation: %d %s", resp.StatusCode, body)
	}
	var saved db.Board
	decodeBody(t, do("POST", "/api/boards/"+boardID+"/repos/resolve", map[string]any{"repo": "app", "daemon_id": "desktop-a", "full_name": "example-org/app"}), &saved)
	if len(saved.Repos) != 1 || saved.Repos[0] != "example-org/app" {
		t.Fatalf("saved repos = %v", saved.Repos)
	}
	var card struct {
		Repos []string `json:"repos"`
	}
	decodeBody(t, do("GET", "/api/cards/"+cardID, nil), &card)
	if len(card.Repos) != 1 || card.Repos[0] != "example-org/app" {
		t.Fatalf("card repos = %v", card.Repos)
	}

	resp = do("PATCH", "/api/boards/"+boardID, map[string]any{"run_runtime": "cluster", "run_daemon_id": ""})
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("cluster target after resolving: %d %s", resp.StatusCode, body)
	}
	resp = do("POST", "/api/cards/"+cardID+"/spawn", map[string]any{"mode": "run"})
	if body := readAll(t, resp); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("cluster run: %d %s", resp.StatusCode, body)
	}
	if st := fake.started[0]; st.Runtime != "cluster" || st.Repo != "example-org/app" {
		t.Fatalf("start = runtime %q repo %q", st.Runtime, st.Repo)
	}
}

// A folder without a remote on the chosen desktop stays bare and desktop-only: the lookup says so
// plainly, nothing is saved, and the cluster stays refused. Another desktop's remote for a folder
// of the same name is never used, even when the runner reports it.
func TestBareRepoWithoutRemoteStaysOnDesktops(t *testing.T) {
	boardID, cardID, fake, _, do := bareRepoFixture(t, map[string]map[string]*runner.RepoOrigin{
		"desktop-a": {"app": nil},
		"desktop-b": {"app": {Provider: "github", FullName: "someone/app"}},
	})
	var found struct {
		FullName string `json:"full_name"`
		Problem  string `json:"problem"`
	}
	decodeBody(t, do("GET", remoteQuery(boardID, "app", "desktop-a"), nil), &found)
	if found.FullName != "" || found.Problem != "this folder has no remote, so the cluster can't clone it; run it on a desktop" {
		t.Fatalf("lookup = %+v", found)
	}
	resp := do("POST", "/api/boards/"+boardID+"/repos/resolve", map[string]any{"repo": "app", "daemon_id": "desktop-a", "full_name": "someone/app"})
	if body := readAll(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "has no remote") {
		t.Fatalf("resolve without a remote: %d %s", resp.StatusCode, body)
	}
	var board db.Board
	decodeBody(t, do("GET", "/api/boards/"+boardID, nil), &board)
	if len(board.Repos) != 1 || board.Repos[0] != "app" {
		t.Fatalf("repos changed: %v", board.Repos)
	}
	resp = do("POST", "/api/cards/"+cardID+"/spawn", map[string]any{"mode": "run", "runtime": "cluster"})
	if body := readAll(t, resp); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("cluster run: %d %s", resp.StatusCode, body)
	}
	var targets struct {
		Targets []struct {
			Runtime     string `json:"runtime"`
			Unavailable string `json:"unavailable"`
		} `json:"targets"`
	}
	decodeBody(t, do("GET", "/api/cards/"+cardID+"/run-targets", nil), &targets)
	if targets.Targets[0].Runtime != "cluster" || targets.Targets[0].Unavailable == "" {
		t.Fatalf("card run targets offer the cluster: %+v", targets)
	}
	// A desktop run is untouched.
	resp = do("POST", "/api/cards/"+cardID+"/spawn", map[string]any{"mode": "run", "runtime": "docker", "daemon_id": "desktop-a"})
	if body := readAll(t, resp); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("desktop run: %d %s", resp.StatusCode, body)
	}
	if st := fake.started[0]; st.Repo != "app" || st.DaemonID != "desktop-a" {
		t.Fatalf("desktop start = %+v", st)
	}
}

// Resolving is a signed-in board admin's, for a bare repo the board has.
func TestBareRepoResolveRefusals(t *testing.T) {
	boardID, _, _, _, do := bareRepoFixture(t, map[string]map[string]*runner.RepoOrigin{
		"desktop-a": {"app": {Provider: "github", FullName: "example-org/app"}},
	})
	for _, tc := range []struct {
		name, path string
		want       int
	}{
		{"not a board repo", remoteQuery(boardID, "other", "desktop-a"), http.StatusUnprocessableEntity},
		{"no desktop", remoteQuery(boardID, "app", ""), http.StatusUnprocessableEntity},
		{"disconnected desktop", remoteQuery(boardID, "app", "desktop-z"), http.StatusUnprocessableEntity},
	} {
		resp := do("GET", tc.path, nil)
		if body := readAll(t, resp); resp.StatusCode != tc.want {
			t.Errorf("%s: %d %s", tc.name, resp.StatusCode, body)
		}
	}
}

// A member without board.admin cannot look up or resolve.
func TestBareRepoResolveNeedsAdmin(t *testing.T) {
	srv, pool := testServer(t)
	driver := &bareRepoRunner{identityRunner: identityRunner{&fakeRunner{lifecycle: "running"}}}
	srvAPI.SetRunner(api.RunnerConfig{Driver: driver, PublicURL: "https://b.test", AgentURL: "http://b.svc"})
	board, err := db.CreateBoard(context.Background(), pool, db.BoardParams{Name: "work", Repos: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	member := memberToken(t)
	resp := request(t, srv, "GET", remoteQuery(board.ID, "app", "desktop-a"), member, nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member lookup: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = request(t, srv, "POST", "/api/boards/"+board.ID+"/repos/resolve", member, nil, map[string]any{"repo": "app", "daemon_id": "desktop-a", "full_name": "x/app"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member resolve: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if len(driver.asked) != 0 {
		t.Fatal("a refused request reached the runner")
	}
}

// Resolving is the board admin's who owns the board's automation token (the account the lookup and
// the cluster runs act as), the same person the settings prompt is shown to.
func TestBareRepoResolveNeedsTheAutomationOwner(t *testing.T) {
	srv, pool := testServer(t)
	driver := &bareRepoRunner{identityRunner: identityRunner{&fakeRunner{lifecycle: "running"}}}
	srvAPI.SetRunner(api.RunnerConfig{Driver: driver, PublicURL: "https://b.test", AgentURL: "http://b.svc"})
	board, err := db.CreateBoard(context.Background(), pool, db.BoardParams{Name: "work", Repos: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetBoardAutomationToken(context.Background(), pool, board.ID, db.AutomationTokenUpdate{Token: runSessionsToken(t), AccountID: "someone-else"}); err != nil {
		t.Fatal(err)
	}
	admin := testHumanToken(t, fullHumanCaps)
	resp := request(t, srv, "GET", remoteQuery(board.ID, "app", "desktop-a"), admin, nil, nil)
	if body := readAll(t, resp); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "owns the board's automation token") {
		t.Fatalf("admin who is not the automation owner: %d %s", resp.StatusCode, body)
	}
	if len(driver.asked) != 0 {
		t.Fatal("a refused lookup reached the runner")
	}
}

// Saving the cluster target names every bare repo the cluster would refuse.
func TestBareReposNamedWhenClusterTargetRefused(t *testing.T) {
	boardID, _, _, _, do := bareRepoFixture(t, nil)
	resp := do("PATCH", "/api/boards/"+boardID, map[string]any{"repos": []string{"app", "tool", "example-org/lib"}})
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("repos: %d %s", resp.StatusCode, body)
	}
	resp = do("PATCH", "/api/boards/"+boardID, map[string]any{"run_runtime": "cluster", "run_daemon_id": ""})
	if body := readAll(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "app, tool are desktop folder names") {
		t.Fatalf("cluster target: %d %s", resp.StatusCode, body)
	}
}

// The board's first repo is its sessions' working directory: resolving it keeps it first, though
// "example-org/app" sorts after "beta".
func TestBareRepoResolveKeepsTheBoardsWorkingDirectory(t *testing.T) {
	boardID, _, _, driver, do := bareRepoFixture(t, map[string]map[string]*runner.RepoOrigin{
		"desktop-a": {"app": {Provider: "github", FullName: "example-org/app"}},
	})
	resp := do("PATCH", "/api/boards/"+boardID, map[string]any{"repos": []string{"app", "beta"}})
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("repos: %d %s", resp.StatusCode, body)
	}
	var saved db.Board
	decodeBody(t, do("POST", "/api/boards/"+boardID+"/repos/resolve", map[string]any{"repo": "app", "daemon_id": "desktop-a", "full_name": "example-org/app"}), &saved)
	if len(saved.Repos) != 2 || saved.Repos[0] != "example-org/app" {
		t.Fatalf("saved repos = %v", saved.Repos)
	}
	driver.repos = nil
	resp = do("GET", "/api/boards/"+boardID+"/launch-options", nil)
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("launch options: %d %s", resp.StatusCode, body)
	}
	if len(driver.repos) == 0 || driver.repos[len(driver.repos)-1] != "example-org/app" {
		t.Fatalf("board discovery used %v, want the resolved app", driver.repos)
	}
}
