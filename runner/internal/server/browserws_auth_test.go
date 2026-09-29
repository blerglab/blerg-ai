package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/gorilla/websocket"
)

// allowWSForTest is the browser-WS authorizer used by every pre-existing
// /ws/browser test in this package. Those tests exercise the socket's protocol
// behaviour, not its auth gate — the gate itself is covered here, by tests that
// wire the real (*API).AuthorizeBrowserWS.
func allowWSForTest(*http.Request) (string, string, bool) { return "", "", true }

// browserWSTestAPI builds an API wired to a fake blerg-core, plus a helper that
// mints browser access tokens that API will accept.
func browserWSTestAPI(t *testing.T) (*API, func(caps ...string) string) {
	t.Helper()
	api := &API{}
	return api, enableBrowserAuth(t, api)
}

// enableBrowserAuth wires a fake blerg-core onto api (so authBrowser /
// AuthorizeBrowserWS can actually verify anything) and returns a helper that
// mints tokens that API will accept. Used by tests elsewhere in this package
// whose endpoints became browser-gated.
func enableBrowserAuth(t *testing.T, api *API) func(caps ...string) string {
	t.Helper()
	return enableBrowserAuthSid(t, api, testSID)
}

// enableBrowserAuthSid is enableBrowserAuth minting tokens that carry the given session id (""
// = a token from before core stamped `sid`).
func enableBrowserAuthSid(t *testing.T, api *API, sid string) func(caps ...string) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	return func(caps ...string) string {
		return mintRunnerToken(t, priv, "core-1", identity.Claims{
			Sub: "user-1", Aud: coreAuthAudience, Kind: "human", Sid: sid,
			Caps: caps, ExpiresAt: time.Now().Add(time.Minute).Unix(),
		})
	}
}

// dialBrowserWS dials srv's /ws/browser with the given subprotocol list.
func dialBrowserWS(srv *httptest.Server, protocols []string) (*websocket.Conn, *http.Response, error) {
	d := websocket.Dialer{Subprotocols: protocols}
	return d.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/browser", nil)
}

