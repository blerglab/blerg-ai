package ownershell

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"testing"
	"time"
)

func TestRuntimeLeaseOfflineExpiryAndNoResurrection(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	g := protocol.ShellGrant{Version: 1, Audience: "runtime", OwnerID: "owner", LoginID: "login", SessionID: "session", LaunchGeneration: "launch", ConnectionID: "connection", ShellGeneration: "shell", Runtime: "sandbox", PolicyRevision: 1, IssuedAt: 1000, ExpiresAt: 1030}
	p := protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}
	killed := 0
	lease, err := NewRuntimeLease(pub, SignGrant(key, g), g, p, func() time.Time { return now }, func() error { killed++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	now = time.Unix(1010, 0)
	renew := g
	renew.IssuedAt = 1010
	renew.ExpiresAt = 1040
	if lease.Renew(SignGrant(key, renew)) != nil {
		t.Fatal("renewal")
	}
	now = time.Unix(1040, 0)
	lease.Tick()
	if killed != 1 || lease.Check() == nil {
		t.Fatal("offline expiry did not kill")
	}
	renew.IssuedAt = 1040
	renew.ExpiresAt = 1070
	if lease.Renew(SignGrant(key, renew)) == nil {
		t.Fatal("expired shell resurrected")
	}
	_ = lease.Close()
	if killed != 1 {
		t.Fatal("cleanup repeated")
	}
}
func TestRuntimeLeaseDisableAcknowledgmentPreservesCleanupFailure(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	g := protocol.ShellGrant{Version: 1, Audience: "runtime", OwnerID: "owner", LoginID: "login", SessionID: "session", LaunchGeneration: "launch", ConnectionID: "connection", ShellGeneration: "shell", Runtime: "sandbox", PolicyRevision: 1, IssuedAt: 1000, ExpiresAt: 1030}
	p := protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}
	failure := errors.New("containment not empty")
	lease, err := NewRuntimeLease(pub, SignGrant(key, g), g, p, func() time.Time { return time.Unix(1001, 0) }, func() error { return failure })
	if err != nil {
		t.Fatal(err)
	}
	lease.Policy(protocol.ShellPolicy{Version: 1, Revision: 2, Enabled: false})
	if lease.Check() == nil || !errors.Is(lease.Close(), failure) {
		t.Fatal("failed cleanup acknowledged")
	}
	lease.Policy(protocol.ShellPolicy{Version: 1, Revision: 3, Enabled: true})
	if lease.Check() == nil {
		t.Fatal("re-enable resurrected lease")
	}
}
