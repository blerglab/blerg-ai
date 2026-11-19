package livetls

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDedicatedTransport(t *testing.T) {
	root := t.TempDir()
	if err := Provision(root); err != nil {
		t.Fatal(err)
	}
	core, err := Load(filepath.Join(root, "core"), "core")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := Load(filepath.Join(root, "runner"), "runner")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: core.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })), ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(tls.NewListener(listener, core.ServerTLS())) }()
	t.Cleanup(func() { _ = srv.Close(); <-done })
	client := runner.Client()
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
	}
	for _, tc := range []struct {
		name, path, forward string
		status              int
	}{
		{"valid", "/internal/live-data/proposals", "", 204},
		{"public route", "/mcp-gw/", "", 404},
		{"forwarded", "/internal/live-data/proposals", "canary-token", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "https://blerg-core"+tc.path, nil)
			if tc.forward != "" {
				req.Header.Set("Forwarded", tc.forward)
			}
			res, e := client.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			res.Body.Close()
			if res.StatusCode != tc.status {
				t.Fatalf("status %d", res.StatusCode)
			}
		})
	}
	for _, kind := range []string{"missing", "wrong role", "wrong CA"} {
		t.Run(kind, func(t *testing.T) {
			c := runner.Client()
			tr := c.Transport.(*http.Transport)
			tr.DialContext = client.Transport.(*http.Transport).DialContext
			switch kind {
			case "missing":
				tr.TLSClientConfig.Certificates = nil
			case "wrong role":
				tr.TLSClientConfig.Certificates = core.ServerTLS().Certificates
			case "wrong CA":
				other := t.TempDir()
				if e := Provision(other); e != nil {
					t.Fatal(e)
				}
				foreign, e := Load(filepath.Join(other, "runner"), "runner")
				if e != nil {
					t.Fatal(e)
				}
				tr.TLSClientConfig.Certificates = foreign.ServerTLS().Certificates
			}
			res, e := c.Post("https://blerg-core/internal/live-data/proposals", "application/json", nil)
			if e == nil {
				res.Body.Close()
				t.Fatal("untrusted certificate accepted")
			}
			if !Refused(e) {
				t.Fatal("TLS refusal misclassified as login or generic network failure")
			}
		})
	}
	before := core.PeerURI
	if err := Provision(root); err != nil {
		t.Fatal(err)
	}
	core, err = Load(filepath.Join(root, "core"), "core")
	if err != nil || core.PeerURI != before {
		t.Fatal("identity changed on re-provision")
	}
}

func TestBundleRefusals(t *testing.T) {
	for _, kind := range []string{"missing", "role", "permissions", "identity", "key", "partial authority"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if err := Provision(root); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "runner")
			switch kind {
			case "missing":
				_ = os.Remove(filepath.Join(dir, "ca.pem"))
			case "role":
				dir = filepath.Join(root, "core")
			case "permissions":
				_ = os.Chmod(filepath.Join(dir, "key.pem"), 0644)
			case "identity":
				_ = os.WriteFile(filepath.Join(dir, "identity.json"), []byte(`{"installation":"invalid"}`), 0600)
			case "key":
				_ = os.WriteFile(filepath.Join(dir, "key.pem"), []byte("invalid"), 0600)
			case "partial authority":
				_ = os.Remove(filepath.Join(authorityPath(filepath.Join(root, "authority")), "identity.json"))
				if Provision(root) == nil {
					t.Fatal("replaced incomplete authority")
				}
				return
			}
			if _, err := Load(dir, "runner"); err == nil {
				t.Fatal("unsafe bundle accepted")
			}
		})
	}
}

func TestDedicatedHandlerAndPeer(t *testing.T) {
	root := t.TempDir()
	if e := Provision(root); e != nil {
		t.Fatal(e)
	}
	core, e := Load(filepath.Join(root, "core"), "core")
	if e != nil {
		t.Fatal(e)
	}
	runner, e := Load(filepath.Join(root, "runner"), "runner")
	if e != nil {
		t.Fatal(e)
	}
	called := 0
	handler := runner.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(204) }))
	for _, tc := range []struct {
		name string
		cert tls.Certificate
		path string
		want int
	}{
		{"missing", tls.Certificate{}, "/internal/live-data/definition", 401},
		{"wrong role", runner.certificate, "/internal/live-data/definition", 401},
		{"valid", core.certificate, "/internal/live-data/definition", 204},
		{"posture", core.certificate, "/internal/live-data/posture", 204},
		{"posture wrong role", runner.certificate, "/internal/live-data/posture", 401},
		{"gateway", core.certificate, "/mcp-gw/", 404},
		{"sandbox", core.certificate, "/internal/tokens", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tc.path, nil)
			if len(tc.cert.Certificate) > 0 {
				leaf, e := x509.ParseCertificate(tc.cert.Certificate[0])
				if e != nil {
					t.Fatal(e)
				}
				req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d", rec.Code)
			}
		})
	}
	if called != 2 {
		t.Fatal("handler reached on refused request")
	}
}

