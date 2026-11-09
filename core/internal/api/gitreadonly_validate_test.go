package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNormalizeGitReadOnlyOwner covers BLG-460 security re-review finding 3's owner rules:
// lowercasing, ".."/trailing-dot refusal, GitHub's 39-character single-segment cap, and
// GitLab's "/"-separated group/subgroup paths up to 255 characters.
func TestNormalizeGitReadOnlyOwner(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		owner    string
		want     string
		wantErr  bool
	}{
		{"github lowercases", "github", "Acme-Corp", "acme-corp", false},
		{"gitlab lowercases", "gitlab", "Acme/Sub-Group", "acme/sub-group", false},
		{"github rejects double dot", "github", "ac..me", "", true},
		{"gitlab rejects double dot across segments", "gitlab", "acme/../etc", "", true},
		{"github rejects trailing dot", "github", "acme.", "", true},
		{"gitlab rejects trailing dot", "gitlab", "acme/sub.", "", true},
		{"github rejects empty", "github", "", "", true},
		{"github accepts 39 chars", "github", strings.Repeat("a", 39), strings.Repeat("a", 39), false},
		{"github rejects 40 chars", "github", strings.Repeat("a", 40), "", true},
		{"gitlab accepts a path up to 255 chars", "gitlab", strings.Repeat("a", 255), strings.Repeat("a", 255), false},
		{"gitlab rejects 256 chars", "gitlab", strings.Repeat("a", 256), "", true},
		{"gitlab rejects an empty path segment", "gitlab", "acme//sub", "", true},
		{"github rejects a slash (not a path provider)", "github", "acme/sub", "", true},
		{"unknown provider refused", "mercurial-hosting", "acme", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeGitReadOnlyOwner(tc.provider, tc.owner)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeGitReadOnlyOwner(%q, %q) = %q, nil; want an error", tc.provider, tc.owner, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeGitReadOnlyOwner(%q, %q) unexpected error: %v", tc.provider, tc.owner, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeGitReadOnlyOwner(%q, %q) = %q, want %q", tc.provider, tc.owner, got, tc.want)
			}
		})
	}
}

// TestCanonicalGitReadOnlyKind covers the composite "git-readonly:<provider>:<owner>" parse +
// normalize, including the matched=false "not a git-readonly kind at all" case that lets
// handleStoreCredential/handleDeleteCredential fall through to their other engine checks.
func TestCanonicalGitReadOnlyKind(t *testing.T) {
	if canonical, matched, err := canonicalGitReadOnlyKind("claude"); matched || err != nil || canonical != "" {
		t.Fatalf("non-git-readonly kind: got canonical=%q err=%v matched=%v, want matched=false", canonical, err, matched)
	}
	canonical, matched, err := canonicalGitReadOnlyKind("git-readonly:github:Acme-Corp")
	if !matched || err != nil {
		t.Fatalf("git-readonly:github:Acme-Corp: got canonical=%q err=%v matched=%v", canonical, err, matched)
	}
	if canonical != "git-readonly:github:acme-corp" {
		t.Fatalf("expected the owner to be lowercased in the canonical kind, got %q", canonical)
	}
	if _, matched, err := canonicalGitReadOnlyKind("git-readonly:github:acme.."); !matched || err == nil {
		t.Fatalf("invalid owner: got err=%v matched=%v, want matched=true with a non-nil error", err, matched)
	}
}

