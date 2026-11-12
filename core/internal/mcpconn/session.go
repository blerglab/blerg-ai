package mcpconn

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"

	"github.com/jackc/pgx/v5"
)

// SessionScope is a trusted control-plane binding, not authorization from a
// runtime. Installation is bookkeeping under the shared internal-key boundary.
type SessionScope struct {
	AccountID            string `json:"account_id"`
	RunnerInstallationID string `json:"runner_installation_id"`
	RunnerSessionID      string `json:"runner_session_id"`
}

// SessionBinding records the human proof that a trusted callback must validate.
type SessionBinding struct {
	SessionScope
	HumanSessionID string    `json:"human_session_id"`
	HumanDeadline  time.Time `json:"human_deadline"`
}

// SessionOAuthCredential is accepted only from Core's OAuth exchange. No HTTP
// route accepts it. Secret fields are excluded from accidental JSON encoding.
type SessionOAuthCredential struct {
	Authorization         *SessionOAuthAuthorization `json:"-"`
	Name                  string
	URL                   string
	Issuer                string
	TokenEndpoint         string
	RevocationEndpoint    string
	ClientID              string
	AuthMethod            string
	ClientSecret          string `json:"-"`
	ClientSecretExpiresAt int64
	AccessToken           string `json:"-"`
	RefreshToken          string `json:"-"`
	ExpiresAt             time.Time
	Scopes                string
}

// SessionCredential contains lifecycle metadata, never provider token material.
type SessionCredential struct {
	ID            string
	Generation    int64
	Lifecycle     string
	HumanDeadline time.Time
	SuspendedAt   *time.Time
	CleanupAfter  *time.Time
}

func validSessionScope(scope SessionScope) bool {
	return uuidRe.MatchString(scope.AccountID) && uuidRe.MatchString(scope.RunnerSessionID) &&
		len(scope.RunnerInstallationID) > 0 && len(scope.RunnerInstallationID) <= 128 && !strings.ContainsAny(scope.RunnerInstallationID, "\r\n\x00")
}

func logSessionFailure(stage string, err error) {
	if err != nil {
		cause := FailureCause(err)
		failurelog.Default.Event("mcp_session:"+stage+":"+cause, slog.LevelWarn, "component", "core", "operation", "mcp_session_"+stage, "cause", cause)
	}
}

const sessionCols = `id::text,credential_generation,lifecycle,human_deadline,suspended_at,cleanup_after`
const sessionWhere = `id=$1 AND account_id=$2 AND runner_installation_id=$3 AND runner_session_id=$4`

func scanSession(row pgx.Row) (SessionCredential, error) {
	var c SessionCredential
	err := row.Scan(&c.ID, &c.Generation, &c.Lifecycle, &c.HumanDeadline, &c.SuspendedAt, &c.CleanupAfter)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return c, err
}

