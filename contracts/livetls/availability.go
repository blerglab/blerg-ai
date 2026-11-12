package livetls

import (
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

// Available refuses this optional feature, never the host process. All warning
// labels are fixed and safe for readiness headers. Expiry warnings start 30 days
// before expiry; renewal is required before this listener will start again.
func Available(dir, role string, now time.Time) (*Identity, string) {
	identity, e := loadAt(dir, role, now)
	cause := ""
	if e != nil {
		cause = "bundle_missing_or_invalid"
		cert, err := tls.LoadX509KeyPair(dir+"/cert.pem", dir+"/key.pem")
		if err == nil {
			leaf, err := x509.ParseCertificate(cert.Certificate[0])
			if err == nil && !now.Before(leaf.NotAfter) {
				if _, err = loadAt(dir, role, leaf.NotAfter.Add(-time.Second)); err == nil {
					cause = "bundle_expired"
				}
			}
		}
	} else {
		leaf, err := x509.ParseCertificate(identity.certificate.Certificate[0])
		if err != nil {
			cause = "bundle_invalid"
		} else if !now.Add(30 * 24 * time.Hour).Before(leaf.NotAfter) {
			cause = "bundle_expiring"
		}
	}
	if cause != "" {
		failurelog.Default.Event("live_data_tls:"+cause, slog.LevelWarn, "cause", cause)
		return nil, cause
	}
	identity.sourceDir = dir
	return identity, ""
}

// Warning is evaluated by readiness probes and request admission, so a running
// service also reports the 30-day renewal window without depending on a restart.
func (i *Identity) Warning(now time.Time) string {
	certificate, e := i.currentCertificate(now)
	if e != nil || certificate.Leaf == nil || !now.Add(30*24*time.Hour).Before(certificate.Leaf.NotAfter) {
		failurelog.Default.Event("live_data_tls:bundle_expiring", slog.LevelWarn)
		return "live_data_tls_expiring"
	}
	return ""
}
