package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DockerWorkspaceContainer is bound to an immutable Docker ID, not a reusable
// name. P6 supplies P4 cancellation and P5's shell-header signal callbacks.
// Construct it only from Docker's create response before starting any writers.
type DockerWorkspaceContainer struct {
	CloseStatus      func(context.Context) error
	coordinator      *workspaceCoordinator
	ResumeCheck      func(context.Context) error
	freezeStop       chan struct{}
	freezeDone       chan error
	ResumeStatus     func(context.Context) error
	Supervisor       protocol.RuntimeSupervisor
	SupervisorLaunch protocol.SupervisorLaunch
	probeMu          sync.Mutex
	probeFenced      bool
	Context          string
	ID               string
	Owner            string
	Session          string
	Launch           string
	Paths            map[string]bool
	CancelStatus     func(context.Context) error
	FenceWriters     func(context.Context) error
	SettleMember     func(context.Context, string, string) (bool, error)
	Signal           func(string)
}

func (d *DockerWorkspaceContainer) RootIdentity(ctx context.Context) (protocol.WorkspaceRoot, error) {
	out, err := d.command(ctx, "exec", d.ID, "stat", "-c", "%d %i", sandboxWorkdir)
	if err != nil {
		return protocol.WorkspaceRoot{}, errors.New("runtime root stat failed")
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return protocol.WorkspaceRoot{}, errors.New("invalid runtime root identity")
	}
	device, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return protocol.WorkspaceRoot{}, err
	}
	inode, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || inode == 0 {
		return protocol.WorkspaceRoot{}, errors.New("invalid runtime root inode")
	}
	return protocol.WorkspaceRoot{Device: device, Inode: inode}, nil
}

// Reconcile fences identity before recovering a freeze left by a daemon crash.
// Inspect runs out of band; there is no exec into a possibly paused container.
func (d *DockerWorkspaceContainer) Reconcile(ctx context.Context) error {
	if err := d.Identity(ctx, d.Session, d.Launch); err != nil {
		return err
	}
	raw, err := d.command(ctx, "inspect", "--format", "{{.State.Paused}}", d.ID)
	if err != nil {
		return errors.New("workspace pause state unknown")
	}
	switch strings.TrimSpace(string(raw)) {
	case "true":
		if err := d.Thaw(ctx); err != nil {
			return err
		}
	case "false":
	default:
		return errors.New("invalid workspace pause state")
	}
	if err := d.cleanProbes(ctx); err != nil {
		return err
	}
	d.probeMu.Lock()
	d.probeFenced = false
	d.probeMu.Unlock()
	return d.Identity(ctx, d.Session, d.Launch)
}

func (d *DockerWorkspaceContainer) RuntimeID() string { return d.Context + ":" + d.ID }

const workspaceSessionLabel = "blerg.runner.workspace-session"
const workspaceLaunchLabel = "blerg.runner.workspace-launch"

func (d *DockerWorkspaceContainer) command(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args = append([]string{"--context", d.Context}, args...)
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // fixed binary, trusted adapter argv, no host shell
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return out, errors.Join(err, ctx.Err())
	}
	return out, err
}

func (d *DockerWorkspaceContainer) Identity(ctx context.Context, session, launch string) error {
	if d.Context == "" || d.ID == "" || len(d.ID) != 64 || d.Session != session || d.Launch != launch || d.Owner == "" {
		return errors.New("invalid workspace runtime binding")
	}
	raw, err := d.command(ctx, "inspect", "--type", "container", d.ID)
	if err != nil {
		return errors.New("workspace container unavailable")
	}
	var values []struct {
		ID     string
		Config struct{ Labels map[string]string }
		State  struct{ Running bool }
	}
	if json.Unmarshal(raw, &values) != nil || len(values) != 1 {
		return errors.New("invalid container identity response")
	}
	v := values[0]
	if v.ID != d.ID || v.Config.Labels[sandboxContextLabel] != d.Context || v.Config.Labels[sandboxOwnerLabel] != d.Owner || v.Config.Labels[workspaceSessionLabel] != session || v.Config.Labels[workspaceLaunchLabel] != launch {
		return errors.New("workspace container identity changed")
	}
	return nil
}

// StopWriters is supplied by the combined agent and P5 supervisor adapter. It
// must fence future starts, kill descendants, drain/reap, and prove no writers
// remain except the bounded settlement service. No callback means refusal.
func (d *DockerWorkspaceContainer) StopWriters(ctx context.Context) error {
	defer func() { _ = d.joinFreeze() }()
	// Close idle explorer helpers before stopping their container, while their
	// EOF/exit acknowledgment is still available. Removal repeats this safely.
	if d.CloseStatus != nil {
		if err := d.CloseStatus(ctx); err != nil {
			return err
		}
	}
	if d.Supervisor != nil {
		proof, err := d.Supervisor.CloseLaunch(ctx, d.SupervisorLaunch, "workspace stopping")
		if err != nil {
			return err
		}
		if err := proof.ValidateFor(d.SupervisorLaunch, ""); err != nil {
			return err
		}
	}
	if d.FenceWriters != nil {
		if err := d.FenceWriters(ctx); err != nil {
			return err
		}
	}
	// Stopping the complete container fences every agent/shell descendant,
	// including detached jobs. Offline settlement uses a new capped worker.
	if _, err := d.command(ctx, "stop", "--time", "1", d.ID); err != nil {
		return err
	}
	raw, err := d.command(ctx, "inspect", "--format", "{{.State.Running}}", d.ID)
	if err != nil || strings.TrimSpace(string(raw)) != "false" {
		return errors.New("runtime writers remain")
	}
	return nil
}

