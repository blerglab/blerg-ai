package api_test

import (
	"context"
	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"net/http"
	"strings"
	"testing"
	"time"
)

type runtimeTargetRunner struct {
	identityRunner
	targets   []runner.LaunchTarget
	preferred string
}

func (f *runtimeTargetRunner) Discover(context.Context, string, string, bool) (runner.LaunchOptions, error) {
	return runner.LaunchOptions{State: "known", Engines: []runner.LaunchEngine{}, Targets: f.targets}, nil
}
func (f *runtimeTargetRunner) DefaultRuntime() string {
	if f.preferred != "" {
		return f.preferred
	}
	return "cluster"
}
func runtimeFixture(t *testing.T, cluster bool) (*fakeRunner, *runtimeTargetRunner, string, string, func(string, string, any) *http.Response) {
	t.Helper()
	srv, pool := testServer(t)
	human := humanToken(t, srv)
	f := &fakeRunner{lifecycle: "running"}
	driver := &runtimeTargetRunner{identityRunner: identityRunner{f}, targets: []runner.LaunchTarget{{DaemonID: "desktop-a", Name: "Desktop", Runtimes: []string{"docker", "daemon"}}}}
	if cluster {
		driver.targets = append(driver.targets, runner.LaunchTarget{Name: "Cluster", Runtimes: []string{"cluster"}})
	}
	srvAPI.SetRunner(api.RunnerConfig{Driver: driver, PublicURL: "https://board.example.com", AgentURL: "http://board.svc"})
	board, err := db.CreateBoard(context.Background(), pool, db.BoardParams{Name: "Runtime", Repos: []string{"example-org/app"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetBoardAutomationToken(context.Background(), pool, board.ID, db.AutomationTokenUpdate{Token: runSessionsToken(t), AccountID: humanSub}); err != nil {
		t.Fatal(err)
	}
	card, err := db.CreateCard(context.Background(), pool, board.ID, db.CardParams{Title: strPtr("Implement the feature"), Repos: &[]string{"example-org/app"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	return f, driver, board.ID, card.Card.ID, func(method, path string, body any) *http.Response {
		return request(t, srv, method, path, human, nil, body)
	}
}
func TestBoardRuntimeSettingAndSingleRunOverride(t *testing.T) {
	f, _, board, card, do := runtimeFixture(t, true)
	var saved map[string]any
	decodeBody(t, do("PATCH", "/api/boards/"+board, map[string]any{"run_runtime": "docker", "run_daemon_id": "desktop-a"}), &saved)
	if saved["run_runtime"] != "docker" || saved["run_daemon_id"] != "desktop-a" {
		t.Fatalf("target not saved: runtime=%v daemon=%v", saved["run_runtime"], saved["run_daemon_id"])
	}
	for _, path := range []string{"/api/cards/" + card + "/spawn", "/api/boards/" + board + "/sessions"} {
		r := do("POST", path, map[string]any{"mode": "discuss"})
		if r.StatusCode != 202 {
			t.Fatalf("start %s: %d %s", path, r.StatusCode, readAll(t, r))
		}
		r.Body.Close()
		got := f.started[len(f.started)-1]
		if got.Runtime != "docker" || got.DaemonID != "desktop-a" {
			t.Fatalf("board default not used: %q %q", got.Runtime, got.DaemonID)
		}
	}
	r := do("POST", "/api/cards/"+card+"/spawn", map[string]any{"mode": "run", "runtime": "cluster"})
	if r.StatusCode != 202 {
		t.Fatalf("override: %d %s", r.StatusCode, readAll(t, r))
	}
	r.Body.Close()
	if got := f.started[len(f.started)-1]; got.Runtime != "cluster" || got.DaemonID != "" {
		t.Fatal("single run did not override the board")
	}
}
func TestDesktopOnlyBoardSelectsDesktopWithoutClusterFallback(t *testing.T) {
	f, _, _, card, do := runtimeFixture(t, false)
	r := do("POST", "/api/cards/"+card+"/spawn", map[string]any{"mode": "run"})
	if r.StatusCode != 202 {
		t.Fatalf("start: %d %s", r.StatusCode, readAll(t, r))
	}
	r.Body.Close()
	if got := f.started[0]; got.Runtime != "docker" || got.DaemonID != "desktop-a" {
		t.Fatalf("desktop-only start: %q %q", got.Runtime, got.DaemonID)
	}
}

// A desktop-only install with several desktops runs a board on the one that has its repository,
// and refuses with a clear message when none or several do; an older runner that does not say
// leaves the choice to the runner.
func TestDesktopOnlyBoardWithSeveralDesktops(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name         string
		a, b         *bool
		wantDaemon   string
		wantRefusal  string
		runnerChoice bool
	}{
		{name: "the one with the repo", a: &no, b: &yes, wantDaemon: "desktop-b"},
		{name: "none has it", a: &no, b: &no, wantRefusal: "none of the 2 connected desktops"},
		{name: "both have it", a: &yes, b: &yes, wantRefusal: "2 connected desktops have"},
		{name: "runner does not say", runnerChoice: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, driver, _, card, do := runtimeFixture(t, false)
			driver.targets[0].HasRepo = tc.a
			driver.targets = append(driver.targets, runner.LaunchTarget{DaemonID: "desktop-b", Name: "Laptop", Runtimes: []string{"docker", "daemon"}, HasRepo: tc.b})
			r := do("POST", "/api/cards/"+card+"/spawn", map[string]any{"mode": "run"})
			body := readAll(t, r)
			r.Body.Close()
			if tc.wantRefusal != "" {
				if r.StatusCode < 400 || !strings.Contains(body, tc.wantRefusal) || f.attemptCount() != 0 {
					t.Fatalf("want refusal %q: %d %s", tc.wantRefusal, r.StatusCode, body)
				}
				return
			}
			if r.StatusCode != 202 {
				t.Fatalf("start: %d %s", r.StatusCode, body)
			}
			got := f.started[0]
			if tc.runnerChoice {
				if got.DaemonID != "" {
					t.Fatalf("board chose %q instead of leaving it to the runner", got.DaemonID)
				}
				return
			}
			if got.Runtime != "docker" || got.DaemonID != tc.wantDaemon {
				t.Fatalf("started on %q %q, want %s", got.Runtime, got.DaemonID, tc.wantDaemon)
			}
		})
	}
}

func TestBoardRuntimeOfflineDoesNotFallBack(t *testing.T) {
	f, driver, board, card, do := runtimeFixture(t, true)
	r := do("PATCH", "/api/boards/"+board, map[string]any{"run_runtime": "docker", "run_daemon_id": "desktop-a"})
	r.Body.Close()
	driver.targets = driver.targets[1:]
	r = do("POST", "/api/cards/"+card+"/spawn", map[string]any{"mode": "run"})
	if r.StatusCode < 400 {
		t.Fatalf("offline target accepted: %d", r.StatusCode)
	}
	r.Body.Close()
	if f.attemptCount() != 0 {
		t.Fatal("offline target fell back to another runtime")
	}
}

func TestBoardRuntimeHostAppliesToAutomatedRuns(t *testing.T) {
	f, _, board, card, do := runtimeFixture(t, true)
	r := do("PATCH", "/api/boards/"+board, map[string]any{"run_runtime": "daemon", "run_daemon_id": "desktop-a", "run_host_acknowledged": true})
	if r.StatusCode != 200 {
		t.Fatalf("host setting: %d %s", r.StatusCode, readAll(t, r))
	}
	r.Body.Close()
	cols, err := db.ListColumns(t.Context(), srvAPI.Pool, board)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MoveCard(t.Context(), srvAPI.Pool, card, columnNamed(t, cols, "ready"), nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	r = do("POST", "/api/boards/"+board+"/run", nil)
	if r.StatusCode != 200 {
		t.Fatalf("run: %d %s", r.StatusCode, readAll(t, r))
	}
	r.Body.Close()
	tickUntilDispatched(t, srvAPI.Pool, card)
	got := f.started[0]
	if got.Runtime != "daemon" || got.DaemonID != "desktop-a" {
		t.Fatalf("automation ignored host setting: %q %q", got.Runtime, got.DaemonID)
	}
}
func TestBoardRuntimeIsBoundToAutomationOwner(t *testing.T) {
	f, _, board, card, do := runtimeFixture(t, true)
	r := do("PATCH", "/api/boards/"+board, map[string]any{"run_runtime": "docker", "run_daemon_id": "desktop-a"})
	r.Body.Close()
	if err := db.SetBoardAutomationToken(t.Context(), srvAPI.Pool, board, db.AutomationTokenUpdate{Token: agentToken(t, "blerg-runner", "new-owner", []string{"session.start"}, time.Hour), AccountID: "new-owner"}); err != nil {
		t.Fatal(err)
	}
	r = do("PATCH", "/api/boards/"+board, map[string]any{"run_runtime": "daemon", "run_daemon_id": "desktop-a"})
	if r.StatusCode != 403 {
		t.Fatalf("another owner changed target: %d", r.StatusCode)
	}
	r.Body.Close()
	r = do("POST", "/api/cards/"+card+"/spawn", map[string]any{"mode": "run"})
	if r.StatusCode != 422 {
		t.Fatalf("stale owner's target accepted: %d", r.StatusCode)
	}
	r.Body.Close()
	if f.attemptCount() != 0 {
		t.Fatal("stale owner's target was used")
	}
}

func TestDesktopOnlyBoardKeepsConfiguredHostDefault(t *testing.T) {
	f, driver, _, card, do := runtimeFixture(t, false)
	driver.preferred = "daemon"
	r := do("POST", "/api/cards/"+card+"/spawn", map[string]any{"mode": "run"})
	if r.StatusCode != 202 {
		t.Fatalf("start: %d %s", r.StatusCode, readAll(t, r))
	}
	r.Body.Close()
	if f.started[0].Runtime != "daemon" {
		t.Fatal("configured host default was lost")
	}
}

// A host ("This machine") target is saved only with the owner's explicit acknowledgement, which is
// recorded with who and when; switching away clears it.
func TestBoardHostTargetNeedsAcknowledgement(t *testing.T) {
	f, _, board, _, do := runtimeFixture(t, true)
	r := do("PATCH", "/api/boards/"+board, map[string]any{"run_runtime": "daemon", "run_daemon_id": "desktop-a"})
	body := readAll(t, r)
	r.Body.Close()
	if r.StatusCode != 422 || !strings.Contains(body, "acknowledgement") {
		t.Fatalf("host saved without acknowledgement: %d %s", r.StatusCode, body)
	}
	var saved map[string]any
	decodeBody(t, do("PATCH", "/api/boards/"+board, map[string]any{"run_runtime": "daemon", "run_daemon_id": "desktop-a", "run_host_acknowledged": true}), &saved)
	if saved["run_runtime"] != "daemon" || saved["run_host_acknowledged_at"] == nil {
		t.Fatalf("acknowledged host not saved: %v", saved)
	}
	var by string
	if err := srvAPI.Pool.QueryRow(t.Context(), `SELECT run_host_ack_by FROM boards WHERE id = $1`, board).Scan(&by); err != nil || by != humanSub {
		t.Fatalf("acknowledgement not recorded against the owner: %q %v", by, err)
	}
	saved = nil
	decodeBody(t, do("PATCH", "/api/boards/"+board, map[string]any{"run_runtime": "docker", "run_daemon_id": "desktop-a"}), &saved)
	if err := srvAPI.Pool.QueryRow(t.Context(), `SELECT run_host_ack_by FROM boards WHERE id = $1`, board).Scan(&by); err != nil || by != "" || saved["run_host_acknowledged_at"] != nil {
		t.Fatalf("acknowledgement kept after leaving the host: %q %v", by, saved)
	}
	// The database refuses an unacknowledged host target whatever path writes it.
	if _, err := srvAPI.Pool.Exec(t.Context(), `UPDATE boards SET run_runtime = 'daemon', run_daemon_id = 'desktop-a', run_target_account = $2 WHERE id = $1`, board, humanSub); err == nil {
		t.Fatal("database accepted a host target without acknowledgement")
	}
	if f.attemptCount() != 0 {
		t.Fatal("nothing should have started")
	}
}

// A single run reaches a desktop's host only where the board's own target is that acknowledged
// host; the card's run menu does not offer the host otherwise.
func TestCardHostRunNeedsBoardAcknowledgement(t *testing.T) {
	f, _, board, card, do := runtimeFixture(t, true)
	menu := func() []any {
		var out map[string]any
		decodeBody(t, do("GET", "/api/cards/"+card+"/run-targets", nil), &out)
		targets, _ := out["targets"].([]any)
		return targets
	}
	hasHost := func(targets []any) bool {
		for _, raw := range targets {
			if m, _ := raw.(map[string]any); m["runtime"] == "daemon" {
				return true
			}
		}
		return false
	}
	if hasHost(menu()) {
		t.Fatal("card menu offered the host without the board's acknowledgement")
	}
	r := do("POST", "/api/cards/"+card+"/spawn", map[string]any{"mode": "run", "runtime": "daemon", "daemon_id": "desktop-a"})
	r.Body.Close()
	if r.StatusCode != 422 || f.attemptCount() != 0 {
		t.Fatalf("host run without acknowledgement: %d", r.StatusCode)
	}
	r = do("PATCH", "/api/boards/"+board, map[string]any{"run_runtime": "daemon", "run_daemon_id": "desktop-a", "run_host_acknowledged": true})
	r.Body.Close()
	if !hasHost(menu()) {
		t.Fatal("card menu hid the board's acknowledged host")
	}
	r = do("POST", "/api/cards/"+card+"/spawn", map[string]any{"mode": "run", "runtime": "daemon", "daemon_id": "desktop-a"})
	if r.StatusCode != 202 {
		t.Fatalf("acknowledged host run: %d %s", r.StatusCode, readAll(t, r))
	}
	r.Body.Close()
	// The acknowledgement belongs to the owner who gave it. When the board's automation owner is
	// someone else (here, the stored target and its acknowledgement are a previous owner's), the
	// host is neither offered nor usable for a single run.
	if _, err := srvAPI.Pool.Exec(t.Context(), `UPDATE boards SET run_target_account = 'old-owner', run_host_ack_by = 'old-owner' WHERE id = $1`, board); err != nil {
		t.Fatal(err)
	}
	if hasHost(menu()) {
		t.Fatal("card menu offered a host a previous owner acknowledged")
	}
	before := f.attemptCount()
	r = do("POST", "/api/cards/"+card+"/spawn", map[string]any{"mode": "run", "runtime": "daemon", "daemon_id": "desktop-a"})
	r.Body.Close()
	if r.StatusCode != 422 || f.attemptCount() != before {
		t.Fatalf("host run on a previous owner's acknowledgement: %d", r.StatusCode)
	}
}
