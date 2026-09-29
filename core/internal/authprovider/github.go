package authprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/blerglab/blerg-ai/core/internal/db"
)

// GitHub is the "github" auth provider: it exchanges an OAuth code for an
// access token, resolves the authenticated GitHub user, and admits them only
// if they are currently a member of the configured org — checked live on
// every login via GitHub's org-membership API, never cached.
//
// Fail-closed is the whole point of this type: Callback must reject the
// login on ANY ambiguous signal (org-membership API unreachable, malformed
// response, non-member) rather than ever silently admitting someone we
// couldn't positively confirm is a member. This is a DIFFERENT fail
// direction from reconcile.go's periodic sweep, which must do the opposite
// (leave existing access alone on an API error) — see reconcile.go's
// doc comment for why the same "GitHub API is unreachable" condition needs
// opposite responses in the two places it's checked.
type GitHub struct {
	st                     db.Store
	clientID, clientSecret string
	org                    string
	http                   *http.Client

	// redirectURL is the OAuth redirect_uri (Task 11), set via SetRedirectURL from
	// Deps.PublicURL + "/auth/callback". GitHub requires the SAME redirect_uri on both the
	// authorize request (LoginURL) and the token exchange (exchangeCode), or it rejects the
	// exchange — see both call sites below. Left empty (the zero value) it's simply omitted
	// from both requests, which only works for an OAuth app configured with a single,
	// unambiguous callback URL; buildAuthProvider in main.go refuses to boot a github/oidc
	// provider without BLERG_CORE_PUBLIC_URL specifically so this is always set in practice.
	redirectURL string

	// BaseURL and AuthorizeURL are test seams: production defaults are set by
	// NewGitHub, and tests point them at an httptest.Server fake standing in
	// for api.github.com / github.com respectively.
	BaseURL, AuthorizeURL string
}

// NewGitHub constructs a GitHub provider backed by st, using clientID/clientSecret
// (the OAuth app's credentials) and org (the GitHub org that gates membership).
func NewGitHub(st db.Store, clientID, clientSecret, org string, httpClient *http.Client) *GitHub {
	return &GitHub{
		st:           st,
		clientID:     clientID,
		clientSecret: clientSecret,
		org:          org,
		http:         httpClient,
		BaseURL:      "https://api.github.com",
		AuthorizeURL: "https://github.com/login/oauth",
	}
}

// ID identifies this provider as "github".
func (*GitHub) ID() string { return "github" }

// SetRedirectURL sets the OAuth redirect_uri sent on both the authorize request (LoginURL) and
// the token exchange (exchangeCode) — see the redirectURL field's doc comment.
func (g *GitHub) SetRedirectURL(u string) { g.redirectURL = u }

// LoginURL builds the GitHub OAuth authorize URL, requesting read:org scope
// (required to check org membership in Callback).
func (g *GitHub) LoginURL(state string) string {
	v := url.Values{
		"client_id": {g.clientID},
		"state":     {state},
		"scope":     {"read:org"},
	}
	if g.redirectURL != "" {
		v.Set("redirect_uri", g.redirectURL)
	}
	return g.AuthorizeURL + "/authorize?" + v.Encode()
}

