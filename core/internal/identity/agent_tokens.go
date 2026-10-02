package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
)

// ErrNotFound is returned by RevokeAgentToken when the id names no agent token belonging to the
// calling account — either because no such token exists at all, or because it belongs to
// somebody else. Deliberately ONE error for both cases: a caller must not be able to tell
// "that token id is real but not yours" from "that token id does not exist", which would turn
// DELETE /api/tokens/{id} into a token-id oracle.
var ErrNotFound = errors.New("identity: agent token not found")

// ErrTooManyTokens is returned by CreateAgentToken when the account already holds
// maxLiveAgentTokens live tokens. The handler answers 409 with the limit and the way out
// (revoke one) rather than letting an account accumulate credentials without bound.
var ErrTooManyTokens = errors.New("identity: too many active agent tokens")

// ErrRevokedAtMint is returned when a freshly minted token comes out already revoked and a
// re-mint past the second boundary did not fix it. It should be unreachable in practice —
// something would have to be writing a revocation for this very account in a tight loop — and
// it exists so the failure is a loud error rather than a token persisted dead on arrival.
var ErrRevokedAtMint = errors.New("identity: freshly minted agent token is already revoked")

// ErrUnknownPreset is returned by CreateAgentToken for a preset name outside AgentTokenPresets.
// Presets are a CLOSED list (spec §2): audience and capabilities are never free-form, so a
// caller can never mint itself an arbitrary audience or an arbitrary capability set.
var ErrUnknownPreset = errors.New("identity: unknown agent token preset")

// AgentTokenPreset is one entry of the closed preset list: the audience the minted token is
// valid for and the capabilities it asks for (before intersection with the owner's role).
type AgentTokenPreset struct {
	Aud  string
	Caps []string
	// Summary is the one-line explanation the Settings UI shows next to the preset name.
	Summary string
}

// AgentTokenPresets is spec §2's table, verbatim. Adding a preset is additive and safe;
// changing an existing preset's audience or caps is a contract break and must not happen.
var AgentTokenPresets = map[string]AgentTokenPreset{
	"run-sessions": {
		Aud:     "blerg-runner",
		Caps:    []string{"session.start"},
		Summary: "Start and follow runner sessions (REST and MCP).",
	},
	"board": {
		Aud:     "blerg-board",
		Caps:    []string{"card.read", "card.write", "column.write"},
		Summary: "Read and write board cards and columns (REST and MCP).",
	},
	"platform": {
		Aud:     "blerg-core",
		Caps:    []string{"card.read"},
		Summary: "Read your own identity and stored credential list on core.",
	},
}

