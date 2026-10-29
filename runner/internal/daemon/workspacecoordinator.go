package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

// Lock order: LocalWorkspace.mu -> workspaceCoordinator.mu -> P5's session
// queue -> its PRIVATE mutex. Never register P5 with LocalWorkspace.mu: a queued
// lifecycle call would reacquire it on another goroutine. P5's reconcile callback
// uses only an immutable Docker tuple; it never calls P2, the registry or readers.
// The registry mutex protects lookup only and is released before external work.
// Readers waiting for P2 must use workspaceReadLock so cancellation can join them
// while the lifecycle owner holds P2. Suspend joins before any pause/stop; resume
// follows verified unpause, root/current validation and P5's running ack.
type workspaceCoordinator struct {
	host          bool // host cleanup requires recorded helper receipts, never a Docker proof
	mu            sync.Mutex
	service       *shellruntime.Service
	launch        protocol.SupervisorLaunch
	binding       protocol.WorkspaceRuntimeBinding
	docker        *DockerWorkspaceContainer
	fencedRuntime bool // independent whole-runtime proof on crash recovery
	retired       bool
}

type workspaceSupervisors struct {
	service  *shellruntime.Service
	mu       sync.Mutex
	sessions map[string]*workspaceCoordinator
}

func newWorkspaceSupervisors(s *shellruntime.Service) *workspaceSupervisors {
	return &workspaceSupervisors{service: s, sessions: map[string]*workspaceCoordinator{}}
}

func (s *workspaceSupervisors) register(ctx context.Context, state workspacePrivateManifest, d *DockerWorkspaceContainer) error {
	if s == nil || s.service == nil || state.Binding == nil || state.Binding.Validate(state.Launch) != nil || state.SupervisorLaunch.Validate() != nil {
		return errors.New("workspace shell registration authority unavailable")
	}
	if _, err := s.peer(state.Session); err == nil {
		return errors.New("workspace coordinator already registered")
	}
	// This hook runs before the engine starts. The fixed helper resolves its own
	// account and passwd shell; browser input supplies no identity or command.
	b := workspaceShellBinding(state)
	b.Audience = ownershell.RuntimeAudience(b.Connection, b.RuntimeGeneration)
	command := shellruntime.SandboxCommand(d.ID, fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), shellruntime.Identity{Cwd: sandboxWorkdir})
	command.PublicRoot = sandboxWorkdir
	if len(state.Launch.Members) > 0 {
		command.Identity.Cwd = path.Join(sandboxWorkdir, state.Launch.Members[0].Path)
	}
	command.Args = append([]string{"--context", d.Context}, command.Args...)
	// Copy the physical tuple: no callback reads P2's mutable fields or locks.
	physical := &DockerWorkspaceContainer{Context: d.Context, ID: d.ID, Owner: d.Owner, Session: d.Session, Launch: d.Launch}
	if err := s.service.Register(ctx, new(sync.Mutex), b, command, physical.reconcileShells); err != nil {
		return err
	}
	c := &workspaceCoordinator{service: s.service, launch: state.SupervisorLaunch, binding: *state.Binding, docker: d}
	s.mu.Lock()
	s.sessions[state.Session] = c
	s.mu.Unlock()
	d.coordinator = c
	return nil
}

// An orphan is never admitted again. Stop the exact physical runtime out of band
// (including a crash-paused container), verify not running, then register that
// independent proof. A missing P5 map alone cannot manufacture a cleanup ack.
func (s *workspaceSupervisors) registerRecovery(ctx context.Context, state workspacePrivateManifest, d *DockerWorkspaceContainer) error {
	if state.Binding == nil || state.Binding.Validate(state.Launch) != nil || state.SupervisorLaunch.Validate() != nil {
		return errors.New("workspace recovery binding unavailable")
	}
	if old, err := s.peer(state.Session); err == nil {
		old.mu.Lock()
		same := old.fencedRuntime && old.launch == state.SupervisorLaunch && old.docker.RuntimeID() == d.RuntimeID()
		old.mu.Unlock()
		if !same {
			return errors.New("orphan recovery overlaps hosted coordinator")
		}
	}
	if err := d.fencePhysical(ctx); err != nil {
		return err
	}
	if err := d.Remove(ctx); err != nil {
		return err
	}
	c := &workspaceCoordinator{service: s.service, launch: state.SupervisorLaunch, binding: *state.Binding, docker: d, fencedRuntime: true}
	s.mu.Lock()
	s.sessions[state.Session] = c
	s.mu.Unlock()
	d.coordinator = c
	return nil
}

