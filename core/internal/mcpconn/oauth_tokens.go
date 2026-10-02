package mcpconn

import (
	"context"
	"errors"
	"log"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// oauthRow is an OAuth connection as oauthAccessToken needs it, read without any lock.
type oauthRow struct {
	url    string
	ct     []byte // the sealed bundle exactly as stored: the compare-and-set token of a write
	bundle tokenBundle
}

func (r oauthRow) result(b tokenBundle) SecretResult {
	exp := b.ExpiresAt
	return SecretResult{URL: r.url, HeaderName: defaultHeaderName, Value: "Bearer " + b.AccessToken, ExpiresAt: &exp}
}

// loadOAuth reads and opens the account's OAuth connection in one short query. A missing row or
// a status other than ok is ErrNotFound (the uniform 404).
func (s *Service) loadOAuth(ctx context.Context, accountID, id string) (oauthRow, error) {
	var r oauthRow
	var status string
	var keyID *string
	err := s.st.Pool().QueryRow(ctx,
		`SELECT url, status, secret_ciphertext, key_id FROM mcp_connections
		  WHERE id = $1 AND account_id = $2 AND auth_kind = 'oauth'`, id, accountID).Scan(&r.url, &status, &r.ct, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return oauthRow{}, ErrNotFound
	}
	if err != nil {
		return oauthRow{}, err
	}
	if status != "ok" {
		return oauthRow{}, ErrNotFound
	}
	if r.bundle, err = s.openBundle(ctx, r.ct, keyID); err != nil {
		return oauthRow{}, err
	}
	return r, nil
}

// refreshFlight is one in-progress refresh that concurrent callers of the same connection share.
type refreshFlight struct {
	done chan struct{}
	res  SecretResult
	err  error
}

// oauthAccessToken returns a usable access token for an OAuth connection, refreshing it when it
// is within refreshWindow of expiry.
//
// No database connection and no row lock is held while the provider is called (a slow provider
// must not be able to stall the pool): the row is read in one short query, the decision is made,
// concurrent callers of the same connection are collapsed into ONE provider call (the in-process
// singleflight, so the rotating refresh token is spent once), and the rotated bundle is written
// back with a compare-and-set on the ciphertext that was read, in one short statement.
//
// A provider that rejects the grant or the client (400/401 with invalid_grant, invalid_client or
// unauthorized_client) marks the connection needs_auth (ErrNotFound, the uniform 404); every
// other failure is transient: the stored refresh token is kept and the current token is returned
// while it is still unexpired, else ErrUnavailable.
func (s *Service) oauthAccessToken(ctx context.Context, accountID, id string) (SecretResult, error) {
	row, err := s.loadOAuth(ctx, accountID, id)
	if err != nil {
		return SecretResult{}, err
	}
	if time.Until(row.bundle.ExpiresAt) > refreshWindow {
		return row.result(row.bundle), nil
	}
	if row.bundle.RefreshToken == "" {
		if time.Until(row.bundle.ExpiresAt) > 0 {
			return row.result(row.bundle), nil
		}
		return s.markNeedsAuth(ctx, id, row.ct)
	}
	return s.refreshOnce(ctx, accountID, id)
}

// refreshOnce runs doRefresh for the connection, sharing one run between concurrent callers. The
// work is on a context detached from the caller's cancellation: once the provider has rotated the
// refresh token, the new one must be stored even if the requesting runner hung up. A caller that
// hangs up simply stops waiting.
func (s *Service) refreshOnce(ctx context.Context, accountID, id string) (SecretResult, error) {
	key := accountID + "/" + id
	s.flightMu.Lock()
	f, running := s.flights[key]
	if !running {
		f = &refreshFlight{done: make(chan struct{})}
		s.flights[key] = f
		go func() {
			wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshOpTimeout)
			defer cancel()
			f.res, f.err = s.doRefresh(wctx, accountID, id)
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

func (s *Service) doRefresh(ctx context.Context, accountID, id string) (SecretResult, error) {
	// Re-read: a flight that finished between the caller's read and this one may have refreshed.
	row, err := s.loadOAuth(ctx, accountID, id)
	if err != nil {
		return SecretResult{}, err
	}
	b := row.bundle
	if time.Until(b.ExpiresAt) > refreshWindow {
		return row.result(b), nil
	}
	if b.RefreshToken == "" {
		if time.Until(b.ExpiresAt) > 0 {
			return row.result(b), nil
		}
		return s.markNeedsAuth(ctx, id, row.ct)
	}
	tr, err := s.tokenRequest(ctx, b, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {b.RefreshToken}, "resource": {b.Resource},
	})
	if err != nil {
		var te *tokenError
		if errors.As(err, &te) && te.rejected {
			log.Printf("mcpconn: oauth refresh rejected for connection %s (status %d, code %q): needs_auth", id, te.status, te.code)
			return s.markNeedsAuth(ctx, id, row.ct)
		}
		log.Printf("mcpconn: oauth refresh failed for connection %s: provider unreachable or erroring (transient)", id)
		if time.Until(b.ExpiresAt) > 0 {
			return row.result(b), nil
		}
		return SecretResult{}, ErrUnavailable
	}
	nb := b
	nb.AccessToken, nb.ExpiresAt = tr.accessToken, tr.expiresAt
	if tr.refreshToken != "" {
		nb.RefreshToken = tr.refreshToken // rotated; the old one is spent
	}
	stored, err := s.storeRotated(ctx, id, accountID, row.ct, nb)
	switch {
	case err != nil:
		// The provider rotated the refresh token but the new bundle could not be stored. The old
		// refresh token is spent, so the connection will need reconnecting once this access
		// token runs out. Say so, without any token; the new access token is still valid now.
		log.Printf("mcpconn: connection %s: the rotated oauth refresh token could not be stored (%v): the connection will need to be reconnected", id, err)
		return row.result(nb), nil
	case stored:
		return row.result(nb), nil
	}
	// The row was deleted, or its bundle replaced (a reconnect), while the provider was being
	// asked: the tokens just issued belong to no stored connection. Take them back.
	s.revokeUpstream(ctx, nb)
	cur, err := s.loadOAuth(ctx, accountID, id)
	if err != nil {
		return SecretResult{}, err
	}
	if time.Until(cur.bundle.ExpiresAt) > 0 {
		return cur.result(cur.bundle), nil
	}
	return SecretResult{}, ErrUnavailable
}

// storeRotated writes nb over the bundle whose ciphertext is old, only if it is still the stored
// one. stored=false with a nil error means the row is gone or was replaced. It runs on a context
// detached from every deadline so the rotated token is persisted even after a slow provider call,
// and retries briefly: a refresh token lost here cannot be recovered.
func (s *Service) storeRotated(ctx context.Context, id, accountID string, old []byte, nb tokenBundle) (stored bool, err error) {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	for attempt := 0; attempt < persistAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 150 * time.Millisecond)
		}
		var ct []byte
		var keyID string
		if ct, keyID, err = s.sealBundle(pctx, nb); err != nil {
			continue
		}
		var tag pgconn.CommandTag
		if tag, err = s.st.Pool().Exec(pctx,
			`UPDATE mcp_connections SET secret_ciphertext = $1, key_id = $2, last_verified_at = now(), updated_at = now()
			  WHERE id = $3 AND account_id = $4 AND auth_kind = 'oauth' AND secret_ciphertext = $5`,
			ct, keyID, id, accountID, old); err != nil {
			continue
		}
		return tag.RowsAffected() == 1, nil
	}
	return false, err
}