// AgentTokenPresetNames returns the preset names in a stable (sorted) order, so callers that
// render or document the list don't expose Go's randomized map iteration order.
func AgentTokenPresetNames() []string {
	out := make([]string, 0, len(AgentTokenPresets))
	for name := range AgentTokenPresets {
		out = append(out, name)
	}
	// Small fixed list; an insertion sort keeps this dependency-free.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// DefaultAgentTokenDays / MaxAgentTokenDays bound expires_in_days (spec §2).
const (
	DefaultAgentTokenDays = 90
	MaxAgentTokenDays     = 365
)

// maxLiveAgentTokens caps how many unrevoked, unexpired tokens one account may hold at once.
// This is the credential-minting path: without a cap, a stolen access token (or a careless
// script in a loop) can quietly accumulate an unbounded pile of long-lived credentials, each
// of which has to be found and revoked individually afterwards. 50 is far above any plausible
// real use — a person names tokens after machines and pipelines — while keeping the blast
// radius of a runaway caller finite and the Settings list readable.
const maxLiveAgentTokens = 50

// AgentTokenRecord is one agent_tokens row as every caller sees it. It deliberately has no
// field for the token or its hash: the raw token is returned separately by CreateAgentToken
// (once) and the hash never leaves this package.
type AgentTokenRecord struct {
	ID         string     `json:"id"`
	AccountID  string     `json:"account_id"`
	Name       string     `json:"name"`
	Aud        string     `json:"aud"`
	Caps       []string   `json:"caps"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// agentTokenIDShape matches a standard 8-4-4-4-12 hex UUID. Every id reaching a query here is
// checked against it first: Postgres errors on a malformed uuid literal, which would turn a
// caller-supplied string into a 500 instead of the intended not-found/false outcome.
var agentTokenIDShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// agentTokenHash is the only form of an agent token ever persisted: hex SHA-256 of the raw
// compact JWT. Note it is not used to LOOK UP tokens (verification is the signature plus the
// revocations table, exactly as for every other core-signed token) — it exists so an operator
// holding a leaked token can identify which row it is without core ever storing the value.
func agentTokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CreateAgentToken mints a named agent token for accountID and persists its metadata.
//
// Capabilities are the preset's caps INTERSECTED with the account's current platform-role caps:
// a member can never mint a token that does more than the member themselves can do, so a leaked
// token is never a privilege escalation over its owner. A disabled account cannot mint at all
// (ErrAccountDisabled), matching MintHumanAccessToken.
//
// expiresIn <= 0 falls back to DefaultAgentTokenDays; callers are expected to have validated
// the caller-supplied bound already (the handler rejects anything outside 1..365 with a 400) —
// this clamp is the defence-in-depth floor, not the user-facing validation.
//
// The returned raw token is the ONLY time the value exists outside the caller's process: only
// its hash is stored.
func (s *Service) CreateAgentToken(ctx context.Context, accountID, name, preset string, expiresIn time.Duration) (AgentTokenRecord, string, error) {
	p, ok := AgentTokenPresets[preset]
	if !ok {
		return AgentTokenRecord{}, "", ErrUnknownPreset
	}
	state, err := s.AccountState(ctx, accountID)
	if err != nil {
		return AgentTokenRecord{}, "", err
	}
	if state.Disabled {
		return AgentTokenRecord{}, "", ErrAccountDisabled
	}
	caps := intersectCaps(p.Caps, PlatformRoleCaps[state.Role])
	if expiresIn <= 0 {
		expiresIn = DefaultAgentTokenDays * 24 * time.Hour
	}

	// Count LIVE tokens only: revoking or letting one expire frees a slot, so the cap bounds
	// what an account holds, never how many it has ever created. Two simultaneous mints could
	// in principle both observe count == limit-1 and both land; that race costs one extra token
	// on a 50-token cap, which is not worth serialising every mint behind a lock for.
	var live int
	if err := s.st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM agent_tokens
		  WHERE account_id = $1 AND kind <> 'exchange' AND revoked_at IS NULL AND expires_at > now()`, accountID).Scan(&live); err != nil {
		return AgentTokenRecord{}, "", err
	}
	if live >= maxLiveAgentTokens {
		return AgentTokenRecord{}, "", ErrTooManyTokens
	}

	// The id must exist before the token is signed (it IS the token's sub), and the token must
	// exist before the row is inserted (the row stores its hash) — so the id is drawn from
	// Postgres first and then supplied explicitly on INSERT, rather than relying on the
	// column's own DEFAULT gen_random_uuid().
	var id string
	if err := s.st.Pool().QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return AgentTokenRecord{}, "", err
	}
	expiresAt := time.Now().Add(expiresIn)
	raw, err := s.mintLiveAgentToken(ctx, AgentTokenInput{
		Sub:        id,
		Aud:        p.Aud,
		Caps:       caps,
		OnBehalfOf: accountID,
		Lineage:    accountID,
		ExpiresAt:  expiresAt,
	})
	if err != nil {
		return AgentTokenRecord{}, "", err
	}

	rec := AgentTokenRecord{ID: id, AccountID: accountID, Name: name, Aud: p.Aud, Caps: caps}
	if err := s.st.Pool().QueryRow(ctx,
		`INSERT INTO agent_tokens (id, account_id, name, aud, caps, token_hash, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 RETURNING created_at, expires_at`,
		id, accountID, name, p.Aud, caps, agentTokenHash(raw), expiresAt,
	).Scan(&rec.CreatedAt, &rec.ExpiresAt); err != nil {
		return AgentTokenRecord{}, "", err
	}
	return rec, raw, nil
}

