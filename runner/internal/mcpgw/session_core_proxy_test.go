package mcpgw

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

func TestSessionCoreIgnoresEnvironmentProxy(t *testing.T) {
	if os.Getenv("BLG145_PROXY_HELPER") == "1" {
		destination, err := url.Parse(os.Getenv("BLG145_DIRECT_TARGET"))
		if err != nil {
			t.Fatal(err)
		}
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if address == "blerg-core:80" {
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
			client := &HTTPCoreClient{BaseURL: "http://blerg-core", InternalKey: "synthetic-private-key", HTTP: supplied}
			if err := client.SessionAdmit(context.Background(), dispatchFixture()); err != nil {
				t.Fatal(err)
			}
			if credential, err := client.SessionToken(context.Background(), dispatchFixture()); err != nil || credential.Value != "Bearer synthetic-access" {
				t.Fatal("direct token response failed", err)
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
		writeReviewCoreResponse(w, r)
	}))
	defer target.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxied.Add(1); writeReviewCoreResponse(w, r) }))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestSessionCoreIgnoresEnvironmentProxy$")
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
	if proxied.Load() != 0 || direct.Load() != 4 {
		t.Fatalf("internal key crossed proxy: proxy=%d direct=%d", proxied.Load(), direct.Load())
	}
}

type reviewAuthorityTransport func(*http.Request) (*http.Response, error)

func (f reviewAuthorityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSessionCoreServiceNamesRequireClusterFlag(t *testing.T) {
	for _, cluster := range []bool{false, true} {
		calls := 0
		client := &HTTPCoreClient{BaseURL: "http://service.synthetic.svc", InternalKey: "synthetic-private-key", ClusterDeployment: cluster, HTTP: &http.Client{Transport: reviewAuthorityTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"live":true}`)), Header: http.Header{}}, nil
		})}}
		err := client.SessionAdmit(context.Background(), dispatchFixture())
		if cluster {
			if err != nil || calls != 1 {
				t.Fatal("cluster service refused", err)
			}
		} else if err == nil || calls != 0 {
			t.Fatal("service suffix granted plaintext trust outside a cluster")
		}
	}
}

func writeReviewCoreResponse(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/token") {
		_, _ = w.Write([]byte(`{"url":"https://resource.example/mcp","header_name":"Authorization","value":"Bearer synthetic-access","pin_policy":"strict"}`))
		return
	}
	_, _ = w.Write([]byte(`{"live":true}`))
}
