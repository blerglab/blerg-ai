package livetls

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOptionalTLSFollowsRenewedLeafWithoutAuthorityMount(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	if e := ProvisionAt(root, now.Add(-40*24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	identity, cause := Available(filepath.Join(root, "core"), "core", now)
	if identity == nil {
		t.Fatal(cause)
	}
	server := identity.ServerTLS()
	client := identity.Client().Transport.(*http.Transport).TLSClientConfig
	if server.GetCertificate == nil || client.GetClientCertificate == nil {
		t.Fatal("optional transport keeps a static leaf after renewal")
	}
	old := identity.certificate.Leaf.SerialNumber.String()
	if e := ProvisionAt(root, now); e != nil {
		t.Fatal(e)
	}
	next, e := server.GetCertificate(&tls.ClientHelloInfo{})
	if e != nil || next.Leaf.SerialNumber.String() == old {
		t.Fatal("server did not follow renewed leaf", e)
	}
	next, e = client.GetClientCertificate(&tls.CertificateRequestInfo{})
	if e != nil || next.Leaf.SerialNumber.String() == old {
		t.Fatal("client did not follow renewed leaf", e)
	}
	if warning := identity.Warning(now.Add(20 * 24 * time.Hour)); warning != "" {
		t.Fatal("warning uses replaced leaf", warning)
	}
	if e = ProvisionAt(root, now.Add(-61*24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	if _, e = identity.currentCertificate(now); e == nil {
		t.Fatal("renewal inside serving cutoff accepted")
	}
	if e = os.Remove(filepath.Join(root, "core", "key.pem")); e != nil {
		t.Fatal(e)
	}
	if _, e = server.GetCertificate(&tls.ClientHelloInfo{}); e == nil {
		t.Fatal("server fell back to old key")
	}
	if _, e = client.GetClientCertificate(&tls.CertificateRequestInfo{}); e == nil {
		t.Fatal("client fell back to old key")
	}
	if identity.Warning(now) == "" {
		t.Fatal("invalid renewal not visible in health")
	}
}

func TestOptionalTLSRenewalKeepsRealTransportRunning(t *testing.T) {
	root := t.TempDir()
	if e := Provision(root); e != nil {
		t.Fatal(e)
	}
	core, _ := Available(filepath.Join(root, "core"), "core", time.Now())
	runner, _ := Available(filepath.Join(root, "runner"), "runner", time.Now())
	if core == nil || runner == nil {
		t.Fatal("fixture identity")
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: core.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fixture-Serial", r.TLS.PeerCertificates[0].SerialNumber.String())
		w.WriteHeader(204)
	}))}
	done := make(chan error, 1)
	go func() { done <- server.Serve(tls.NewListener(listener, core.ServerTLS())) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	client := runner.Client()
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
	}
	invoke := func() (string, string) {
		t.Helper()
		response, err := client.Post("https://blerg-core/internal/live-data/proposals", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != 204 {
			t.Fatal(response.StatusCode)
		}
		return response.Header.Get("X-Fixture-Serial"), response.TLS.PeerCertificates[0].SerialNumber.String()
	}
	beforeClient, beforeServer := invoke()
	if e = Provision(root); e != nil {
		t.Fatal(e)
	}
	afterClient, afterServer := invoke()
	if beforeClient == afterClient || beforeServer == afterServer {
		t.Fatal("live transport kept old leaf after projected Secret renewal")
	}
}
func TestOptionalTLSRenewalRejectsAuthorityAndIdentitySubstitution(t *testing.T) {
	for _, kind := range []string{"authority", "identity"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if e := Provision(root); e != nil {
				t.Fatal(e)
			}
			identity, _ := Available(filepath.Join(root, "core"), "core", time.Now())
			if identity == nil {
				t.Fatal("fixture")
			}
			foreign := t.TempDir()
			if e := Provision(foreign); e != nil {
				t.Fatal(e)
			}
			if kind == "authority" {
				raw, e := os.ReadFile(filepath.Join(root, "authority", "bundle", "identity.json"))
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(foreign, "authority", "bundle", "identity.json"), raw, 0600); e != nil {
					t.Fatal(e)
				}
			} else {
				for _, name := range []string{"key.pem", "ca.pem"} {
					raw, e := os.ReadFile(filepath.Join(root, "authority", "bundle", name))
					if e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(filepath.Join(foreign, "authority", "bundle", name), raw, 0600); e != nil {
						t.Fatal(e)
					}
				}
			}
			if e := Provision(foreign); e != nil {
				t.Fatal(e)
			}
			for _, name := range []string{"key.pem", "ca.pem", "cert.pem", "identity.json"} {
				raw, e := os.ReadFile(filepath.Join(foreign, "core", name))
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(root, "core", name), raw, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := identity.ServerTLS().GetCertificate(&tls.ClientHelloInfo{}); e == nil {
				t.Fatal("replacement escaped original authority and scope", kind)
			}
		})
	}
}
