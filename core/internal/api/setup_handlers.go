package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/blerglab/blerg-ai/core/internal/authprovider"
)

// First-run setup (authprovider/setup.go): while no account exists, the person creates the first
// admin on /setup. A request needs the setup code from the link (minted only by
// `blerg-core setup-link`), unless it is trusted as local (setupTrustedLocal: the desktop install,
// whose core is published on loopback only, reached directly from this machine).

// Values of GET /auth/provider's "setup" field.
const (
	setupNoCode   = "open" // setup mode, this request needs no code
	setupWithCode = "code" // setup mode, a setup code is required
)

// setupLimiterKey is the LoginLimiter subject bucket for setup-code guesses. A colon can never be
// in a username, so it cannot share a bucket with a real login; it is per IP so nobody can lock
// everyone else out of setup.
func setupLimiterKey(ip string) string { return "setup:" + ip }

// localProvider is the active provider when it is the local (password) one.
func (d Deps) localProvider() (*authprovider.Local, bool) {
	local, ok := d.AuthProvider.(*authprovider.Local)
	return local, ok
}

// setupTrustedLocal reports whether r may use /setup without a code. All of these must hold:
//   - Deps.SetupAllowLocal (core is published on a loopback address only);
//   - the browser addressed core by a loopback name (Host), which a DNS-rebinding page cannot fake;
//   - no proxy forwarding headers. This only catches proxies that add them: a relay that adds
//     none (nginx's default proxy_pass, ssh/socat tunnels) still looks local, which the desktop
//     README warns about (and BLERG_SETUP_ALLOW_LOCAL=false in .env turns this path off);
//   - the TCP peer is loopback, or the container's default gateway (Deps.SetupLocalGateway, set
//     only with BLERG_SETUP_TRUST_DOCKER_GATEWAY=1 from the desktop compose file): with
//     a Docker-published port, a connection from the host's own 127.0.0.1 arrives from the
//     gateway of the container's network, not from loopback.
func (d Deps) setupTrustedLocal(r *http.Request) bool {
	if !d.SetupAllowLocal || !loopbackHost(r.Host) {
		return false
	}
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-Ip"} {
		if _, ok := r.Header[h]; ok {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	peer := net.ParseIP(host)
	if peer == nil {
		return false
	}
	return peer.IsLoopback() || (d.SetupLocalGateway != nil && peer.Equal(d.SetupLocalGateway))
}

// noFraming forbids putting the setup page (or its answers) in a frame.
func noFraming(w http.ResponseWriter) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
}

// loopbackHost reports whether a Host header names this machine's loopback interface.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// setupMode is "" when setup is closed (an account exists, the provider is not local, or the
// database could not say — failing closed), else setupNoCode or setupWithCode for this request.
func (d Deps) setupMode(r *http.Request) string {
	local, ok := d.localProvider()
	if !ok {
		return ""
	}
	open, err := local.SetupOpen(r.Context())
	if err != nil || !open {
		return ""
	}
	if d.setupTrustedLocal(r) {
		return setupNoCode
	}
	return setupWithCode
}

// serveApp serves core's web app (index.html) for a page route registered on the mux.
func (d Deps) serveApp(w http.ResponseWriter, r *http.Request) {
	if d.App == nil {
		http.NotFound(w, r)
		return
	}
	d.App.ServeHTTP(w, r)
}

// handleSetupPage is GET /setup: the app's "Create your account" page while setup is open, and a
// redirect to /login once it is closed. The setup code travels in the URL fragment, which no
// server sees; the page is still never cached, sends no Referer and cannot be framed.
func (d Deps) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	noFraming(w)
	if d.setupMode(r) == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	d.serveApp(w, r)
}

// handleSetupSlash is GET /setup/ and anything under it: the app's router would render the setup
// page there too, without handleSetupPage's headers, so it is sent to /setup (the browser keeps
// the #code fragment across the redirect).
func (d Deps) handleSetupSlash(w http.ResponseWriter, r *http.Request) {
	noFraming(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, "/setup", http.StatusFound)
}

// handleLoginPage is GET /login. While setup is open and this request needs no code, there is
// nobody to sign in as yet: go to /setup. Otherwise the app's login page, which tells a person on
// a setup-mode install that needs a code to open the setup link from the installer.
func (d Deps) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if d.setupMode(r) == setupNoCode {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	d.serveApp(w, r)
}

// setupErrorHeader values on a refused POST /auth/setup, so the page can put the message by the
// right field. The JSON {"error"} body carries text shown as is.
const (
	setupErrUsername = "username"
	setupErrPassword = "password"
	setupErrCode     = "code"
	setupErrComplete = "complete"
)

func writeSetupError(w http.ResponseWriter, status int, tag, msg string) {
	w.Header().Set(changePasswordErrorHeader, tag)
	writeJSONStatus(w, status, map[string]string{"error": msg})
}

// handleSetup is POST /auth/setup {"code", "provider_subject", "password"}: creates the first
// account (an admin) and signs the browser in, exactly like a successful POST /auth/login (the
// caller then goes through GET /auth/refresh). Same protections as the other auth forms: the
// same-origin check, a 16 KiB body cap, and the login rate limiter (per IP) for every refused
// attempt. A set-up install answers 409 from a cheap read, before any bcrypt or lock.
func (d Deps) handleSetup(w http.ResponseWriter, r *http.Request) {
	noFraming(w)
	if !d.requireSameOrigin(w, r) {
		return
	}
	local, ok := d.localProvider()
	if !ok {
		http.Error(w, "local provider not active", http.StatusNotFound)
		return
	}
	ip := d.clientIP(r)
	if ok, retryAfter := d.LoginLimiter.Allow(ip, setupLimiterKey(ip)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		http.Error(w, "too many attempts — try again later", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		Code            string `json:"code"`
		ProviderSubject string `json:"provider_subject"`
		Password        string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	acc, err := local.CreateFirstAdmin(r.Context(), authprovider.FirstAdmin{
		Username: body.ProviderSubject,
		Password: body.Password,
		Code:     body.Code,
		Local:    d.setupTrustedLocal(r),
	})
	// Every refused attempt counts against this IP's setup bucket — on a set-up install too, so
	// the endpoint stays rate-limited for the life of the install.
	if err != nil {
		d.LoginLimiter.Fail(ip, setupLimiterKey(ip))
	}
	switch {
	case errors.Is(err, authprovider.ErrInvalidUsername):
		writeSetupError(w, http.StatusBadRequest, setupErrUsername, err.Error())
		return
	case errors.Is(err, authprovider.ErrWeakPassword):
		writeSetupError(w, http.StatusBadRequest, setupErrPassword, "use 12 to 72 characters")
		return
	case errors.Is(err, authprovider.ErrSetupComplete):
		writeSetupError(w, http.StatusConflict, setupErrComplete, "this Blerg is already set up — sign in instead")
		return
	case errors.Is(err, authprovider.ErrSetupCode):
		writeSetupError(w, http.StatusForbidden, setupErrCode, err.Error())
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	d.LoginLimiter.Reset(ip, setupLimiterKey(ip))
	if d.startHumanSession(w, r, acc.ID, nil) {
		w.WriteHeader(http.StatusOK)
	}
}
