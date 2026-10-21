package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
)

const humanAccessTokenTTL = 10 * time.Minute

// defaultSessionTTL is used whenever SetSessionTTL has never been called (zero value), and
// matches the migration's own DEFAULT for expires_at (30 days), kept here as the fallback used
// by IssueRefreshToken/rotation for a Service built without an explicit call to SetSessionTTL.
const defaultSessionTTL = 720 * time.Hour

// defaultSessionLockTimeout bounds how long a session write waits for the account's session fence
// (lockAccountSessions) or a session row when SetSessionLockTimeout was never called. The fence
// holders are short transactions with no outside I/O, so a wait this long means something is
// wrong, and giving up beats tying up a pooled connection indefinitely.
const defaultSessionLockTimeout = 5 * time.Second

// SessionRevocationMargin is how long past the access-token lifetime a browser session's "sid"
// revocation is kept (PruneSessionRevocations): slack for clock skew between core and the
// components that verify its tokens.
const SessionRevocationMargin = time.Hour

// ErrSessionBusy is returned when a session write could not take its locks within the session
// lock timeout. Nothing was written and the presented token is unchanged: it is a retry, not a
// sign-out.
var ErrSessionBusy = errors.New("identity: session busy; try again")

// sessionBusy maps retryable transaction aborts and lock timeouts to a temporary failure.
func sessionBusy(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "55P03" || pgErr.Code == "40P01" || pgErr.Code == "40001") { // lock_not_available
		return fmt.Errorf("%w: %s", ErrSessionBusy, pgErr.Message)
	}
	return err
}

// AsSessionBusy is sessionBusy for callers outside the package that ran session work on their own
// transaction (RevokeAccountEverywhereTx): a lock timeout there is ErrSessionBusy too.
func AsSessionBusy(err error) error { return sessionBusy(err) }

// retryBusy runs an idempotent session revocation, trying again a few times when it was only
// busy: an end the person or theft detection asked for should not be dropped for a lock wait.
func retryBusy(f func() error) error {
	var err error
	for range 3 {
		if err = sessionBusy(f()); !errors.Is(err, ErrSessionBusy) {
			return err
		}
	}
	return err
}

// ErrSessionRevoked is returned by RefreshAccessToken when the raw refresh token doesn't
// match any live (non-revoked, non-expired) human_sessions row.
var ErrSessionRevoked = errors.New("identity: session revoked or unknown")

// ErrSessionReplayed is returned by RefreshAccessToken when the presented raw refresh token
// matches a row that has already been rotated out (revoked_at set AND replaced_by set) — i.e.
// a refresh token that was already exchanged for a newer one is being presented again. This is
// theft-detection: a legitimate client never has a reason to reuse a token it already rotated
// away from, so this is treated as evidence of a stolen/duplicated token and the entire
// rotation chain is revoked.
var ErrSessionReplayed = errors.New("identity: rotated-out refresh token reused; its browser session is revoked")

// ErrAccountDisabled is returned when the account's disabled_at is set (reconcile removed it
// from the org, or an admin deactivated it). Login and refresh both refuse such accounts.
var ErrAccountDisabled = errors.New("identity: account disabled")

// AccountState is the per-account gating state every mint consults.
type AccountState struct {
	Role               string
	Disabled           bool
	MustChangePassword bool
}

// rowQuerier is what both a pool and a transaction offer for a single-row read.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// AccountState reads the current role/disabled/must-change-password state for accountID.
func (s *Service) AccountState(ctx context.Context, accountID string) (AccountState, error) {
	return accountState(ctx, s.st.Pool(), accountID)
}

func accountState(ctx context.Context, q rowQuerier, accountID string) (AccountState, error) {
	var a AccountState
	var disabledAt *time.Time
	err := q.QueryRow(ctx,
		`SELECT role, disabled_at, must_change_password FROM accounts WHERE id = $1`, accountID).
		Scan(&a.Role, &disabledAt, &a.MustChangePassword)
	if err != nil {
		return a, err
	}
	a.Disabled = disabledAt != nil
	return a, nil
}

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
	return s.mintHumanAccessToken(ctx, s.st.Pool(), accountID, audience, sessionID)
}

