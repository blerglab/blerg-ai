package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

// OAuth connections (docs/design/ai-crons.md 4.3): the start and reconnect routes are human-only
// and same-origin; the callback is reached by the browser coming back from the authorization
// server, carries no bearer token by design, and is protected by the single-use state row plus the
// SameSite=Lax state cookie (the refresh cookie is Strict and is not sent on that top-level
// redirect). No response here carries a token; the authorization URL and the callback redirect are
// Cache-Control: no-store.

const (
	// mcpOAuthCookiePrefix starts the name of the cookie that holds SHA-256(state) (never the state
	// itself). The name carries the first 8 hex digits of that hash, so two flows in flight (two
	// tabs) each keep their own cookie. It is deliberately not the login flow's blerg_oauth_state,
	// so a connect flow and a sign-in can be in flight together.
	mcpOAuthCookiePrefix = "blerg_mcp_oauth_"
	mcpOAuthCallback     = "/auth/mcp/callback"
	mcpOAuthBodyLimit    = 16 << 10
	// settingsPath is where the browser lands after the callback (core's own settings page).
	settingsPath = "/settings"
)

// scopeList accepts scopes as one space separated string or as an array of strings.
type scopeList string

func (s *scopeList) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = scopeList(str)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err != nil {
		return errors.New("scopes must be a string or an array of strings")
	}
	*s = scopeList(strings.Join(arr, " "))
	return nil
}

type mcpOAuthStartRequest struct {
	Name     string    `json:"name"`
	URL      string    `json:"url"`
	ClientID string    `json:"client_id"`
	Scopes   scopeList `json:"scopes"`
}

type mcpOAuthReconnectRequest struct {
	ClientID string    `json:"client_id"`
	Scopes   scopeList `json:"scopes"`
}

type mcpOAuthStartResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	// AuthorizationServer is the host of the authorization server the person is being sent to.
	AuthorizationServer string `json:"authorization_server,omitempty"`
}

// errPublicURLRequired is what a person who reaches the OAuth routes on a non-loopback host of a
// deployment without BLERG_CORE_PUBLIC_URL is told.
const errPublicURLRequired = "OAuth connections need BLERG_CORE_PUBLIC_URL to be set to this deployment's public address (for example https://core.example.com): " +
	"the redirect URI sent to the authorization server must not be guessed from request headers"

// mcpOAuthOrigin is the origin of this deployment for the OAuth redirect URI. It is
// BLERG_CORE_PUBLIC_URL; only when that is unset AND the request is to a loopback host (a desktop
// install) does it fall back to the request's own origin. Anything else would let the Host or
// X-Forwarded-Proto of a request decide which redirect URI is registered with a third party.
func (d Deps) mcpOAuthOrigin(r *http.Request) (string, bool) {
	if d.PublicURL != "" {
		return strings.TrimRight(d.PublicURL, "/"), true
	}
	if isLoopbackHost(r.Host) {
		return d.publicOrigin(r), true
	}
	return "", false
}

// isLoopbackHost reports whether a Host header names this machine: localhost or a loopback IP.
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// mcpOAuthRedirectURI is the exact redirect URI registered with the provider and sent on both the
// authorization and the token request.
func mcpOAuthRedirectURI(origin string) string { return origin + mcpOAuthCallback }

// mcpOAuthCookieName is the state cookie's name for the flow whose hex SHA-256(state) is hash.
func mcpOAuthCookieName(hash string) string {
	if len(hash) > 8 {
		hash = hash[:8]
	}
	return mcpOAuthCookiePrefix + hash
}

