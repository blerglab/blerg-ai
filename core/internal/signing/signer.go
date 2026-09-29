package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

func (kp KeyPair) Sign(c identity.Claims) (string, error) {
	hdr, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kp.Kid})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signingInput := identity.EncodeSigningInput(hdr, payload)
	sig := ed25519.Sign(kp.Priv, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
