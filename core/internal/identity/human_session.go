package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
)

const humanAccessTokenTTL = 10 * time.Minute

// defaultSessionTTL is used whenever SetSessionTTL has never been called (zero value), and
// matches the migration's own DEFAULT for expires_at (30 days), kept here as the fallback used
// by IssueRefreshToken/rotation for a Service built without an explicit call to SetSessionTTL.
const defaultSessionTTL = 720 * time.Hour

// ErrSessionRevoked is returned by RefreshAccessToken when the raw refresh token doesn't
// match any live (non-revoked, non-expired) human_sessions row.
var ErrSessionRevoked = errors.New("identity: session revoked or unknown")

// ErrSessionReplayed is returned by RefreshAccessToken when the presented raw refresh token
// matches a row that has already been rotated out (revoked_at set AND replaced_by set) — i.e.
// a refresh token that was already exchanged for a newer one is being presented again. This is
// theft-detection: a legitimate client never has a reason to reuse a token it already rotated
// away from, so this is treated as evidence of a stolen/duplicated token and the entire
// rotation chain is revoked.
var ErrSessionReplayed = errors.New("identity: rotated-out refresh token replayed; chain revoked")

// ErrAccountDisabled is returned when the account's disabled_at is set (reconcile removed it
// from the org, or an admin deactivated it). Login and refresh both refuse such accounts.
var ErrAccountDisabled = errors.New("identity: account disabled")

// AccountState is the per-account gating state every mint consults.
type AccountState struct {
	Role               string
	Disabled           bool
	MustChangePassword bool
}

// AccountState reads the current role/disabled/must-change-password state for accountID.
func (s *Service) AccountState(ctx context.Context, accountID string) (AccountState, error) {
	var a AccountState
	var disabledAt *time.Time
	err := s.st.Pool().QueryRow(ctx,
		`SELECT role, disabled_at, must_change_password FROM accounts WHERE id = $1`, accountID).
		Scan(&a.Role, &disabledAt, &a.MustChangePassword)
	if err != nil {
		return a, err
	}
	a.Disabled = disabledAt != nil
	return a, nil
}

// replayGraceWindow is how long after a legitimate rotation a client may still present the
// prior (now rotated-out) refresh token before it's treated as a replay/theft signal. This
// tolerates lost 302 redirects and multi-tab races that legitimately double-refresh close
// together: within the window, as long as the successor session this token was rotated into is
// still live, the caller simply gets a fresh access token minted against that existing
// successor session (no new refresh token, no rows touched, no third token issued) instead of
// the whole chain being killed. Outside the window, or if the successor is no longer live, the
// strict chain-kill still applies.
const replayGraceWindow = 60 * time.Second

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// newRefreshToken generates a fresh 32-byte opaque refresh token and returns both its raw
// (base64url, for the caller to persist as a cookie value) and hashed (SHA-256, the only form
// ever persisted) forms.
func newRefreshToken() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, hashToken(raw), nil
}

// MintHumanAccessToken signs a short-lived Kind:"human" access token for accountID, carrying
// that account's current platform-role capabilities (resolved fresh via AccountState so a role
// change takes effect on the next mint/refresh, not just at next login). Refuses a disabled
// account with ErrAccountDisabled — reconcile or an admin having disabled the account must take
// effect immediately, not just block future logins.
func (s *Service) MintHumanAccessToken(ctx context.Context, accountID, audience string) (string, error) {
	return s.MintHumanAccessTokenForSession(ctx, accountID, audience, "")
}

// MintHumanAccessTokenForSession is MintHumanAccessToken for an access token that comes out of
// a browser session: sessionID (the session's rotation-chain id, stable across refresh
// rotations) is stamped into the `sid` claim so a component that verified the bearer token can
// name the exact session to core when it later needs a live-session proof (the internal
// credential/plugin endpoints; see HumanSessionLive). An empty sessionID mints a token with no
// `sid`, which those endpoints refuse.
func (s *Service) MintHumanAccessTokenForSession(ctx context.Context, accountID, audience, sessionID string) (string, error) {
	a, err := s.AccountState(ctx, accountID)
	if err != nil {
		return "", err
	}
	if a.Disabled {
		return "", ErrAccountDisabled
	}
	caps := PlatformRoleCaps[a.Role]
	if a.MustChangePassword {
		// Server-side enforcement of the bootstrap rotation (R7/I-7): nothing but the
		// change-password endpoint accepts this token, because nothing else asks for
		// password.change. This overrides the role's normal caps entirely — even an admin
		// account with the flag set gets only this one capability until the password is
		// changed.
		caps = []string{"password.change"}
	}
	now := time.Now()
	return s.signer.Sign(cid.Claims{
		Sub: accountID, Aud: audience, Kind: "human",
		Caps: caps, Lineage: accountID, Sid: sessionID,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(humanAccessTokenTTL).Unix(),
	})
}

