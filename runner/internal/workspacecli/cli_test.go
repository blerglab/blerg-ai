package workspacecli

import (
	"bytes"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMetricsUsesScopedServerRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/main/workspace/metrics" || r.URL.Query().Get("session") != "worker" || r.URL.Query().Get("turn") != "17" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Errorf("request did not preserve session scope or authentication")
		}
		_, _ = w.Write([]byte(`{"turn_seq":17}`))
	}))
	defer server.Close()
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", server.URL)
	t.Setenv("BLERG_RUNNER_SESSION_ID", "main")
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-token")
	t.Setenv("BLERG_RUNNER_DAEMON_STATE_DIR", t.TempDir())
	var out, stderr bytes.Buffer
	if code := Main([]string{"metrics", "--json", "--session", "worker", "--turn", "17"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "17") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), stderr.String())
	}
}
func TestMetricsDoesNotExposeServerFailureBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte("private-server-detail"))
	}))
	defer server.Close()
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", server.URL)
	t.Setenv("BLERG_RUNNER_SESSION_ID", "main")
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-token")
	t.Setenv("BLERG_RUNNER_DAEMON_STATE_DIR", t.TempDir())
	var out, stderr bytes.Buffer
	if code := Main([]string{"metrics"}, &out, &stderr); code != 1 || strings.Contains(stderr.String(), "private-server-detail") || strings.Contains(stderr.String(), "fixture-token") {
		t.Fatalf("code=%d error=%s", code, stderr.String())
	}
}

func TestImportExistingGateRecorder(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var run activity.Run
		if err := json.NewDecoder(r.Body).Decode(&run); err != nil {
			t.Fatal(err)
		}
		if r.Method != "POST" || run.ID != "record-runner-unit" || run.Suite != "runner-unit" || run.Commit != "abcdef1" || !run.FullSuite || run.EndedAt.Sub(run.StartedAt) != 10*time.Second {
			t.Errorf("registration: %+v", run)
		}
		requests++
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", server.URL)
	t.Setenv("BLERG_RUNNER_SESSION_ID", "main")
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-token")
	t.Setenv("BLERG_RUNNER_DAEMON_STATE_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "gate.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"record","commit":"abcdef1","full_matrix":true,"suites":[{"suite":"runner-unit","started_at":"2026-10-31T12:00:00Z","seconds":10,"exit_code":0,"tests":{"counts":{"pass":2}}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := Main([]string{"metrics", "--import-gates", path}, &out, &stderr); code != 0 || requests != 1 {
		t.Fatalf("code=%d requests=%d error=%s", code, requests, stderr.String())
	}
}

func TestSkillRecordsUseExistingOwnedMetricsScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/main/workspace/metrics" || r.URL.Query().Get("session") != "worker" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("skill read widened scope")
		}
		_, _ = w.Write([]byte(`{"workers":[{"session_id":"worker","skills":{"loads":[],"reporting":[],"evidence":[],"truncated":false}}]}`))
	}))
	defer server.Close()
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", server.URL)
	t.Setenv("BLERG_RUNNER_SESSION_ID", "main")
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-token")
	t.Setenv("BLERG_RUNNER_DAEMON_STATE_DIR", t.TempDir())
	var out, stderr bytes.Buffer
	if code := Main([]string{"skills", "--session", "worker"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), `"worker"`) {
		t.Fatalf("skills: %d %s", code, stderr.String())
	}
}