func (d *DockerWorkspaceContainer) QuiesceStatus(ctx context.Context) error {
	if d.CancelStatus == nil {
		return errors.New("workspace status cancellation unavailable")
	}
	return d.CancelStatus(ctx)
}

func (d *DockerWorkspaceContainer) memberPath(member string) (string, error) {
	if !d.Paths[member] || path.IsAbs(member) || path.Clean(member) != member || member == ".." || strings.HasPrefix(member, "../") {
		return "", errors.New("unknown runtime member")
	}
	return path.Join(sandboxWorkdir, member), nil
}

func (d *DockerWorkspaceContainer) Head(ctx context.Context, member string) (string, error) {
	cwd, err := d.memberPath(member)
	if err != nil {
		return "", err
	}
	d.probeMu.Lock()
	defer d.probeMu.Unlock()
	if d.probeFenced {
		return "", errors.New("runtime head probe cleanup unavailable")
	}
	// The in-container timer is independent of the host Docker client. No Git,
	// config/includes, child processes, or blocking opens are involved.
	out, err := d.command(ctx, "exec", "-w", cwd, d.ID, "env", "-i", "PATH=/usr/bin:/bin", "python3", "-I", "-S", "-c", workspaceHeadProbe, "1")
	if err != nil {
		// An interrupted exec acknowledgement is uncertain. Fence additional probes
		// until runtime reconciliation; do not infer cleanup from client exit.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			cleanupErr := d.cleanProbes(cleanup)
			cancel()
			d.probeFenced = true
			return "", &workspaceProbeUncertain{errors.Join(err, cleanupErr)}
		}
		return "", errors.New("runtime member head unavailable")
	}
	head := strings.TrimSpace(string(out))
	if len(head) > 64 || (head != "" && !protocol.ValidSessionOriginCommit(head)) {
		return "", errors.New("invalid runtime head response")
	}
	return head, nil
}

func (d *DockerWorkspaceContainer) Freeze(ctx context.Context) error {
	if d.coordinator != nil {
		until, err := d.coordinator.freeze(ctx)
		if err != nil {
			return err
		}
		d.freezeStop = make(chan struct{})
		d.freezeDone = make(chan error, 1)
		stop, done := d.freezeStop, d.freezeDone
		physical := &DockerWorkspaceContainer{Context: d.Context, ID: d.ID, Owner: d.Owner, Session: d.Session, Launch: d.Launch}
		go func() {
			timer := time.NewTimer(time.Until(until))
			defer timer.Stop()
			select {
			case <-stop:
				done <- nil
			case <-timer.C:
				call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				done <- errors.Join(errors.New("workspace freeze exceeded shell lease"), physical.fencePhysical(call))
			}
		}()
	} else if d.Supervisor != nil {
		if err := d.Supervisor.Notify(ctx, d.SupervisorLaunch, "syncing"); err != nil {
			return err
		}
	}
	_, err := d.command(ctx, "pause", d.ID)
	if err != nil {
		return err
	}
	return d.verifyPause(ctx, true)
}
func (d *DockerWorkspaceContainer) joinFreeze() error {
	if d.freezeStop == nil {
		return nil
	}
	close(d.freezeStop)
	err := <-d.freezeDone
	d.freezeStop = nil
	d.freezeDone = nil
	return err
}
func (d *DockerWorkspaceContainer) Thaw(ctx context.Context) error {
	_, err := d.command(ctx, "unpause", d.ID)
	if err == nil {
		err = d.verifyPause(ctx, false)
	}
	err = errors.Join(err, d.joinFreeze())
	if err == nil && d.coordinator != nil {
		root, rootErr := d.RootIdentity(ctx)
		if rootErr != nil || root != d.coordinator.binding.Root {
			err = errors.New("workspace root changed during freeze")
		}
	}
	if err == nil && d.ResumeCheck != nil {
		err = d.ResumeCheck(ctx)
	}
	if err == nil && d.Supervisor != nil {
		err = d.Supervisor.Notify(ctx, d.SupervisorLaunch, "running")
	}
	return err
}
func (d *DockerWorkspaceContainer) verifyPause(ctx context.Context, want bool) error {
	if err := d.Identity(ctx, d.Session, d.Launch); err != nil {
		return err
	}
	raw, err := d.command(ctx, "inspect", "--format", "{{.State.Running}} {{.State.Paused}}", d.ID)
	if err != nil || strings.TrimSpace(string(raw)) != "true "+strconv.FormatBool(want) {
		return errors.New("workspace running/pause state unverified")
	}
	return nil
}

