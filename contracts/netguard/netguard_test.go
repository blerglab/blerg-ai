package netguard

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseHostList(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"  ", nil},
		{"a.example", []string{"a.example"}},
		{" A.Example , b.example,,", []string{"a.example", "b.example"}},
		{"[::1],localhost", []string{"::1", "localhost"}},
	}
	for _, c := range cases {
		got := ParseHostList(c.in)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("ParseHostList(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCheckURL(t *testing.T) {
	t.Parallel()
	p := Policy{AllowHTTPHosts: []string{"plain.example", "127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}}
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"https ok", "https://mcp.example/path", true},
		{"https with port", "https://mcp.example:8443/x", true},
		{"http not listed", "http://mcp.example/x", false},
		{"http listed", "http://plain.example/x", true},
		{"http listed host uppercase", "http://PLAIN.example:8080/x", true},
		{"http listed ip", "http://127.0.0.1:9/x", true},
		{"userinfo", "https://user:pw@mcp.example/", false},
		{"user only", "https://user@mcp.example/", false},
		{"ftp", "ftp://mcp.example/", false},
		{"no scheme", "mcp.example/x", false},
		{"empty host", "https:///x", false},
		{"empty", "", false},
		{"garbage", "http://[::1", false},
		{"uppercase scheme https", "HTTPS://mcp.example/", true},
		// A literal internal address is refused up front (at connection create time), not only
		// when the socket is opened, unless the operator listed it.
		{"literal listed private", "https://127.0.0.1/x", true},
		{"literal public", "https://8.8.8.8/x", true},
		{"literal metadata", "https://169.254.169.254/latest", false},
		{"literal rfc1918", "https://10.1.2.3/x", false}, // scrub:allow (a range the guard must refuse)
		{"literal cgnat", "https://100.64.0.9/x", false}, // scrub:allow (a range the guard must refuse)
		{"literal ula", "https://[fd00::1]/x", false},
		{"literal v4-mapped", "https://[::ffff:10.0.0.1]/x", false}, // scrub:allow (a range the guard must refuse)
		{"literal 6to4", "https://[2002:a00:1::1]/x", false},
		{"literal loopback unlisted", "https://127.0.0.2/x", false},
	}
	for _, c := range cases {
		err := p.CheckURL(c.raw)
		if (err == nil) != c.ok {
			t.Errorf("%s: CheckURL(%q) err=%v, want ok=%v", c.name, c.raw, err, c.ok)
		}
	}
}

func TestBlockedIP(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"127.0.0.1", "127.255.255.254", "::1",
		"0.0.0.0", "::", "0.1.2.3",
		"169.254.169.254", "169.254.1.1", "fe80::1", "fd00:ec2::254",
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1", // scrub:allow (private ranges the guard must refuse)
		"fc00::1", "fd12:3456::1",
		"100.64.0.1", "100.100.100.200", "100.127.255.255", // scrub:allow (private ranges the guard must refuse)
		"224.0.0.1", "239.255.255.250", "ff02::1", "ff05::2",
		"198.18.0.1", "240.0.0.1", "255.255.255.255", "192.0.0.192",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", // scrub:allow (private ranges the guard must refuse)
		"::ffff:0.0.0.0", "::ffff:100.64.0.1", "::ffff:192.168.0.1", // scrub:allow (private ranges the guard must refuse)
		"::ffff:224.0.0.1", "64:ff9b::7f00:1", "64:ff9b::a00:1",
		"2002:7f00:1::1", "2002:a00:1::", "2002::1", // 6to4 embeds an IPv4 address
		"2001::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2", // Teredo
		"fec0::1", "feff::1", // deprecated site-local
		"192.88.99.1", "192.88.99.255", // 6to4 relay anycast
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test ip %q", s)
		}
		if !blockedIP(ip) {
			t.Errorf("%s should be blocked", s)
		}
	}
	open := []string{
		"8.8.8.8", "1.1.1.1", "93.184.216.34", "172.32.0.1", "100.128.0.1",
		"2606:4700:4700::1111", "::ffff:8.8.8.8", "64:ff9b::808:808",
		"2001:4860:4860::8888", "2003::1", "192.88.98.1", "192.88.100.1",
	}
	for _, s := range open {
		if blockedIP(net.ParseIP(s)) {
			t.Errorf("%s should not be blocked", s)
		}
	}
}

