package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
)

type cardGitReader struct {
	runner.Driver
	calls int
	patch bool
}

func (f *cardGitReader) ReadGit(_ context.Context, sid string, q url.Values, patch bool) ([]byte, int, error) {
	f.calls++
	f.patch = patch
	if patch {
		return []byte("diff --git a/a b/a\n+local\n"), 200, nil
	}
	return []byte(`{"branch":"work/card","commits":[{"sha":"abcdef1234567890","message":"Local work"}]}`), 200, nil
}
func TestCardGitReadsOnlyLinkedWorkspace(t *testing.T) {
	_, id, do, _ := artifactCard(t, "https://runner.example.com/sessions")
	reader := &cardGitReader{}
	srvAPI.SetRunner(api.RunnerConfig{Driver: reader, UIBase: "https://runner.example.com/sessions"})
	response := do("GET", "/api/cards/"+id+"/git", nil)
	if response.StatusCode != 409 || reader.calls != 0 {
		t.Fatalf("no workspace: %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = do("PATCH", "/api/cards/"+id, map[string]any{"add_links": []db.Link{{Kind: "session", URL: "https://runner.example.com/sessions/" + artSession}}})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("link: %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = do("GET", "/api/cards/"+id+"/git", nil)
	var info map[string]any
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 || info["branch"] != "work/card" || reader.calls != 1 {
		t.Fatalf("local read: %d %v", response.StatusCode, info)
	}
	response = do("GET", "/api/cards/"+id+"/diff", nil)
	_ = response.Body.Close()
	if response.StatusCode != 200 || !reader.patch {
		t.Fatalf("local diff: %d", response.StatusCode)
	}

	if response.Header.Get("Content-Type") != "text/plain; charset=utf-8" || response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("diff must remain inert text: %v", response.Header)
	}
	before := reader.calls
	response = do("GET", "/api/cards/"+id+"/git?session_id=00000000-0000-4000-8000-000000000001", nil)
	_ = response.Body.Close()
	if response.StatusCode != 404 || reader.calls != before {
		t.Fatalf("unlinked read: %d", response.StatusCode)
	}
}