// TestServeBrowser_UnauthenticatedUpgradeRejected is the regression guard for
// the vulnerability where /ws/browser had no auth check at all: any origin
// could connect and immediately receive initial_state (every daemon, session
// and message) and issue state-changing commands.
func TestServeBrowser_UnauthenticatedUpgradeRejected(t *testing.T) {
	api, _ := browserWSTestAPI(t)
	hub := NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(nil, api.AuthorizeBrowserWS))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn, resp, err := dialBrowserWS(srv, nil)
	if err == nil {
		conn.Close()
		t.Fatal("dial succeeded without a token, want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

// TestServeBrowser_BadTokenUpgradeRejected: a syntactically well-formed
// subprotocol list carrying a token core never signed must not upgrade either.
func TestServeBrowser_BadTokenUpgradeRejected(t *testing.T) {
	api, _ := browserWSTestAPI(t)
	hub := NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(nil, api.AuthorizeBrowserWS))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn, resp, err := dialBrowserWS(srv, []string{wsBearerSubprotocol, "not.a.real.token"})
	if err == nil {
		conn.Close()
		t.Fatal("dial succeeded with a forged token, want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

// TestServeBrowser_TokenWithoutBrowserCapRejected: holding a core token is not
// enough — it must also assert coreAuthBrowserCap, exactly like the REST gate.
func TestServeBrowser_TokenWithoutBrowserCapRejected(t *testing.T) {
	api, mint := browserWSTestAPI(t)
	hub := NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(nil, api.AuthorizeBrowserWS))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn, resp, err := dialBrowserWS(srv, []string{wsBearerSubprotocol, mint("card.read")})
	if err == nil {
		conn.Close()
		t.Fatal("dial succeeded without " + coreAuthBrowserCap + ", want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

// TestServeBrowser_ValidTokenUpgradeSucceeds proves the gate is an auth check,
// not a blanket ban: a valid token upgrades, negotiates the "bearer"
// subprotocol (never echoing the token itself), and receives initial_state.
func TestServeBrowser_ValidTokenUpgradeSucceeds(t *testing.T) {
	api, mint := browserWSTestAPI(t)
	hub := NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(nil, api.AuthorizeBrowserWS))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tok := mint(coreAuthBrowserCap)
	conn, _, err := dialBrowserWS(srv, []string{wsBearerSubprotocol, tok})
	if err != nil {
		t.Fatalf("dial with valid token: %v", err)
	}
	defer conn.Close()

	if got := conn.Subprotocol(); got != wsBearerSubprotocol {
		t.Fatalf("negotiated subprotocol = %q, want %q", got, wsBearerSubprotocol)
	}
	if strings.Contains(conn.Subprotocol(), tok) {
		t.Fatal("server echoed the access token back as the negotiated subprotocol")
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read initial_state: %v", err)
	}
	if !strings.Contains(string(msg), "initial_state") {
		t.Fatalf("first message = %s, want initial_state", msg)
	}
}

// TestServeBrowser_NilAuthorizeFailsClosed: a call site that forgets to pass an
// authorizer must reject every upgrade rather than silently serving an open
// socket (which is exactly how the original bug read).
func TestServeBrowser_NilAuthorizeFailsClosed(t *testing.T) {
	hub := NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(nil, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn, resp, err := dialBrowserWS(srv, nil)
	if err == nil {
		conn.Close()
		t.Fatal("dial succeeded against a nil authorizer, want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

// TestCheckBrowserWSOrigin covers the CheckOrigin replacement: same-origin and
// header-less requests pass, a hostile cross-origin request does not, and an
// explicitly allowlisted origin does.
func TestCheckBrowserWSOrigin(t *testing.T) {
	t.Setenv("BLERG_RUNNER_ALLOWED_ORIGINS", "https://runner.example.com, https://ops.example.com")
	// browserWSAllowedOrigins is a sync.OnceValue; rebind it for this test so
	// the env var above is actually read (the production value is boot config).
	saved := browserWSAllowedOrigins
	browserWSAllowedOrigins = func() map[string]bool {
		return map[string]bool{
			"https://runner.example.com": true,
			"https://ops.example.com":    true,
		}
	}
	t.Cleanup(func() { browserWSAllowedOrigins = saved })

	newReq := func(origin, forwardedProto string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://runner.local/ws/browser", nil)
		r.Host = "runner.local"
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if forwardedProto != "" {
			r.Header.Set("X-Forwarded-Proto", forwardedProto)
		}
		return r
	}

	cases := []struct {
		origin         string
		forwardedProto string
		want           bool
	}{
		{"", "", true},                           // non-browser client
		{"http://runner.local", "", true},        // same origin
		{"https://runner.local", "", false},      // same host, different scheme: a different origin
		{"https://runner.local", "https", true},  // behind the TLS ingress the request IS https
		{"http://runner.local", "https", false},  // plain-http page against the https app
		{"https://evil.example.com", "", false},  // hostile page
		{"https://runner.example.com", "", true}, // allowlisted
		{"https://ops.example.com", "", true},    // allowlisted
		{"not a url", "", false},                 // unparseable
	}
	for _, c := range cases {
		if got := checkBrowserWSOrigin(newReq(c.origin, c.forwardedProto)); got != c.want {
			t.Errorf("checkBrowserWSOrigin(Origin=%q, X-Forwarded-Proto=%q) = %v, want %v", c.origin, c.forwardedProto, got, c.want)
		}
	}
}

// TestBrowserRESTEndpointsRequireToken covers the three browser-facing REST
// endpoints that the platform-identity project left ungated.
func TestBrowserRESTEndpointsRequireToken(t *testing.T) {
	api, mint := browserWSTestAPI(t)

	cases := []struct {
		name    string
		method  string
		path    string
		handler http.HandlerFunc
	}{
		{"GET /api/messages", http.MethodGet, "/api/messages", api.HandleGetMessages},
		{"GET /api/boards", http.MethodGet, "/api/boards", api.HandleGetBoards},
		{"POST /api/push/subscribe", http.MethodPost, "/api/push/subscribe", api.HandlePostPushSubscribe},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c.handler(w, httptest.NewRequest(c.method, c.path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("no token: status = %d, want 401", w.Code)
			}

			// A token lacking the browser capability is rejected too.
			w = httptest.NewRecorder()
			r := httptest.NewRequest(c.method, c.path, nil)
			r.Header.Set("Authorization", "Bearer "+mint("card.read"))
			c.handler(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("token without %s: status = %d, want 401", coreAuthBrowserCap, w.Code)
			}

			// A valid browser token gets past the gate (the handlers then
			// degrade gracefully on a nil dbPool — anything but 401 proves the
			// gate let it through).
			w = httptest.NewRecorder()
			r = httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer "+mint(coreAuthBrowserCap))
			c.handler(w, r)
			if w.Code == http.StatusUnauthorized {
				t.Fatalf("valid browser token was rejected with 401")
			}
		})
	}
}
