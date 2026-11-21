package mcpconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const SessionOAuthStatePrefix = "smcp_"

// IsSessionOAuthState separates the 48-character namespace from legacy
// 43-character random states, even if their random prefix happens to match.
func IsSessionOAuthState(state string) bool {
	return len(state) == len(SessionOAuthStatePrefix)+43 && strings.HasPrefix(state, SessionOAuthStatePrefix)
}

type SessionOAuthPreviewInput struct {
	SessionOAuthRequest
	Scopes      string
	RedirectURI string
	SessionLive func(context.Context, string, string) (bool, error)
}

type SessionOAuthPreview struct {
	DraftID         string   `json:"draft_id"`
	RunnerSessionID string   `json:"runner_session_id"`
	Name            string   `json:"name"`
	Source          string   `json:"source"`
	ResourceHost    string   `json:"resource_host"`
	IssuerHost      string   `json:"issuer_host"`
	ProviderHost    string   `json:"provider_host"`
	Scopes          []string `json:"scopes"`
}

type SessionOAuthCompletion struct {
	SessionCredential
	AttemptID string
}
type SessionOAuthAttemptStatus struct {
	Stage   string              `json:"stage"`
	Preview SessionOAuthPreview `json:"preview"`
}

// SessionOAuthFlow is the narrow human API boundary. Production uses Service;
// HTTP handler tests can substitute a flow without weakening the provider client.
type SessionOAuthFlow interface {
	PreviewSessionOAuth(context.Context, SessionOAuthPreviewInput) (SessionOAuthPreview, error)
	StartSessionOAuth(context.Context, string, string, string, string) (OAuthStartResult, error)
	CompleteSessionOAuth(context.Context, OAuthCallbackInput) (SessionOAuthCompletion, error)
	SessionOAuthStatus(context.Context, string, string, string) (SessionOAuthAttemptStatus, error)
	CancelSessionOAuth(context.Context, string, string, string) error
}

type sessionOAuthDraft struct {
	Authorization SessionOAuthAuthorization `json:"authorization"`
	Server        authServer                `json:"server"`
	Scopes        string                    `json:"scopes"`
	RedirectURI   string                    `json:"redirect_uri"`
}

