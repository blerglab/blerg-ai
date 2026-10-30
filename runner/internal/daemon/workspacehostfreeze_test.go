package daemon

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type freezeFailWriters struct {
	thawErr error
	thaws   int
}

func (f *freezeFailWriters) Start(*exec.Cmd) error { return nil }
func (f *freezeFailWriters) Freeze(context.Context) error {
	return errors.New("waiting for freeze acknowledgement: context deadline exceeded")
}
func (f *freezeFailWriters) Thaw(context.Context) error { f.thaws++; return f.thawErr }
func (f *freezeFailWriters) Stop(context.Context) error { return nil }

type phaseRecorder struct {
	protocol.RuntimeSupervisor
	phases []string
}

func (p *phaseRecorder) Notify(_ context.Context, _ protocol.SupervisorLaunch, phase protocol.SupervisorPhase) error {
	p.phases = append(p.phases, string(phase))
	return nil
}

// A freeze that can't be proven skips the turn's sync but leaves the session usable: the writers
// are thawed and the session goes back to running. Only a failed thaw keeps it closed.
func TestHostSyncThawsAndReopensAfterAFailedFreeze(t *testing.T) {
	for _, c := range []struct {
		name       string
		thawErr    error
		wantPhases string
		wantResume int
	}{
		{"thaw works: session reopened", nil, "syncing,running", 1},
		{"thaw fails: session stays closed", errors.New("helper gone"), "syncing", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			writers := &freezeFailWriters{thawErr: c.thawErr}
			sup := &phaseRecorder{}
			resumed := 0
			svc := &LocalWorkspaceService{Supervisor: sup, ResumeStatus: func(context.Context, string) error { resumed++; return nil }}
			w := &LocalWorkspace{mu: new(sync.Mutex), state: workspacePrivateManifest{Session: "session"}}
			w.authorize = func(context.Context) error { return nil }
			h := &hostWorkspace{writers: writers, w: w, service: svc}
			w.host = h
			_, err := h.sync(t.Context())
			if err == nil || !strings.Contains(err.Error(), "host workspace writer freeze") {
				t.Fatalf("sync after a failed freeze: %v", err)
			}
			if writers.thaws != 1 {
				t.Fatalf("thaws = %d, want 1", writers.thaws)
			}
			if got := strings.Join(sup.phases, ","); got != c.wantPhases {
				t.Fatalf("phases = %q, want %q", got, c.wantPhases)
			}
			if resumed != c.wantResume {
				t.Fatalf("status resumed %d times, want %d", resumed, c.wantResume)
			}
		})
	}
}
