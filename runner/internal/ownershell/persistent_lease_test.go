package ownershell

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type persistentLeaseFixture struct {
	lease   *PersistentLease
	key     ed25519.PrivateKey
	grant   protocol.ShellGrant
	wall    time.Time
	elapsed time.Duration
	stops   int
	cleanup error
}

func newPersistentLeaseFixture(t *testing.T) *persistentLeaseFixture {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &persistentLeaseFixture{key: key, wall: time.Unix(1000, 0)}
	f.grant = protocol.ShellGrant{Version: 2, Audience: "runtime", OwnerID: "owner", LoginID: "login", SessionID: "session", LaunchGeneration: "launch", ConnectionID: "connection", ShellGeneration: "shell", Runtime: "host", ConsentID: "consent", PolicyRevision: 1, IssuedAt: 1000, ExpiresAt: 1030, CustodyID: "custody", AccessEpoch: 1}
	f.lease, err = NewPersistentLease(pub, SignGrant(key, f.grant), f.grant, protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}, time.Unix(1000, 0).Add(7*24*time.Hour), func() (time.Time, time.Duration, error) { return f.wall, f.elapsed, nil }, func() error { f.stops++; return f.cleanup })
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *persistentLeaseFixture) advance(d time.Duration) { f.wall = f.wall.Add(d); f.elapsed += d }
func (f *persistentLeaseFixture) next() SignedGrant {
	g := f.grant
	g.AccessEpoch++
	g.ConnectionID = "new-connection"
	g.Audience = "new-audience"
	g.IssuedAt = f.wall.Unix()
	g.ExpiresAt = g.IssuedAt + 30
	return SignGrant(f.key, g)
}

func TestPersistentLeaseLapseSuspendsWithoutKilling(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	f.advance(12 * time.Hour)
	f.lease.Tick()
	if f.stops != 0 || f.lease.Check() == nil {
		t.Fatal("sleep must suspend access without killing process")
	}
	// Neither wall-clock rollback nor a valid same-epoch renewal restores access.
	f.wall = time.Unix(1001, 0)
	if f.lease.Check() == nil {
		t.Fatal("clock rollback restored lapsed access")
	}
	g := f.grant
	g.IssuedAt = 1001
	g.ExpiresAt = 1031
	if f.lease.Renew(SignGrant(f.key, g)) == nil {
		t.Fatal("renewal resurrected lapsed epoch")
	}
}

func TestPersistentLeaseAdoptionNeedsPrepareAndCommit(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	f.advance(time.Hour)
	g := f.next()
	if err := f.lease.Prepare("transaction", f.key.Public().(ed25519.PublicKey), g); err != nil {
		t.Fatal(err)
	}
	if f.lease.Check() == nil {
		t.Fatal("prepare opened access")
	}
	if err := f.lease.Prepare("transaction", f.key.Public().(ed25519.PublicKey), g); err != nil {
		t.Fatal("identical prepare must be idempotent", err)
	}
	if f.lease.Commit("wrong") == nil {
		t.Fatal("wrong transaction committed")
	}
	if err := f.lease.Commit("transaction"); err != nil {
		t.Fatal(err)
	}
	if f.lease.Check() != nil || f.stops != 0 {
		t.Fatal("committed adoption did not restore access")
	}
	if err := f.lease.Commit("transaction"); err != nil {
		t.Fatal("lost acknowledgment retry", err)
	}
	if f.lease.Renew(SignGrant(f.key, f.grant)) == nil {
		t.Fatal("old epoch restored")
	}
}

func TestPersistentLeaseTerminalEventsCannotAdopt(t *testing.T) {
	for _, event := range []string{"close", "policy", "lifetime"} {
		t.Run(event, func(t *testing.T) {
			f := newPersistentLeaseFixture(t)
			f.cleanup = errors.New("cleanup pending")
			switch event {
			case "close":
				_ = f.lease.Close()
			case "policy":
				f.lease.Policy(protocol.ShellPolicy{Version: 1, Revision: 2, Enabled: true})
			case "lifetime":
				f.advance(7 * 24 * time.Hour)
				f.lease.Tick()
			}
			if f.stops != 1 || f.lease.Check() == nil {
				t.Fatal("terminal event failed to close")
			}
			if f.lease.Prepare("transaction", f.key.Public().(ed25519.PublicKey), f.next()) == nil {
				t.Fatal("terminal generation adopted")
			}
			if !errors.Is(f.lease.Close(), f.cleanup) || f.stops != 1 {
				t.Fatal("lost cleanup failure or repeated cleanup")
			}
		})
	}
}

