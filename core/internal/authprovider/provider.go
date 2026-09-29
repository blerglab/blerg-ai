// Package authprovider defines the pluggable login-mechanism interface used
// by core's human authentication path, and its "local" (password) provider.
package authprovider

import (
	"context"
	"net/http"
)

// Account is the resolved identity a Provider.Callback returns on a
// successful login.
type Account struct {
	ID, Provider, ProviderSubject, Email string
}

// Provider is one pluggable login mechanism (local/github/oidc). Exactly one
// is active per process, selected by BLERG_CORE_AUTH_PROVIDER — this is NOT
// a multi-entry registry like runner's EngineSpec map; only ID()'s owner is
// ever constructed.
type Provider interface {
	ID() string
	LoginURL(state string) string
	// Callback must fail closed: any ambiguous upstream error (IdP
	// unreachable, malformed response, wrong password) is a login failure,
	// never a silent allow.
	Callback(ctx context.Context, r *http.Request) (Account, error)
}
