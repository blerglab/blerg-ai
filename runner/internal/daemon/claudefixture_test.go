package daemon

import (
	"os/exec"
	"testing"
)

// Synthetic processes have the same lifecycle channels as a launched process.
// Their driver must stop before test globals are restored or idle callbacks can
// reach a later test, even when no subprocess was started.
func claudeTestProc(t *testing.T, d *claudeCodeDriver, p *ccProc) *ccProc {
	t.Helper()
	p.done = make(chan struct{})
	p.closeReq = make(chan struct{})
	if p.writeCh == nil {
		p.writeCh = make(chan []byte, 8)
	}
	if p.cmd == nil {
		p.cmd = &exec.Cmd{}
	}
	t.Cleanup(func() {
		d.sm.Lock()
		defer d.sm.Unlock()
		d.steer.closed = true
		d.stopIdleTimerLocked()
		d.stopTimerLocked(&d.steer.killTimer)
		d.stopTimerLocked(&d.steer.settleTimer)
		close(p.done)
	})
	return p
}
