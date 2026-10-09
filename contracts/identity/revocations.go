package identity

import "time"

// IssuedAtRevocationChecker is the timestamp-aware extension of RevocationChecker.
// Verify prefers it when the checker implements it: a "sub", "lineage" or "sid" revocation
// then applies only to tokens issued at or before the revocation's revoked_at, so a
// token minted AFTER the revocation (a re-login following a password change, logout-all,
// theft detection or reconcile re-enable) verifies even against a consumer whose cached
// revocation list still carries the entry. "kid" revocations stay unconditional — a
// compromised signing key invalidates every token it ever signed.
//
// A checker that implements only RevocationChecker keeps the legacy unconditional
// semantics, which is the fail-closed direction.
type IssuedAtRevocationChecker interface {
	RevocationChecker
	// RevokedFor reports whether a token with the given kid/lineage/sub/sid and iat (unix
	// seconds) is revoked.
	RevokedFor(kid, lineage, sub, sid string, issuedAt int64) bool
}

// RevocationSet is the shared in-memory shape of core's revocation list: "kind:value" →
// revoked_at. It is what core's own snapshot checker and the board/runner coreauth
// clients build from GET /revocations, so the "which tokens does an entry apply to"
// rule lives in exactly one place. A zero revoked_at (an entry from a core that predates
// the timestamp, or one whose JSON omitted it) revokes unconditionally.
type RevocationSet map[string]time.Time

// Add records one entry. revokedAt may be the zero time (unconditional).
func (s RevocationSet) Add(kind, value string, revokedAt time.Time) {
	s[kind+":"+value] = revokedAt
}

// Revoked implements RevocationChecker's legacy, unconditional check: any matching
// entry revokes regardless of when the token was issued.
func (s RevocationSet) Revoked(kid, lineage, sub, sid string) bool {
	if _, ok := s["kid:"+kid]; ok {
		return true
	}
	if lineage != "" {
		if _, ok := s["lineage:"+lineage]; ok {
			return true
		}
	}
	if sub != "" {
		if _, ok := s["sub:"+sub]; ok {
			return true
		}
	}
	if sid != "" {
		if _, ok := s["sid:"+sid]; ok {
			return true
		}
	}
	return false
}

// RevokedFor implements IssuedAtRevocationChecker: kid entries are unconditional;
// lineage, sub and sid entries apply to tokens whose iat is at or before revoked_at
// (equality fails closed, as does a missing iat, since 0 <= anything).
func (s RevocationSet) RevokedFor(kid, lineage, sub, sid string, issuedAt int64) bool {
	if _, ok := s["kid:"+kid]; ok {
		return true
	}
	if lineage != "" {
		if at, ok := s["lineage:"+lineage]; ok && revocationApplies(at, issuedAt) {
			return true
		}
	}
	if sub != "" {
		if at, ok := s["sub:"+sub]; ok && revocationApplies(at, issuedAt) {
			return true
		}
	}
	// sid: one browser session (rotation chain), the scope of a detected refresh-token reuse.
	if sid != "" {
		if at, ok := s["sid:"+sid]; ok && revocationApplies(at, issuedAt) {
			return true
		}
	}
	return false
}

// revocationApplies is the single comparison rule: a zero revoked_at revokes everything;
// otherwise the token is revoked iff it was issued at or before the revocation (whole
// seconds, iat's own granularity — a token minted later in the same second as the
// revocation is treated as revoked, never the other way round).
func revocationApplies(revokedAt time.Time, issuedAt int64) bool {
	if revokedAt.IsZero() {
		return true
	}
	return issuedAt <= revokedAt.Unix()
}
