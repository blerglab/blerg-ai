package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// refreshCookieName is the human-session refresh cookie set by handleLogin/handleCallback and
// cleared by handleLogout.
const refreshCookieName = "blerg_core_refresh"

// oauthStateCookie holds SHA-256(state) — never the state itself — binding the CSRF state
// handleStart hands to the IdP to this specific browser, checked by handleCallback.
const oauthStateCookie = "blerg_oauth_state"

// identitySvc type-asserts d.Identity down to the concrete *identity.Service so handlers can
// reach the Task 3 human-session methods (IssueRefreshToken, RefreshAccessToken,
// RevokeHumanSession) that aren't part of the identityProvider interface. Deps.Identity is
// deliberately typed as the narrower identityProvider interface elsewhere in router.go so
// tests can substitute a stub (see router_test.go's stubIdentity) — every caller here MUST
// check ok and fail closed (500) rather than assume the concrete type and panic if a Deps is
// ever built with a non-*identity.Service Identity.
func (d Deps) identitySvc() (*identity.Service, bool) {
	svc, ok := d.Identity.(*identity.Service)
	return svc, ok
}

// publicOrigin is core's own browser-facing origin: BLERG_CORE_PUBLIC_URL when set, else
// derived from the request (X-Forwarded-Proto/TLS + Host).
//
// The request-derived fallback trusts X-Forwarded-Proto unconditionally (unlike clientIP's
// Deps.BehindProxy gate above) because a wrong scheme guess here only ever makes
// sameOriginRequest's comparison MORE strict, never less: publicOrigin is compared against a
// caller-supplied Origin header, so a forged X-Forwarded-Proto can only cause a legitimate
// same-origin request to be wrongly rejected (fails closed), not a cross-site one to be wrongly
// accepted (there is no way to spoof X-Forwarded-Proto into producing a string that matches an
// attacker's own Origin, since Host still comes from r.Host either way). Contrast with
// clientIP, where trusting a spoofed header without BehindProxy would let a caller forge
// attribution in an audit log — a materially different risk this derivation doesn't share.
// Operators who need a stable, correct origin regardless (e.g. Task 11's OAuth redirect URLs)
// should set BLERG_CORE_PUBLIC_URL rather than rely on this fallback.
func (d Deps) publicOrigin(r *http.Request) string {
	if d.PublicURL != "" {
		return strings.TrimRight(d.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// sameOriginRequest is the CSRF check for cookie-bearing state changes (R4/I-4: login CSRF).
// Browsers send Sec-Fetch-Site on every request and Origin on every POST; a request carrying
// neither is a non-browser client and is allowed (CSRF is a browser-only attack, since it
// relies on the victim's browser automatically attaching cookies/credentials the attacker page
// can't read). Anything else must be same-origin.
func (d Deps) sameOriginRequest(r *http.Request) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" {
		return sfs == "same-origin" || sfs == "none"
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return strings.EqualFold(strings.TrimRight(origin, "/"), d.publicOrigin(r))
}

// requireSameOrigin is sameOriginRequest's write-a-403-and-return-false shape, factored out
// (Task 9's review) so the CSRF guard is written once rather than the same three lines of
// http.Error boilerplate copy-pasted at the top of every cookie/session-mutating handler.
// Callers must return immediately when this reports false — the response has already been
// written.
func (d Deps) requireSameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if !d.sameOriginRequest(r) {
		http.Error(w, "cross-site request rejected", http.StatusForbidden)
		return false
	}
	return true
}

// handleLogin is the local (password) provider's login endpoint: a same-origin form POST with
// provider_subject/password. On success it issues a new human session (refresh token cookie);
// the caller then hits GET /auth/refresh to exchange that cookie for a short-lived access token.
//
// Guarded by sameOriginRequest (R4/I-4): without this, a hostile page could POST a victim's
// browser straight to /auth/login with the ATTACKER's own credentials, silently logging the
// victim into the attacker's account (login CSRF) — the victim then unknowingly stores
// data/credentials into an account the attacker controls.
func (d Deps) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	local, ok := d.AuthProvider.(*authprovider.Local)
	if !ok {
		http.Error(w, "local provider not active", http.StatusNotFound)
		return
	}
	// The limiter keys on the claimed subject, so the form is read here under
	// the same 16 KiB cap Local.Callback applies (ParseForm is idempotent).
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ip, subject := d.clientIP(r), r.PostFormValue("provider_subject")
	if ok, retryAfter := d.LoginLimiter.Allow(ip, subject); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		http.Error(w, "too many failed logins — try again later", http.StatusTooManyRequests)
		return
	}
	acc, err := local.Callback(r.Context(), r)
	if err != nil {
		d.LoginLimiter.Fail(ip, subject)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	d.LoginLimiter.Reset(ip, subject)
	if d.startHumanSession(w, r, acc.ID) {
		w.WriteHeader(http.StatusOK)
	}
}

