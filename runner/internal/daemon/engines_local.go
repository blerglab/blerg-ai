package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Local-endpoint detection: an engine is "local" when its model endpoint is on this machine or its
// LAN and no provider key is configured for it, so a session on it spends hardware, not provider
// quota. The daemon reports the ids in hello and heartbeat (local_engines); the server uses them for
// the budget and for the local_only delegation rule. Everything here fails closed: an unreadable,
// unparseable or unclear config is "not local". Nothing read here is ever logged or sent.

const (
	localResolveTimeout = 2 * time.Second
	localEnginesTTL     = 5 * time.Minute
	// openclawConfigMaxRead bounds the config read; a larger file is "not local".
	openclawConfigMaxRead = 1 << 20
)

// openclawProviderKeyEnv are the environment variables that make an OpenClaw endpoint a paid one.
var openclawProviderKeyEnv = []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"}

// lookupIPs resolves a host; a variable so tests need no network.
var lookupIPs = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// endpointIsLocal reports whether rawURL's host is "localhost" or every address it resolves to is
// loopback or private (RFC 1918 / ULA). Resolution is bounded to 2 s. Unparseable → false.
func endpointIsLocal(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	isLocalIP := func(ip net.IP) bool { return ip.IsLoopback() || ip.IsPrivate() }
	if ip := net.ParseIP(host); ip != nil {
		return isLocalIP(ip)
	}
	ctx, cancel := context.WithTimeout(context.Background(), localResolveTimeout)
	defer cancel()
	ips, err := lookupIPs(ctx, host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !isLocalIP(ip) {
			return false
		}
	}
	return true
}

// hermesLocal: ~/.hermes/config.yaml's model base_url is local AND ~/.hermes/.env holds no provider
// key line.
func hermesLocal() bool {
	home := homeDir()
	if home == "" {
		return false
	}
	if hermesConfigKeyPattern.MatchString(readFile(filepath.Join(home, ".hermes", ".env"))) || providerKeyInEnv() || hermesProviderCredentialPresent(home) {
		return false
	}
	cfg, err := readHermesRouteConfig(filepath.Join(home, ".hermes", "config.yaml"))
	return err == nil && hermesRoutesLocal(cfg)
}

// hermesRoutesLocal is the configuration half of hermesLocal: the default
// endpoint and every enabled provider are local, and none carries a key.
func hermesRoutesLocal(cfg hermesRouteConfig) bool {
	if cfg.Default.BaseURL == "" || cfg.Fallback || cfg.Unknown || cfg.LocalUnknown || (cfg.Default.Provider != "" && cfg.Default.Provider != "custom") {
		return false
	}
	if !endpointIsLocal(cfg.Default.BaseURL) {
		return false
	}
	for _, p := range cfg.Providers {
		if !p.Disabled && (p.Credential || !endpointIsLocal(p.BaseURL)) {
			return false
		}
	}
	return true
}

