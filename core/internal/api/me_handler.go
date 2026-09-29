package api

import (
	"net/http"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
)

// meResponse is GET /api/me's success body — exactly the fields the SPA needs to show who is
// signed in. Deliberately excludes password_hash (and anything else from the accounts row not
// listed here): the SPA reads identity from this endpoint rather than decoding the access
// token client-side, since the token's claims (contracts/identity.Claims) carry only Sub — not
// provider/email/role/must_change_password.
type meResponse struct {
	AccountID          string `json:"account_id"`
	Provider           string `json:"provider"`
	ProviderSubject    string `json:"provider_subject"`
	Email              string `json:"email"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"must_change_password"`
}

// agentMeResponse is GET /api/me's body for an agent principal (spec §2): what this token is,
// whose account it acts for, and what it may do — the introspection an external tool uses to
// confirm a token works before it tries to do anything with it. Caps come from the token's own
// verified claims, so what is reported is exactly what will be enforced.
type agentMeResponse struct {
	Kind            string   `json:"kind"`
	TokenID         string   `json:"token_id"`
	AccountID       string   `json:"account_id"`
	ProviderSubject string   `json:"provider_subject"`
	Role            string   `json:"role"`
	Caps            []string `json:"caps"`
}

// handleMe is GET /api/me. Gated behind requirePrincipal in router.go, and branches on the
// principal's kind here: a human session gets meResponse, a user-minted `platform` agent token
// gets agentMeResponse, and anything else is refused. The account looked up is always the
// caller's own — Sub for a human, OnBehalfOf for an agent token, both from verified claims,
// never anything client-supplied.
func (d Deps) handleMe(w http.ResponseWriter, r *http.Request) {
	if d.Store == nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	switch principal.Kind {
	case string(cid.Human):
	case string(cid.Agent):
		d.handleAgentMe(w, r, principal)
		return
	default:
		// A service token (or any future kind) has no account behind it to report.
		http.Error(w, "human access token or agent token required", http.StatusForbidden)
		return
	}
	var resp meResponse
	// accounts.email is nullable (EnsureBootstrapAdmin's own INSERT never sets it — see
	// authprovider.Local.Callback's own comment on this same nullability). Scanning a SQL
	// NULL directly into a non-pointer string errors, so scan into *string, mirroring
	// Callback's convention, and normalize NULL to "" for the response.
	var email *string
	err := d.Store.Pool().QueryRow(r.Context(),
		`SELECT provider, provider_subject, email, role, must_change_password FROM accounts WHERE id = $1`,
		principal.Sub,
	).Scan(&resp.Provider, &resp.ProviderSubject, &email, &resp.Role, &resp.MustChangePassword)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if email != nil {
		resp.Email = *email
	}
	resp.AccountID = principal.Sub
	writeJSON(w, resp)
}

// handleAgentMe answers GET /api/me for an agent principal.
//
// The token's signature and the revocation check already passed (verifyBearer), but that is not
// enough on its own: a service-style agent token minted internally — for a component, not by a
// user — has a sub that names no agent_tokens row and an on_behalf_of that may be empty. Those
// are refused with 403 rather than reported as somebody's identity, so only a token an account
// actually minted and still stands behind (unrevoked, unexpired: AgentTokenLive) can introspect
// an account here.
func (d Deps) handleAgentMe(w http.ResponseWriter, r *http.Request, principal cid.Principal) {
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
		return
	}
	accountID := principal.OnBehalfOf
	live, err := svc.AgentTokenLive(r.Context(), accountID, principal.Sub)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !live {
		http.Error(w, "not a user-minted agent token", http.StatusForbidden)
		return
	}
	resp := agentMeResponse{
		Kind:      string(cid.Agent),
		TokenID:   principal.Sub,
		AccountID: accountID,
		// Never nil: the empty case must marshal as [] rather than null.
		Caps: append([]string{}, principal.Caps...),
	}
	if err := d.Store.Pool().QueryRow(r.Context(),
		`SELECT provider_subject, role FROM accounts WHERE id = $1`, accountID,
	).Scan(&resp.ProviderSubject, &resp.Role); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}
