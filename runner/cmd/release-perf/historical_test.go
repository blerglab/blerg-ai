package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoricalRunnerServesItsOwnBuildWithoutChangingCheckout(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "runner", "frontend", "dist")
	historical := filepath.Join(t.TempDir(), "dist")
	logs := t.TempDir()
	for _, d := range []string{current, historical} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(current, "index.html"), []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(historical, "index.html"), []byte("historical"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := runnerWorkingDir(root, logs, historical, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "frontend", "dist", "index.html"))
	if err != nil || string(got) != "historical" {
		t.Fatalf("served %q; %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(current, "index.html"))
	if err != nil || string(got) != "current" {
		t.Fatal("changed checkout")
	}
}

func TestRunnerEntryRejectsWrongServedVersion(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "index.html"), []byte("historical"), 0o600); err != nil {
		t.Fatal(err)
	}
	content := "wrong build"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(content)) }))
	defer server.Close()
	if verifyRunnerEntry(context.Background(), server.URL, d) == nil {
		t.Fatal("accepted wrong served build")
	}
	content = "historical"
	if err := verifyRunnerEntry(context.Background(), server.URL, d); err != nil {
		t.Fatal(err)
	}
}