func githubFake(t *testing.T, status int, oauthScopesHeader string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if oauthScopesHeader != "" {
			w.Header().Set("X-OAuth-Scopes", oauthScopesHeader)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestValidateGitHubReadOnlyToken covers finding 2's GitHub branch: a "ghp_" prefix is refused
// outright (no network call is even required for that case, but the fake still answers 200 to
// prove the prefix check alone is driving the refusal); an X-OAuth-Scopes response header is
// refused even without that prefix; a 401 is refused; and a clean fine-grained-shaped response
// is accepted.
func TestValidateGitHubReadOnlyToken(t *testing.T) {
	t.Run("ghp_ prefix refused even if the server would accept it", func(t *testing.T) {
		srv := githubFake(t, http.StatusOK, "")
		err := validateGitHubReadOnlyToken(context.Background(), srv.Client(), srv.URL, "ghp_classic")
		if !errors.Is(err, errClassicGitHubToken) {
			t.Fatalf("got %v, want errClassicGitHubToken", err)
		}
	})
	t.Run("X-OAuth-Scopes header refused regardless of prefix", func(t *testing.T) {
		srv := githubFake(t, http.StatusOK, "repo,user")
		err := validateGitHubReadOnlyToken(context.Background(), srv.Client(), srv.URL, "some_legacy_hex_token")
		if !errors.Is(err, errClassicGitHubToken) {
			t.Fatalf("got %v, want errClassicGitHubToken", err)
		}
	})
	t.Run("401 refused", func(t *testing.T) {
		srv := githubFake(t, http.StatusUnauthorized, "")
		err := validateGitHubReadOnlyToken(context.Background(), srv.Client(), srv.URL, "github_pat_bad")
		if err == nil {
			t.Fatal("expected a refusal for a 401")
		}
	})
	t.Run("fine-grained token accepted", func(t *testing.T) {
		srv := githubFake(t, http.StatusOK, "")
		if err := validateGitHubReadOnlyToken(context.Background(), srv.Client(), srv.URL, "github_pat_good"); err != nil {
			t.Fatalf("expected acceptance, got %v", err)
		}
	})
	t.Run("unreachable provider refused, not stored unchecked", func(t *testing.T) {
		deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		deadURL := deadSrv.URL
		deadSrv.Close() // closed before use: connections to it now fail, simulating "unreachable".
		err := validateGitHubReadOnlyToken(context.Background(), http.DefaultClient, deadURL, "github_pat_good")
		if err == nil {
			t.Fatal("expected a refusal when the provider can't be reached")
		}
	})
}

func gitlabFake(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestValidateGitLabReadOnlyToken covers finding 2's GitLab branch: scopes within
// {read_repository, read_api} are accepted, anything else is refused by name, a 401 is refused,
// and a response GitLab never actually sends (no scopes at all) is refused rather than treated
// as "nothing to complain about".
func TestValidateGitLabReadOnlyToken(t *testing.T) {
	t.Run("read-only scopes accepted", func(t *testing.T) {
		srv := gitlabFake(t, http.StatusOK, `{"scopes":["read_repository"]}`)
		if err := validateGitLabReadOnlyToken(context.Background(), srv.Client(), srv.URL, "glpat-good"); err != nil {
			t.Fatalf("expected acceptance, got %v", err)
		}
	})
	t.Run("read_api alone accepted", func(t *testing.T) {
		srv := gitlabFake(t, http.StatusOK, `{"scopes":["read_api"]}`)
		if err := validateGitLabReadOnlyToken(context.Background(), srv.Client(), srv.URL, "glpat-good"); err != nil {
			t.Fatalf("expected acceptance, got %v", err)
		}
	})
	t.Run("write_repository refused and named", func(t *testing.T) {
		srv := gitlabFake(t, http.StatusOK, `{"scopes":["read_repository","write_repository"]}`)
		err := validateGitLabReadOnlyToken(context.Background(), srv.Client(), srv.URL, "glpat-bad")
		if err == nil || !strings.Contains(err.Error(), "write_repository") {
			t.Fatalf("got %v, want a refusal naming write_repository", err)
		}
	})
	t.Run("full api scope refused", func(t *testing.T) {
		srv := gitlabFake(t, http.StatusOK, `{"scopes":["api"]}`)
		err := validateGitLabReadOnlyToken(context.Background(), srv.Client(), srv.URL, "glpat-bad")
		if err == nil || !strings.Contains(err.Error(), "api") {
			t.Fatalf("got %v, want a refusal naming api", err)
		}
	})
	t.Run("no scopes refused", func(t *testing.T) {
		srv := gitlabFake(t, http.StatusOK, `{"scopes":[]}`)
		if err := validateGitLabReadOnlyToken(context.Background(), srv.Client(), srv.URL, "glpat-empty"); err == nil {
			t.Fatal("expected a refusal for a token with no scopes")
		}
	})
	t.Run("401 refused", func(t *testing.T) {
		srv := gitlabFake(t, http.StatusUnauthorized, "")
		if err := validateGitLabReadOnlyToken(context.Background(), srv.Client(), srv.URL, "glpat-bad"); err == nil {
			t.Fatal("expected a refusal for a 401")
		}
	})
}
