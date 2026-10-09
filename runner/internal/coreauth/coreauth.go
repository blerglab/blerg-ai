// Package coreauth lets blerg-runner accept blerg-core-issued identity tokens
// alongside its static BLERG_RUNNER_KEY. It fetches and caches blerg-core's
// JWKS and revocation list so runner/internal/server can call
// identity.Verify without hitting the network on every request.
package coreauth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

const (
	jwksRefreshInterval        = 5 * time.Minute
	revocationsRefreshInterval = 60 * time.Second
	// staleCeiling is how long the revocation list can go unrefreshed before
	// RevocationChecker.StaleBeyondCeiling() starts fail-closing sensitive caps.
	staleCeiling = 5 * time.Minute
	httpTimeout  = 10 * time.Second
)

// revocationEntry mirrors core's GET /revocations rows. RevokedAt (RFC3339)
// scopes sub/lineage entries to tokens issued at or before it; a core that
// omits it leaves the zero time, which identity.RevocationSet treats as
// "revoke everything" — backward-safe.
type revocationEntry struct {
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	RevokedAt time.Time `json:"revoked_at"`
}

// Client polls blerg-core for its signing keys and revocation list, and
// implements identity.RevocationChecker for use with identity.Verify.
type Client struct {
	baseURL string
	http    *http.Client

	mu            sync.RWMutex
	keys          identity.KeySet
	revoked       identity.RevocationSet // kind+":"+value → revoked_at
	revocationsAt time.Time
	jwksAt        time.Time
	jwksErrLogged bool
	revErrLogged  bool
}

// New creates a client against blerg-core at baseURL (e.g. $BLERG_CORE_URL)
// and starts its background refresh loops. Returns nil if baseURL is empty.
func New(baseURL string) *Client {
	if baseURL == "" {
		return nil
	}
	c := &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: httpTimeout},
		keys:    identity.KeySet{},
		revoked: identity.RevocationSet{},
	}
	c.refreshJWKS()
	c.refreshRevocations()
	go c.loop(jwksRefreshInterval, c.refreshJWKS)
	go c.loop(revocationsRefreshInterval, c.refreshRevocations)
	return c
}

func (c *Client) loop(interval time.Duration, fn func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		fn()
	}
}

// KeySet returns the current cached JWKS, safe for concurrent use with the
// background refresh loop (identity.Verify only reads from it).
func (c *Client) KeySet() identity.KeySet {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(identity.KeySet, len(c.keys))
	for k, v := range c.keys {
		out[k] = v
	}
	return out
}

func (c *Client) refreshJWKS() {
	var raw map[string]string
	if err := c.getJSON("/.well-known/jwks", &raw); err != nil {
		if !c.jwksErrLogged {
			log.Printf("coreauth: jwks fetch failed (will retry): %v", err)
			c.jwksErrLogged = true
		}
		return
	}
	keys := make(identity.KeySet, len(raw))
	for kid, b64 := range raw {
		pub, err := base64.RawURLEncoding.DecodeString(b64)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			log.Printf("coreauth: skipping malformed jwks entry kid=%s", kid)
			continue
		}
		keys[kid] = ed25519.PublicKey(pub)
	}
	c.mu.Lock()
	c.keys = keys
	c.jwksAt = time.Now()
	c.jwksErrLogged = false
	c.mu.Unlock()
}

func (c *Client) refreshRevocations() {
	var entries []revocationEntry
	if err := c.getJSON("/revocations", &entries); err != nil {
		if !c.revErrLogged {
			log.Printf("coreauth: revocations fetch failed (will retry): %v", err)
			c.revErrLogged = true
		}
		return
	}
	revoked := make(identity.RevocationSet, len(entries))
	for _, e := range entries {
		revoked.Add(e.Kind, e.Value, e.RevokedAt)
	}
	c.mu.Lock()
	c.revoked = revoked
	c.revocationsAt = time.Now()
	c.revErrLogged = false
	c.mu.Unlock()
}

func (c *Client) getJSON(path string, v any) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return &httpStatusError{path: path, status: resp.StatusCode}
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

type httpStatusError struct {
	path   string
	status int
}

func (e *httpStatusError) Error() string {
	return "unexpected status " + http.StatusText(e.status) + " for " + e.path
}

// Revoked implements identity.RevocationChecker (unconditional; identity.Verify
// prefers RevokedFor below).
func (c *Client) Revoked(kid, lineage, sub, sid string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.revoked.Revoked(kid, lineage, sub, sid)
}

// RevokedFor implements identity.IssuedAtRevocationChecker: a sub/lineage
// entry applies only to tokens issued at or before its revoked_at, so a token
// minted by a re-login after a password change / logout-all verifies here
// even before the next 60 s revocation poll drops the entry. kid entries stay
// unconditional.
func (c *Client) RevokedFor(kid, lineage, sub, sid string, issuedAt int64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.revoked.RevokedFor(kid, lineage, sub, sid, issuedAt)
}

// StaleBeyondCeiling implements identity.RevocationChecker: true once the
// revocation list hasn't refreshed within staleCeiling, causing
// identity.Verify to fail closed on sensitive capabilities.
func (c *Client) StaleBeyondCeiling() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.revocationsAt.IsZero() {
		return true
	}
	return time.Since(c.revocationsAt) > staleCeiling
}