func sessionHTTPS(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, invalid("remote sign-in requires a valid HTTPS address")
	}
	for _, c := range u.Host {
		if c > 127 || c < 33 {
			return nil, invalid("remote sign-in requires an ASCII provider host")
		}
	}
	return u, nil
}
func sessionRedirect(raw string) error {
	u, err := sessionHTTPS(raw)
	if err != nil {
		return err
	}
	if u.Path != "/auth/mcp/callback" || u.RawQuery != "" || u.ForceQuery {
		return invalid("remote sign-in callback is not configured")
	}
	return nil
}
func sessionScopes(raw string) (string, error) {
	if err := validateScopes(raw); err != nil {
		return "", err
	}
	tokens := strings.Fields(raw)
	slices.Sort(tokens)
	return strings.Join(slices.Compact(tokens), " "), nil
}
func (s *Service) validateSessionAuthorization(a SessionOAuthAuthorization, request SessionOAuthRequest) error {
	if !validSessionScope(a.Binding.SessionScope) || a.Binding.AccountID != request.AccountID || a.Binding.HumanSessionID != request.HumanSessionID || a.Binding.RunnerSessionID != request.RunnerSessionID || !uuidRe.MatchString(a.Binding.HumanSessionID) || !a.Binding.HumanDeadline.After(s.oauthTime()) || a.LaunchGeneration <= 0 || a.Definition.ID != request.DefinitionID || !uuidRe.MatchString(a.Definition.ID) || a.Definition.Version <= 0 {
		return ErrNotFound
	}
	if a.Definition.Source != "settings" && a.Definition.Source != "session_suggestion_saved_by_human" {
		return ErrNotFound
	}
	if err := s.validateName(a.Definition.Name); err != nil {
		return err
	}
	for _, raw := range []string{a.Definition.URL, a.Definition.Issuer} {
		u, err := sessionHTTPS(raw)
		if err != nil {
			return err
		}
		if u.RawQuery != "" || u.ForceQuery {
			return invalid("remote server definitions cannot contain query parameters")
		}
		if err = s.policy.CheckURL(raw); err != nil {
			return invalid("remote server definition is not permitted")
		}
	}
	if a.Definition.ClientID != "" && !clientIDRe.MatchString(a.Definition.ClientID) {
		return invalid("OAuth client is not valid")
	}
	return nil
}
func (s *Service) sessionDiscovery(ctx context.Context, definition SessionServerDefinition) (authServer, error) {
	hint, _, err := s.probe(ctx, definition.URL)
	if err != nil {
		return authServer{}, err
	}
	resource, err := s.fetchResourceMetadata(ctx, definition.URL, hint)
	if err != nil {
		return authServer{}, err
	}
	if len(resource.AuthorizationServers) > maxAuthServers || !slices.Contains(resource.AuthorizationServers, definition.Issuer) {
		return authServer{}, invalid("the selected issuer is not advertised by this resource")
	}
	server, err := s.fetchAuthServer(ctx, definition.Issuer)
	if err != nil {
		return authServer{}, err
	}
	if err = s.checkAuthServer(&server); err != nil {
		return authServer{}, err
	}
	for _, raw := range []string{server.AuthorizationEndpoint, server.TokenEndpoint, server.RegistrationEndpoint, server.RevocationEndpoint} {
		if raw != "" {
			if _, err = sessionHTTPS(raw); err != nil {
				return authServer{}, err
			}
		}
	}
	authURL, _ := url.Parse(server.AuthorizationEndpoint)
	q, err := url.ParseQuery(authURL.RawQuery)
	if err != nil {
		return authServer{}, invalid("authorization endpoint query is not valid")
	}
	reserved := map[string]bool{"state": true, "redirect_uri": true, "code_challenge": true, "code_challenge_method": true, "client_id": true, "response_type": true, "scope": true, "resource": true, "response_mode": true}
	for key, values := range q {
		if reserved[key] || len(values) != 1 {
			return authServer{}, invalid("authorization endpoint contains reserved or repeated parameters")
		}
	}
	return server, nil
}

