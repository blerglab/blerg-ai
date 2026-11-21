package mcpconn

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/url"
	"slices"
	"strings"
)

// A lease that might have spent a rotating token is never reclaimed. Suspension
// advances the generation and starts bounded retention; a new human sign-in is
// required. Closing/deleted or newer generations are never changed here.
func (s *Service) suspendSessionRefresh(ctx context.Context, in SessionDispatchRequest) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	_, err := s.st.Pool().Exec(cleanup, `UPDATE mcp_connections SET lifecycle='suspended',status='needs_auth',
 credential_generation=credential_generation+1,refresh_fence=refresh_fence+1,
 suspended_at=statement_timestamp(),cleanup_after=statement_timestamp()+interval '168 hours',
 binding_lease_until=NULL,session_refresh_lease_until=NULL,updated_at=now()
 WHERE `+sessionWhere+` AND credential_generation=$5 AND bound_sid=$6 AND lifecycle='active'`, in.ConnectionID, in.AccountID, in.RunnerInstallationID, in.RunnerSessionID, in.CredentialGeneration, in.HumanSessionID)
	return err
}
func (s *Service) refreshSessionOnce(ctx context.Context, in SessionDispatchRequest) (SecretResult, error) {
	if err := ctx.Err(); err != nil {
		return SecretResult{}, err
	}
	raw, _ := json.Marshal(in)
	hash := stateHash(string(raw))
	key := "scoped/" + hex.EncodeToString(hash[:])
	s.flightMu.Lock()
	f, running := s.flights[key]
	if !running {
		f = &refreshFlight{done: make(chan struct{})}
		s.flights[key] = f
		go func() {
			work, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshOpTimeout)
			defer cancel()
			f.res, f.err = s.doSessionRefresh(work, in)
			s.flightMu.Lock()
			delete(s.flights, key)
			s.flightMu.Unlock()
			close(f.done)
		}()
	}
	s.flightMu.Unlock()
	select {
	case <-f.done:
		return f.res, f.err
	case <-ctx.Done():
		return SecretResult{}, ctx.Err()
	}
}
func (s *Service) doSessionRefresh(ctx context.Context, in SessionDispatchRequest) (out SecretResult, err error) {
	defer func() { logSessionFailure("refresh", err) }()
	row, err := s.admittedSessionRow(ctx, in, true)
	if err != nil {
		return out, err
	}
	old, err := s.openBundle(ctx, row.ct, row.key)
	if err != nil {
		return out, err
	}
	result := func(bundle tokenBundle) SecretResult {
		expiry := bundle.ExpiresAt
		return SecretResult{URL: row.url, HeaderName: defaultHeaderName, Value: "Bearer " + bundle.AccessToken, ExpiresAt: &expiry, PinPolicy: PinPolicyStrict}
	}
	if old.ExpiresAt.Sub(s.oauthTime()) > refreshWindow {
		return result(old), nil
	}
	if old.RefreshToken == "" {
		if old.ExpiresAt.After(s.oauthTime()) {
			return result(old), nil
		}
		if cleanupErr := s.suspendSessionRefresh(ctx, in); cleanupErr != nil {
			logSessionFailure("refresh_missing_cleanup", cleanupErr)
		}
		return out, errSessionRefreshUncertain
	}
	args := []any{in.ConnectionID, in.AccountID, in.RunnerInstallationID, in.RunnerSessionID, in.HumanSessionID, in.CredentialGeneration, in.ActorSessionID, row.ct}
	var fence int64
	err = s.st.Pool().QueryRow(ctx, `WITH eligible AS (SELECT c.id`+sessionDispatchFrom+`
 AND c.secret_ciphertext=$8 AND c.session_refresh_lease_until IS NULL FOR UPDATE OF c)
 UPDATE mcp_connections c SET refresh_fence=c.refresh_fence+1,session_refresh_lease_until=clock_timestamp()+interval '60 seconds'
 FROM eligible WHERE c.id=eligible.id AND c.runner_session_id=$4 RETURNING c.refresh_fence`, args...).Scan(&fence)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrSessionRefreshBusy
	}
	if err != nil {
		return out, err
	}
	tokens, err := s.tokenRequest(ctx, old, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {old.RefreshToken}, "resource": {old.Resource}})
	if err != nil {
		logSessionFailure("refresh_request", err)
		var failure *tokenError
		if errors.As(err, &failure) && failure.notSent {
			released, releaseErr := s.releaseUnspentSessionRefresh(ctx, in, row.ct, fence)
			if releaseErr != nil {
				logSessionFailure("refresh_no_send_release", releaseErr)
				return out, errSessionRefreshNotSent
			}
			if !released {
				logSessionFailure("refresh_no_send_superseded", ErrNotFound)
				return out, ErrNotFound
			}
			return out, errSessionRefreshNotSent
		}
		if errors.As(err, &failure) && failure.rejected {
			if cleanupErr := s.suspendSessionRefresh(ctx, in); cleanupErr != nil {
				logSessionFailure("refresh_rejected_cleanup", cleanupErr)
			}
			return out, errSessionRefreshRejected
		}
		if cleanupErr := s.suspendSessionRefresh(ctx, in); cleanupErr != nil {
			logSessionFailure("refresh_uncertain_cleanup", cleanupErr)
		}
		return out, errSessionRefreshUncertain
	}
	next := old
	next.AccessToken = tokens.accessToken
	next.ExpiresAt = tokens.expiresAt
	if tokens.refreshToken != "" {
		next.RefreshToken = tokens.refreshToken
	}
	stored := false
	defer func() {
		if !stored {
			s.revokeUpstream(ctx, next)
			if cleanupErr := s.suspendSessionRefresh(ctx, in); cleanupErr != nil {
				logSessionFailure("refresh_lost_cleanup", cleanupErr)
			}
		}
	}()
	meta := row.meta
	if len(tokens.scopeRaw) > 0 {
		var returned string
		if json.Unmarshal(tokens.scopeRaw, &returned) != nil || string(tokens.scopeRaw) == "null" {
			return out, errSessionRefreshUncertain
		}
		scope, scopeErr := sessionScopes(returned)
		if scopeErr != nil {
			logSessionFailure("refresh_scope", scopeErr)
			return out, errSessionRefreshUncertain
		}
		for _, value := range strings.Fields(scope) {
			if !slices.Contains(strings.Fields(meta.Scope), value) {
				return out, errSessionRefreshUncertain
			}
		}
		meta.Scope = scope
	}
	// Persist on a detached, bounded context: a canceled caller cannot discard a
	// successful rotation. The final CAS still requires the live original login,
	// binding lease, generation, claim fence, old ciphertext and unexpired lease.
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	ciphertext, keyID, err := s.sealBundle(persist, next)
	if err != nil {
		return out, err
	}
	metadata, err := json.Marshal(meta)
	if err != nil {
		return out, err
	}
	// An approver may leave while the original bound session stays authorized.
	// Preserve the rotated credential for that original session; final response
	// and actual dispatch separately recheck the approver's live login.
	args[6] = ""
	args = append(args, fence, ciphertext, keyID, metadata)
	tag, err := s.st.Pool().Exec(persist, `WITH eligible AS (SELECT c.id`+sessionDispatchFrom+`
 AND c.secret_ciphertext=$8 AND c.refresh_fence=$9 AND c.session_refresh_lease_until>clock_timestamp() FOR UPDATE OF c)
 UPDATE mcp_connections c SET secret_ciphertext=$10,key_id=$11,oauth_meta=$12,session_refresh_lease_until=NULL,
 last_verified_at=now(),updated_at=now() FROM eligible WHERE c.id=eligible.id AND c.runner_session_id=$4`, args...)
	if err != nil {
		return out, err
	}
	if tag.RowsAffected() != 1 {
		return out, errSessionRefreshUncertain
	}
	stored = true
	return result(next), nil
}

// Release only this still-live, unspent claim; never clear a newer or expired lease.
func (s *Service) releaseUnspentSessionRefresh(ctx context.Context, in SessionDispatchRequest, ct []byte, fence int64) (bool, error) {
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	tag, err := s.st.Pool().Exec(persist, `UPDATE mcp_connections SET session_refresh_lease_until=NULL
 WHERE `+sessionWhere+` AND credential_generation=$5 AND bound_sid=$6 AND refresh_fence=$7
 AND secret_ciphertext=$8 AND lifecycle='active' AND session_refresh_lease_until>clock_timestamp()`, in.ConnectionID, in.AccountID, in.RunnerInstallationID, in.RunnerSessionID, in.CredentialGeneration, in.HumanSessionID, fence, ct)
	return tag.RowsAffected() == 1, err
}