// snapshotRevChecker is the timestamp-aware revocation view over one live snapshot — the same
// shape core's router builds per request (api.snapChecker). Never stale: core is authoritative
// against its own table.
type snapshotRevChecker struct{ cid.RevocationSet }

func (snapshotRevChecker) StaleBeyondCeiling() bool { return false }

// revocationChecker reads the revocations table into the shared cid.RevocationSet, so the
// "which tokens does an entry apply to" rule is the one every consumer uses rather than a
// second implementation that could drift from it.
func (s *Service) revocationChecker(ctx context.Context) (cid.RevocationChecker, error) {
	snap, err := s.RevocationSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	set := cid.RevocationSet{}
	for _, r := range snap {
		set.Add(r.Kind, r.Value, r.RevokedAt)
	}
	return snapshotRevChecker{set}, nil
}

// noSensitiveCaps is the fail-closed-capability predicate for a self-check: staleness handling
// is irrelevant here because snapshotRevChecker is never stale.
func noSensitiveCaps(string) bool { return false }

// mintLiveAgentToken signs the token and then verifies it against the CURRENT revocation
// snapshot before the caller is allowed to keep it.
//
// This exists because of a sharp edge in the timestamp rule: a "sub"/"lineage" revocation
// applies to tokens issued at or before its revoked_at, compared in WHOLE SECONDS with equality
// failing closed (contracts/identity.revocationApplies). The account-wide "lineage" entry is
// permanent, so a token minted in the very same second as a revocation — sign out everywhere,
// then immediately create a token — is revoked for its entire 90-day life, while the Settings
// list shows it as perfectly live and every call it makes returns an opaque 401. That is a
// miserable thing to debug and a trivial thing to prevent.
//
// So: verify, and if the answer is "revoked", wait out the rest of the second and mint once
// more with a fresh iat. A second attempt that still comes back revoked is a real revocation
// (not the boundary), and is an error rather than a token persisted dead on arrival.
func (s *Service) mintLiveAgentToken(ctx context.Context, in AgentTokenInput) (string, error) {
	keys, err := s.JWKS(ctx)
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := s.MintAgentToken(ctx, in)
		if err != nil {
			return "", err
		}
		checker, err := s.revocationChecker(ctx)
		if err != nil {
			return "", err
		}
		// Full Verify, not just the revocation lookup: it is the exact check every consumer
		// will apply, so anything it rejects (audience, expiry, signature) is caught here
		// rather than by the user's first confused request.
		switch _, err := cid.Verify(raw, in.Aud, keys, checker, noSensitiveCaps); {
		case err == nil:
			return raw, nil
		case !errors.Is(err, cid.ErrRevoked):
			return "", err
		}
		if attempt == 0 {
			if err := sleepPastSecondBoundary(ctx); err != nil {
				return "", err
			}
		}
	}
	return "", ErrRevokedAtMint
}