func TestPersistentLeaseAdoptionPinsImmutableCustody(t *testing.T) {
	for _, field := range []string{"OwnerID", "LoginID", "SessionID", "LaunchGeneration", "ShellGeneration", "Runtime", "ConsentID", "CustodyID", "PolicyRevision"} {
		t.Run(field, func(t *testing.T) {
			f := newPersistentLeaseFixture(t)
			f.advance(time.Hour)
			g := f.next().Grant
			switch field {
			case "OwnerID":
				g.OwnerID = "other"
			case "LoginID":
				g.LoginID = "other"
			case "SessionID":
				g.SessionID = "other"
			case "LaunchGeneration":
				g.LaunchGeneration = "other"
			case "ShellGeneration":
				g.ShellGeneration = "other"
			case "Runtime":
				g.Runtime = "sandbox"
				g.ConsentID = "consent"
			case "ConsentID":
				g.ConsentID = "other"
			case "CustodyID":
				g.CustodyID = "other"
			case "PolicyRevision":
				g.PolicyRevision++
			}
			if f.lease.Prepare("transaction", f.key.Public().(ed25519.PublicKey), SignGrant(f.key, g)) == nil {
				t.Fatal("changed custody adopted")
			}
			if f.stops != 0 {
				t.Fatal("invalid frame killed process")
			}
		})
	}
}

func TestPersistentLeaseCloseWinsPreparedAdoption(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	f.advance(time.Hour)
	if err := f.lease.Prepare("transaction", f.key.Public().(ed25519.PublicKey), f.next()); err != nil {
		t.Fatal(err)
	}
	_ = f.lease.Close()
	if f.lease.Commit("transaction") == nil || f.stops != 1 {
		t.Fatal("prepared adoption bypassed close")
	}
}

func TestPersistentLeaseBootTimeBoundsBackwardWallClock(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	f.elapsed = 7 * 24 * time.Hour
	f.wall = time.Unix(900, 0)
	f.lease.Tick()
	if f.stops != 1 {
		t.Fatal("wall rollback extended finite process lifetime")
	}
}

func TestPersistentLeaseRenewalDoesNotExtendProcessLifetime(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	f.advance(7*24*time.Hour - time.Second)
	if err := f.lease.Prepare("transaction", f.key.Public().(ed25519.PublicKey), f.next()); err != nil {
		t.Fatal(err)
	}
	if err := f.lease.Commit("transaction"); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Second)
	f.lease.Tick()
	if f.stops != 1 {
		t.Fatal("adoption extended lifetime")
	}
}

