package daemon

import "testing"

// A fixed session ID is valid again in the next test invocation (-count).
func TestSandboxCloneFixtureIsolated(t *testing.T) {
	for range 2 {
		t.Run("iteration", func(t *testing.T) {
			sandboxTestClone(t, "repeat-fixture")
		})
	}
}

// Exercise shutdown directly so a malformed replacement cannot lurk until the
// production idle timeout fires in an unrelated test.
func TestClaudeMCPReplacementCanClose(t *testing.T) {
	d, p := claudeMCPFixture(t)
	d.closeStdin(p)
	select {
	case <-p.closeReq:
	default:
		t.Fatal("close request was not delivered")
	}
}

func TestLineFixtureCanIdleShutdown(t *testing.T) {
	d, _, feed := newLineDriver(t)
	feed(`{"type":"result","subtype":"success"}`)
	d.sm.Lock()
	p, epoch := d.steer.proc, d.steer.idleEpoch
	d.sm.Unlock()
	d.idleFired(p.gen, epoch)
	select {
	case <-p.closeReq:
	default:
		t.Fatal("idle fixture did not close stdin")
	}
}

func TestLineFixtureStopsTimers(t *testing.T) {
	var d *claudeCodeDriver
	t.Run("fixture", func(t *testing.T) {
		var feed func(string)
		d, _, feed = newLineDriver(t)
		feed(`{"type":"result","subtype":"success"}`)
	})
	d.sm.Lock()
	defer d.sm.Unlock()
	if !d.steer.closed || d.steer.idleTimer != nil || d.steer.killTimer != nil || d.steer.settleTimer != nil {
		t.Fatal("fixture left a live driver or timer after cleanup")
	}
	select {
	case <-d.steer.proc.done:
	default:
		t.Fatal("fixture process did not finish")
	}
}
