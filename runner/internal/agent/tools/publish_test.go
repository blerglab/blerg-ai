package tools

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A symlink inside the mockup directory must not smuggle a file from outside
// the workspace into the published archive.
func TestPushMockupSkipsSymlinks(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	mock := filepath.Join(work, "mock")
	if err := os.Mkdir(mock, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mock, "index.html"), []byte("<h1>hi</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(mock, "leak.txt")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}

	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"url":"http://x/m"}`))
	}))
	defer srv.Close()

	tool := PushMockup(work, PublishConfig{Base: srv.URL, Token: "t"})
	in, _ := json.Marshal(map[string]string{"dir": "mock"})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 {
		t.Fatal("nothing uploaded")
	}
	// The archive is gzip-compressed, so decode before looking for the marker.
	if strings.Contains(string(gunzipT(t, body)), "TOP-SECRET") {
		t.Fatal("symlinked file outside the workspace was published")
	}
}

func TestReadFileMaxRejectsOversize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(p, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFileMax(p, 99); !errors.Is(err, errFileTooLarge) {
		t.Fatalf("want errFileTooLarge, got %v", err)
	}
	if data, err := readFileMax(p, 100); err != nil || len(data) != 100 {
		t.Fatalf("len=%d err=%v", len(data), err)
	}
}

func gunzipT(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
