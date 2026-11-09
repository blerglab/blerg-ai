package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryCommandRequiresInput(t *testing.T) {
	var out bytes.Buffer
	if err := run(nil, &out); err == nil {
		t.Fatal("missing history accepted")
	}
}

func TestHistoryCommandImportsWithSessionAuthority(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/sessions/example-session/test-runs/import" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("wrong session authority")
		}
		raw, _ := io.ReadAll(r.Body)
		if bytes.Contains(raw, []byte(`"counts":{"pass":0`)) {
			t.Error("missing test counts fabricated as zero")
		}
		if bytes.Contains(raw, []byte("private-canary")) {
			t.Error("host metadata forwarded")
		}
		w.WriteHeader(201)
	}))
	defer server.Close()
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", server.URL)
	t.Setenv("BLERG_RUNNER_SESSION_ID", "example-session")
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-token")
	file := filepath.Join(t.TempDir(), "history.jsonl")
	body := `{"id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","host":"private-canary","seconds":0,"complete":true,"finished_at":"` + time.Now().UTC().Format(time.RFC3339) + `","suites":[{"suite":"unit","seconds":10,"outcome":"pass"}]}`
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"-history", file, "-member", "repository"}, &out); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !strings.Contains(out.String(), "Imported 1") {
		t.Fatalf("requests=%d output=%s", requests, &out)
	}
}

func TestHistoryCommandCountsSupersededAndNotRetained(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Contains(raw, []byte(`"commit":null`)) {
			t.Error("commitless report is not null")
		}
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			_, _ = io.WriteString(w, `{"ok":true,"superseded":true,"status":"retained"}`)
		} else {
			_, _ = io.WriteString(w, `{"ok":true,"status":"not_retained"}`)
		}
	}))
	defer server.Close()
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", server.URL)
	t.Setenv("BLERG_RUNNER_SESSION_ID", "example-session")
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-token")
	file := filepath.Join(t.TempDir(), "history.jsonl")
	row := `{"id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","commit":null,"seconds":0,"complete":true,"finished_at":"` + time.Now().UTC().Format(time.RFC3339) + `","suites":[]}`
	if err := os.WriteFile(file, []byte(row+"\n"+row), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"-history", file, "-member", "repository"}, &out); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || !strings.Contains(out.String(), "superseded 1; not retained 1") {
		t.Fatalf("requests=%d output=%s", requests, &out)
	}
}

func TestReportDiagnosticsAreActionableWithoutPrivateContent(t *testing.T) {
	if got := publicReportMessage([]byte(`{"error":"Test history is unavailable."}`)); got != "Test history is unavailable." {
		t.Fatal(got)
	}
	if got := publicReportMessage([]byte(`{"error":"private-canary"}`)); strings.Contains(got, "private-canary") {
		t.Fatal("untrusted body leaked")
	}
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", "http://example.invalid")
	t.Setenv("BLERG_RUNNER_SESSION_ID", "example-session")
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-token")
	err := run([]string{"-history", filepath.Join(t.TempDir(), "private-canary"), "-member", "repository"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not_found") || strings.Contains(err.Error(), "private-canary") {
		t.Fatalf("unsafe or unhelpful file diagnostic: %v", err)
	}
}

func TestHistoryCommandProjectBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".blerg"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".blerg", "test-budgets.json"), []byte(`{"version":1,"suites":{"runner-go":{"layer":"3b","budget_seconds":120}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Contains(raw, []byte(`"budget_seconds":120`)) || !bytes.Contains(raw, []byte(`"layer":"3b"`)) {
			t.Errorf("real gate suite budget lost: %s", raw)
		}
		_, _ = io.WriteString(w, `{"ok":true,"status":"retained"}`)
	}))
	defer srv.Close()
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", srv.URL)
	t.Setenv("BLERG_RUNNER_SESSION_ID", "example-session")
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-token")
	path := filepath.Join(root, "history.jsonl")
	raw := `{"id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","seconds":1,"complete":true,"finished_at":"` + time.Now().UTC().Format(time.RFC3339) + `","suites":[{"suite":"runner-go","seconds":1,"outcome":"pass"}]}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-history", path, "-member", "repository", "-root", root}, io.Discard); err != nil {
		t.Fatal(err)
	}
}