// HumanSessionLive reports whether sessionID (a `sid` claim value: the session's rotation-chain
// id) names a live browser session OF accountID — some row of that chain is unrevoked and
// unexpired, and the account is not disabled. It returns the id of that live row (for the audit
// log). Not-live, someone else's, unknown and malformed are all the same (false, nil): an
// ordinary outcome, indistinguishable to the caller. Like AgentTokenLive it is the "this
// account's owner still stands behind it" check, not token verification.
func (s *Service) HumanSessionLive(ctx context.Context, accountID, sessionID string) (string, bool, error) {
	if !agentTokenIDShape.MatchString(sessionID) || !agentTokenIDShape.MatchString(accountID) {
		return "", false, nil
	}
	var id string
	err := s.st.Pool().QueryRow(ctx,
		`SELECT hs.id::text FROM human_sessions hs JOIN accounts a ON a.id = hs.account_id
		  WHERE hs.chain_id = $1 AND hs.account_id = $2 AND hs.revoked_at IS NULL
		    AND hs.expires_at > now() AND a.disabled_at IS NULL
		  ORDER BY hs.created_at DESC LIMIT 1`, sessionID, accountID).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return id, true, nil
}

// SetSessionTTL sets how long a newly issued (or rotated) refresh session lives before
// expires_at. Not concurrency-safe against other SetSessionTTL calls — intended to be set once
// at process startup (main.go), before any request-serving goroutine reads it.
func (s *Service) SetSessionTTL(d time.Duration) {
	s.sessionTTLValue = d
}

// sessionTTL returns the configured session TTL, or defaultSessionTTL (720h / 30 days) when
// SetSessionTTL was never called.
func (s *Service) sessionTTL() time.Duration {
	if s.sessionTTLValue <= 0 {
		return defaultSessionTTL
	}
	return s.sessionTTLValue
}

// IssueRefreshToken creates a new human_sessions row (starting a fresh rotation chain) and
// returns the raw opaque token — the caller sets this as a cookie value; only its SHA-256 hash
// is ever persisted. The row's chain_id starts as its own id, and expires_at is set from the
// configured session TTL (sessionTTL()).
func (s *Service) IssueRefreshToken(ctx context.Context, accountID, userAgent, ip string) (string, error) {
	raw, hash, err := newRefreshToken()
	if err != nil {
		return "", err
	}
	ttl := s.sessionTTL()
	_, err = s.st.Pool().Exec(ctx, `
		WITH ins AS (
		  INSERT INTO human_sessions (account_id, token_hash, user_agent, ip, expires_at, chain_id)
		  VALUES ($1, $2, $3, $4, now() + $5::interval, gen_random_uuid()) RETURNING id)
		UPDATE human_sessions SET chain_id = ins.id FROM ins WHERE human_sessions.id = ins.id`,
		accountID, hash, userAgent, ip, ttl.String())
	if err != nil {
		return "", err
	}
	return raw, nil
}

