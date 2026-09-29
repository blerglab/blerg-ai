package identity

import "encoding/base64"

// EncodeSigningInput builds the "header.payload" string that gets signed.
func EncodeSigningInput(header, payload []byte) string {
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
}

// Claims is the payload of a Blerg capability token (§4.1).
type Claims struct {
	Sub        string   `json:"sub"`                    // principal id
	Aud        string   `json:"aud"`                    // target component; rejects cross-component replay
	Project    string   `json:"project,omitempty"`      // project scope (agent tokens)
	Kind       string   `json:"kind"`                   // "human" | "service" | "agent"
	Caps       []string `json:"caps,omitempty"`         // capability list (authorization source of truth)
	OnBehalfOf string   `json:"on_behalf_of,omitempty"` // attribution only, never authorization
	Lineage    string   `json:"lineage,omitempty"`      // stable across refresh
	Sid        string   `json:"sid,omitempty"`          // human access tokens: the browser session (rotation chain) that minted it
	IssuedAt   int64    `json:"iat"`
	ExpiresAt  int64    `json:"exp"`
}
