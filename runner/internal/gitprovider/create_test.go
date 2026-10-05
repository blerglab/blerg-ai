package gitprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type createCall struct {
	method, path, auth string
	body               map[string]any
}

// createServer fakes both APIs: /user is "me" (GitHub login, GitLab username),
// the group "grp" exists with id 42, "taken" already exists under every owner.
func createServer(t *testing.T) (*httptest.Server, func() []createCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []createCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := createCall{method: r.Method, path: r.URL.EscapedPath(), auth: r.Header.Get("Authorization")}
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &c.body)
		}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		name, _ := c.body["name"].(string)
		switch {
		case c.path == "/user" || c.path == "/api/v4/user":
			_, _ = w.Write([]byte(`{"login":"Me","username":"Me"}`))
		case c.path == "/api/v4/namespaces/grp":
			_, _ = w.Write([]byte(`{"id":42,"full_path":"grp"}`))
		case c.path == "/api/v4/namespaces/nogroup":
			http.NotFound(w, r)
		case c.path == "/user/repos" || strings.HasPrefix(c.path, "/orgs/"):
			if strings.HasPrefix(c.path, "/orgs/forbidden/") {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token SECRET-ish body"}`))
				return
			}
			if name == "taken" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Repository creation failed.","errors":[{"resource":"Repository","code":"custom","field":"name","message":"name already exists on this account"}]}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"full_name":"x/y"}`))
		case c.path == "/api/v4/projects":
			if name == "taken" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":{"path":["has already been taken"]}}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":7}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []createCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]createCall(nil), calls...)
	}
}

func TestGitHubCreateRepoUserAndOrg(t *testing.T) {
	srv, calls := createServer(t)
	gh := NewGitHubAt("gh", "gh.example", srv.URL, srv.Client())
	ctx := context.Background()

	if err := gh.CreateRepo(ctx, "tok-1", "me", "proj", true); err != nil {
		t.Fatalf("user repo: %v", err)
	}
	got := calls()
	if len(got) != 2 || got[0].path != "/user" || got[1].path != "/user/repos" || got[1].method != http.MethodPost {
		t.Fatalf("calls = %+v", got)
	}
	if b := got[1].body; b["name"] != "proj" || b["private"] != true || b["auto_init"] != true {
		t.Fatalf("body = %v", b)
	}
	if got[1].auth != "Bearer tok-1" {
		t.Fatalf("auth = %q", got[1].auth)
	}

	if err := gh.CreateRepo(ctx, "tok-1", "acme", "proj", false); err != nil {
		t.Fatalf("org repo: %v", err)
	}
	got = calls()
	last := got[len(got)-1]
	if last.path != "/orgs/acme/repos" || last.body["private"] != false {
		t.Fatalf("org call = %+v", last)
	}
}

func TestGitHubCreateRepoErrors(t *testing.T) {
	srv, _ := createServer(t)
	gh := NewGitHubAt("gh", "gh.example", srv.URL, srv.Client())
	ctx := context.Background()

	if err := gh.CreateRepo(ctx, "tok", "me", "taken", true); !errors.Is(err, ErrExists) {
		t.Fatalf("taken name: err = %v, want ErrExists", err)
	}
	err := gh.CreateRepo(ctx, "tok", "forbidden", "proj", true)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Fatalf("forbidden org: err = %v, want StatusError 403", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("the provider's body leaked into the error: %v", err)
	}
	if err := gh.CreateRepo(ctx, "tok", "-bad", "proj", true); err == nil {
		t.Fatal("an invalid owner reached the network")
	}
}

func TestGitLabCreateRepoUserAndGroup(t *testing.T) {
	srv, calls := createServer(t)
	gl := NewGitLabAt("gl", "gl.example", srv.URL, srv.Client())
	ctx := context.Background()

	if err := gl.CreateRepo(ctx, "tok-2", "me", "tool", true); err != nil {
		t.Fatalf("user project: %v", err)
	}
	got := calls()
	if len(got) != 2 || got[0].path != "/api/v4/user" || got[1].path != "/api/v4/projects" {
		t.Fatalf("calls = %+v", got)
	}
	if b := got[1].body; b["name"] != "tool" || b["path"] != "tool" || b["visibility"] != "private" || b["initialize_with_readme"] != true || b["namespace_id"] != nil {
		t.Fatalf("body = %v", b)
	}

	if err := gl.CreateRepo(ctx, "tok-2", "grp", "tool", false); err != nil {
		t.Fatalf("group project: %v", err)
	}
	got = calls()
	if got[len(got)-2].path != "/api/v4/namespaces/grp" {
		t.Fatalf("no namespace lookup: %+v", got)
	}
	if b := got[len(got)-1].body; b["namespace_id"] != float64(42) || b["visibility"] != "public" {
		t.Fatalf("group body = %v", b)
	}

	if err := gl.CreateRepo(ctx, "tok-2", "me", "taken", true); !errors.Is(err, ErrExists) {
		t.Fatalf("taken path: err = %v, want ErrExists", err)
	}
	err := gl.CreateRepo(ctx, "tok-2", "nogroup", "tool", true)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusNotFound {
		t.Fatalf("missing group: err = %v, want StatusError 404", err)
	}
	// A name GitHub accepts but GitLab refuses (".atom" suffix) never reaches the network.
	before := len(calls())
	if err := gl.CreateRepo(ctx, "tok-2", "me", "feed.atom", true); err == nil || len(calls()) != before {
		t.Fatalf("invalid GitLab name: err = %v, calls %d → %d", err, before, len(calls()))
	}
}
