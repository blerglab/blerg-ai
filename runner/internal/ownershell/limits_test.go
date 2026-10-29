package ownershell

import (
	"testing"
	"time"
)

// advance moves the fake clock in renewal-sized steps, ticking each time, as the server's
// one-second ticker would (the grant lease must keep renewing for the shell to live).
func (f *fixture) advance(t *testing.T, d time.Duration) {
	t.Helper()
	for end := f.now.Add(d); f.now.Before(end); {
		f.now = f.now.Add(10 * time.Second)
		f.m.Tick(t.Context())
	}
}

func (f *fixture) useRuntime(runtime string) {
	f.r.binding.Runtime = runtime
	if runtime == "host" {
		f.r.binding.HostConsentValid = true
		f.r.binding.Consent = "consent"
	}
}

func (f *fixture) alive(handle string) bool {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	return f.m.shells[handle] != nil
}

func TestDesktopShellSurvivesSixHoursDetachedAndIdle(t *testing.T) {
	for _, runtime := range []string{"sandbox", "host"} {
		t.Run(runtime, func(t *testing.T) {
			f := setup(t)
			f.useRuntime(runtime)
			i, a := f.open(t)
			// Six hours with no typing while a browser stays attached.
			for end := f.now.Add(6 * time.Hour); f.now.Before(end); {
				f.now = f.now.Add(10 * time.Second)
				if err := f.m.Heartbeat(t.Context(), f.actor, i.Handle, a.ID); err != nil {
					t.Fatalf("heartbeat after %s idle: %v", f.now.Sub(time.Unix(1000, 0)), err)
				}
				f.m.Tick(t.Context())
			}
			if !f.alive(i.Handle) {
				t.Fatal("a desktop shell ended after six idle hours")
			}
			// Six more hours with no browser attached at all.
			f.m.Detach(f.actor, i.Handle, a.ID)
			f.advance(t, 6*time.Hour)
			if !f.alive(i.Handle) || f.s.closed != 0 {
				t.Fatal("a desktop shell ended after six detached hours")
			}
			if _, err := f.m.Attach(t.Context(), f.actor, i.Handle, false); err != nil {
				t.Fatalf("reattach after six hours: %v", err)
			}
		})
	}
}

func TestClusterShellKeepsFiveAndThirtyMinuteLimits(t *testing.T) {
	f := setup(t)
	f.useRuntime("cluster")
	i, a := f.open(t)
	f.m.Detach(f.actor, i.Handle, a.ID)
	f.advance(t, ReconnectGrace-10*time.Second)
	if !f.alive(i.Handle) {
		t.Fatal("a cluster shell ended before the reconnect grace")
	}
	f.advance(t, 10*time.Second)
	if f.alive(i.Handle) {
		t.Fatal("a detached cluster shell outlived 5 minutes")
	}
	if r, after, ok := f.m.Ended(f.actor, i.Handle); !ok || r != ReasonDetached || after != ReconnectGrace {
		t.Fatalf("ended = %q %s %v, want detached after 5m", r, after, ok)
	}

	f = setup(t)
	f.useRuntime("cluster")
	i, a = f.open(t)
	for end := f.now.Add(IdleTimeout - 10*time.Second); f.now.Before(end); {
		f.now = f.now.Add(10 * time.Second)
		_ = f.m.Heartbeat(t.Context(), f.actor, i.Handle, a.ID)
		f.m.Tick(t.Context())
	}
	if !f.alive(i.Handle) {
		t.Fatal("a cluster shell ended before the idle timeout")
	}
	f.now = f.now.Add(10 * time.Second)
	_ = f.m.Heartbeat(t.Context(), f.actor, i.Handle, a.ID)
	f.m.Tick(t.Context())
	if f.alive(i.Handle) {
		t.Fatal("an idle cluster shell outlived 30 minutes")
	}
	if r, after, ok := f.m.Ended(f.actor, i.Handle); !ok || r != ReasonIdle || after != IdleTimeout {
		t.Fatalf("ended = %q %s %v, want idle after 30m", r, after, ok)
	}
	if _, _, ok := f.m.Ended(Actor{"human", "owner", "other-login"}, i.Handle); ok {
		t.Fatal("another login read the ended cause")
	}
	if _, _, ok := f.m.Ended(Actor{"human", "intruder", "login"}, i.Handle); ok {
		t.Fatal("another account read the ended cause")
	}
}