// beginMCPOAuth is the shared tail of start and reconnect: it runs the flow's first half and
// answers with the authorization URL and the Lax state cookie.
func (d Deps) beginMCPOAuth(w http.ResponseWriter, r *http.Request, in mcpconn.OAuthStartInput) {
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	// The flow is bound to the browser login session that starts it, and is only completed while
	// that session is alive.
	if principal.Sid == "" {
		http.Error(w, "a signed-in browser session is required: sign in again", http.StatusBadRequest)
		return
	}
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, live, err := svc.HumanSessionLive(r.Context(), principal.Sub, principal.Sid); err != nil {
		log.Printf("mcp oauth start: session liveness: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !live {
		http.Error(w, "session is no longer signed in", http.StatusUnauthorized)
		return
	}
	origin, ok := d.mcpOAuthOrigin(r)
	if !ok {
		http.Error(w, errPublicURLRequired, http.StatusBadRequest)
		return
	}
	in.AccountID, in.SessionID, in.RedirectURI = principal.Sub, principal.Sid, mcpOAuthRedirectURI(origin)
	res, err := d.MCPConnections.StartOAuth(r.Context(), in)
	if err != nil {
		writeMCPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.SetCookie(w, d.cookie(mcpOAuthCookieName(res.StateCookie), res.StateCookie, http.SameSiteLaxMode, int(mcpconn.OAuthStateTTL.Seconds())))
	writeJSON(w, mcpOAuthStartResponse{AuthorizationURL: res.AuthorizationURL, AuthorizationServer: res.AuthorizationServer})
}

// handleStartMCPOAuth is POST /api/mcp/connections/oauth/start.
func (d Deps) handleStartMCPOAuth(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	if d.MCPConnections == nil {
		http.Error(w, "mcp connections unavailable", http.StatusServiceUnavailable)
		return
	}
	var body mcpOAuthStartRequest
	if !decodeMCPBody(w, r, mcpOAuthBodyLimit, &body) {
		return
	}
	d.beginMCPOAuth(w, r, mcpconn.OAuthStartInput{
		Name: body.Name, URL: body.URL, ClientID: body.ClientID, Scopes: string(body.Scopes),
	})
}

// handleReconnectMCPConnection is POST /api/mcp/connections/{id}/reconnect: the same flow for an
// existing OAuth connection, whose tokens the callback replaces. The body is optional.
func (d Deps) handleReconnectMCPConnection(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	if d.MCPConnections == nil {
		http.Error(w, "mcp connections unavailable", http.StatusServiceUnavailable)
		return
	}
	var body mcpOAuthReconnectRequest
	r.Body = http.MaxBytesReader(w, r.Body, mcpOAuthBodyLimit)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
	}
	d.beginMCPOAuth(w, r, mcpconn.OAuthStartInput{
		ReconnectID: r.PathValue("id"), ClientID: body.ClientID, Scopes: string(body.Scopes),
	})
}

// handleMCPOAuthCallback is GET /auth/mcp/callback. It is unauthenticated by design: the state row
// (single use, bound to an account and login session) and the Lax state cookie are the proof. A
// request whose state does not hash to the cookie is refused before anything is consumed or
// cleared, so a replayed or lured link can neither burn nor unbind a legitimate flow. After that,
// every outcome consumes the state, clears the flow's cookie and
// ends in a redirect to the settings page with a fixed-vocabulary hint; provider error text is
// never reflected.
func (d Deps) handleMCPOAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if d.MCPConnections == nil {
		http.Error(w, "mcp connections unavailable", http.StatusServiceUnavailable)
		return
	}
	origin, ok := d.mcpOAuthOrigin(r)
	if !ok {
		http.Error(w, errPublicURLRequired, http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" || len(q["state"]) != 1 {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}
	hash := hashState(state)
	cookie, err := r.Cookie(mcpOAuthCookieName(hash))
	if err != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(cookie.Value)) != 1 {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}
	// The cookie matched: this browser started the flow. Only now is it cleared.
	http.SetCookie(w, d.cookie(mcpOAuthCookieName(hash), "", http.SameSiteLaxMode, -1))
	in := mcpconn.OAuthCallbackInput{
		State:         state,
		Code:          q.Get("code"),
		Iss:           strings.Join(q["iss"], ","), // a repeated iss can never equal the issuer
		IssPresent:    q.Has("iss"),
		ProviderError: q.Has("error"),
		RedirectURI:   mcpOAuthRedirectURI(origin),
		SessionLive: func(ctx context.Context, accountID, sid string) (bool, error) {
			svc, ok := d.identitySvc()
			if !ok {
				return false, errors.New("identity service unavailable")
			}
			_, live, err := svc.HumanSessionLive(ctx, accountID, sid)
			return live, err
		},
	}
	if len(q["code"]) > 1 {
		in.Code = ""
	}
	c, err := d.MCPConnections.CompleteOAuth(r.Context(), in)
	hint := url.Values{"mcp_oauth": {"success"}, "connection": {c.ID}}
	if err != nil {
		reason := "internal"
		var ce *mcpconn.CallbackError
		if errors.As(err, &ce) {
			reason = ce.Reason
		}
		if reason == "internal" || reason == "exchange" {
			// Fixed words only: the wrapped error carries no token, code or provider text.
			log.Printf("mcp oauth callback: %v", err)
		}
		hint = url.Values{"mcp_oauth": {"error"}, "reason": {reason}}
	}
	http.Redirect(w, r, origin+settingsPath+"?"+hint.Encode(), http.StatusSeeOther) //nolint:gosec // G710: the origin is BLERG_CORE_PUBLIC_URL (or, on a loopback host, the host of this request), the path is fixed and the query is our own fixed-vocabulary hint
}
