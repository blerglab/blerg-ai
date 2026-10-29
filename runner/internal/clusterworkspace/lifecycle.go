package clusterworkspace

import (
	"context"
	"errors"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Git is per-member; implementations must use immutable context/explicit
// destinations and validate pinned local identities before touching metadata.
type Git interface {
	Prepare(context.Context, Member, string, bool) (base, head string, err error)
	Head(context.Context, Member) (string, error)
	Push(context.Context, Member, string, string) error
}

// SnapshotGit separates the frozen copy from network publication. Live pod
// implementations must implement it; stateless lifecycle fakes may use Git.
type SnapshotGit interface {
	Snapshot(context.Context, Member, string) error
	PushSnapshot(context.Context, Member, string, string) error
}

// Runtime serializes preparation, turn-end push, stop and recovery. A single
// worker and rotating first member share one deadline; successful results are
// preserved and pending members retained. Agent start is outside Prepare and
// allowed only on success. No failed/uncertain allocation is deleted here.
type Runtime struct {
	copied           map[string]string
	closing          atomic.Bool
	writers          Writers
	armed            bool
	cleanupComplete  bool
	revision         int64
	supervisor       protocol.RuntimeSupervisor
	supervisorLaunch protocol.SupervisorLaunch
	mu               sync.Mutex
	plan             Plan
	git              Git
	ready            bool
	stopped          bool
	first            int
	results          map[string]protocol.WorkspaceMemberResult
}

func New(plan Plan, git Git) (*Runtime, error) {
	if git == nil {
		return nil, ErrPlan
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	// Own copies: callers cannot mutate authorization after admission.
	plan.Manifest.Members = append([]protocol.WorkspaceMember(nil), plan.Manifest.Members...)
	plan.Members = append([]Member(nil), plan.Members...)
	plan.LayoutSources = append([]protocol.DiscoverySource(nil), plan.LayoutSources...)
	for i := range plan.Members {
		for _, member := range plan.Manifest.Members {
			if plan.Members[i].ID == member.ID {
				plan.Members[i].Path = member.Path
				break
			}
		}
	}
	r := &Runtime{copied: map[string]string{}, revision: 1, plan: plan, git: git, results: map[string]protocol.WorkspaceMemberResult{}}
	for _, m := range plan.Members {
		r.results[m.ID] = protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: m.ID, LaunchGeneration: plan.Manifest.LaunchGeneration}, Branch: "wip/" + plan.SessionID, Pending: true, RecoveryLocation: path.Join("projects", m.Path)}
	}
	for _, result := range plan.PreviousResults {
		r.results[result.MemberID] = result
	}
	return r, nil
}
func (r *Runtime) Prepare(parent context.Context, resume bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() { r.revision++ }()
	if r.stopped {
		return ErrPlan
	}
	if r.ready {
		return nil
	}
	deadline := time.Now().Add(time.Duration(protocol.DefaultWorkspaceBudget().PrepareSeconds) * time.Second)
	if r.plan.PrepareDeadlineMillis > 0 && time.UnixMilli(r.plan.PrepareDeadlineMillis).Before(deadline) {
		deadline = time.UnixMilli(r.plan.PrepareDeadlineMillis)
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	if r.supervisor != nil {
		proof, e := r.supervisor.RecoverLaunch(ctx, r.supervisorLaunch)
		if e != nil || proof.ValidateFor(r.supervisorLaunch, "") != nil {
			return ErrPlan
		}
	}
	for _, m := range r.plan.Members {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		base, head, e := r.git.Prepare(ctx, m, "wip/"+r.plan.SessionID, resume)
		v := r.results[m.ID]
		if e != nil {
			v.SyncError = "repository preparation failed; uncertain data retained"
			v.Pending = true
			r.results[m.ID] = v
			if rollback, ok := r.git.(interface{ RollbackUnused() []string }); ok {
				for _, id := range rollback.RollbackUnused() {
					removed := r.results[id]
					removed.RecoveryLocation = ""
					removed.SyncError = "unused clone rolled back after preparation failed"
					r.results[id] = removed
				}
			}
			return errors.New(v.SyncError)
		}
		if v.Base == "" {
			v.Base = base
		}
		v.Pending = v.Pending || v.Head != head || v.PushError != "" || v.SyncError != ""
		v.Head = head
		v.SyncError = ""
		r.results[m.ID] = v
	}
	// Prime immutable copies before any agent can run. An unchanged first
	// turn (or resume retry) can publish these without freezing live writers.
	if copier, ok := r.git.(SnapshotGit); ok {
		refusals := r.beginRound(ctx)
		for _, m := range r.plan.Members {
			if refusals[m.ID] != nil {
				return ErrPlan
			}
			head := r.results[m.ID].Head
			if m.PushAuthorized {
				if err := copier.Snapshot(ctx, m, head); err != nil {
					return err
				}
			}
			r.copied[m.ID] = head
		}
	}
	r.ready = true
	return nil
}
func (r *Runtime) PushRound(parent context.Context) []protocol.WorkspaceMemberResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() { r.revision++ }()
	if !r.ready || r.stopped || r.closing.Load() {
		return r.snapshot()
	}
	end := time.Now().Add(time.Duration(protocol.DefaultWorkspaceBudget().SyncSeconds) * time.Second)
	ctx, cancel := context.WithDeadline(parent, end.Add(-5*time.Second))
	defer cancel()
	needed, copyHeads := r.needsSettlement(ctx)
	copyNeeded := len(copyHeads) > 0
	if !needed {
		return r.snapshot()
	}
	// One rotating start belongs to the entire round, including copy and
	// publication. Even a deadline-consuming copy must yield next round.
	defer r.advanceRound()
	refusals := r.beginRound(ctx)
	copier, staged := r.git.(SnapshotGit)
	if !copyNeeded {
		// Network-only retries use the last immutable copy; no phase/freeze work.
		r.pushRound(ctx, staged, refusals)
		return r.snapshot()
	}
	if r.supervisor != nil {
		if err := r.supervisor.Notify(ctx, r.supervisorLaunch, "syncing"); err != nil {
			r.retain("shell admission fence unavailable; storage retained")
			r.stopped = true
			return r.snapshot()
		}
	}
	if r.armed {
		if err := r.writers.Freeze(ctx); err != nil {
			r.retain("writer freeze unproven; storage retained")
			r.stopped = true
			return r.snapshot()
		}
	}
	func() {
		defer func() {
			cleanup, done := context.WithDeadline(context.WithoutCancel(parent), end)
			defer done()
			cleanup, stop := context.WithTimeout(cleanup, 5*time.Second)
			defer stop()
			if r.armed {
				if err := r.writers.Thaw(cleanup); err != nil {
					r.retain("writer resume unproven; storage retained")
					r.stopped = true
					return
				}
			}
			if r.supervisor != nil {
				if err := r.supervisor.Notify(cleanup, r.supervisorLaunch, "running"); err != nil {
					r.retain("shell running phase unavailable; storage retained")
					r.stopped = true
				}
			}
		}()
		if !staged {
			r.pushRound(ctx, false, refusals)
			return
		}
		for i := range len(r.plan.Members) {
			m := r.plan.Members[(r.first+i)%len(r.plan.Members)]
			if refusals[m.ID] != nil || !m.PushAuthorized || copyHeads[m.ID] == "" {
				continue
			}
			head, err := r.git.Head(ctx, m)
			if err != nil {
				refusals[m.ID] = err
				continue
			}
			if r.copied[m.ID] == head {
				continue
			}
			r.copied[m.ID] = ""
			if err := copier.Snapshot(ctx, m, head); err != nil {
				refusals[m.ID] = err
				continue
			}
			r.copied[m.ID] = head
		}
	}()
	if staged && !r.stopped {
		// The real pod's network I/O happens only after proven thaw. It still
		// shares this turn's single deadline and aggregate traversal/copy budgets.
		r.pushRound(ctx, true, refusals)
	}
	return r.snapshot()
}