// providerKeyInEnv reports whether the daemon's own environment holds a provider key (names only;
// the values are never read into a log or a message).
func providerKeyInEnv() bool {
	for _, k := range openclawProviderKeyEnv {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// stripJSON5 removes line and block comments and trailing commas (outside strings) so a JSON5-ish
// config parses as JSON. Anything else JSON5 allows (unquoted keys, single quotes) is left alone and
// fails the parse, which is "not local".
func stripJSON5(src string) []byte {
	out := make([]byte, 0, len(src))
	inStr := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case inStr:
			out = append(out, c)
			if c == '\\' && i+1 < len(src) {
				i++
				out = append(out, src[i])
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return append(out, '/', '*') // unterminated: leave it to fail the parse
			}
			i += 2 + end + 1
			out = append(out, ' ')
		case c == '}' || c == ']':
			j := len(out) - 1
			for j >= 0 && (out[j] == ' ' || out[j] == '\n' || out[j] == '\t' || out[j] == '\r') {
				j--
			}
			if j >= 0 && out[j] == ',' {
				out = append(out[:j], out[j+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// openclawLocal: every "baseUrl"/"base_url" string in ~/.openclaw/openclaw.json is local, at least
// one exists, and no provider key is set in the daemon's environment.
func openclawLocal() bool {
	if providerKeyInEnv() {
		return false
	}
	home := homeDir()
	if home == "" {
		return false
	}
	path := filepath.Join(home, ".openclaw", "openclaw.json")
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || info.Size() > openclawConfigMaxRead {
		return false
	}
	var doc any
	if err := json.Unmarshal(stripJSON5(readFile(path)), &doc); err != nil {
		return false
	}
	var urls []string
	collectBaseURLs(doc, &urls, 0)
	if len(urls) == 0 {
		return false
	}
	for _, u := range urls {
		if !endpointIsLocal(u) {
			return false
		}
	}
	return true
}

// collectBaseURLs walks a decoded JSON value and gathers every string under a baseUrl/base_url key.
// A non-string value under such a key is kept as an empty string, which is not local.
func collectBaseURLs(v any, out *[]string, depth int) {
	if depth > 32 {
		*out = append(*out, "") // too deep to trust
		return
	}
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "baseUrl" || k == "base_url" {
				s, _ := val.(string)
				*out = append(*out, s)
				continue
			}
			collectBaseURLs(val, out, depth+1)
		}
	case []any:
		for _, val := range t {
			collectBaseURLs(val, out, depth+1)
		}
	}
}

var localCache struct {
	mu   sync.Mutex
	at   time.Time
	home string
	ids  []string
}

func resetLocalEnginesCache() {
	localCache.mu.Lock()
	localCache.at = time.Time{}
	localCache.mu.Unlock()
}

// localEngines returns the ids whose DetectLocal is true, in engineOrder. Cached for 5 minutes (per
// home directory) so a heartbeat does not resolve DNS every time.
func localEngines() []string {
	home := homeDir()
	localCache.mu.Lock()
	defer localCache.mu.Unlock()
	if !localCache.at.IsZero() && localCache.home == home && time.Since(localCache.at) < localEnginesTTL {
		return append([]string(nil), localCache.ids...)
	}
	var ids []string
	for _, id := range engineOrder {
		if d := engineRegistry[id].DetectLocal; d != nil && d() {
			ids = append(ids, id)
		}
	}
	localCache.at, localCache.home, localCache.ids = time.Now(), home, ids
	return append([]string(nil), ids...)
}

// A cached native login can fund auxiliary fallback calls even when the main
// endpoint needs no key. Empty native metadata is harmless; actual or unknown
// authority withholds the claim. Credential values are never interpreted or reported.
func hermesProviderCredentialPresent(home string) bool {
	if hermesAuthStoreHasAuthority(filepath.Join(home, ".hermes", "auth.json")) {
		return true
	}
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if ok && value != "" && strings.HasSuffix(name, "_API_KEY") && !strings.HasPrefix(name, "BLERG_") {
			return true
		}
	}
	return false
}

func hermesAuthStoreHasAuthority(path string) bool {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // trusted native auth path, nonblocking regular file and bounded read only.
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 256<<10 {
		return true
	}
	data, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil || len(data) > 256<<10 {
		return true
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root == nil {
		return true
	}
	for key, raw := range root {
		switch key {
		case "version", "updated_at":
			continue
		case "active_provider":
			var value *string
			if json.Unmarshal(raw, &value) != nil || (value != nil && *value != "") {
				return true
			}
		case "providers", "credential_pool":
			if string(raw) == "null" {
				continue
			}
			var entries map[string]json.RawMessage
			if json.Unmarshal(raw, &entries) != nil {
				return true
			}
			for _, entry := range entries {
				if string(entry) != "null" && string(entry) != "[]" && string(entry) != "{}" {
					return true
				}
			}
		default:
			return true
		}
	}
	return false
}
