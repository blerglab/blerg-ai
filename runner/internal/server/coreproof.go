package server

import (
	"context"
	"errors"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

// coreProof names WHICH live thing authorises a call to blerg-core's internal endpoints
// (/internal/credentials/fetch, /internal/credentials/list, /internal/plugins/list): exactly one
// of the agent token the session runs as, or the human browser session (the `sid` claim of the
// verified access token the launch request carried) that started it. Core checks that specific
// token/session is live and belongs to the account, so an account uuid alone is never enough.
//
// There is no fallback: a call with neither (a token minted before core started stamping `sid`,
// a legacy path) fails closed with errNoLivenessProof rather than asking core "any live session".
type coreProof struct {
	TokenID   string
	SessionID string
}

// valid reports whether exactly one proof is named, the only shape core accepts.
func (p coreProof) valid() bool { return (p.TokenID != "") != (p.SessionID != "") }

// errNoLivenessProof is what a session start, a credential listing or a resume answers when the
// caller's access token carries no session id. Those tokens live at most ten minutes (core's
// access-token TTL), so the message points at the one thing that fixes it: a fresh token.
var errNoLivenessProof = errors.New("your sign-in predates a blerg update and cannot authorise use of your " +
	"personal credentials yet. Reload the page (or sign in again) to get a fresh session, then retry")

// proofFromPrincipal is the proof for a session launched from a browser-authenticated request:
// the session the verified token names. An agent-kind token proves itself by its own id.
func proofFromPrincipal(p identity.Principal) coreProof {
	if p.Kind == "agent" {
		return coreProof{TokenID: p.Sub}
	}
	return coreProof{SessionID: p.Sid}
}

type coreProofKey struct{}

// withCoreProof carries the request's proof down to the per-user credential/repo lookups, whose
// signatures (shared with the lister's cache and test hooks) take a context but no principal.
func withCoreProof(ctx context.Context, p coreProof) context.Context {
	return context.WithValue(ctx, coreProofKey{}, p)
}

// coreProofFrom returns the proof withCoreProof stored, or the zero (invalid) proof.
func coreProofFrom(ctx context.Context) coreProof {
	p, _ := ctx.Value(coreProofKey{}).(coreProof)
	return p
}
