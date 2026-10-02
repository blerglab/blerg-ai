// Package auth implements blerg-board's principals: human (a blerg-core-issued
// bearer token, verified via a.core.Verify and mapped to KindService with
// FromCore set — see corePrincipal), service (BLERG_BOARD_SERVICE_KEY,
// registered as a tokens row at boot so writes stay attributable), and agent
// (scoped board tokens). Unauthenticated requests are rejected — "human" is a
// positive assertion, never the absence of a token (see the design spec's
// gate-bypass correction).
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/board/internal/coreauth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Kind string

const (
	KindHuman   Kind = "human"
	KindService Kind = "service"
	KindAgent   Kind = "agent"
)

// Principal is the resolved identity of a request.
type Principal struct {
	Kind      Kind
	Token     *db.Token // set for service and agent
	SessionID string    // set for human

	// FromCore is true when this principal was resolved via blerg-core
	// (corePrincipal), rather than board's own session/service-key/token
	// paths. RequireBoard uses it to deny such principals the blanket
	// KindService bypass: that bypass exists only for board's native
	// trusted actors (the human session and the registered
	// BLERG_BOARD_SERVICE_KEY row, whose db.Token already carries the full
	// capability set) — a core-issued token must always be capability- and
	// board-scope-checked like an agent token, even when its Kind claim
	// (e.g. "service") would otherwise map here to KindService.
	FromCore bool
}

// EventMeta translates the principal into event attribution.
func (p Principal) EventMeta() db.EventMeta {
	m := db.EventMeta{Actor: string(p.Kind)}
	switch {
	case p.FromCore:
		// corePrincipal synthesizes Token.ID as "core:"+sub for display and
		// HasCap/RequireBoard bookkeeping — it is not a row in board's own
		// tokens table, so it can never satisfy actor_token_id's uuid FK
		// (card_events/admission_reviews REFERENCES tokens(id)). Attribute
		// it via the session column instead (also uuid-typed, like board's
		// own human sessions), using a stable hash of the synthesized ID so
		// the same core actor groups together in event history.
		if p.Token != nil {
			sid := coreActorSessionID(p.Token.ID)
			m.ActorSessionID = &sid
		}
	case p.Token != nil:
		m.ActorTokenID = &p.Token.ID
	}
	if p.SessionID != "" {
		sid := p.SessionID
		m.ActorSessionID = &sid
	}
	return m
}

// GateActorTokenID is the admission_reviews.actor_token_id value for this
// principal: the real tokens-table id for native token principals, nil for
// core-issued ones (their synthesized "core:"+sub id is not a uuid and not a
// row — see EventMeta). The gate's dedup queries treat nil as "no actor".
func (p Principal) GateActorTokenID() *string {
	if p.FromCore || p.Token == nil {
		return nil
	}
	id := p.Token.ID
	return &id
}

// coreActorSessionID derives a stable, uuid-shaped attribution id from a
// core-issued principal's synthesized Token.ID (e.g. "core:sub"):
// actor_session_id is a uuid column (see migrations/001_init.sql), which
// rejects that string as-is, but Postgres's uuid input accepts 32 bare hex
// digits — a truncated SHA-256 of the id satisfies that and stays stable per
// actor, so repeated writes from the same core sub group together in event
// history.
func coreActorSessionID(tokenID string) string {
	sum := sha256.Sum256([]byte(tokenID))
	return hex.EncodeToString(sum[:16])
}

type Auth struct {
	pool       *pgxpool.Pool
	serviceKey string

	// core is set (via SetCore) when BLERG_CORE_URL is configured, making
	// blerg-core the identity/discovery authority for bearer tokens that
	// aren't board's own opaque tokens or the service key. Nil ⇒ inert;
	// board behaves exactly as it did standalone.
	core *coreauth.Client
}

// SetCore wires blerg-core as an additional (additive, tried last) source of
// truth for bearer-token authentication. Native service-key/agent token
// paths are always tried first and are unaffected by this.
func (a *Auth) SetCore(c *coreauth.Client) {
	a.core = c
}

