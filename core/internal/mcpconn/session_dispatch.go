package mcpconn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

// SessionDispatchRequest is supplied only by the trusted gateway control plane.
// GrantTokenHash identifies the exact issued grant, never a reusable gateway token.
type SessionDispatchRequest struct {
	SessionScope
	ConnectionID         string `json:"connection_id"`
	HumanSessionID       string `json:"human_session_id"`
	LaunchGeneration     int64  `json:"launch_generation"`
	CredentialGeneration int64  `json:"credential_generation"`
	GrantTokenHash       string `json:"grant_token_hash"`
	ActorSessionID       string `json:"actor_session_id,omitempty"`
}

type SessionDispatchAuthority interface {
	ValidateSessionDispatch(context.Context, SessionDispatchRequest, SessionOAuthAuthorization) error
}

const sessionDispatchAuthorityTTL = time.Second

var ErrSessionRefreshBusy = fmt.Errorf("%w: scoped refresh in progress", ErrUnavailable)
var errSessionRefreshAbandoned = fmt.Errorf("%w: scoped refresh lease abandoned", ErrNotFound)
var errSessionRefreshNotSent = fmt.Errorf("%w: scoped refresh not sent", ErrUnavailable)
var errSessionRefreshRejected = fmt.Errorf("%w: scoped refresh rejected", ErrNotFound)
var errSessionRefreshUncertain = fmt.Errorf("%w: scoped refresh uncertain", ErrNotFound)

// The exact connection primary key and scope are read on EVERY dispatch. The
// original human chain and optional approver chain remain live in Core; no cache
// stands in for these checks. Only the Runner authority answer is briefly cached.
const sessionDispatchFrom = ` FROM mcp_connections c
 WHERE c.id=$1 AND c.account_id=$2 AND c.runner_installation_id=$3 AND c.runner_session_id=$4
 AND c.bound_sid=$5 AND c.credential_generation=$6 AND c.lifecycle='active' AND c.status='ok'
 AND c.secret_ciphertext IS NOT NULL AND c.human_deadline>clock_timestamp() AND c.binding_lease_until>clock_timestamp()
 AND EXISTS(SELECT 1 FROM human_sessions hs JOIN accounts a ON a.id=hs.account_id
  WHERE hs.account_id=c.account_id AND hs.chain_id=c.bound_sid AND hs.revoked_at IS NULL
  AND hs.expires_at>clock_timestamp() AND a.disabled_at IS NULL AND NOT a.must_change_password)
 AND ($7::text='' OR EXISTS(SELECT 1 FROM human_sessions actor WHERE actor.account_id=c.account_id
  AND actor.chain_id=nullif($7,'')::uuid AND actor.revoked_at IS NULL AND actor.expires_at>clock_timestamp()))`

type sessionDispatchRow struct {
	meta           oauthMeta
	url            string
	ct             []byte
	key            *string
	refreshing     bool
	refreshExpired bool
}

