package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A 404 from blerg-runner means the runner has no record of the session — its Job
// finished and was reaped, or it never existed. Status must surface this as
// ErrSessionGone so ingest can settle the row instead of polling a corpse
// forever.
func TestBlergRunnerStatusNotFoundIsSessionGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no such session"}`))
	}))
	defer srv.Close()

	k := NewBlergRunner(srv.URL, "key", "")
	_, err := k.Status(context.Background(), "ext-gone")
	if err == nil {
		t.Fatal("expected an error for a 404 session")
	}
	if !errors.Is(err, ErrSessionGone) {
		t.Fatalf("expected ErrSessionGone, got: %v", err)
	}
}

// Other failures (5xx, network errors, non-404 4xx) must NOT be mistaken for
// a gone session — ingest should keep polling and leave the lifecycle alone.
func TestBlergRunnerStatusServerErrorIsNotSessionGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	k := NewBlergRunner(srv.URL, "key", "")
	_, err := k.Status(context.Background(), "ext-broken")
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if errors.Is(err, ErrSessionGone) {
		t.Fatalf("500 must not be treated as ErrSessionGone: %v", err)
	}
}

// SetModel: the happy path is a JSON answer, and the request must carry the
// model to the documented route.
func TestBlergRunnerSetModel(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	k := NewBlergRunner(srv.URL, "key", "")
	if err := k.SetModel(context.Background(), "ext-1", "claude-sonnet-5"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if gotPath != "/api/runner/sessions/ext-1/model" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, `"model":"claude-sonnet-5"`) {
		t.Fatalf("body = %q", gotBody)
	}
}

// A runner with no such route serves its single-page app instead: HTTP 200
// with an HTML body. That is "unsupported", not success — otherwise blerg-board
// would tell a session its model changed when nothing happened.
func TestBlergRunnerSetModelSPAFallbackIsUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>app</body></html>"))
	}))
	defer srv.Close()

	err := NewBlergRunner(srv.URL, "key", "").SetModel(context.Background(), "ext-1", "claude-sonnet-5")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got: %v", err)
	}
}

// Strictly-routed runners answer 405/501 for a verb they don't have; a 404
// keeps its contract meaning of "no such session".
func TestBlergRunnerSetModelStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusMethodNotAllowed, ErrUnsupported},
		{http.StatusNotImplemented, ErrUnsupported},
		{http.StatusNotFound, ErrSessionGone},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
		}))
		err := NewBlergRunner(srv.URL, "key", "").SetModel(context.Background(), "ext-1", "m")
		srv.Close()
		if !errors.Is(err, tc.want) {
			t.Fatalf("HTTP %d: expected %v, got: %v", tc.status, tc.want, err)
		}
	}
}

// 204 No Content is a legitimate success even though it carries no JSON.
func TestBlergRunnerSetModelNoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := NewBlergRunner(srv.URL, "key", "").SetModel(context.Background(), "ext-1", "m"); err != nil {
		t.Fatalf("204 should succeed, got: %v", err)
	}
}

// startBody runs one Start against a fake runner and returns the JSON body it
// posted.
func startBody(t *testing.T, k *BlergRunner) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runner/start" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"session_id":"ext-1"}`))
	}))
	defer srv.Close()
	k.url = srv.URL
	id, err := k.Start(context.Background(), StartRequest{Repo: "app", Prompt: "do it", Token: "automation-tok"})
	if err != nil || id != "ext-1" {
		t.Fatalf("Start = %q, %v", id, err)
	}
	return got
}

