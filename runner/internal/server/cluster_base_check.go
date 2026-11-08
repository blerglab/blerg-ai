package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
)

// errBaseBranchUnavailable is checkBaseBranchPushed's own transport failure — distinct from
// errRepositoryCheckUnavailable only so a base-check refusal never gets mistaken for an
// ordinary repository-readability refusal when read back from a log line.
var errBaseBranchUnavailable = errors.New("the base branch could not be checked against the provider right now; retry when the git service is available")

// credentialRefusal is returned by checkBaseBranchRemote (and surfaced by its callers) when
// the provider's response can be told apart from "branch not pushed": a token was offered
// and rejected, or none was offered and one is plainly required. Distinguishing these two
// from the generic "not pushed" case is what Codex's review of 1524a5d8 asked for (row 4/5
// vs row 3 must never collapse into the same message).
type credentialRefusal int

const (
	credentialOK       credentialRefusal = iota
	credentialMissing                    // no token offered, provider demands one (row 4)
	credentialRejected                   // a token was offered, provider refused it anyway
)

// checkBaseBranchRemote is BLG-501's provider-side half of design §3.3's base-push check
// (docs/design/cluster-workers-from-desktop.md §9, revised after Codex's review of
// 1524a5d8): it reads the provider's own current SHA for branch from the same smart-HTTP ref
// advertisement checkRepositoryRead already reads the head of — parsed fully here instead of
// sniffed. remoteSHA is empty exactly when the provider never advertised that branch at all
// (never pushed, ever). The caller (hostClusterChildWorkspace) is the one that compares this
// against Main's own local SHA (resolved separately, via the daemon) — this function never
// sees or needs Main's own history.
func (j *JobManager) checkBaseBranchRemote(ctx context.Context, cloneURL, branch, token string) (remoteSHA string, cred credentialRefusal, err error) {
	if j.baseBranchRemote != nil {
		return j.baseBranchRemote(ctx, cloneURL, branch, token)
	}
	endpointHost := ""
	if u, perr := url.Parse(cloneURL); perr == nil {
		endpointHost = u.Hostname()
	}
	policy := netguard.Policy{}
	if base, berr := url.Parse(j.ExplicitGitURLBase); berr == nil && base.Hostname() != "" && base.Hostname() == endpointHost {
		policy.AllowPrivateHosts = []string{base.Hostname()}
		if base.Scheme == "http" {
			policy.AllowHTTPHosts = []string{base.Hostname()}
		}
	}
	client := policy.Client(10*time.Second, 1<<20)
	defer client.CloseIdleConnections()
	return checkBaseBranchAdvertisement(ctx, client, cloneURL, branch, token)
}

// checkBaseBranchAdvertisement is checkBaseBranchRemote's transport-injectable core, split
// out so a test can supply its own *http.Client (an httptest fixture) without going through
// netguard's host policy, exactly as checkRepositoryRead already does.
func checkBaseBranchAdvertisement(ctx context.Context, client *http.Client, cloneURL, branch, token string) (remoteSHA string, cred credentialRefusal, err error) {
	endpoint, perr := url.Parse(cloneURL)
	if perr != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
		return "", credentialOK, errRepositoryAddress
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/info/refs"
	endpoint.RawPath = ""
	endpoint.RawQuery = "service=git-upload-pack"
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil) // #nosec G704 -- same shape as checkRepositoryRead; the caller's client enforces the netguard allowlist in production.
	if rerr != nil {
		return "", credentialOK, errRepositoryAddress
	}
	req.Header.Set("Accept", "application/x-git-upload-pack-advertisement")
	req.Header.Set("User-Agent", "blerg-runner")
	if token != "" && endpoint.Scheme == "https" {
		req.SetBasicAuth(gitprovider.Default.TokenUserForHost(endpoint.Hostname()), token)
	}
	resp, derr := client.Do(req) // #nosec G704 -- see above.
	if derr != nil {
		if errors.Is(derr, netguard.ErrURLNotPermitted) || errors.Is(derr, netguard.ErrBlockedAddress) {
			return "", credentialOK, errRepositoryAddress
		}
		return "", credentialOK, errBaseBranchUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		// Without a token, this is row 4's shape: nothing to even ask the provider with.
		// With one, the token itself was rejected (row 5's shape, expired or otherwise
		// invalid) — never folded into "not pushed" either way.
		if token == "" {
			return "", credentialMissing, nil
		}
		return "", credentialRejected, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", credentialOK, errBaseBranchUnavailable
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if rerr != nil {
		return "", credentialOK, errBaseBranchUnavailable
	}
	refs, perr2 := parseGitRefAdvertisement(body)
	if perr2 != nil {
		return "", credentialOK, errBaseBranchUnavailable
	}
	return refs["refs/heads/"+branch], credentialOK, nil
}

// errCompareUnavailable is checkSHAPushed's own transport failure.
var errCompareUnavailable = errors.New("the base branch's history could not be checked against the provider right now; retry when the git service is available")

