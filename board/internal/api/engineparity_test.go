package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"github.com/blerglab/blerg-ai/contracts/engines"
)

type parityRunner struct {
	*identityRunner
	tokens      []string
	clientIDs   []string
	effort      string
	status      runner.Status
	statusErr   error
	fallbackErr error
	discovery   runner.LaunchOptions
}

func (f *parityRunner) Status(ctx context.Context, id string) (runner.Status, error) {
	if f.fallbackErr != nil {
		return runner.Status{}, f.fallbackErr
	}
	return f.fakeRunner.Status(ctx, id)
}

func (f *parityRunner) MessageAs(_ context.Context, token, id, text, source, clientID string) error {
	f.tokens = append(f.tokens, token)
	f.clientIDs = append(f.clientIDs, clientID)
	return nil
}
func (f *parityRunner) SetModelAs(_ context.Context, token, id, model, effort string) error {
	f.tokens = append(f.tokens, token)
	f.effort = effort
	return nil
}
func (f *parityRunner) StatusAs(_ context.Context, token, id string) (runner.Status, error) {
	f.tokens = append(f.tokens, token)
	return f.status, f.statusErr
}
func (f *parityRunner) DiscoverSession(ctx context.Context, token, id string, refresh bool) (runner.LaunchOptions, error) {
	return f.Discover(ctx, token, id, refresh)
}

func (f *parityRunner) Discover(_ context.Context, token, repo string, refresh bool) (runner.LaunchOptions, error) {
	f.tokens = append(f.tokens, token)
	return f.discovery, nil
}

