package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// The documented lifecycle enum (spec §3) is five values; sessions.status
// carries more (waiting/idle/stopped) because the daemon state machine is
// finer-grained. Both the status and the result endpoint must project onto the
// documented five and nothing else.
func TestRunnerLifecycleMapping(t *testing.T) {
	cases := []struct {
		status      string
		jobFinished bool
		want        string
	}{
		{"starting", false, "starting"},
		{"running", false, "running"},
		{"waiting", false, "running"},
		{"idle", false, "running"},
		{"disconnected", false, "disconnected"},
		{"ended", false, "ended"},
		{"stopped", false, "ended"},
		{"error", false, "error"},
		// A Job that exited while the session never got past "starting" is a
		// startup failure, not a session still booting.
		{"starting", true, "error"},
		{"running", true, "running"},
	}
	for _, c := range cases {
		if got := runnerLifecycle(c.status, c.jobFinished); got != c.want {
			t.Errorf("runnerLifecycle(%q, %v) = %q, want %q", c.status, c.jobFinished, got, c.want)
		}
	}
	for _, c := range []struct {
		lifecycle string
		want      bool
	}{{"starting", false}, {"running", false}, {"disconnected", false}, {"ended", true}, {"error", true}} {
		if got := runnerLifecycleTerminal(c.lifecycle); got != c.want {
			t.Errorf("runnerLifecycleTerminal(%q) = %v, want %v", c.lifecycle, got, c.want)
		}
	}
}

// buildSessionResult is the shared builder Tasks 8–10 (SSE end event, webhook,
// MCP get_result) call, so it is tested directly as well as through the route.
func TestBuildSessionResultRunningAndEnded(t *testing.T) {
	api, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	ctx := context.Background()

	const daemonID = "00000000-0000-4000-8000-0000000000d1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	sessionID := newUUID()
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/workspace/org/proj", "org/proj", "T", ""); err != nil {
		t.Fatal(err)
	}
	// Only the consolidated (done=true) assistant_text events count; a later
	// streaming delta must not win, nor a tool_result.
	for _, ev := range []struct{ kind, payload string }{
		{"assistant_text", `{"text":"first answer","done":true}`},
		{"assistant_text", `{"text":"final answer","done":true}`},
		{"assistant_text", `{"text":"partial…","done":false}`},
		{"tool_result", `{"output":"not an assistant message"}`},
	} {
		if _, _, err := db.AppendAgentEvent(ctx, pool, sessionID, newUUID(), ev.kind, ev.payload); err != nil {
			t.Fatal(err)
		}
	}

	res, err := api.buildSessionResult(ctx, sessionID)
	if err != nil {
		t.Fatalf("buildSessionResult: %v", err)
	}
	if res.SessionID != sessionID || res.Lifecycle != "running" || res.Terminal {
		t.Errorf("running session: %+v", res)
	}
	if res.Repo != "org/proj" {
		t.Errorf("repo = %q, want org/proj", res.Repo)
	}
	if res.Branch != "wip/"+sessionID {
		t.Errorf("branch = %q, want wip/%s", res.Branch, sessionID)
	}
	if res.LastAssistantMessage != "final answer" {
		t.Errorf("last_assistant_message = %q, want the newest done assistant_text", res.LastAssistantMessage)
	}
	if res.Artifacts == nil || len(res.Artifacts) != 0 {
		t.Errorf("artifacts = %v, want an empty (non-null) list", res.Artifacts)
	}
	if res.EndedAt != nil {
		t.Errorf("ended_at = %v, want nil while running", res.EndedAt)
	}

	ended := time.Now()
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "stopped", &ended); err != nil {
		t.Fatal(err)
	}
	res, err = api.buildSessionResult(ctx, sessionID)
	if err != nil {
		t.Fatalf("buildSessionResult (ended): %v", err)
	}
	if res.Lifecycle != "ended" || !res.Terminal {
		t.Errorf("stopped session: lifecycle=%q terminal=%v, want ended/true", res.Lifecycle, res.Terminal)
	}
	if res.EndedAt == nil {
		t.Error("ended_at must be set once the session is over")
	}

	// An unknown session is an error the handler turns into a 404, not a
	// zero-valued result.
	if _, err := api.buildSessionResult(ctx, newUUID()); err == nil {
		t.Error("buildSessionResult on an unknown session: want an error")
	}
}

func TestRunnerResultEndpoint(t *testing.T) {
	api, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	ctx := context.Background()

	const daemonID = "00000000-0000-4000-8000-0000000000d2"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	sessionID := newUUID()
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "error", "/workspace/org/proj", "org/proj", "T", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionError(ctx, pool, sessionID, "clone failed"); err != nil {
		t.Fatal(err)
	}
	// A callback secret on the row must never reach any response body.
	if err := db.SetSessionCallback(ctx, pool, sessionID, "https://hook.example.test/x", "super-secret-hmac-key"); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/runner/sessions/"+sessionID+"/result", nil)
	req.Header.Set("Authorization", "Bearer "+runnerTestKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("result: %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	raw := readAllString(t, resp.Body)
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"session_id", "lifecycle", "terminal", "runtime", "repo", "branch",
		"last_assistant_message", "error_reason", "started_at", "ended_at", "artifacts", "git_url"} {
		if _, ok := body[key]; !ok {
			t.Errorf("result body is missing %q: %s", key, raw)
		}
	}
	if body["lifecycle"] != "error" || body["terminal"] != true {
		t.Errorf("lifecycle/terminal = %v/%v, want error/true", body["lifecycle"], body["terminal"])
	}
	if body["error_reason"] != "clone failed" {
		t.Errorf("error_reason = %v", body["error_reason"])
	}
	if strings.Contains(raw, "super-secret-hmac-key") || strings.Contains(raw, "callback_secret") {
		t.Errorf("the callback secret must never be returned: %s", raw)
	}

	// Unknown session ⇒ 404; unauthenticated ⇒ 401.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/runner/sessions/"+newUUID()+"/result", nil)
	req.Header.Set("Authorization", "Bearer "+runnerTestKey)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("unknown session: %d, want 404", resp2.StatusCode)
	}
	resp3, err := http.Get(srv.URL + "/api/runner/sessions/" + sessionID + "/result")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Errorf("no credential: %d, want 401", resp3.StatusCode)
	}
}

// The status endpoint projects onto the same enum as the result endpoint: a
// "stopped" row must report "ended", not leak the internal value.
func TestRunnerStatusUsesTheDocumentedEnum(t *testing.T) {
	api, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	ctx := context.Background()
	const daemonID = "00000000-0000-4000-8000-0000000000d3"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	sessionID := newUUID()
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "stopped", "/workspace/org/proj", "org/proj", "T", ""); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/runner/sessions/"+sessionID, nil)
	req.Header.Set("Authorization", "Bearer "+runnerTestKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Lifecycle string `json:"lifecycle"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Lifecycle != "ended" {
		t.Errorf("lifecycle = %q, want ended", out.Lifecycle)
	}
}
