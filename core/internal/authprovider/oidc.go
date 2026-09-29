package authprovider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/blerglab/blerg-ai/core/internal/db"
)

// OIDC is the "oidc" auth provider: a generic OpenID Connect login that
// works against any spec-compliant IdP (Google Workspace, Okta, Keycloak,
// etc). Unlike GitHub, there is no group/role claim mapping here — this is
// authentication-only per the spec's Non-goals. Every account OIDC creates
// gets role='member'; an admin promotes someone afterward via direct DB
// access (or, once it exists, an admin UI). Do not extend Callback to parse
// "groups" or "roles" claims even if a real IdP happens to send them — that
// is explicitly out of scope for v1.
//
// ID-token verification (signature against the IdP's real JWKS, iss/aud/exp)
// is delegated entirely to github.com/coreos/go-oidc/v3/oidc, which builds
// on golang.org/x/oauth2 — this file never parses or verifies a JWT itself.
// Hand-rolling that verification is a well-known class of security bug
// (algorithm confusion, missing expiry checks, etc).
type OIDC struct {
	st           db.Store
	oauth2Config oauth2.Config
	verifier     *oidc.IDTokenVerifier
}

// NewOIDC constructs an OIDC provider backed by st, discovering the issuer's
// OpenID configuration (and JWKS location) up front via oidc.NewProvider.
// This makes NewOIDC fail fast at construction time if issuerURL's discovery
// document isn't reachable or valid, rather than failing lazily on the
// first login attempt.
func NewOIDC(ctx context.Context, st db.Store, issuerURL, clientID, clientSecret string) (*OIDC, error) {
	provider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc: discover issuer %q: %w", issuerURL, err)
	}

	return &OIDC{
		st: st,
		oauth2Config: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "email"},
		},
		// ClientID is required here: it's how the verifier checks the
		// token's "aud" claim actually names this client, not just any
		// client of the same IdP.
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
	}, nil
}

// ID identifies this provider as "oidc".
func (*OIDC) ID() string { return "oidc" }

// SetRedirectURL sets the OAuth redirect_uri go-oidc's oauth2.Config sends on both the
// authorize request (LoginURL) and the token exchange (Callback) — same requirement as
// GitHub's SetRedirectURL, just backed by oauth2.Config's own RedirectURL field.
func (o *OIDC) SetRedirectURL(u string) { o.oauth2Config.RedirectURL = u }

// LoginURL builds the IdP's authorization URL, requesting the openid and
// email scopes (the only claims this provider consumes).
func (o *OIDC) LoginURL(state string) string {
	return o.oauth2Config.AuthCodeURL(state)
}

// oidcClaims is deliberately narrow: only the claims v1 consumes. Do NOT add
// "groups" or "roles" here — see the OIDC type's doc comment for why that's
// out of scope.
type oidcClaims struct {
	Email string `json:"email"`
}

// Callback exchanges the authorization code for tokens, then verifies the
// resulting id_token's signature, issuer, audience, and expiry via
// go-oidc's IDTokenVerifier before trusting any claim inside it. Any
// failure along the way — code exchange, a missing id_token, or a token
// that fails verification (forged signature, wrong issuer/audience,
// expired, malformed) — is a rejected login; verification failures are
// never treated as "inconclusive, so allow."
func (o *OIDC) Callback(ctx context.Context, r *http.Request) (Account, error) {
	code := r.URL.Query().Get("code")
	if code == "" {
		return Account{}, fmt.Errorf("oidc callback: missing code")
	}

	tok, err := o.oauth2Config.Exchange(ctx, code)
	if err != nil {
		return Account{}, fmt.Errorf("oidc callback: code exchange: %w", err)
	}

	rawIDToken, ok := tok.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Account{}, fmt.Errorf("oidc callback: token response had no id_token")
	}

	idToken, err := o.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return Account{}, fmt.Errorf("oidc callback: id_token verification failed: %w", err)
	}

	subject := idToken.Subject
	if subject == "" {
		return Account{}, fmt.Errorf("oidc callback: id_token missing sub claim")
	}

	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		return Account{}, fmt.Errorf("oidc callback: decoding claims: %w", err)
	}

	var accountID string
	err = o.st.Pool().QueryRow(ctx,
		`INSERT INTO accounts (provider, provider_subject, email, role)
		 VALUES ('oidc', $1, $2, 'member')
		 ON CONFLICT (provider, provider_subject) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id::text`, subject, claims.Email).Scan(&accountID)
	if err != nil {
		return Account{}, fmt.Errorf("oidc callback: upsert account: %w", err)
	}
	return Account{ID: accountID, Provider: "oidc", ProviderSubject: subject, Email: claims.Email}, nil
}
