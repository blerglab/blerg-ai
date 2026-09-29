// Package coreauth makes blerg-board a consumer of blerg-core as the
// identity/discovery authority (§ control-plane integration): it fetches and
// caches core's JWKS and revocation list, verifies core-issued bearer tokens
// against the shared identity contract (contracts/identity), and registers
// blerg-board itself into core's component directory.
//
// Everything here is additive and degrades to absent: a nil/unconfigured
// Client means blerg-board keeps working exactly as it did standalone (see
// board/internal/auth's native token/session/service-key paths, which are
// tried first).
package coreauth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

const (
	jwksRefreshInterval = 5 * time.Minute
	revRefreshInterval  = 60 * time.Second
	// revStaleCeiling bounds how long a stale (unrefreshed) revocation view is
	// tolerated before identity.Verify fails closed on sensitive capabilities
	// (§4.1). Also the value returned as "stale" when we have never once
	// managed to fetch the revocation list at all.
	revStaleCeiling = 5 * time.Minute
)

// SensitiveCaps reports whether a capability must fail closed when core's
// revocation view is stale beyond the ceiling. Kept in sync by hand with the
// capabilities blerg-core treats as sensitive; board only needs to recognise
// the ones it can itself be asked to honor.
func SensitiveCaps(capability string) bool {
	switch capability {
	case "secrets.read", "session.start", "merge", "membership.write", "board.admin", "gate.bypass":
		return true
	default:
		return false
	}
}

// Synthesized is the board-agnostic shape a verified core Principal is
// reduced to. board/internal/auth maps this into its own Principal/db.Token
// types (kept here to avoid an import cycle: auth needs this package, so this
// package must not need auth's types).
type Synthesized struct {
	Sub        string
	Kind       string // "human" | "service" | "agent" (identity.PrincipalKind values)
	Caps       []string
	Lineage    string
	OnBehalfOf string
}

// Synthesize reduces a verified identity.Principal to the fields callers need
// to build a board-local principal.
func Synthesize(p identity.Principal) Synthesized {
	return Synthesized{
		Sub:        p.Sub,
		Kind:       p.Kind,
		Caps:       append([]string(nil), p.Caps...),
		Lineage:    p.Lineage,
		OnBehalfOf: p.OnBehalfOf,
	}
}

// Client caches blerg-core's JWKS and revocation list and verifies bearer
// tokens against them. It implements identity.RevocationChecker directly.
type Client struct {
	baseURL    string
	httpClient *http.Client

	keysMu sync.RWMutex
	keys   identity.KeySet

	revMu   sync.RWMutex
	revoked identity.RevocationSet
	revAt   time.Time // zero until the first successful fetch
}

// NewClient builds a Client against coreURL (e.g. "http://blerg-core:8080").
// Nothing is fetched until Start is called.
func NewClient(coreURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(coreURL, "/"),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		keys:       identity.KeySet{},
		revoked:    identity.RevocationSet{},
	}
}

// Start performs an initial fetch of both JWKS and revocations, then keeps
// them refreshed in the background until ctx is done.
func (c *Client) Start(ctx context.Context) {
	c.refreshJWKS(ctx)
	c.refreshRevocations(ctx)
	go c.loop(ctx, jwksRefreshInterval, c.refreshJWKS)
	go c.loop(ctx, revRefreshInterval, c.refreshRevocations)
}

func (c *Client) loop(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

func (c *Client) refreshJWKS(ctx context.Context) {
	raw, err := c.get(ctx, "/.well-known/jwks")
	if err != nil {
		log.Printf("coreauth: jwks refresh failed: %v", err)
		return
	}
	var enc map[string]string
	if err := json.Unmarshal(raw, &enc); err != nil {
		log.Printf("coreauth: jwks decode failed: %v", err)
		return
	}
	keys := make(identity.KeySet, len(enc))
	for kid, b64 := range enc {
		pub, err := base64.RawURLEncoding.DecodeString(b64)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			log.Printf("coreauth: jwks: skipping malformed key %q", kid)
			continue
		}
		keys[kid] = ed25519.PublicKey(pub)
	}
	c.keysMu.Lock()
	c.keys = keys
	c.keysMu.Unlock()
	log.Printf("coreauth: refreshed jwks (%d key(s))", len(keys))
}

// revocation mirrors core/internal/identity.Revocation's JSON shape. RevokedAt
// (RFC3339) scopes sub/lineage entries to tokens issued at or before it; a
// core that omits it (or sends null) leaves the zero time, which
// identity.RevocationSet treats as "revoke everything" — backward-safe.
type revocation struct {
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	RevokedAt time.Time `json:"revoked_at"`
}