// The interaction mode is the caller's to say: sent as `interaction` when set,
// left out (the runner's own default) when not.
func TestBlergRunnerStartSendsInteraction(t *testing.T) {
	for _, mode := range []string{"interactive", "unattended"} {
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"session_id":"ext-1"}`))
		}))
		k := NewBlergRunner(srv.URL, "shared-key", "")
		_, err := k.Start(context.Background(), StartRequest{Repo: "app", Token: "automation-tok", Interaction: mode})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if body["interaction"] != mode {
			t.Errorf("start interaction = %v, want %s", body["interaction"], mode)
		}
	}
	// startBody's request sets none.
	if v, ok := startBody(t, NewBlergRunner("", "shared-key", ""))["interaction"]; ok {
		t.Errorf("a start with no Interaction sent interaction = %v, want the key omitted", v)
	}
}

// Start is made under the per-call token — never the shared key — and carries
// the engine; every other verb keeps using the shared key.
func TestBlergRunnerStartUsesPerCallTokenAndEngine(t *testing.T) {
	var startAuth, statusAuth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runner/start":
			startAuth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"session_id":"ext-1"}`))
		default:
			statusAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"lifecycle":"running"}`))
		}
	}))
	defer srv.Close()
	k := NewBlergRunner(srv.URL, "shared-key", "")
	if _, err := k.Start(context.Background(), StartRequest{Repo: "app", Token: "automation-tok", Engine: "hermes"}); err != nil {
		t.Fatal(err)
	}
	if startAuth != "Bearer automation-tok" {
		t.Errorf("start Authorization = %q, want the automation token", startAuth)
	}
	if body["engine"] != "hermes" {
		t.Errorf("start engine = %v, want hermes", body["engine"])
	}
	if _, err := k.Status(context.Background(), "ext-1"); err != nil {
		t.Fatal(err)
	}
	if statusAuth != "Bearer shared-key" {
		t.Errorf("status Authorization = %q, want the shared key", statusAuth)
	}
}

// No token, no start: the driver never falls back to the shared key, and never
// even calls the runner.
func TestBlergRunnerStartRefusesWithoutToken(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	_, err := NewBlergRunner(srv.URL, "shared-key", "").Start(context.Background(), StartRequest{Repo: "app"})
	if !errors.Is(err, ErrNoStartIdentity) {
		t.Fatalf("err = %v, want ErrNoStartIdentity", err)
	}
	if called {
		t.Fatal("the runner was called without a start identity")
	}
}

// A 401/403 on start is ErrStartUnauthorized — the board's token, not the card.
func TestBlergRunnerStartUnauthorized(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"unauthorized"}`, code)
		}))
		_, err := NewBlergRunner(srv.URL, "shared-key", "").Start(context.Background(),
			StartRequest{Repo: "app", Token: "dead-token"})
		srv.Close()
		if !errors.Is(err, ErrStartUnauthorized) {
			t.Errorf("HTTP %d: err = %v, want ErrStartUnauthorized", code, err)
		}
		if err != nil && strings.Contains(err.Error(), "dead-token") {
			t.Errorf("HTTP %d: error leaks the token: %v", code, err)
		}
	}
}

// With no runtime pinned the board leaves the choice to the runner: the start
// body carries no runtime key at all, so the runner's own default applies.
func TestBlergRunnerStartOmitsRuntimeByDefault(t *testing.T) {
	body := startBody(t, NewBlergRunner("http://unused", "key", ""))
	if _, ok := body["runtime"]; ok {
		t.Fatalf("runtime sent with nothing pinned: %v", body)
	}
}

// A pinned runtime (BLERG_BOARD_RUNNER_RUNTIME) travels on every start.
func TestBlergRunnerStartSendsPinnedRuntime(t *testing.T) {
	for _, rt := range []string{"daemon", "docker", "cluster"} {
		k := NewBlergRunner("http://unused", "key", "")
		k.SetRuntime(rt)
		for i := 0; i < 2; i++ {
			if got := startBody(t, k)["runtime"]; got != rt {
				t.Fatalf("pinned %q, start %d sent runtime = %v", rt, i, got)
			}
		}
	}
}

// ParseRuntime accepts exactly the runner contract's values plus empty, and
// names the variable and the value when it refuses anything else.
func TestParseRuntime(t *testing.T) {
	for _, v := range []string{"", "daemon", "docker", "cluster"} {
		got, err := ParseRuntime(v)
		if err != nil || got != v {
			t.Errorf("ParseRuntime(%q) = %q, %v", v, got, err)
		}
	}
	for _, v := range []string{"kubernetes", "Docker", "host", " daemon"} {
		_, err := ParseRuntime(v)
		if err == nil || !strings.Contains(err.Error(), "BLERG_BOARD_RUNNER_RUNTIME") || !strings.Contains(err.Error(), v) {
			t.Errorf("ParseRuntime(%q) error = %v, want one naming the variable and the value", v, err)
		}
	}
}