// handleStart begins an OAuth/OIDC login: a fresh random state is bound to this browser via
// an HttpOnly cookie holding its SHA-256 (never the state itself), then the browser is sent
// to the IdP. 404 under the local provider, which logs in via a form POST instead.
func (d Deps) handleStart(w http.ResponseWriter, r *http.Request) {
	if d.AuthProvider == nil || d.AuthProvider.ID() == "local" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, d.cookie(oauthStateCookie, hashState(state), http.SameSiteLaxMode, 600))
	http.Redirect(w, r, d.AuthProvider.LoginURL(state), http.StatusFound)
}

// authCookiePath scopes every cookie core sets to the auth endpoints, the only ones that
// read them.
const authCookiePath = "/auth"

// cookie builds every cookie core sets, so the Secure attribute has one source
// of truth (Deps.CookieSecure) instead of eight literals.
func (d Deps) cookie(name, value string, sameSite http.SameSite, maxAge int) *http.Cookie {
	// Secure follows Deps.CookieSecure so a plain-HTTP desktop install can sign in; HttpOnly
	// and SameSite are always set.
	return &http.Cookie{Name: name, Value: value, Path: authCookiePath, HttpOnly: true, Secure: d.CookieSecure, SameSite: sameSite, MaxAge: maxAge} //nolint:gosec // G124: Secure is operator configuration, on by default; HttpOnly and SameSite are unconditional
}

func hashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

// handleProvider tells the login page which provider is active so it can render a password
// form (local) or a "Sign in with …" button (github/oidc).
func (d Deps) handleProvider(w http.ResponseWriter, _ *http.Request) {
	id := "local"
	if d.AuthProvider != nil {
		id = d.AuthProvider.ID()
	}
	writeJSON(w, map[string]string{"id": id})
}