func (s *workspaceSupervisors) peer(id string) (*workspaceCoordinator, error) {
	if s == nil || s.service == nil {
		return nil, errors.New("workspace shell lifecycle is not registered")
	}
	s.mu.Lock()
	c := s.sessions[id]
	s.mu.Unlock()
	if c == nil {
		return nil, errors.New("workspace shell lifecycle is not registered")
	}
	return c, nil
}
func (c *workspaceCoordinator) peer(l protocol.SupervisorLaunch) (protocol.RuntimeSupervisor, protocol.SupervisorLaunch, error) {
	if l != c.launch || c.retired || c.fencedRuntime {
		return nil, protocol.SupervisorLaunch{}, errors.New("workspace shell incarnation fenced")
	}
	b, err := c.service.Binding(l.SessionID)
	if err != nil || b.Session != l.SessionID || b.Launch != l.LaunchGeneration || b.RuntimeGeneration != l.RuntimeGeneration || b.Runtime != l.Runtime || b.Root != c.binding.Root || b.ManifestDigest != c.binding.ManifestDigest {
		return nil, protocol.SupervisorLaunch{}, errors.New("workspace shell binding changed")
	}
	p := c.service.Lifecycle(l.SessionID)
	if p == nil {
		return nil, protocol.SupervisorLaunch{}, errors.New("workspace shell lifecycle is not registered")
	}
	return p, ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch, nil
}
func (s *workspaceSupervisors) AdmitShell(ctx context.Context, v protocol.SupervisorShell) error {
	c, err := s.peer(v.SessionID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, l, err := c.peer(v.Launch)
	if err != nil {
		return err
	}
	v.Launch = l
	return p.AdmitShell(ctx, v)
}
func (s *workspaceSupervisors) Notify(ctx context.Context, l protocol.SupervisorLaunch, phase protocol.SupervisorPhase) error {
	c, err := s.peer(l.SessionID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, local, err := c.peer(l)
	if err != nil {
		return err
	}
	return p.Notify(ctx, local, phase)
}
func (s *workspaceSupervisors) CloseShell(ctx context.Context, v protocol.SupervisorShell, reason string) (protocol.SupervisorCleanup, error) {
	c, err := s.peer(v.SessionID)
	if err != nil {
		return protocol.SupervisorCleanup{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, l, err := c.peer(v.Launch)
	if err != nil {
		return protocol.SupervisorCleanup{}, err
	}
	original := v.Launch
	v.Launch = l
	proof, err := p.CloseShell(ctx, v, reason)
	if err == nil {
		err = proof.ValidateFor(l, v.ShellGeneration)
		proof.Launch = original
	}
	return proof, err
}
func (s *workspaceSupervisors) CloseLaunch(ctx context.Context, l protocol.SupervisorLaunch, reason string) (protocol.SupervisorCleanup, error) {
	return s.proof(ctx, l, false, reason)
}
func (s *workspaceSupervisors) RecoverLaunch(ctx context.Context, l protocol.SupervisorLaunch) (protocol.SupervisorCleanup, error) {
	return s.proof(ctx, l, true, "recovery")
}
func (s *workspaceSupervisors) proof(ctx context.Context, l protocol.SupervisorLaunch, recovery bool, reason string) (protocol.SupervisorCleanup, error) {
	c, err := s.peer(l.SessionID)
	if err != nil {
		return protocol.SupervisorCleanup{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if l != c.launch {
		return protocol.SupervisorCleanup{}, errors.New("workspace launch changed")
	}
	if c.host && (c.retired || c.fencedRuntime) {
		return protocol.SupervisorCleanup{Launch: l, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}, nil
	}
	if c.retired && !c.fencedRuntime {
		if recovery {
			return protocol.SupervisorCleanup{}, errors.New("retired workspace cannot recover admission")
		}
		absent, err := c.docker.Absent(ctx)
		if err != nil || !absent {
			return protocol.SupervisorCleanup{}, errors.Join(err, errors.New("retired runtime absence unproved"))
		}
		return protocol.SupervisorCleanup{Launch: l, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}, nil
	}
	if c.fencedRuntime {
		if err := c.docker.fencePhysical(ctx); err != nil {
			return protocol.SupervisorCleanup{}, err
		}
		return protocol.SupervisorCleanup{Launch: l, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}, nil
	}
	p, local, err := c.peer(l)
	if err != nil {
		return protocol.SupervisorCleanup{}, err
	}
	var proof protocol.SupervisorCleanup
	if recovery {
		proof, err = p.RecoverLaunch(ctx, local)
	} else {
		err = p.Notify(ctx, local, "stopping")
		if err == nil {
			proof, err = p.CloseLaunch(ctx, local, reason)
		}
		if err != nil {
			if c.docker == nil {
				return protocol.SupervisorCleanup{}, err
			}
			// A failed helper close is not proof. The runtime owner can fence the
			// complete physical namespace and then join P5 against that evidence.
			if fenceErr := c.docker.fencePhysical(ctx); fenceErr != nil {
				return protocol.SupervisorCleanup{}, errors.Join(err, fenceErr)
			}
			proof, err = p.CloseLaunch(ctx, local, "physical_runtime_fenced")
		}
	}
	if err == nil {
		err = proof.ValidateFor(local, "")
		proof.Launch = l
	}
	return proof, err
}

func (c *workspaceCoordinator) rebind(ctx context.Context, l protocol.SupervisorLaunch) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.launch
	old.ConnectionID = l.ConnectionID
	if old != l || l.Validate() != nil {
		return errors.New("workspace rebind incarnation changed")
	}
	if c.retired || c.fencedRuntime {
		if c.host {
			c.launch = l
			return nil
		}
		absent, err := c.docker.Absent(ctx)
		if err != nil || !absent {
			return errors.Join(err, errors.New("retired workspace still has a physical runtime"))
		}
		c.launch = l
		return nil
	}
	previous := c.launch
	c.launch = l
	p, local, err := c.peer(l)
	if err == nil {
		var proof protocol.SupervisorCleanup
		proof, err = p.RecoverLaunch(ctx, local)
		if err == nil {
			err = proof.ValidateFor(local, "")
		}
	}
	if err != nil {
		c.launch = previous
	}
	return err
}

func workspaceReadLock(ctx context.Context, mu *sync.Mutex) error {
	for !mu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	if err := ctx.Err(); err != nil {
		mu.Unlock()
		return err
	}
	return nil
}

func (c *workspaceCoordinator) freeze(ctx context.Context) (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, l, err := c.peer(c.launch)
	if err != nil {
		return time.Time{}, err
	}
	freezer, ok := p.(interface {
		Freeze(context.Context, protocol.SupervisorLaunch) (time.Time, error)
	})
	if !ok {
		return time.Time{}, errors.New("workspace shell freeze unavailable")
	}
	return freezer.Freeze(ctx, l)
}

func (d *DockerWorkspaceContainer) fencePhysical(ctx context.Context) error {
	absent, err := d.Absent(ctx)
	if err != nil || absent {
		return err
	}
	if err = d.Identity(ctx, d.Session, d.Launch); err != nil {
		return err
	}
	raw, err := d.command(ctx, "inspect", "--format", "{{.State.Running}}", d.ID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) == "true" {
		if _, err = d.command(ctx, "kill", "--signal", "KILL", d.ID); err != nil {
			return err
		}
	}
	raw, err = d.command(ctx, "inspect", "--format", "{{.State.Running}}", d.ID)
	if err != nil || strings.TrimSpace(string(raw)) != "false" {
		return errors.New("workspace physical fencing unproved")
	}
	return nil
}

// Fixed, in-namespace kernel observation, never an empty Go map or client exit.
// A surviving helper refuses admission; crash recovery fences the whole runtime.
const workspaceShellProbe = `import os,sys
helper=os.stat('/usr/local/bin/blerg-shell-supervisor')
for name in os.listdir('/proc'):
 if not name.isdecimal(): continue
 try:
  if os.stat('/proc/'+name).st_uid!=os.geteuid(): continue
  p=os.stat('/proc/'+name+'/exe')
 except FileNotFoundError: continue
 if (p.st_dev,p.st_ino)==(helper.st_dev,helper.st_ino): sys.exit(1)
print('empty')
`

func (d *DockerWorkspaceContainer) reconcileShells(ctx context.Context) error {
	absent, err := d.Absent(ctx)
	if err != nil || absent {
		return err
	}
	if err = d.Identity(ctx, d.Session, d.Launch); err != nil {
		return err
	}
	running, err := d.command(ctx, "inspect", "--format", "{{.State.Running}} {{.State.Paused}}", d.ID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(running)) == "false false" {
		return nil
	}
	if strings.TrimSpace(string(running)) != "true false" {
		return errors.New("workspace shell reconciliation requires running unfrozen runtime")
	}
	raw, err := d.command(ctx, "exec", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), d.ID, "env", "-i", "PATH=/usr/bin:/bin", "python3", "-I", "-S", "-c", workspaceShellProbe)
	if err != nil || strings.TrimSpace(string(raw)) != "empty" {
		return errors.New("workspace orphan shell cleanup unproved")
	}
	return nil
}
func (d *DockerWorkspaceContainer) retireShells(ctx context.Context) error {
	if d.CloseStatus != nil {
		if err := d.CloseStatus(ctx); err != nil {
			return err
		}
	}
	if d.coordinator == nil {
		return nil
	}
	c := d.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retired {
		return nil
	}
	if c.fencedRuntime {
		c.retired = true
		return nil
	}
	_, l, err := c.peer(c.launch)
	if err != nil {
		return err
	}
	// Retire itself returns the exact all-launch proof and remains retryable
	// after a failed join. A queued close cannot enter a retiring registration.
	proof, err := c.service.Retire(ctx, l)
	if err != nil {
		return err
	}
	if err = proof.ValidateFor(l, ""); err != nil {
		return err
	}
	c.retired = true
	return nil
}

// Old journals have no affirmative eligibility and remain lifecycle-only. The
// server still independently requires a live human owner; eligibility is not a grant.
func workspaceShellBinding(state workspacePrivateManifest) ownershell.Binding {
	return ownershell.Binding{Session: state.Session, Owner: state.Launch.OwnerID, Launch: state.Launch.LaunchGeneration, Runtime: "sandbox", RuntimeGeneration: state.SupervisorLaunch.RuntimeGeneration, Connection: state.SupervisorLaunch.ConnectionID, Root: state.Binding.Root, ManifestDigest: state.Launch.Digest, Live: true, Child: !state.ShellEligible}
}

// A persisted stop intent never goes through live-running shell recovery on
// reconnect. Preserve the terminal fence, including a lost removal/retire ack.
func (c *workspaceCoordinator) rebindStopping(ctx context.Context, l protocol.SupervisorLaunch) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.launch
	old.ConnectionID = l.ConnectionID
	if old != l || l.Validate() != nil {
		return errors.New("workspace stop rebind incarnation changed")
	}
	absent, err := c.docker.Absent(ctx)
	if err != nil {
		return err
	}
	if !c.retired && !c.fencedRuntime {
		p, local, peerErr := c.peer(c.launch)
		if peerErr != nil {
			return peerErr
		}
		if absent {
			proof, retireErr := c.service.Retire(ctx, local)
			if retireErr != nil {
				return retireErr
			}
			if err = proof.ValidateFor(local, ""); err != nil {
				return err
			}
			c.retired = true
		} else {
			err = p.Notify(ctx, local, "stopping")
			if err != nil {
				if fenceErr := c.docker.fencePhysical(ctx); fenceErr != nil {
					return errors.Join(err, fenceErr)
				}
				proof, closeErr := p.CloseLaunch(ctx, local, "stop_reconnect")
				if closeErr != nil {
					return closeErr
				}
				if err = proof.ValidateFor(local, ""); err != nil {
					return err
				}
			}
		}
	}
	c.launch = l
	return nil
}