// mintHumanAccessToken is MintHumanAccessTokenForSession reading the account through q, so a
// refresh rotation mints on its own transaction rather than a second pooled connection.
func (s *Service) mintHumanAccessToken(ctx context.Context, q rowQuerier, accountID, audience, sessionID string) (string, error) {
	a, err := accountState(ctx, q, accountID)
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

// ErrLoginSuperseded is returned when the account was signed out everywhere (log out
// everywhere, a password change, a disable) after a sign-in began checking its credential: that
// check may have used what the revocation replaced, so the sign-in must start over.
var ErrLoginSuperseded = errors.New("identity: signed out everywhere during sign-in; sign in again")

// LoginMark is what a sign-in saw before it checked the credential (BeginLogin). Under the
// account fence, IssueRefreshTokenSince refuses the sign-in when an account-wide revocation
// committed after the mark.
type LoginMark struct {
	start     time.Time  // database clock when the sign-in began
	accountID string     // the account the subject named at the start; "" when none did
	lineage   *time.Time // that account's "lineage" revocation then; nil when it had none
}

// BeginLogin marks the start of a sign-in, before its credential is checked. provider and
// subject name the account when the caller knows them up front (the password form); pass ""
// for both when the account is only known after the check (an identity provider's callback).
//
// The account's "lineage" revocation is the version that is compared: every revocation
// everywhere rewrites its revoked_at and nothing ever deletes it, so a sign-in that sees a
// different value under the fence knows a revocation committed in between, however its
// timestamps fall. Without an account, the mark's start time is compared instead.
func (s *Service) BeginLogin(ctx context.Context, provider, subject string) (LoginMark, error) {
	var m LoginMark
	var accountID *string
	err := s.st.Pool().QueryRow(ctx, `
  SELECT now(), a.id::text, r.revoked_at
    FROM (SELECT 1) one
    LEFT JOIN accounts a ON a.provider = $1 AND a.provider_subject = $2 AND $1 <> ''
    LEFT JOIN revocations r ON r.kind = 'lineage' AND r.value = a.id::text`, provider, subject).
		Scan(&m.start, &accountID, &m.lineage)
	if err != nil {
		return LoginMark{}, sessionBusy(err)
	}
	if accountID != nil {
		m.accountID = *accountID
	}
	return m, nil
}

// supersededBy reports whether lineage (the account's "lineage" revocation, read under the
// fence) shows a revocation that committed after the mark.
func (m *LoginMark) supersededBy(accountID string, lineage *time.Time) bool {
	if lineage != nil && !lineage.Before(m.start) {
		return true
	}
	if m.accountID == "" {
		return false
	}
	if m.accountID != accountID {
		return true
	}
	if (lineage == nil) != (m.lineage == nil) {
		return true
	}
	return lineage != nil && !lineage.Equal(*m.lineage)
}

// IssueRefreshToken creates a new human_sessions row (starting a fresh rotation chain) and
// returns the raw opaque token — the caller sets this as a cookie value; only its SHA-256 hash
// is ever persisted. The row's chain_id starts as its own id, and expires_at is set from the
// configured session TTL (sessionTTL()).
//
// It is for a session that follows no credential check (first-run setup). A sign-in uses
// IssueRefreshTokenSince with the mark it took before checking the credential.
func (s *Service) IssueRefreshToken(ctx context.Context, accountID, userAgent, ip string) (string, error) {
	return s.issueRefreshToken(ctx, nil, accountID, userAgent, ip)
}

// IssueRefreshTokenSince is IssueRefreshToken for a sign-in that began at mark (BeginLogin). It
// returns ErrLoginSuperseded, and changes nothing, when the account was revoked everywhere after
// the mark: a login whose password check ran before a password change must not survive it, nor
// clear the revocation the change wrote.
func (s *Service) IssueRefreshTokenSince(ctx context.Context, mark LoginMark, accountID, userAgent, ip string) (string, error) {
	return s.issueRefreshToken(ctx, &mark, accountID, userAgent, ip)
}

func (s *Service) issueRefreshToken(ctx context.Context, mark *LoginMark, accountID, userAgent, ip string) (string, error) {
	raw, hash, err := newRefreshToken()
	if err != nil {
		return "", err
	}
	// Login and sign-out are ordered by the account fence, not HTTP arrival time.
	// A login that obtains it first commits before sign-out and is revoked by it.
	// One that obtains it after sign-out is refused when it began before the sign-out (mark),
	// and is a fresh login otherwise. Clearing the sub revocation and inserting the session
	// must commit together under that same fence.
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return "", sessionBusy(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := s.lockAccountSessions(ctx, tx, accountID); err != nil {
		return "", sessionBusy(err)
	}
	state, err := accountState(ctx, tx, accountID)
	if err != nil {
		return "", err
	}
	if state.Disabled {
		return "", ErrAccountDisabled
	}
	if mark != nil {
		var lineage *time.Time
		err := tx.QueryRow(ctx, `SELECT revoked_at FROM revocations WHERE kind = 'lineage' AND value = $1`, accountID).Scan(&lineage)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", sessionBusy(err)
		}
		if mark.supersededBy(accountID, lineage) {
			return "", ErrLoginSuperseded
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM revocations WHERE kind = 'sub' AND value = $1`, accountID); err != nil {
		return "", sessionBusy(err)
	}
	ttl := s.sessionTTL()
	_, err = tx.Exec(ctx, `
  INSERT INTO human_sessions (id, account_id, token_hash, user_agent, ip, expires_at, chain_id)
  SELECT id, $1, $2, $3, $4, now() + $5::interval, id FROM (SELECT gen_random_uuid() AS id) seed`,
		accountID, hash, userAgent, ip, ttl.String())
	if err != nil {
		return "", sessionBusy(err)
	}
	s.sessionHook("issue")
	if err := tx.Commit(ctx); err != nil {
		return "", sessionBusy(err)
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
// both set), one of two things is true:
//
//  1. The browser never got the rotation. The response that carried the successor cookie was
//     lost (a frame torn down on a timeout, a connection that dropped after core committed, a
//     second tab that raced the first), so the browser still holds the old token and the
//     successor has NEVER been presented (its last_used_at is null). Bookkeeping-only
//     superseded successors are followed to the live head. The rotation is redone:
//     the never-used successor is revoked ('superseded') and a new successor is issued in the
//     same chain. The browser ends up where it should have been; nobody else loses anything,
//     and no clock is involved — a tab that wakes an hour later is treated the same.
//  2. Otherwise the successor has been presented, so two parties have held the same token:
//     reuse, the theft signal. The CHAIN is revoked — every row of this browser's session and,
//     through a "sid" entry in the shared revocations table, every access token it minted — and
//     ErrSessionReplayed is returned. The person's other devices, and the agent tokens their
//     tools hold, are untouched: a reused refresh token says nothing about them.
//
// Any other revoked, expired, or unknown token returns ErrSessionRevoked.
func (s *Service) RefreshAccessToken(ctx context.Context, rawRefreshToken, audience string) (access, newRefresh string, err error) {
	defer func() {
		if err != nil {
			err = sessionBusy(err)
		}
	}()
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after a successful commit is a no-op; on failure the original error is returned

	// Take the account's session fence before any row lock (see lockAccountSessions). A row's
	// account never changes, so reading it unlocked first is safe; everything that decides
	// whether the token is still good is read again below, under the fence.
	var owner string
	if err := tx.QueryRow(ctx, `SELECT account_id::text FROM human_sessions WHERE token_hash = $1`,
		hashToken(rawRefreshToken)).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", ErrSessionRevoked
		}
		return "", "", err
	}
	if err := s.lockAccountSessions(ctx, tx, owner); err != nil {
		return "", "", err
	}

	var id, accountID, chainID string
	var revokedAt, expiresAt *time.Time
	var replacedBy *string
	err = tx.QueryRow(ctx, `SELECT id::text, account_id::text, chain_id::text, revoked_at, expires_at, replaced_by::text
		FROM human_sessions WHERE token_hash = $1 FOR UPDATE`, hashToken(rawRefreshToken)).
		Scan(&id, &accountID, &chainID, &revokedAt, &expiresAt, &replacedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrSessionRevoked
	}
	if err != nil {
		return "", "", err
	}

	if revokedAt != nil && replacedBy != nil {
		successor := *replacedBy
		seen := map[string]bool{id: true}
		recoverable := false
		for !seen[successor] {
			seen[successor] = true
			var revoked, expires, used *time.Time
			var next *string
			var reason string
			if err := tx.QueryRow(ctx, `SELECT revoked_at, expires_at, last_used_at, replaced_by::text, COALESCE(revoke_reason, '')
                FROM human_sessions WHERE id = $1 AND account_id = $2 AND chain_id = $3 FOR UPDATE`, successor, accountID, chainID).
				Scan(&revoked, &expires, &used, &next, &reason); err != nil {
				return "", "", err
			}
			if revoked == nil {
				recoverable = expires != nil && expires.After(time.Now()) && used == nil
				break
			}
			// Only bookkeeping replacements can be crossed. A rotated row
			// proves that a browser presented a successor: the replay boundary.
			if reason != "superseded" || next == nil {
				break
			}
			successor = *next
		}
		if recoverable {
			// Case 1: the rotation never reached the browser. Redo it from the successor the
			// browser never saw: that row is superseded, a new one takes its place.
			log.Printf("identity: refresh token presented again %s after a rotation its browser never received (chain %.8s); redoing the rotation",
				time.Since(*revokedAt).Round(time.Second), chainID)
			access, newRefresh, err = s.rotateTx(ctx, tx, successor, accountID, chainID, audience, "superseded")
			if err != nil {
				return "", "", err
			}
			s.sessionHook("redo")
			// The presented token was used, whatever its state.
			if _, err := tx.Exec(ctx, `UPDATE human_sessions SET last_used_at = now() WHERE id = $1`, id); err != nil {
				return "", "", err
			}
			if err := tx.Commit(ctx); err != nil {
				return "", "", err
			}
			return access, newRefresh, nil
		}
		// Case 2: reuse. Commit first (nothing was written in this branch — it releases the
		// FOR UPDATE row locks), then revoke the chain. Said out loud, with which chain and how
		// stale the token was: that is what tells a stolen cookie from a browser that kept an
		// old one.
		if err := tx.Commit(ctx); err != nil {
			return "", "", err
		}
		log.Printf("identity: rotated-out refresh token reused (chain %.8s, rotated %s ago); revoking that browser session only (other devices and agent tokens untouched)",
			chainID, time.Since(*revokedAt).Round(time.Second))
		if err := s.RevokeChain(ctx, chainID, "reuse"); err != nil {
			return "", "", err
		}
		return "", "", ErrSessionReplayed
	}
	if revokedAt != nil || expiresAt == nil || !expiresAt.After(time.Now()) {
		return "", "", ErrSessionRevoked
	}

	access, newRefresh, err = s.rotateTx(ctx, tx, id, accountID, chainID, audience, "rotated")
	if err != nil {
		return "", "", err
	}
	s.sessionHook("rotate")
	if err := tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return access, newRefresh, nil
}

// rotateTx rotates the live row fromID inside tx: a new row is issued in the same chain,
// carrying forward the original's user_agent/ip and absolute expires_at, and fromID is revoked
// with the given reason, replaced_by the new row, and marked used. The successor starts with no
// last_used_at: it has not been presented until the browser comes back with it, which is how
// RefreshAccessToken tells a lost rotation from a reused token. The access token is minted
// before the caller commits, so a mint failure rolls the whole rotation back.
func (s *Service) rotateTx(ctx context.Context, tx pgx.Tx, fromID, accountID, chainID, audience, reason string) (access, newRaw string, err error) {
	newRaw, newHash, err := newRefreshToken()
	if err != nil {
		return "", "", err
	}
	var newID string
	if err := tx.QueryRow(ctx, `INSERT INTO human_sessions (account_id, token_hash, chain_id, expires_at, user_agent, ip)
		SELECT account_id, $2, chain_id, expires_at, user_agent, ip FROM human_sessions WHERE id = $1 RETURNING id::text`,
		fromID, newHash).Scan(&newID); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE human_sessions SET revoked_at = now(), revoke_reason = $3, replaced_by = $2, last_used_at = now() WHERE id = $1`, fromID, newID, reason); err != nil {
		return "", "", err
	}
	access, err = s.mintHumanAccessToken(ctx, tx, accountID, audience, chainID)
	if err != nil {
		return "", "", err
	}
	return access, newRaw, nil
}

// sessionHook runs the test-only beforeSessionCommit hook, if one is set.
func (s *Service) sessionHook(point string) {
	if s.beforeSessionCommit != nil {
		s.beforeSessionCommit(point)
	}
}

// lockAccountSessions takes the account's session fence for the rest of tx: a
// transaction-scoped advisory lock that every refresh rotation and every multi-row session
// revocation of that account takes BEFORE it locks or updates any human_sessions row. It makes
// those writers take turns, so a revocation can never run against a snapshot that misses the
// successor of a rotation committing at the same moment, and a rotation that waited on a
// revocation re-reads its row and finds it revoked. One lock, always first, so no two of these
// paths can wait on each other in opposite orders.
//
// It also sets lock_timeout for the rest of tx, so neither the fence nor a later row lock is waited
// on for longer than the session lock timeout; a wait that runs out fails with SQLSTATE 55P03,
// which sessionBusy turns into ErrSessionBusy. Every fence holder does all of its work on tx, so
// it never needs a second pooled connection while waiters hold theirs.
func (s *Service) lockAccountSessions(ctx context.Context, tx pgx.Tx, accountID string) error {
	// At least 1ms: Postgres reads lock_timeout = 0 as "wait forever".
	ms := strconv.FormatInt(max(s.sessionLockTimeout().Milliseconds(), 1), 10)
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, ms); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('human_sessions:' || $1::text, 0))`, accountID)
	return err
}

// SetSessionLockTimeout sets how long a session write waits for its locks before giving up with
// ErrSessionBusy. Like SetSessionTTL, set it once at startup (or in a test) before serving.
func (s *Service) SetSessionLockTimeout(d time.Duration) {
	s.sessionLockTimeoutValue = d
}

func (s *Service) sessionLockTimeout() time.Duration {
	if s.sessionLockTimeoutValue <= 0 {
		return defaultSessionLockTimeout
	}
	return s.sessionLockTimeoutValue
}

// revokeChainTx ends the chain inside tx, under the account's session fence: every live row of
// the chain is revoked with reason, and the chain's sid is revoked in the shared table.
func (s *Service) revokeChainTx(ctx context.Context, tx pgx.Tx, accountID, chainID, reason string) error {
	if err := s.lockAccountSessions(ctx, tx, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE human_sessions SET revoked_at = now(), revoke_reason = $2 WHERE chain_id = $1 AND revoked_at IS NULL`,
		chainID, reason); err != nil {
		return err
	}
	return revokeSid(ctx, tx, chainID)
}