func TestPersistentLeaseRenewalAndKeyRotation(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	f.advance(10 * time.Second)
	g := f.grant
	g.IssuedAt = f.wall.Unix()
	g.ExpiresAt = g.IssuedAt + 30
	if err := f.lease.Renew(SignGrant(f.key, g)); err != nil {
		t.Fatal(err)
	}
	f.advance(25 * time.Second)
	if f.lease.Check() != nil {
		t.Fatal("valid renewal lost")
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	next := f.next().Grant
	if err = f.lease.Prepare("new-key", pub, SignGrant(key, next)); err != nil {
		t.Fatal(err)
	}
	if err = f.lease.Commit("new-key"); err != nil {
		t.Fatal(err)
	}
	next.IssuedAt++
	next.ExpiresAt++
	if f.lease.Renew(SignGrant(f.key, next)) == nil {
		t.Fatal("old key renewed new epoch")
	}
	if err = f.lease.Renew(SignGrant(key, next)); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentLeaseUnknownClockClosesWithinLifetimeFence(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	f.lease.clock = func() (time.Time, time.Duration, error) { return time.Time{}, 0, errors.New("clock unavailable") }
	f.lease.Tick()
	if f.stops != 1 || f.lease.Check() == nil {
		t.Fatal("unverifiable lifetime clock left writer unbounded")
	}
}

func TestLegacyLeaseCannotSelectPersistentSemantics(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	if _, err := NewRuntimeLease(f.key.Public().(ed25519.PublicKey), SignGrant(f.key, f.grant), f.grant, protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}, func() time.Time { return f.wall }, func() error { return nil }); err == nil {
		t.Fatal("legacy lease accepted v2")
	}
	g := f.grant
	g.Version = 1
	if VerifyGrant(f.key.Public().(ed25519.PublicKey), SignGrant(f.key, g), g, 1, f.wall) == nil {
		t.Fatal("v1 envelope accepted persistence fields")
	}
	g = f.grant
	g.Runtime = "cluster"
	if VerifyGrant(f.key.Public().(ed25519.PublicKey), SignGrant(f.key, g), g, 1, f.wall) == nil {
		t.Fatal("cluster implicitly selected persistence")
	}
}

func TestPersistentAccessGuardChecksTheFinalOperation(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	called := 0
	write := func() error { called++; return nil }
	if err := f.lease.Access(SignGrant(f.key, f.grant), write); err != nil || called != 1 {
		t.Fatal("valid final syscall refused", err)
	}
	f.lease.Suspend()
	if f.lease.Access(SignGrant(f.key, f.grant), write) == nil || called != 1 {
		t.Fatal("suspended syscall committed")
	}
	g := f.next()
	if err := f.lease.Prepare("transaction", f.key.Public().(ed25519.PublicKey), g); err != nil {
		t.Fatal(err)
	}
	if err := f.lease.Commit("transaction"); err != nil {
		t.Fatal(err)
	}
	if f.lease.Access(SignGrant(f.key, f.grant), write) == nil || called != 1 {
		t.Fatal("old-epoch syscall committed")
	}
	if err := f.lease.CheckEpoch(g.Grant.AccessEpoch); err != nil {
		t.Fatal(err)
	}
	if f.lease.CheckEpoch(f.grant.AccessEpoch) == nil {
		t.Fatal("old-epoch output committed")
	}
	if err := f.lease.Access(g, write); err != nil || called != 2 {
		t.Fatal("new-epoch syscall refused", err)
	}
	_ = f.lease.Close()
	if !f.lease.Closed() || f.lease.Access(g, write) == nil || called != 2 {
		t.Fatal("closed syscall committed")
	}
}

func TestPersistentLeaseSyncDeadlineStartsAlreadySuspended(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	f.advance(time.Hour)
	f.lease.Tick()
	until := f.wall.Add(2 * time.Minute)
	if err := f.lease.FreezeUntil(until); err != nil {
		t.Fatal("sync cannot start during access lapse", err)
	}
	f.advance(time.Minute)
	f.lease.Tick()
	if f.stops != 0 {
		t.Fatal("access lapse replaced independent sync deadline")
	}
	// A repeated phase message cannot slide the original sync watchdog.
	if err := f.lease.FreezeUntil(f.wall.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	f.wall = f.wall.Add(-time.Hour)
	f.lease.Tick()
	if f.stops != 1 {
		t.Fatal("sync watchdog ignored elapsed suspend time")
	}
	if f.lease.Thawed() == nil {
		t.Fatal("thaw revived expired sync")
	}
}

func TestPersistentLeaseThawNeedsFreshAdoption(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	if err := f.lease.FreezeUntil(f.wall.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	g := f.next()
	if err := f.lease.Prepare("sync-adopt", f.key.Public().(ed25519.PublicKey), g); err != nil {
		t.Fatal(err)
	}
	if f.lease.Commit("sync-adopt") == nil {
		t.Fatal("access opened during physical sync")
	}
	if err := f.lease.Thawed(); err != nil {
		t.Fatal(err)
	}
	if f.lease.Check() == nil {
		t.Fatal("thaw restored old grant")
	}
	if err := f.lease.Commit("sync-adopt"); err != nil {
		t.Fatal(err)
	}
	if f.lease.Check() != nil {
		t.Fatal("fresh adoption after thaw refused")
	}
}

func TestPersistentLeaseRejectsEpochJumpWithoutPoisoningRekey(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	next := f.next()
	next.Grant.AccessEpoch = ^uint64(0) - 1
	if f.lease.Prepare("attacker", f.key.Public().(ed25519.PublicKey), SignGrant(f.key, next.Grant)) == nil {
		t.Fatal("unbounded epoch accepted")
	}
	if err := f.lease.Prepare("legitimate", f.key.Public().(ed25519.PublicKey), f.next()); err != nil {
		t.Fatal("epoch jump poisoned legitimate rekey", err)
	}
}

func TestPersistentLeaseEnforcesDesktopLifetimeLocally(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	g := f.grant
	g.Runtime = "host"
	g.ConsentID = "consent"
	_, err := NewPersistentLease(f.key.Public().(ed25519.PublicKey), SignGrant(f.key, g), g, protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}, f.wall.Add(protocol.DesktopShellLifetime+protocol.DesktopShellClockSkew+time.Second), func() (time.Time, time.Duration, error) { return f.wall, 0, nil }, func() error { return nil })
	if err == nil {
		t.Fatal("helper lease accepted desktop lifetime beyond seven days plus clock margin")
	}
}

func TestPersistentLeaseRejectsSandboxBootstrap(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	g := f.grant
	g.Runtime = "sandbox"
	if _, err := NewPersistentLease(f.key.Public().(ed25519.PublicKey), SignGrant(f.key, g), g, protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}, f.wall.Add(time.Hour), func() (time.Time, time.Duration, error) { return f.wall, 0, nil }, func() error { return nil }); err == nil {
		t.Fatal("sandbox v2 lease accepted")
	}
}

func TestPersistentLeaseAcceptsSmallDesktopClockSkew(t *testing.T) {
	f := newPersistentLeaseFixture(t)
	wall := f.wall.Add(-2 * time.Second)
	if _, err := NewPersistentLease(f.key.Public().(ed25519.PublicKey), SignGrant(f.key, f.grant), f.grant, protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}, f.wall.Add(protocol.DesktopShellLifetime), func() (time.Time, time.Duration, error) { return wall, 0, nil }, func() error { return nil }); err != nil {
		t.Fatal("small clock skew rejected seven-day allocation", err)
	}
}
