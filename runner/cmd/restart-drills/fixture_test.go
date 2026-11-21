package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestEngineBarrierAndCommitThroughHTTP(t *testing.T) {
	previousDir, previousTransport, previousURL := dataDir, fixtureTransport, fixtureControlURL
	t.Cleanup(func() { dataDir = previousDir; fixtureTransport = previousTransport; fixtureControlURL = previousURL })
	dataDir = t.TempDir()
	workspace := filepath.Join(dataDir, "repos", ".scratch-fixture")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	t.Setenv("DRILL_CONTROL_KEY", "fixture-control")
	t.Setenv("BLERG_RUNNER_SESSION_ID", "session")
	if err := save(dataDir, "active.json", turnState{Case: "server", Session: "session"}); err != nil {
		t.Fatal(err)
	}
	if err := save(dataDir, "private.json", privateState{Token: "fixture-token"}); err != nil {
		t.Fatal(err)
	}
	if err := save(dataDir, "daemon.json", daemonState{Boot: "first"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	completed, stopped := false, false
	prefix := `{"events":[{"seq":1,"kind":"user_message","payload":{"text":"start"}}]}`
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/events"):
			if !completed {
				_, _ = w.Write([]byte(prefix))
			} else {
				_, _ = w.Write([]byte(`{"events":[{"seq":1,"kind":"user_message","payload":{"text":"start"}},{"seq":2,"kind":"assistant_text","payload":{"text":"restart-drill-committed","done":true}},{"seq":3,"kind":"turn_done","payload":{"stop_reason":"end_turn"}}]}`))
			}
		case strings.HasSuffix(r.URL.Path, "/stop"):
			stopped = true
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{"lifecycle":"ended"}`))
		}
	}))
	defer runner.Close()
	runnerURL, _ := url.Parse(runner.URL)
	base := http.DefaultTransport
	fixtureTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "blerg-runner:8080" {
			clone := r.Clone(r.Context())
			u := *r.URL
			u.Host = runnerURL.Host
			clone.URL = &u
			return base.RoundTrip(clone)
		}
		return base.RoundTrip(r)
	})
	f := &fixture{barrierNotice: make(chan struct{}, 1)}
	server := httptest.NewServer(f.handler())
	defer server.Close()
	fixtureControlURL = server.URL
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- engineTurn(ctx, "native-fixture") }()
	select {
	case <-f.barrierNotice:
	case <-ctx.Done():
		t.Fatal("no mid-turn barrier")
	}
	var active turnState
	if readJSON(filepath.Join(dataDir, "active.json"), &active) != nil || active.Base == "" {
		t.Fatal("barrier not durable")
	}
	count, err := git(ctx, workspace, "rev-list", "--count", active.Base+"..HEAD")
	if err != nil || count != "0" {
		t.Fatal("committed before fault barrier")
	}
	if err = os.MkdirAll(filepath.Join(dataDir, "state", "agents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = save(filepath.Join(dataDir, "state", "agents"), "session.json", object{"session_id": "session", "work_dir": workspace, "turn_active": true, "claude_session_id": "native-fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err = control(ctx, "armed", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = control(ctx, "release", nil); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	completed = true
	mu.Unlock()
	raw, err := control(ctx, "verify", nil)
	if err != nil {
		t.Fatal(err)
	}
	if assertions(raw) != nil {
		t.Fatal("incomplete proof")
	}
	mu.Lock()
	defer mu.Unlock()
	if !stopped {
		t.Fatal("session not stopped")
	}
}
func TestFixtureRefusesUnauthenticatedControl(t *testing.T) {
	t.Setenv("DRILL_CONTROL_KEY", "private")
	f := &fixture{}
	request := httptest.NewRequest("POST", "/release", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	f.handler().ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("unauthorized control accepted")
	}
}
func TestFixtureTransportNeverLeaksBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "credential-must-not-escape", 500) }))
	defer server.Close()
	err := apiCall(t.Context(), safeClient(), "POST", server.URL, "/", "fixture-secret", "", nil, nil)
	if err == nil || strings.Contains(err.Error(), "credential") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("raw diagnostics escaped")
	}
}
func TestDatabaseURLScope(t *testing.T) {
	for _, dsn := range []string{"", "postgres://blerg:pw@elsewhere.example.invalid:5432/postgres", "postgres://other:pw@restart-postgres:5432/postgres", "https://restart-postgres:5432"} {
		t.Setenv("DRILL_ADMIN_DATABASE_URL", dsn)
		if _, err := dbURL("blerg_core"); err == nil {
			t.Fatal("external database accepted")
		}
	}
	t.Setenv("DRILL_ADMIN_DATABASE_URL", "postgres://blerg:pw@restart-postgres:5432/postgres")
	if _, err := dbURL("blerg_core"); err != nil {
		t.Fatal(err)
	}
}
func TestRegressionReceiptRejectsMissingDuplicateAndFailed(t *testing.T) {
	names := []string{"process-shapes", "parallel-commits", "idle-respawn", "restart-failed-freeze", "restart-child", "discard-cleanup", "discard-delivery", "child-attention", "child-escalation-restart"}
	for _, mode := range []string{"pass", "missing", "duplicate", "failed"} {
		t.Run(mode, func(t *testing.T) {
			rows := []object{}
			for _, name := range names {
				rows = append(rows, object{"Name": name, "Passed": true, "Seconds": 0.1, "Error": "not exported"})
			}
			switch mode {
			case "missing":
				rows = rows[:8]
			case "duplicate":
				rows[1]["Name"] = names[0]
			case "failed":
				rows[0]["Passed"] = false
			}
			dir := t.TempDir()
			if err := save(dir, "summary.json", rows); err != nil {
				t.Fatal(err)
			}
			receipt, err := regressionReceipt(filepath.Join(dir, "summary.json"))
			if (err == nil) != (mode == "pass") {
				t.Fatal("incorrect regression verdict")
			}
			raw, _ := json.Marshal(receipt)
			if strings.Contains(string(raw), "not exported") {
				t.Fatal("raw errors exported")
			}
		})
	}
}

func TestRecoveredEngineRefusesLostMarker(t *testing.T) {
	previous := dataDir
	t.Cleanup(func() { dataDir = previous })
	dataDir = t.TempDir()
	workspace := filepath.Join(dataDir, "repos", ".scratch-fixture")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	if _, err := git(t.Context(), workspace, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if _, err := git(t.Context(), workspace, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "baseline"); err != nil {
		t.Fatal(err)
	}
	base, err := git(t.Context(), workspace, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err = save(dataDir, "active.json", turnState{Path: workspace, Base: base, Native: "native"}); err != nil {
		t.Fatal(err)
	}
	if err = engineTurn(t.Context(), "native"); err == nil || !strings.Contains(err.Error(), "uncommitted marker lost") {
		t.Fatalf("lost marker accepted: %v", err)
	}
}
