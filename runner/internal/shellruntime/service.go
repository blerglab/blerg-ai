package shellruntime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
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
	StartCause  string                      `json:"start_cause,omitempty"`
	ErrorCode   string                      `json:"error_code,omitempty"`
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
	// Window is server→daemon output flow control. The server offers it on
	// open; a daemon that supports it echoes it in the reply, sends at most that
	// many output bytes before the server returns "credit" (Seq bytes), and an
	// older peer ignores the field.
	Window uint32 `json:"window,omitempty"`
}

// maxServerWindow bounds a window the server asks for.
const maxServerWindow = 1 << 20

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
	// credits holds each open shell's server window, keyed by session and shell.
	credits map[string]*outputCredit
	// senders holds each open shell's output sender, keyed like credits, so a
	// single-shell cleanup can stop and join exactly that shell's output.
	senders map[string]*shellSender
	// sendContext, when set, carries shell output with the shell's lifetime,
	// so a shell that ends cancels its queued or in-flight frame.
	sendContext func(context.Context, RPC) error
}

// SetOutputSender installs a cancelable transport for shell output. Without
// one, output uses send and a finished shell still stops before its next frame.
func (s *Service) SetOutputSender(send func(context.Context, RPC) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendContext = send
}

func (s *Service) outputSender() func(context.Context, RPC) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendContext != nil {
		return s.sendContext
	}
	return func(_ context.Context, r RPC) error { return s.send(r) }
}

func creditKey(session, shell string) string { return session + "\x00" + shell }

// shellSender is one shell's output sender: stop cancels its lifetime, and
// done closes once it has returned, including any frame it had committed to
// the transport (bounded by the transport's own write deadline).
type shellSender struct {
	stop context.CancelFunc
	done chan struct{}
}

