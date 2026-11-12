package mcpconn

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSessionAuthorityIgnoresEnvironmentProxy(t *testing.T) {
	if os.Getenv("BLG145_PROXY_HELPER") == "1" {
		destination, err := url.Parse(os.Getenv("BLG145_DIRECT_TARGET"))
		if err != nil {
			t.Fatal(err)
		}
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if address == "blerg-runner:80" {
				address = destination.Host
			} else if address != os.Getenv("BLG145_PROXY_ADDRESS") {
				return nil, errors.New("unexpected test network destination")
			}
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}
		http.DefaultTransport = base
		for _, configured := range []bool{false, true} {
			var supplied *http.Client
			if configured {
				supplied = &http.Client{Transport: base}
			}
			client := &HTTPSessionOAuthAuthority{BaseURL: "http://blerg-runner", InternalKey: "synthetic-private-key", HTTP: supplied}
			if err := client.Validate(context.Background(), SessionOAuthAuthorization{}); err != nil {
				t.Fatal(err)
			}
			if base.Proxy == nil {
				t.Fatal("source transport was mutated")
			}
		}
		return
	}
	var direct, proxied atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		direct.Add(1)
		if r.Header.Get("X-Internal-Key") != "synthetic-private-key" {
			t.Error("direct request missing key")
		}
		_, _ = w.Write([]byte(`{"live":true}`))
	}))
	defer target.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxied.Add(1); _, _ = w.Write([]byte(`{"live":true}`)) }))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestSessionAuthorityIgnoresEnvironmentProxy$")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "BLG145_PROXY_HELPER", "BLG145_DIRECT_TARGET", "BLG145_PROXY_ADDRESS":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "BLG145_PROXY_HELPER=1", "BLG145_DIRECT_TARGET="+target.URL, "BLG145_PROXY_ADDRESS="+proxyURL.Host, "HTTP_PROXY="+proxy.URL, "HTTPS_PROXY="+proxy.URL, "NO_PROXY=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("proxy helper failed: %v: %s", err, out)
	}
	if proxied.Load() != 0 || direct.Load() != 2 {
		t.Fatalf("internal key crossed proxy: proxy=%d direct=%d", proxied.Load(), direct.Load())
	}
}

type reviewAuthorityTransport func(*http.Request) (*http.Response, error)

func (f reviewAuthorityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSessionAuthorityServiceNamesRequireClusterFlag(t *testing.T) {
	for _, cluster := range []bool{false, true} {
		calls := 0
		client := &HTTPSessionOAuthAuthority{BaseURL: "http://service.synthetic.svc", InternalKey: "synthetic-private-key", ClusterDeployment: cluster, HTTP: &http.Client{Transport: reviewAuthorityTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"live":true}`)), Header: http.Header{}}, nil
		})}}
		err := client.Validate(context.Background(), SessionOAuthAuthorization{})
		if cluster {
			if err != nil || calls != 1 {
				t.Fatal("cluster service refused", err)
			}
		} else if err == nil || calls != 0 {
			t.Fatal("service suffix granted plaintext trust outside a cluster")
		}
	}
}