// The shipped listener, not httptest's TLS configuration, runs in a separate
// process. Only its bounded ready signal and public response cross the boundary.
func TestTLSListenerProcess(t *testing.T) {
	if os.Getenv("BLERG_TLS_TEST_HELPER") == "1" {
		identity, e := Load(os.Getenv("BLERG_TLS_TEST_DIR"), "core")
		if e != nil {
			os.Exit(2)
		}
		listener, e := identity.Listen(context.Background(), "127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), func() { os.Exit(3) })
		if e != nil {
			os.Exit(4)
		}
		fmt.Println(listener.(*http.Server).Addr)
		var stop [1]byte
		_, _ = os.Stdin.Read(stop[:])
		_ = listener.Close()
		os.Exit(0)
	}
	root := t.TempDir()
	if e := Provision(root); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTLSListenerProcess$")
	cmd.Env = append(os.Environ(), "BLERG_TLS_TEST_HELPER=1", "BLERG_TLS_TEST_DIR="+filepath.Join(root, "core"))
	input, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	output, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	var logs bytes.Buffer
	cmd.Stderr = &logs
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		_ = input.Close()
		if e := cmd.Wait(); e != nil {
			t.Errorf("listener process failed: %v", e)
		}
	}()
	ready := bufio.NewScanner(output)
	if !ready.Scan() {
		t.Fatal("no ready signal")
	}
	addr := ready.Text()
	runner, e := Load(filepath.Join(root, "runner"), "runner")
	if e != nil {
		t.Fatal(e)
	}
	client := runner.Client()
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://blerg-core/internal/live-data/approve", nil)
	response, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal("valid request refused")
	}
	req.Header.Set("Forwarded", "tls-log-canary")
	response, e = client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("forwarded identity accepted")
	}
	_ = input.Close()
	// Logs never contain request headers, including canary credentials.
	// Read after the child exits in the deferred cleanup to avoid concurrent access.
	t.Cleanup(func() {
		if strings.Contains(logs.String(), "tls-log-canary") {
			t.Error("credential leaked into TLS logs")
		}
	})
}

func TestCertificateLifetimeAndHostname(t *testing.T) {
	root := t.TempDir()
	if e := Provision(root); e != nil {
		t.Fatal(e)
	}
	core, e := Load(filepath.Join(root, "core"), "core")
	if e != nil {
		t.Fatal(e)
	}
	runner, e := Load(filepath.Join(root, "runner"), "runner")
	if e != nil {
		t.Fatal(e)
	}
	cert, e := x509.ParseCertificate(runner.certificate.Certificate[0])
	if e != nil {
		t.Fatal(e)
	}
	if _, e = cert.Verify(x509.VerifyOptions{Roots: runner.roots, CurrentTime: cert.NotAfter.Add(time.Second)}); e == nil {
		t.Fatal("expired certificate accepted")
	}
	// Sign an expired leaf with the real fixture CA: both bundle loading and
	// the shipped request verifier must refuse it, independently of TLS defaults.
	caPEM, e := os.ReadFile(filepath.Join(authorityPath(filepath.Join(root, "authority")), "ca.pem"))
	if e != nil {
		t.Fatal(e)
	}
	caBlock, _ := pem.Decode(caPEM)
	ca, e := x509.ParseCertificate(caBlock.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	keyPEM, e := os.ReadFile(filepath.Join(authorityPath(filepath.Join(root, "authority")), "key.pem"))
	if e != nil {
		t.Fatal(e)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	key, e := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	cert.NotBefore = time.Now().Add(-2 * time.Hour)
	cert.NotAfter = time.Now().Add(-time.Hour)
	expired, e := x509.CreateCertificate(rand.Reader, cert, ca, cert.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "runner", "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: expired}), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Load(filepath.Join(root, "runner"), "runner"); e == nil {
		t.Fatal("expired bundle accepted")
	}
	expiredLeaf, e := x509.ParseCertificate(expired)
	if e != nil {
		t.Fatal(e)
	}
	request := httptest.NewRequest("POST", "/internal/live-data/approve", nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{expiredLeaf}}
	if e = core.VerifyRequest(request); e == nil {
		t.Fatal("expired peer accepted")
	}
	listener, e := core.Listen(context.Background(), "127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), func() { t.Error("listener failed") })
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	client := runner.Client()
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, listener.(*http.Server).Addr)
	}
	res, e := client.Post("https://wrong-service.example/internal/live-data/approve", "application/json", nil)
	if e == nil {
		res.Body.Close()
		t.Fatal("wrong server hostname accepted")
	}
	// A fresh leaf is issued on each provisioning run, while the CA and subject stay stable.
	old := string(core.certificate.Certificate[0])
	if e = Provision(root); e != nil {
		t.Fatal(e)
	}
	fresh, e := Load(filepath.Join(root, "core"), "core")
	if e != nil {
		t.Fatal(e)
	}
	if string(fresh.certificate.Certificate[0]) == old || fresh.SelfURI != core.SelfURI {
		t.Fatal("renewal failed or changed identity")
	}
}

func TestRequestRequiresDedicatedRoot(t *testing.T) {
	root := t.TempDir()
	if e := Provision(root); e != nil {
		t.Fatal(e)
	}
	runner, e := Load(filepath.Join(root, "runner"), "runner")
	if e != nil {
		t.Fatal(e)
	}
	other := t.TempDir()
	if e := Provision(other); e != nil {
		t.Fatal(e)
	}
	foreign, e := Load(filepath.Join(other, "core"), "core")
	if e != nil {
		t.Fatal(e)
	}
	// Simulate a separate public listener accepting a different CA's matching URI.
	// A pre-existing VerifiedChains value is insufficient for dedicated authority.
	runner.PeerURI = foreign.SelfURI
	leaf, e := x509.ParseCertificate(foreign.certificate.Certificate[0])
	if e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("POST", "/internal/live-data/definition", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	if e := runner.VerifyRequest(req); e == nil {
		t.Fatal("foreign listener root accepted")
	}
}