// ErrNoCore: an operation needs blerg-core (BLERG_CORE_URL) and none is wired.
var ErrNoCore = errors.New("blerg-core is not configured (BLERG_CORE_URL)")

// Core is the wired blerg-core client, or nil when board runs standalone.
func (a *Auth) Core() *coreauth.Client { return a.core }

// VerifyAgentToken verifies a blerg-core agent token board HOLDS (not one a
// request presented) for audience aud. ErrNoCore when core isn't wired.
func (a *Auth) VerifyAgentToken(raw, aud string) (coreauth.AgentToken, error) {
	if a.core == nil {
		return coreauth.AgentToken{}, ErrNoCore
	}
	return a.core.VerifyAgentToken(raw, aud)
}

// HumanAccountID is the blerg-core account id of a signed-in human (the core
// token's sub), or "" for every other principal.
func (p Principal) HumanAccountID() string {
	if !p.IsHuman() || !p.FromCore || p.Token == nil {
		return ""
	}
	return strings.TrimPrefix(p.Token.ID, "core:")
}

// New wires auth. serviceKey empty ⇒ no service tier. Human auth is entirely
// blerg-core's: board no longer has a native password/session scheme (see
// corePrincipal) — SetCore must be called for humans to authenticate at all.
func New(pool *pgxpool.Pool, serviceKey string) *Auth {
	return &Auth{pool: pool, serviceKey: serviceKey}
}

// EnsureServiceToken registers the env service key as a tokens row so writes
// made with it carry an actor_token_id (attribution, not just access).
func (a *Auth) EnsureServiceToken(ctx context.Context) error {
	if a.serviceKey == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(a.serviceKey))
	_, err := a.pool.Exec(ctx, `
		INSERT INTO tokens (kind, label, token_hash, capabilities, lineage_id)
		VALUES ('service', 'env service key', $1,
			'{card.read,card.write,column.write,board.admin,runner.report,gate.bypass}',
			gen_random_uuid())
		ON CONFLICT (token_hash) DO NOTHING`, sum[:])
	return err
}

var ErrUnauthenticated = errors.New("unauthenticated")