// handleCallback is the IdP redirect target for OAuth-style providers (GitHub/OIDC). The local
// provider has no callback leg (it logs in via a direct form POST to /auth/login), so this
// 404s whenever local is the active provider. The state query param must hash to the value in
// the state cookie set by handleStart (CSRF binding); the cookie is cleared either way so it
// can never be replayed against a second callback.
func (d Deps) handleCallback(w http.ResponseWriter, r *http.Request) {
	if d.AuthProvider == nil || d.AuthProvider.ID() == "local" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(oauthStateCookie)
	http.SetCookie(w, d.cookie(oauthStateCookie, "", http.SameSiteLaxMode, -1))
	if state == "" || err != nil ||
		subtle.ConstantTimeCompare([]byte(hashState(state)), []byte(cookie.Value)) != 1 {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}
	acc, err := d.AuthProvider.Callback(r.Context(), r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !d.startHumanSession(w, r, acc.ID) {
		return
	}
	origin := d.publicOrigin(r)
	http.Redirect(w, r, origin+"/auth/refresh?return_to="+url.QueryEscape(origin+"/app"), http.StatusFound) //nolint:gosec // G710: origin is BLERG_CORE_PUBLIC_URL, or else the host this very request was sent to; the path is fixed
}

// sessionCookieMaxAge returns the refresh cookie's Max-Age, in seconds, derived from
// Deps.SessionTTL (BLERG_CORE_SESSION_TTL) — or 30 days (720h) when SessionTTL is unset (zero
// or negative), matching identity.Service's own default so the cookie and the DB row's
// expires_at agree even if only one of the two was configured.
func (d Deps) sessionCookieMaxAge() int {
	if d.SessionTTL <= 0 {
		return int((720 * time.Hour).Seconds())
	}
	return int(d.SessionTTL.Seconds())
}

// clientIP is attribution only (human_sessions.ip). Behind the ingress RemoteAddr is always
// the proxy, so trust X-Forwarded-For's first entry when the operator says there is one
// (Deps.BehindProxy / BLERG_CORE_BEHIND_PROXY) — never otherwise, since a caller could
// otherwise spoof X-Forwarded-For to forge attribution.
func (d Deps) clientIP(r *http.Request) string {
	if d.BehindProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// startHumanSession refuses disabled accounts, clears any stale "sub" revocation (a
// successful login IS the re-enable signal), issues a refresh token and sets it as the
// HttpOnly session cookie. It writes no success status — callers decide between 200
// (form login) and a redirect (OAuth callback). Returns false when a response was written.
func (d Deps) startHumanSession(w http.ResponseWriter, r *http.Request, accountID string) bool {
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "identity service unavailable", http.StatusInternalServerError)
		return false
	}
	state, err := svc.AccountState(r.Context(), accountID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if state.Disabled {
		http.Error(w, "account disabled", http.StatusForbidden)
		return false
	}
	if err := svc.Unrevoke(r.Context(), "sub", accountID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	raw, err := svc.IssueRefreshToken(r.Context(), accountID, r.UserAgent(), d.clientIP(r))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	http.SetCookie(w, d.cookie(refreshCookieName, raw, http.SameSiteStrictMode, d.sessionCookieMaxAge()))
	return true
}

// handleRefresh validates the refresh cookie against a live human session and, if return_to's
// origin is on the AllowedReturnOrigins allowlist, redirects to return_to carrying a freshly
// minted access token in the URL fragment (never the query string, so it doesn't get logged by
// intermediate proxies or land in return_to's own server access logs).
//
// The origin check happens before the cookie is even read: an invalid return_to is rejected
// with 400 regardless of session state, so this endpoint can never be used as an open redirect
// even by a legitimately logged-in caller.
func (d Deps) handleRefresh(w http.ResponseWriter, r *http.Request) {
	returnTo := r.URL.Query().Get("return_to")
	if !d.returnToAllowed(r, returnTo) {
		http.Error(w, "invalid return_to", http.StatusBadRequest)
		return
	}
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil {
		writeSignInRequired(w, r, returnTo, "signed_out", "You're not signed in.")
		return
	}
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "identity service unavailable", http.StatusInternalServerError)
		return
	}
	// A must-change-password account (the bootstrap admin before its first rotation, or anyone
	// an admin reset via `users set-password`) only ever gets a password.change-only token —
	// and the only screen that can do anything with one is core's own /change-password. Board
	// and runner have no such screen, so when the user arrived via one of them (the usual
	// path: landing page → board tile → sign-in-required → login → back to board) they would
	// land on a component that can't even read /api/me. Decide the destination here, server
	// side, before minting: the token is minted for core's audience and the browser is sent to
	// /change-password with the original return_to preserved, so after the change (which
	// revokes everything) the login page can send them back where they were going.
	audience := d.audienceForReturnTo(returnTo)
	dest := returnTo
	if must, err := svc.SessionMustChangePassword(r.Context(), cookie.Value); err == nil && must {
		audience = d.Audience
		dest = d.changePasswordURL(r, returnTo)
	}
	access, newRefresh, err := svc.RefreshAccessToken(r.Context(), cookie.Value, audience)
	if err != nil {
		// The presented cookie is dead (revoked/expired/replayed) — clear it rather than
		// leaving a known-dead value sitting in the browser for a future request to retry.
		http.SetCookie(w, d.cookie(refreshCookieName, "", http.SameSiteStrictMode, -1))
		if errors.Is(err, identity.ErrSessionReplayed) {
			// The request that tripped the revocation of its browser session: where it came from, for diagnosing it.
			log.Printf("auth: refresh token reuse from ip=%s ua=%q return_to=%s", d.clientIP(r), r.UserAgent(), returnTo)
		}
		writeSignInRequired(w, r, returnTo, "expired", "Your session has expired or was signed out.")
		return
	}
	// RefreshAccessToken rotates the session and returns the new raw token (a rotation the
	// browser never received is simply redone, so a presented stale cookie gets a new value
	// too). The empty case is kept for a transport that mints without rotating.
	if newRefresh != "" {
		// Session lifetime is absolute from login, not reset on every rotation: size Max-Age
		// to the session's REMAINING lifetime (expires_at - now), not a fresh full TTL, so the
		// cookie and the DB row's expires_at agree.
		expiresAt, err := svc.SessionExpiresAt(r.Context(), newRefresh)
		maxAge := d.sessionCookieMaxAge()
		if err == nil {
			if remaining := int(time.Until(expiresAt).Seconds()); remaining > 0 {
				maxAge = remaining
			} else {
				maxAge = 1
			}
		}
		http.SetCookie(w, d.cookie(refreshCookieName, newRefresh, http.SameSiteStrictMode, maxAge))
	}
	http.Redirect(w, r, dest+"#access_token="+access, http.StatusFound) //nolint:gosec // G710: dest is return_to after returnToAllowed matched its origin against the allow-list, or core's own change-password page
}