func (s *Service) PreviewSessionOAuth(ctx context.Context, in SessionOAuthPreviewInput) (out SessionOAuthPreview, err error) {
	defer func() { logSessionFailure("oauth_preview", err) }()
	if s.sessionAuthority == nil {
		return out, errSessionOAuthAuthority
	}
	if !uuidRe.MatchString(in.AccountID) || !uuidRe.MatchString(in.HumanSessionID) || !uuidRe.MatchString(in.RunnerSessionID) || !uuidRe.MatchString(in.DefinitionID) {
		return out, ErrNotFound
	}
	if err = sessionRedirect(in.RedirectURI); err != nil {
		return out, err
	}
	if in.SessionLive == nil {
		return out, ErrNotFound
	}
	live, err := in.SessionLive(ctx, in.AccountID, in.HumanSessionID)
	if err != nil {
		return out, err
	}
	if !live {
		return out, ErrNotFound
	}
	scope, err := sessionScopes(in.Scopes)
	if err != nil {
		return out, err
	}
	if !s.limiter.allow(in.AccountID) {
		return out, ErrRateLimited
	}
	authorization, err := s.sessionAuthority.Resolve(ctx, in.SessionOAuthRequest)
	if err != nil {
		return out, err
	}
	if err = s.validateSessionAuthorization(authorization, in.SessionOAuthRequest); err != nil {
		return out, err
	}
	// Core owns the absolute login lifetime; a trusted Runner response cannot
	// extend it. Freeze the shorter deadline into both the draft and bundle.
	var loginDeadline time.Time
	err = s.st.Pool().QueryRow(ctx, `SELECT hs.expires_at FROM human_sessions hs JOIN accounts a ON a.id=hs.account_id WHERE hs.account_id=$1 AND hs.chain_id=$2 AND hs.revoked_at IS NULL AND hs.expires_at>clock_timestamp() AND a.disabled_at IS NULL AND NOT a.must_change_password`, in.AccountID, in.HumanSessionID).Scan(&loginDeadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	authorization.Binding.HumanDeadline = minTime(authorization.Binding.HumanDeadline, loginDeadline)
	server, err := s.sessionDiscovery(ctx, authorization.Definition)
	if err != nil {
		return out, err
	}
	if err = s.sessionAuthority.Validate(ctx, authorization); err != nil {
		return out, err
	}
	draft := sessionOAuthDraft{Authorization: authorization, Server: server, Scopes: scope, RedirectURI: in.RedirectURI}
	raw, err := json.Marshal(draft)
	if err != nil {
		return out, err
	}
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockAccount(ctx, tx, in.AccountID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE mcp_session_oauth_attempts SET stage='expired',verifier_ciphertext=NULL,verifier_key_id=NULL,client_ciphertext=NULL,client_key_id=NULL WHERE account_id=$1 AND expires_at<=clock_timestamp() AND stage IN ('preview','starting','pending','exchanging')`, in.AccountID); err != nil {
		return out, err
	}
	// A reload from the same login replaces its own exact server attempt. A
	// different login cannot cancel it. Exchanging callbacks lose their final
	// stage guard and revoke any tokens issued while this transaction ran.
	if _, err = tx.Exec(ctx, `UPDATE mcp_session_oauth_attempts SET stage='canceled',verifier_ciphertext=NULL,verifier_key_id=NULL,client_ciphertext=NULL,client_key_id=NULL
 WHERE account_id=$1 AND sid=$2 AND draft->'authorization'->'binding'->>'runner_installation_id'=$3
 AND draft->'authorization'->'binding'->>'runner_session_id'=$4 AND draft->'authorization'->'definition'->>'id'=$5
 AND stage IN ('preview','starting','pending','exchanging')`, in.AccountID, in.HumanSessionID, authorization.Binding.RunnerInstallationID, in.RunnerSessionID, in.DefinitionID); err != nil {
		return out, err
	}
	var ownerCount, total int
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE account_id=$1),count(*) FROM mcp_session_oauth_attempts WHERE stage IN ('preview','starting','pending','exchanging')`, in.AccountID).Scan(&ownerCount, &total); err != nil {
		return out, err
	}
	if ownerCount >= maxPendingStates || total >= 10000 {
		return out, ErrLimit
	}
	expires := minTime(s.oauthTime().Add(OAuthStateTTL), authorization.Binding.HumanDeadline)
	err = tx.QueryRow(ctx, `INSERT INTO mcp_session_oauth_attempts(account_id,sid,draft,expires_at) VALUES($1,$2,$3,$4) RETURNING id::text`, in.AccountID, in.HumanSessionID, raw, expires).Scan(&out.DraftID)
	if isUnique(err) {
		return out, invalid("a sign-in for this session and server is already pending")
	}
	if err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	resourceURL, _ := url.Parse(authorization.Definition.URL)
	issuerURL, _ := url.Parse(server.Issuer)
	providerURL, _ := url.Parse(server.AuthorizationEndpoint)
	out.RunnerSessionID = authorization.Binding.RunnerSessionID
	out.Name = authorization.Definition.Name
	out.Source = authorization.Definition.Source
	out.ResourceHost = resourceURL.Host
	out.IssuerHost = issuerURL.Host
	out.ProviderHost = providerURL.Host
	out.Scopes = strings.Fields(scope)
	if out.Scopes == nil {
		out.Scopes = []string{}
	}
	return out, nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *Service) failSessionAttempt(ctx context.Context, id string) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := s.st.Pool().Exec(cleanup, `UPDATE mcp_session_oauth_attempts SET stage='failed',verifier_ciphertext=NULL,verifier_key_id=NULL,client_ciphertext=NULL,client_key_id=NULL WHERE id=$1 AND stage IN ('starting','exchanging')`, id)
	logSessionFailure("oauth_failure_cleanup", err)
}
func (s *Service) StartSessionOAuth(ctx context.Context, account, sid, id, redirect string) (out OAuthStartResult, err error) {
	defer func() { logSessionFailure("oauth_start", err) }()
	if s.sessionAuthority == nil {
		return out, errSessionOAuthAuthority
	}
	if !uuidRe.MatchString(account) || !uuidRe.MatchString(sid) || !uuidRe.MatchString(id) {
		return out, ErrNotFound
	}
	var raw []byte
	err = s.st.Pool().QueryRow(ctx, `UPDATE mcp_session_oauth_attempts SET stage='starting',expires_at=least(clock_timestamp()+interval '10 minutes',(draft->'authorization'->'binding'->>'human_deadline')::timestamptz) WHERE id=$1 AND account_id=$2 AND sid=$3 AND stage='preview' AND expires_at>clock_timestamp() RETURNING draft`, id, account, sid).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	defer func() {
		if err != nil {
			s.failSessionAttempt(ctx, id)
		}
	}()
	var draft sessionOAuthDraft
	if err = json.Unmarshal(raw, &draft); err != nil {
		return out, ErrUnavailable
	}
	if draft.RedirectURI != redirect {
		return out, refuse("redirect_uri", nil)
	}
	if err = s.sessionAuthority.Validate(ctx, draft.Authorization); err != nil {
		return out, err
	}
	if err = s.liveSessionDraft(ctx, draft); err != nil {
		return out, err
	}
	var client registeredClient
	if draft.Authorization.Definition.ClientID != "" {
		if !slices.Contains(effectiveAuthMethods(draft.Server.TokenAuthMethods), "none") {
			return out, invalid("this server requires client registration")
		}
		client = registeredClient{id: draft.Authorization.Definition.ClientID, method: "none"}
	} else {
		if draft.Server.RegistrationEndpoint == "" {
			return out, invalid("this server requires a registered client ID")
		}
		client, err = s.register(ctx, draft.Server, redirect, draft.Scopes)
		if err != nil {
			return out, err
		}
	}
	state, err := randomToken()
	if err != nil {
		return out, err
	}
	state = SessionOAuthStatePrefix + state
	verifier, err := randomToken()
	if err != nil {
		return out, err
	}
	verifierCT, verifierKey, err := s.backend.Encrypt(ctx, []byte(verifier))
	if err != nil {
		return out, err
	}
	bundle := tokenBundle{ClientID: client.id, ClientSecret: client.secret, AuthMethod: client.method, ClientSecretExpiresAt: client.secretExpiresAt, TokenEndpoint: draft.Server.TokenEndpoint, RevocationEndpoint: draft.Server.RevocationEndpoint, Resource: draft.Authorization.Definition.URL}
	clientCT, clientKey, err := s.sealBundle(ctx, bundle)
	if err != nil {
		return out, err
	}
	if err = s.sessionAuthority.Validate(ctx, draft.Authorization); err != nil {
		return out, err
	}
	if err = s.liveSessionDraft(ctx, draft); err != nil {
		return out, err
	}
	target, _ := url.Parse(draft.Server.AuthorizationEndpoint)
	q := target.Query()
	q.Set("response_type", "code")
	q.Set("client_id", client.id)
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	q.Set("code_challenge", pkceChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	q.Set("resource", bundle.Resource)
	if draft.Scopes != "" {
		q.Set("scope", draft.Scopes)
	}
	target.RawQuery = q.Encode()
	if err = validateSessionAuthorizationURL(target.String(), draft, client.id, state, verifier); err != nil {
		return out, err
	}
	h := stateHash(state)
	tag, err := s.st.Pool().Exec(ctx, `UPDATE mcp_session_oauth_attempts SET stage='pending',expires_at=least(clock_timestamp()+interval '10 minutes',(draft->'authorization'->'binding'->>'human_deadline')::timestamptz),state_hash=$2,verifier_ciphertext=$3,verifier_key_id=$4,client_ciphertext=$5,client_key_id=$6 WHERE id=$1 AND stage='starting' AND expires_at>clock_timestamp()`, id, h[:], verifierCT, verifierKey, clientCT, clientKey)
	if err != nil {
		return out, err
	}
	if tag.RowsAffected() != 1 {
		return out, ErrNotFound
	}
	return OAuthStartResult{AuthorizationURL: target.String(), AuthorizationServer: target.Host, StateCookie: fmt.Sprintf("%x", h[:])}, nil
}
func (s *Service) liveSessionDraft(ctx context.Context, draft sessionOAuthDraft) error {
	var live bool
	err := s.st.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM human_sessions hs JOIN accounts a ON a.id=hs.account_id WHERE hs.account_id=$1 AND hs.chain_id=$2 AND hs.revoked_at IS NULL AND hs.expires_at>clock_timestamp() AND a.disabled_at IS NULL AND NOT a.must_change_password)`, draft.Authorization.Binding.AccountID, draft.Authorization.Binding.HumanSessionID).Scan(&live)
	if err != nil {
		return err
	}
	if !live || !draft.Authorization.Binding.HumanDeadline.After(s.oauthTime()) {
		return ErrNotFound
	}
	return nil
}

func (s *Service) CompleteSessionOAuth(ctx context.Context, in OAuthCallbackInput) (out SessionOAuthCompletion, err error) {
	defer func() { logSessionFailure("oauth_callback", err) }()
	if s.sessionAuthority == nil {
		return out, errSessionOAuthAuthority
	}
	if !IsSessionOAuthState(in.State) || in.SessionLive == nil {
		return out, refuse("state", nil)
	}
	h := stateHash(in.State)
	var id string
	var raw, verifierCT, clientCT []byte
	var verifierKey, clientKey string
	err = s.st.Pool().QueryRow(ctx, `WITH old AS (SELECT * FROM mcp_session_oauth_attempts WHERE state_hash=$1 AND stage='pending' AND expires_at>clock_timestamp() FOR UPDATE), claimed AS (UPDATE mcp_session_oauth_attempts a SET stage='exchanging',verifier_ciphertext=NULL,verifier_key_id=NULL,client_ciphertext=NULL,client_key_id=NULL FROM old WHERE a.id=old.id RETURNING old.id::text,old.draft,old.verifier_ciphertext,old.verifier_key_id,old.client_ciphertext,old.client_key_id) SELECT * FROM claimed`, h[:]).Scan(&id, &raw, &verifierCT, &verifierKey, &clientCT, &clientKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, refuse("state", nil)
	}
	if err != nil {
		return out, refuse("internal", err)
	}
	defer func() {
		if err != nil {
			s.failSessionAttempt(ctx, id)
		}
	}()
	var draft sessionOAuthDraft
	if err = json.Unmarshal(raw, &draft); err != nil {
		return out, refuse("internal", err)
	}
	if in.RedirectURI != draft.RedirectURI {
		return out, refuse("redirect_uri", nil)
	}
	if (in.IssPresent && in.Iss != draft.Server.Issuer) || (!in.IssPresent && draft.Server.IssParamSupported) {
		return out, refuse("issuer", nil)
	}
	if in.ProviderError {
		return out, refuse("denied", nil)
	}
	if in.Code == "" || len(in.Code) > 8192 {
		return out, refuse("exchange", nil)
	}
	if err = s.sessionAuthority.Validate(ctx, draft.Authorization); err != nil {
		return out, refuse("session", err)
	}
	live, err := in.SessionLive(ctx, draft.Authorization.Binding.AccountID, draft.Authorization.Binding.HumanSessionID)
	if err != nil {
		return out, refuse("internal", err)
	}
	if !live {
		return out, refuse("session", nil)
	}
	verifier, err := s.backend.Decrypt(ctx, verifierCT, verifierKey)
	if err != nil {
		return out, refuse("internal", err)
	}
	bundle, err := s.openBundle(ctx, clientCT, &clientKey)
	if err != nil {
		return out, refuse("internal", err)
	}
	tokens, err := s.tokenRequest(ctx, bundle, url.Values{"grant_type": {"authorization_code"}, "code": {in.Code}, "redirect_uri": {draft.RedirectURI}, "code_verifier": {string(verifier)}, "resource": {bundle.Resource}})
	if err != nil {
		return out, refuse("exchange", err)
	}
	bundle.AccessToken, bundle.RefreshToken, bundle.ExpiresAt = tokens.accessToken, tokens.refreshToken, tokens.expiresAt
	stored := false
	defer func() {
		if !stored {
			s.revokeUpstream(ctx, bundle)
		}
	}()
	effective := draft.Scopes
	if len(tokens.scopeRaw) > 0 {
		var returned string
		if json.Unmarshal(tokens.scopeRaw, &returned) != nil || string(tokens.scopeRaw) == "null" {
			return out, refuse("exchange", invalid("provider returned invalid scopes"))
		}
		effective, err = sessionScopes(returned)
		if err != nil {
			return out, refuse("exchange", err)
		}
		requested := strings.Fields(draft.Scopes)
		for _, scope := range strings.Fields(effective) {
			if !slices.Contains(requested, scope) {
				return out, refuse("exchange", invalid("provider expanded the reviewed scopes"))
			}
		}
	}
	if err = s.sessionAuthority.Validate(ctx, draft.Authorization); err != nil {
		return out, refuse("session", err)
	}
	live, err = in.SessionLive(ctx, draft.Authorization.Binding.AccountID, draft.Authorization.Binding.HumanSessionID)
	if err != nil {
		return out, refuse("internal", err)
	}
	if !live {
		return out, refuse("session", nil)
	}
	auth := draft.Authorization
	credential, err := s.storeSessionOAuth(ctx, auth.Binding, SessionOAuthCredential{Authorization: &auth, Name: auth.Definition.Name, URL: auth.Definition.URL, Issuer: draft.Server.Issuer, TokenEndpoint: bundle.TokenEndpoint, RevocationEndpoint: bundle.RevocationEndpoint, ClientID: bundle.ClientID, AuthMethod: bundle.AuthMethod, ClientSecret: bundle.ClientSecret, ClientSecretExpiresAt: bundle.ClientSecretExpiresAt, AccessToken: bundle.AccessToken, RefreshToken: bundle.RefreshToken, ExpiresAt: bundle.ExpiresAt, Scopes: effective}, func(tx pgx.Tx, connection string) error {
		var proof string
		// Hold the live proof until the encrypted insert commits, so logout racing
		// the post-exchange check cannot leave a credential behind.
		if err := tx.QueryRow(ctx, `SELECT hs.id::text FROM human_sessions hs JOIN accounts a ON a.id=hs.account_id WHERE hs.account_id=$1 AND hs.chain_id=$2 AND hs.revoked_at IS NULL AND hs.expires_at>clock_timestamp() AND a.disabled_at IS NULL AND NOT a.must_change_password FOR SHARE OF hs,a`, auth.Binding.AccountID, auth.Binding.HumanSessionID).Scan(&proof); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE mcp_session_oauth_attempts SET stage='completed',connection_id=$2 WHERE id=$1 AND account_id=$3 AND sid=$4 AND stage='exchanging' AND expires_at>clock_timestamp()`, id, connection, auth.Binding.AccountID, auth.Binding.HumanSessionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return out, refuse("gone", err)
	}
	stored = true
	out.SessionCredential = credential
	out.AttemptID = id
	return out, nil
}
func (s *Service) CancelSessionOAuth(ctx context.Context, account, sid, id string) (err error) {
	defer func() { logSessionFailure("oauth_cancel", err) }()
	if s.sessionAuthority == nil {
		return errSessionOAuthAuthority
	}
	if !uuidRe.MatchString(account) || !uuidRe.MatchString(sid) || !uuidRe.MatchString(id) {
		return ErrNotFound
	}
	tag, err := s.st.Pool().Exec(ctx, `UPDATE mcp_session_oauth_attempts SET stage='canceled',verifier_ciphertext=NULL,verifier_key_id=NULL,client_ciphertext=NULL,client_key_id=NULL WHERE id=$1 AND account_id=$2 AND sid=$3 AND stage IN ('preview','starting','pending','exchanging','canceled')`, id, account, sid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) SessionOAuthStatus(ctx context.Context, account, sid, id string) (out SessionOAuthAttemptStatus, err error) {
	defer func() { logSessionFailure("oauth_status", err) }()
	if s.sessionAuthority == nil {
		return out, errSessionOAuthAuthority
	}
	if !uuidRe.MatchString(account) || !uuidRe.MatchString(sid) || !uuidRe.MatchString(id) {
		return out, ErrNotFound
	}
	var raw []byte
	err = s.st.Pool().QueryRow(ctx, `SELECT a.stage,a.draft FROM mcp_session_oauth_attempts a WHERE a.id=$1 AND a.account_id=$2 AND a.sid=$3 AND a.expires_at>clock_timestamp() AND (a.stage<>'completed' OR EXISTS(SELECT 1 FROM mcp_connections c WHERE c.id=a.connection_id AND c.account_id=a.account_id AND c.bound_sid=a.sid AND c.runner_session_id IS NOT NULL AND c.lifecycle IN ('active','suspended') AND c.secret_ciphertext IS NOT NULL AND c.status='ok'))`, id, account, sid).Scan(&out.Stage, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	var draft sessionOAuthDraft
	if err = json.Unmarshal(raw, &draft); err != nil {
		return out, ErrUnavailable
	}
	if err = s.sessionAuthority.Validate(ctx, draft.Authorization); err != nil {
		return out, err
	}
	if err = s.liveSessionDraft(ctx, draft); err != nil {
		return out, err
	}
	resource, _ := url.Parse(draft.Authorization.Definition.URL)
	issuer, _ := url.Parse(draft.Server.Issuer)
	provider, _ := url.Parse(draft.Server.AuthorizationEndpoint)
	out.Preview = SessionOAuthPreview{DraftID: id, RunnerSessionID: draft.Authorization.Binding.RunnerSessionID, Name: draft.Authorization.Definition.Name, Source: draft.Authorization.Definition.Source, ResourceHost: resource.Host, IssuerHost: issuer.Host, ProviderHost: provider.Host, Scopes: strings.Fields(draft.Scopes)}
	if out.Preview.Scopes == nil {
		out.Preview.Scopes = []string{}
	}
	return out, nil
}

func validateSessionAuthorizationURL(raw string, draft sessionOAuthDraft, clientID, state, verifier string) error {
	target, err := sessionHTTPS(raw)
	if err != nil {
		return err
	}
	frozen, err := sessionHTTPS(draft.Server.AuthorizationEndpoint)
	if err != nil {
		return err
	}
	if target.Scheme != frozen.Scheme || target.Host != frozen.Host || target.EscapedPath() != frozen.EscapedPath() {
		return invalid("authorization address differs from the reviewed provider")
	}
	expected, err := url.ParseQuery(frozen.RawQuery)
	if err != nil {
		return invalid("reviewed authorization parameters are not valid")
	}
	for key, value := range map[string]string{"response_type": "code", "client_id": clientID, "redirect_uri": draft.RedirectURI, "state": state, "code_challenge": pkceChallenge(verifier), "code_challenge_method": "S256", "resource": draft.Authorization.Definition.URL} {
		expected.Set(key, value)
	}
	if draft.Scopes != "" {
		expected.Set("scope", draft.Scopes)
	}
	actual, err := url.ParseQuery(target.RawQuery)
	if err != nil || actual.Encode() != expected.Encode() {
		return invalid("authorization parameters differ from the reviewed request")
	}
	return nil
}