func TestControlRejectsBlockedRanges(t *testing.T) {
	t.Parallel()
	addrs := []string{
		"127.0.0.1:80", "[::1]:80", "[::ffff:10.0.0.1]:80", // scrub:allow (private ranges the guard must refuse)
		"0.0.0.0:80", "[::]:80", "169.254.169.254:80",
	}
	for _, addr := range addrs {
		if err := control(false)("tcp", addr, nil); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("control(false)(%s) = %v, want ErrBlockedAddress", addr, err)
		}
		if err := control(true)("tcp", addr, nil); err != nil {
			t.Errorf("control(true)(%s) = %v, want nil", addr, err)
		}
	}
	if err := control(false)("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("public address rejected: %v", err)
	}
	if err := control(false)("tcp", "not-an-address", nil); err == nil {
		t.Error("unparsable address accepted")
	}
}

func newServer(t *testing.T, h http.HandlerFunc) (host, port string) {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname(), u.Port()
}

func get(t *testing.T, c *http.Client, raw string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, raw, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	return c.Do(req)
}

func okHandler(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }

func TestClientLoopbackBlockedUnlessAllowListed(t *testing.T) {
	host, port := newServer(t, okHandler)
	target := "http://" + host + ":" + port + "/"

	// http allowed, private not: blocked at connect time.
	p := Policy{AllowHTTPHosts: []string{host}}
	resp, err := get(t, p.Client(5*time.Second, 1024), target)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("loopback reached without AllowPrivateHosts")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress", err)
	}

	// Listed as private: succeeds.
	p.AllowPrivateHosts = []string{host}
	resp, err = get(t, p.Client(5*time.Second, 1024), target)
	if err != nil {
		t.Fatalf("allow-listed private host: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || string(b) != "ok" {
		t.Fatalf("body = %q, %v", b, err)
	}
}

func TestClientIPv4MappedLiteralBlocked(t *testing.T) {
	_, port := newServer(t, okHandler)
	p := Policy{AllowHTTPHosts: []string{"::ffff:127.0.0.1", "0.0.0.0", "localhost"}}
	for _, h := range []string{"[::ffff:127.0.0.1]", "0.0.0.0", "localhost"} {
		resp, err := get(t, p.Client(5*time.Second, 1024), "http://"+h+":"+port+"/")
		if err == nil {
			_ = resp.Body.Close()
			t.Errorf("%s reached", h)
			continue
		}
		if !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s: err = %v, want ErrBlockedAddress", h, err)
		}
	}
}

func TestClientHostnameResolvingToPrivateRefused(t *testing.T) {
	_, port := newServer(t, okHandler)
	mapping := map[string]string{"rebind.example": "127.0.0.1"}
	target := "http://rebind.example:" + port + "/"

	p := Policy{AllowHTTPHosts: []string{"rebind.example"}, hostMap: mapping}
	resp, err := get(t, p.Client(5*time.Second, 1024), target)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("name resolving to loopback was dialled")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress", err)
	}

	p.AllowPrivateHosts = []string{"rebind.example"}
	resp, err = get(t, p.Client(5*time.Second, 1024), target)
	if err != nil {
		t.Fatalf("listed host: %v", err)
	}
	_ = resp.Body.Close()
}