// changePasswordURL is where handleRefresh sends a must-change-password account instead of
// returnTo: core's own /change-password, carrying returnTo (already validated by
// returnToAllowed) as its own return_to so ChangePassword.tsx → /login can hand it back into
// /auth/refresh afterwards. A returnTo that is ALREADY that page (the page's own apiFetch
// refreshing on a 401) is returned unchanged rather than nested another level deep.
func (d Deps) changePasswordURL(r *http.Request, returnTo string) string {
	base := d.publicOrigin(r) + "/change-password"
	if u, err := url.Parse(returnTo); err == nil && u.Scheme+"://"+u.Host+u.Path == base {
		return returnTo
	}
	return base + "?return_to=" + url.QueryEscape(returnTo)
}

// writeSignInRequired handles GET /auth/refresh's two recoverable failures — no refresh
// cookie, and a cookie whose session was revoked/expired.
//
// A real user arrives here by full-page navigation: board's and runner's fetch wrappers, and
// core's own SPA, all send the whole page to /auth/refresh on a 401, and a signed-out visitor
// to the root lands here the same way. For them the answer is the login form itself — a 302
// to /login carrying return_to (so a successful sign-in goes back where they were headed) and
// a reason code Login.tsx turns into a one-line notice ("expired") or nothing ("signed_out").
// Nobody should have to notice a "Sign in" link to find out they need to sign in.
//
// Anything that is NOT a browser navigation (a script, curl, a fetch() that did not follow
// the wrapper convention) still gets the 401 with a small HTML page linking to /login: the
// status stays an authentication failure for callers that key off it, and the page is not a
// dead end for a human who somehow lands on it.
//
// return_to is not an open-redirect vector in either branch: returnToAllowed has already
// checked it against the allowlist, and Login.tsx only ever feeds it back into /auth/refresh,
// which checks it again server-side.
func writeSignInRequired(w http.ResponseWriter, r *http.Request, returnTo, reason, detail string) {
	loginURL := "/login"
	sep := "?"
	if returnTo != "" {
		loginURL += sep + "return_to=" + url.QueryEscape(returnTo)
		sep = "&"
	}
	if reason != "" {
		loginURL += sep + "reason=" + url.QueryEscape(reason)
	}
	if isBrowserNavigation(r) {
		http.Redirect(w, r, loginURL, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`<!doctype html>
<meta charset="utf-8">
<title>Sign in — blerg</title>
<main style="font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1.5rem">
<h1 style="font-size:1.3rem">Sign in required</h1>
<p>` + template.HTMLEscapeString(detail) + `</p>
<p><a href="` + template.HTMLEscapeString(loginURL) + `">Sign in</a></p>
</main>
`))
}

// isBrowserNavigation reports whether r is a top-level page navigation by a browser, as
// opposed to a programmatic fetch/XHR or a non-browser client. Every current browser sends
// Sec-Fetch-Mode: navigate on navigations; older ones are recognised by a text/html Accept,
// which fetch()/XHR callers do not send by default.
func isBrowserNavigation(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Mode") == "navigate" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// handleLogout revokes the session named by the current refresh cookie (if any) and clears the
// cookie. Missing/already-invalid cookies are not an error — logging out an already-logged-out
// session is a no-op, not a failure.
func (d Deps) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	if cookie, err := r.Cookie(refreshCookieName); err == nil {
		// Best-effort revocation: even if the identity service isn't the concrete type (a
		// misconfigured Deps, or a test stub), the cookie is still cleared below — logging
		// out must never get stuck behind an internal wiring problem. Any revocation
		// failure is logged so a "logout didn't actually revoke the session" report is
		// debuggable later.
		if svc, ok := d.identitySvc(); ok {
			if err := svc.RevokeHumanSession(r.Context(), cookie.Value); err != nil {
				log.Printf("handleLogout: RevokeHumanSession failed: %v", err)
			}
		} else {
			log.Printf("handleLogout: identity service unavailable, session not revoked")
		}
	}
	http.SetCookie(w, d.cookie(refreshCookieName, "", http.SameSiteStrictMode, -1))
	w.WriteHeader(http.StatusOK)
}

