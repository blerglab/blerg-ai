package mcpconn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// The OAuth exchange (docs/design/ai-crons.md 4.3). StartOAuth discovers the server, registers a
// client when needed and stores a single-use state row; CompleteOAuth consumes it on the
// callback, trades the code for tokens and stores them encrypted with the key backend; the access
// token is handed out by FetchSecret, which refreshes it under a row lock. The refresh token never
// leaves this package. Nothing here logs a token, code, verifier or state.

const (
	// OAuthStateTTL is how long a started flow may take.
	OAuthStateTTL = 10 * time.Minute
	// refreshWindow is how close to expiry an access token must be for FetchSecret to refresh it.
	refreshWindow = 60 * time.Second

	maxPendingStates   = 20
	defaultAccessTTL   = time.Hour
	maxAccessTTL       = 30 * 24 * time.Hour
	oauthHTTPTimeout   = 15 * time.Second
	oauthMaxBody       = 1 << 20
	refreshOpTimeout   = 30 * time.Second
	revokeOpTimeout    = 10 * time.Second
	persistTimeout     = 20 * time.Second
	persistAttempts    = 3
	defaultStartLimit  = 10
	defaultStartWindow = time.Minute
)

var (
	// ErrRateLimited means the account started too many OAuth flows in the last minute.
	ErrRateLimited = errors.New("mcpconn: too many oauth flows started")
	// ErrUnavailable means a token could not be produced right now (the provider is unreachable
	// or erroring) although the connection itself is not known to be bad.
	ErrUnavailable = errors.New("mcpconn: token temporarily unavailable")
)

// CallbackError is why a callback was refused. Reason is one of a fixed set of words (state,
// denied, exchange, session, redirect_uri, issuer, name_taken, limit, gone, internal) and is the
// only thing ever reflected to the browser; Err is for logs and never holds a secret.
type CallbackError struct {
	Reason string
	Err    error
}

func (e *CallbackError) Error() string {
	if e.Err != nil {
		return "mcpconn: oauth callback refused (" + e.Reason + "): " + e.Err.Error()
	}
	return "mcpconn: oauth callback refused (" + e.Reason + ")"
}

func (e *CallbackError) Unwrap() error { return e.Err }

func refuse(reason string, err error) error { return &CallbackError{Reason: reason, Err: err} }

// OAuthStartInput begins a connect (new connection) or a reconnect (ReconnectID set).
type OAuthStartInput struct {
	AccountID string
	// SessionID is the login session (`sid`) the flow belongs to; the callback requires it live.
	SessionID string
	Name, URL string
	// ClientID names a client the person registered at the server; empty means register one.
	ClientID string
	// Scopes is an optional space separated scope list.
	Scopes string
	// ReconnectID, when set, replaces the tokens of that existing OAuth connection; Name and URL
	// are then taken from it.
	ReconnectID string
	// RedirectURI is <BLERG_CORE_PUBLIC_URL>/auth/mcp/callback.
	RedirectURI string
}

// OAuthStartResult is what the browser needs: where to go and the value of the state cookie.
type OAuthStartResult struct {
	AuthorizationURL string
	// AuthorizationServer is the host of the authorization server the flow sends the person to,
	// so the UI can show where they are going (a protected resource may name any server).
	AuthorizationServer string
	// StateCookie is hex SHA-256(state): the value of the SameSite=Lax cookie that binds the
	// callback to the browser that started the flow. It never holds the state itself.
	StateCookie string
}

// OAuthCallbackInput is what the callback request carries.
type OAuthCallbackInput struct {
	State, Code string
	// Iss and IssPresent are the RFC 9207 iss parameter and whether it was sent at all.
	Iss        string
	IssPresent bool
	// ProviderError is true when the provider answered with an error instead of a code.
	ProviderError bool
	// RedirectURI is the redirect URI this deployment's callback computes for itself; it must
	// equal the one recorded when the flow started.
	RedirectURI string
	// SessionLive reports whether the login session that started the flow still stands.
	SessionLive func(ctx context.Context, accountID, sid string) (bool, error)
}

