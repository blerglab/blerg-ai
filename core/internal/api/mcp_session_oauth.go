package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

func (d Deps) sessionOAuthFlow() mcpconn.SessionOAuthFlow {
	if d.SessionMCPOAuth != nil {
		return d.SessionMCPOAuth
	}
	if d.MCPConnections != nil {
		return d.MCPConnections
	}
	return nil
}
func sessionOAuthHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
}
func sessionOAuthCause(cause string) {
	failurelog.Default.Event("mcp_session_http:"+cause, slog.LevelWarn, "component", "core", "operation", "mcp_session_oauth", "cause", cause)
}
func sessionOAuthError(w http.ResponseWriter, err error) {
	sessionOAuthCause(mcpconn.FailureCause(err))
	var validation *mcpconn.ValidationError
	switch {
	case errors.Is(err, mcpconn.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, mcpconn.ErrUnavailable):
		http.Error(w, "Remote MCP sign-in is not available.", http.StatusServiceUnavailable)
	case errors.Is(err, mcpconn.ErrRateLimited):
		http.Error(w, "Too many sign-in attempts. Try again in a minute.", http.StatusTooManyRequests)
	case errors.As(err, &validation), errors.Is(err, mcpconn.ErrLimit):
		http.Error(w, "Review the server settings and try again.", http.StatusBadRequest)
	default:
		http.Error(w, "Sign-in could not be completed. Try again.", http.StatusInternalServerError)
	}
}
func (d Deps) sessionOAuthOrigin() (string, error) {
	u, err := url.Parse(d.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !d.CookieSecure {
		return "", mcpconn.ErrUnavailable
	}
	return strings.TrimRight(d.PublicURL, "/"), nil
}
func (d Deps) sessionOAuthHuman(w http.ResponseWriter, r *http.Request) (account, sid string, ok bool) {
	principal, ok := principalFromCtx(r.Context())
	if !ok || principal.Sid == "" {
		sessionOAuthCause("human_login_required")
		http.Error(w, "Sign in to Blerg again.", http.StatusUnauthorized)
		return "", "", false
	}
	svc, ok := d.identitySvc()
	if !ok {
		sessionOAuthError(w, mcpconn.ErrUnavailable)
		return "", "", false
	}
	_, _, live, err := svc.HumanSessionDeadline(r.Context(), principal.Sub, principal.Sid)
	if err != nil {
		sessionOAuthError(w, err)
		return "", "", false
	}
	if !live {
		sessionOAuthCause("human_login_expired")
		http.Error(w, "Sign in to Blerg again.", http.StatusUnauthorized)
		return "", "", false
	}
	return principal.Sub, principal.Sid, true
}
func (d Deps) sessionOAuthLive(ctx context.Context, account, sid string) (bool, error) {
	svc, ok := d.identitySvc()
	if !ok {
		return false, mcpconn.ErrUnavailable
	}
	_, live, err := svc.HumanSessionLive(ctx, account, sid)
	return live, err
}
func (d Deps) handleSessionOAuthPreview(w http.ResponseWriter, r *http.Request) {
	sessionOAuthHeaders(w)
	if !d.requireSameOrigin(w, r) {
		sessionOAuthCause("origin_refused")
		return
	}
	account, sid, ok := d.sessionOAuthHuman(w, r)
	if !ok {
		return
	}
	origin, err := d.sessionOAuthOrigin()
	if err != nil {
		sessionOAuthError(w, err)
		return
	}
	flow := d.sessionOAuthFlow()
	if flow == nil {
		sessionOAuthError(w, mcpconn.ErrUnavailable)
		return
	}
	var body struct {
		RunnerSessionID string    `json:"runner_session_id"`
		DefinitionID    string    `json:"definition_id"`
		Scopes          scopeList `json:"scopes"`
	}
	if !decodeMCPBody(w, r, mcpOAuthBodyLimit, &body) {
		sessionOAuthCause("invalid_body")
		return
	}
	preview, err := flow.PreviewSessionOAuth(r.Context(), mcpconn.SessionOAuthPreviewInput{SessionOAuthRequest: mcpconn.SessionOAuthRequest{AccountID: account, HumanSessionID: sid, RunnerSessionID: body.RunnerSessionID, DefinitionID: body.DefinitionID}, Scopes: string(body.Scopes), RedirectURI: mcpOAuthRedirectURI(origin), SessionLive: d.sessionOAuthLive})
	if err != nil {
		sessionOAuthError(w, err)
		return
	}
	writeJSON(w, preview)
}
func (d Deps) handleSessionOAuthStart(w http.ResponseWriter, r *http.Request) {
	sessionOAuthHeaders(w)
	if !d.requireSameOrigin(w, r) {
		sessionOAuthCause("origin_refused")
		return
	}
	account, sid, ok := d.sessionOAuthHuman(w, r)
	if !ok {
		return
	}
	origin, err := d.sessionOAuthOrigin()
	if err != nil {
		sessionOAuthError(w, err)
		return
	}
	flow := d.sessionOAuthFlow()
	if flow == nil {
		sessionOAuthError(w, mcpconn.ErrUnavailable)
		return
	}
	var body struct{}
	if !decodeMCPBody(w, r, mcpOAuthBodyLimit, &body) {
		sessionOAuthCause("invalid_body")
		return
	}
	start, err := flow.StartSessionOAuth(r.Context(), account, sid, r.PathValue("id"), mcpOAuthRedirectURI(origin))
	if err != nil {
		sessionOAuthError(w, err)
		return
	}
	http.SetCookie(w, d.cookie(mcpOAuthCookieName(start.StateCookie), start.StateCookie, http.SameSiteLaxMode, int(mcpconn.OAuthStateTTL.Seconds())))
	writeJSON(w, mcpOAuthStartResponse{AuthorizationURL: start.AuthorizationURL, AuthorizationServer: start.AuthorizationServer})
}
func (d Deps) handleSessionOAuthStatus(w http.ResponseWriter, r *http.Request) {
	sessionOAuthHeaders(w)
	account, sid, ok := d.sessionOAuthHuman(w, r)
	if !ok {
		return
	}
	flow := d.sessionOAuthFlow()
	if flow == nil {
		sessionOAuthError(w, mcpconn.ErrUnavailable)
		return
	}
	status, err := flow.SessionOAuthStatus(r.Context(), account, sid, r.PathValue("id"))
	if err != nil {
		sessionOAuthError(w, err)
		return
	}
	writeJSON(w, status)
}
func (d Deps) handleSessionOAuthCancel(w http.ResponseWriter, r *http.Request) {
	sessionOAuthHeaders(w)
	if !d.requireSameOrigin(w, r) {
		sessionOAuthCause("origin_refused")
		return
	}
	account, sid, ok := d.sessionOAuthHuman(w, r)
	if !ok {
		return
	}
	flow := d.sessionOAuthFlow()
	if flow == nil {
		sessionOAuthError(w, mcpconn.ErrUnavailable)
		return
	}
	if err := flow.CancelSessionOAuth(r.Context(), account, sid, r.PathValue("id")); err != nil {
		sessionOAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (d Deps) handleSessionMCPOAuthCallback(w http.ResponseWriter, r *http.Request) {
	sessionOAuthHeaders(w)
	reject := func() {
		sessionOAuthCause("invalid_callback")
		http.Error(w, "Sign-in response is not valid.", http.StatusBadRequest)
	}
	if len(r.URL.RawQuery) > 16<<10 {
		reject()
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		reject()
		return
	}
	for _, key := range []string{"state", "code", "error", "iss", "response_mode"} {
		if len(q[key]) > 1 {
			reject()
			return
		}
	}
	state := q.Get("state")
	if !strings.HasPrefix(state, mcpconn.SessionOAuthStatePrefix) || len(state) > 128 || (q.Has("iss") && (q.Get("iss") == "" || len(q.Get("iss")) > 2048)) || (q.Has("response_mode") && q.Get("response_mode") != "query") || (q.Has("code") && q.Has("error")) || (!q.Has("error") && (q.Get("code") == "" || len(q.Get("code")) > 8192)) || (q.Has("error") && (q.Get("error") == "" || len(q.Get("error")) > 128)) {
		reject()
		return
	}
	hash := hashState(state)
	cookie, err := r.Cookie(mcpOAuthCookieName(hash))
	if err != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(cookie.Value)) != 1 {
		reject()
		return
	}
	origin, err := d.sessionOAuthOrigin()
	if err != nil {
		sessionOAuthError(w, err)
		return
	}
	flow := d.sessionOAuthFlow()
	if flow == nil {
		sessionOAuthError(w, mcpconn.ErrUnavailable)
		return
	}
	http.SetCookie(w, d.cookie(mcpOAuthCookieName(hash), "", http.SameSiteLaxMode, -1))
	completion, err := flow.CompleteSessionOAuth(r.Context(), mcpconn.OAuthCallbackInput{State: state, Code: q.Get("code"), Iss: q.Get("iss"), IssPresent: q.Has("iss"), ProviderError: q.Has("error"), RedirectURI: mcpOAuthRedirectURI(origin), SessionLive: d.sessionOAuthLive})
	if errors.Is(err, mcpconn.ErrUnavailable) {
		sessionOAuthError(w, err)
		return
	}
	hint := url.Values{"section": {"integrations"}}
	if err != nil {
		sessionOAuthCause(mcpconn.FailureCause(err))
		reason := "state"
		var callback *mcpconn.CallbackError
		if errors.As(err, &callback) {
			switch callback.Reason {
			case "state", "session", "issuer", "redirect_uri", "exchange", "denied", "gone", "internal":
				reason = callback.Reason
			}
		}
		hint.Set("mcp_session_error", reason)
	} else {
		if !uuidShape.MatchString(completion.AttemptID) {
			sessionOAuthError(w, errors.New("invalid completion reference"))
			return
		}
		hint.Set("mcp_session_result", completion.AttemptID)
	}
	http.Redirect(w, r, origin+settingsPath+"?"+hint.Encode(), http.StatusSeeOther) // Configured HTTPS origin, fixed path and bounded server-owned reference.
}