// RefreshAccessToken validates rawRefreshToken against human_sessions and, on success, ROTATES
// the session: the presented token's row is revoked (revoked_at set, replaced_by pointing at
// the new row) and a brand-new refresh token is issued in the same chain, carrying forward the
// original row's user_agent/ip and its (absolute, login-anchored) expires_at, atomically (a
// single DB transaction — a crash between the two writes rolls back entirely, leaving the
// original row live and unrotated rather than producing two simultaneously-live tokens). The new
// access token is minted BEFORE the transaction commits, so a mint failure (e.g. the account's
// role lookup fails) rolls the whole rotation back rather than leaving a rotated session with no
// usable access token. It returns the access token AND the new raw refresh token the caller must
// set as the new cookie value.
//
// If the presented token matches a row that was already rotated out (revoked_at AND replaced_by
// both set), that is either theft (a stolen/duplicated token being replayed) or a legitimate
// double-refresh race (a lost 302, two tabs refreshing close together) — the two are
// indistinguishable from the row alone. Within replayGraceWindow of the original rotation, and
// only if the successor session (replaced_by) is still live, this is treated as the benign case:
// a fresh access token is minted against that existing successor session and returned with an
// empty newRefresh (no new row, no third token). Outside the window, or if the successor is no
// longer live, the whole chain is revoked and ErrSessionReplayed is returned. Any other revoked,
// expired, or unknown token returns ErrSessionRevoked.
func (s *Service) RefreshAccessToken(ctx context.Context, rawRefreshToken, audience string) (access, newRefresh string, err error) {
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after a successful commit is a no-op; on failure the original error is returned

	var id, accountID, chainID string
	var revokedAt, expiresAt *time.Time
	var replacedBy *string
	err = tx.QueryRow(ctx, `SELECT id::text, account_id::text, chain_id::text, revoked_at, expires_at, replaced_by::text
		FROM human_sessions WHERE token_hash = $1 FOR UPDATE`, hashToken(rawRefreshToken)).
		Scan(&id, &accountID, &chainID, &revokedAt, &expiresAt, &replacedBy)
	if err != nil {
		return "", "", ErrSessionRevoked
	}

	if revokedAt != nil && replacedBy != nil {
		if time.Since(*revokedAt) < replayGraceWindow {
			var succRevokedAt, succExpiresAt *time.Time
			if err := tx.QueryRow(ctx, `SELECT revoked_at, expires_at FROM human_sessions WHERE id = $1`, *replacedBy).
				Scan(&succRevokedAt, &succExpiresAt); err != nil {
				return "", "", err
			}
			if succRevokedAt == nil && succExpiresAt != nil && succExpiresAt.After(time.Now()) {
				// Grace window, and the successor this token was rotated into is still
				// live: treat as a benign double-refresh, not theft. No rows are touched
				// (the transaction is rolled back via the defer above) — just mint a
				// fresh access token against the existing successor session.
				access, err = s.MintHumanAccessTokenForSession(ctx, accountID, audience, chainID)
				if err != nil {
					return "", "", err
				}
				return access, "", nil
			}
		}
		// Outside the grace window, or the successor is no longer live: indistinguishable
		// from theft. Commit first (nothing was written yet in this branch — releases the
		// FOR UPDATE row lock) then call RevokeAccountEverywhere: every live session for the
		// account, not just this chain, AND the account's "sub" in the shared revocations
		// table, so already-minted access tokens stop verifying immediately too (controller
		// ruling carried from Task 8 — now that "sub" revocation is reversible via Unrevoke,
		// a subsequent successful login clears it, so this is no longer a permanent lockout).
		if err := tx.Commit(ctx); err != nil {
			return "", "", err
		}
		// Said out loud because it signs the person out of every device: which login chain it was and how
		// stale the presented token was are what tell a stolen token from a browser that kept an old cookie.
		log.Printf("identity: rotated-out refresh token replayed (chain %.8s, rotated %s ago); revoking every session of the account",
			chainID, time.Since(*revokedAt).Round(time.Second))
		if err := s.RevokeAccountEverywhere(ctx, accountID); err != nil {
			return "", "", err
		}
		return "", "", ErrSessionReplayed
	}
	if revokedAt != nil || expiresAt == nil || !expiresAt.After(time.Now()) {
		return "", "", ErrSessionRevoked
	}

	newRaw, newHash, err := newRefreshToken()
	if err != nil {
		return "", "", err
	}
	var newID string
	if err := tx.QueryRow(ctx, `INSERT INTO human_sessions (account_id, token_hash, chain_id, expires_at, last_used_at, user_agent, ip)
		SELECT account_id, $2, chain_id, expires_at, now(), user_agent, ip FROM human_sessions WHERE id = $1 RETURNING id::text`,
		id, newHash).Scan(&newID); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE human_sessions SET revoked_at = now(), replaced_by = $2, last_used_at = now() WHERE id = $1`, id, newID); err != nil {
		return "", "", err
	}

	// Mint BEFORE committing: a mint failure here must roll the whole rotation back rather
	// than leaving a committed rotation with no usable access token.
	access, err = s.MintHumanAccessTokenForSession(ctx, accountID, audience, chainID)
	if err != nil {
		return "", "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return access, newRaw, nil
}

// SessionExpiresAt looks up the (absolute, login-anchored) expires_at of the live human_sessions
// row matching rawRefreshToken's hash. Used by the HTTP layer after a rotation to size the
// rotated cookie's Max-Age to the session's REMAINING lifetime, rather than resetting the clock
// to a fresh full TTL on every refresh — session lifetime is absolute from login, rotation only
// changes which token names it, not how long it lasts.
func (s *Service) SessionExpiresAt(ctx context.Context, rawRefreshToken string) (time.Time, error) {
	var expiresAt time.Time
	err := s.st.Pool().QueryRow(ctx,
		`SELECT expires_at FROM human_sessions WHERE token_hash = $1`, hashToken(rawRefreshToken)).
		Scan(&expiresAt)
	if err != nil {
		return time.Time{}, ErrSessionRevoked
	}
	return expiresAt, nil
}

// SessionMustChangePassword reports whether the account behind rawRefreshToken's session still
// has must_change_password set. The HTTP layer consults it BEFORE minting on GET /auth/refresh so
// a pending-rotation account is steered to core's own /change-password page instead of being
// handed a password.change-only token for board or runner — components that have no screen for
// such a token and would just render a page of silent 403s. Looks the row up by hash regardless
// of its revoked/expired state (RefreshAccessToken decides that; this only answers "whose is
// it?"), so a rotated-out token inside the replay-grace window is answered the same way.
func (s *Service) SessionMustChangePassword(ctx context.Context, rawRefreshToken string) (bool, error) {
	var must bool
	err := s.st.Pool().QueryRow(ctx,
		`SELECT a.must_change_password FROM human_sessions s JOIN accounts a ON a.id = s.account_id
		 WHERE s.token_hash = $1`, hashToken(rawRefreshToken)).Scan(&must)
	if err != nil {
		return false, ErrSessionRevoked
	}
	return must, nil
}

// RevokeHumanSession marks one refresh token's session revoked ("log out this device").
func (s *Service) RevokeHumanSession(ctx context.Context, rawRefreshToken string) error {
	_, err := s.st.Pool().Exec(ctx,
		`UPDATE human_sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
		hashToken(rawRefreshToken))
	return err
}