func TestShortenedLimitAppliesToRunningShellsWithTenMinuteFloor(t *testing.T) {
	f := setup(t)
	i, a := f.open(t)
	f.m.Detach(f.actor, i.Handle, a.ID)
	f.advance(t, 2*time.Hour)
	if !f.alive(i.Handle) {
		t.Fatal("desktop default ended a detached shell")
	}
	// The admin now limits desktop shells to 10 minutes detached; the shell has been
	// detached for two hours, but keeps 10 minutes from the change.
	l := f.m.Limits()
	l.Desktop.Detached = MinLimit
	f.m.SetLimits(l)
	f.advance(t, MinLimit-10*time.Second)
	if !f.alive(i.Handle) {
		t.Fatal("a shortened limit ended a running shell before the 10-minute floor")
	}
	f.advance(t, 10*time.Second)
	if f.alive(i.Handle) {
		t.Fatal("a shortened limit never applied to the running shell")
	}
	if r, after, ok := f.m.Ended(f.actor, i.Handle); !ok || r != ReasonDetached || after != MinLimit {
		t.Fatalf("ended = %q %s %v", r, after, ok)
	}

	// A lengthened limit applies at once: a cluster shell detached for 4 minutes survives
	// past 5 once the limit is an hour, and ends at the hour.
	f = setup(t)
	f.useRuntime("cluster")
	i, a = f.open(t)
	f.m.Detach(f.actor, i.Handle, a.ID)
	f.advance(t, 4*time.Minute)
	l = f.m.Limits()
	l.Cluster.Detached = time.Hour
	l.Cluster.Idle = 2 * time.Hour
	f.m.SetLimits(l)
	f.advance(t, 50*time.Minute)
	if !f.alive(i.Handle) {
		t.Fatal("a lengthened limit did not apply to the running shell")
	}
	f.advance(t, 6*time.Minute+10*time.Second)
	if f.alive(i.Handle) {
		t.Fatal("the lengthened limit never ended the shell")
	}

	// An idle limit set on desktop shells applies to a shell idle for hours, after the floor.
	f = setup(t)
	i, a = f.open(t)
	for end := f.now.Add(3 * time.Hour); f.now.Before(end); {
		f.now = f.now.Add(10 * time.Second)
		_ = f.m.Heartbeat(t.Context(), f.actor, i.Handle, a.ID)
		f.m.Tick(t.Context())
	}
	l = f.m.Limits()
	l.Desktop.Idle = time.Hour
	f.m.SetLimits(l)
	for end := f.now.Add(MinLimit - 10*time.Second); f.now.Before(end); {
		f.now = f.now.Add(10 * time.Second)
		_ = f.m.Heartbeat(t.Context(), f.actor, i.Handle, a.ID)
		f.m.Tick(t.Context())
	}
	if !f.alive(i.Handle) {
		t.Fatal("a new idle limit ended a running shell before the floor")
	}
	f.now = f.now.Add(10 * time.Second)
	f.m.Tick(t.Context())
	if f.alive(i.Handle) {
		t.Fatal("a new idle limit never applied")
	}
	if r, _, _ := f.m.Ended(f.actor, i.Handle); r != ReasonIdle {
		t.Fatalf("ended reason %q, want idle", r)
	}
}

func TestDesktopShellStillEndsWithSession(t *testing.T) {
	f := setup(t)
	i, _ := f.open(t)
	f.r.binding.Live = false
	f.m.Tick(t.Context())
	if f.alive(i.Handle) {
		t.Fatal("a desktop shell outlived its session")
	}
	if _, _, ok := f.m.Ended(f.actor, i.Handle); ok {
		t.Fatal("a session end is not an inactivity limit")
	}
}