// markNeedsAuth flags the connection as needing a new sign-in, but only if its bundle is still
// the one that was judged bad (a reconnect in between must not be undone). The answer is the
// uniform not-found either way.
func (s *Service) markNeedsAuth(ctx context.Context, id string, ct []byte) (SecretResult, error) {
	if _, err := s.st.Pool().Exec(ctx,
		`UPDATE mcp_connections SET status = 'needs_auth', updated_at = now() WHERE id = $1 AND secret_ciphertext = $2`, id, ct); err != nil {
		return SecretResult{}, err
	}
	return SecretResult{}, ErrNotFound
}

// revokeUpstream asks the provider to revoke a bundle's tokens (RFC 7009), best effort: the
// refresh token first (which normally takes the access token with it), then the access token.
// Failures are logged without any token and never returned.
func (s *Service) revokeUpstream(ctx context.Context, b tokenBundle) {
	if b.RevocationEndpoint == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeOpTimeout)
	defer cancel()
	for _, t := range []struct{ token, hint string }{{b.RefreshToken, "refresh_token"}, {b.AccessToken, "access_token"}} {
		if t.token == "" {
			continue
		}
		if err := s.revokeOne(ctx, b, t.token, t.hint); err != nil {
			log.Printf("mcpconn: upstream revocation (%s) failed: %v", t.hint, err)
		}
	}
}

func (s *Service) revokeOne(ctx context.Context, b tokenBundle, token, hint string) error {
	req, err := newFormRequest(ctx, b.RevocationEndpoint, url.Values{"token": {token}, "token_type_hint": {hint}}, b)
	if err != nil {
		return errors.New("bad endpoint")
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		return errors.New("request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return errors.New("provider answered " + resp.Status)
	}
	return nil
}
