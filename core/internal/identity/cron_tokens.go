package identity

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Agent token kinds (agent_tokens.kind).
const (
	AgentTokenKindToken = "token"
	AgentTokenKindCron  = "cron"
)

// cronTokenPreset is the preset a cron token follows: same audience and capabilities as
// "run-sessions" (spec 7.4), so its row reads like any other run-sessions token.
const cronTokenPreset = "run-sessions"

// CreateUnsignedAgentToken creates the identity row of a cron (spec 7.4): an agent_tokens row of
// kind "cron", audience and capabilities of the run-sessions preset (caps intersected with the
// owner's role like every agent token), and a random token_hash that no value was ever derived
// from. NO JWT is signed, so there is nothing that could be presented on the wire; the row only
// lets the token id be proven live (AgentTokenLive / AgentTokenStatus), revoked
// (RevokeAgentToken, RevokeCronToken, RevokeAllAgentTokens on log-out-everywhere) and counted.
//
// It obeys the same cap of live tokens as CreateAgentToken (cron rows count) and the same
// 365-day maximum. expiresIn <= 0 means DefaultAgentTokenDays.
func (s *Service) CreateUnsignedAgentToken(ctx context.Context, accountID, name string, expiresIn time.Duration) (AgentTokenRecord, error) {
	p := AgentTokenPresets[cronTokenPreset]
	state, err := s.AccountState(ctx, accountID)
	if err != nil {
		return AgentTokenRecord{}, err
	}
	if state.Disabled {
		return AgentTokenRecord{}, ErrAccountDisabled
	}
	caps := intersectCaps(p.Caps, PlatformRoleCaps[state.Role])
	if expiresIn <= 0 {
		expiresIn = DefaultAgentTokenDays * 24 * time.Hour
	}
	if expiresIn > MaxAgentTokenDays*24*time.Hour {
		expiresIn = MaxAgentTokenDays * 24 * time.Hour
	}

	var live int
	if err := s.st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM agent_tokens
		  WHERE account_id = $1 AND kind <> 'exchange' AND revoked_at IS NULL AND expires_at > now()`, accountID).Scan(&live); err != nil {
		return AgentTokenRecord{}, err
	}
	if live >= maxLiveAgentTokens {
		return AgentTokenRecord{}, ErrTooManyTokens
	}

	// 32 random bytes, hashed: a value nobody holds. It only exists to satisfy the UNIQUE NOT NULL
	// column; it is never returned and cannot be the hash of any real token.
	var seed [32]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return AgentTokenRecord{}, err
	}
	rec := AgentTokenRecord{AccountID: accountID, Name: name, Aud: p.Aud, Caps: caps}
	if err := s.st.Pool().QueryRow(ctx,
		`INSERT INTO agent_tokens (account_id, name, aud, caps, token_hash, expires_at, kind)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 RETURNING id::text, created_at, expires_at`,
		accountID, name, p.Aud, caps, agentTokenHash(string(seed[:])), time.Now().Add(expiresIn), AgentTokenKindCron,
	).Scan(&rec.ID, &rec.CreatedAt, &rec.ExpiresAt); err != nil {
		return AgentTokenRecord{}, err
	}
	return rec, nil
}

// RevokeCronToken revokes id only when it is a cron-kind token of accountID's. It is the path a
// holder of nothing but the internal key may use (the scheduler pausing a cron on the owner's
// behalf): any other token, another account's, or a missing id is ErrNotFound. Idempotent.
func (s *Service) RevokeCronToken(ctx context.Context, accountID, id string) error {
	return s.revokeTokenOfKind(ctx, accountID, id, AgentTokenKindCron)
}

// revokeTokenOfKind stamps revoked_at and writes the "sub" revocation for id, only when it is a
// token of the given kind owned by accountID; anything else is ErrNotFound. Idempotent.
func (s *Service) revokeTokenOfKind(ctx context.Context, accountID, id, kind string) error {
	if !agentTokenIDShape.MatchString(id) || !agentTokenIDShape.MatchString(accountID) {
		return ErrNotFound
	}
	var revoked bool
	err := s.st.Pool().QueryRow(ctx,
		`UPDATE agent_tokens SET revoked_at = COALESCE(revoked_at, now())
		  WHERE id = $1 AND account_id = $2 AND kind = $3
		  RETURNING true`, id, accountID, kind).Scan(&revoked)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return s.Revoke(ctx, "sub", id)
}

// AgentTokenName is the name the owner gave token id, "" when it is not accountID's. The name
// is the owner's own label (shown back to them by the runner as who started a session), never
// a secret.
func (s *Service) AgentTokenName(ctx context.Context, accountID, id string) (string, error) {
	if !agentTokenIDShape.MatchString(id) || !agentTokenIDShape.MatchString(accountID) {
		return "", nil
	}
	var name string
	err := s.st.Pool().QueryRow(ctx, `SELECT name FROM agent_tokens WHERE id = $1 AND account_id = $2`, id, accountID).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return name, nil
}

// AgentTokenStatus reports whether id is a token of accountID's (owned) and, if so, whether it is
// live: unrevoked, unexpired and its account not disabled. A token that is not accountID's, or
// does not exist, is owned=false, so a caller can answer both with one uniform not-found.
func (s *Service) AgentTokenStatus(ctx context.Context, accountID, id string) (live, owned bool, err error) {
	if !agentTokenIDShape.MatchString(id) || !agentTokenIDShape.MatchString(accountID) {
		return false, false, nil
	}
	var disabled bool
	err = s.st.Pool().QueryRow(ctx,
		`SELECT a.disabled_at IS NOT NULL FROM agent_tokens t JOIN accounts a ON a.id = t.account_id
		  WHERE t.id = $1 AND t.account_id = $2`, id, accountID).Scan(&disabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, false, nil
		}
		return false, false, err
	}
	if disabled {
		return false, true, nil
	}
	live, err = s.AgentTokenLive(ctx, accountID, id)
	return live, true, err
}
