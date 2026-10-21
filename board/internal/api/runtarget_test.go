package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
)

// targetRunner is the production-shaped fake with discovery: it starts under the board's
// automation identity and lists the runner's execution targets.
type targetRunner struct {
	identityRunner
	tokens []string
}

func (f *targetRunner) Discover(_ context.Context, token, _ string, _ bool) (runner.LaunchOptions, error) {
	f.tokens = append(f.tokens, token)
	return runner.LaunchOptions{State: "known", Engines: []runner.LaunchEngine{}, Targets: []runner.LaunchTarget{
		{Name: "Cluster", Runtimes: []string{"cluster"}},
		{DaemonID: "daemon-laptop", Name: "laptop", Runtimes: []string{"daemon", "docker"}},
	}}, nil
}

// runTargetFixture is a board whose automation token belongs to owner, and one card on it.
func runTargetFixture(t *testing.T, owner string) (_ func(string) string, fake *fakeRunner, cardID string, do func(method, path, token string, body any) *http.Response, driver *targetRunner) {
	t.Helper()
	srv, pool := testServer(t)
	ctx := context.Background()
	_ = humanToken(t, srv)
	fake = &fakeRunner{lifecycle: "running"}
	driver = &targetRunner{identityRunner: identityRunner{fake}}
	srvAPI.SetRunner(api.RunnerConfig{Driver: driver, PublicURL: "https://b.test", AgentURL: "http://b.svc"})
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"acme/app"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetBoardAutomationToken(ctx, pool, board.ID, db.AutomationTokenUpdate{Token: runSessionsToken(t), AccountID: owner}); err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strPtr("build it"), Repos: &[]string{"acme/app"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	do = func(method, path, token string, body any) *http.Response {
		return request(t, srv, method, path, token, nil, body)
	}
	return nil, fake, res.Card.ID, do, driver
}

// The owner of the board's automation token runs a card on a desktop they choose: the start
// carries that target, under their own token, through the same start path.
func TestRunCardOnChosenTarget(t *testing.T) {
	_, fake, cardID, do, _ := runTargetFixture(t, humanSub)
	human := testHumanToken(t, fullHumanCaps)
	resp := do("POST", "/api/cards/"+cardID+"/spawn", human, map[string]any{"mode": "run", "runtime": "docker", "daemon_id": "daemon-laptop"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("spawn: %d %s", resp.StatusCode, readAll(t, resp))
	}
	resp.Body.Close()
	st := fake.started[0]
	if st.Runtime != "docker" || st.DaemonID != "daemon-laptop" || st.Token == "" || st.Repo != "acme/app" {
		t.Fatalf("start = runtime %q daemon %q token set %v repo %q", st.Runtime, st.DaemonID, st.Token != "", st.Repo)
	}

	// Without a choice the start is what it always was.
	resp = do("POST", "/api/cards/"+cardID+"/spawn", human, map[string]any{"mode": "run"})
	resp.Body.Close()
	if st := fake.started[1]; st.Runtime != "" || st.DaemonID != "" {
		t.Fatalf("default start named a target: %q %q", st.Runtime, st.DaemonID)
	}
}

// A choice is the token owner's alone (the session runs as them), and a malformed one is
// refused before the card is claimed or anything starts.
func TestRunCardTargetRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, owner string
		body        map[string]any
		want        int
	}{
		{"someone else's automation", "someone-else", map[string]any{"runtime": "docker", "daemon_id": "daemon-laptop"}, http.StatusForbidden},
		{"unknown runtime", humanSub, map[string]any{"runtime": "kubernetes"}, http.StatusUnprocessableEntity},
		{"host without daemon", humanSub, map[string]any{"runtime": "daemon"}, http.StatusUnprocessableEntity},
		{"daemon without runtime", humanSub, map[string]any{"daemon_id": "daemon-laptop"}, http.StatusUnprocessableEntity},
		{"cluster with a daemon", humanSub, map[string]any{"runtime": "cluster", "daemon_id": "daemon-laptop"}, http.StatusUnprocessableEntity},
		{"malformed daemon", humanSub, map[string]any{"runtime": "docker", "daemon_id": "../x y"}, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, fake, cardID, do, _ := runTargetFixture(t, tc.owner)
			resp := do("POST", "/api/cards/"+cardID+"/spawn", testHumanToken(t, fullHumanCaps), tc.body)
			body := readAll(t, resp)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d (%s), want %d", resp.StatusCode, body, tc.want)
			}
			if fake.attemptCount() != 0 {
				t.Fatal("a refused choice reached the runner")
			}
		})
	}
}

