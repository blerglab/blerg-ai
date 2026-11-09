package daemon

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
func (*freezeFailWriters) OnLimit(func(string))         {}
func (f *freezeFailWriters) Stop(context.Context) error { return nil }

type phaseRecorder struct {
	protocol.RuntimeSupervisor
	phases     []string
	privileged bool
}

func (p *phaseRecorder) PrivilegedShellOpen(string) (bool, bool) { return p.privileged, true }

func TestHostSyncCarriesPrivilegedCopyNote(t *testing.T) {
	sup := &phaseRecorder{privileged: true}
	w := &LocalWorkspace{mu: new(sync.Mutex), record: filepath.Join(t.TempDir(), "record.json"), state: workspacePrivateManifest{Session: "session", Members: []workspacePrivateMember{{Result: protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "member"}}}}}}
	w.authorize = func(context.Context) error { return nil }
	h := &hostWorkspace{writers: &countingWriters{}, w: w, service: &LocalWorkspaceService{Supervisor: sup}}
	w.host = h
	results, err := h.sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !strings.Contains(results[0].SyncNote, "may include in-flight writes") {
		t.Fatalf("missing turn copy note: %+v", results)
	}
	sup.privileged = false
	results, err = h.sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].SyncNote != "" {
		t.Fatal("new ordinary copy kept privileged note")
	}
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
			ctx, work := activity.NewWork(t.Context())
			_, err := h.sync(ctx)
			measured := work.Seconds(time.Second)
			if measured["freeze"] <= 0 || measured["thaw"] <= 0 {
				t.Fatalf("lifecycle intervals missing: %+v", measured)
			}
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

type countingWriters struct{ freezes, thaws int }

func (c *countingWriters) Start(*exec.Cmd) error        { return nil }
func (c *countingWriters) Freeze(context.Context) error { c.freezes++; return nil }
func (c *countingWriters) Thaw(context.Context) error   { c.thaws++; return nil }
func (*countingWriters) OnLimit(func(string))           {}
func (c *countingWriters) Stop(context.Context) error   { return nil }

// After a proven freeze the sync checks its authority again. A definitive refusal (the launch was
// replaced or the session ended) keeps the writers stopped and says why. Anything else (the
// connection dropped, a timeout, a server error, an unexplained refusal) is not a verdict: the
// writers are thawed, this turn's sync is skipped with a note, and the next turn end tries again.
func TestHostSyncAuthorityFailureAfterFreeze(t *testing.T) {
	for _, c := range []struct {
		name        string
		err         error
		wantThaws   int
		wantPhases  string
		wantResumed int
		wantText    string
	}{
		{"refused: launch replaced", &WorkspaceRefusedError{Reason: "a newer launch replaced this one"}, 0, "syncing", 0, "a newer launch replaced this one"},
		{"transient: disconnected", errors.New("workspace authority disconnected"), 1, "syncing,running", 1, "skipped"},
		{"transient: timeout", context.DeadlineExceeded, 1, "syncing,running", 1, "skipped"},
		{"transient: unexplained refusal", errors.New("workspace authority refused"), 1, "syncing,running", 1, "skipped"},
	} {
		t.Run(c.name, func(t *testing.T) {
			writers := &countingWriters{}
			sup := &phaseRecorder{}
			resumed := 0
			svc := &LocalWorkspaceService{Supervisor: sup, ResumeStatus: func(context.Context, string) error { resumed++; return nil }}
			w := &LocalWorkspace{mu: new(sync.Mutex), state: workspacePrivateManifest{Session: "session"}}
			checks := 0
			w.authorize = func(context.Context) error {
				checks++
				if checks == 2 { // the check after the freeze
					return c.err
				}
				return nil
			}
			h := &hostWorkspace{writers: writers, w: w, service: svc}
			w.host = h
			ctx, work := activity.NewWork(t.Context())
			_, err := h.sync(ctx)
			measured := work.Seconds(time.Second)
			_, thawed := measured["thaw"]
			if measured["freeze"] <= 0 || thawed != (c.wantThaws > 0) {
				t.Fatalf("lifecycle intervals missing: %+v", measured)
			}
			if err == nil || !strings.Contains(err.Error(), c.wantText) {
				t.Fatalf("sync: %v, want it to say %q", err, c.wantText)
			}
			if writers.freezes != 1 || writers.thaws != c.wantThaws {
				t.Fatalf("freezes=%d thaws=%d, want 1 and %d", writers.freezes, writers.thaws, c.wantThaws)
			}
			if got := strings.Join(sup.phases, ","); got != c.wantPhases {
				t.Fatalf("phases = %q, want %q", got, c.wantPhases)
			}
			if resumed != c.wantResumed {
				t.Fatalf("status resumed %d times, want %d", resumed, c.wantResumed)
			}
			if c.wantThaws == 0 {
				return
			}
			// The next turn end tries again: it freezes, checks, and thaws as usual.
			_, _ = h.sync(t.Context())
			if writers.freezes != 2 || writers.thaws != 2 || checks != 4 {
				t.Fatalf("retry: freezes=%d thaws=%d checks=%d, want 2, 2, 4", writers.freezes, writers.thaws, checks)
			}
		})
	}
}

func TestHostMembershipFenceWaitsForPrivilegedShell(t *testing.T) {
	sup := &phaseRecorder{privileged: true}
	writers := &countingWriters{}
	svc := &LocalWorkspaceService{Supervisor: sup}
	w := &LocalWorkspace{state: workspacePrivateManifest{Session: "session"}, host: &hostWorkspace{writers: writers}}
	err := svc.fenceMembership(t.Context(), w)
	var waiting *membershipWaiting
	if !errors.As(err, &waiting) || waiting.code != protocol.MembershipWaitShell || writers.freezes != 0 {
		t.Fatalf("fence must wait without accepting freeze proof: err=%v freezes=%d", err, writers.freezes)
	}
	sup.privileged = false
	if err := svc.fenceMembership(t.Context(), w); err != nil || writers.freezes != 1 {
		t.Fatalf("closed shell must allow fence: err=%v freezes=%d", err, writers.freezes)
	}
}
