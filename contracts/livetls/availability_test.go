package livetls

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOptionalTLSAvailabilityAndRenewal(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	if identity, cause := Available(filepath.Join(root, "core"), "core", now); identity != nil || cause == "" {
		t.Fatal("missing bundle opened listener")
	}
	if e := ProvisionAt(root, now.Add(-100*24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	if identity, cause := Available(filepath.Join(root, "core"), "core", now); identity != nil || cause != "bundle_expired" {
		t.Fatal("expired bundle available", cause)
	}
	// A fresh service-start leaf is generated without deploy.sh, retaining IDs.
	out := t.TempDir()
	if e := RenewAtStart(filepath.Join(root, "authority"), filepath.Join(root, "core"), out, "core"); e != nil {
		t.Fatal(e)
	}
	identity, cause := Available(out, "core", now)
	if identity == nil || cause != "" {
		t.Fatal("startup renewal failed", cause)
	}
	if identity.Warning(now.Add(61*24*time.Hour)) == "" {
		t.Fatal("30-day readiness warning absent")
	}
	if identity, cause := Available(out, "core", now.Add(61*24*time.Hour)); identity != nil || cause != "bundle_expiring" {
		t.Fatal("soon-expiring bundle accepted", cause)
	}
	empty := t.TempDir()
	if e := RenewAtStart(empty, empty, t.TempDir(), "runner"); e != nil {
		t.Fatal("optional missing authority blocked startup")
	}
}
