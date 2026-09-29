package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
)

// fakeRunner drives the contract from the other side. mu guards the recorded
// calls: tests that fire concurrent dispatcher passes hit Start from several
// goroutines at once.
type fakeRunner struct {
	mu        sync.Mutex
	started   []runner.StartRequest
	lifecycle string
	// errorReason is what the runner recorded about a failed session; it
	// reaches blerg-board only through Status (the runner's own error
	// broadcast is one-shot and long gone by poll time).
	errorReason string
	gone        bool // Status reports ErrSessionGone (runner 404s the session)
	events      []runner.Event
	messaged    []fakeMessage
	startErr    error  // when set, Start fails instead of succeeding
	attempts    int    // every Start asked for, refused ones included
	onStart     func() // fires inside Start, before it returns: the window
	//                  between "the runner has the session" and "blerg-board has
	//                  written that down" is where a cancelled context does
	//                  its damage, and this is how a test gets into it

	modelSet    []fakeModelSet // every SetModel the API asked for
	setModelErr error          // when set, SetModel fails instead of succeeding
}

// startCount is the race-free reader for len(f.started).
func (f *fakeRunner) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

type fakeMessage struct {
	SessionID, Text, Source string
}

type fakeModelSet struct {
	SessionID, Model string
}

func (f *fakeRunner) Name() string { return "fake" }
func (f *fakeRunner) Start(_ context.Context, r runner.StartRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.startErr != nil {
		return "", f.startErr
	}
	f.started = append(f.started, r)
	if f.onStart != nil {
		f.onStart()
	}
	if len(f.started) == 1 {
		return "ext-123", nil
	}
	// later sessions get their own id, so a test that must tell "one session"
	// from "two sessions on the same card" can
	return fmt.Sprintf("ext-123-%d", len(f.started)), nil
}

// attemptCount is the race-free reader for f.attempts. It counts refusals too,
// which startCount cannot: "how often did we ask a runner that keeps saying
// no" is exactly the question the capacity backoff answers.
func (f *fakeRunner) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}
func (f *fakeRunner) Status(_ context.Context, _ string) (runner.Status, error) {
	if f.gone {
		return runner.Status{}, runner.ErrSessionGone
	}
	return runner.Status{Lifecycle: f.lifecycle, Runtime: "cluster", Resumable: false, ErrorReason: f.errorReason}, nil
}
func (f *fakeRunner) Message(_ context.Context, sessionID, text, source string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messaged = append(f.messaged, fakeMessage{sessionID, text, source})
	return nil
}
func (f *fakeRunner) SetModel(_ context.Context, sessionID, model string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setModelErr != nil {
		return f.setModelErr
	}
	f.modelSet = append(f.modelSet, fakeModelSet{sessionID, model})
	return nil
}
func (f *fakeRunner) Interrupt(_ context.Context, _ string) error { return nil }
func (f *fakeRunner) Stop(_ context.Context, _ string) error      { return nil }
func (f *fakeRunner) Events(_ context.Context, _ string, after int64, _ int) ([]runner.Event, bool, error) {
	var out []runner.Event
	for _, ev := range f.events {
		if ev.Seq > after {
			out = append(out, ev)
		}
	}
	return out, false, nil
}

func TestSpawnFromCardAndIngest(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running", events: []runner.Event{
		{Seq: 1, Ts: "2026-07-31T20:00:00Z", Kind: "assistant_turn", Payload: json.RawMessage(`{"text":"reading the card"}`)},
		{Seq: 2, Ts: "2026-07-31T20:00:05Z", Kind: "tool_call", Payload: json.RawMessage(`{"name":"bash"}`)},
	}}
	apiSrv := srvAPI // captured by testServer
	apiSrv.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc", UIBase: "https://runner.test/sessions"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("build the thing"), Repos: &[]string{"blerg-board"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}

	// Spawn from the card (human).
	resp := request(t, srv, "POST", "/api/cards/"+res.Card.ID+"/spawn", cookie, nil,
		map[string]string{"prompt": "focus on tests"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("spawn: %d", resp.StatusCode)
	}
	var spawned struct {
		RunnerSessionID   string `json:"runner_session_id"`
		ExternalSessionID string `json:"external_session_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&spawned)
	if spawned.ExternalSessionID != "ext-123" {
		t.Fatalf("external id: %q", spawned.ExternalSessionID)
	}
	// The runner got the card env: token + board + card.
	st := fake.started[0]
	if st.Repo != "blerg-board" || st.Env["BLERG_BOARD_TOKEN"] == "" || st.Env["BLERG_BOARD_BOARD"] != board.ID {
		t.Fatalf("start request env wrong: %+v", st)
	}
	// The minted token is live and board-scoped.
	tok, err := db.ValidateToken(ctx, pool, st.Env["BLERG_BOARD_TOKEN"])
	if err != nil || tok.BoardID == nil || *tok.BoardID != board.ID {
		t.Fatalf("session token invalid: %v", err)
	}
	// The card gained a session link built from the configured UI base.
	card, _ := db.GetCard(ctx, pool, res.Card.ID)
	if len(card.Links) != 1 || card.Links[0].Kind != "session" ||
		card.Links[0].URL != "https://runner.test/sessions/ext-123" {
		t.Fatalf("card links: %+v", card.Links)
	}

	// Ingest pulls the events.
	apiSrv.IngestOnce(ctx)
	resp = request(t, srv, "GET", "/api/runner-sessions/"+spawned.RunnerSessionID+"/events", cookie, nil, nil)
	var events []struct {
		Seq  int64  `json:"seq"`
		Kind string `json:"kind"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&events)
	if len(events) != 2 || events[0].Kind != "assistant_turn" {
		t.Fatalf("ingested events: %+v", events)
	}

	// Terminal lifecycle revokes the session token.
	fake.lifecycle = "stopped"
	apiSrv.IngestOnce(ctx)
	if _, err := db.ValidateToken(ctx, pool, st.Env["BLERG_BOARD_TOKEN"]); err == nil {
		t.Fatal("token must be revoked when the session ends")
	}
	// And ingest stops tracking it (no panic / no further updates needed).
	apiSrv.IngestOnce(ctx)
}