// Events blerg-board does not draw are not stored: raw provider blocks, thinking, the running
// totals of a subagent and the background-task events. Everything else comes through (unmapped
// kinds as "status").
func TestBlergRunnerEventsDropsUndrawnKinds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"has_more":false,"events":[
			{"seq":1,"kind":"provider_blocks","payload":{}},
			{"seq":2,"kind":"assistant_thinking","payload":{"text":"hmm"}},
			{"seq":3,"kind":"subagent_update","payload":{"child_id":"c"}},
			{"seq":4,"kind":"assistant_text","payload":{"text":"hi","done":true}},
			{"seq":5,"kind":"background_task_started","payload":{"task_id":"b1"}},
			{"seq":6,"kind":"subagent_done","payload":{"child_id":"c"}},
			{"seq":7,"kind":"background_task_done","payload":{"task_id":"b1"}}]}`))
	}))
	defer srv.Close()

	k := NewBlergRunner(srv.URL, "key", "")
	events, _, err := k.Events(context.Background(), "ext-1", 0, 200)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	if got := strings.Join(kinds, ","); got != "assistant_turn,status" {
		t.Fatalf("kinds = %s, want assistant_turn,status", got)
	}
	var wrapped struct {
		SourceKind string `json:"source_kind"`
	}
	_ = json.Unmarshal(events[1].Payload, &wrapped)
	if wrapped.SourceKind != "subagent_done" {
		t.Fatalf("source_kind = %q", wrapped.SourceKind)
	}
}

func TestIdentityControlsUseFreshTokenAndPreserveSettings(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer renewed-owner" {
			t.Error("control did not use fresh owner authority")
		}
		var body map[string]any
		if r.Method == "POST" {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		got = append(got, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"lifecycle":"disconnected","resumable":true,"resume_mode":"native"}`))
	}))
	defer srv.Close()
	k := NewBlergRunner(srv.URL, "operator-key", "")
	if err := k.MessageAs(context.Background(), "renewed-owner", "ext-1", "hello", "human", "stable-turn"); err != nil {
		t.Fatal(err)
	}
	if err := k.SetModelAs(context.Background(), "renewed-owner", "ext-1", "codex-model", "high"); err != nil {
		t.Fatal(err)
	}
	st, err := k.StatusAs(context.Background(), "renewed-owner", "ext-1")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Resumable || st.ResumeMode != "native" {
		t.Fatalf("recovery metadata lost: %+v", st)
	}
	if got[0]["client_id"] != "stable-turn" || got[1]["effort"] != "high" {
		t.Fatalf("control fields lost: %#v", got)
	}
}

func TestDiscoveryUsesOwnerAndConfiguredTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer owner-proof" {
			t.Error("discovery used shared authority")
		}
		if r.URL.Path != "/api/runner/launch-options" || r.URL.Query().Get("runtime") != "cluster" || r.URL.Query().Get("repo") != "app" || r.URL.Query().Get("kind") != "agent" {
			t.Error("discovery target disagrees with launch")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"known","runtime":"cluster","engines":[{"id":"codex","name":"Codex","models":{"models":[{"id":"runtime-model","efforts":["high"]}]}}],"token":"must-not-be-returned"}`))
	}))
	defer srv.Close()
	k := NewBlergRunner(srv.URL, "operator", "")
	k.SetRuntime("cluster")
	opts, err := k.Discover(context.Background(), "owner-proof", "app", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Engines) != 1 || opts.Engines[0].Models.Models[0].ID != "runtime-model" {
		t.Fatalf("wrong discovered models: %+v", opts)
	}
	raw, _ := json.Marshal(opts)
	if strings.Contains(string(raw), "must-not-be-returned") {
		t.Fatal("arbitrary upstream metadata escaped typed discovery")
	}
}

func TestRunnerErrorsDoNotEchoOwnerProof(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(r.Header.Get("Authorization")))
	}))
	defer srv.Close()
	err := NewBlergRunner(srv.URL, "operator", "").MessageAs(context.Background(), "private-proof", "ext", "hello", "human", "")
	if err == nil || strings.Contains(err.Error(), "private-proof") {
		t.Fatal("runner error echoed credential")
	}
}

func TestSessionDiscoveryCannotDriftToBoardDefaultTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("session_id") != "ext-1" || q.Get("runtime") != "" || q.Get("repo") != "" || q.Get("daemon_id") != "" {
			t.Errorf("session discovery overrides existing target: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"known","engines":[]}`))
	}))
	defer srv.Close()
	k := NewBlergRunner(srv.URL, "operator", "")
	k.SetRuntime("cluster")
	if _, err := k.DiscoverSession(context.Background(), "owner", "ext-1", true); err != nil {
		t.Fatal(err)
	}
}