// Resolve identifies the request's principal from
// bearer/`X-Blerg-Board-Token`: board's own service key or agent tokens, or
// (additively) a blerg-core-issued token, including for humans — see
// corePrincipal. No credential → error.
func (a *Auth) Resolve(r *http.Request) (Principal, error) {
	raw := ""
	if h := r.Header.Get("Authorization"); h != "" {
		if t, ok := strings.CutPrefix(h, "Bearer "); ok {
			raw = t
		}
	}
	if raw == "" {
		raw = r.Header.Get("X-Blerg-Board-Token")
	}
	if raw == "" {
		return Principal{}, ErrUnauthenticated
	}
	if a.serviceKey != "" && subtle.ConstantTimeCompare([]byte(raw), []byte(a.serviceKey)) == 1 {
		// Resolve the registered row for attribution.
		tok, err := db.ValidateToken(r.Context(), a.pool, raw)
		if err != nil {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{Kind: KindService, Token: &tok}, nil
	}
	tok, err := db.ValidateToken(r.Context(), a.pool, raw)
	if err == nil {
		kind := KindAgent
		// Invariant: the only tokens row with kind 'service' is the env key
		// registered by EnsureServiceToken — every mint path (handleMintToken,
		// spawn, board sessions, standing agents) writes kind 'agent'. So a
		// KindService principal here is the native key (IsNativeService).
		if tok.Kind == "service" {
			kind = KindService
		}
		return Principal{Kind: kind, Token: &tok}, nil
	}

	// ADDITIVE: raw isn't board's own opaque token (those are always
	// "blergb_"-prefixed, and already tried above) or the service key — try
	// blerg-core as the identity/discovery authority. Inert (a.core == nil)
	// unless BLERG_CORE_URL is configured.
	if a.core != nil && !strings.HasPrefix(raw, "blergb_") {
		if synth, verr := a.core.Verify(raw); verr == nil {
			return corePrincipal(synth), nil
		}
	}

	return Principal{}, ErrUnauthenticated
}

// corePrincipal maps a verified blerg-core token into a board-internal
// Principal: a service-or-agent principal carrying the core token's
// capabilities via a synthesized (not DB-backed) *db.Token, so downstream
// code (RequireBoard, EventMeta) treats it like any native token principal.
// Core tokens never become KindHuman: a bearer token is not a login session.
//
// FromCore is always set here: RequireBoard uses it to make sure a core
// token's Kind claim ("service", "human", or anything other than "agent",
// which all map to KindService below) can never reach board's native
// KindService bypass — see RequireBoard and the FromCore doc comment.
func corePrincipal(s coreauth.Synthesized) Principal {
	kind := KindService
	if s.Kind == "agent" {
		kind = KindAgent
	}
	tok := &db.Token{
		ID:           "core:" + s.Sub,
		Kind:         s.Kind,
		Label:        "blerg-core: " + s.Sub,
		Capabilities: s.Caps,
		LineageID:    s.Lineage,
	}
	// A core-issued token of ANY kind carrying a non-empty Project claim is
	// scoped to that board: for the blerg-board audience, Project is the board
	// id (the cron/board exchange mints it so). Setting BoardID makes every
	// existing board-scope check (requireCapability, RequireGlobalRead, the
	// list/search narrowing) treat it like a board-scoped native agent token.
	// A malformed Project fails closed: the token is scoped to a board id that
	// exists nowhere and carries no capabilities, never left unscoped. Tokens
	// without a Project claim are never narrowed.
	if s.Project != "" {
		board := s.Project
		if !validProject(board) {
			board = invalidProjectBoard
			tok.Capabilities = nil
		}
		tok.BoardID = &board
	}
	return Principal{Kind: kind, FromCore: true, Token: tok}
}

// invalidProjectBoard is the board a token with a malformed Project claim is
// scoped to. It is not a uuid, so it names no board, and its token also holds
// no capabilities.
const invalidProjectBoard = "\x00invalid-project"

// validProject: a Project claim must be non-empty, at most 64 bytes, with no
// control characters.
func validProject(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}

// ProjectScoped reports the single board a core-issued token is scoped to
// (its Project claim, any token kind). It is false for every other principal,
// including native board-scoped agent tokens, whose behaviour is unchanged.
// The REST and MCP surfaces use it for their uniform not-found pre-check.
func (p Principal) ProjectScoped() (boardID string, ok bool) {
	if !p.FromCore || p.Token == nil || p.Token.BoardID == nil {
		return "", false
	}
	return *p.Token.BoardID, true
}

// RequireBoard checks that a principal is authorized for capability on
// boardID.
//
// Board's own trusted actor bypasses this check entirely: the native
// BLERG_BOARD_SERVICE_KEY, registered at boot as a tokens row carrying the
// full capability set (see EnsureServiceToken) — trusting it here is no
// broader than trusting the env var itself. There is no human bypass: board
// has no native human principal any more (see corePrincipal), and a
// blerg-core-issued human token is capability-checked exactly like every
// other core-issued principal, below.
//
// Every other principal — every agent token, and every core-issued
// principal regardless of the Kind its core Claims.Kind mapped to — is
// always capability- and board-scope-checked. This matters because
// corePrincipal maps any non-"agent" core Kind (service/human/unknown) to
// KindService: without the FromCore guard below, such a token would silently
// inherit the native service key's blanket bypass and get full board-admin
// on every board regardless of its actual capability claims.
//
// Board scope: native agent tokens carry an explicit BoardID and are rejected
// outside it. Core-issued principals are capability-gated and instance-wide
// (checked against every board's required capability, not narrowed) EXCEPT a
// core-issued AGENT token with a Project claim: corePrincipal maps that claim
// (a board id) onto BoardID, so it is narrowed to that one board exactly like
// a native agent token (see ProjectScoped).
func (p Principal) RequireBoard(boardID, capability string) error {
	if p.IsNativeService() {
		return nil
	}
	return p.requireCapability(boardID, capability)
}

// IsHuman reports whether this principal represents an actual human user —
// via a blerg-core-issued token whose original Kind claim was "human"
// (corePrincipal maps it to KindService+FromCore but preserves the raw claim
// in Token.Kind), or (retained for source compatibility; nothing produces
// this any more) a native KindHuman principal. Used for the handful of
// checks that are about *who* is acting rather than *what they're allowed to
// do* — the admission gate's human bypass, and human-only actions like board
// deletion and held-review resolution — which must not be satisfied by a
// core-issued non-human principal (e.g. Kind claim "service") just because
// it also maps to KindService.
func (p Principal) IsHuman() bool {
	return p.Kind == KindHuman || (p.FromCore && p.Token != nil && p.Token.Kind == "human")
}

// IsNativeService is board's ONLY blanket-trust principal: the env
// BLERG_BOARD_SERVICE_KEY. A core-issued token whose Kind claim mapped to
// KindService is never native (FromCore) and is always capability-checked.
//
// Every privileged handler gates on this model — IsNativeService, IsAdmin,
// RequireAdmin, RequireGlobalRead — applied to ALL principals. Gating on
// `p.Kind == KindAgent` alone is the bug this replaces: corePrincipal maps a
// core "member" to KindService, so such a gate treated members as the
// service tier (audit C1).
func (p Principal) IsNativeService() bool { return p.Kind == KindService && !p.FromCore }

// IsAdmin reports blanket admin rights: the native service key, or any
// principal whose capability set carries board.admin. It ignores board
// scope — use RequireAdmin when a board is in play. This is NOT generally
// interchangeable with RequireAdmin(boardID): a board-scoped agent token
// with board.admin on board A would read IsAdmin()==true even on board B,
// where RequireAdmin("B") denies it. It is safe as the source of /api/me's
// instance-wide is_admin (consumed for board-scoped UI gating, e.g. per-card
// Accept/Reject) only because core-issued principals — the only ones humans
// or member/admin roles actually authenticate as — never carry a board
// scope to begin with (only a core AGENT token with a Project claim does,
// see RequireBoard): for them IsAdmin() and RequireAdmin(anyBoardID) agree.
// A board-scoped agent token (native, or core-issued with a Project claim)
// is the one case where they can diverge, and agents don't consume /api/me
// for UI decisions.
func (p Principal) IsAdmin() bool {
	return p.IsNativeService() || (p.Token != nil && p.Token.HasCap("board.admin"))
}

// RequireAdmin is RequireBoard(boardID, "board.admin") with the native
// service-key bypass. boardID "" means "instance-wide" (create board, list
// tokens): a board-scoped agent token is rejected there because its BoardID
// never equals "".
func (p Principal) RequireAdmin(boardID string) error {
	if p.IsNativeService() {
		return nil
	}
	return p.requireCapability(boardID, "board.admin")
}

// RequireGlobalRead gates cross-board reads (overview, global sessions, the
// unfiltered review list): native ok; a board-scoped token is rejected;
// otherwise card.read.
func (p Principal) RequireGlobalRead() error {
	if p.IsNativeService() {
		return nil
	}
	if p.Token != nil && p.Token.BoardID != nil {
		return fmt.Errorf("token is scoped to a single board")
	}
	return p.requireCapability("", "card.read")
}

// requireCapability is the non-bypassable check: board scope (when the
// token carries one) plus the requested capability. Used for native agent
// tokens and for every core-issued principal (agent-kind or not).
func (p Principal) requireCapability(boardID, capability string) error {
	if p.Token == nil {
		return ErrUnauthenticated
	}
	if p.Token.BoardID != nil && *p.Token.BoardID != boardID {
		return fmt.Errorf("token not scoped to this board")
	}
	if capability != "" && !p.Token.HasCap(capability) {
		return fmt.Errorf("token lacks capability %s", capability)
	}
	return nil
}