// revokeSid writes (or refreshes) the chain's "sid" entry in the shared revocations table, so
// access tokens minted for that browser session stop verifying.
func revokeSid(ctx context.Context, tx pgx.Tx, chainID string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO revocations(kind, value, revoked_at) VALUES ('sid', $1, now())
		 ON CONFLICT (kind, value) DO UPDATE SET revoked_at = now()`, chainID)
	return err
}

// RevokeChain ends one browser session: every row of the rotation chain is revoked with the
// reason, and a "sid" entry in the shared revocations table stops the access tokens it minted
// from verifying anywhere. The scope of a detected refresh-token reuse. It runs under the
// account's session fence, so a rotation of the chain in flight either commits first (and its
// successor is revoked here too) or waits and is then refused.
//
// A lock wait that runs out is retried a few times (retryBusy) before ErrSessionBusy is returned.
func (s *Service) RevokeChain(ctx context.Context, chainID, reason string) error {
	return retryBusy(func() error { return s.revokeChainOnce(ctx, chainID, reason) })
}

func (s *Service) revokeChainOnce(ctx context.Context, chainID, reason string) error {
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after a successful commit is a no-op

	var accountID string
	err = tx.QueryRow(ctx, `SELECT account_id::text FROM human_sessions WHERE chain_id = $1 LIMIT 1`, chainID).Scan(&accountID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No rows to revoke; the sid entry still ends any access token naming the chain.
		if err := revokeSid(ctx, tx, chainID); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if err := s.revokeChainTx(ctx, tx, accountID, chainID, reason); err != nil {
			return err
		}
	}
	s.sessionHook("revoke")
	return tx.Commit(ctx)
}

// PruneSessionRevocations deletes "sid" revocations whose browser session has ended and whose
// access tokens have all expired: written more than the access-token lifetime plus margin ago,
// with no live row left in the chain (once a chain has none, nothing can mint for it again). It
// returns how many rows went. Every logout writes one, and GET /revocations serves the whole table
// to every verifier, so without this the list grows for good. Other kinds are untouched.
func (s *Service) PruneSessionRevocations(ctx context.Context, margin time.Duration) (int64, error) {
	tag, err := s.st.Pool().Exec(ctx,
		`DELETE FROM revocations r WHERE r.kind = 'sid'
		   AND r.revoked_at < now() - make_interval(secs => $1)
		   AND NOT EXISTS (SELECT 1 FROM human_sessions h
		                    WHERE h.chain_id::text = r.value AND h.revoked_at IS NULL AND h.expires_at > now())`,
		(humanAccessTokenTTL + margin).Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
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

// RevokeHumanSession ends the browser session the refresh token belongs to ("log out this
// device"): every live row of its rotation chain is revoked as 'logout' and the chain's sid is
// revoked, whichever row of the chain the token names. It runs under the account's session fence,
// so a refresh of the same cookie committing at the same moment cannot leave a live successor.
// The account's other browser sessions (other chains) are untouched. An unknown token is a no-op.
//
// A lock wait that runs out is retried a few times (retryBusy) before ErrSessionBusy is returned.
func (s *Service) RevokeHumanSession(ctx context.Context, rawRefreshToken string) error {
	return retryBusy(func() error { return s.revokeHumanSessionOnce(ctx, rawRefreshToken) })
}

func (s *Service) revokeHumanSessionOnce(ctx context.Context, rawRefreshToken string) error {
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after a successful commit is a no-op
	var accountID, chainID string
	err = tx.QueryRow(ctx, `SELECT account_id::text, chain_id::text FROM human_sessions WHERE token_hash = $1`,
		hashToken(rawRefreshToken)).Scan(&accountID, &chainID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.revokeChainTx(ctx, tx, accountID, chainID, "logout"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RevokeAllSessions marks every live (non-revoked) human_sessions row for accountID revoked —
// "log out everywhere", sessions only. It does not touch the shared revocations table; callers
// that also need already-minted access tokens to stop verifying should use
// RevokeAccountEverywhere instead.
//
// It runs under the account's session fence (lockAccountSessions), so a refresh rotation in
// flight cannot leave a live successor behind it.
//
// A lock wait that runs out is retried a few times (retryBusy) before ErrSessionBusy is returned.
func (s *Service) RevokeAllSessions(ctx context.Context, accountID string) error {
	return retryBusy(func() error { return s.revokeAllSessionsOnce(ctx, accountID) })
}

func (s *Service) revokeAllSessionsOnce(ctx context.Context, accountID string) error {
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after a successful commit is a no-op
	if err := s.lockAccountSessions(ctx, tx, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE human_sessions SET revoked_at = now(), revoke_reason = 'logout_all' WHERE account_id = $1 AND revoked_at IS NULL`,
		accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
// Deliberately, only the "sub" entry is ever cleared again (by a sign-in, in
// issueRefreshToken, and by Unrevoke in reconcile's re-enable branch); the "lineage" entry is
// left in place for good (sign-in also reads it as the account's revocation version). Removing it
// would RESURRECT every agent token the revocation killed — a human access token lives ten
// minutes, so clearing its entry is harmless, but a user-minted agent token lives up to a
// year, and "I revoked everything, then signed back in, and the leaked token started working
// again" is exactly the failure this entry exists to prevent.
//
// All four writes are one transaction under the account's session fence (RevokeAccountEverywhereTx):
// they happen together or not at all. A lock wait that runs out is retried a few times before
// ErrSessionBusy is returned.
func (s *Service) RevokeAccountEverywhere(ctx context.Context, accountID string) error {
	return retryBusy(func() error {
		tx, err := s.st.Pool().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // a rollback after a successful commit is a no-op
		if err := s.RevokeAccountEverywhereTx(ctx, tx, accountID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

// RevokeAccountEverywhereTx is RevokeAccountEverywhere inside the caller's transaction, so the
// revocation commits (or not) together with the caller's own change, all or nothing: a password
// change uses it so a new password never stands while the old sessions and tokens still work. It
// takes the account's session fence, so it must run before the transaction locks the account's
// row or any human_sessions row (the order every fenced path keeps). A lock wait that runs out
// returns ErrSessionBusy and the caller should roll back and may retry.
func (s *Service) RevokeAccountEverywhereTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	if err := s.lockAccountSessions(ctx, tx, accountID); err != nil {
		return sessionBusy(err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE human_sessions SET revoked_at = now(), revoke_reason = 'logout_all' WHERE account_id = $1 AND revoked_at IS NULL`,
		accountID); err != nil {
		return sessionBusy(err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO revocations(kind, value, revoked_at) VALUES ('sub', $1, now()), ('lineage', $1, now())
		 ON CONFLICT (kind, value) DO UPDATE SET revoked_at = now()`, accountID); err != nil {
		return sessionBusy(err)
	}
	s.sessionHook("logout_all")
	// The lineage entry is what actually stops those tokens verifying; this marks the ROWS
	// revoked too, so the owner's token list says what is true instead of showing a page of
	// "Active" credentials that no component will accept.
	if _, err := tx.Exec(ctx,
		`UPDATE agent_tokens SET revoked_at = now() WHERE account_id = $1 AND revoked_at IS NULL`, accountID); err != nil {
		return sessionBusy(err)
	}
	return nil
}