func TestBoardScopedDiscoveryAndFreshControls(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	human := humanToken(t, srv)
	fake := &parityRunner{identityRunner: &identityRunner{&fakeRunner{lifecycle: "running"}}, discovery: runner.LaunchOptions{State: "known", Engines: []runner.LaunchEngine{{Descriptor: engines.Descriptor{ID: "codex", Name: "Codex"}, Models: runner.ModelList{Models: []runner.Model{{ID: "codex-native", Efforts: []string{"high"}}}}}},
		// A current runner lists where sessions can run (an older one that does not is refused an unplaced start).
		Targets: []runner.LaunchTarget{{Name: "Cluster", Runtimes: []string{"cluster"}, ResolvesBareRepos: true}}}}
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, UIBase: "https://runner.example.test/sessions"})
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "parity", Repos: []string{"app"}, Model: strPtr("claude-old"), Effort: strPtr("high"), AutomationEngine: strPtr("codex")})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/boards/" + board.ID
	// No stored authority means no discovery, and it never falls back to operator.
	resp := request(t, srv, "GET", path+"/launch-options", human, nil, nil)
	if resp.StatusCode != 422 {
		t.Fatalf("missing identity HTTP %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	token := runSessionsToken(t)
	decodeBody(t, request(t, srv, "PATCH", path, human, nil, map[string]string{"automation_token": token}), &map[string]any{})
	var opts runner.LaunchOptions
	decodeBody(t, request(t, srv, "GET", path+"/launch-options", human, nil, nil), &opts)
	if len(opts.Engines) != 1 || opts.Engines[0].ID != "codex" {
		t.Fatalf("wrong options: %+v", opts)
	}
	before := len(fake.tokens)
	resp = request(t, srv, "GET", path+"/launch-options", testHumanToken(t, []string{"card.read"}), nil, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("read-only discovery HTTP %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if len(fake.tokens) != before {
		t.Fatal("unauthorized discovery reached runner")
	}
	card, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strPtr("card"), Repos: &[]string{"app"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	rsID := spawnForRemodel(t, srv, human, card.Card.ID)
	var model, effort, engine string
	if err := pool.QueryRow(ctx, `SELECT model,effort,engine FROM runner_sessions WHERE id=$1`, rsID).Scan(&model, &effort, &engine); err != nil {
		t.Fatal(err)
	}
	if model != "" || effort != "" || engine != "codex" {
		t.Fatalf("recorded settings disagree with start: %q/%q/%q", model, effort, engine)
	}
	var sessionOpts runner.LaunchOptions
	decodeBody(t, request(t, srv, "GET", "/api/runner-sessions/"+rsID+"/launch-options", human, nil, nil), &sessionOpts)
	resp = request(t, srv, "GET", "/api/runner-sessions/"+rsID+"/launch-options", "svc-key-123", nil, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("nonhuman session discovery HTTP %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	renewed := agentToken(t, "blerg-runner", humanSub, []string{"session.start"}, 2*time.Hour)
	decodeBody(t, request(t, srv, "PATCH", path, human, nil, map[string]string{"automation_token": renewed}), &map[string]any{})
	resp = request(t, srv, "POST", "/api/runner-sessions/"+rsID+"/message", human, nil, map[string]string{"text": "hello", "client_id": "stable-board-turn"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("message HTTP %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = request(t, srv, "POST", "/api/runner-sessions/"+rsID+"/model", human, nil, map[string]string{"model": "codex-native", "effort": "high"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("model HTTP %d: %s", resp.StatusCode, readAll(t, resp))
	}
	_ = resp.Body.Close()
	if fake.tokens[len(fake.tokens)-1] != renewed || fake.tokens[len(fake.tokens)-2] != renewed || fake.clientIDs[0] != "stable-board-turn" || fake.effort != "high" {
		t.Fatal("controls failed to use renewed identity/settings")
	}
	// Even when lifecycle stays disconnected, changed authority/recovery metadata
	// must replace stale values. A gone session clears eligibility permanently.
	_, _ = pool.Exec(ctx, `UPDATE runner_sessions SET lifecycle='disconnected',resumable=true WHERE id=$1`, rsID)
	steering := true
	fake.status = runner.Status{SteeringSupported: &steering, Lifecycle: "disconnected", Runtime: "cluster", Resumable: false, ResumeReason: "Native history unavailable", Engine: "codex", Model: "codex-native", Effort: "high"}
	srvAPI.IngestOnce(ctx)
	var sessions []map[string]any
	decodeBody(t, request(t, srv, "GET", "/api/cards/"+card.Card.ID+"/runner-sessions", human, nil, nil), &sessions)
	raw, _ := json.Marshal(sessions)
	if len(sessions) != 1 || sessions[0]["resumable"] != false || sessions[0]["steering_supported"] != true || sessions[0]["board_id"] != board.ID || sessions[0]["session_url"] != "https://runner.example.test/sessions/ext-123" || !strings.Contains(string(raw), "Native history unavailable") {
		t.Fatalf("incorrect recovery metadata: %s", raw)
	}
	fake.lifecycle = "disconnected"
	fake.statusErr = runner.ErrControlUnauthorized
	srvAPI.IngestOnce(ctx)
	var resumable bool
	_ = pool.QueryRow(ctx, `SELECT resumable FROM runner_sessions WHERE id=$1`, rsID).Scan(&resumable)
	if resumable {
		t.Fatal("revoked authority left recovery enabled")
	}
	fake.fallbackErr = runner.ErrSessionGone
	fake.statusErr = runner.ErrControlUnauthorized
	srvAPI.IngestOnce(ctx)
	var preserved string
	_ = pool.QueryRow(ctx, `SELECT lifecycle FROM runner_sessions WHERE id=$1`, rsID).Scan(&preserved)
	if preserved != "disconnected" {
		t.Fatal("privacy404 under fallback authority ended the owner's session")
	}
	fake.fallbackErr = nil
	fake.statusErr = errors.New("transient") // transient failure never fabricates stopped
	srvAPI.IngestOnce(ctx)
	var lc string
	_ = pool.QueryRow(ctx, `SELECT lifecycle FROM runner_sessions WHERE id=$1`, rsID).Scan(&lc)
	if lc == "stopped" {
		t.Fatal("temporary status error ended session")
	}
	fake.statusErr = runner.ErrSessionGone
	srvAPI.IngestOnce(ctx)
	_ = pool.QueryRow(ctx, `SELECT lifecycle,resumable FROM runner_sessions WHERE id=$1`, rsID).Scan(&lc, &resumable)
	if lc != "stopped" || resumable {
		t.Fatalf("gone session remains recoverable: %s/%v", lc, resumable)
	}
}
