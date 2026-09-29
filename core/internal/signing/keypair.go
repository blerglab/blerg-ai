package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

type KeyPair struct {
	Kid  string
	Priv ed25519.PrivateKey
	Pub  ed25519.PublicKey
}

func GenerateKeyPair() (KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, err
	}
	sum := sha256.Sum256(pub)
	return KeyPair{Kid: base64.RawURLEncoding.EncodeToString(sum[:8]), Priv: priv, Pub: pub}, nil
}