// Raw sends an upload's declared size (the chat proxy's Content-Length header) as the request's
// own length, so the runner can refuse a file over its limit before reading it; without one the
// body goes chunked.
func TestRawSendsTheDeclaredContentLength(t *testing.T) {
	var gotLen int64
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotLen, gotBody = r.ContentLength, string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	k := NewBlergRunner(srv.URL, "key", "")

	for _, c := range []struct {
		header string
		want   int64
	}{{"7", 7}, {"", -1}, {"junk", -1}} {
		headers := map[string]string{"X-Artifact-Name": "a.txt"}
		if c.header != "" {
			headers["Content-Length"] = c.header
		}
		resp, err := k.Raw(context.Background(), http.MethodPost, "/api/runner/sessions/s/uploads", io.MultiReader(strings.NewReader("abc"), strings.NewReader("defg")), headers)
		if err != nil {
			t.Fatalf("Raw(%q): %v", c.header, err)
		}
		resp.Body.Close()
		if gotLen != c.want || gotBody != "abcdefg" {
			t.Errorf("Content-Length %q: runner saw length %d body %q, want %d", c.header, gotLen, gotBody, c.want)
		}
	}
}

// A start the runner refuses (422) reports the runner's own reason as a StartRefusedError, not the
// HTTP exchange, and never carries the start credential.
func TestBlergRunnerStartRefusalIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"repo must be given as org/name for cluster sessions (or set BLERG_RUNNER_GITHUB_ORG)"}`))
	}))
	defer srv.Close()
	_, err := NewBlergRunner(srv.URL, "key", "").Start(context.Background(), StartRequest{Repo: "app", Token: "secret-start-token"})
	var refused *StartRefusedError
	if !errors.As(err, &refused) || !strings.HasPrefix(refused.Message, ClusterBareRepoRefusal) {
		t.Fatalf("err = %v, want a StartRefusedError with the runner's reason", err)
	}
	if strings.Contains(err.Error(), "HTTP") || strings.Contains(err.Error(), "secret-start-token") {
		t.Fatalf("err = %q", err)
	}
}

// Every runner refusal of a start (409 workspace selection, 503 capacity, …) is a typed
// StartRefusedError carrying the runner's words and its status, never the HTTP exchange.
func TestBlergRunnerStartRefusalsOfAnyStatusAreTyped(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusConflict, `{"error":"Select an existing repository using fresh source references."}`, "Select an existing repository"},
		{http.StatusServiceUnavailable, `{"error":"session cap reached"}`, "session cap reached"},
		{http.StatusBadGateway, `<html>bad gateway</html>`, "HTTP 502"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := NewBlergRunner(srv.URL, "key", "").Start(context.Background(), StartRequest{Repo: "app", Token: "secret-start-token"})
		srv.Close()
		var refused *StartRefusedError
		if !errors.As(err, &refused) || refused.Status != tc.status || !strings.Contains(refused.Message, tc.want) {
			t.Fatalf("%d: err = %v", tc.status, err)
		}
		if strings.Contains(err.Error(), "blerg-runner POST") || strings.Contains(err.Error(), "secret-start-token") {
			t.Fatalf("%d: raw exchange in %q", tc.status, err)
		}
	}
}
