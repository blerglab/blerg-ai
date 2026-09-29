package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// postSession runs POST /api/sessions against a daemon d1 and returns the
// status plus the spawn_session the daemon received (nil if none).
func postSession(t *testing.T, body map[string]any) (int, *protocol.SpawnSession) {
	t.Helper()
	hub := NewHub()
	dc := &DaemonConn{ID: "d1", Name: "laptop", send: make(chan []byte, 8)}
	hub.Register(dc)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	tok := enableBrowserAuth(t, api)("session.start")
	if _, ok := body["daemon_id"]; !ok {
		body["daemon_id"] = "d1"
	}
	if _, ok := body["repo"]; !ok {
		body["repo"] = "my-app"
	}
	if _, ok := body["runtime"]; !ok {
		body["runtime"] = "docker"
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostSessions(rec, req)
	select {
	case m := <-dc.send:
		var msg protocol.SpawnSession
		if err := json.Unmarshal(m, &msg); err != nil {
			t.Fatal(err)
		}
		return rec.Code, &msg
	default:
		return rec.Code, nil
	}
}

func TestPostSessionsCarriesModelAndEffort(t *testing.T) {
	for _, kind := range []string{"", "agent"} {
		code, msg := postSession(t, map[string]any{"kind": kind, "model": "claude-opus-5-5", "effort": "xhigh"})
		if code != http.StatusAccepted || msg == nil {
			t.Fatalf("kind %q: status %d, msg %v", kind, code, msg)
		}
		if msg.Model != "claude-opus-5-5" || msg.Effort != "xhigh" {
			t.Errorf("kind %q: spawn carried model %q effort %q", kind, msg.Model, msg.Effort)
		}
	}
	// No effort: none is sent (the model's own default applies).
	_, msg := postSession(t, map[string]any{"model": "sonnet"})
	if msg == nil || msg.Effort != "" || msg.Model != "sonnet" {
		t.Errorf("alias without effort: %+v", msg)
	}
}

// The launch effort is written to the session row at spawn, so the session
// list shows it before any engine reports one. Requires TEST_DATABASE_URL.
func TestPostSessionsRecordsLaunchEffort(t *testing.T) {
	api, _, dc, pool := spawnTokenFixture(t)
	tok := enableBrowserAuth(t, api)("session.start")
	body, _ := json.Marshal(map[string]any{"daemon_id": dc.ID, "repo": "app", "runtime": "docker",
		"kind": "agent", "model": "claude-opus-5-5", "effort": "max"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostSessions(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	row, err := db.GetSession(context.Background(), pool, resp["session_id"])
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.Effort == nil || *row.Effort != "max" || row.Model == nil || *row.Model != "claude-opus-5-5" {
		t.Fatalf("row model %v effort %v", row.Model, row.Effort)
	}
	if info := sessionRowToInfo(*row, ""); info.Effort != "max" {
		t.Errorf("session info effort = %q", info.Effort)
	}
}

func TestPostSessionsRejectsBadModelOrEffort(t *testing.T) {
	for _, body := range []map[string]any{
		{"model": "sonnet --dangerously-skip-permissions"},
		{"model": "-p"},
		{"model": "$(id)"},
		{"model": "claude-opus;rm"},
		{"model": "sonnet", "effort": "ultra"},
		{"model": "sonnet", "effort": "high; id"},
		{"model": "sonnet", "effort": "HIGH"},
		{"engine": "codex", "model": "gpt 5"},
		{"engine": "codex", "effort": "bogus"},
		{"engine": "codex", "effort": "auto"},          // not a level at all
		{"engine": "hermes", "effort": "auto"},         // "Auto" is sent as no effort, never as a value
		{"engine": "hermes", "effort": "high --yolo"},  // injection
		{"engine": "codex", "effort": "high\nsandbox"}, // injection
		// OpenClaw registers no effort levels, so it takes none.
		{"engine": "openclaw", "effort": "high"},
	} {
		code, msg := postSession(t, body)
		if code != http.StatusUnprocessableEntity {
			t.Errorf("%v: status %d, want 422", body, code)
		}
		if msg != nil {
			t.Errorf("%v: a spawn reached the daemon", body)
		}
	}
}

// For a model the engine's list knows, the effort must be one of that model's
// own levels; aliases and unknown ids get the engine rules only.
func TestPostSessionsChecksEffortAgainstTheModelsOwnLevels(t *testing.T) {
	for _, tc := range []struct {
		model, effort string
		want          int
	}{
		{"claude-opus-4-6", "xhigh", http.StatusUnprocessableEntity}, // 4.6 has no xhigh
		{"claude-haiku-4-5-20251001", "high", http.StatusUnprocessableEntity},
		{"claude-opus-4-6", "max", http.StatusAccepted},
		{"claude-opus-5-5", "xhigh", http.StatusAccepted},
		{"sonnet", "xhigh", http.StatusAccepted},          // alias: engine rules only
		{"claude-future-9", "xhigh", http.StatusAccepted}, // not in the list: engine rules only
		{"claude-haiku-4-5-20251001", "", http.StatusAccepted},
	} {
		if code, _ := postSession(t, map[string]any{"model": tc.model, "effort": tc.effort}); code != tc.want {
			t.Errorf("model %s effort %q: %d, want %d", tc.model, tc.effort, code, tc.want)
		}
	}
}

// The documented behaviour for codex on a daemon that reported no list,
// through the real routes and the server's real registry — not a mock source.
func TestCodexThroughTheRealRoutes(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "d1", Name: "laptop", send: make(chan []byte, 8)}
	dc.SetCheckedOutRepos([]string{"app"})
	hub.Register(dc)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	api.SetRunnerKey("runner-key")
	api.SetModelSources(NewModelSources(nil, hub))
	mux := http.NewServeMux()
	RegisterRunnerContractRoutes(mux, api)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer runner-key")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := do("GET", "/api/models/codex", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"models":[]`) || !strings.Contains(rec.Body.String(), `"source":"none"`) {
		t.Errorf("GET /api/models/codex = %d %s", rec.Code, rec.Body.String())
	}
	rec = do("POST", "/api/runner/start", `{"repo":"app","engine":"codex","runtime":"daemon","effort":"auto"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "effort must be one of none, minimal, low, medium, high, xhigh, max, ultra") {
		t.Errorf("codex + foreign effort = %d %s", rec.Code, rec.Body.String())
	}
	rec = do("POST", "/api/runner/start", `{"repo":"app","engine":"openclaw","runtime":"daemon","effort":"high"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "engine openclaw takes no effort") {
		t.Errorf("openclaw + effort = %d %s", rec.Code, rec.Body.String())
	}
	rec = do("POST", "/api/runner/start", `{"repo":"app","engine":"codex","runtime":"daemon","model":"-c"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("codex + flag-shaped model = %d %s", rec.Code, rec.Body.String())
	}
	rec = do("POST", "/api/runner/start", `{"repo":"app","engine":"codex","runtime":"daemon","model":"gpt-5-codex","effort":"ultra"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("codex + model = %d %s", rec.Code, rec.Body.String())
	}
	var msg protocol.SpawnSession
	_ = json.Unmarshal(<-dc.send, &msg)
	if msg.Engine != "codex" || msg.Model != "gpt-5-codex" || msg.Effort != "ultra" {
		t.Errorf("spawn = engine %q model %q effort %q", msg.Engine, msg.Model, msg.Effort)
	}
}

func TestPostSessionsAcceptsProviderShapedModelForOtherEngines(t *testing.T) {
	code, msg := postSession(t, map[string]any{"engine": "hermes", "model": "openrouter/anthropic/claude-sonnet-4"})
	if code != http.StatusAccepted || msg == nil || msg.Model != "openrouter/anthropic/claude-sonnet-4" {
		t.Fatalf("status %d msg %+v", code, msg)
	}
}

func TestRunnerStartValidatesModelAndEffort(t *testing.T) {
	api := NewAPI(NewHub(), nil, "daemon-tok-1234567890", nil, "")
	api.SetRunnerKey("runner-key")
	for _, tc := range []struct {
		model, effort string
	}{
		{"sonnet", "ultra"},
		{"sonnet;id", ""},
		{"--model", "high"},
		{"claude-sonnet-5", "Max"},
	} {
		_, apiErr := api.StartSession(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind},
			runnerStartRequest{Repo: "app", Model: tc.model, Effort: tc.effort}, "")
		if apiErr == nil || apiErr.Status != http.StatusUnprocessableEntity {
			t.Errorf("model %q effort %q: got %v, want 422", tc.model, tc.effort, apiErr)
		}
	}
}

// With a valid model/effort, a daemon-routed start carries both to the daemon.
func TestRunnerStartCarriesEffortToDaemon(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "d1", Name: "laptop", send: make(chan []byte, 8)}
	dc.SetCheckedOutRepos([]string{"app"})
	hub.Register(dc)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	api.SetRunnerKey("runner-key")
	_, apiErr := api.StartSession(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind},
		runnerStartRequest{Repo: "app", Model: "claude-sonnet-5", Effort: "low", Runtime: "daemon"}, "")
	if apiErr != nil {
		t.Fatalf("start: %v", apiErr)
	}
	select {
	case raw := <-dc.send:
		var msg protocol.SpawnSession
		_ = json.Unmarshal(raw, &msg)
		if msg.Model != "claude-sonnet-5" || msg.Effort != "low" {
			t.Errorf("spawn carried model %q effort %q", msg.Model, msg.Effort)
		}
	default:
		t.Fatal("no spawn reached the daemon")
	}
}

// Regression: a bad in-chat /model or /effort that reached the session row
// (model_changed from a daemon or pod built before in-session changes were
// validated) must not turn a cluster resume into a pod the runner refuses.
// The resume drops what fails the engine's rules and keeps what passes.
// Requires TEST_DATABASE_URL.
func TestResumeAfterBadInChatValueDropsIt(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	daemonID := "00000000-0000-0000-0000-0000000000f1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	for _, tc := range []struct {
		name, id, model, effort string
		wantModel, wantEffort   string
	}{
		{"bad model and effort", "00000000-0000-0000-0000-0000000000f2", "Foo Bar", "foo", "", ""},
		{"bad effort only", "00000000-0000-0000-0000-0000000000f3", "claude-opus-5-5", "ultra", "claude-opus-5-5", ""},
		{"both good", "00000000-0000-0000-0000-0000000000f4", "claude-opus-5-5", "max", "claude-opus-5-5", "max"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.InsertSession(ctx, pool, tc.id, daemonID, "running", "/workspace/proj", "proj", "T", "claude-sonnet-5"); err != nil {
				t.Fatalf("InsertSession: %v", err)
			}
			// The in-chat change as it was persisted: a model_changed event.
			payload, _ := json.Marshal(map[string]string{"model": tc.model, "effort": tc.effort, "source": "command"})
			hub := NewHub()
			dc := &DaemonConn{ID: daemonID, send: make(chan []byte, 8)}
			HandleAgentEvent(ctx, hub, pool, dc, protocol.AgentEvent{
				Type: "agent_event", SessionID: tc.id, ClientEventID: newUUID(), Kind: "model_changed", Payload: payload,
			})
			if row, _ := db.GetSession(ctx, pool, tc.id); row == nil || derefOrEmpty(row.Effort) != tc.effort {
				t.Fatalf("setup: the row should hold the persisted effort %q", tc.effort)
			}
			if _, err := pool.Exec(ctx, "UPDATE sessions SET status = 'disconnected' WHERE id = $1", tc.id); err != nil {
				t.Fatal(err)
			}
			f := &fakeK8s{}
			hub.SetJobManager(newTestJobManager(t, f))
			resumeClusterSession(ctx, hub, pool, tc.id, "continue", "", testSID)
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.created) != 1 {
				t.Fatalf("resume created %d Jobs, want 1", len(f.created))
			}
			raw, _ := json.Marshal(f.created[0])
			body := string(raw)
			for _, want := range []string{
				`"BLERG_RUNNER_MODEL","value":"` + tc.wantModel + `"`,
				`"BLERG_RUNNER_EFFORT","value":"` + tc.wantEffort + `"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("Job missing %s", want)
				}
			}
		})
	}
}

// Adding effort must not change the idempotency hash of a request that does
// not use it — a retry across the upgrade would otherwise 409.
func TestStartRequestHashStableWithoutEffort(t *testing.T) {
	h, err := startRequestHash(runnerStartRequest{Repo: "app", Model: "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(runnerStartRequest{Repo: "app", Model: "sonnet"})
	if strings.Contains(string(raw), "effort") {
		t.Errorf("an unset effort must be omitted from the hashed body: %s", raw)
	}
	h2, _ := startRequestHash(runnerStartRequest{Repo: "app", Model: "sonnet", Effort: "high"})
	if h == h2 {
		t.Error("a different effort must be a different request")
	}
}

func TestClusterJobCarriesEffort(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s-eff", Repo: "app", Model: "claude-opus-5-5", Effort: "max"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.created[0])
	body := string(raw)
	for _, want := range []string{
		`"BLERG_RUNNER_MODEL","value":"claude-opus-5-5"`,
		`"BLERG_RUNNER_EFFORT","value":"max"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("manifest missing %s", want)
		}
	}
}
