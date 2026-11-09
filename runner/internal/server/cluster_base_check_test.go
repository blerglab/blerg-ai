package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gitAdvertisement builds a pkt-line framed "info/refs?service=git-upload-pack" body
// advertising the given ref -> sha pairs, matching what checkBaseBranchAdvertisement parses.
func gitAdvertisement(refs map[string]string) string {
	var b strings.Builder
	service := "# service=git-upload-pack\n"
	b.WriteString(pktLine(service))
	b.WriteString("0000")
	first := true
	for ref, sha := range refs {
		line := sha + " " + ref
		if first {
			line += "\x00no-done\n"
			first = false
		} else {
			line += "\n"
		}
		b.WriteString(pktLine(line))
	}
	b.WriteString("0000")
	return b.String()
}

func pktLine(s string) string {
	n := len(s) + 4
	return hex4(n) + s
}

func hex4(n int) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[(n>>12)&0xf], hex[(n>>8)&0xf], hex[(n>>4)&0xf], hex[n&0xf]})
}

func TestParseGitRefAdvertisement(t *testing.T) {
	sha := strings.Repeat("a", 40)
	body := gitAdvertisement(map[string]string{"refs/heads/alpha/0.6.0": sha})
	refs, err := parseGitRefAdvertisement([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if refs["refs/heads/alpha/0.6.0"] != sha {
		t.Fatalf("got %v", refs)
	}
}

func TestParseGitRefAdvertisementMalformed(t *testing.T) {
	for _, tc := range []string{"zzzz", "0100ab", "-001ab"} {
		if _, err := parseGitRefAdvertisement([]byte(tc)); err == nil {
			t.Fatalf("%q: expected a refusal, got none", tc)
		}
	}
}

// Row 3 (base branch not yet pushed): the branch is simply absent from the advertisement.
func TestCheckBaseBranchAdvertisementNotPushed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/example-org/app.git/info/refs" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte(gitAdvertisement(map[string]string{"refs/heads/main": strings.Repeat("b", 40)})))
	}))
	defer srv.Close()
	remoteSHA, cred, err := checkBaseBranchAdvertisement(t.Context(), srv.Client(), srv.URL+"/example-org/app.git", "alpha/0.6.0", "")
	if err != nil || remoteSHA != "" || cred != credentialOK {
		t.Fatalf("remoteSHA=%q cred=%v err=%v", remoteSHA, cred, err)
	}
}

// The base branch IS advertised: its remote SHA comes back, nothing to refuse here (the
// caller compares it against Main's own local SHA).
func TestCheckBaseBranchAdvertisementPushed(t *testing.T) {
	sha := strings.Repeat("c", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte(gitAdvertisement(map[string]string{"refs/heads/alpha/0.6.0": sha})))
	}))
	defer srv.Close()
	remoteSHA, cred, err := checkBaseBranchAdvertisement(t.Context(), srv.Client(), srv.URL+"/example-org/app.git", "alpha/0.6.0", "")
	if err != nil || remoteSHA != sha || cred != credentialOK {
		t.Fatalf("remoteSHA=%q cred=%v err=%v", remoteSHA, cred, err)
	}
}

// Row 4 (missing owner token): a private repository refuses with no token offered — nothing
// to even ask the provider with.
func TestCheckBaseBranchAdvertisementAuthRequiredWithoutToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	remoteSHA, cred, err := checkBaseBranchAdvertisement(t.Context(), srv.Client(), srv.URL+"/example-org/app.git", "alpha/0.6.0", "")
	if err != nil || remoteSHA != "" || cred != credentialMissing {
		t.Fatalf("remoteSHA=%q cred=%v err=%v", remoteSHA, cred, err)
	}
}

// Row 5 (token rejected): a stored token exists but the provider refuses it anyway — a
// different shape from "missing", never folded into "not pushed" either.
func TestCheckBaseBranchAdvertisementTokenRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	remoteSHA, cred, err := checkBaseBranchAdvertisement(t.Context(), srv.Client(), srv.URL+"/example-org/app.git", "alpha/0.6.0", "fixture-token")
	if err != nil || remoteSHA != "" || cred != credentialRejected {
		t.Fatalf("remoteSHA=%q cred=%v err=%v", remoteSHA, cred, err)
	}
}

func TestCheckBaseBranchAdvertisementTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, _, err := checkBaseBranchAdvertisement(t.Context(), srv.Client(), srv.URL+"/example-org/app.git", "alpha/0.6.0", "")
	if !errors.Is(err, errBaseBranchUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestGithubOwnerRepo(t *testing.T) {
	owner, repo, err := githubOwnerRepo("https://github.com/example-org/app.git")
	if err != nil || owner != "example-org" || repo != "app" {
		t.Fatalf("owner=%q repo=%q err=%v", owner, repo, err)
	}
	for _, bad := range []string{"https://gitlab.com/example-org/app.git", "https://github.com/example-org", "https://github.com/example-org/app/extra.git"} {
		if _, _, err := githubOwnerRepo(bad); err == nil {
			t.Fatalf("%q: expected a refusal", bad)
		}
	}
}

func TestCheckGitHubCompareIdenticalOrBehindIsPushed(t *testing.T) {
	for _, status := range []string{"identical", "behind"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/repos/example-org/app/compare/alpha/0.6.0...bbbb" {
				t.Errorf("unexpected path %s", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"status":"` + status + `"}`))
		}))
		pushed, ahead, unknown, err := checkGitHubCompare(t.Context(), srv.Client(), srv.URL+"/repos/example-org/app/compare/alpha%2F0.6.0...bbbb", "")
		srv.Close()
		if err != nil || !pushed || ahead != 0 || unknown {
			t.Fatalf("status=%s pushed=%v ahead=%d unknown=%v err=%v", status, pushed, ahead, unknown, err)
		}
	}
}

func TestCheckGitHubCompareAheadNamesTheGap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ahead","ahead_by":3}`))
	}))
	defer srv.Close()
	pushed, ahead, unknown, err := checkGitHubCompare(t.Context(), srv.Client(), srv.URL, "fixture-token")
	if err != nil || pushed || ahead != 3 || unknown {
		t.Fatalf("pushed=%v ahead=%d unknown=%v err=%v", pushed, ahead, unknown, err)
	}
}

func TestCheckGitHubCompareDivergedIsUnknownNotGuessed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"diverged","ahead_by":5,"behind_by":2}`))
	}))
	defer srv.Close()
	pushed, _, unknown, err := checkGitHubCompare(t.Context(), srv.Client(), srv.URL, "")
	if err != nil || pushed || !unknown {
		t.Fatalf("pushed=%v unknown=%v err=%v", pushed, unknown, err)
	}
}

// Credentials were already proven to work before this is ever called (checkBaseBranchRemote
// runs first); a 404 here means the provider simply doesn't know this sha — never pushed.
func TestCheckGitHubCompareNotFoundIsNotPushedUnknownCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	pushed, ahead, unknown, err := checkGitHubCompare(t.Context(), srv.Client(), srv.URL, "fixture-token")
	if err != nil || pushed || ahead != 0 || !unknown {
		t.Fatalf("pushed=%v ahead=%d unknown=%v err=%v", pushed, ahead, unknown, err)
	}
}

func TestCheckGitHubCompareTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, _, _, err := checkGitHubCompare(t.Context(), srv.Client(), srv.URL, "")
	if !errors.Is(err, errCompareUnavailable) {
		t.Fatalf("got %v", err)
	}
}
