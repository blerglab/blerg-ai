// Package livetls owns the dedicated, installation-local live-data transport.
// It never accepts proxy identities or falls back to the public HTTP services.
package livetls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

var ErrIdentity = errors.New("live_data_tls_identity_invalid")

type Manifest struct {
	Installation string `json:"installation"`
	Runner       string `json:"runner"`
	Role         string `json:"role"`
}
type Identity struct {
	Installation string
	Runner       string
	PeerURI      string
	SelfURI      string
	certificate  tls.Certificate
	roots        *x509.CertPool
	role         string
	sourceDir    string
}

func uri(m Manifest, role string) string {
	s := "spiffe://blerg/installation/" + m.Installation + "/" + role
	if role == "runner" {
		s += "/" + m.Runner
	}
	return s
}

// Load refuses partial bundles, role substitution and invalid chains. Only the
// provisioning command ever receives the CA private key.
func Load(dir, role string) (*Identity, error) { return loadAt(dir, role, time.Now()) }
func loadAt(dir, role string, now time.Time) (*Identity, error) {
	if role != "core" && role != "runner" {
		return nil, ErrIdentity
	}
	raw, e := os.ReadFile(filepath.Join(dir, "identity.json")) // #nosec G304 G703 -- operator-provisioned private bundle, never request input.
	if e != nil {
		return nil, ErrIdentity
	}
	var m Manifest
	if json.Unmarshal(raw, &m) != nil || m.Role != role || !uuid(m.Installation) || !uuid(m.Runner) {
		return nil, ErrIdentity
	}
	info, e := os.Stat(filepath.Join(dir, "key.pem")) // #nosec G703 -- trusted operator directory and fixed filename.
	if e != nil || info.Mode().Perm()&0027 != 0 {
		return nil, ErrIdentity
	}
	cert, e := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if e != nil {
		return nil, ErrIdentity
	}
	ca, e := os.ReadFile(filepath.Join(dir, "ca.pem")) // #nosec G304 G703 -- trusted operator directory and fixed filename.
	if e != nil {
		return nil, ErrIdentity
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, ErrIdentity
	}
	leaf, e := x509.ParseCertificate(cert.Certificate[0])
	if e != nil || len(leaf.URIs) != 1 || leaf.URIs[0].String() != uri(m, role) {
		return nil, ErrIdentity
	}
	if _, e = leaf.Verify(x509.VerifyOptions{CurrentTime: now, Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}); e != nil {
		return nil, ErrIdentity
	}
	cert.Leaf = leaf
	peer := "runner"
	if role == "runner" {
		peer = "core"
	}
	return &Identity{Installation: m.Installation, Runner: m.Runner, PeerURI: uri(m, peer), SelfURI: uri(m, role), certificate: cert, roots: roots, role: role}, nil
}
func uuid(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func (i *Identity) verify(state tls.ConnectionState) error {
	if len(state.PeerCertificates) == 0 {
		return ErrIdentity
	}
	leaf := state.PeerCertificates[0]
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != i.PeerURI {
		return ErrIdentity
	}
	// Recheck against this dedicated root even on a different listener.
	if _, e := leaf.Verify(x509.VerifyOptions{Roots: i.roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); e != nil {
		return ErrIdentity
	}
	return nil
}
func (i *Identity) ServerTLS() *tls.Config {
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{i.certificate}, ClientCAs: i.roots, ClientAuth: tls.RequireAndVerifyClientCert, VerifyConnection: i.verify}
	if i.sourceDir != "" {
		config.Certificates = nil
		config.SessionTicketsDisabled = true
		config.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return i.currentCertificate(time.Now()) }
	}
	return config
}
func (i *Identity) Client() *http.Client {
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{i.certificate}, RootCAs: i.roots, VerifyConnection: i.verify}
	if i.sourceDir != "" {
		config.Certificates = nil
		config.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return i.currentCertificate(time.Now()) }
	}
	return &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{DisableKeepAlives: i.sourceDir != "", TLSClientConfig: config}}
}

// currentCertificate follows only a leaf renewal under the original identity and
// CA. Missing, partial or substituted projections never reuse the old key.
func (i *Identity) currentCertificate(now time.Time) (*tls.Certificate, error) {
	if i.sourceDir == "" {
		return &i.certificate, nil
	}
	next, e := loadAt(i.sourceDir, i.role, now)
	if e != nil || next.SelfURI != i.SelfURI || next.PeerURI != i.PeerURI || !next.roots.Equal(i.roots) || !now.Add(30*24*time.Hour).Before(next.certificate.Leaf.NotAfter) {
		return nil, ErrIdentity
	}
	return &next.certificate, nil
}

func (i *Identity) VerifyRequest(r *http.Request) error {
	if i.Warning(time.Now()) != "" {
		return ErrIdentity
	}
	if r.TLS == nil {
		return ErrIdentity
	}
	return i.verify(*r.TLS)
}
func (i *Identity) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if i.VerifyRequest(r) != nil {
			failurelog.Default.Event("live_data_tls_request_refused", slog.LevelWarn)
			w.Header().Set("X-Blerg-Live-Data-Cause", "service_identity_required")
			http.Error(w, "Unavailable.", http.StatusUnauthorized)
			return
		}
		for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP"} {
			if _, ok := r.Header[http.CanonicalHeaderKey(h)]; ok {
				failurelog.Default.Event("live_data_tls_request_refused", slog.LevelWarn)
				w.Header().Set("X-Blerg-Live-Data-Cause", "service_identity_required")
				http.Error(w, "Unavailable.", http.StatusUnauthorized)
				return
			}
		}
		allowed := false
		if i.role == "runner" {
			allowed = r.URL.Path == "/internal/live-data/definition" || r.URL.Path == "/internal/live-data/attempt" || r.URL.Path == "/internal/live-data/posture"
		} else {
			switch r.URL.Path {
			case "/internal/live-data/issuer/provider", "/internal/live-data/issuer/mint", "/internal/live-data/issuer/keys", "/internal/live-data/issuer/current", "/internal/live-data/issuer/invalidate", "/internal/live-data/proposals", "/internal/live-data/proposal", "/internal/live-data/approve", "/internal/live-data/policy", "/internal/live-data/receipt", "/internal/live-data/confirm", "/internal/live-data/cancel":
				allowed = true
			}
		}
		if !allowed {
			failurelog.Default.Event("live_data_tls_route_refused", slog.LevelWarn)
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