func TestClientSchemeAndUserinfoRules(t *testing.T) {
	host, port := newServer(t, okHandler)
	p := Policy{AllowPrivateHosts: []string{host}}
	c := p.Client(5*time.Second, 1024)
	for _, raw := range []string{
		"http://" + host + ":" + port + "/",     // http not allow-listed
		"http://u:p@" + host + ":" + port + "/", // userinfo
		"ftp://" + host + "/",                   // wrong scheme
	} {
		resp, err := get(t, c, raw)
		if err == nil {
			_ = resp.Body.Close()
			t.Errorf("%s accepted", raw)
		}
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var hits int
	host, port := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/final" {
			_, _ = io.WriteString(w, "final")
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	p := Policy{AllowHTTPHosts: []string{host}, AllowPrivateHosts: []string{host}}
	resp, err := get(t, p.Client(5*time.Second, 1024), "http://"+host+":"+port+"/start")
	if err != nil {
		t.Fatalf("redirect should return the 302, got error %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302", resp.StatusCode)
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}
}

func TestClientIgnoresProxyEnv(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	host, port := newServer(t, okHandler)
	p := Policy{AllowHTTPHosts: []string{host}, AllowPrivateHosts: []string{host}}
	resp, err := get(t, p.Client(5*time.Second, 1024), "http://"+host+":"+port+"/")
	if err != nil {
		t.Fatalf("proxy env honoured? %v", err)
	}
	_ = resp.Body.Close()
}

func TestClientBodyCap(t *testing.T) {
	cases := []struct {
		name    string
		size    int
		chunked bool
		limit   int64
		wantErr bool
	}{
		{"under", 10, false, 100, false},
		{"exactly", 100, false, 100, false},
		{"over content-length", 101, false, 100, true},
		{"over chunked", 500, true, 100, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host, port := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
				body := strings.Repeat("a", c.size)
				if c.chunked {
					f, _ := w.(http.Flusher)
					_, _ = io.WriteString(w, body[:c.size/2])
					f.Flush()
					_, _ = io.WriteString(w, body[c.size/2:])
					return
				}
				_, _ = io.WriteString(w, body)
			})
			p := Policy{AllowHTTPHosts: []string{host}, AllowPrivateHosts: []string{host}}
			resp, err := get(t, p.Client(5*time.Second, c.limit), "http://"+host+":"+port+"/")
			if err == nil {
				defer resp.Body.Close()
				_, err = io.ReadAll(resp.Body)
			}
			if c.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if c.wantErr && !errors.Is(err, ErrBodyTooLarge) {
				t.Errorf("err = %v, want ErrBodyTooLarge", err)
			}
		})
	}
}

// A name whose answer lists a public address first and a private one second (a rebind or a
// split-horizon trick) must never end up connected to the private one: every address the dialer
// tries goes through the check, not just the first.
func TestClientMultiAnswerNeverConnectsToPrivate(t *testing.T) {
	var hits atomic.Int32
	_, port := newServer(t, func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); okHandler(w, nil) })
	p := Policy{
		AllowHTTPHosts: []string{"multi.example"},
		hostMap:        map[string]string{"multi.example": "192.0.2.1,127.0.0.1"}, // TEST-NET-1 first, loopback second
	}
	resp, err := get(t, p.Client(2*time.Second, 1024), "http://multi.example:"+port+"/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the private second answer was connected to")
	}
	if hits.Load() != 0 {
		t.Fatalf("the private server saw %d requests", hits.Load())
	}
	// Listed by the operator: the dialer moves past a refused first answer to the second, so the
	// failure above was the guard and not the fake setup.
	p.hostMap = map[string]string{"multi.example": "127.0.0.2,127.0.0.1"}
	p.AllowPrivateHosts = []string{"multi.example"}
	resp, err = get(t, p.Client(2*time.Second, 1024), "http://multi.example:"+port+"/")
	if err != nil {
		t.Fatalf("listed host, unreachable first answer, reachable second: %v", err)
	}
	_ = resp.Body.Close()
}

// A response header block past the cap is refused, not buffered.
func TestClientResponseHeaderCap(t *testing.T) {
	host, port := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Big", strings.Repeat("a", 200<<10))
		_, _ = io.WriteString(w, "ok")
	})
	p := Policy{AllowHTTPHosts: []string{host}, AllowPrivateHosts: []string{host}}
	resp, err := get(t, p.Client(5*time.Second, 1024), "http://"+host+":"+port+"/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a 200 KiB header block was accepted")
	}
}