func TestValidLimit(t *testing.T) {
	for d, want := range map[time.Duration]bool{0: true, MinLimit: true, MaxLimit: true, MinLimit - time.Second: false, MaxLimit + time.Second: false, -time.Minute: false, 5 * time.Minute: false} {
		if ValidLimit(d) != want {
			t.Errorf("ValidLimit(%s) = %v", d, !want)
		}
	}
}

// Review fix: changing one limit never moves the other limit's deadline.
func TestUnchangedIdleLimitIsNotPostponedByDetachedChanges(t *testing.T) {
	f := setup(t)
	l := f.m.Limits()
	l.Desktop.Idle = MinLimit
	f.m.SetLimits(l)
	i, a := f.open(t)
	start := f.now
	for step := 0; f.alive(i.Handle) && f.now.Sub(start) < 2*time.Hour; step++ {
		if step%54 == 0 { // every nine minutes
			l = f.m.Limits()
			if l.Desktop.Detached == 20*time.Minute {
				l.Desktop.Detached = MinLimit
			} else {
				l.Desktop.Detached = 20 * time.Minute
			}
			f.m.SetLimits(l)
		}
		f.now = f.now.Add(10 * time.Second)
		_ = f.m.Heartbeat(t.Context(), f.actor, i.Handle, a.ID)
		f.m.Tick(t.Context())
	}
	if f.alive(i.Handle) {
		t.Fatal("detached-limit changes postponed an unchanged idle limit")
	}
	if got := f.now.Sub(start); got != MinLimit {
		t.Fatalf("idle shell ended after %s, want %s", got, MinLimit)
	}
}

// Review fix: shortening again (after lengthening in between) cannot slide the floor forever.
func TestRepeatedShorteningsCannotSlideTheFloor(t *testing.T) {
	f := setup(t)
	i, a := f.open(t)
	f.m.Detach(f.actor, i.Handle, a.ID)
	f.advance(t, 2*time.Hour)
	l := f.m.Limits()
	l.Desktop.Detached = MinLimit
	f.m.SetLimits(l)
	for cycle := 0; cycle < 12 && f.alive(i.Handle); cycle++ {
		f.advance(t, 9*time.Minute)
		l.Desktop.Detached = 20 * time.Minute
		f.m.SetLimits(l)
		l.Desktop.Detached = MinLimit
		f.m.SetLimits(l)
	}
	f.advance(t, time.Minute)
	if f.alive(i.Handle) {
		t.Fatal("repeated shortenings kept a detached shell alive past the first floor")
	}
	if r, _, ok := f.m.Ended(f.actor, i.Handle); !ok || r != ReasonDetached {
		t.Fatalf("ended %q %v", r, ok)
	}
}

// Review fix: a second shortening of a pending floor keeps the earlier deadline, and a new
// shortening after the person typed is anchored to the new change.
func TestFloorIsAnchoredToTheChangeAndNeverExtendsADeadline(t *testing.T) {
	f := setup(t)
	f.useRuntime("cluster")
	i, a := f.open(t)
	l := f.m.Limits()
	l.Cluster.Idle = 2 * time.Hour
	f.m.SetLimits(l)
	beat := func(d time.Duration) {
		for end := f.now.Add(d); f.now.Before(end); {
			f.now = f.now.Add(10 * time.Second)
			_ = f.m.Heartbeat(t.Context(), f.actor, i.Handle, a.ID)
			f.m.Tick(t.Context())
		}
	}
	beat(time.Hour + 55*time.Minute) // idle deadline is in 5 minutes
	l.Cluster.Idle = time.Hour       // shortened: the floor may not push past the old deadline
	f.m.SetLimits(l)
	beat(5*time.Minute - 10*time.Second)
	if !f.alive(i.Handle) {
		t.Fatal("ended before its original deadline")
	}
	beat(10 * time.Second)
	if f.alive(i.Handle) {
		t.Fatal("a shortening extended the shell past its original deadline")
	}
}
