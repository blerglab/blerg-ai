package ownershell

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"
)

// testClock is safe to advance while drain goroutines read it.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func setupClock(t *testing.T) (*fixture, *testClock) {
	t.Helper()
	f := setup(t)
	clock := &testClock{t: f.now}
	f.m.now = clock.Now
	return f, clock
}

func (f *fixture) closedCount() int {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	return f.s.closed
}

func (f *fixture) audits() []Audit {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	return append([]Audit(nil), f.audit.events...)
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A browser that stops reading fills its queue; the drain waits rather than
// ending the shell, and only after OutputStall detaches the attachment. The
// shell keeps running and can be reattached.
func TestStalledAttachmentDetachesAfterStallWithoutClosing(t *testing.T) {
	f, clock := setupClock(t)
	i, a := f.open(t)
	for range OutputQueue/MaxFrame + 2 {
		f.s.outputs[0] <- []byte("private-output")
	}
	eventually(t, "attachment queue did not fill", func() bool { return len(a.Output) == cap(a.Output) })
	clock.Add(OutputStall - time.Second)
	time.Sleep(50 * time.Millisecond)
	if f.closedCount() != 0 {
		t.Fatal("a full attachment queue ended the shell")
	}
	f.m.mu.Lock()
	attached := f.m.shells[i.Handle] != nil && f.m.shells[i.Handle].attachment != nil
	f.m.mu.Unlock()
	if !attached {
		t.Fatal("detached before the stall timeout")
	}
	clock.Add(time.Second)
	eventually(t, "stalled attachment was not detached", func() bool {
		f.m.mu.Lock()
		defer f.m.mu.Unlock()
		return f.m.shells[i.Handle] != nil && f.m.shells[i.Handle].attachment == nil
	})
	if f.closedCount() != 0 {
		t.Fatal("stall detach ended the shell")
	}
	events := f.audits()
	last := events[len(events)-1]
	if last.Event != Detached || last.Reason != ReasonSlow {
		t.Fatalf("stall cause not recorded: %+v", last)
	}
	delivered := false
	_ = f.m.Forward(t.Context(), f.actor, i.Handle, a.ID, []byte("queued"), func([]byte) error { delivered = true; return nil })
	if delivered {
		t.Fatal("the detached attachment still received output")
	}
	again, err := f.m.Attach(t.Context(), f.actor, i.Handle, false)
	if err != nil {
		t.Fatal("reattach after a stall:", err)
	}
	f.s.outputs[0] <- []byte("after-reattach")
	eventually(t, "reattached browser got no output", func() bool {
		for {
			select {
			case data := <-again.Output:
				if bytes.Equal(data, []byte("after-reattach")) {
					return true
				}
			default:
				return false
			}
		}
	})
}

// The output budget is shared by a session's four shells. Over it, output
// waits for the next second instead of closing anything.
func TestOutputBudgetSharedAcrossShellsThrottlesInsteadOfClosing(t *testing.T) {
	f, clock := setupClock(t)
	var attachments []Attachment
	for range MaxShells {
		_, a := f.open(t)
		attachments = append(attachments, a)
	}
	for _, ch := range f.s.outputs {
		ch <- make([]byte, MaxFrame)
	}
	f.s.outputs[0] <- make([]byte, MaxFrame)
	queued := func() int {
		n := 0
		for _, a := range attachments {
			n += len(a.Output)
		}
		return n
	}
	eventually(t, "budgeted frames were not delivered", func() bool { return queued() == 4 })
	time.Sleep(100 * time.Millisecond)
	if queued() != 4 {
		t.Fatal("output budget multiplied per pane")
	}
	if f.closedCount() != 0 {
		t.Fatal("output over budget ended a shell")
	}
	clock.Add(time.Second)
	eventually(t, "throttled frame never arrived", func() bool { return queued() == 5 })
	if f.closedCount() != 0 {
		t.Fatal("throttled output ended a shell")
	}
}

// A burst of small frames under the frame budget reaches the browser: drain
// pays the budget once, so Forward must not charge each frame a second time.
func TestBurstOfSmallFramesIsForwardedWithoutDoubleCounting(t *testing.T) {
	f, _ := setupClock(t)
	i, a := f.open(t)
	const frames = 100
	go func() {
		for n := range frames {
			f.s.outputs[0] <- []byte{byte(n)}
		}
	}()
	var got []byte
	for range frames {
		select {
		case data := <-a.Output:
			if err := f.m.Forward(t.Context(), f.actor, i.Handle, a.ID, data, func(b []byte) error { got = append(got, b...); return nil }); err != nil {
				t.Fatalf("frame %d refused: %v", len(got), err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of %d frames arrived", len(got), frames)
		}
	}
	for n := range frames {
		if got[n] != byte(n) {
			t.Fatal("frames reordered")
		}
	}
	if f.closedCount() != 0 {
		t.Fatal("a burst of small frames ended the shell")
	}
}

// A browser write that fails (a dead socket) detaches; the shell survives.
func TestFailedBrowserWriteDetachesWithoutClosing(t *testing.T) {
	f, _ := setupClock(t)
	i, a := f.open(t)
	err := f.m.Forward(t.Context(), f.actor, i.Handle, a.ID, []byte("x"), func([]byte) error { return errors.New("write deadline") })
	if err == nil {
		t.Fatal("failed write reported success")
	}
	if f.closedCount() != 0 {
		t.Fatal("a failed browser write ended the shell")
	}
	if _, err = f.m.Attach(t.Context(), f.actor, i.Handle, false); err != nil {
		t.Fatal("shell not reattachable after a failed write:", err)
	}
}

// A drain waiting on a full attachment or the budget must not hold revocation.
func TestBlockedDrainCannotHoldRevocation(t *testing.T) {
	f, _ := setupClock(t)
	f.s.closedEvents = make(chan struct{}, 1)
	_, a := f.open(t)
	for range OutputQueue/MaxFrame + 2 {
		f.s.outputs[0] <- []byte("x")
	}
	eventually(t, "attachment queue did not fill", func() bool { return len(a.Output) == cap(a.Output) })
	started := time.Now()
	f.m.Revoke(t.Context(), f.actor.Owner, "", "")
	if time.Since(started) > time.Second {
		t.Fatal("a blocked drain delayed revocation")
	}
	select {
	case <-f.s.closedEvents:
	case <-time.After(time.Second):
		t.Fatal("revocation did not close the shell")
	}
}

type creditSupervisor struct {
	*fakeSupervisor
	mu       sync.Mutex
	consumed int
}

func (c *creditSupervisor) Consumed(_ SignedGrant, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consumed += n
}
func (c *creditSupervisor) total() int { c.mu.Lock(); defer c.mu.Unlock(); return c.consumed }

// Credit goes back to the runtime only for frames the manager finished with.
// While a slow browser keeps its queue full, the frame waiting for a slot is
// not credited, so the runtime (and the program) is held back instead of the
// server queue growing; each frame the browser takes releases exactly its bytes.
func TestCreditReturnedOnlyForFinishedFrames(t *testing.T) {
	f, _ := setupClock(t)
	credit := &creditSupervisor{fakeSupervisor: f.s}
	f.m.supervisor = credit
	_, a := f.open(t)
	frame := []byte("0123456789")
	slots := cap(a.Output)
	for range slots + 3 {
		f.s.outputs[0] <- frame
	}
	eventually(t, "attachment queue did not fill", func() bool { return len(a.Output) == slots })
	time.Sleep(50 * time.Millisecond)
	if got := credit.total(); got != slots*len(frame) {
		t.Fatalf("credited %d bytes with %d delivered", got, slots*len(frame))
	}
	<-a.Output // the browser makes progress: one more frame finishes
	eventually(t, "progress returned no credit", func() bool { return credit.total() == (slots+1)*len(frame) })
	time.Sleep(50 * time.Millisecond)
	if got := credit.total(); got != (slots+1)*len(frame) {
		t.Fatalf("credited %d bytes after one frame of progress", got)
	}
}
