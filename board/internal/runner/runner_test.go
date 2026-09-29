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
