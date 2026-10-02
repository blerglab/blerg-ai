package identity

import (
	"context"
	"errors"
	"log"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// AgentTokenKindExchange is the agent_tokens.kind of a token exchanged for a built-in target
// (spec 4.5, 10.2): signed, short-lived, hidden from the owner's token list and not counted
// toward the per-account live-token cap.
const AgentTokenKindExchange = "exchange"

// ExchangeTokenTTL is how long an exchanged token lives. The runner re-exchanges as it nears expiry.
const ExchangeTokenTTL = 10 * time.Minute

// maxLiveExchangeTokens bounds the live exchange tokens of one account. They are exempt from the
// user cap (maxLiveAgentTokens) because the runner mints and revokes them automatically, but a
// runaway caller still must not accumulate rows without bound.
const maxLiveExchangeTokens = 100

// ExchangeRevocationMargin is how long past its exp an exchange token's row and its "sub"
// revocation are kept. A token past exp cannot verify whatever the revocation set says, so the
// margin only has to cover verifier clock skew and revocation-poll staleness.
const ExchangeRevocationMargin = time.Hour

// ErrNoExchangeCaps is returned by CreateExchangeToken when the proof token holds none of the
// capabilities the target would grant.
var ErrNoExchangeCaps = errors.New("identity: proof token holds no capability for this target")

// exchangeTargetCaps are the capabilities an exchanged token of a target carries (before
// intersection with the owner's role and the proof). The board target does NOT get column.write:
// the built-in board connection has no tool that creates or moves columns.
var exchangeTargetCaps = map[string][]string{
	"board": {"card.read", "card.write"},
}

// ErrUnknownExchangeTarget is returned by CreateExchangeToken for a target outside ExchangeTargets.
var ErrUnknownExchangeTarget = errors.New("identity: unknown exchange target")

// ExchangeTargets maps a built-in target name to the agent-token preset that fixes its audience
// and capabilities. Only "board" exists; audience and caps are never caller-supplied.
var ExchangeTargets = map[string]string{
	"board": "board",
}

// CreateExchangeToken mints the short-lived token for target, scoped to one project (for the
// board, the board id carried in the Project claim). Audience and caps come from the target's
// preset, INTERSECTED with the account's current role caps, exactly as CreateAgentToken does. The
// token is kind "agent" on the wire, on behalf of (and lineage of) the account, and its sub is
// the id of an agent_tokens row of kind "exchange", so RevokeExchangeToken and a log-out-everywhere
// both kill it. The same-second revocation handling of mintLiveAgentToken applies.
//
// proofCaps are the capabilities of the agent token that proved liveness; nil means the proof was
// a human session (the account's role caps apply, as above). A proof token can never yield more
// than it holds: the issued caps are intersected with proofCaps, except that a token holding
// session.start (run-sessions, cron) delegates the target's caps, because starting the session
// the board connection belongs to is exactly what it is for. A proof that leaves no capability is
// ErrNoExchangeCaps.
func (s *Service) CreateExchangeToken(ctx context.Context, accountID, target, project string, proofCaps []string) (AgentTokenRecord, string, error) {
	presetName, ok := ExchangeTargets[target]
	if !ok {
		return AgentTokenRecord{}, "", ErrUnknownExchangeTarget
	}
	p := AgentTokenPresets[presetName]
	state, err := s.AccountState(ctx, accountID)
	if err != nil {
		return AgentTokenRecord{}, "", err
	}
	if state.Disabled {
		return AgentTokenRecord{}, "", ErrAccountDisabled
	}
	caps := intersectCaps(exchangeTargetCaps[target], PlatformRoleCaps[state.Role])
	if proofCaps != nil && !slices.Contains(proofCaps, "session.start") {
		caps = intersectCaps(caps, proofCaps)
	}
	if len(caps) == 0 {
		return AgentTokenRecord{}, "", ErrNoExchangeCaps
	}

	var live int
	if err := s.st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM agent_tokens
		  WHERE account_id = $1 AND kind = $2 AND revoked_at IS NULL AND expires_at > now()`,
		accountID, AgentTokenKindExchange).Scan(&live); err != nil {
		return AgentTokenRecord{}, "", err
	}
	if live >= maxLiveExchangeTokens {
		return AgentTokenRecord{}, "", ErrTooManyTokens
	}

	var id string
	if err := s.st.Pool().QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return AgentTokenRecord{}, "", err
	}
	expiresAt := time.Now().Add(ExchangeTokenTTL)
	raw, err := s.mintLiveAgentToken(ctx, AgentTokenInput{
		Sub:        id,
		Aud:        p.Aud,
		Project:    project,
		Caps:       caps,
		OnBehalfOf: accountID,
		Lineage:    accountID,
		ExpiresAt:  expiresAt,
	})
	if err != nil {
		return AgentTokenRecord{}, "", err
	}
	rec := AgentTokenRecord{ID: id, AccountID: accountID, Name: "exchange: " + target, Aud: p.Aud, Caps: caps}
	if err := s.st.Pool().QueryRow(ctx,
		`INSERT INTO agent_tokens (id, account_id, name, aud, caps, token_hash, expires_at, kind)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 RETURNING created_at, expires_at`,
		id, accountID, rec.Name, p.Aud, caps, agentTokenHash(raw), expiresAt, AgentTokenKindExchange,
	).Scan(&rec.CreatedAt, &rec.ExpiresAt); err != nil {
		return AgentTokenRecord{}, "", err
	}
	return rec, raw, nil
}

// RevokeExchangeToken revokes id only when it is an exchange-kind token of accountID's; any other
// token, another account's, or a missing id is ErrNotFound. Idempotent.
func (s *Service) RevokeExchangeToken(ctx context.Context, accountID, id string) error {
	return s.revokeTokenOfKind(ctx, accountID, id, AgentTokenKindExchange)
}

// AgentTokenKindOf returns the kind of id when it is one of accountID's tokens, or "" when it is
// not (unknown, malformed, or another account's).
func (s *Service) AgentTokenKindOf(ctx context.Context, accountID, id string) (string, error) {
	if !agentTokenIDShape.MatchString(id) || !agentTokenIDShape.MatchString(accountID) {
		return "", nil
	}
	var kind string
	err := s.st.Pool().QueryRow(ctx,
		`SELECT kind FROM agent_tokens WHERE id = $1 AND account_id = $2`, id, accountID).Scan(&kind)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return kind, nil
}

// AgentTokenCaps returns the capabilities recorded on id when it is one of accountID's tokens; an
// unknown, malformed or foreign id is ErrNotFound.
func (s *Service) AgentTokenCaps(ctx context.Context, accountID, id string) ([]string, error) {
	if !agentTokenIDShape.MatchString(id) || !agentTokenIDShape.MatchString(accountID) {
		return nil, ErrNotFound
	}
	var caps []string
	err := s.st.Pool().QueryRow(ctx, `SELECT caps FROM agent_tokens WHERE id = $1 AND account_id = $2`, id, accountID).Scan(&caps)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if caps == nil {
		caps = []string{}
	}
	return caps, nil
}

// PruneExchangeTokens deletes exchange-kind agent_tokens rows whose exp is more than margin in
// the past, together with their "sub" revocations, and returns how many rows went. Such a token
// can no longer verify, so neither the row nor its revocation still matters; everything else in
// revocations (lineage, kid, other subs, revocations of cron and ordinary tokens) is untouched.
// The runner mints and revokes one exchange token per session or fire, so without this both
// tables, and the GET /revocations snapshot every verifier polls, grow without bound.
func (s *Service) PruneExchangeTokens(ctx context.Context, margin time.Duration) (int64, error) {
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	secs := margin.Seconds()
	if _, err := tx.Exec(ctx,
		`DELETE FROM revocations WHERE kind = 'sub' AND value IN
		   (SELECT id::text FROM agent_tokens WHERE kind = $1 AND expires_at < now() - make_interval(secs => $2))`,
		AgentTokenKindExchange, secs); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx,
		`DELETE FROM agent_tokens WHERE kind = $1 AND expires_at < now() - make_interval(secs => $2)`,
		AgentTokenKindExchange, secs)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

// StartExchangePruneLoop prunes once now and then every interval until ctx ends. Idempotent, so
// several core replicas may run it.
func StartExchangePruneLoop(ctx context.Context, s *Service, interval time.Duration) {
	run := func() {
		if n, err := s.PruneExchangeTokens(ctx, ExchangeRevocationMargin); err != nil {
			log.Printf("identity: prune exchange tokens: %v", err)
		} else if n > 0 {
			log.Printf("identity: pruned %d expired exchange tokens", n)
		}
	}
	go func() {
		run()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				run()
			}
		}
	}()
}
