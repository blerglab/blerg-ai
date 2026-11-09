package daemon

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var cloneProxyKeys = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"}
var errCloneEnvironment = errors.New("could not prepare sandbox clone certificate configuration")

// Only public, valid CA certificates cross into a private clone container.
// Trust files can contain keys or unrelated data; never mount their host paths.
func cloneCertificates(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, errCloneEnvironment
	}
	file, err := os.Open(path) // #nosec G304 -- daemon operator's CA environment selects this input; only parsed public certificates leave the host.
	if err != nil {
		return nil, errCloneEnvironment
	}
	defer func() { _ = file.Close() }()
	// Bound the read independently of a file growing after Stat.
	raw, err := io.ReadAll(io.LimitReader(file, 16<<20+1))
	if err != nil || len(raw) > 16<<20 {
		return nil, errCloneEnvironment
	}
	var out bytes.Buffer
	for len(raw) > 0 {
		block, rest := pem.Decode(raw)
		if block == nil {
			break
		}
		raw = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			continue
		}
		_ = pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})
	}
	if out.Len() == 0 {
		return nil, errCloneEnvironment
	}
	return out.Bytes(), nil
}

func sandboxCloneEnvironment(tmp string) ([]string, error) {
	var args []string
	for _, key := range cloneProxyKeys {
		if _, ok := os.LookupEnv(key); ok {
			args = append(args, "-e", key)
		} // Docker reads the value from its own environment; no secret in argv.
	}
	for i, key := range []string{"SSL_CERT_FILE", "GIT_SSL_CAINFO", "SSL_CERT_DIR", "GIT_SSL_CAPATH"} {
		value, ok := os.LookupEnv(key)
		if !ok || value == "" {
			continue
		}
		dir := filepath.Join(tmp, "trust-"+strconv.Itoa(i))
		if err := os.Mkdir(dir, 0700); err != nil { // #nosec G703 -- tmp is the daemon's MkdirTemp clone directory; suffix is a fixed trust index.
			return nil, errCloneEnvironment
		}
		container := "/clone/" + filepath.Base(dir)
		if strings.HasSuffix(key, "DIR") || strings.HasSuffix(key, "CAPATH") {
			entries, err := os.ReadDir(value)
			if err != nil || len(entries) > 1024 {
				return nil, errCloneEnvironment
			}
			copied := 0
			var size int64
			for _, entry := range entries {
				info, err := os.Stat(filepath.Join(value, entry.Name()))
				if err != nil || !info.Mode().IsRegular() {
					continue
				}
				size += info.Size()
				if size > 16<<20 {
					return nil, errCloneEnvironment
				}
				certs, err := cloneCertificates(filepath.Join(value, entry.Name()))
				if err != nil {
					continue
				}
				if err := os.WriteFile(filepath.Join(dir, entry.Name()), certs, 0600); err != nil { // #nosec G703 -- ReadDir supplies a single basename, copied into a fresh daemon-owned trust directory.
					return nil, errCloneEnvironment
				}
				copied++
			}
			if copied == 0 {
				return nil, errCloneEnvironment
			}
		} else {
			certs, err := cloneCertificates(value)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(dir, "ca.pem"), certs, 0600); err != nil { // #nosec G703 -- fixed filename beneath the fresh daemon-owned trust directory.
				return nil, errCloneEnvironment
			}
			container += "/ca.pem"
		}
		args = append(args, "-e", key+"="+container)
	}
	return args, nil
}
