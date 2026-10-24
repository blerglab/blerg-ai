package shellruntime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// RPC travels only over the already authenticated daemon/pod connection. It has
// no path, shell executable, argv, env, container or user supplied by a browser.
type RPC struct {
	Launch      protocol.SupervisorLaunch   `json:"launch,omitzero"`
	Version     int                         `json:"version"`
	Type        string                      `json:"type"`
	SessionID   string                      `json:"session_id"`
	RequestID   string                      `json:"request_id,omitempty"`
	Op          string                      `json:"op"`
	Binding     ownershell.Binding          `json:"binding,omitzero"`
	Identity    Identity                    `json:"identity,omitzero"`
	Key         ed25519.PublicKey           `json:"key,omitempty"`
	Grant       ownershell.SignedGrant      `json:"grant,omitzero"`
	Policy      protocol.ShellPolicy        `json:"policy,omitzero"`
	Consent     *protocol.HostShellConsent  `json:"consent,omitempty"`
	Phase       protocol.SupervisorPhase    `json:"phase,omitempty"`
	FrozenUntil int64                       `json:"frozen_until,omitempty"`
	Cleanup     *protocol.SupervisorCleanup `json:"cleanup,omitempty"`
	Shell       string                      `json:"shell,omitempty"`
	Seq         uint64                      `json:"seq,omitempty"`
	Cols, Rows  uint16
	Data        []byte `json:"data,omitempty"`
	OK          bool   `json:"ok,omitempty"`
}
type registered struct {
	containmentHistory []*Coordinator
	atomicStart        func(context.Context, func() error) error
	opMu               sync.Mutex // serialized commands and retirement, never P2
	output             sync.WaitGroup
	consentDirty       atomic.Bool
	fenced, terminal   bool
	closedShells       map[string]bool
	highestRevision    uint64
	phase              protocol.SupervisorPhase
	retiring           bool
	binding            ownershell.Binding
	command            Command
	mu                 *sync.Mutex
	coordinator        *Coordinator
	// Reconcile is supplied by the runtime owner: prove orphan cleanup BEFORE
	// a restarted service may acknowledge all-launch recovery or create writers.
	reconcile func(context.Context) error
}
type sessionQueue struct {
	done    chan struct{}
	control chan RPC
	data    chan RPC
}
type negotiatedCapabilities struct {
	server, runtime protocol.WorkspaceCapabilities
}

type Service struct {
	capabilities    atomic.Pointer[negotiatedCapabilities]
	resolveIdentity func(string) (Identity, error)
	link            sync.RWMutex
	connection      string
	local           map[string]chan RPC
	done            chan struct{}
	shutdown        sync.Once
	workers         sync.WaitGroup
	mu              sync.Mutex
	launches        map[string]*registered
	queues          map[string]*sessionQueue
	send            func(RPC) error
}

func NewService(send func(RPC) error) *Service {
	return &Service{resolveIdentity: ResolveIdentity, local: map[string]chan RPC{}, done: make(chan struct{}), launches: map[string]*registered{}, queues: map[string]*sessionQueue{}, send: send}
}

// SetCapabilities is a local transport hook, never decoded from shell RPC.
// Lifecycle cleanup remains available after capability loss; shell admission does not.
func (s *Service) SetCapabilities(server, runtime protocol.WorkspaceCapabilities) {
	s.capabilities.Store(&negotiatedCapabilities{server, runtime})
}
func (s *Service) negotiatedBinding(b ownershell.Binding) ownershell.Binding {
	if caps := s.capabilities.Load(); caps != nil {
		b.ServerCapabilities = protocol.NegotiateWorkspaceCapabilities(b.ServerCapabilities, caps.server)
		b.RuntimeCapabilities = protocol.NegotiateWorkspaceCapabilities(b.RuntimeCapabilities, caps.runtime)
	}
	return b
}

// Register is P2/P3's post-prepare binding hook. Reconcile must prove no orphan
// supervisors of this physical incarnation. Never derive this state from RPC.
func (s *Service) Register(ctx context.Context, mu *sync.Mutex, b ownershell.Binding, command Command, reconcile func(context.Context) error) error {
	return s.register(ctx, mu, b, command, reconcile, nil)
}