// sleepPastSecondBoundary waits until the wall clock has moved into the next whole second, so
// a re-minted token's iat is strictly greater than a revoked_at recorded in this one. The small
// margin covers the truncation being exact at the boundary.
func sleepPastSecondBoundary(ctx context.Context) error {
	now := time.Now()
	wait := time.Until(now.Truncate(time.Second).Add(time.Second)) + 10*time.Millisecond
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ListAgentTokens returns accountID's own tokens, newest first, metadata only — revoked and
// expired ones included, so the owner can see the whole history rather than having rows
// silently vanish. Cron-kind rows (CreateUnsignedAgentToken) are NOT listed: they are no
// credential the owner holds, so there is nothing to copy; they still count toward the cap and
// are managed from the runner's crons page.
func (s *Service) ListAgentTokens(ctx context.Context, accountID string) ([]AgentTokenRecord, error) {
	rows, err := s.st.Pool().Query(ctx,
		`SELECT id::text, name, aud, caps, created_at, expires_at, last_used_at, revoked_at
		   FROM agent_tokens WHERE account_id = $1 AND kind = 'token' ORDER BY created_at DESC, id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Never nil: the empty case must marshal as [] rather than null.
	out := []AgentTokenRecord{}
	for rows.Next() {
		rec := AgentTokenRecord{AccountID: accountID}
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Aud, &rec.Caps,
			&rec.CreatedAt, &rec.ExpiresAt, &rec.LastUsedAt, &rec.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// RevokeAgentToken kills one of accountID's own tokens: it stamps revoked_at (what the owner's
// list shows) and writes a revocations("sub", id) row (what actually stops the token verifying,
// everywhere — core checks it per request and board/runner poll /revocations).
//
// Idempotent: revoking an already-revoked token is a success and leaves the original
// revoked_at in place, because the revocations row is what does the work and the first
// revocation is the truthful timestamp. A token belonging to a different account — or none —
// is ErrNotFound, never a silent success and never a distinguishable "exists but not yours".
func (s *Service) RevokeAgentToken(ctx context.Context, accountID, id string) error {
	if !agentTokenIDShape.MatchString(id) {
		return ErrNotFound
	}
	var revoked bool
	err := s.st.Pool().QueryRow(ctx,
		`UPDATE agent_tokens SET revoked_at = COALESCE(revoked_at, now())
		  WHERE id = $1 AND account_id = $2
		  RETURNING true`, id, accountID).Scan(&revoked)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return s.Revoke(ctx, "sub", id)
}

// RevokeAllAgentTokens marks every live agent token of accountID's revoked. It is the
// agent_tokens half of RevokeAccountEverywhere: the "lineage" revocation entry already stops
// those tokens verifying, but the ROW is what the owner reads in Settings, so without this a
// password change or a "log out everywhere" would leave a list of tokens that all still say
// "Active" while none of them work. No revocations row is written per token — the account's
// lineage entry covers them all — so this is one statement, not one per token.
func (s *Service) RevokeAllAgentTokens(ctx context.Context, accountID string) error {
	_, err := s.st.Pool().Exec(ctx,
		`UPDATE agent_tokens SET revoked_at = now() WHERE account_id = $1 AND revoked_at IS NULL`,
		accountID)
	return err
}

// AgentTokenLive reports whether id is a live token of accountID's — present, unrevoked and
// unexpired — and stamps last_used_at when it is. This is the liveness gate the internal
// credential endpoints use in place of a live human session for an agent-token session: it is
// NOT token verification (the signature and the revocations table do that), it is the
// "this credential's owner still stands behind this token" check that bounds the blast radius
// of a leaked internal key exactly the way the human-session check does.
//
// A not-live token is (false, nil): an ordinary expected outcome, not an error.
func (s *Service) AgentTokenLive(ctx context.Context, accountID, id string) (bool, error) {
	if !agentTokenIDShape.MatchString(id) || !agentTokenIDShape.MatchString(accountID) {
		return false, nil
	}
	var live bool
	err := s.st.Pool().QueryRow(ctx,
		`UPDATE agent_tokens t SET last_used_at = now()
		  WHERE t.id = $1 AND t.account_id = $2 AND t.revoked_at IS NULL AND t.expires_at > now()
		    AND EXISTS (SELECT 1 FROM accounts a WHERE a.id = t.account_id AND a.disabled_at IS NULL)
		  RETURNING true`, id, accountID).Scan(&live)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return live, nil
}

// intersectCaps returns the capabilities present in BOTH lists, in want's order. An empty
// result is legal (and means a token that can authenticate but do nothing) rather than an
// error: it is the honest consequence of a role that has none of the preset's capabilities,
// and failing the mint instead would leak which capabilities a role carries.
func intersectCaps(want, have []string) []string {
	has := make(map[string]bool, len(have))
	for _, c := range have {
		has[c] = true
	}
	out := []string{}
	for _, c := range want {
		if has[c] {
			out = append(out, c)
		}
	}
	return out
}