// RevokeAllSessions marks every live (non-revoked) human_sessions row for accountID revoked —
// "log out everywhere", sessions only. It does not touch the shared revocations table; callers
// that also need already-minted access tokens to stop verifying should use
// RevokeAccountEverywhere instead.
func (s *Service) RevokeAllSessions(ctx context.Context, accountID string) error {
	_, err := s.st.Pool().Exec(ctx,
		`UPDATE human_sessions SET revoked_at = now() WHERE account_id = $1 AND revoked_at IS NULL`,
		accountID)
	return err
}

// RevokeAccountEverywhere kills every live session for accountID (RevokeAllSessions), marks
// every one of the account's live agent tokens revoked (RevokeAllAgentTokens) AND
// revokes the account in the shared revocations table under BOTH kinds, so an access token
// already issued for that account — not just future refresh attempts — also fails Verify's
// fast-path revocation check.
//
// Both kinds are needed because the two token families carry the account id in different
// claims. A human access token has sub = account id, caught by the "sub" entry. A user-minted
// AGENT token has sub = its own token id and only lineage = account id (spec §2), so nothing
// but a "lineage" entry reaches it: without one, "log out everywhere", a password change, an
// admin disable or a reconcile removal would leave every one of that account's agent tokens
// alive and verifying. cid.RevocationSet matches an entry against the claim of the SAME name,
// never across kinds, so one entry genuinely cannot stand in for the other.
//
// Neither entry is a lockout: both are timestamp-scoped (RevokedFor applies them only to
// tokens issued at or before revoked_at), so a re-login and any token minted after it verify
// normally while everything issued before stays dead.
//
// Deliberately, only the "sub" entry is ever cleared again (Unrevoke, in startHumanSession and
// reconcile's re-enable branch); the "lineage" entry is left in place for good. Removing it
// would RESURRECT every agent token the revocation killed — a human access token lives ten
// minutes, so clearing its entry is harmless, but a user-minted agent token lives up to a
// year, and "I revoked everything, then signed back in, and the leaked token started working
// again" is exactly the failure this entry exists to prevent.
func (s *Service) RevokeAccountEverywhere(ctx context.Context, accountID string) error {
	if err := s.RevokeAllSessions(ctx, accountID); err != nil {
		return err
	}
	if err := s.Revoke(ctx, "sub", accountID); err != nil {
		return err
	}
	if err := s.Revoke(ctx, "lineage", accountID); err != nil {
		return err
	}
	// The lineage entry is what actually stops those tokens verifying; this marks the ROWS
	// revoked too, so the owner's token list says what is true instead of showing a page of
	// "Active" credentials that no component will accept.
	return s.RevokeAllAgentTokens(ctx, accountID)
}