// RegisterWithAdmission installs the outer workspace admission before making
// the registration visible to the authenticated request worker.
func (s *Service) RegisterWithAdmission(ctx context.Context, mu *sync.Mutex, b ownershell.Binding, command Command, reconcile func(context.Context) error, start func(context.Context, func() error) error) error {
	if start == nil {
		return ErrClosed
	}
	return s.register(ctx, mu, b, command, reconcile, start)
}
func (s *Service) register(ctx context.Context, mu *sync.Mutex, b ownershell.Binding, command Command, reconcile func(context.Context) error, start func(context.Context, func() error) error) error {
	s.link.RLock()
	defer s.link.RUnlock()
	if ProbeCommand(ctx, command) != nil {
		return ErrClosed
	}
	if runtime.GOOS != "linux" || mu == nil || reconcile == nil || ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch.Validate() != nil {
		return ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.launches[b.Session] != nil {
		return ErrClosed
	}
	b.ServerCapabilities = protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1}
	b.RuntimeCapabilities = b.ServerCapabilities
	if s.connection != "" {
		b.Connection = s.connection
		b.Audience = ownershell.RuntimeAudience(s.connection, b.RuntimeGeneration)
	}
	s.launches[b.Session] = &registered{atomicStart: start, closedShells: map[string]bool{}, phase: "running", binding: b, command: command, mu: mu, reconcile: reconcile}
	return nil
}