// Callback exchanges the OAuth code for a token, resolves the authenticated
// user, and fails closed unless a live org-membership check positively
// confirms the user is a current member of g.org. Any error along the way —
// token exchange failure, malformed /user response, a membership check that
// errors, times out, or returns anything other than GitHub's documented
// "is a member" response (204 No Content) — is a rejected login. A 404 (not
// a member) and a 5xx (GitHub is down) are handled identically here: both
// are "not positively confirmed as a member," so both reject.
func (g *GitHub) Callback(ctx context.Context, r *http.Request) (Account, error) {
	code := r.URL.Query().Get("code")
	if code == "" {
		return Account{}, fmt.Errorf("github callback: missing code")
	}

	tok, err := g.exchangeCode(ctx, code)
	if err != nil {
		return Account{}, err
	}

	user, err := g.fetchUser(ctx, tok)
	if err != nil {
		return Account{}, err
	}

	if err := g.confirmOrgMembership(ctx, tok, user.Login); err != nil {
		return Account{}, err
	}

	subject := fmt.Sprintf("%d", user.ID)
	var accountID string
	err = g.st.Pool().QueryRow(ctx,
		`INSERT INTO accounts (provider, provider_subject, email, role)
		 VALUES ('github', $1, $2, 'member')
		 ON CONFLICT (provider, provider_subject) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id::text`, subject, user.Login).Scan(&accountID)
	if err != nil {
		return Account{}, fmt.Errorf("github callback: upsert account: %w", err)
	}
	return Account{ID: accountID, Provider: "github", ProviderSubject: subject, Email: user.Login}, nil
}

func (g *GitHub) exchangeCode(ctx context.Context, code string) (string, error) {
	tokReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.AuthorizeURL+"/access_token", nil)
	if err != nil {
		return "", err
	}
	tokReq.Header.Set("Accept", "application/json")
	v := url.Values{
		"client_id":     {g.clientID},
		"client_secret": {g.clientSecret},
		"code":          {code},
	}
	if g.redirectURL != "" {
		v.Set("redirect_uri", g.redirectURL)
	}
	tokReq.URL.RawQuery = v.Encode()

	tokResp, err := g.http.Do(tokReq)
	if err != nil {
		return "", fmt.Errorf("github token exchange: %w", err)
	}
	defer func() { _ = tokResp.Body.Close() }()
	if tokResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github token exchange: unexpected status %d", tokResp.StatusCode)
	}

	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tokResp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("github token exchange: malformed response")
	}
	return tok.AccessToken, nil
}

type githubUser struct {
	ID    int    `json:"id"`
	Login string `json:"login"`
}

func (g *GitHub) fetchUser(ctx context.Context, token string) (githubUser, error) {
	userReq, err := http.NewRequestWithContext(ctx, http.MethodGet, g.BaseURL+"/user", nil)
	if err != nil {
		return githubUser{}, err
	}
	userReq.Header.Set("Authorization", "Bearer "+token)
	userResp, err := g.http.Do(userReq)
	if err != nil {
		return githubUser{}, fmt.Errorf("github /user: %w", err)
	}
	defer func() { _ = userResp.Body.Close() }()
	if userResp.StatusCode != http.StatusOK {
		return githubUser{}, fmt.Errorf("github /user: unexpected status %d", userResp.StatusCode)
	}
	var user githubUser
	if err := json.NewDecoder(userResp.Body).Decode(&user); err != nil || user.Login == "" {
		return githubUser{}, fmt.Errorf("github /user: malformed response")
	}
	return user, nil
}

// confirmOrgMembership performs GitHub's documented org-membership check:
// GET /orgs/{org}/members/{username} returns 204 No Content when the
// authenticated user is a member (or 302 to their profile for a public
// membership check without auth — not applicable here since we always
// authenticate), and 404 when they are not a member of the org, or the org
// doesn't exist, or the caller lacks visibility. Anything other than 204 —
// including a 404 and including a network/5xx error reaching GitHub at
// all — is treated as "not confirmed," which fails the login closed. This
// is the security-critical half of this file: an inconclusive check must
// never be treated as a pass.
func (g *GitHub) confirmOrgMembership(ctx context.Context, token, login string) error {
	memberReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/orgs/%s/members/%s", g.BaseURL, g.org, login), nil)
	if err != nil {
		return err
	}
	memberReq.Header.Set("Authorization", "Bearer "+token)
	memberResp, err := g.http.Do(memberReq)
	if err != nil {
		return fmt.Errorf("github org membership check: %w", err)
	}
	defer func() { _ = memberResp.Body.Close() }()
	if memberResp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("github org membership check: user %q is not a confirmed member of %q (status %d)",
			login, g.org, memberResp.StatusCode)
	}
	return nil
}