// handleLogoutAll is POST /auth/logout-all, gated behind requireHumanPrincipal: unlike
// handleLogout (which trusts only the refresh cookie and revokes just that one device),
// handleLogoutAll authenticates via the caller's bearer access token and revokes every live
// session AND the account's sub in the shared revocations table (RevokeAccountEverywhere), so
// already-minted access tokens on OTHER devices also stop verifying immediately rather than
// merely failing their next refresh. It also clears the calling browser's own refresh cookie,
// same as handleLogout, since the caller's own device session was revoked too.
func (d Deps) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "identity service unavailable", http.StatusInternalServerError)
		return
	}
	if err := svc.RevokeAccountEverywhere(r.Context(), principal.Sub); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, d.cookie(refreshCookieName, "", http.SameSiteStrictMode, -1))
	w.WriteHeader(http.StatusOK)
}

// returnToAllowed reports whether returnTo parses as an absolute URL whose scheme+host origin is
// either core's own origin (d.publicOrigin(r)) or exactly one of d.AllowedReturnOrigins. An
// empty, unparseable, or off-allowlist return_to is rejected.
//
// Core's own origin is ALWAYS accepted, independent of AllowedReturnOrigins — even against an
// EMPTY allowlist (the carried controller ruling). Both the local-login flow and the OAuth
// callback redirect to "<publicOrigin>/app" via exactly this endpoint (see handleCallback
// above), so without this carve-out an install whose operator configured
// BLERG_CORE_ALLOWED_RETURN_ORIGINS for board/runner but never thought to also list core's own
// origin would break its own login.
//
// This is not an open-redirect widening — but NOT because Host is somehow trustworthy: r.Host
// (and X-Forwarded-Proto) IS caller-supplied, exactly like any other request header, and an
// attacker's own request can set it to whatever they like. What actually makes this safe is who
// the attacker needs to fool: an open redirect via return_to only matters if it can send some
// OTHER party (a victim) somewhere the attacker chose. But a victim's own browser decides Host
// for itself, from the URL it is actually connecting to — a malicious page cannot make a
// victim's browser send a forged Host to core's real server; it can only ever cause the victim's
// browser to send core's real Host. An attacker forging their OWN request's Host (e.g. via curl,
// bypassing a browser entirely) only ever gets a return_to accepted for THEIR OWN request — there
// is no victim to redirect anywhere, so nothing is gained.
//
// This reasoning is specific to that browser-mediated attack shape and must NOT be reused
// anywhere the caller making the request and the party who'd be redirected are the same actor —
// i.e. anywhere a self-forged Host could matter on its own. That is exactly why the non-local
// providers (github/oidc, buildAuthProvider in cmd/blerg-core/main.go) require
// BLERG_CORE_PUBLIC_URL instead of trusting this same request-derived fallback for their OAuth
// redirect_uri: that URL is registered up front with the IdP and reused across every login
// attempt, so a value influenced by whichever caller happens to be present when it's computed
// would let any requester steer where authorization codes get delivered — a very different,
// requester-controlled risk this return_to carve-out doesn't share.
func (d Deps) returnToAllowed(r *http.Request, returnTo string) bool {
	if returnTo == "" {
		return false
	}
	u, err := url.Parse(returnTo)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	origin := u.Scheme + "://" + u.Host
	if origin == d.publicOrigin(r) {
		return true
	}
	for _, allowed := range d.AllowedReturnOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

// handleChangePassword is POST /auth/password (requireHumanPrincipal): the only endpoint a
// password-change-only token (an account with must_change_password set — see
// MintHumanAccessToken) can do anything useful at, since nothing else's capability check asks
// for "password.change" — GET /api/me and POST /auth/logout-all are deliberately reachable too
// (any human token, no capability gate), but neither leaks or changes anything the flag needs
// to withhold. On success the account is revoked everywhere (RevokeAccountEverywhere: every
// live session AND the "sub" entry in the shared revocations table, so an access token minted
// on some OTHER device before this change — the scenario a password rotation exists to defend
// against — stops verifying immediately, not just on its next refresh) and this browser's own
// refresh cookie is cleared: the response tells the caller to sign in again with the new
// password rather than trying to keep the current request's session alive. A subsequent
// successful login clears the "sub" revocation (startHumanSession's existing Unrevoke call).
//
// Guarded by the SAME LoginLimiter instance as POST /auth/login (desktop-security controller
// addendum to Task 7): a hijacked/stolen access token otherwise buys the holder unlimited
// bcrypt-verified guesses at the real password with NO other rate limit anywhere on this path,
// and success revokes every other session (full account takeover). Keyed on the authenticated
// principal's Sub (the account ID) — there is no unauthenticated "claimed subject" here the way
// handleLogin has one (a typed provider_subject) — AND on the caller's IP, matching handleLogin's
// own two-key shape. Because handleLogin's subject key is the typed provider_subject string
// while this one is the account ID, the two endpoints' PER-SUBJECT buckets are independent for
// the same account (an ID and its username essentially never collide); the two endpoints DO
// share the per-IP bucket, so enough wrong guesses at either endpoint from the same IP can still
// 429 the other one for the window — see the accepted trade-off in CONFIG.md. A wrong old
// password counts as a failure the same way a wrong login password does; success clears both
// buckets.
// changePasswordErrorHeader carries handleChangePassword's wrong-current-password reason on its
// 401 so the browser can tell it apart from a stale-token 401 (see the case below).
const changePasswordErrorHeader = "X-Blerg-Error" //nolint:gosec // G101: a response header name, not a credential

func (d Deps) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	local, ok := d.AuthProvider.(*authprovider.Local)
	if !ok {
		http.Error(w, "local provider not active", http.StatusNotFound)
		return
	}
	p, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	ip := d.clientIP(r)
	if ok, retryAfter := d.LoginLimiter.Allow(ip, p.Sub); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		http.Error(w, "too many attempts — try again later", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	switch err := local.ChangePassword(r.Context(), p.Sub, body.OldPassword, body.NewPassword); {
	case errors.Is(err, authprovider.ErrWeakPassword):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, authprovider.ErrInvalidCredentials):
		d.LoginLimiter.Fail(ip, p.Sub)
		// Distinguish "wrong current password" from the middleware's own 401s (missing or
		// expired bearer token — verifyBearer above), which reach the same page with the same
		// status: ChangePassword.tsx shows this one as an inline error and treats any other
		// 401 as a stale token to refresh. Safe to reveal — the caller already holds a valid
		// token for this very account, so "your guess at your own password was wrong" is not
		// an oracle about anyone else.
		w.Header().Set(changePasswordErrorHeader, "invalid_credentials")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	d.LoginLimiter.Reset(ip, p.Sub)
	// RevokeAccountEverywhere (not just RevokeAllSessions): a password change is presumed to
	// follow a compromise, so an access token already minted on some OTHER device must stop
	// verifying immediately (via the shared revocations table's "sub" entry), not merely fail
	// its next refresh. This browser's own cookie is cleared below so the current caller
	// re-authenticates immediately too. A subsequent successful login clears the "sub"
	// revocation (startHumanSession's Unrevoke call) — it is not a permanent lockout.
	if svc, ok := d.identitySvc(); ok {
		if err := svc.RevokeAccountEverywhere(r.Context(), p.Sub); err != nil {
			log.Printf("handleChangePassword: RevokeAccountEverywhere: %v", err)
		}
	}
	http.SetCookie(w, d.cookie(refreshCookieName, "", http.SameSiteStrictMode, -1))
	w.WriteHeader(http.StatusOK)
}

// audienceForReturnTo maps return_to's origin (scheme+host) to the access-token audience that
// origin's component expects, via the operator-configured Deps.OriginAudiences map (e.g.
// "http://board.example.com" -> "blerg-board"). If return_to doesn't parse, or its origin has
// no entry in OriginAudiences, it falls back to d.Audience (core's own audience,
// "blerg-core") — never a hardcoded stub. Callers should have already validated returnTo via
// returnToAllowed before calling this; an unrecognized origin here still degrades safely to
// core's own audience rather than panicking or minting an empty aud.
//
// This fallback is also what makes core's own origin (returnToAllowed's own-origin carve-out
// above) map to core's own audience: an operator would not normally add core's own origin to
// OriginAudiences (why would core mint itself a token for a DIFFERENT audience?), so a
// return_to of core's own origin falls straight through to d.Audience here, same as any other
// unmapped-but-allowed origin.
func (d Deps) audienceForReturnTo(returnTo string) string {
	u, err := url.Parse(returnTo)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return d.Audience
	}
	origin := u.Scheme + "://" + u.Host
	if aud, ok := d.OriginAudiences[origin]; ok {
		return aud
	}
	return d.Audience
}