// Handle does bounded dispatch off the WebSocket read loop. Each session owns
// one command queue, so a slow runtime cannot block another session or agent IO.
func (s *Service) Handle(raw []byte) { s.HandleContext(context.Background(), raw) }
func (s *Service) HandleContext(parent context.Context, raw []byte) {
	if len(raw) > 32768 {
		return
	}
	var r RPC
	if json.Unmarshal(raw, &r) != nil || r.Type != "owner_shell_request" || r.Version != 1 || r.SessionID == "" || len(r.Data) > 16384 {
		return
	}
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		return
	default:
	}
	queue := s.queues[r.SessionID]
	if queue == nil {
		if len(s.queues) >= 128 {
			s.mu.Unlock()
			return
		}
		queue = &sessionQueue{done: make(chan struct{}), control: make(chan RPC, 16), data: make(chan RPC, 16)}
		s.queues[r.SessionID] = queue
		s.workers.Add(1)
		go func() { defer s.workers.Done(); s.worker(context.WithoutCancel(parent), queue) }()
	}
	s.mu.Unlock()
	lane := queue.control
	if r.Op == "input" || r.Op == "resize" {
		lane = queue.data
	}
	select {
	case lane <- r:
	default:
		s.deliver(RPC{Version: 1, Type: "owner_shell_result", SessionID: r.SessionID, RequestID: r.RequestID, Op: r.Op})
	}
}
func (s *Service) worker(parent context.Context, queue *sessionQueue) {
	burst := 0
	for {
		var r RPC
		found := false
		select {
		case <-s.done:
			return
		case <-queue.done:
			return
		default:
		}
		if burst < 4 {
			select {
			case r = <-queue.control:
				found = true
				burst++
			default:
			}
		}
		if !found {
			select {
			case r = <-queue.data:
				found = true
				burst = 0
			default:
			}
		}
		if !found {
			select {
			case r = <-queue.control:
				burst++
			case r = <-queue.data:
				burst = 0
			case <-s.done:
				return
			case <-queue.done:
				return
			}
		}
		ctx, cancel := context.WithTimeout(parent, 2*time.Second)
		s.link.RLock()
		reply := s.execute(ctx, r)
		s.link.RUnlock()
		cancel()
		s.deliver(reply)
	}
}
func (s *Service) execute(ctx context.Context, r RPC) RPC {
	result := RPC{Version: 1, Type: "owner_shell_result", SessionID: r.SessionID, RequestID: r.RequestID, Op: r.Op}
	s.mu.Lock()
	launch := s.launches[r.SessionID]
	unavailable := launch == nil || launch.retiring
	s.mu.Unlock()
	if unavailable {
		return result
	}
	launch.opMu.Lock()
	defer launch.opMu.Unlock()
	s.mu.Lock()
	unavailable = launch.retiring || s.launches[r.SessionID] != launch
	s.mu.Unlock()
	if unavailable {
		return result
	}
	if launch.binding.Runtime == "host" && (r.Op == "describe" || r.Op == "configure" || r.Op == "recover" || launch.consentDirty.Load()) {
		if s.refreshHostIdentity(ctx, launch) != nil {
			return result
		}
	}
	if r.Op == "describe" {
		s.mu.Lock()
		c := launch.coordinator
		s.mu.Unlock()
		result.Binding = launch.binding
		result.Binding.Live = launch.phase == "running" || launch.phase == "syncing"
		if c != nil {
			c.sessionMu.Lock()
			result.Binding = c.binding
			result.Binding.Live = c.phase == "running" || c.phase == "syncing"
			c.sessionMu.Unlock()
		}
		result.Binding = s.negotiatedBinding(result.Binding)
		result.Identity = launch.command.Identity
		result.OK = true
		return result
	}
	expectedLaunch := ownershell.SupervisorIdentity(launch.binding, ownershell.SignedGrant{}).Launch
	if r.Op == "close_shell" && r.Launch != expectedLaunch {
		return result
	}
	if r.Op == "close" || r.Op == "close_shell" {
		id := r.Shell
		if r.Op == "close" {
			id = r.Grant.Grant.ShellGeneration
			g := r.Grant.Grant
			if g.SessionID != expectedLaunch.SessionID || g.ConnectionID != expectedLaunch.ConnectionID || g.LaunchGeneration != expectedLaunch.LaunchGeneration || g.Audience != launch.binding.Audience {
				return result
			}
		}
		if id == "" {
			return result
		}
		launch.closedShells[id] = true
		if launch.coordinator == nil {
			if launch.reconcile(ctx) != nil {
				launch.mu.Lock()
				launch.fenced = true
				launch.mu.Unlock()
				return result
			}
			result.Cleanup = &protocol.SupervisorCleanup{Launch: expectedLaunch, ShellGeneration: id, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
			result.OK = true
			return result
		}
	}
	if r.Op == "open" && launch.closedShells[r.Grant.Grant.ShellGeneration] {
		return result
	}

	if r.Op == "freeze" {
		r.Op = "phase"
		r.Phase = "syncing"
	}
	if r.Op == "phase" || r.Op == "close_launch" || r.Op == "recover" || r.Op == "admit" || r.Op == "close_shell" {
		if r.Launch != expectedLaunch {
			return result
		}
	}
	launch.mu.Lock()
	if r.Op == "phase" {
		if !r.Phase.Valid() {
			launch.mu.Unlock()
			return result
		}
		launch.phase = r.Phase
		if r.Phase != "running" && r.Phase != "syncing" {
			launch.fenced = true
			if r.Phase != "paused" {
				launch.terminal = true
			}
		}
	}
	if r.Op == "close_launch" {
		launch.fenced = true
	}
	blocked := launch.fenced || launch.terminal
	recoverable := !launch.terminal && launch.phase == "running" && launch.binding.Live
	launch.mu.Unlock()
	if (r.Op == "configure" || r.Op == "open" || r.Op == "admit") && blocked {
		return result
	}
	if r.Op == "recover" && !recoverable {
		return result
	}
	if launch.coordinator == nil && (r.Op == "phase" || r.Op == "recover" || r.Op == "close_launch") {
		if r.Op == "phase" {
			result.OK = true
			result.FrozenUntil = time.Now().Add(time.Duration(protocol.DefaultWorkspaceBudget().SyncSeconds) * time.Second).Unix()
			return result
		}
		if launch.reconcile(ctx) != nil {
			launch.mu.Lock()
			launch.fenced = true
			launch.mu.Unlock()
			return result
		}
		if r.Op == "recover" {
			launch.mu.Lock()
			launch.fenced = false
			launch.mu.Unlock()
		}
		result.Cleanup = &protocol.SupervisorCleanup{Launch: expectedLaunch, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
		result.OK = true
		return result
	}
	if r.Op == "configure" || r.Op == "open" {
		b := s.negotiatedBinding(launch.binding)
		if b.ServerCapabilities.ShellVersion != 1 || b.RuntimeCapabilities.ShellVersion != 1 {
			return result
		}
	}
	if r.Op == "configure" {
		if r.Policy.Revision < launch.highestRevision {
			return result
		}
		if launch.phase != "running" {
			return result
		}
		base := r.Binding
		base.Consent = launch.binding.Consent
		base.HostConsentValid = launch.binding.HostConsentValid
		if len(r.Key) != ed25519.PublicKeySize || base != s.negotiatedBinding(launch.binding) || r.Policy.Version != 1 || !r.Policy.Enabled {
			return result
		}
		command := launch.command
		if bindingRuntime := launch.binding.Runtime; bindingRuntime == "host" {
			command.identityInvalidated = func() { launch.consentDirty.Store(true) }
		}
		command.Policy = r.Policy
		command.Consent = r.Consent
		binding := s.negotiatedBinding(launch.binding)
		if binding.Runtime == "host" {
			if r.Consent != nil {
				binding.Consent = r.Consent.ID
			}
			binding.HostConsentValid = consentMatches(r.Consent, binding, command.Identity)
			if !binding.HostConsentValid {
				return result
			}
		}
		if launch.coordinator != nil {
			old := launch.coordinator
			old.sessionMu.Lock()
			eligible := old.phase == "running" && !old.terminal && !old.recovering && !old.fenced && !old.launchClosing.Load()
			old.sessionMu.Unlock()
			if !eligible {
				return result
			}
			if old.key.Equal(r.Key) && old.command.Policy == r.Policy && old.binding == binding {
				result.Binding = binding
				result.OK = true
				return result
			}
			if _, err := old.CloseLaunch(ctx, old.launch(), "reconfigure"); err != nil {
				return result
			}
			old.sessionMu.Lock()
			for id := range old.closed {
				launch.closedShells[id] = true
			}
			old.sessionMu.Unlock()
		}
		command.Recovery = func(call context.Context, l protocol.SupervisorLaunch) error {
			if l != ownershell.SupervisorIdentity(binding, ownershell.SignedGrant{}).Launch {
				return ErrClosed
			}
			return launch.reconcile(call)
		}
		c, err := NewCoordinator(launch.mu, binding, command, r.Key)
		if err != nil {
			return result
		}
		c.Signal = func(p protocol.SupervisorPhase) error {
			return s.send(RPC{Version: 1, Type: "owner_shell_phase", SessionID: r.SessionID, Launch: c.launch(), Phase: p})
		}
		s.mu.Lock()
		if len(launch.containmentHistory) >= 128 {
			s.mu.Unlock()
			return result
		}
		launch.containmentHistory = append(launch.containmentHistory, c)
		s.mu.Unlock()
		if _, err = c.RecoverLaunch(ctx, c.launch()); err != nil {
			return result
		}
		s.mu.Lock()
		if launch.retiring || s.launches[r.SessionID] != launch {
			s.mu.Unlock()
			_, _ = c.CloseLaunch(ctx, c.launch(), "retired_during_setup")
			return result
		}
		launch.coordinator = c
		launch.highestRevision = r.Policy.Revision
		s.mu.Unlock()
		result.Binding = binding
		result.OK = true
		return result
	}
	c := launch.coordinator
	if c == nil {
		return result
	}
	switch r.Op {
	case "close_shell":
		proof, err := c.CloseShell(ctx, protocol.SupervisorShell{Launch: r.Launch, ShellGeneration: r.Shell}, "lifecycle_close")
		if err != nil {
			launch.mu.Lock()
			launch.fenced = true
			launch.mu.Unlock()
		}
		if err == nil {
			result.Cleanup = &proof
			result.OK = true
		}
	case "admit":
		result.OK = c.AdmitShell(ctx, protocol.SupervisorShell{Launch: r.Launch, ShellGeneration: r.Shell}) == nil
	case "open":
		c.sessionMu.Lock()
		reserved := c.reserved[r.Grant.Grant.ShellGeneration]
		c.sessionMu.Unlock()
		var out <-chan []byte
		var err error
		start := func() error {
			if reserved {
				out, err = c.OpenReserved(ctx, ownershell.SupervisorIdentity(c.binding, r.Grant), c.binding, r.Grant, c.key)
			} else {
				out, err = c.Open(ctx, c.binding, r.Grant, c.key)
			}
			return err
		}
		if launch.atomicStart != nil {
			err = launch.atomicStart(ctx, start)
		} else {
			err = start()
		}
		result.OK = err == nil
		if err == nil {
			launch.output.Add(1)
			go func() {
				defer launch.output.Done()
				for data := range out {
					if s.send(RPC{Version: 1, Type: "owner_shell_output", SessionID: r.SessionID, Shell: r.Grant.Grant.ShellGeneration, Data: data}) != nil {
						_, _ = c.CloseShell(context.WithoutCancel(ctx), ownershell.SupervisorIdentity(c.binding, r.Grant), "transport_lost")
						return
					}
				}
			}()
		}
	case "renew":
		result.OK = c.Renew(ctx, r.Grant) == nil
	case "input":
		result.OK = c.Input(ctx, r.Grant, r.Seq, r.Data) == nil
	case "resize":
		result.OK = c.Resize(ctx, r.Grant, r.Seq, r.Cols, r.Rows) == nil
	case "phase":
		result.OK = c.Notify(ctx, c.launch(), r.Phase) == nil
		if result.OK && r.Phase == "syncing" {
			result.FrozenUntil = c.FrozenUntil().Unix()
		}
	case "close":
		proof, err := c.CloseShell(ctx, ownershell.SupervisorIdentity(c.binding, r.Grant), "revoked")
		if err != nil {
			launch.mu.Lock()
			launch.fenced = true
			launch.mu.Unlock()
		}
		if err == nil {
			result.Cleanup = &proof
			result.OK = true
		}
	case "close_launch":
		proof, err := c.CloseLaunch(ctx, c.launch(), "runtime_stop")
		if err == nil {
			err = joinShellOutput(ctx, launch)
		}
		if err == nil {
			result.Cleanup = &proof
			result.OK = true
		}
	case "recover":
		proof, err := c.RecoverLaunch(ctx, c.launch())
		if err == nil {
			err = joinShellOutput(ctx, launch)
		}
		if err == nil {
			launch.mu.Lock()
			launch.fenced = false
			launch.mu.Unlock()
			result.Cleanup = &proof
			result.OK = true
		}
	}
	return result
}

// Called by the serialized session worker. Rotate the authenticated launch nonce
// on any observed transition, even A -> B -> A seen only by a running helper.
func (s *Service) refreshHostIdentity(ctx context.Context, launch *registered) error {
	publicRoot := launch.command.PublicRoot
	if publicRoot == "" {
		publicRoot = launch.command.Identity.Cwd
	}
	current, err := s.resolveIdentity(launch.command.Identity.Cwd)
	if err != nil || !matchesRoot(publicRoot, launch.binding.Root) {
		launch.consentDirty.Store(true)
		return ErrClosed
	}
	if current == launch.command.Identity && !launch.consentDirty.Load() {
		return nil
	}
	launch.mu.Lock()
	launch.fenced = true
	launch.mu.Unlock()
	if c := launch.coordinator; c != nil {
		if _, err := c.CloseLaunch(ctx, c.launch(), "identity_changed"); err != nil {
			return err
		}
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrClosed
	}
	s.mu.Lock()
	launch.mu.Lock()
	launch.command.Identity = current
	launch.binding.Connection = hex.EncodeToString(nonce[:])
	launch.binding.Audience = ownershell.RuntimeAudience(launch.binding.Connection, launch.binding.RuntimeGeneration)
	launch.binding.Consent = ""
	launch.binding.HostConsentValid = false
	launch.coordinator = nil
	launch.consentDirty.Store(false)
	launch.mu.Unlock()
	s.mu.Unlock()
	return nil
}

// Lifecycle returns P0b's actual supervisor to P2/P3 for synchronous phase and
// storage settlement calls, avoiding an unrelated mutex or a browser receipt.
func (s *Service) Lifecycle(session string) protocol.RuntimeSupervisor {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.launches[session]; entry != nil {
		return nativeSupervisor{service: s, launch: ownershell.SupervisorIdentity(entry.binding, ownershell.SignedGrant{}).Launch}
	}
	return nil
}
func (s *Service) deliver(reply RPC) {
	s.mu.Lock()
	wait := s.local[reply.RequestID]
	delete(s.local, reply.RequestID)
	s.mu.Unlock()
	if wait != nil {
		wait <- reply
	} else {
		_ = s.send(reply)
	}
}
func (s *Service) localCall(ctx context.Context, request RPC) (RPC, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return RPC{}, ErrClosed
	}
	request.Type = "owner_shell_request"
	request.Version = 1
	request.RequestID = hex.EncodeToString(nonce[:])
	reply := make(chan RPC, 1)
	s.mu.Lock()
	if len(s.local) >= 256 {
		s.mu.Unlock()
		return RPC{}, ErrClosed
	}
	s.local[request.RequestID] = reply
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.local, request.RequestID); s.mu.Unlock() }()
	raw, err := json.Marshal(request)
	if err != nil {
		return RPC{}, ErrClosed
	}
	s.HandleContext(context.WithoutCancel(ctx), raw)
	select {
	case response := <-reply:
		if !response.OK {
			return response, ErrClosed
		}
		return response, nil
	case <-ctx.Done():
		return RPC{}, ctx.Err()
	case <-s.done:
		return RPC{}, ErrClosed
	}
}

