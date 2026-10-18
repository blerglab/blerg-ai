package coreauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/contracts/identity"
)

func TestOneCardApprovalFailsClosedOnStaleRevocationCache(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := boardfiling.Capability("11111111-1111-4111-8111-111111111111", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient("https://core.example.test")
	c.keys = identity.KeySet{"core-1": pub}
	c.revAt = time.Now()
	claims := identity.Claims{Sub: "11111111-1111-4111-8111-111111111111", Kind: "agent", Aud: "blerg-board", Project: "22222222-2222-4222-8222-222222222222", OnBehalfOf: "33333333-3333-4333-8333-333333333333", Lineage: "33333333-3333-4333-8333-333333333333", Caps: []string{capability}, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(30 * time.Second).Unix()}
	token := mint(t, priv, "core-1", claims)
	if _, err := c.Verify(token); err != nil {
		t.Fatalf("fresh revocation view refused valid approval: %v", err)
	}
	for _, at := range []time.Time{time.Now().Add(-revStaleCeiling - time.Second), {}} {
		c.revAt = at
		if _, err := c.Verify(token); !errors.Is(err, identity.ErrFailClosed) {
			t.Fatalf("stale revocation view accepted one-card write: %v", err)
		}
	}
	claims.Caps = []string{"card.read"}
	if _, err := c.Verify(mint(t, priv, "core-1", claims)); err != nil {
		t.Fatal("read-only stale policy changed while adding one-card write classification")
	}
}
