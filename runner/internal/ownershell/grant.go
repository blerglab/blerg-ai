// Package ownershell implements the owner-only shell channel independently of agent terminals.
package ownershell

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

var ErrDenied = errors.New("shell unavailable")
var ErrBusy = errors.New("shell busy")
var ErrLimit = errors.New("shell limit exceeded")

// ErrGrantClock is a grant that is valid in every respect except that it was issued
// further in the future than GrantClockSkew allows: the runtime's clock is behind the
// runner's. It still satisfies errors.Is(err, ErrDenied).
var ErrGrantClock = fmt.Errorf("%w: grant issued in the future", ErrDenied)

// ErrGrantExpired is an otherwise valid grant whose expiry has passed on this clock.
// Expiry alone says nothing about clock skew. It satisfies errors.Is(err, ErrDenied).
var ErrGrantExpired = fmt.Errorf("%w: grant expired", ErrDenied)

// GrantClockSkew is how far, in seconds, the runtime's clock may lag the runner's when
// checking a grant's issued-at. A desktop clock a second or two behind (common under
// WSL) must not refuse every shell. The tolerance applies once, to issued-at only:
// expiry is strict on the runtime clock, so a runtime at most GrantClockSkew behind
// keeps a grant at most ShellGrantSeconds+GrantClockSkew (35 s) of real time, and a
// runtime ahead keeps it shorter.
const GrantClockSkew int64 = 5

// SignedGrant is an authenticated envelope around the unchanged P0 grant.
// Keys are ephemeral runner keys provisioned through the authenticated runtime connection.
// A reconnect must pin a new key and connection ID before accepting any grant.
type SignedGrant = protocol.OwnerShellSignedGrant

func SignGrant(key ed25519.PrivateKey, g protocol.ShellGrant) SignedGrant {
	b, _ := json.Marshal(g)
	return SignedGrant{Grant: g, Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, b))}
}

// VerifyGrant must run locally on every runtime operation, including output, and on a timer.
// expected comes from trusted runtime launch state, never from the incoming envelope.
func VerifyGrant(key ed25519.PublicKey, signed SignedGrant, expected protocol.ShellGrant, revision uint64, now time.Time) error {
	g := signed.Grant
	b, _ := json.Marshal(g)
	sig, err := base64.RawURLEncoding.DecodeString(signed.Signature)
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, b, sig) {
		return ErrDenied
	}
	if g.Version != 1 || g.PolicyRevision != revision || revision == 0 || g.IssuedAt <= 0 || g.ExpiresAt <= g.IssuedAt || g.ExpiresAt-g.IssuedAt > protocol.ShellGrantSeconds {
		return ErrDenied
	}
	if g.Audience == "" || g.OwnerID == "" || g.LoginID == "" || g.SessionID == "" || g.LaunchGeneration == "" || g.ConnectionID == "" || g.ShellGeneration == "" {
		return ErrDenied
	}
	if g.Audience != expected.Audience || g.OwnerID != expected.OwnerID || g.LoginID != expected.LoginID || g.SessionID != expected.SessionID || g.LaunchGeneration != expected.LaunchGeneration || g.ConnectionID != expected.ConnectionID || g.ShellGeneration != expected.ShellGeneration || g.Runtime != expected.Runtime || g.ConsentID != expected.ConsentID {
		return ErrDenied
	}
	if g.Runtime != "host" && g.Runtime != "sandbox" && g.Runtime != "cluster" {
		return ErrDenied
	}
	if g.Runtime == "host" && g.ConsentID == "" {
		return ErrDenied
	}
	// The time window is checked last so ErrGrantClock and ErrGrantExpired are only
	// ever returned for a grant that is otherwise authentic and bound to this launch.
	if g.IssuedAt > now.Unix()+GrantClockSkew {
		return ErrGrantClock
	}
	if g.ExpiresAt <= now.Unix() {
		return ErrGrantExpired
	}
	return nil
}