type nativeSupervisor struct {
	service *Service
	launch  protocol.SupervisorLaunch
}

func (n nativeSupervisor) AdmitShell(ctx context.Context, s protocol.SupervisorShell) error {
	if s.Launch != n.launch || s.Validate() != nil {
		return ErrClosed
	}
	_, err := n.service.localCall(ctx, RPC{SessionID: s.SessionID, Op: "admit", Launch: s.Launch, Shell: s.ShellGeneration})
	return err
}
func (n nativeSupervisor) Notify(ctx context.Context, l protocol.SupervisorLaunch, p protocol.SupervisorPhase) error {
	if l != n.launch || !p.Valid() {
		return ErrClosed
	}
	_, err := n.service.localCall(ctx, RPC{SessionID: l.SessionID, Op: "phase", Launch: l, Phase: p})
	return err
}

// Freeze fences shell admission before returning the earliest current lease.
// Renewal during sync cannot extend this deadline: the unfrozen owner must kill
// the runtime if copy/thaw cannot finish while every paused helper remains valid.
func (n nativeSupervisor) Freeze(ctx context.Context, l protocol.SupervisorLaunch) (time.Time, error) {
	if l != n.launch {
		return time.Time{}, ErrClosed
	}
	r, err := n.service.localCall(ctx, RPC{SessionID: l.SessionID, Op: "freeze", Launch: l})
	if err != nil || r.FrozenUntil == 0 {
		return time.Time{}, ErrClosed
	}
	return time.Unix(r.FrozenUntil, 0), nil
}
func (n nativeSupervisor) CloseShell(ctx context.Context, s protocol.SupervisorShell, _ string) (protocol.SupervisorCleanup, error) {
	if s.Launch != n.launch {
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	r, err := n.service.localCall(ctx, RPC{SessionID: s.SessionID, Op: "close_shell", Launch: s.Launch, Shell: s.ShellGeneration})
	if err != nil || r.Cleanup == nil {
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	return *r.Cleanup, r.Cleanup.ValidateFor(s.Launch, s.ShellGeneration)
}
func (n nativeSupervisor) CloseLaunch(ctx context.Context, l protocol.SupervisorLaunch, _ string) (protocol.SupervisorCleanup, error) {
	return n.proof(ctx, l, "close_launch")
}
func (n nativeSupervisor) RecoverLaunch(ctx context.Context, l protocol.SupervisorLaunch) (protocol.SupervisorCleanup, error) {
	return n.proof(ctx, l, "recover")
}
func (n nativeSupervisor) proof(ctx context.Context, l protocol.SupervisorLaunch, op string) (protocol.SupervisorCleanup, error) {
	if l != n.launch {
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	r, err := n.service.localCall(ctx, RPC{SessionID: l.SessionID, Op: op, Launch: l})
	if err != nil || r.Cleanup == nil {
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	return *r.Cleanup, r.Cleanup.ValidateFor(l, "")
}

// Shutdown first joins bounded command workers, then tears down every owned
// launch. Expiry/reaping lives in helpers even if the caller stops waiting.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.shutdown.Do(func() { close(s.done) })
	s.mu.Unlock()
	complete := make(chan error, 1)
	go func() {
		s.workers.Wait()
		s.mu.Lock()
		var coordinators []*Coordinator
		for _, launch := range s.launches {
			if launch.coordinator != nil {
				coordinators = append(coordinators, launch.coordinator)
			}
		}
		s.mu.Unlock()
		var failure error
		for _, c := range coordinators {
			call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			_, err := c.CloseLaunch(call, c.launch(), "runtime_shutdown")
			cancel()
			if err != nil && failure == nil {
				failure = err
			}
		}
		complete <- failure
	}()
	select {
	case err := <-complete:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Retire is called before P2/P3 replaces or deletes an incarnation. It obtains
// P0b's all-shell proof before removing registration; failure retains it.
func (s *Service) Retire(ctx context.Context, l protocol.SupervisorLaunch) (protocol.SupervisorCleanup, error) {
	s.link.RLock()
	defer s.link.RUnlock()
	s.mu.Lock()
	entry := s.launches[l.SessionID]
	if entry == nil || ownershell.SupervisorIdentity(entry.binding, ownershell.SignedGrant{}).Launch != l {
		s.mu.Unlock()
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	s.mu.Unlock()
	entry.opMu.Lock()
	defer entry.opMu.Unlock()
	s.mu.Lock()
	entry.retiring = true
	coordinator := entry.coordinator
	s.mu.Unlock()
	if coordinator == nil {
		if entry.reconcile(ctx) != nil {
			return protocol.SupervisorCleanup{}, ErrClosed
		}
	} else {
		if err := coordinator.Notify(ctx, l, "replacing"); err != nil {
			return protocol.SupervisorCleanup{}, err
		}
		proof, err := coordinator.CloseLaunch(ctx, l, "retire")
		if err != nil || proof.ValidateFor(l, "") != nil {
			return protocol.SupervisorCleanup{}, ErrClosed
		}
	}
	if err := joinShellOutput(ctx, entry); err != nil {
		return protocol.SupervisorCleanup{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.launches[l.SessionID] != entry {
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	delete(s.launches, l.SessionID)
	if queue := s.queues[l.SessionID]; queue != nil {
		close(queue.done)
		delete(s.queues, l.SessionID)
	}
	return protocol.SupervisorCleanup{Launch: l, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}, nil
}

// Rebind is invoked locally before each new authenticated socket hello. It
// destroys old helpers before assigning a fresh connection nonce; no old grant,
// consent, key or controlling attachment transfers to the new connection.
func (s *Service) Rebind(ctx context.Context) error {
	s.link.Lock()
	defer s.link.Unlock()
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrClosed
	}
	s.mu.Lock()
	var entries []*registered
	for _, entry := range s.launches {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	slots := make(chan struct{}, 8)
	var wg sync.WaitGroup
	failures := make(chan error, len(entries))
	for _, entry := range entries {
		if entry.coordinator == nil {
			continue
		}
		slots <- struct{}{}
		wg.Add(1)
		go func(entry *registered) {
			defer wg.Done()
			defer func() { <-slots }()
			if _, err := entry.coordinator.CloseLaunch(ctx, entry.coordinator.launch(), "connection_replaced"); err != nil {
				failures <- err
			} else if err := joinShellOutput(ctx, entry); err != nil {
				failures <- err
			}
		}(entry)
	}
	wg.Wait()
	close(failures)
	if len(failures) > 0 {
		return ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connection = hex.EncodeToString(nonce[:])
	for _, entry := range entries {
		if entry.coordinator != nil {
			entry.coordinator.sessionMu.Lock()
			entry.phase = entry.coordinator.phase
			for id := range entry.coordinator.closed {
				entry.closedShells[id] = true
			}
			entry.coordinator.sessionMu.Unlock()
		}
		entry.coordinator = nil
		entry.binding.Connection = s.connection
		entry.binding.Audience = ownershell.RuntimeAudience(s.connection, entry.binding.RuntimeGeneration)
		if entry.binding.Runtime == "host" {
			entry.binding.Consent = ""
			entry.binding.HostConsentValid = false
		}
	}
	return nil
}
func (s *Service) Binding(session string) (ownershell.Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.launches[session]
	if entry == nil {
		return ownershell.Binding{}, ErrClosed
	}
	return s.negotiatedBinding(entry.binding), nil
}

func joinShellOutput(ctx context.Context, entry *registered) error {
	done := make(chan struct{})
	go func() { entry.output.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