func validSessionDispatch(in SessionDispatchRequest) bool {
	hash, err := hex.DecodeString(in.GrantTokenHash)
	return validSessionScope(in.SessionScope) && uuidRe.MatchString(in.ConnectionID) && uuidRe.MatchString(in.HumanSessionID) && in.LaunchGeneration > 0 && in.CredentialGeneration > 0 && err == nil && len(hash) == sha256.Size && (in.ActorSessionID == "" || uuidRe.MatchString(in.ActorSessionID))
}
func sessionDispatchKey(in SessionDispatchRequest, auth SessionOAuthAuthorization) [32]byte {
	raw, _ := json.Marshal(struct {
		Dispatch      SessionDispatchRequest
		Authorization SessionOAuthAuthorization
	}{in, auth})
	return sha256.Sum256(raw)
}
func (s *Service) loadSessionDispatch(ctx context.Context, in SessionDispatchRequest, secret bool) (row sessionDispatchRow, err error) {
	if !validSessionDispatch(in) {
		return row, ErrNotFound
	}
	var raw []byte
	args := []any{in.ConnectionID, in.AccountID, in.RunnerInstallationID, in.RunnerSessionID, in.HumanSessionID, in.CredentialGeneration, in.ActorSessionID}
	if secret {
		err = s.st.Pool().QueryRow(ctx, `SELECT c.oauth_meta,c.url,c.secret_ciphertext,c.key_id,c.session_refresh_lease_until IS NOT NULL,coalesce(c.session_refresh_lease_until<=clock_timestamp(),false)`+sessionDispatchFrom, args...).Scan(&raw, &row.url, &row.ct, &row.key, &row.refreshing, &row.refreshExpired)
	} else {
		err = s.st.Pool().QueryRow(ctx, `SELECT c.oauth_meta,c.session_refresh_lease_until IS NOT NULL,coalesce(c.session_refresh_lease_until<=clock_timestamp(),false)`+sessionDispatchFrom, args...).Scan(&raw, &row.refreshing, &row.refreshExpired)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return row, ErrNotFound
	}
	if err != nil {
		return row, err
	}
	if err = json.Unmarshal(raw, &row.meta); err != nil {
		return row, err
	}
	auth := row.meta.SessionAuthorization
	if auth == nil || auth.Binding.SessionScope != in.SessionScope || auth.Binding.HumanSessionID != in.HumanSessionID || auth.LaunchGeneration != in.LaunchGeneration || auth.Definition.Version <= 0 || auth.Definition.ID == "" {
		return row, ErrNotFound
	}
	return row, nil
}
func (s *Service) sessionDispatchAuthority(ctx context.Context, in SessionDispatchRequest, auth SessionOAuthAuthorization) (network bool, err error) {
	authority, ok := s.sessionAuthority.(SessionDispatchAuthority)
	if !ok || authority == nil {
		return false, ErrUnavailable
	}
	key := sessionDispatchKey(in, auth)
	s.sessionDispatchMu.Lock()
	deadline, hit := s.sessionDispatchCache[key]
	s.sessionDispatchMu.Unlock()
	if hit && s.oauthTime().Before(deadline) {
		return false, nil
	}
	if err = authority.ValidateSessionDispatch(ctx, in, auth); err != nil {
		return true, err
	}
	s.sessionDispatchMu.Lock()
	if s.sessionDispatchCache == nil || len(s.sessionDispatchCache) >= 1024 {
		s.sessionDispatchCache = make(map[[32]byte]time.Time)
	}
	s.sessionDispatchCache[key] = s.oauthTime().Add(sessionDispatchAuthorityTTL)
	s.sessionDispatchMu.Unlock()
	return true, nil
}
func (s *Service) admittedSessionRow(ctx context.Context, in SessionDispatchRequest, secret bool) (row sessionDispatchRow, err error) {
	if s.sessionAuthority == nil {
		return row, ErrUnavailable
	}
	row, err = s.loadSessionDispatch(ctx, in, secret)
	if err != nil {
		return row, err
	}
	if row.refreshExpired {
		if cleanupErr := s.suspendSessionRefresh(ctx, in); cleanupErr != nil {
			logSessionFailure("refresh_abandoned_cleanup", cleanupErr)
		}
		return row, errSessionRefreshAbandoned
	}
	if row.refreshing {
		return row, ErrSessionRefreshBusy
	}
	before := sessionDispatchKey(in, *row.meta.SessionAuthorization)
	network, err := s.sessionDispatchAuthority(ctx, in, *row.meta.SessionAuthorization)
	if err != nil {
		return row, err
	}
	if network {
		// Never let a slow external authority hide a logout, detach or changed
		// generation that committed while its response was in flight.
		row, err = s.loadSessionDispatch(ctx, in, secret)
		if err != nil {
			return row, err
		}
		if row.refreshExpired {
			if cleanupErr := s.suspendSessionRefresh(ctx, in); cleanupErr != nil {
				logSessionFailure("refresh_abandoned_cleanup", cleanupErr)
			}
			return row, errSessionRefreshAbandoned
		}
		if row.refreshing {
			return row, ErrSessionRefreshBusy
		}
		if sessionDispatchKey(in, *row.meta.SessionAuthorization) != before {
			return row, ErrNotFound
		}
	}
	return row, nil
}
func (s *Service) CheckSessionDispatch(ctx context.Context, in SessionDispatchRequest) (err error) {
	defer func() { logSessionFailure("dispatch_check", err) }()
	_, err = s.admittedSessionRow(ctx, in, false)
	return err
}
func (s *Service) FetchSessionSecret(ctx context.Context, in SessionDispatchRequest) (out SecretResult, err error) {
	defer func() { logSessionFailure("dispatch_token", err) }()
	row, err := s.admittedSessionRow(ctx, in, true)
	if err != nil {
		return out, err
	}
	grantHash, _ := hex.DecodeString(in.GrantTokenHash)
	if _, err = s.st.Pool().Exec(ctx, `INSERT INTO mcp_secret_access_log
 (account_id,connection_id,fetched_by_session_id,runner_installation_id,runner_session_id,runner_launch_generation,credential_generation,gateway_grant_hash,scoped_actor_sid)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, in.AccountID, in.ConnectionID, in.HumanSessionID, in.RunnerInstallationID, in.RunnerSessionID, in.LaunchGeneration, in.CredentialGeneration, grantHash, nullable(in.ActorSessionID)); err != nil {
		return out, err
	}
	bundle, err := s.openBundle(ctx, row.ct, row.key)
	if err != nil {
		return out, err
	}
	if bundle.ExpiresAt.Sub(s.oauthTime()) <= refreshWindow {
		if bundle.RefreshToken != "" {
			out, err = s.refreshSessionOnce(ctx, in)
			if err != nil {
				return out, err
			}
			if err = s.CheckSessionDispatch(ctx, in); err != nil {
				return SecretResult{}, err
			}
			return out, nil
		}
		if !bundle.ExpiresAt.After(s.oauthTime()) {
			if cleanupErr := s.suspendSessionRefresh(ctx, in); cleanupErr != nil {
				logSessionFailure("refresh_missing_cleanup", cleanupErr)
			}
			return out, errSessionRefreshUncertain
		}
	}

	// A remote key backend may have taken time. This proof is fresh at return,
	// and the gateway must still perform admission again at actual dispatch.
	if err = s.CheckSessionDispatch(ctx, in); err != nil {
		return out, err
	}
	expiry := bundle.ExpiresAt
	return SecretResult{URL: row.url, HeaderName: defaultHeaderName, Value: "Bearer " + bundle.AccessToken, ExpiresAt: &expiry, PinPolicy: PinPolicyStrict}, nil
}
