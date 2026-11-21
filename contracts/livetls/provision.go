package livetls

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Provision preserves the authority and installation identity across upgrades.
// The root directory must be private to the operator (never an agent mount).
// Only core/ and runner/ are mounted in the respective runtime containers.
func Provision(root string) error { return ProvisionAt(root, time.Now()) }

// ProvisionAt accepts an explicit authority clock for deterministic expiry proofs.
func ProvisionAt(root string, now time.Time) error {
	if e := os.MkdirAll(filepath.Join(root, "authority"), 0700); e != nil {
		return ErrIdentity
	}
	authorityRoot := filepath.Join(root, "authority")
	authority := authorityPath(authorityRoot)
	var m Manifest
	var ca *x509.Certificate
	var key ed25519.PrivateKey
	raw, e := os.ReadFile(filepath.Join(authority, "identity.json")) // #nosec G304 -- offline operator directory, never request input.
	if os.IsNotExist(e) {
		// Refuse an incomplete previous authority instead of silently replacing it.
		entries, err := os.ReadDir(authorityRoot)
		if err != nil {
			return ErrIdentity
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".provision-") {
				return ErrIdentity
			}
		}
		authority, err = os.MkdirTemp(authorityRoot, ".provision-")
		if err != nil {
			return ErrIdentity
		}
		defer func() { _ = os.RemoveAll(authority) }()
		m = Manifest{Installation: newUUID(), Runner: newUUID()}
		_, key, e = ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return ErrIdentity
		}
		template := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "Blerg live-data CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, MaxPathLenZero: true}
		der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
		if err != nil {
			return ErrIdentity
		}
		ca, err = x509.ParseCertificate(der)
		if err != nil {
			return ErrIdentity
		}
		encoded, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return ErrIdentity
		}
		if write(authority, "key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})) != nil || write(authority, "ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) != nil {
			return ErrIdentity
		}
		raw, err = json.Marshal(m)
		if err != nil || write(authority, "identity.json", raw) != nil {
			return ErrIdentity
		}
		if os.Rename(authority, filepath.Join(authorityRoot, "bundle")) != nil {
			return ErrIdentity
		}
	} else {
		if e != nil || json.Unmarshal(raw, &m) != nil || !uuid(m.Installation) || !uuid(m.Runner) {
			return ErrIdentity
		}
		raw, e = os.ReadFile(filepath.Join(authority, "key.pem")) // #nosec G304 -- offline operator directory, never request input.
		if e != nil {
			return ErrIdentity
		}
		block, _ := pem.Decode(raw)
		if block == nil {
			return ErrIdentity
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return ErrIdentity
		}
		var ok bool
		key, ok = parsed.(ed25519.PrivateKey)
		if !ok {
			return ErrIdentity
		}
		raw, e = os.ReadFile(filepath.Join(authority, "ca.pem")) // #nosec G304 -- offline operator directory, never request input.
		if e != nil {
			return ErrIdentity
		}
		block, _ = pem.Decode(raw)
		if block == nil {
			return ErrIdentity
		}
		ca, e = x509.ParseCertificate(block.Bytes)
		pub, ok := caPublicKey(ca)
		if e != nil || !ok || !ca.IsCA || !pub.Equal(key.Public()) || now.AddDate(0, 0, 90).After(ca.NotAfter) {
			return ErrIdentity
		}
	}
	for _, role := range []string{"core", "runner"} {
		dir := filepath.Join(root, role)
		if os.MkdirAll(dir, 0700) != nil {
			return ErrIdentity
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return ErrIdentity
		}
		identity, err := url.Parse(uri(m, role))
		if err != nil {
			return ErrIdentity
		}
		template := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "Blerg live-data " + role}, URIs: []*url.URL{identity}, DNSNames: []string{"blerg-" + role, "blerg-" + role + "-live-data"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(0, 0, 90), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, pub, key)
		if err != nil {
			return ErrIdentity
		}
		encoded, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return ErrIdentity
		}
		m.Role = role
		manifest, err := json.Marshal(m)
		if err != nil {
			return ErrIdentity
		}
		for name, data := range map[string][]byte{"ca.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), "cert.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "key.pem": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), "identity.json": manifest} {
			if write(dir, name, data) != nil {
				return ErrIdentity
			}
		}
	}
	return nil
}
func write(dir, name string, data []byte) error {
	f, e := os.CreateTemp(dir, ".identity-")
	if e != nil {
		return e
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(dir, name))
}
func serial() *big.Int {
	n, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		panic("live_data_entropy_unavailable")
	}
	return n
}
func newUUID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("live_data_entropy_unavailable")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func caPublicKey(c *x509.Certificate) (ed25519.PublicKey, bool) {
	if c == nil {
		return nil, false
	}
	k, ok := c.PublicKey.(ed25519.PublicKey)
	return k, ok
}

// authorityPath supports existing flat authorities and atomically committed new ones.
func authorityPath(root string) string {
	if info, err := os.Stat(filepath.Join(root, "bundle")); err == nil && info.IsDir() {
		return filepath.Join(root, "bundle")
	}
	return root
}
