package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestReadGitCarriesHumanProofAndNeverUsesGitProvider(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/sessions/session/git" || r.URL.Query().Get("branch") != "work/card" {
			t.Errorf("wrong request %s", r.URL)
		}
		if r.Header.Get("X-Blerg-Desktop-Viewer") != "human-proof" {
			t.Error("missing human proof")
		}
		_, _ = w.Write([]byte(`{"branch":"work/card","commits":[]}`))
	}))
	defer srv.Close()
	driver := NewBlergRunner(srv.URL, "automation", "")
	if _, code, err := driver.ReadGit(context.Background(), "session", nil, false); code != 403 || err == nil || calls != 0 {
		t.Fatal("automation read without a human")
	}
	body, code, err := driver.ReadGit(WithDesktopViewerProof(context.Background(), "human-proof"), "session", url.Values{"branch": {"work/card"}}, false)
	if err != nil || code != 200 || len(body) == 0 || calls != 1 {
		t.Fatalf("read %d %v", code, err)
	}
}

func TestReadGitRefusalsNeverExposeRunnerDiagnostics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(413)
		_, _ = w.Write([]byte("human-proof automation private diagnostics"))
	}))
	defer srv.Close()
	driver := NewBlergRunner(srv.URL, "automation", "")
	body, code, err := driver.ReadGit(WithDesktopViewerProof(context.Background(), "human-proof"), "session", nil, true)
	if code != 413 || len(body) != 0 || err == nil || err.Error() != "This diff is too large to open here." {
		t.Fatalf("refusal: %d %v", code, err)
	}
}

func TestReadGitExplainsMissingObjectsAndOldDaemons(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{{424, "missing"}, {409, "update"}} {
		t.Run(tc.want, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code) }))
			defer srv.Close()
			driver := NewBlergRunner(srv.URL, "automation", "")
			_, code, err := driver.ReadGit(WithDesktopViewerProof(context.Background(), "human-proof"), "session", nil, true)
			if code != tc.code || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal: %d %v", code, err)
			}
		})
	}
}