// The card's run menu: the owner sees the board default and every target; anyone else sees
// only the default, with the reason.
func TestRunTargetsForCard(t *testing.T) {
	_, _, cardID, do, driver := runTargetFixture(t, humanSub)
	resp := do("GET", "/api/cards/"+cardID+"/run-targets", testHumanToken(t, fullHumanCaps), nil)
	var got struct {
		DefaultRuntime string `json:"default_runtime"`
		CanChoose      bool   `json:"can_choose"`
		Targets        []struct {
			Runtime  string `json:"runtime"`
			DaemonID string `json:"daemon_id"`
			Label    string `json:"label"`
		} `json:"targets"`
	}
	raw := readAll(t, resp)
	if err := json.Unmarshal([]byte(raw), &got); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("run-targets: %d %s", resp.StatusCode, raw)
	}
	// The cluster and the desktop's sandbox, owned by the automation account. The desktop's host
	// is not offered for a single run until the board's own target is that acknowledged host.
	if !got.CanChoose || len(got.Targets) != 2 {
		t.Fatalf("owner's targets = %+v", got)
	}
	if got.Targets[1].Runtime != "docker" || got.Targets[1].DaemonID != "daemon-laptop" || !strings.Contains(got.Targets[1].Label, "laptop") {
		t.Fatalf("targets = %+v", got.Targets)
	}
	if strings.Contains(raw, "automation") && strings.Contains(raw, driver.tokens[0]) {
		t.Fatal("the automation token was returned")
	}

	_, _, cardID, do, _ = runTargetFixture(t, "someone-else")
	resp = do("GET", "/api/cards/"+cardID+"/run-targets", testHumanToken(t, fullHumanCaps), nil)
	raw = readAll(t, resp)
	got.Targets = nil
	_ = json.Unmarshal([]byte(raw), &got)
	if got.CanChoose || len(got.Targets) != 0 || !strings.Contains(raw, "reason") {
		t.Fatalf("non-owner run-targets = %s", raw)
	}
}

// A respawn of a card's quiet worker keeps the target the person chose for it.
func TestRespawnKeepsChosenTarget(t *testing.T) {
	_, fake, cardID, do, _ := runTargetFixture(t, humanSub)
	resp := do("POST", "/api/cards/"+cardID+"/spawn", testHumanToken(t, fullHumanCaps), map[string]any{"runtime": "docker", "daemon_id": "daemon-laptop"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("spawn: %d %s", resp.StatusCode, readAll(t, resp))
	}
	resp.Body.Close()
	ctx := context.Background()
	pool := srvAPI.Pool
	card, err := db.GetCard(ctx, pool, cardID)
	if err != nil {
		t.Fatal(err)
	}
	// The worker went quiet and stopped; the board is running.
	if _, err := pool.Exec(ctx, `UPDATE runner_sessions SET lifecycle = 'stopped', last_activity_at = now() - interval '2 hours' WHERE card_id = $1`, cardID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE cards SET updated_at = now() - interval '2 hours' WHERE id = $1`, cardID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO board_runs (board_id) VALUES ($1)`, card.BoardID); err != nil {
		t.Fatal(err)
	}
	srvAPI.RunTick(ctx)
	if n := fake.startCount(); n != 2 {
		t.Fatalf("starts = %d, want the run and one respawn", n)
	}
	fake.mu.Lock()
	st := fake.started[1]
	fake.mu.Unlock()
	if st.Runtime != "docker" || st.DaemonID != "daemon-laptop" {
		t.Fatalf("respawn went to %q %q, want the chosen sandbox", st.Runtime, st.DaemonID)
	}
}
