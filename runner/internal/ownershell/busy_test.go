package ownershell

import (
	"context"
	"testing"
	"time"
)

type busyResolver struct{ binding Binding }

func (r *busyResolver) Resolve(context.Context, string) (Binding, error) {
	return r.binding, Refuse(ReasonRuntimeBusy)
}

func closedFor(f *fixture) int {
	n := 0
	for _, e := range f.audit.events {
		if e.Event == Closed {
			n++
		}
	}
	return n
}

// Review finding 1: busy is inconclusive only while it names this shell's own session and owner.
func TestBusyNamingAnotherOwnerClosesTheShell(t *testing.T) {
	f := setup(t)
	f.open(t)
	f.m.mu.Lock()
	f.m.resolver = &busyResolver{Binding{Session: "session", Owner: "someone-else"}}
	f.m.mu.Unlock()
	f.m.Tick(t.Context())
	if closedFor(f) != 1 {
		t.Fatalf("busy hid an owner change: %+v", f.audit.events)
	}
}

func TestBusyNamingThisShellKeepsIt(t *testing.T) {
	f := setup(t)
	f.open(t)
	f.m.mu.Lock()
	f.m.resolver = &busyResolver{Binding{Session: "session", Owner: "owner"}}
	f.m.mu.Unlock()
	f.m.Tick(t.Context())
	if closedFor(f) != 0 {
		t.Fatalf("busy ended the shell: %+v", f.audit.events)
	}
}

// Review finding 3: output that cannot be checked is never silently dropped from a
// connected browser stream. The attachment ends (the shell stays) so the browser reattaches.
func TestBusyDuringOutputEndsTheAttachmentNotTheShell(t *testing.T) {
	f := setup(t)
	info, at := f.open(t)
	f.m.mu.Lock()
	f.m.resolver = &busyResolver{Binding{Session: "session", Owner: "owner"}}
	f.m.mu.Unlock()
	f.s.outputs[0] <- []byte("unchecked")
	select {
	case data, ok := <-at.Output:
		if ok {
			t.Fatalf("unchecked output delivered: %q", data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("busy output check left the attachment open with a silent hole")
	}
	f.m.mu.Lock()
	_, kept := f.m.shells[info.Handle]
	f.m.mu.Unlock()
	if !kept || closedFor(f) != 0 {
		t.Fatalf("busy output check ended the shell: %+v", f.audit.events)
	}
}

// #267 with #249 and #268: a busy check during drain ends the browser attachment (the
// shell stays), still returns the frame's credit to the runtime, and keeps the frame in
// the scrollback ring, which only a reattachment that passed its own check replays.
func TestBusyDuringOutputKeepsRingAndCredit(t *testing.T) {
	f := setup(t)
	credit := &creditSupervisor{fakeSupervisor: f.s}
	f.m.supervisor = credit
	info, at := f.open(t)
	f.s.outputs[0] <- []byte("before|")
	select {
	case data := <-at.Output:
		if string(data) != "before|" {
			t.Fatalf("first output = %q", data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no output before busy")
	}
	f.m.mu.Lock()
	f.m.resolver = &busyResolver{Binding{Session: "session", Owner: "owner"}}
	f.m.mu.Unlock()
	f.s.outputs[0] <- []byte("during|")
	select {
	case data, ok := <-at.Output:
		if ok {
			t.Fatalf("unchecked output delivered on the connected stream: %q", data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("busy output check left the attachment open")
	}
	eventually(t, "busy frame's credit was not returned", func() bool { return credit.total() == len("before|")+len("during|") })
	if closedFor(f) != 0 {
		t.Fatalf("busy output check ended the shell: %+v", f.audit.events)
	}
	f.m.mu.Lock()
	f.m.resolver = f.r
	f.m.mu.Unlock()
	again, err := f.m.AttachWithReplay(t.Context(), f.actor, info.Handle, false)
	if err != nil {
		t.Fatal("reattach after busy", err)
	}
	if string(again.Replay) != "before|during|" || again.ReplayEnd != uint64(len("before|during|")) {
		t.Fatalf("replay after busy = %q end %d", again.Replay, again.ReplayEnd)
	}
	f.s.outputs[0] <- []byte("after")
	select {
	case data := <-again.Output:
		if string(data) != "after" {
			t.Fatalf("output after reattach = %q", data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no output after reattaching")
	}
}
