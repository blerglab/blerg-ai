package mcpgw

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// ParseTrustedProxies reads a comma-separated list of CIDRs or addresses
// (BLERG_RUNNER_TRUSTED_PROXIES): the proxies, an ingress controller for example, whose
// X-Forwarded-For and X-Real-IP the gateway's public route believes. Empty is nil (trust none).
func ParseTrustedProxies(v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q is not a CIDR or an address", part)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

func trustedAddr(a netip.Addr, trusted []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// publicClientIP is the client address the public route throttles: the TCP peer, unless the peer
// is a trusted proxy; then the right-most X-Forwarded-For hop that is not itself trusted (the
// entries to its left are whatever the client chose to send), else X-Real-IP. A header that does
// not parse falls back to the peer.
func publicClientIP(r *http.Request, trusted []netip.Prefix) string {
	peer := clientIP(r)
	if len(trusted) == 0 {
		return peer
	}
	pa, err := netip.ParseAddr(peer)
	if err != nil || !trustedAddr(pa, trusted) {
		return peer
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	if len(hops) == 1 && strings.TrimSpace(hops[0]) == "" {
		hops = nil
	}
	for i := len(hops) - 1; i >= 0; i-- {
		h := strings.TrimSpace(hops[i])
		if host, _, err := net.SplitHostPort(h); err == nil {
			h = host
		}
		a, err := netip.ParseAddr(strings.Trim(h, "[]"))
		if err != nil {
			return peer
		}
		if !trustedAddr(a, trusted) || i == 0 {
			return a.Unmap().String()
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		if a, err := netip.ParseAddr(realIP); err == nil {
			return a.Unmap().String()
		}
	}
	return peer
}

// lookupBudget is the public route's global allowance for database lookups of well-formed tokens
// from addresses already over their failure allowance: a token bucket, generous enough that a
// valid token from a shared or noisy address is still looked up.
type lookupBudget struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func newLookupBudget(perSecond, burst int, now func() time.Time) *lookupBudget {
	return &lookupBudget{rate: float64(perSecond), burst: float64(burst), tokens: float64(burst), now: now}
}

func (b *lookupBudget) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.now()
	if !b.last.IsZero() {
		b.tokens += t.Sub(b.last).Seconds() * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}
	b.last = t
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