// TestSpawnOmitsSessionLinkWithoutUIBase: RUNNER_UI_BASE unset means the
// runner has no browsable UI to link to — the card must not gain a link.
func TestSpawnOmitsSessionLinkWithoutUIBase(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	apiSrv := srvAPI
	apiSrv.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("build the thing"), Repos: &[]string{"blerg-board"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	resp := request(t, srv, "POST", "/api/cards/"+res.Card.ID+"/spawn", cookie, nil, map[string]string{})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("spawn: %d", resp.StatusCode)
	}
	card, _ := db.GetCard(ctx, pool, res.Card.ID)
	if len(card.Links) != 0 {
		t.Fatalf("expected no session link without UIBase, got: %+v", card.Links)
	}
}

// TestIngestSettlesSessionTheRunnerHasForgotten covers the case that never
// reached a terminal lifecycle before this fix: blerg-runner 404s the session
// (its Job finished and was reaped) without ever reporting "stopped". Ingest
// must settle the row itself — stale rows otherwise poll forever, pin as
// "current" for the review-findings relay, and count as an active reviewer.
func TestIngestSettlesSessionTheRunnerHasForgotten(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "idle"}
	apiSrv := srvAPI
	apiSrv.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc", UIBase: "https://runner.test/sessions"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("build the thing"), Repos: &[]string{"blerg-board"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	resp := request(t, srv, "POST", "/api/cards/"+res.Card.ID+"/spawn", cookie, nil, map[string]string{})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("spawn: %d", resp.StatusCode)
	}
	var spawned struct {
		RunnerSessionID string `json:"runner_session_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&spawned)
	st := fake.started[0]

	// The runner settles at "idle", never a terminal lifecycle — ingest must
	// not touch it yet.
	apiSrv.IngestOnce(ctx)
	resp = request(t, srv, "GET", "/api/cards/"+res.Card.ID+"/runner-sessions", cookie, nil, nil)
	var sessions []struct {
		Lifecycle string `json:"lifecycle"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&sessions)
	if len(sessions) != 1 || sessions[0].Lifecycle != "idle" {
		t.Fatalf("expected idle session, got: %+v", sessions)
	}
	if _, err := db.ValidateToken(ctx, pool, st.Env["BLERG_BOARD_TOKEN"]); err != nil {
		t.Fatal("token must still be live while the session is only idle")
	}

	// Now the runner has no record of it at all (404) — ingest settles it
	// as stopped and revokes its token.
	fake.gone = true
	apiSrv.IngestOnce(ctx)
	resp = request(t, srv, "GET", "/api/cards/"+res.Card.ID+"/runner-sessions", cookie, nil, nil)
	_ = json.NewDecoder(resp.Body).Decode(&sessions)
	if len(sessions) != 1 || sessions[0].Lifecycle != "stopped" {
		t.Fatalf("expected the forgotten session to settle as stopped, got: %+v", sessions)
	}
	if _, err := db.ValidateToken(ctx, pool, st.Env["BLERG_BOARD_TOKEN"]); err == nil {
		t.Fatal("token must be revoked once the runner forgets the session")
	}
	// Idempotent: ingest no longer polls it (it's excluded by the terminal
	// filter), so re-running must not panic or error.
	apiSrv.IngestOnce(ctx)
}

func strPtr(s string) *string { return &s }