func (c *Client) refreshRevocations(ctx context.Context) {
	raw, err := c.get(ctx, "/revocations")
	if err != nil {
		log.Printf("coreauth: revocations refresh failed: %v", err)
		return
	}
	var list []revocation
	if err := json.Unmarshal(raw, &list); err != nil {
		log.Printf("coreauth: revocations decode failed: %v", err)
		return
	}
	m := make(identity.RevocationSet, len(list))
	for _, r := range list {
		m.Add(r.Kind, r.Value, r.RevokedAt)
	}
	c.revMu.Lock()
	c.revoked = m
	c.revAt = time.Now()
	c.revMu.Unlock()
	log.Printf("coreauth: refreshed revocations (%d entr(y/ies))", len(list))
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, httpStatusError(resp.StatusCode)
	}
	return buf, nil
}

type httpStatusError int

func (e httpStatusError) Error() string {
	return "unexpected status from blerg-core: " + http.StatusText(int(e))
}

// KeySet returns the currently cached JWKS snapshot.
func (c *Client) KeySet() identity.KeySet {
	c.keysMu.RLock()
	defer c.keysMu.RUnlock()
	return c.keys
}

// Revoked implements identity.RevocationChecker (unconditional; identity.Verify
// prefers RevokedFor below).
func (c *Client) Revoked(kid, lineage, sub string) bool {
	c.revMu.RLock()
	defer c.revMu.RUnlock()
	return c.revoked.Revoked(kid, lineage, sub)
}

// RevokedFor implements identity.IssuedAtRevocationChecker: a sub/lineage
// entry applies only to tokens issued at or before its revoked_at, so a token
// minted by a re-login after a password change / logout-all verifies here
// even before the next 60 s revocation poll drops the entry. kid entries stay
// unconditional.
func (c *Client) RevokedFor(kid, lineage, sub string, issuedAt int64) bool {
	c.revMu.RLock()
	defer c.revMu.RUnlock()
	return c.revoked.RevokedFor(kid, lineage, sub, issuedAt)
}

// StaleBeyondCeiling implements identity.RevocationChecker. Never having
// fetched successfully counts as stale, so sensitive capabilities fail closed
// until the first fetch lands.
func (c *Client) StaleBeyondCeiling() bool {
	c.revMu.RLock()
	defer c.revMu.RUnlock()
	if c.revAt.IsZero() {
		return true
	}
	return time.Since(c.revAt) > revStaleCeiling
}

// AgentToken is a verified blerg-core AGENT token, reduced to what board
// needs to act as its owner: which token it is (core's agent_tokens id, the
// JWT's sub) and whose (on_behalf_of). Never carries the raw token.
type AgentToken struct {
	TokenID   string
	AccountID string
	Aud       string
	Caps      []string
	ExpiresAt time.Time
}

// Has reports whether the token carries capability c.
func (t AgentToken) Has(c string) bool {
	for _, have := range t.Caps {
		if have == c {
			return true
		}
	}
	return false
}

// ErrNotAgentToken: the token verified but is not a blerg-core agent token
// acting for an account (a human's access token, a service token, or an
// agent token with no owner) — the only kind a person mints for a tool.
var ErrNotAgentToken = errors.New("coreauth: not an agent token minted by a person")

// VerifyAgentToken verifies raw as a blerg-core-issued agent token for
// audience aud: signature, expiry and revocation against the cached JWKS and
// revocation list (no network), exactly like Verify. Used for tokens board
// HOLDS rather than receives — a board's automation token (aud blerg-runner),
// the gate's account token — to learn whose they are and whether they are
// still live.
func (c *Client) VerifyAgentToken(raw, aud string) (AgentToken, error) {
	p, err := identity.Verify(raw, aud, c.KeySet(), c, SensitiveCaps)
	if err != nil {
		return AgentToken{}, err
	}
	if p.Kind != "agent" || p.Sub == "" || p.OnBehalfOf == "" {
		return AgentToken{}, ErrNotAgentToken
	}
	return AgentToken{
		TokenID: p.Sub, AccountID: p.OnBehalfOf, Aud: p.Aud,
		Caps: append([]string(nil), p.Caps...), ExpiresAt: time.Unix(p.ExpiresAt, 0),
	}, nil
}

// Verify checks raw as a blerg-core-issued token for audience "blerg-board"
// and, on success, reduces it to a Synthesized principal.
func (c *Client) Verify(raw string) (Synthesized, error) {
	p, err := identity.Verify(raw, "blerg-board", c.KeySet(), c, SensitiveCaps)
	if err != nil {
		return Synthesized{}, err
	}
	return Synthesize(p), nil
}