func (d *DockerWorkspaceContainer) SyncSignal(message string) {
	if d.Signal != nil {
		d.Signal(message)
	}
}
func (d *DockerWorkspaceContainer) Settle(ctx context.Context, member, session string) (bool, error) {
	if _, err := d.memberPath(member); err != nil || session != d.Session {
		return false, errors.New("invalid settlement binding")
	}
	if d.SettleMember != nil {
		return d.SettleMember(ctx, member, session)
	}
	return false, nil // after removal the isolated worker snapshots remaining work
}

func (d *DockerWorkspaceContainer) Remove(ctx context.Context) error {
	if d.ID == "" || len(d.ID) != 64 {
		return errors.New("invalid workspace container ID")
	}
	existing, err := d.command(ctx, "ps", "-a", "--no-trunc", "--filter", "id="+d.ID, "--format", "{{.ID}}")
	if err != nil {
		return errors.New("workspace container absence unknown")
	}
	if strings.TrimSpace(string(existing)) == "" {
		return d.retireShells(ctx)
	}

	if err := d.Identity(ctx, d.Session, d.Launch); err != nil {
		return err
	}
	if _, err := d.command(ctx, "rm", "-f", d.ID); err != nil {
		return fmt.Errorf("workspace removal uncertain: %w", err)
	}
	// The exact immutable ID must be absent, including stopped containers.
	out, err := d.command(ctx, "ps", "-a", "--no-trunc", "--filter", "id="+d.ID, "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(out)) != "" {
		return errors.New("workspace container removal unproven")
	}
	return d.retireShells(ctx)
}

// WorkspaceDockerMountPlan is daemon-private. Do not serialize it to an agent,
// browser, or public protocol payload: object mount paths are confidential.
// The runtime starter consumes this plan before starting any agent/shell, then
// calls BindRuntime with Docker's immutable ID and reports the returned P0b root.
type WorkspaceDockerMountPlan struct {
	PublicRoot  string
	Mounts      []string
	Labels      []string
	MemberPaths []string
	Notice      string
}

func (w *LocalWorkspace) DockerMountPlan(ctx context.Context) (WorkspaceDockerMountPlan, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.Stopped || w.state.Unavailable {
		return WorkspaceDockerMountPlan{}, errors.New("workspace unavailable")
	}
	if err := w.validateAllocation(ctx); err != nil {
		return WorkspaceDockerMountPlan{}, err
	}
	if currentDockerContext() == "" || sandboxOwner() == "" {
		return WorkspaceDockerMountPlan{}, errors.New("workspace Docker authority unavailable")
	}
	mountSource := w.state.Root
	if remote, ok := w.storage.(workspaceRemoteStorage); ok {
		var err error
		mountSource, err = remote.MountSource(ctx, w.state.Session, w.state.Root)
		if err != nil {
			return WorkspaceDockerMountPlan{}, err
		}
	}
	rootMount := workspaceStorageMount(mountSource, sandboxWorkdir)
	plan := WorkspaceDockerMountPlan{PublicRoot: w.state.Root, Notice: "Only selected repositories were copied.", Labels: []string{
		sandboxOwnerLabel + "=" + sandboxOwner(), sandboxContextLabel + "=" + currentDockerContext(), workspaceSessionLabel + "=" + w.state.Session, workspaceLaunchLabel + "=" + w.state.Launch.LaunchGeneration,
	}, Mounts: []string{rootMount}}
	for _, m := range w.state.Members {
		if err := w.validateMember(m); err != nil {
			return WorkspaceDockerMountPlan{}, err
		}
		if !safeSandboxGitTarget(m.Source.Objects) {
			return WorkspaceDockerMountPlan{}, errors.New("unsafe workspace object mount target")
		}
		plan.Mounts = append(plan.Mounts, sandboxBindMount(m.Source.Objects, m.Source.Objects)+",readonly")
		plan.MemberPaths = append(plan.MemberPaths, m.Member.Path)
	}
	return plan, nil
}

// Absent is a generation-bound exact-ID absence check; it never removes anything.
func (d *DockerWorkspaceContainer) Absent(ctx context.Context) (bool, error) {
	if d.Context == "" || len(d.ID) != 64 {
		return false, errors.New("invalid runtime absence binding")
	}
	out, err := d.command(ctx, "ps", "-aq", "--no-trunc", "--filter", "id="+d.ID)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "", nil
}

func (d *DockerWorkspaceContainer) cleanProbes(ctx context.Context) error {
	hash := sha256.Sum256([]byte(workspaceHeadProbe))
	raw, err := d.command(ctx, "exec", d.ID, "env", "-i", "PATH=/usr/bin:/bin", "python3", "-I", "-S", "-c", workspaceProbeCleanup, hex.EncodeToString(hash[:]))
	if err != nil || strings.TrimSpace(string(raw)) != "clean" {
		return errors.New("probe cleanup unproved")
	}
	return nil
}
