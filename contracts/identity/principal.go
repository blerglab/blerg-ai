package identity

import "crypto/ed25519"

type PrincipalKind string

const (
	Human   PrincipalKind = "human"
	Service PrincipalKind = "service"
	Agent   PrincipalKind = "agent"
)

type Principal struct{ Claims }

func (p Principal) Has(capability string) bool {
	for _, c := range p.Caps {
		if c == capability {
			return true
		}
	}
	return false
}

type KeySet map[string]ed25519.PublicKey

// RevocationChecker is the minimal revocation view Verify consults. Revoked is the
// unconditional check (any matching kid/lineage/sub entry revokes the token, whenever it
// was issued); a checker that also implements IssuedAtRevocationChecker (revocations.go)
// gets the timestamp-aware check instead.
type RevocationChecker interface {
	Revoked(kid, lineage, sub, sid string) bool
	StaleBeyondCeiling() bool
}