// finishShellOutput stops one shell's sender and joins it before a cleanup
// proof is published, so no output for the shell can follow the proof on any
// path (the wire receipt or a native lifecycle caller). Other shells are not
// waited on. It fails, and the caller refuses the proof, if ctx ends first.
func (s *Service) finishShellOutput(ctx context.Context, session, shell string) error {
	s.mu.Lock()
	sender := s.senders[creditKey(session, shell)]
	s.mu.Unlock()
	if sender == nil {
		return nil
	}
	sender.stop()
	select {
	case <-sender.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func NewService(send func(RPC) error) *Service {
	return &Service{resolveIdentity: ResolveIdentity, local: map[string]chan RPC{}, done: make(chan struct{}), launches: map[string]*registered{}, queues: map[string]*sessionQueue{}, send: send, credits: map[string]*outputCredit{}, senders: map[string]*shellSender{}}
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

// Native containment runs only on Linux. A Darwin desktop may coordinate the
// fixed Linux helper inside its sandbox; no native tracing happens in Service.
// Keep registration available for cleanup even when a release switch is OFF:
// its empty capabilities still refuse configure/open.
func servicePlatformSupported(runtime, platform string) bool {
	if runtime != "host" && runtime != "sandbox" && runtime != "cluster" {
		return false
	}
	return platform == "linux" || platform == "darwin" && runtime == "sandbox"
}

func (s *Service) register(ctx context.Context, mu *sync.Mutex, b ownershell.Binding, command Command, reconcile func(context.Context) error, start func(context.Context, func() error) error) error {
	s.link.RLock()
	defer s.link.RUnlock()
	if ProbeCommand(ctx, command) != nil {
		return ErrClosed
	}
	if !servicePlatformSupported(b.Runtime, runtime.GOOS) || mu == nil || reconcile == nil || ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch.Validate() != nil {
		return ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.launches[b.Session] != nil {
		return ErrClosed
	}
	b.ServerCapabilities = protocol.WorkspaceCapabilitiesFor(b.Runtime, runtime.GOOS)
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
	// Credit only lets this daemon send the server more of a shell's output.
	// It carries no authority and gets no reply, so it is applied here, off
	// the session's command queue, and can never wait behind a slow command.
	if r.Op == "credit" {
		s.mu.Lock()
		credit := s.credits[creditKey(r.SessionID, r.Shell)]
		s.mu.Unlock()
		if credit != nil {
			credit.add(r.Seq)
		}
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
func (s *Service) execute(ctx context.Context, r RPC) (result RPC) {
	result = RPC{Version: 1, Type: "owner_shell_result", SessionID: r.SessionID, RequestID: r.RequestID, Op: r.Op, ErrorCode: string(ownershell.ReasonOpenRuntime)}
	defer func() {
		if result.OK {
			result.ErrorCode = ""
			return
		}
		op := "unknown"
		switch r.Op {
		case "describe", "configure", "open", "admit", "recover", "close", "close_shell", "close_launch", "phase", "freeze", "renew", "input", "resize":
			op = r.Op
		}
		log.Printf("owner shell refused: op=%s reason=%s", op, result.ErrorCode)
	}()
	s.mu.Lock()
	launch := s.launches[r.SessionID]
	unavailable := launch == nil || launch.retiring
	s.mu.Unlock()
	if unavailable {
		result.ErrorCode = string(ownershell.ReasonRuntimeBinding)
		return result
	}
	launch.opMu.Lock()
	defer launch.opMu.Unlock()
	s.mu.Lock()
	unavailable = launch.retiring || s.launches[r.SessionID] != launch
	s.mu.Unlock()
	if unavailable {
		result.ErrorCode = string(ownershell.ReasonRuntimeBinding)
		return result
	}
	if launch.command.SourceLive != nil && sourceChecked(r.Op) && !launch.command.SourceLive() {
		// The attached source changed under a running launch. Refuse, and make the
		// next host describe rotate the connection, so restoring the source cannot
		// restore the old shell's authority.
		launch.consentDirty.Store(true)
		result.ErrorCode = string(ownershell.ReasonRuntimeIdentity)
		return result
	}
	if launch.binding.Runtime == "host" && (r.Op == "describe" || r.Op == "configure" || r.Op == "recover" || launch.consentDirty.Load()) {
		if err := s.refreshHostIdentity(ctx, launch); err != nil {
			result.ErrorCode = string(ownershell.RefusalReason(err))
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
	// Identity rotation retires the old coordinator before replacing its nonce.
	// The owner transport still needs that exact completed receipt to retire its
	// old handle. A tombstone grants no admission, input, or renewed lease.
	if r.Op == "close" || r.Op == "close_shell" {
		for _, old := range launch.containmentHistory {
			id := r.Shell
			if r.Op == "close" {
				g, b := r.Grant.Grant, old.binding
				if g.SessionID != b.Session || g.OwnerID != b.Owner || g.Runtime != b.Runtime || g.LaunchGeneration != b.Launch || g.ConnectionID != b.Connection || g.Audience != b.Audience || g.ConsentID != b.Consent {
					continue
				}
				id = g.ShellGeneration
			} else if r.Launch != old.launch() {
				continue
			}
			old.sessionMu.Lock()
			proof, ok := old.closed[id]
			old.sessionMu.Unlock()
			if ok && proof.ValidateFor(old.launch(), id) == nil {
				// A cached proof is still a proof: the shell's output sender must be
				// stopped and joined first (an earlier attempt may have run out of
				// time for that join). Per shell; refused if this deadline passes too.
				if s.finishShellOutput(ctx, r.SessionID, id) != nil {
					return result
				}
				result.Cleanup, result.OK = &proof, true
				return result
			}
		}
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
		result.ErrorCode = string(ownershell.ReasonRuntimeBinding)
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
	recoverable := !launch.terminal && (launch.phase == "running" || launch.phase == "syncing") && launch.binding.Live
	launch.mu.Unlock()
	if (r.Op == "configure" || r.Op == "open" || r.Op == "admit") && blocked {
		result.ErrorCode = string(ownershell.ReasonRuntimeFenced)
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
			launch.phase = "running"
			launch.mu.Unlock()
		}
		result.Cleanup = &protocol.SupervisorCleanup{Launch: expectedLaunch, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
		result.OK = true
		return result
	}
	if r.Op == "configure" || r.Op == "open" {
		b := s.negotiatedBinding(launch.binding)
		if !b.ServerCapabilities.Supports(b.Runtime, true) || !b.RuntimeCapabilities.Supports(b.Runtime, true) {
			result.ErrorCode = string(ownershell.ReasonRuntimeCapability)
			return result
		}
	}
	if r.Op == "configure" {
		result.ErrorCode = string(ownershell.ReasonRuntimePolicy)
		if r.Policy.Revision < launch.highestRevision {
			return result
		}
		if launch.phase != "running" {
			return result
		}
		result.ErrorCode = string(ownershell.ReasonRuntimeBinding)
		base := r.Binding
		base.Consent = launch.binding.Consent
		base.HostConsentValid = launch.binding.HostConsentValid
		if len(r.Key) != ed25519.PublicKeySize || base != s.negotiatedBinding(launch.binding) || r.Policy.Version != 1 || !r.Policy.Enabled {
			return result
		}
		command := launch.command
		command.diagnostic = func(shell string, err error) {
			diagnostic, cause := startDiagnostic(err)
			log.Printf("owner shell control failed: error=%q", diagnostic)
			_ = s.send(RPC{Version: 1, Type: "owner_shell_output", SessionID: r.SessionID, Shell: shell, StartCause: cause})
		}
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
				result.ErrorCode = string(ownershell.ReasonRuntimeConsent)
				return result
			}
		}
		result.ErrorCode = string(ownershell.ReasonRuntimeCleanup)
		if launch.coordinator != nil {
			old := launch.coordinator
			old.sessionMu.Lock()
			eligible := old.phase == "running" && !old.terminal && !old.recovering && !old.fenced && !old.launchClosing.Load()
			old.sessionMu.Unlock()
			if !eligible {
				result.ErrorCode = string(ownershell.ReasonRuntimeFenced)
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
		result.ErrorCode = string(ownershell.ReasonRuntimeBinding)
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
		result.ErrorCode = string(ownershell.ReasonRuntimeCleanup)
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
		result.ErrorCode = string(ownershell.ReasonRuntimeBinding)
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
		// The proof also covers output: join this shell's sender first.
		if err == nil && s.finishShellOutput(ctx, r.SessionID, r.Shell) == nil {
			result.Cleanup = &proof
			result.OK = true
		}
	case "admit":
		result.OK = c.AdmitShell(ctx, protocol.SupervisorShell{Launch: r.Launch, ShellGeneration: r.Shell}) == nil
	case "open":
		result.ErrorCode = string(ownershell.ReasonRuntimeBinding)
		c.sessionMu.Lock()
		reserved := c.reserved[r.Grant.Grant.ShellGeneration]
		c.sessionMu.Unlock()

		var out <-chan []byte
		var err error
		start := func() error {
			result.ErrorCode = string(ownershell.ReasonRuntimeStart)
			if reserved {
				out, err = c.OpenReserved(ctx, ownershell.SupervisorIdentity(c.binding, r.Grant), c.binding, r.Grant, c.key)
			} else {
				out, err = c.Open(ctx, c.binding, r.Grant, c.key)
			}
			return err
		}
		if launch.atomicStart != nil {
			err = launch.atomicStart(ctx, start)
			if err != nil {
				var staged *startError
				if !errors.As(err, &staged) {
					err = stageError("workspace_admission", err)
					result.ErrorCode = string(ownershell.ReasonRuntimeStart)
				}
			}
		} else {
			err = start()
		}
		result.OK = err == nil
		if err != nil {
			diagnostic, cause := startDiagnostic(err)
			result.StartCause = cause
			log.Printf("owner shell start failed: error=%q", diagnostic)
		}
		if err == nil {
			shell := r.Grant.Grant.ShellGeneration
			var credit *outputCredit
			if r.Window > 0 {
				credit = newOutputCredit(int(min(r.Window, maxServerWindow)))
				result.Window = uint32(credit.window) //nolint:gosec // bounded by maxServerWindow
				s.mu.Lock()
				s.credits[creditKey(r.SessionID, shell)] = credit
				s.mu.Unlock()
			}
			sender := s.outputSender()
			launch.output.Add(1)
			// The sender's whole life is bounded by the shell's: once the helper
			// exits, every wait (credit, retry, an in-flight send) returns at once.
			life, stop := context.WithCancel(context.WithoutCancel(ctx))
			tracked := &shellSender{stop: stop, done: make(chan struct{})}
			key := creditKey(r.SessionID, shell)
			s.mu.Lock()
			s.senders[key] = tracked
			s.mu.Unlock()
			done := c.shellDone(shell)
			go func() {
				select {
				case <-done:
				case <-life.Done():
				}
				stop()
			}()
			go func() {
				defer launch.output.Done()
				defer close(tracked.done)
				defer func() {
					s.mu.Lock()
					if s.senders[key] == tracked {
						delete(s.senders, key)
					}
					s.mu.Unlock()
				}()
				defer stop()
				defer func() {
					if credit != nil {
						s.mu.Lock()
						if s.credits[creditKey(r.SessionID, shell)] == credit {
							delete(s.credits, creditKey(r.SessionID, shell))
						}
						s.mu.Unlock()
					}
				}()
				if sendOutput(life, out, credit, func(data []byte) RPC {
					return RPC{Version: 1, Type: "owner_shell_output", SessionID: r.SessionID, Shell: shell, Data: data}
				}, sender, ownershell.OutputStall) == outputLost {
					_, _ = c.CloseShell(context.WithoutCancel(ctx), ownershell.SupervisorIdentity(c.binding, r.Grant), "transport_lost")
				}
			}()
		}
	case "renew":
		controlResult(&result, c.Renew(ctx, r.Grant))
	case "input":
		controlResult(&result, c.Input(ctx, r.Grant, r.Seq, r.Data))
	case "resize":
		controlResult(&result, c.Resize(ctx, r.Grant, r.Seq, r.Cols, r.Rows))
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
		// The proof also covers output: join this shell's sender first.
		if err == nil && s.finishShellOutput(ctx, r.SessionID, r.Grant.Grant.ShellGeneration) == nil {
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
			launch.phase = "running"
			launch.mu.Unlock()
			result.Cleanup = &proof
			result.OK = true
		}
	}
	return result
}

// sourceChecked reports operations that verify or use shell authority. Cleanup
// (close, close_shell, close_launch, phase, freeze, recover) stays available.
func sourceChecked(op string) bool {
	switch op {
	case "describe", "configure", "open", "admit", "renew", "input", "resize":
		return true
	}
	return false
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
		return ownershell.Refuse(ownershell.ReasonRuntimeIdentity)
	}
	if current == launch.command.Identity && !launch.consentDirty.Load() {
		return nil
	}
	launch.mu.Lock()
	wasFenced := launch.fenced
	launch.fenced = true
	launch.mu.Unlock()
	if c := launch.coordinator; c != nil {
		if _, err := c.CloseLaunch(ctx, c.launch(), "identity_changed"); err != nil {
			return ownershell.Refuse(ownershell.ReasonRuntimeCleanup)
		}
	}
	// Reopening with fresh consent requires the runtime owner's independent
	// orphan proof as well as the coordinator's exact cleanup receipt. Preserve
	// any pre-existing lifecycle fence; identity rotation cannot resume a stop.
	if err := launch.reconcile(ctx); err != nil {
		return ownershell.Refuse(ownershell.ReasonRuntimeCleanup)
	}
	if err := joinShellOutput(ctx, launch); err != nil {
		return ownershell.Refuse(ownershell.ReasonRuntimeCleanup)
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ownershell.Refuse(ownershell.ReasonRuntimeIdentity)
	}
	s.mu.Lock()
	launch.mu.Lock()
	launch.fenced = wasFenced
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

func controlResult(result *RPC, err error) {
	result.OK = err == nil
	if err != nil {
		diagnostic, cause := startDiagnostic(err)
		result.StartCause = cause
		log.Printf("owner shell control failed: error=%q", diagnostic)
	}
}

type outputResult int

const (
	outputSent outputResult = iota
	outputCanceled
	outputLost
)

// sendOutput forwards one shell's output to the server until the stream ends,
// the shell ends (life), or the link is lost. With a server window it never
// has more unacknowledged bytes than the window: it waits for credit, which
// stops the coordinator, the helper's window and finally the program.
func sendOutput(life context.Context, out <-chan []byte, credit *outputCredit, frame func([]byte) RPC, send func(context.Context, RPC) error, stall time.Duration) outputResult {
	for {
		var data []byte
		select {
		case <-life.Done():
			return outputCanceled
		case chunk, ok := <-out:
			if !ok {
				return outputSent
			}
			data = chunk
		}
		for len(data) > 0 {
			n := len(data)
			if credit != nil {
				if n = credit.take(life, n); n == 0 {
					return outputCanceled
				}
			}
			if result := sendWithin(life, send, frame(data[:n]), stall); result != outputSent {
				return result
			}
			data = data[n:]
		}
	}
}

// sendWithin retries one output frame while the connection is briefly busy
// (another lane holding the writer), so a burst waits instead of ending the
// shell. Only a link that refuses output for the whole stall counts as lost.
// The shell's life is checked before every attempt and passed to the send, so
// once the shell ends no new frame starts and a queued or in-flight one is
// canceled by the transport; the send itself is joined, never abandoned, so no
// output can follow a cleanup proof. Cancellation is never reported as sent.
func sendWithin(life context.Context, send func(context.Context, RPC) error, frame RPC, stall time.Duration) outputResult {
	deadline := time.Now().Add(stall)
	for {
		if life.Err() != nil {
			return outputCanceled
		}
		if send(life, frame) == nil {
			return outputSent
		}
		if life.Err() != nil {
			return outputCanceled
		}
		if !time.Now().Before(deadline) {
			return outputLost
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-timer.C:
		case <-life.Done():
			timer.Stop()
			return outputCanceled
		}
	}
}
