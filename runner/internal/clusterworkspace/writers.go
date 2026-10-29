package clusterworkspace

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Writers is the pod's dedicated agent + shell process containment. Freeze must
// wait until every descendant (including detached jobs) is frozen; Stop waits
// until all are reaped. Neither timeout nor a disconnected transport is proof.
// The runner and Git worker MUST live outside this containment. Calls are bound
// to this Runtime's immutable generation; a reused pod-wide freezer is invalid.
// P6 supplies the concrete containment with P5; fixtures use deterministic fakes.
type Writers interface {
	Freeze(context.Context) error
	Thaw(context.Context) error
	Stop(context.Context) error
}

func (r *Runtime) SetWriters(w Writers) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ready || r.stopped || w == nil {
		return ErrPlan
	}
	r.writers = w
	return nil
}

// ArmWriters precedes agent creation. Test runtimes with no agent need no
// freezer, but production may never silently settle a live, uncontained pod.
func (r *Runtime) ArmWriters() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready || r.stopped || r.closing.Load() || r.writers == nil {
		return ErrPlan
	}
	r.armed = true
	return nil
}
func (r *Runtime) retain(reason string) {
	for id, v := range r.results {
		v.Pending = true
		v.SyncError = reason
		r.results[id] = v
	}
}

// StartShell is the atomic process-creation entry point for P5's pod adapter,
// after its grant/key/lease checks. start may create the reserved process but
// must not wait for output. A separate AdmitShell call is not exec authority.
func (r *Runtime) StartShell(ctx context.Context, shell protocol.SupervisorShell, start func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready || r.stopped || r.closing.Load() || !r.armed || r.supervisor == nil || shell.Launch != r.supervisorLaunch || shell.Validate() != nil || start == nil {
		return ErrPlan
	}
	if err := r.supervisor.AdmitShell(ctx, shell); err != nil {
		return err
	}
	if err := start(); err != nil {
		proof, closeErr := r.supervisor.CloseShell(ctx, shell, "shell start failed")
		if closeErr != nil || proof.ValidateFor(shell.Launch, shell.ShellGeneration) != nil {
			r.stopped = true
			r.retain("shell cleanup unproven; storage retained")
		}
		return err
	}
	return nil
}

// FenceReaders prevents settlement after an uncertain reader shutdown. Stop
// still attempts all writer cleanup, but cannot turn uncertainty into success.
func (r *Runtime) FenceReaders() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	r.retain("runtime reader cleanup unproven; storage retained")
}

// StartWriter serializes trusted engine creation or P5's authenticated PTY
// admission with settlement. P5 retains its own reservation and grant checks.
func (r *Runtime) StartWriter(ctx context.Context, start func() error) error {
	// Never wait on the outer mutex from P5's serialized worker: settlement
	// may already own it while awaiting a P5 lifecycle reply.
	if !r.mu.TryLock() {
		return ErrPlan
	}
	defer r.mu.Unlock()
	if ctx.Err() != nil || !r.ready || !r.armed || r.stopped || r.closing.Load() || start == nil {
		return ErrPlan
	}
	return start()
}