// Compare heads to the last immutable copy, independently of push success.
// A pending same-head push needs publication but never another freeze/copy.
func (r *Runtime) needsSettlement(ctx context.Context) (needed bool, copyHeads map[string]string) {
	copyHeads = map[string]string{}
	for _, m := range r.plan.Members {
		v := r.results[m.ID]
		head, err := r.git.Head(ctx, m)
		if err != nil {
			v.Pending = true
			v.SyncError = "repository identity or head unavailable"
			r.results[m.ID] = v
			continue
		}
		if head != v.Head || v.Pending || v.PushError != "" || v.SyncError != "" {
			needed = true
		}
		if m.PushAuthorized && head != r.copied[m.ID] {
			copyHeads[m.ID] = head
		}
	}
	return needed, copyHeads
}
func (r *Runtime) beginRound(ctx context.Context) map[string]error {
	if rounds, ok := r.git.(interface{ BeginRound() }); ok {
		rounds.BeginRound()
	}
	if validator, ok := r.git.(interface {
		ValidateRound(context.Context) map[string]error
	}); ok {
		return validator.ValidateRound(ctx)
	}
	return map[string]error{}
}

func (r *Runtime) advanceRound() { r.first = (r.first + 1) % len(r.plan.Members) }
func (r *Runtime) push(ctx context.Context) {
	defer r.advanceRound()
	r.pushRound(ctx, false, r.beginRound(ctx))
}
func (r *Runtime) pushRound(parent context.Context, staged bool, refusals map[string]error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(protocol.DefaultWorkspaceBudget().SyncSeconds)*time.Second)
	defer cancel()
	n := len(r.plan.Members)
	for i := range n {
		m := r.plan.Members[(r.first+i)%n]
		v := r.results[m.ID]
		if refusals[m.ID] != nil {
			v.SyncError = "repository traversal or aggregate budget unavailable"
			v.Pending = true
			r.results[m.ID] = v
			continue
		}
		if staged && r.copied[m.ID] == v.Head && !v.Pending && v.SyncError == "" && v.PushError == "" {
			continue
		}
		if ctx.Err() != nil {
			v.Pending = true
			r.results[m.ID] = v
			continue
		}
		head, e := r.git.Head(ctx, m)
		if staged && m.PushAuthorized && e == nil {
			head = r.copied[m.ID]
		}
		if e != nil {
			v.SyncError = "repository identity or head unavailable"
			v.Pending = true
			r.results[m.ID] = v
			continue
		}
		// A successful unchanged head does not need another push. Failed pushes
		// keep Pending/PushError and are retried, including immediately on resume.
		if head == v.Head && !v.Pending && v.PushError == "" && v.SyncError == "" {
			continue
		}
		v.Head = head
		v.SyncError = ""
		if !m.PushAuthorized {
			v.PushError = "push is not authorized for this member"
			v.Pending = true
		} else {
			if staged {
				copier, ok := r.git.(SnapshotGit)
				if !ok {
					e = ErrPlan
				} else {
					e = copier.PushSnapshot(ctx, m, v.Branch, head)
				}
			} else {
				r.copied[m.ID] = head
				e = r.git.Push(ctx, m, v.Branch, head)
			}
			if e != nil {
				v.PushError = "push rejected or unavailable; work retained for retry"
				v.Pending = true
			} else {
				v.PushError = ""
				v.Pending = false
			}
		}
		r.results[m.ID] = v
	}
}

