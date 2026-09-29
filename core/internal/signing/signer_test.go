package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

func TestSignProducesVerifiableEdDSAToken(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	tok, err := kp.Sign(identity.Claims{Sub: "u1", Aud: "blerg-board", Kind: "agent", ExpiresAt: 1})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	// Header pins EdDSA + kid.
	hdrBytes, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var hdr map[string]string
	_ = json.Unmarshal(hdrBytes, &hdr)
	if hdr["alg"] != "EdDSA" || hdr["kid"] != kp.Kid {
		t.Fatalf("header = %v, want alg=EdDSA kid=%s", hdr, kp.Kid)
	}
	// Signature verifies against the public key over "header.payload".
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	signingInput := parts[0] + "." + parts[1]
	if !ed25519.Verify(kp.Pub, []byte(signingInput), sig) {
		t.Fatal("signature does not verify")
	}
}
