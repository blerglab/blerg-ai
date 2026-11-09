package livetls

import (
	"crypto/tls"
	"errors"
	"net"
	"strings"
)

// Refused classifies both local verification errors and the TLS alert returned
// when the peer rejects our client certificate. Raw error text is never logged.
func Refused(err error) bool {
	if errors.Is(err, ErrIdentity) {
		return true
	}
	var verification *tls.CertificateVerificationError
	if errors.As(err, &verification) {
		return true
	}
	var alert tls.AlertError
	if errors.As(err, &alert) {
		return true
	}
	var record tls.RecordHeaderError
	if errors.As(err, &record) {
		return true
	}
	var remote *net.OpError
	return errors.As(err, &remote) && remote.Op == "remote error" && remote.Err != nil && strings.HasPrefix(remote.Err.Error(), "tls:")
}
