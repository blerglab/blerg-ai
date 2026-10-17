package daemon

import (
	"sync"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func idleTimerFixture(t *testing.T) (*claudeCodeDriver, *ccProc) {
	t.Helper()
	shrink(t, &ccIdleShutdown, time.Hour)
	d := newClaudeCodeDriver(t.TempDir(), "fixture", "", &collectEmitter{}, nil)
	d.steer.legacy = false
	p := &ccProc{gen: 1, closeReq: make(chan struct{}), done: make(chan struct{})}
	d.steer.proc = p
	t.Cleanup(func() { d.sm.Lock(); d.steer.closed = true; d.stopIdleTimerLocked(); d.sm.Unlock(); close(p.done) })
	return d, p
}

func TestIdleTimerOldCallbackCannotCloseResetWindow(t *testing.T) {
	d, p := idleTimerFixture(t)
	startTestTask(d, "last")
	d.sm.Lock()
	d.idleLocked(p)
	oldGeneration := p.gen
	oldEpoch := d.steer.idleEpoch
	d.sm.Unlock()
	d.closeBackgroundTask(noticeFromBlock("<task-id>last</task-id><status>completed</status>"))
	d.idleFired(oldGeneration, oldEpoch)
	select {
	case <-p.closeReq:
		t.Fatal("stale idle callback closed the refreshed idle window")
	default:
	}
}

// A reporter can block indefinitely; removing the last task must refresh the
// window before invoking it, rather than after an event happens to finish.
func TestIdleTimerLastTaskRefreshesBeforeReport(t *testing.T) {
	d, p := idleTimerFixture(t)
	startTestTask(d, "last")
	d.sm.Lock()
	d.idleLocked(p)
	old := d.steer.idleEpoch
	d.sm.Unlock()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseGate := idleTimerGateCleanup(t, release, done)
	d.onOpenWork = func(_ []agent.BackgroundTaskStartedPayload, _ []OpenSubagent) { close(entered); <-release }
	go func() {
		defer close(done)
		d.closeBackgroundTask(noticeFromBlock("<task-id>last</task-id><status>completed</status>"))
	}()
	waitIdleTimerSignal(t, entered)
	d.sm.Lock()
	fresh := d.steer.idleEpoch
	d.sm.Unlock()
	releaseGate()
	waitIdleTimerSignal(t, done)
	if fresh == old {
		t.Fatal("last-task removal did not refresh idle window before reporting")
	}
	d.idleFired(p.gen, old)
	assertIdleTimerOpen(t, p)
}

type idleCompletionEmitter struct {
	kind             string
	entered, release chan struct{}
}

func (e *idleCompletionEmitter) Emit(ev agent.Event) {
	if ev.Kind == e.kind {
		close(e.entered)
		<-e.release
	}
}

func TestIdleTimerLastWorkRefreshesBeforeCompletionEvent(t *testing.T) {
	for _, sub := range []bool{false, true} {
		name := "task"
		if sub {
			name = "resumed-subagent"
		}
		t.Run(name, func(t *testing.T) {
			d, p := idleTimerFixture(t)
			if sub {
				d.subagents = map[string]*ccSubagent{"resume": {p: agent.SubagentPayload{ChildID: "resume"}, resumed: true, background: true, started: time.Now()}}
			} else {
				startTestTask(d, "last")
			}
			d.sm.Lock()
			d.idleLocked(p)
			old := d.steer.idleEpoch
			d.sm.Unlock()
			kind := "background_task_done"
			if sub {
				kind = "subagent_done"
			}
			em := &idleCompletionEmitter{kind: kind, entered: make(chan struct{}), release: make(chan struct{})}
			d.emitter = em
			done := make(chan struct{})
			releaseGate := idleTimerGateCleanup(t, em.release, done)
			go func() {
				defer close(done)
				if sub {
					d.closeBackgroundSubagent(ccNotice{callID: "resume", status: "completed"})
				} else {
					d.closeBackgroundTask(ccNotice{taskID: "last", status: "completed"})
				}
			}()
			waitIdleTimerSignal(t, em.entered)
			d.idleFired(p.gen, old)
			// Release the emitter before assertions so a failure cannot leave a goroutine.
			releaseGate()
			waitIdleTimerSignal(t, done)
			assertIdleTimerOpen(t, p)
		})
	}
}

func assertIdleTimerOpen(t *testing.T, p *ccProc) {
	t.Helper()
	select {
	case <-p.closeReq:
		t.Fatal("stale callback closed the refreshed idle window")
	default:
	}
}

func TestIdleTimerDeferralsKeepCapAndCommitShutdown(t *testing.T) {
	d, p := idleTimerFixture(t)
	setDeferrals(t, 2)
	startTestTask(d, "running")
	d.sm.Lock()
	d.idleLocked(p)
	d.sm.Unlock()
	for i := 0; i < 3; i++ {
		d.sm.Lock()
		epoch := d.steer.idleEpoch
		d.sm.Unlock()
		d.idleFired(p.gen, epoch)
		d.sm.Lock()
		closed, deferrals, intentional := p.stdinClosed, d.steer.idleDeferrals, p.intentionalClose
		d.sm.Unlock()
		if i < 2 {
			if closed || deferrals != i+1 {
				t.Fatalf("deferral %d: closed=%v deferrals=%d", i, closed, deferrals)
			}
		} else {
			if !closed || !intentional || deferrals != 2 {
				t.Fatalf("shutdown not committed at cap: closed=%v intentional=%v deferrals=%d", closed, intentional, deferrals)
			}
			select {
			case <-p.closeReq:
			default:
				t.Fatal("committed shutdown did not close stdin")
			}
			// A callback that was already dispatched cannot close twice or rearm.
			d.idleFired(p.gen, epoch)
		}
	}
}

func TestIdleTimerStoppedCallbackCannotCloseSession(t *testing.T) {
	d, p := idleTimerFixture(t)
	d.sm.Lock()
	d.idleLocked(p)
	old := d.steer.idleEpoch
	d.stopIdleTimerLocked()
	d.sm.Unlock()
	d.idleFired(p.gen, old)
	assertIdleTimerOpen(t, p)
}

// A missing event should fail promptly and release its producer, even when the
// assertion that awaited the event terminates the test early.
func idleTimerGateCleanup(t *testing.T, release, done chan struct{}) func() {
	t.Helper()
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); waitIdleTimerSignal(t, done) })
	return unblock
}
func waitIdleTimerSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for idle timer fixture")
	}
}
