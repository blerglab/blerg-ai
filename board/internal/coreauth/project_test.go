package coreauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

// Synthesize carries the verified Project claim through; a token without one
// yields the empty string (so nothing is narrowed by accident).
func TestSynthesizeCarriesProject(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := identity.KeySet{"core-1": pub}
	now := time.Now().Unix()

	for _, project := range []string{"board-uuid-1", ""} {
		tok := mint(t, priv, "core-1", identity.Claims{
			Sub: "agent-1", Aud: "blerg-board", Kind: "agent", Project: project,
			Caps: []string{"card.read"}, ExpiresAt: now + 60,
		})
		p, err := identity.Verify(tok, "blerg-board", keys, fakeRev{}, SensitiveCaps)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if got := Synthesize(p).Project; got != project {
			t.Errorf("Synthesize().Project = %q, want %q", got, project)
		}
	}
}
