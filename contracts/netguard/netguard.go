// Package netguard is the SSRF-safe outbound HTTP policy shared by core and
// the runner for fetching user-supplied URLs (MCP servers, OAuth discovery).
//
// The guard runs on the resolved IP address at connect time, on every new
// connection, so a name that resolves (or rebinds) to an internal address is
// refused no matter what the URL looked like. Redirects are never followed and
// proxy environment variables are never honoured.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"
)

var (
	// ErrBlockedAddress means the resolved address is one the policy refuses.
	ErrBlockedAddress = errors.New("netguard: address not permitted")
	// ErrBodyTooLarge means a response body exceeded the configured cap.
	ErrBodyTooLarge = errors.New("netguard: response body exceeds size limit")
	// ErrURLNotPermitted means the URL failed the scheme/userinfo rules.
	ErrURLNotPermitted = errors.New("netguard: url not permitted")
)

// Policy is the operator-supplied allow-list. The zero value is the strictest
// policy: https only, no private addresses.
type Policy struct {
	// AllowHTTPHosts lists hostnames (no port) that may be reached over plain http.
	AllowHTTPHosts []string
	// AllowPrivateHosts lists hostnames (no port) exempt from the address check.
	AllowPrivateHosts []string

	// hostMap substitutes the address dialled for a hostname. Tests use it to
	// simulate DNS; the address check still runs on the substituted address.
	hostMap map[string]string
}

// ParseHostList splits a comma separated environment value into lower-cased
// hostnames, dropping blanks and IPv6 brackets.
func ParseHostList(csv string) []string {
	var out []string
	for part := range strings.SplitSeq(csv, ",") {
		h := normalizeHost(part)
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}

func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".")
	return strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
}

func hostListed(list []string, host string) bool {
	return slices.Contains(list, normalizeHost(host))
}

// CheckURL validates a URL against the scheme and userinfo rules: https only
// unless the host is in AllowHTTPHosts, and no embedded credentials.
func (p Policy) CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: unparsable", ErrURLNotPermitted)
	}
	return p.checkParsed(u)
}

func (p Policy) checkParsed(u *url.URL) error {
	if u.User != nil {
		return fmt.Errorf("%w: credentials in url", ErrURLNotPermitted)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrURLNotPermitted)
	}
	// A literal address is judged here, up front, so a connection pointing at an internal or
	// metadata address is refused when it is created and not only when a socket opens. Names
	// cannot be judged without resolving them; the connect-time check covers those.
	if addr, err := netip.ParseAddr(host); err == nil && !hostListed(p.AllowPrivateHosts, host) && blockedAddr(addr) {
		return fmt.Errorf("%w: address not permitted", ErrBlockedAddress)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if hostListed(p.AllowHTTPHosts, host) {
			return nil
		}
		return fmt.Errorf("%w: http not allowed for this host", ErrURLNotPermitted)
	default:
		return fmt.Errorf("%w: scheme %q", ErrURLNotPermitted, u.Scheme)
	}
}

func mustPrefix(s string) netip.Prefix {
	return netip.MustParsePrefix(s)
}

// Ranges net/netip does not classify as private or special but that belong to
// infrastructure. 100.64/10 is carrier-grade NAT (which also holds several
// cloud metadata addresses); 0.0.0.0/8 is "this network"; 192.0.0.0/24 is the
// IETF protocol block (one cloud metadata address lives there); 198.18/15 is
// benchmarking; 240/4 is reserved and includes the broadcast address.
var blockedPrefixes = []netip.Prefix{
	mustPrefix("0.0.0.0/8"),
	mustPrefix("100.64.0.0/10"), // scrub:allow (a blocked range, not a host)
	mustPrefix("192.0.0.0/24"),
	mustPrefix("198.18.0.0/15"),
	mustPrefix("240.0.0.0/4"),
	mustPrefix("192.88.99.0/24"), // 6to4 relay anycast (deprecated)
	mustPrefix("2002::/16"),      // 6to4: embeds an IPv4 address the guard cannot see through
	mustPrefix("2001::/32"),      // Teredo: same
	mustPrefix("fec0::/10"),      // deprecated site-local, still routed inside some networks
}

// nat64 is the well-known NAT64 prefix; the low 32 bits carry an IPv4 address.
var nat64 = mustPrefix("64:ff9b::/96")

// blockedIP reports whether an address is one the policy refuses: loopback,
// link-local, unspecified, RFC1918, ULA, CGNAT, multicast, cloud metadata and
// IPv4-mapped or NAT64 forms of any of these.
func blockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	return blockedAddr(addr)
}

func blockedAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr.Is6() && nat64.Contains(addr) {
		b := addr.As16()
		addr = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() {
		return true
	}
	for _, pfx := range blockedPrefixes {
		if pfx.Contains(addr) {
			return true
		}
	}
	return false
}

// control returns a net.Dialer.Control hook that runs on the address about to
// be connected to (the resolved IP), before the socket connects.
func control(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: unparsable address", ErrBlockedAddress)
		}
		if blockedAddr(ap.Addr()) {
			return ErrBlockedAddress
		}
		return nil
	}
}

// maxResponseHeaderBytes caps the header block of a response; an MCP server has no use for more.
const maxResponseHeaderBytes = 64 << 10

// Client returns an http.Client that enforces the policy: URL rules on every
// request, the connect-time address check, no redirects, no proxy, an overall
// timeout, and response bodies capped at maxBody bytes (reading past the cap
// fails with ErrBodyTooLarge).
func (p Policy) Client(timeout time.Duration, maxBody int64) *http.Client {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("netguard: %w", err)
			}
			allow := hostListed(p.AllowPrivateHosts, host)
			d := &net.Dialer{Timeout: timeout, Control: control(allow)}
			if mapped, ok := p.hostMap[normalizeHost(host)]; ok {
				// Test DNS: a comma separated answer list, tried in order like a real
				// resolver's, each one through the same connect-time check.
				var lastErr error
				for ip := range strings.SplitSeq(mapped, ",") {
					conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip, port))
					if err == nil {
						return conn, nil
					}
					lastErr = err
				}
				return nil, lastErr
			}
			return d.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
		MaxIdleConns:           10,
		IdleConnTimeout:        30 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &guardedTransport{policy: p, next: tr, maxBody: maxBody},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type guardedTransport struct {
	policy  Policy
	next    http.RoundTripper
	maxBody int64
}

func (g *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := g.policy.checkParsed(req.URL); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	resp, err := g.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > g.maxBody {
		_ = resp.Body.Close()
		return nil, ErrBodyTooLarge
	}
	resp.Body = &cappedBody{rc: resp.Body, remaining: g.maxBody}
	return resp, nil
}

type cappedBody struct {
	rc        io.ReadCloser
	remaining int64
}

func (c *cappedBody) Read(b []byte) (int, error) {
	if c.remaining < 0 {
		return 0, ErrBodyTooLarge
	}
	// Allow one byte past the cap so an over-long body is detected, not truncated.
	if int64(len(b)) > c.remaining+1 {
		b = b[:c.remaining+1]
	}
	n, err := c.rc.Read(b)
	c.remaining -= int64(n)
	if c.remaining < 0 {
		return max(n+int(c.remaining), 0), ErrBodyTooLarge
	}
	return n, err
}

func (c *cappedBody) Close() error { return c.rc.Close() }