// draft is the state row's jsonb: what the callback needs to finish the connection.
type draft struct {
	Name                  string `json:"name"`
	URL                   string `json:"url"`
	Scope                 string `json:"scope,omitempty"`
	ClientID              string `json:"client_id"`
	ClientSecretCT        string `json:"client_secret_ct,omitempty"` // sealed with the key backend
	ClientSecretKeyID     string `json:"client_secret_key_id,omitempty"`
	AuthMethod            string `json:"auth_method"`
	Dynamic               bool   `json:"dynamic_client"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint,omitempty"`
	IssParamSupported     bool   `json:"iss_param_supported"`
	ReconnectID           string `json:"reconnect_id,omitempty"`
	// ClientSecretExpiresAt is RFC 7591's client_secret_expires_at (unix seconds, 0 = never).
	ClientSecretExpiresAt int64 `json:"client_secret_expires_at,omitempty"`
}

// tokenBundle is what secret_ciphertext holds for an OAuth connection, sealed as one JSON blob.
type tokenBundle struct {
	AccessToken        string    `json:"access_token"`
	RefreshToken       string    `json:"refresh_token,omitempty"`
	ExpiresAt          time.Time `json:"expires_at"`
	TokenEndpoint      string    `json:"token_endpoint"`
	RevocationEndpoint string    `json:"revocation_endpoint,omitempty"`
	ClientID           string    `json:"client_id"`
	ClientSecret       string    `json:"client_secret,omitempty"`
	AuthMethod         string    `json:"auth_method"`
	Resource           string    `json:"resource"`
	// ClientSecretExpiresAt is RFC 7591's client_secret_expires_at (unix seconds, 0 = never).
	ClientSecretExpiresAt int64 `json:"client_secret_expires_at,omitempty"`
}

// oauthMeta is oauth_meta: issuer, resource, client id and endpoints. No secrets.
type oauthMeta struct {
	Issuer                string `json:"issuer"`
	Resource              string `json:"resource"`
	ClientID              string `json:"client_id"`
	Dynamic               bool   `json:"dynamic_client"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint,omitempty"`
	AuthMethod            string `json:"token_endpoint_auth_method"`
	Scope                 string `json:"scope,omitempty"`
	IssParamSupported     bool   `json:"iss_param_supported"`
	// RedirectURI is the redirect URI a dynamically registered client was registered with: a
	// reconnect under a different one registers again.
	RedirectURI string `json:"redirect_uri,omitempty"`
}

// startLimiter is a process-local sliding window per account (the login limiter's approach).
type startLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
}

func (l *startLimiter) allow(account string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cut := now.Add(-l.window)
	kept := l.hits[account][:0]
	for _, t := range l.hits[account] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.hits[account] = kept
		return false
	}
	l.hits[account] = append(kept, now)
	return true
}

