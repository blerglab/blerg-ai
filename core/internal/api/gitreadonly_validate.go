package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	defaultGitHubAPIBase = "https://api.github.com"
	defaultGitLabAPIBase = "https://gitlab.com/api/v4"
)

// errClassicGitHubToken's text is deliberately short — the section's own description already
// spells out the fine-grained-token guidance in full (BLG-460 security re-review round 2
// finding 1: don't repeat that whole paragraph back in the per-save error). User-facing copy,
// not a typical internal Go error string, hence the capital and the period.
//
//nolint:staticcheck,revive // user-facing error text; see comment above
var errClassicGitHubToken = errors.New("Classic GitHub tokens can't be limited to read. Use a fine-grained token with Contents: read-only.")

// validateGitReadOnlyToken is BLG-460 security re-review finding 2: read-only is trust-on-entry
// (the vault only ever stores an opaque string), so this checks what CAN be checked at save
// time, with one request to the provider's own API host and a short timeout, and refuses what's
// plainly writable rather than storing any pasted token unchecked:
//
//   - GitHub classic tokens (a "ghp_" prefix, or any token whose GET /user response carries an
//     X-OAuth-Scopes header — the header classic tokens always send and fine-grained tokens
//     never do) are refused outright. A classic token can't be limited to read, however it was
//     created, so there's nothing to check further.
//   - GitHub fine-grained tokens ("github_pat_") are accepted once GET /user confirms the token
//     authenticates (a 401 is refused). GitHub's API has no endpoint that reports a fine-grained
//     token's own permissions, so this can't verify more than that it works — the person must
//     set Contents: read-only themselves when creating it.
//   - GitLab tokens are checked against GET /personal_access_tokens/self and accepted only when
//     every scope on the token is read_repository or read_api; anything else (write_repository,
//     api, sudo, ...) is refused, naming the offending scopes.
//
// Never logs the token itself — callers log only the provider and the refusal/failure kind.
func (d Deps) validateGitReadOnlyToken(ctx context.Context, provider, token string) error {
	client := d.GitProviderClient
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	switch provider {
	case "github":
		return validateGitHubReadOnlyToken(ctx, client, d.gitHubAPIBase(), token)
	case "gitlab":
		return validateGitLabReadOnlyToken(ctx, client, d.gitLabAPIBase(), token)
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}
}

func (d Deps) gitHubAPIBase() string {
	if d.GitHubAPIBase != "" {
		return d.GitHubAPIBase
	}
	return defaultGitHubAPIBase
}

func (d Deps) gitLabAPIBase() string {
	if d.GitLabAPIBase != "" {
		return d.GitLabAPIBase
	}
	return defaultGitLabAPIBase
}

func validateGitHubReadOnlyToken(ctx context.Context, client *http.Client, base, token string) error {
	if strings.HasPrefix(token, "ghp_") {
		return errClassicGitHubToken
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return fmt.Errorf("building GitHub validation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("could not reach GitHub to check this token; please try again")
	}
	defer func() { _ = resp.Body.Close() }()
	// A classic token (of any prefix, including none at all for an old 40-hex-character token)
	// always carries this header on GET /user; a fine-grained token never does. Checked before
	// the status code, since a classic token can still authenticate successfully.
	if resp.Header.Get("X-OAuth-Scopes") != "" {
		return errClassicGitHubToken
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("GitHub rejected this token")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned an unexpected status (%d) while checking this token", resp.StatusCode)
	}
	return nil
}

type gitlabTokenSelf struct {
	Scopes []string `json:"scopes"`
}

var gitlabReadOnlyScopes = map[string]bool{"read_repository": true, "read_api": true}

func validateGitLabReadOnlyToken(ctx context.Context, client *http.Client, base, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/personal_access_tokens/self", nil)
	if err != nil {
		return fmt.Errorf("building GitLab validation request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("could not reach GitLab to check this token; please try again")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("GitLab rejected this token")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitLab returned an unexpected status (%d) while checking this token", resp.StatusCode)
	}
	var self gitlabTokenSelf
	if err := json.NewDecoder(resp.Body).Decode(&self); err != nil {
		return errors.New("could not parse GitLab's response while checking this token")
	}
	if len(self.Scopes) == 0 {
		return errors.New("this token has no scopes")
	}
	var bad []string
	for _, s := range self.Scopes {
		if !gitlabReadOnlyScopes[s] {
			bad = append(bad, s)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("this token has scopes beyond read-only (%s); use a token scoped to read_repository or read_api", strings.Join(bad, ", "))
	}
	return nil
}