// Stop runs after the owner has terminated all agent/shell writers; failure to
// prove that must leave storage retained. A canceled push still retains pending.
func (r *Runtime) Stop(ctx context.Context) []protocol.WorkspaceMemberResult {
	r.FenceAdmission()
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() { r.revision++ }()
	if r.ready && !r.cleanupComplete {
		failure := ""
		if r.supervisor != nil {
			err := r.supervisor.Notify(ctx, r.supervisorLaunch, "stopping")
			proof, closeErr := r.supervisor.CloseLaunch(ctx, r.supervisorLaunch, "session stopping")
			if err != nil || closeErr != nil || proof.ValidateFor(r.supervisorLaunch, "") != nil {
				failure = "supervisor cleanup unproven; storage retained"
			}
		}
		// One failed containment must not prevent cleanup of the other writers.
		if r.armed {
			if err := r.writers.Stop(ctx); err != nil {
				failure = "writer cleanup unproven; storage retained"
			}
		}
		if failure != "" {
			r.retain(failure)
			r.stopped = true
			return r.snapshot()
		}
		r.cleanupComplete = true
		if !r.stopped {
			r.push(ctx)
		}
	}
	r.stopped = true
	return r.snapshot()
}
func (r *Runtime) Results() []protocol.WorkspaceMemberResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot()
}
func (r *Runtime) snapshot() []protocol.WorkspaceMemberResult {
	out := make([]protocol.WorkspaceMemberResult, 0, len(r.plan.Members))
	for _, m := range r.plan.Members {
		out = append(out, r.results[m.ID])
	}
	return out
}

// SetSupervisor connects P5's containment seam before preparation. A nil
// supervisor means no shell capability; it must never be used to bypass P5.
func (r *Runtime) SetSupervisor(supervisor protocol.RuntimeSupervisor, launch protocol.SupervisorLaunch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ready || r.stopped || supervisor == nil || launch.Validate() != nil || launch.SessionID != r.plan.SessionID || launch.LaunchGeneration != r.plan.Runtime.LaunchGeneration || launch.RuntimeGeneration != r.plan.Runtime.Generation || launch.Runtime != "cluster" {
		return ErrPlan
	}
	r.supervisor = supervisor
	r.supervisorLaunch = launch
	return nil
}

// AdmitShell is P5's entry into the runtime coordinator after its live grant
// checks. The shared mutex closes the late-admission race against Stop.
func (r *Runtime) AdmitShell(ctx context.Context, shell protocol.SupervisorShell) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready || r.stopped || r.closing.Load() || r.supervisor == nil || shell.Launch != r.supervisorLaunch || shell.Validate() != nil {
		return ErrPlan
	}
	return r.supervisor.AdmitShell(ctx, shell)
}

func (r *Runtime) Prepared() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ready && !r.stopped && !r.closing.Load()
}

// Snapshot binds report ordering to the state copied under the session mutex.
func (r *Runtime) Snapshot() ([]protocol.WorkspaceMemberResult, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot(), r.revision
}

// FenceAdmission is synchronous even while a settlement holds the coordinator.
func (r *Runtime) FenceAdmission() { r.closing.Store(true) }

func (r *Runtime) AdmissionFenced() bool { return r.closing.Load() }