// checkSHAPushed is design §10's history half: once checkBaseBranchRemote has already
// proven the branch exists and the token works (credentialOK, a non-empty remoteSHA), this
// asks the provider's own compare API whether sha (Main's own local commit for that branch,
// resolved by the daemon reading a ref — never an object) is reachable from the branch: at
// or behind its tip (pushed, possibly earlier than the current tip), ahead of it (not
// pushed; aheadCount names the gap), or diverged (not pushed; no reliable count). GitHub
// only, for now (design §10/§2's own GitHub-first scope) — cloneURL must be on github.com.
func (a *API) checkSHAPushed(ctx context.Context, provider, cloneURL, branch, sha, token string) (pushed bool, aheadCount int, aheadUnknown bool, err error) {
	if a.baseSHAPushed != nil {
		return a.baseSHAPushed(ctx, provider, cloneURL, branch, sha, token)
	}
	if provider != "github" {
		return false, 0, false, errCompareUnavailable
	}
	owner, repo, perr := githubOwnerRepo(cloneURL)
	if perr != nil {
		return false, 0, false, errCompareUnavailable
	}
	client := (&netguard.Policy{}).Client(10*time.Second, 1<<20)
	defer client.CloseIdleConnections()
	endpoint := "https://api.github.com/repos/" + owner + "/" + repo + "/compare/" + url.PathEscape(branch) + "..." + sha
	return checkGitHubCompare(ctx, client, endpoint, token)
}

// githubOwnerRepo splits a GitHub HTTPS clone URL ("https://github.com/<owner>/<repo>.git")
// into its owner and repo path segments.
func githubOwnerRepo(cloneURL string) (owner, repo string, err error) {
	u, perr := url.Parse(cloneURL)
	if perr != nil || u.Hostname() != "github.com" {
		return "", "", errCompareUnavailable
	}
	trimmed := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	owner, repo, ok := strings.Cut(trimmed, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", errCompareUnavailable
	}
	return owner, repo, nil
}

// checkGitHubCompare is checkSHAPushed's transport-injectable core, split out so a test can
// supply its own *http.Client (an httptest fixture) without going through netguard's host
// policy, exactly as checkRepositoryRead/checkBaseBranchAdvertisement already do.
func checkGitHubCompare(ctx context.Context, client *http.Client, endpoint, token string) (pushed bool, aheadCount int, aheadUnknown bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil) // #nosec G704 -- endpoint is built from a validated owner/repo/branch/sha; the caller's client enforces the netguard allowlist in production.
	if rerr != nil {
		return false, 0, false, errCompareUnavailable
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "blerg-runner")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, derr := client.Do(req) // #nosec G704 -- see above.
	if derr != nil {
		return false, 0, false, errCompareUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		// Credentials were already proven to work by checkBaseBranchRemote before this is
		// ever called — a 404 here means the provider does not know this sha at all, i.e.
		// it was never pushed. No count to report.
		return false, 0, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, 0, false, errCompareUnavailable
	}
	var out struct {
		Status  string `json:"status"`
		AheadBy int    `json:"ahead_by"`
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if rerr != nil {
		return false, 0, false, errCompareUnavailable
	}
	if jerr := json.Unmarshal(body, &out); jerr != nil {
		return false, 0, false, errCompareUnavailable
	}
	switch out.Status {
	case "identical", "behind":
		// sha is at or behind the branch's own tip: already pushed, possibly to an
		// earlier point than where the branch is now — that's not this check's concern.
		return true, 0, false, nil
	case "ahead":
		return false, out.AheadBy, false, nil
	default: // "diverged", or anything this client doesn't recognize.
		return false, 0, true, nil
	}
}

// parseGitRefAdvertisement reads a git smart-HTTP "info/refs?service=git-upload-pack"
// response body (pkt-line framed) into ref name -> object id. Malformed framing refuses
// rather than guesses; an advertisement with no refs returns an empty, non-nil map.
func parseGitRefAdvertisement(body []byte) (map[string]string, error) {
	refs := map[string]string{}
	i := 0
	for i+4 <= len(body) {
		n, err := strconv.ParseInt(string(body[i:i+4]), 16, 32)
		if err != nil || n < 0 || (n != 0 && n < 4) || i+int(n) > len(body) {
			return nil, errors.New("malformed pkt-line length")
		}
		if n == 0 {
			i += 4
			continue
		}
		line := body[i+4 : i+int(n)]
		i += int(n)
		line = bytes.TrimSuffix(line, []byte("\n"))
		if bytes.HasPrefix(line, []byte("# service=")) {
			continue
		}
		if idx := bytes.IndexByte(line, 0); idx >= 0 {
			line = line[:idx]
		}
		parts := bytes.SplitN(line, []byte(" "), 2)
		if len(parts) != 2 {
			continue
		}
		sha, ref := string(parts[0]), string(parts[1])
		if len(sha) == 40 || len(sha) == 64 {
			refs[ref] = sha
		}
	}
	return refs, nil
}
