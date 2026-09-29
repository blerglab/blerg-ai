package api

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

const wsBearerSubprotocol = "bearer"

// wsBearerToken returns the token a browser offered as ["bearer", <token>] in
// Sec-WebSocket-Protocol (browsers cannot set Authorization on an upgrade).
func wsBearerToken(r *http.Request) string {
	protos := websocket.Subprotocols(r)
	for i, p := range protos {
		if p == wsBearerSubprotocol && i+1 < len(protos) {
			return protos[i+1]
		}
	}
	return ""
}

// wsAllowedOrigins parses BLERG_BOARD_ALLOWED_ORIGINS — a comma-separated
// list of scheme://host origins permitted to open a board WebSocket
// cross-origin. Same shape as runner's BLERG_RUNNER_ALLOWED_ORIGINS.
//
// Parsed once, lazily: the value is process configuration and never changes
// after boot.
var wsAllowedOrigins = sync.OnceValue(func() map[string]bool {
	out := map[string]bool{}
	for _, o := range strings.Split(os.Getenv("BLERG_BOARD_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			out[strings.ToLower(o)] = true
		}
	}
	return out
})

// checkWSOrigin replaces the old unconditional `return true`. A browser
// always sends Origin on a WebSocket handshake, so this is the CSRF-equivalent
// defence for the upgrade: without it any page on the internet could open a
// socket against a logged-in user's board.
//
// Accepted: a request with no Origin at all (non-browser clients — the CLI,
// MCP tooling, and Go tests — which cannot be driven by a hostile page
// anyway), a same scheme+host request as the request's own Host, and any
// origin explicitly listed in BLERG_BOARD_ALLOWED_ORIGINS.
func checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	if strings.EqualFold(u.Scheme+"://"+u.Host, scheme+"://"+r.Host) {
		return true
	}
	return wsAllowedOrigins()[strings.ToLower(u.Scheme+"://"+u.Host)]
}