// SetOAuthStartLimit sets the per-account limit on StartOAuth calls (default 10 per minute).
// Call it before serving.
func (s *Service) SetOAuthStartLimit(n int, window time.Duration) {
	s.limiter.mu.Lock()
	defer s.limiter.mu.Unlock()
	s.limiter.max, s.limiter.window = n, window
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func stateHash(state string) [sha256.Size]byte { return sha256.Sum256([]byte(state)) }

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

var clientIDRe = regexp.MustCompile(`^[\x21-\x7e]{1,256}$`)

// checkRoom is the non-authoritative early check of the connection limit and the name, so a
// person is told before being sent to the provider. The insert re-checks under the account lock.
func (s *Service) checkRoom(ctx context.Context, accountID, name string) error {
	rows, err := s.st.Pool().Query(ctx, `SELECT name FROM mcp_connections WHERE account_id = $1`, accountID)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	want := NormalizeName(name)
	for rows.Next() {
		var other string
		if err := rows.Scan(&other); err != nil {
			return err
		}
		n++
		if NormalizeName(other) == want {
			return ErrNameTaken
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n >= MaxPerAccount {
		return ErrLimit
	}
	return nil
}

// loadBundle reads and opens an OAuth connection's token bundle (no lock).
func (s *Service) openBundle(ctx context.Context, ct []byte, keyID *string) (tokenBundle, error) {
	var b tokenBundle
	if keyID == nil || len(ct) == 0 {
		return b, errors.New("mcpconn: oauth connection has no token bundle")
	}
	plain, err := s.backend.Decrypt(ctx, ct, *keyID)
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(plain, &b); err != nil {
		return b, errors.New("mcpconn: corrupt token bundle")
	}
	return b, nil
}

func (s *Service) sealBundle(ctx context.Context, b tokenBundle) ([]byte, string, error) {
	raw, err := json.Marshal(b) //nolint:gosec // G117: the bundle is sealed with the key backend on the next line and never stored or logged in the clear
	if err != nil {
		return nil, "", err
	}
	return s.backend.Encrypt(ctx, raw)
}

// StartOAuth discovers the server, registers a client when needed, records the single-use state
// and returns the authorization URL.
func (s *Service) StartOAuth(ctx context.Context, in OAuthStartInput) (OAuthStartResult, error) {
	if !uuidRe.MatchString(in.AccountID) {
		return OAuthStartResult{}, ErrNotFound
	}
	if in.SessionID == "" {
		return OAuthStartResult{}, invalid("a signed-in browser session is required")
	}
	if ru, err := url.Parse(in.RedirectURI); err != nil || in.RedirectURI == "" || ru.Host == "" || ru.Fragment != "" {
		return OAuthStartResult{}, invalid("redirect uri is not configured")
	}
	if err := validateScopes(in.Scopes); err != nil {
		return OAuthStartResult{}, err
	}
	if in.ClientID != "" && !clientIDRe.MatchString(in.ClientID) {
		return OAuthStartResult{}, invalid("client_id is not valid")
	}
	if !s.limiter.allow(in.AccountID) {
		return OAuthStartResult{}, ErrRateLimited
	}

	name, mcpURL := in.Name, in.URL
	var old *tokenBundle
	var oldMeta oauthMeta
	if in.ReconnectID != "" {
		c, err := s.Get(ctx, in.AccountID, in.ReconnectID)
		if err != nil {
			return OAuthStartResult{}, err
		}
		if c.AuthKind != "oauth" {
			return OAuthStartResult{}, invalid("only an oauth connection can be reconnected")
		}
		name, mcpURL = c.Name, c.URL
		var ct []byte
		var keyID *string
		var meta []byte
		if err := s.st.Pool().QueryRow(ctx, `SELECT secret_ciphertext, key_id, oauth_meta FROM mcp_connections WHERE id = $1 AND account_id = $2`,
			c.ID, in.AccountID).Scan(&ct, &keyID, &meta); err != nil {
			return OAuthStartResult{}, err
		}
		if b, err := s.openBundle(ctx, ct, keyID); err == nil {
			old = &b
		}
		if json.Unmarshal(meta, &oldMeta) != nil {
			oldMeta = oauthMeta{}
		}
	} else {
		if err := s.validateName(name); err != nil {
			return OAuthStartResult{}, err
		}
		if err := s.validateURL(mcpURL); err != nil {
			return OAuthStartResult{}, err
		}
		if err := s.checkRoom(ctx, in.AccountID, name); err != nil {
			return OAuthStartResult{}, err
		}
	}
	if in.ReconnectID != "" {
		if err := s.validateURL(mcpURL); err != nil {
			return OAuthStartResult{}, err
		}
	}

	disc, err := s.discover(ctx, mcpURL)
	if err != nil {
		return OAuthStartResult{}, err
	}
	scope := in.Scopes
	if scope == "" {
		scope = disc.scope
	}

	d := draft{
		Name: name, URL: mcpURL, Scope: scope, AuthorizationEndpoint: disc.as.AuthorizationEndpoint,
		TokenEndpoint: disc.as.TokenEndpoint, RevocationEndpoint: disc.as.RevocationEndpoint,
		IssParamSupported: disc.as.IssParamSupported, ReconnectID: in.ReconnectID,
	}
	switch {
	case in.ClientID != "":
		// A client_id alone is a public client: the server must accept "none" (RFC 8414: an absent
		// list means client_secret_basic only).
		if !slices.Contains(effectiveAuthMethods(disc.as.TokenAuthMethods), "none") {
			return OAuthStartResult{}, invalid("this server requires client authentication, so a client_id alone cannot be used: leave it empty to register a client automatically")
		}
		d.ClientID, d.AuthMethod = in.ClientID, "none"
	case old != nil && old.ClientID != "" && oldMeta.Issuer == disc.as.Issuer && clientReusable(old, oldMeta, in.RedirectURI):
		// Reconnecting to the same authorization server: the client registered earlier is reused
		// (unless it was registered for another redirect URI or its secret has expired).
		d.ClientID, d.AuthMethod, d.Dynamic, d.ClientSecretExpiresAt = old.ClientID, old.AuthMethod, oldMeta.Dynamic, old.ClientSecretExpiresAt
		var err error
		if d.ClientSecretCT, d.ClientSecretKeyID, err = s.encryptDraftSecret(ctx, old.ClientSecret); err != nil {
			return OAuthStartResult{}, err
		}
	case disc.as.RegistrationEndpoint != "":
		rc, err := s.register(ctx, disc.as, in.RedirectURI, scope)
		if err != nil {
			return OAuthStartResult{}, err
		}
		d.ClientID, d.AuthMethod, d.Dynamic, d.ClientSecretExpiresAt = rc.id, rc.method, true, rc.secretExpiresAt
		if d.ClientSecretCT, d.ClientSecretKeyID, err = s.encryptDraftSecret(ctx, rc.secret); err != nil {
			return OAuthStartResult{}, err
		}
	default:
		return OAuthStartResult{}, invalid("this server does not support dynamic client registration: supply a client_id registered with it")
	}

	state, err := randomToken(32)
	if err != nil {
		return OAuthStartResult{}, err
	}
	verifier, err := randomToken(32)
	if err != nil {
		return OAuthStartResult{}, err
	}
	draftJSON, err := json.Marshal(d)
	if err != nil {
		return OAuthStartResult{}, err
	}
	h := stateHash(state)
	if err := s.storeState(ctx, h[:], in, verifier, draftJSON, disc); err != nil {
		return OAuthStartResult{}, err
	}

	au, err := url.Parse(disc.as.AuthorizationEndpoint)
	if err != nil {
		return OAuthStartResult{}, invalid("the authorization endpoint is not valid")
	}
	q := au.Query()
	q.Set("response_type", "code")
	q.Set("client_id", d.ClientID)
	q.Set("redirect_uri", in.RedirectURI)
	q.Set("code_challenge", pkceChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("resource", disc.resource)
	if scope != "" {
		q.Set("scope", scope)
	}
	au.RawQuery = q.Encode()
	var asHost string
	if iu, err := url.Parse(disc.as.Issuer); err == nil {
		asHost = iu.Host
	}
	return OAuthStartResult{AuthorizationURL: au.String(), AuthorizationServer: asHost, StateCookie: fmt.Sprintf("%x", h[:])}, nil
}

// clientReusable reports whether the client a connection was registered with can be used for a
// reconnect: a client the person supplied always can; a dynamically registered one only under the
// redirect URI it was registered with and while its secret has not expired (RFC 7591
// client_secret_expires_at, with a minute of margin).
func clientReusable(old *tokenBundle, meta oauthMeta, redirectURI string) bool {
	if !meta.Dynamic {
		return true
	}
	if meta.RedirectURI != redirectURI {
		return false
	}
	return old.ClientSecretExpiresAt == 0 || old.ClientSecretExpiresAt > time.Now().Add(time.Minute).Unix()
}

// storeState inserts the single-use row and keeps at most maxPendingStates per account.
func (s *Service) storeState(ctx context.Context, hash []byte, in OAuthStartInput, verifier string, draftJSON []byte, disc *discovery) error {
	if _, err := s.st.Pool().Exec(ctx,
		`INSERT INTO mcp_oauth_state (state_hash, account_id, sid, code_verifier, draft, issuer, resource, redirect_uri, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now() + make_interval(secs => $9))`,
		hash, in.AccountID, in.SessionID, verifier, draftJSON, disc.as.Issuer, disc.resource, in.RedirectURI, OAuthStateTTL.Seconds()); err != nil {
		return err
	}
	_, err := s.st.Pool().Exec(ctx,
		`DELETE FROM mcp_oauth_state WHERE account_id = $1 AND state_hash NOT IN
		   (SELECT state_hash FROM mcp_oauth_state WHERE account_id = $1 ORDER BY expires_at DESC LIMIT $2)`,
		in.AccountID, maxPendingStates)
	return err
}

// PruneOAuthState deletes state rows past their expiry and returns how many.
func (s *Service) PruneOAuthState(ctx context.Context) (int64, error) {
	tag, err := s.st.Pool().Exec(ctx, `DELETE FROM mcp_oauth_state WHERE expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CompleteOAuth finishes a flow from the callback. The state row is consumed (deleted) on first
// use whatever the outcome. On success it returns the new or reconnected connection.
func (s *Service) CompleteOAuth(ctx context.Context, in OAuthCallbackInput) (Connection, error) {
	if in.State == "" {
		return Connection{}, refuse("state", nil)
	}
	h := stateHash(in.State)
	var accountID, sid, verifier, issuer, resource, redirectURI string
	var draftJSON []byte
	var live bool
	err := s.st.Pool().QueryRow(ctx,
		`DELETE FROM mcp_oauth_state WHERE state_hash = $1
		 RETURNING account_id::text, sid, code_verifier, draft, issuer, resource, redirect_uri, expires_at > now()`,
		h[:]).Scan(&accountID, &sid, &verifier, &draftJSON, &issuer, &resource, &redirectURI, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, refuse("state", nil)
	}
	if err != nil {
		return Connection{}, refuse("internal", err)
	}
	if !live {
		return Connection{}, refuse("state", nil)
	}
	if in.ProviderError {
		return Connection{}, refuse("denied", nil)
	}
	if in.Code == "" {
		return Connection{}, refuse("exchange", nil)
	}
	if in.RedirectURI != redirectURI {
		return Connection{}, refuse("redirect_uri", nil)
	}
	var d draft
	if err := json.Unmarshal(draftJSON, &d); err != nil {
		return Connection{}, refuse("internal", err)
	}
	// RFC 9207: a returned iss must be the issuer the flow started with, and a server that
	// advertises the parameter must send it.
	if (in.IssPresent && in.Iss != issuer) || (!in.IssPresent && d.IssParamSupported) {
		return Connection{}, refuse("issuer", nil)
	}
	if in.SessionLive == nil {
		return Connection{}, refuse("session", nil)
	}
	ok, err := in.SessionLive(ctx, accountID, sid)
	if err != nil {
		return Connection{}, refuse("internal", err)
	}
	if !ok {
		return Connection{}, refuse("session", nil)
	}
	if d.ReconnectID == "" {
		if err := s.checkRoom(ctx, accountID, d.Name); err != nil {
			return Connection{}, roomRefusal(err)
		}
	}

	secret, err := s.decryptDraftSecret(ctx, d.ClientSecretCT, d.ClientSecretKeyID)
	if err != nil {
		return Connection{}, refuse("internal", err)
	}
	b := tokenBundle{
		TokenEndpoint: d.TokenEndpoint, RevocationEndpoint: d.RevocationEndpoint, ClientID: d.ClientID,
		ClientSecret: secret, AuthMethod: d.AuthMethod, Resource: resource, ClientSecretExpiresAt: d.ClientSecretExpiresAt,
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {in.Code}, "redirect_uri": {redirectURI},
		"code_verifier": {verifier}, "resource": {resource},
	}
	tr, err := s.tokenRequest(ctx, b, form)
	if err != nil {
		return Connection{}, refuse("exchange", err)
	}
	b.AccessToken, b.RefreshToken, b.ExpiresAt = tr.accessToken, tr.refreshToken, tr.expiresAt

	ct, keyID, err := s.sealBundle(ctx, b)
	if err != nil {
		return Connection{}, refuse("internal", err)
	}
	meta, err := json.Marshal(oauthMeta{
		Issuer: issuer, Resource: resource, ClientID: d.ClientID, Dynamic: d.Dynamic,
		AuthorizationEndpoint: d.AuthorizationEndpoint, TokenEndpoint: d.TokenEndpoint,
		RevocationEndpoint: d.RevocationEndpoint, AuthMethod: d.AuthMethod, Scope: d.Scope,
		IssParamSupported: d.IssParamSupported, RedirectURI: dynamicRedirect(d.Dynamic, redirectURI),
	})
	if err != nil {
		return Connection{}, refuse("internal", err)
	}

	var c Connection
	var replaced *tokenBundle
	if d.ReconnectID != "" {
		c, replaced, err = s.replaceOAuth(ctx, accountID, d.ReconnectID, ct, keyID, meta)
	} else {
		c, err = s.insert(ctx, accountID, d.Name, d.URL, "oauth", nil, ct, &keyID, meta)
	}
	if err != nil {
		// The provider already issued tokens for a connection that was not stored: take them back.
		s.revokeUpstream(ctx, b)
		if errors.Is(err, ErrNotFound) {
			return Connection{}, refuse("gone", nil)
		}
		return Connection{}, roomRefusal(err)
	}
	if replaced != nil {
		// The new grant is stored: the old one is no longer needed anywhere. Best effort.
		s.revokeUpstream(ctx, *replaced)
	}
	return c, nil
}

func dynamicRedirect(dynamic bool, redirectURI string) string {
	if dynamic {
		return redirectURI
	}
	return ""
}

func roomRefusal(err error) error {
	switch {
	case errors.Is(err, ErrNameTaken):
		return refuse("name_taken", nil)
	case errors.Is(err, ErrLimit):
		return refuse("limit", nil)
	default:
		return refuse("internal", err)
	}
}

// replaceOAuth swaps the tokens and metadata of an existing OAuth connection (reconnect) in one
// short transaction and returns the bundle it replaced (nil when that was unreadable) so the
// caller can revoke the old grant upstream after the commit.
func (s *Service) replaceOAuth(ctx context.Context, accountID, id string, ct []byte, keyID string, meta []byte) (Connection, *tokenBundle, error) {
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return Connection{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var oldCT []byte
	var oldKey *string
	err = tx.QueryRow(ctx,
		`SELECT secret_ciphertext, key_id FROM mcp_connections WHERE id = $1 AND account_id = $2 AND auth_kind = 'oauth' FOR UPDATE`,
		id, accountID).Scan(&oldCT, &oldKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, nil, ErrNotFound
	}
	if err != nil {
		return Connection{}, nil, err
	}
	c, err := scan(tx.QueryRow(ctx,
		`UPDATE mcp_connections SET secret_ciphertext = $1, key_id = $2, oauth_meta = $3, status = 'ok',
		        last_verified_at = now(), updated_at = now()
		  WHERE id = $4 AND account_id = $5 AND auth_kind = 'oauth' RETURNING `+cols,
		ct, keyID, meta, id, accountID))
	if err != nil {
		return Connection{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Connection{}, nil, err
	}
	var old *tokenBundle
	if b, err := s.openBundle(ctx, oldCT, oldKey); err == nil {
		old = &b
	}
	return c, old, nil
}

type tokenResult struct {
	accessToken, refreshToken string
	expiresAt                 time.Time
}

// tokenError is a failed call to the token endpoint. rejected means the provider refused the grant
// or the client: a 400 or 401 whose RFC 6749 error code is invalid_grant, invalid_client or
// unauthorized_client. Anything else (network, 5xx, 408, 429, other 4xx, an unparseable or
// non-bearer 200) is transient and must never cost the person their connection.
type tokenError struct {
	rejected bool
	status   int
	code     string // the RFC 6749 error code when well formed, never free text
}

func (e *tokenError) Error() string {
	return fmt.Sprintf("token endpoint: status %d code %q rejected=%v", e.status, e.code, e.rejected)
}

var errCodeRe = regexp.MustCompile(`^[a-z_]{1,40}$`)

// tokenRequest posts form to b's token endpoint with the client authenticated as registered.
func (s *Service) tokenRequest(ctx context.Context, b tokenBundle, form url.Values) (tokenResult, error) {
	req, err := newFormRequest(ctx, b.TokenEndpoint, form, b)
	if err != nil {
		return tokenResult{}, &tokenError{}
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		return tokenResult{}, &tokenError{}
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		AccessToken  string          `json:"access_token"`
		TokenType    string          `json:"token_type"`
		RefreshToken string          `json:"refresh_token"`
		ExpiresIn    json.RawMessage `json:"expires_in"`
		Error        string          `json:"error"`
	}
	dec := json.NewDecoder(resp.Body)
	derr := dec.Decode(&body)
	if resp.StatusCode != http.StatusOK {
		te := &tokenError{status: resp.StatusCode}
		if derr == nil && errCodeRe.MatchString(body.Error) {
			te.code = body.Error
		}
		te.rejected = (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized) &&
			(te.code == "invalid_grant" || te.code == "invalid_client" || te.code == "unauthorized_client")
		return tokenResult{}, te
	}
	if derr != nil || body.AccessToken == "" || !strings.EqualFold(body.TokenType, "bearer") {
		// A success that is not a usable bearer token says nothing about the grant: transient.
		return tokenResult{}, &tokenError{status: resp.StatusCode}
	}
	ttl := defaultAccessTTL
	if secs, ok := parseExpiresIn(body.ExpiresIn); ok {
		ttl = min(time.Duration(secs*float64(time.Second)), maxAccessTTL)
	}
	return tokenResult{accessToken: body.AccessToken, refreshToken: body.RefreshToken, expiresAt: time.Now().Add(ttl)}, nil
}

// parseExpiresIn reads expires_in as a JSON number or a numeric string (some providers send
// "3600"); ok is false for anything else or a non-positive value.
func parseExpiresIn(raw json.RawMessage) (float64, bool) {
	var secs float64
	if json.Unmarshal(raw, &secs) != nil {
		var str string
		if json.Unmarshal(raw, &str) != nil {
			return 0, false
		}
		var err error
		if secs, err = strconv.ParseFloat(strings.TrimSpace(str), 64); err != nil {
			return 0, false
		}
	}
	return secs, secs > 0 && !math.IsInf(secs, 0) && !math.IsNaN(secs)
}

// newFormRequest builds a form POST to endpoint with the client authenticated the way it was
// registered: client_secret_basic in the Authorization header, client_secret_post in the body,
// and a public client ("none") by client_id alone.
func newFormRequest(ctx context.Context, endpoint string, form url.Values, b tokenBundle) (*http.Request, error) {
	form = cloneValues(form)
	basic := b.ClientSecret != "" && b.AuthMethod != "client_secret_post" && b.AuthMethod != "none"
	switch {
	case b.ClientSecret != "" && b.AuthMethod == "client_secret_post":
		form.Set("client_id", b.ClientID)
		form.Set("client_secret", b.ClientSecret)
	case !basic:
		form.Set("client_id", b.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		req.SetBasicAuth(url.QueryEscape(b.ClientID), url.QueryEscape(b.ClientSecret))
	}
	return req, nil
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v)+2)
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