// StoreSessionOAuth seals a newly exchanged bundle and stores it suspended. This
// primitive does not validate human authorization: its only production caller is the
// Core callback after all browser, live-login and immutable draft checks succeed.
// No runtime route exposes this method, and it cannot replace existing tokens.
func (s *Service) StoreSessionOAuth(ctx context.Context, b SessionBinding, in SessionOAuthCredential) (SessionCredential, error) {
	return s.storeSessionOAuth(ctx, b, in, nil)
}
func (s *Service) storeSessionOAuth(ctx context.Context, b SessionBinding, in SessionOAuthCredential, finish func(pgx.Tx, string) error) (c SessionCredential, err error) {
	defer func() { logSessionFailure("store", err) }()
	if !validSessionScope(b.SessionScope) || !uuidRe.MatchString(b.HumanSessionID) || !b.HumanDeadline.After(s.oauthTime()) {
		return c, invalid("a complete live session binding is required")
	}
	if err = s.validateName(in.Name); err != nil {
		return c, err
	}
	for _, endpoint := range []string{in.URL, in.Issuer, in.TokenEndpoint} {
		if err = s.validateURL(endpoint); err != nil {
			return c, err
		}
	}
	if in.RevocationEndpoint != "" {
		if err = s.validateURL(in.RevocationEndpoint); err != nil {
			return c, err
		}
	}
	if !clientIDRe.MatchString(in.ClientID) || !in.ExpiresAt.After(s.oauthTime()) {
		return c, invalid("OAuth client and unexpired token are required")
	}
	if err = validateSecret(in.AccessToken); err != nil {
		return c, err
	}
	if in.RefreshToken != "" {
		if err = validateSecret(in.RefreshToken); err != nil {
			return c, err
		}
	}
	authMethod := in.AuthMethod
	if authMethod == "" {
		authMethod = "none"
	}
	switch authMethod {
	case "none":
		if in.ClientSecret != "" || in.ClientSecretExpiresAt != 0 {
			return c, invalid("a public OAuth client cannot have a client secret")
		}
	case "client_secret_basic", "client_secret_post":
		if err = validateSecret(in.ClientSecret); err != nil {
			return c, err
		}
		if in.ClientSecretExpiresAt < 0 || (in.ClientSecretExpiresAt > 0 && in.ClientSecretExpiresAt <= s.oauthTime().Unix()) {
			return c, invalid("OAuth client secret has expired")
		}
	default:
		return c, invalid("unsupported OAuth client authentication method")
	}
	bundle := tokenBundle{AccessToken: in.AccessToken, RefreshToken: in.RefreshToken, ExpiresAt: in.ExpiresAt, TokenEndpoint: in.TokenEndpoint, RevocationEndpoint: in.RevocationEndpoint, ClientID: in.ClientID, ClientSecret: in.ClientSecret, ClientSecretExpiresAt: in.ClientSecretExpiresAt, AuthMethod: authMethod, Resource: in.URL}
	ct, kid, err := s.sealBundle(ctx, bundle)
	if err != nil {
		return c, err
	}
	meta, err := json.Marshal(oauthMeta{Issuer: in.Issuer, Resource: in.URL, ClientID: in.ClientID, TokenEndpoint: in.TokenEndpoint, RevocationEndpoint: in.RevocationEndpoint, AuthMethod: authMethod, Scope: in.Scopes, SessionAuthorization: in.Authorization})
	if err != nil {
		return c, err
	}
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return c, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockAccount(ctx, tx, b.AccountID); err != nil {
		return c, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM mcp_connections WHERE account_id=$1 AND lifecycle IS DISTINCT FROM 'deleted'`, b.AccountID).Scan(&count); err != nil {
		return c, err
	}
	if count >= MaxPerAccount {
		return c, ErrLimit
	}
	now := s.oauthTime()
	c, err = scanSession(tx.QueryRow(ctx, `INSERT INTO mcp_connections
 (account_id,name,url,auth_kind,secret_ciphertext,key_id,oauth_meta,runner_installation_id,runner_session_id,bound_sid,credential_generation,lifecycle,human_deadline,suspended_at,cleanup_after,refresh_fence)
 VALUES ($1,$2,$3,'oauth',$4,$5,$6,$7,$8,$9,1,'suspended',$10,$11,$11::timestamptz+interval '168 hours',0) RETURNING `+sessionCols,
		b.AccountID, in.Name, in.URL, ct, kid, meta, b.RunnerInstallationID, b.RunnerSessionID, b.HumanSessionID, b.HumanDeadline, now))
	if isUnique(err) {
		err = ErrNameTaken
	}
	if isFKViolation(err) {
		err = ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if finish != nil {
		if err = finish(tx, c.ID); err != nil {
			return c, err
		}
	}
	err = tx.Commit(ctx)
	return c, err
}

// GetSessionCredential returns only metadata for the exact trusted session scope.
func (s *Service) GetSessionCredential(ctx context.Context, scope SessionScope, id string) (c SessionCredential, err error) {
	defer func() { logSessionFailure("get", err) }()
	if !validSessionScope(scope) || !uuidRe.MatchString(id) {
		return c, ErrNotFound
	}
	return scanSession(s.st.Pool().QueryRow(ctx, `SELECT `+sessionCols+` FROM mcp_connections WHERE `+sessionWhere, id, scope.AccountID, scope.RunnerInstallationID, scope.RunnerSessionID))
}

// SuspendSessionCredential fences a live generation. Repeated suspension cannot
// extend its retention deadline. A lost acknowledgment may recover the immediately
// following suspended generation, never a newer active attachment. Closing/deleted
// rows cannot be resurrected.
func (s *Service) SuspendSessionCredential(ctx context.Context, scope SessionScope, id string, generation int64) (c SessionCredential, err error) {
	defer func() { logSessionFailure("suspend", err) }()
	if !validSessionScope(scope) || !uuidRe.MatchString(id) || generation <= 0 {
		return c, ErrNotFound
	}
	now := s.oauthTime()
	return scanSession(s.st.Pool().QueryRow(ctx, `UPDATE mcp_connections SET
 credential_generation=credential_generation+CASE WHEN lifecycle='active' THEN 1 ELSE 0 END,
 suspended_at=coalesce(suspended_at,$6),cleanup_after=coalesce(cleanup_after,$6::timestamptz+interval '168 hours'),
 status=CASE WHEN session_refresh_lease_until IS NOT NULL THEN 'needs_auth' ELSE status END,
 lifecycle='suspended',binding_lease_until=NULL,session_refresh_lease_until=NULL,updated_at=now()
 WHERE `+sessionWhere+` AND (credential_generation=$5 OR (lifecycle='suspended' AND credential_generation=$5+1)) AND lifecycle IN ('active','suspended') RETURNING `+sessionCols,
		id, scope.AccountID, scope.RunnerInstallationID, scope.RunnerSessionID, generation, now))
}

// CloseSessionCredential commits a durable closing state before network I/O,
// revokes best effort, then erases the sealed bundle. Retry a crashed closing
// operation using its current generation; stale generations cannot erase a row.
func (s *Service) CloseSessionCredential(ctx context.Context, scope SessionScope, id string, generation int64) (err error) {
	defer func() { logSessionFailure("close", err) }()
	if !validSessionScope(scope) || !uuidRe.MatchString(id) || generation <= 0 {
		return ErrNotFound
	}
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	c, err := scanSession(tx.QueryRow(ctx, `SELECT `+sessionCols+` FROM mcp_connections WHERE `+sessionWhere+` FOR UPDATE`, id, scope.AccountID, scope.RunnerInstallationID, scope.RunnerSessionID))
	if err != nil {
		return err
	}
	if c.Generation != generation {
		if c.Generation > generation && (c.Lifecycle == "closing" || c.Lifecycle == "deleted") {
			return nil
		}
		return ErrNotFound
	}
	if c.Lifecycle == "deleted" {
		return nil
	}
	if c.Lifecycle != "closing" {
		c.Generation++
		if _, err = tx.Exec(ctx, `UPDATE mcp_connections SET lifecycle='closing',credential_generation=$5,cleanup_after=$6,binding_lease_until=NULL,session_refresh_lease_until=NULL,updated_at=now() WHERE `+sessionWhere, id, scope.AccountID, scope.RunnerInstallationID, scope.RunnerSessionID, c.Generation, s.oauthTime()); err != nil {
			return err
		}
	}
	var ct []byte
	var kid *string
	if err = tx.QueryRow(ctx, `SELECT secret_ciphertext,key_id FROM mcp_connections WHERE `+sessionWhere, id, scope.AccountID, scope.RunnerInstallationID, scope.RunnerSessionID).Scan(&ct, &kid); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if bundle, openErr := s.openBundle(ctx, ct, kid); openErr != nil {
		logSessionFailure("close_decrypt", openErr)
	} else {
		s.revokeUpstream(ctx, bundle)
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	_, err = s.st.Pool().Exec(persist, `UPDATE mcp_connections SET secret_ciphertext=NULL,key_id=NULL,lifecycle='deleted',cleanup_after=NULL,binding_lease_until=NULL,updated_at=now() WHERE `+sessionWhere+` AND credential_generation=$5 AND lifecycle IN ('closing','deleted')`, id, scope.AccountID, scope.RunnerInstallationID, scope.RunnerSessionID, c.Generation)
	if err != nil {
		return err
	}
	// Another closer may have erased/pruned the already-authorized terminal row.
	// Bound tombstones per exact session; quotas count only live credentials.
	_, err = s.st.Pool().Exec(persist, `DELETE FROM mcp_connections WHERE id IN
 (SELECT id FROM mcp_connections WHERE account_id=$1 AND runner_installation_id=$2 AND runner_session_id=$3 AND lifecycle='deleted'
  ORDER BY updated_at DESC,id DESC OFFSET 20)`, scope.AccountID, scope.RunnerInstallationID, scope.RunnerSessionID)
	return err
}
