//go:build linux

package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"sync"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"golang.org/x/sys/unix"
)

const clusterPrivateRoot = "/run/blerg-workspace-private"
const clusterSupervisorUID = 1001

// ConfigureClusterRuntime is called by the shipped pod entry point. Storage
// and identities are fixed by the image+Job, not an environment path. The
// supervisor UID has only the capabilities needed to trace/drop to agent UID
// 1000 and access that user's checkout. Agent exec clears all capabilities and
// inherits no-new-privileges plus the containment filter.
func ConfigureClusterRuntime(cfg *Config) error {
	if cfg.Workspace == nil {
		return cfg.WorkspaceErr
	}
	if os.Geteuid() != clusterSupervisorUID || os.Getpid() != 1 || cfg.Home != "/workspace" {
		return clusterworkspace.ErrPlan
	}
	fd, err := unix.Open(clusterPrivateRoot, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return clusterworkspace.ErrPlan
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != clusterSupervisorUID || st.Mode&0777 != 0700 {
		return clusterworkspace.ErrPlan
	}
	// Require the deployment's separate mount, not a same-filesystem directory
	// supplied through HOME or a rewritten environment variable.
	home, err := os.Open(cfg.Home)
	if err != nil {
		return clusterworkspace.ErrPlan
	}
	defer func() { _ = home.Close() }()
	var privateMount, homeMount unix.Statx_t
	if unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID, &privateMount) != nil || unix.Statx(int(home.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID, &homeMount) != nil || privateMount.Mask&unix.STATX_MNT_ID == 0 || homeMount.Mask&unix.STATX_MNT_ID == 0 || privateMount.Mnt_id == homeMount.Mnt_id {
		return clusterworkspace.ErrPlan
	}
	var caps unix.CapUserData
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	pair := [2]unix.CapUserData{}
	if unix.Capget(&header, &pair[0]) != nil {
		return clusterworkspace.ErrPlan
	}
	caps = pair[0]
	for _, bit := range []uint{unix.CAP_CHOWN, unix.CAP_DAC_OVERRIDE, unix.CAP_SETUID, unix.CAP_SETGID, unix.CAP_KILL, unix.CAP_SYS_PTRACE} {
		if caps.Effective&(1<<bit) == 0 {
			return clusterworkspace.ErrPlan
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	group := processcontainment.NewGroup(executable, 1000, 1000)
	var nonce [24]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	pod := &podRuntime{group: group, launch: protocol.SupervisorLaunch{SessionID: cfg.SessionID, LaunchGeneration: cfg.Workspace.Runtime.LaunchGeneration, RuntimeGeneration: cfg.Workspace.Runtime.Generation, Runtime: "cluster", ConnectionID: hex.EncodeToString(nonce[:])}}
	cfg.ClusterPrivateRoot = clusterPrivateRoot
	cfg.ClusterWriters = pod
	cfg.StartCommand = func(cmd *exec.Cmd) error {
		if err := projectClusterLaunchFiles(cmd); err != nil {
			return err
		}
		return group.Start(cmd)
	}
	cfg.Supervisor = pod
	cfg.SupervisorLaunch = pod.launch
	cfg.ClusterAttach = func(ctx context.Context, client *daemon.WSClient) error {
		pod.service = client.OwnerShellRuntimeContext(ctx)
		return nil
	}
	homePath := cfg.Home
	child := cfg.RequireRecoveryReady
	cfg.ClusterServices = func(public, private string, m protocol.WorkspaceManifest, b protocol.WorkspaceRuntimeBinding, rt *clusterworkspace.Runtime) (ClusterServices, error) {
		if private != clusterPrivateRoot || pod.service == nil {
			return nil, clusterworkspace.ErrPlan
		}
		// Before the first untrusted program: transfer only the prepared HOME and
		// checkout, without following links, never the private mount or descriptors.
		home, err := os.OpenRoot(homePath)
		if err != nil {
			return nil, err
		}
		err = fs.WalkDir(home.FS(), ".", func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			return home.Lchown(path, 1000, 1000)
		})
		_ = home.Close()
		if err != nil {
			return nil, err
		}
		services, err := newPodServices(public, m, b, pod.service)
		if err != nil {
			return nil, err
		}
		identity, err := shellruntime.ResolveClusterIdentity(public)
		if err != nil {
			_ = services.Close(context.Background())
			return nil, err
		}
		binding := ownershell.Binding{Child: child, Session: pod.launch.SessionID, Owner: m.OwnerID, Launch: m.LaunchGeneration, Connection: pod.launch.ConnectionID, RuntimeGeneration: b.Generation, Audience: ownershell.RuntimeAudience(pod.launch.ConnectionID, b.Generation), Runtime: "cluster", Root: b.Root, ManifestDigest: m.Digest, Live: true}
		command := shellruntime.Command{Executable: executable, Args: []string{"cluster-shell-supervisor"}, PublicRoot: public, Root: b.Root, Identity: identity}
		if err = pod.service.RegisterWithAdmission(context.Background(), &sync.Mutex{}, binding, command, func(context.Context) error { return pod.service.ProveRecordedShellsEmpty(binding.Session) }, func(ctx context.Context, start func() error) error {
			services.mu.Lock()
			closed := services.closed
			services.mu.Unlock()
			if closed {
				return clusterworkspace.ErrPlan
			}
			return rt.StartWriter(ctx, start)
		}); err != nil {
			_ = services.Close(context.Background())
			return nil, err
		}
		pod.mu.Lock()
		pod.readers = services
		pod.mu.Unlock()
		return services, nil
	}
	return nil
}

func ServeClusterWriter(args []string) (bool, int) {
	if len(args) > 0 && args[0] == "cluster-writer" {
		return true, processcontainment.ServeCommand(args[1:])
	}
	return false, 0
}

type podRuntime struct {
	mu      sync.Mutex
	group   *processcontainment.Group
	service *shellruntime.Service
	readers *podServices
	launch  protocol.SupervisorLaunch
}

func (p *podRuntime) Freeze(ctx context.Context) error {
	p.mu.Lock()
	readers := p.readers
	p.mu.Unlock()
	if readers != nil {
		if err := readers.pause(ctx); err != nil {
			return err
		}
	}
	if p.service != nil {
		if err := p.service.ProcessPhase(ctx, p.launch.SessionID, true); err != nil {
			return err
		}
	}
	return p.group.Freeze(ctx)
}
func (p *podRuntime) Thaw(ctx context.Context) error {
	if err := p.group.Thaw(ctx); err != nil {
		return err
	}
	if p.service != nil {
		if err := p.service.ProcessPhase(ctx, p.launch.SessionID, false); err != nil {
			return err
		}
	}
	p.mu.Lock()
	if p.readers != nil {
		p.readers.resume()
	}
	p.mu.Unlock()
	return nil
}
func (p *podRuntime) Stop(ctx context.Context) error { return p.group.Stop(ctx) }
func (p *podRuntime) lifecycle() (protocol.RuntimeSupervisor, protocol.SupervisorLaunch) {
	p.mu.Lock()
	bound := p.readers != nil
	p.mu.Unlock()
	if !bound {
		return nil, p.launch
	}
	if p.service == nil {
		return nil, protocol.SupervisorLaunch{}
	}
	b, err := p.service.Binding(p.launch.SessionID)
	if err != nil {
		return nil, protocol.SupervisorLaunch{}
	}
	return p.service.Lifecycle(p.launch.SessionID), ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch
}
func (p *podRuntime) Notify(ctx context.Context, l protocol.SupervisorLaunch, phase protocol.SupervisorPhase) error {
	if l != p.launch {
		return clusterworkspace.ErrPlan
	}
	s, current := p.lifecycle()
	if s == nil {
		if current != p.launch {
			return clusterworkspace.ErrPlan
		}
		return nil
	}
	return s.Notify(ctx, current, phase)
}
func (p *podRuntime) AdmitShell(ctx context.Context, shell protocol.SupervisorShell) error {
	s, current := p.lifecycle()
	if s == nil || shell.Launch != current {
		return clusterworkspace.ErrPlan
	}
	return s.AdmitShell(ctx, shell)
}
func (p *podRuntime) CloseShell(ctx context.Context, shell protocol.SupervisorShell, reason string) (protocol.SupervisorCleanup, error) {
	s, current := p.lifecycle()
	if s == nil || shell.Launch != current {
		return protocol.SupervisorCleanup{}, clusterworkspace.ErrPlan
	}
	return s.CloseShell(ctx, shell, reason)
}
func (p *podRuntime) proof(ctx context.Context, l protocol.SupervisorLaunch, recovering bool) (protocol.SupervisorCleanup, error) {
	if l != p.launch {
		return protocol.SupervisorCleanup{}, clusterworkspace.ErrPlan
	}
	s, current := p.lifecycle()
	if s == nil {
		if current != p.launch {
			return protocol.SupervisorCleanup{}, clusterworkspace.ErrPlan
		}
		return protocol.SupervisorCleanup{Launch: l, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}, nil
	}
	var proof protocol.SupervisorCleanup
	var err error
	if recovering {
		proof, err = s.RecoverLaunch(ctx, current)
	} else {
		proof, err = s.CloseLaunch(ctx, current, "workspace stopping")
	}
	if err != nil || proof.ValidateFor(current, "") != nil {
		return protocol.SupervisorCleanup{}, clusterworkspace.ErrPlan
	}
	proof.Launch = l
	return proof, nil
}
func (p *podRuntime) RecoverLaunch(ctx context.Context, l protocol.SupervisorLaunch) (protocol.SupervisorCleanup, error) {
	return p.proof(ctx, l, true)
}
func (p *podRuntime) CloseLaunch(ctx context.Context, l protocol.SupervisorLaunch, _ string) (protocol.SupervisorCleanup, error) {
	return p.proof(ctx, l, false)
}

type podServices struct {
	mu              sync.Mutex
	explorer        *daemon.LocalExplorer
	shell           *shellruntime.Service
	active          map[uint64]context.CancelFunc
	next            uint64
	changed         chan struct{}
	paused, closed  bool
	resourcesClosed bool
	closeErr        error
}

func newPodServices(public string, m protocol.WorkspaceManifest, b protocol.WorkspaceRuntimeBinding, s *shellruntime.Service) (*podServices, error) {
	p := &podServices{shell: s, active: map[uint64]context.CancelFunc{}, changed: make(chan struct{})}
	e, err := daemon.NewLocalExplorer(public, m, b, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return !p.closed && !p.paused })
	if err != nil {
		return nil, err
	}
	p.explorer = e
	return p, nil
}
func (p *podServices) reader(parent context.Context) (context.Context, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paused || p.closed || len(p.active) >= 2 {
		return nil, nil, clusterworkspace.ErrPlan
	}
	ctx, cancel := context.WithCancel(parent)
	p.next++
	id := p.next
	p.active[id] = cancel
	return ctx, func() {
		cancel()
		p.mu.Lock()
		delete(p.active, id)
		close(p.changed)
		p.changed = make(chan struct{})
		p.mu.Unlock()
	}, nil
}
func (p *podServices) Explore(ctx context.Context, q protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	read, done, err := p.reader(ctx)
	if err != nil {
		return protocol.WorkspaceExplorerResult{Type: "workspace_explorer_result", RequestID: q.RequestID, SessionID: q.SessionID, WorkspaceRuntimeRef: q.WorkspaceRuntimeRef, ErrorCode: "unavailable"}
	}
	defer done()
	return p.explorer.Explore(read, q)
}
func (p *podServices) ReadFile(ctx context.Context, q protocol.ReadWorkspaceFile) protocol.WorkspaceFileResult {
	read, done, err := p.reader(ctx)
	if err != nil {
		return protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: q.RequestID, SessionID: q.SessionID, WorkspaceRuntimeRef: q.WorkspaceRuntimeRef, ErrorCode: "unavailable"}
	}
	defer done()
	return p.explorer.ReadFile(read, q)
}
func (p *podServices) Explorer(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var q protocol.WorkspaceExplorerRequest
	if json.Unmarshal(raw, &q) != nil {
		return nil, clusterworkspace.ErrPlan
	}
	return json.Marshal(p.Explore(ctx, q))
}
func (p *podServices) Shell(ctx context.Context, raw json.RawMessage, emit func(json.RawMessage) error) error {
	var q shellruntime.RPC
	if p.shell == nil || json.Unmarshal(raw, &q) != nil || q.Version != 1 {
		return clusterworkspace.ErrPlan
	}
	reply, err := p.shell.Call(ctx, q)
	if err != nil {
		return err
	}
	data, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	return emit(data)
}
func (p *podServices) Cancel() {
	p.mu.Lock()
	for _, cancel := range p.active {
		cancel()
	}
	p.mu.Unlock()
}
func (p *podServices) pause(ctx context.Context) error {
	p.mu.Lock()
	p.paused = true
	for _, cancel := range p.active {
		cancel()
	}
	for len(p.active) > 0 {
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		p.mu.Lock()
	}
	p.mu.Unlock()
	return nil
}
func (p *podServices) resume() {
	p.mu.Lock()
	if !p.closed {
		p.paused = false
	}
	p.mu.Unlock()
}
func (p *podServices) Close(ctx context.Context) error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	if err := p.pause(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.resourcesClosed {
		return p.closeErr
	}
	p.resourcesClosed = true
	p.closeErr = p.explorer.Close()
	return p.closeErr
}

var _ clusterworkspace.Writers = (*podRuntime)(nil)
var _ protocol.RuntimeSupervisor = (*podRuntime)(nil)
var _ daemon.WorkspaceExplorer = (*podServices)(nil)

// A non-nil factory alone is not installed services. The exact writer and P5
// owner must have been attached to the outbound client before advertising.
func podCapabilityServicesReady(cfg Config) bool {
	pod, ok := cfg.Supervisor.(*podRuntime)
	return ok && pod != nil && pod.group != nil && pod.service != nil && cfg.ClusterWriters == pod && cfg.ClusterPrivateRoot == clusterPrivateRoot
}
