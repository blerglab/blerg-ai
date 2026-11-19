package livedata

import (
	"net/http"
	"strings"
)

type RunnerIdentity struct{ Installation, Runner string }

// AuthenticatedRunner uses only a TLS-verified client certificate, never proxy
// headers or request scope. Provision this dedicated identity outside host .env.
func AuthenticatedRunner(r *http.Request) (RunnerIdentity, error) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
		return RunnerIdentity{}, ErrDenied
	}
	leaf := r.TLS.VerifiedChains[0][0]
	if len(leaf.URIs) != 1 {
		return RunnerIdentity{}, ErrDenied
	}
	uri := leaf.URIs[0]
	parts := strings.Split(strings.Trim(uri.Path, "/"), "/")
	if uri.Scheme != "spiffe" || uri.Host != "blerg" || uri.RawQuery != "" || uri.Fragment != "" || len(parts) != 4 || parts[0] != "installation" || parts[2] != "runner" {
		return RunnerIdentity{}, ErrDenied
	}
	s := Scope{Installation: parts[1], Runner: parts[3], Owner: parts[1], Binding: parts[1], Connection: parts[1], Tool: "identity", Hash: strings.Repeat("0", 64), Digest: strings.Repeat("0", 64), Revision: 1, OwnerEpoch: 1, AuthorityEpoch: 1, ConnectionGeneration: 1}
	if !s.Valid() {
		return RunnerIdentity{}, ErrDenied
	}
	return RunnerIdentity{parts[1], parts[3]}, nil
}
