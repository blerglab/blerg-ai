package ownershell

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestVerifyGrantToleratesBoundedClockSkew(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	g := protocol.ShellGrant{Version: 1, Audience: "runtime", OwnerID: "owner", LoginID: "login", SessionID: "session", LaunchGeneration: "launch", ConnectionID: "connection", ShellGeneration: "shell", Runtime: "sandbox", PolicyRevision: 1, IssuedAt: 1000, ExpiresAt: 1030}
	signed := SignGrant(key, g)
	at := func(d time.Duration) time.Time { return time.Unix(1000, 0).Add(d) }
	cases := []struct {
		name string
		now  time.Time
		want error
	}{
		// The runner issued the grant at 1000 by its clock; these are the desktop's clock.
		{"desktop 1.5 s behind", at(-1500 * time.Millisecond), nil},
		{"desktop 1.5 s ahead", at(1500 * time.Millisecond), nil},
		{"desktop 5 s behind", at(-5 * time.Second), nil},
		{"desktop 6 s behind", at(-6 * time.Second), ErrGrantClock},
		{"desktop 10 s behind", at(-10 * time.Second), ErrGrantClock},
		{"just before expiry", at(29 * time.Second), nil},
		// Expiry is strict: the tolerance is applied once, to issued-at.
		{"at expiry", at(30 * time.Second), ErrGrantExpired},
		{"expired 2 s ago", at(32 * time.Second), ErrGrantExpired},
		{"long after expiry", at(time.Hour), ErrGrantExpired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := VerifyGrant(pub, signed, g, 1, c.now)
			if c.want == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) || !errors.Is(err, ErrDenied) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			if errors.Is(c.want, ErrGrantExpired) && errors.Is(err, ErrGrantClock) {
				t.Fatal("expiry reported as clock skew")
			}
		})
	}
}

// The worst case is a runtime clock GrantClockSkew behind the runner: the grant must
// still stop within ShellGrantSeconds+GrantClockSkew of issuer time after issuance, also
// through a RuntimeLease, never 30+5+5.
func TestOfflineLifetimeBoundWithSlowRuntimeClock(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	g := protocol.ShellGrant{Version: 1, Audience: "runtime", OwnerID: "owner", LoginID: "login", SessionID: "session", LaunchGeneration: "launch", ConnectionID: "connection", ShellGeneration: "shell", Runtime: "sandbox", PolicyRevision: 1, IssuedAt: 1000, ExpiresAt: 1030}
	p := protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}
	// issuer is the runner's (true) time since issuance; the runtime runs 5 s behind it.
	issuer := time.Unix(1000, 0)
	runtimeNow := func() time.Time { return issuer.Add(-time.Duration(GrantClockSkew) * time.Second) }
	killed := 0
	lease, err := NewRuntimeLease(pub, SignGrant(key, g), g, p, runtimeNow, func() error { killed++; return nil })
	if err != nil {
		t.Fatalf("grant from a runtime 5 s behind refused: %v", err)
	}
	maximum := time.Duration(protocol.ShellGrantSeconds+GrantClockSkew) * time.Second
	issuer = time.Unix(1000, 0).Add(maximum - time.Millisecond)
	if lease.Check() != nil || killed != 0 {
		t.Fatal("lease ended before the maximum")
	}
	issuer = time.Unix(1000, 0).Add(maximum)
	lease.Tick()
	if lease.Check() == nil || killed != 1 {
		t.Fatalf("lease outlived %v of issuer time", maximum)
	}
	// One second further behind and the grant is refused outright.
	issuer = time.Unix(1000, 0)
	slower := func() time.Time { return issuer.Add(-time.Duration(GrantClockSkew+1) * time.Second) }
	if _, err = NewRuntimeLease(pub, SignGrant(key, g), g, p, slower, func() error { return nil }); err == nil {
		t.Fatal("runtime 6 s behind accepted a grant")
	}
}

func TestVerifyGrantClockToleranceDoesNotWeakenOtherChecks(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	g := protocol.ShellGrant{Version: 1, Audience: "runtime", OwnerID: "owner", LoginID: "login", SessionID: "session", LaunchGeneration: "launch", ConnectionID: "connection", ShellGeneration: "shell", Runtime: "sandbox", PolicyRevision: 1, IssuedAt: 1000, ExpiresAt: 1030}
	now := time.Unix(1001, 0)
	timeError := func(err error) bool { return errors.Is(err, ErrGrantClock) || errors.Is(err, ErrGrantExpired) }
	long := g
	long.ExpiresAt = long.IssuedAt + protocol.ShellGrantSeconds + 1
	if err := VerifyGrant(pub, SignGrant(key, long), long, 1, now); err == nil || timeError(err) {
		t.Fatalf("lifetime bound weakened: %v", err)
	}
	other := g
	other.ConnectionID = "other"
	if err := VerifyGrant(pub, SignGrant(key, other), g, 1, now); err == nil || timeError(err) {
		t.Fatalf("binding check weakened: %v", err)
	}
	// A wrong binding outside the time window is still a binding failure.
	for _, at := range []int64{900, 5000} {
		if err := VerifyGrant(pub, SignGrant(key, other), g, 1, time.Unix(at, 0)); err == nil || timeError(err) {
			t.Fatalf("binding failure reported as a time problem: %v", err)
		}
	}
	forged := SignGrant(key, g)
	forged.Grant.ExpiresAt++
	if err := VerifyGrant(pub, forged, forged.Grant, 1, now); err == nil || timeError(err) {
		t.Fatalf("signature check weakened: %v", err)
	}
	if err := VerifyGrant(pub, SignGrant(key, g), g, 2, now); err == nil {
		t.Fatal("revision check weakened")
	}
}
