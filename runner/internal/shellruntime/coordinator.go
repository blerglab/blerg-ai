package shellruntime

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/contracts/runtimelifecycle"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Coordinator uses the runtime owner's shell-state mutex. P2 holds its outer
// workspace transaction lock across queued calls, so that lock must never be
// supplied here. Callbacks must not acquire the outer lock. Configuration is from trusted
// prepared runtime state and authenticated connection, never an open request.
// Reservations and helper creation serialize with freeze/stop/replacement.
type Coordinator struct {
	launchClosing               atomic.Bool
	pendingClosed               sync.Map
	recovering                  bool
	epoch                       uint64
	sessionMu                   *sync.Mutex
	binding                     ownershell.Binding
	command                     Command
	key                         ed25519.PublicKey
	phase                       protocol.SupervisorPhase
	recovered, fenced, terminal bool
	peers                       map[string]*helperPeer
	grants                      map[string]ownershell.SignedGrant
	applied                     map[string]ownershell.SignedGrant // last lease acknowledged by each helper
	reserved                    map[string]bool
	closed                      map[string]protocol.SupervisorCleanup
	budgetMu                    sync.Mutex
	second                      int64
	input, output, frames       int
	// Signal must publish the syncing header before Notify returns to the freezer.
	Signal func(protocol.SupervisorPhase) error
}

func NewCoordinator(mu *sync.Mutex, b ownershell.Binding, command Command, key ed25519.PublicKey) (*Coordinator, error) {
	launch := ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch
	if !b.Live || b.Child || b.Cron || b.Owner == "" || command.Recovery == nil || mu == nil || b.Audience != ownershell.RuntimeAudience(b.Connection, b.RuntimeGeneration) || launch.Validate() != nil || b.Root.Inode == 0 || b.ManifestDigest == "" || len(key) != ed25519.PublicKeySize {
		return nil, ErrClosed
	}
	if command.Policy.Version != 1 || !command.Policy.Enabled || command.Policy.Revision == 0 {
		return nil, ErrClosed
	}
	command.Root = b.Root
	if b.Runtime == "host" && (!b.HostConsentValid || b.Consent == "" || !consentMatches(command.Consent, b, command.Identity)) {
		return nil, ErrClosed
	}
	return &Coordinator{sessionMu: mu, binding: b, command: command, key: append(ed25519.PublicKey(nil), key...), phase: runtimelifecycle.Running, peers: map[string]*helperPeer{}, grants: map[string]ownershell.SignedGrant{}, applied: map[string]ownershell.SignedGrant{}, reserved: map[string]bool{}, closed: map[string]protocol.SupervisorCleanup{}}, nil
}
func (c *Coordinator) launch() protocol.SupervisorLaunch {
	return ownershell.SupervisorIdentity(c.binding, ownershell.SignedGrant{}).Launch
}
func (c *Coordinator) AdmitShell(ctx context.Context, s protocol.SupervisorShell) error {
	if c.launchClosing.Load() {
		return stageError("admit_shell", nil)
	}
	if _, ok := c.pendingClosed.Load(s.ShellGeneration); ok {
		return stageError("admit_shell", nil)
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if ctx.Err() != nil || s.Validate() != nil || s.Launch != c.launch() || !c.recovered || c.recovering || c.launchClosing.Load() || c.fenced || c.terminal || c.phase != runtimelifecycle.Running || c.reserved[s.ShellGeneration] || len(c.reserved) >= 4 {
		return stageError("admit_shell", nil)
	}
	// Four reservations partition ONE 128-task budget, never four 128-task budgets.
	c.reserved[s.ShellGeneration] = true
	return nil
}
func (c *Coordinator) Open(ctx context.Context, b ownershell.Binding, g ownershell.SignedGrant, key ed25519.PublicKey) (<-chan []byte, error) {
	s := ownershell.SupervisorIdentity(b, g)
	if err := c.AdmitShell(ctx, s); err != nil {
		return nil, err
	}
	return c.OpenReserved(ctx, s, b, g, key)
}
func (c *Coordinator) OpenReserved(ctx context.Context, s protocol.SupervisorShell, b ownershell.Binding, g ownershell.SignedGrant, key ed25519.PublicKey) (<-chan []byte, error) {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if b != c.binding || s != ownershell.SupervisorIdentity(c.binding, g) {
		return nil, stageError("verify_binding", nil)
	}
	if !c.reserved[s.ShellGeneration] || c.peers[s.ShellGeneration] != nil {
		return nil, stageError("verify_reservation", nil)
	}
	if !c.recovered || c.recovering || c.launchClosing.Load() || c.fenced || c.terminal || c.phase != runtimelifecycle.Running {
		return nil, stageError("admit_shell", nil)
	}
	if !key.Equal(c.key) {
		return nil, stageError("verify_policy", nil)
	}
	if err := c.verify(g); err != nil {
		if errors.Is(err, ownershell.ErrGrantClock) {
			return nil, stageError("grant_clock", nil)
		}
		if errors.Is(err, ownershell.ErrGrantExpired) {
			return nil, stageError("lease_create", nil)
		}
		return nil, stageError("verify_policy", nil)
	}
	peer, err := startPeer(ctx, c.command, g, c.key)
	if peer != nil {
		c.peers[s.ShellGeneration] = peer
		c.grants[s.ShellGeneration] = g
		c.applied[s.ShellGeneration] = g
	}
	if err != nil {
		c.fenced = true
		return nil, err
	}
	out := make(chan []byte, 8)
	peer.forwardDone = make(chan struct{})
	go func() {
		defer close(out)
		defer close(peer.forwardDone)
		for {
			// Everything queued becomes one frame, so a slow consumer gets fewer,
			// larger frames instead of an overflow.
			data, ok := peer.output.Read(ownershell.MaxFrame, nil)
			if !ok {
				return
			}
			peer.consumed(len(data))
			c.sessionMu.Lock()
			allowed := !c.launchClosing.Load() && c.valid(c.grants[s.ShellGeneration]) && !c.fenced && !c.terminal && c.phase == runtimelifecycle.Running
			c.sessionMu.Unlock()
			if !allowed {
				continue
			}
			// Over budget or a full consumer means wait, never close. Both waits end
			// when the helper exits, and cleanup joins this goroutine only after that,
			// so neither can hold a close, expiry or cleanup proof.
			if !c.waitOutput(len(data), peer.done) {
				return
			}
			select {
			case out <- data:
			case <-peer.done:
				return
			}
		}
	}()
	return out, nil
}
func (c *Coordinator) valid(g ownershell.SignedGrant) bool { return c.verify(g) == nil }

// verify is valid with the reason kept, so a clock-window refusal can be told apart.
func (c *Coordinator) verify(g ownershell.SignedGrant) error {
	if c.launchClosing.Load() {
		return ErrClosed
	}
	if _, closed := c.pendingClosed.Load(g.Grant.ShellGeneration); closed {
		return ErrClosed
	}
	expected := g.Grant
	expected.Audience = c.binding.Audience
	expected.OwnerID = c.binding.Owner
	expected.SessionID = c.binding.Session
	expected.LaunchGeneration = c.binding.Launch
	expected.ConnectionID = c.binding.Connection
	expected.Runtime = c.binding.Runtime
	expected.ConsentID = c.binding.Consent
	return ownershell.VerifyGrant(c.key, g, expected, c.command.Policy.Revision, time.Now())
}
func (c *Coordinator) operation(ctx context.Context, g ownershell.SignedGrant, frame HelperFrame) error {
	if c.launchClosing.Load() {
		return ErrClosed
	}
	if _, ok := c.pendingClosed.Load(g.Grant.ShellGeneration); ok {
		return ErrClosed
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	peer := c.peers[g.Grant.ShellGeneration]
	current := c.grants[g.Grant.ShellGeneration]
	if peer == nil || !c.valid(g) || current.Grant.OwnerID != g.Grant.OwnerID || current.Grant.LoginID != g.Grant.LoginID || current.Grant.PolicyRevision != g.Grant.PolicyRevision || c.fenced || c.terminal || c.phase != runtimelifecycle.Running {
		return ErrClosed
	}
	frame.Grant = g
	if err := peer.call(ctx, frame); err != nil {
		c.fenced = true
		return err
	}
	if frame.Op == "renew" {
		c.grants[g.Grant.ShellGeneration] = g
		c.applied[g.Grant.ShellGeneration] = g
	}
	return nil
}
func (c *Coordinator) Renew(ctx context.Context, g ownershell.SignedGrant) error {
	c.sessionMu.Lock()
	if c.phase == runtimelifecycle.Syncing && !c.fenced && !c.terminal && c.valid(g) {
		current, ok := c.grants[g.Grant.ShellGeneration]
		if ok && current.Grant.LoginID == g.Grant.LoginID && g.Grant.IssuedAt >= current.Grant.IssuedAt && g.Grant.ExpiresAt >= current.Grant.ExpiresAt {
			c.grants[g.Grant.ShellGeneration] = g
			c.sessionMu.Unlock()
			return nil
		}
	}
	c.sessionMu.Unlock()
	return c.operation(ctx, g, HelperFrame{Op: "renew"})
}
func (c *Coordinator) Input(ctx context.Context, g ownershell.SignedGrant, seq uint64, data []byte) error {
	if len(data) > 16384 || !c.inputRate(len(data)) {
		return ErrClosed
	}
	return c.operation(ctx, g, HelperFrame{Op: "input", Seq: seq, Data: data})
}
func (c *Coordinator) Resize(ctx context.Context, g ownershell.SignedGrant, seq uint64, cols, rows uint16) error {
	return c.operation(ctx, g, HelperFrame{Op: "resize", Seq: seq, Cols: cols, Rows: rows})
}
func (c *Coordinator) inputRate(n int) bool {
	c.budgetMu.Lock()
	defer c.budgetMu.Unlock()
	now := time.Now().Unix()
	if now != c.second {
		c.second = now
		c.input = 0
		c.output = 0
		c.frames = 0
	}
	c.input += n
	return c.input <= 16384
}

// OutputFramesPerSecond paces frames to half the server's per-session budget,
// leaving room for skew between this budget's seconds and the server's.
const OutputFramesPerSecond = 64

// waitOutput takes n bytes and one frame from the session's output budget,
// waiting for the next second when it is spent. It returns false once stop
// closes. n never exceeds ownershell.MaxFrame, so a fresh second always fits.
func (c *Coordinator) waitOutput(n int, stop <-chan struct{}) bool {
	for {
		c.budgetMu.Lock()
		now := time.Now()
		if now.Unix() != c.second {
			c.second = now.Unix()
			c.input = 0
			c.output = 0
			c.frames = 0
		}
		if c.output+n <= ownershell.OutputRate && c.frames < OutputFramesPerSecond {
			c.output += n
			c.frames++
			c.budgetMu.Unlock()
			return true
		}
		wait := time.Unix(c.second+1, 0).Sub(now)
		c.budgetMu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-stop:
			timer.Stop()
			return false
		}
	}
}

// shellDone is closed when the shell's helper has exited (or is already gone).
func (c *Coordinator) shellDone(shell string) <-chan struct{} {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if peer := c.peers[shell]; peer != nil {
		return peer.done
	}
	gone := make(chan struct{})
	close(gone)
	return gone
}

func (c *Coordinator) Notify(ctx context.Context, l protocol.SupervisorLaunch, p protocol.SupervisorPhase) error {
	c.sessionMu.Lock()
	if ctx.Err() != nil || l != c.launch() || !p.Valid() {
		c.sessionMu.Unlock()
		return ErrClosed
	}
	if c.Signal != nil {
		if err := c.Signal(p); err != nil {
			c.fenced = true
			c.sessionMu.Unlock()
			return err
		}
	}
	c.epoch++
	c.phase = p
	terminal := p == runtimelifecycle.Stopping || p == runtimelifecycle.Replacing || p == runtimelifecycle.Ended || p == runtimelifecycle.Discarded
	if terminal {
		c.terminal = true
	}
	if p == runtimelifecycle.Running {
		for id, peer := range c.peers {
			if err := peer.call(ctx, HelperFrame{Op: "renew", Grant: c.grants[id]}); err != nil {
				c.fenced = true
				c.sessionMu.Unlock()
				return err
			}
			c.applied[id] = c.grants[id]
		}
	}
	c.sessionMu.Unlock()
	if terminal || p == runtimelifecycle.Paused {
		_, err := c.CloseLaunch(ctx, l, "phase_changed")
		return err
	}
	return nil
}
func (c *Coordinator) Close(ctx context.Context, g ownershell.SignedGrant) error {
	_, err := c.CloseShell(ctx, ownershell.SupervisorIdentity(c.binding, g), "revoked")
	return err
}
func (c *Coordinator) closeShell(ctx context.Context, s protocol.SupervisorShell) (protocol.SupervisorCleanup, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	c.sessionMu.Lock()
	if s.Launch != c.launch() {
		c.sessionMu.Unlock()
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	if proof, ok := c.closed[s.ShellGeneration]; ok {
		c.sessionMu.Unlock()
		return proof, nil
	}
	peer := c.peers[s.ShellGeneration]
	if peer == nil {
		if !c.reserved[s.ShellGeneration] {
			c.sessionMu.Unlock()
			return protocol.SupervisorCleanup{}, ErrClosed
		}
		delete(c.reserved, s.ShellGeneration)
		proof := c.proof(s.ShellGeneration)
		c.closed[s.ShellGeneration] = proof
		c.sessionMu.Unlock()
		return proof, nil
	}
	grant := c.grants[s.ShellGeneration]
	c.sessionMu.Unlock()
	// No session lock spans IO or reader joins. Four revocations must clean up
	// together, and output forwarding needs that lock to observe its fence.
	peer.closeMu.Lock()
	defer peer.closeMu.Unlock()
	err := peer.call(ctx, HelperFrame{Op: "close", Grant: grant})
	if err == nil {
		err = joinHelperPeers(ctx, []*helperPeer{peer})
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if err != nil {
		c.fenced = true
		return protocol.SupervisorCleanup{}, err
	}
	proof := c.proof(s.ShellGeneration)
	c.closed[s.ShellGeneration] = proof
	delete(c.peers, s.ShellGeneration)
	delete(c.grants, s.ShellGeneration)
	delete(c.applied, s.ShellGeneration)
	delete(c.reserved, s.ShellGeneration)
	return proof, nil
}
func (c *Coordinator) proof(shell string) protocol.SupervisorCleanup {
	return protocol.SupervisorCleanup{Launch: c.launch(), ShellGeneration: shell, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
}
func (c *Coordinator) closeLaunch(ctx context.Context, l protocol.SupervisorLaunch, reason string) (protocol.SupervisorCleanup, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c.sessionMu.Lock()
	if l != c.launch() {
		c.sessionMu.Unlock()
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	c.epoch++
	epoch := c.epoch
	c.fenced = true
	ids := make([]string, 0, len(c.reserved))
	for id := range c.reserved {
		ids = append(ids, id)
	}
	c.sessionMu.Unlock()
	var failure error
	failures := make(chan error, len(ids))
	for _, id := range ids {
		go func(id string) {
			_, err := c.CloseShell(ctx, protocol.SupervisorShell{Launch: l, ShellGeneration: id}, reason)
			failures <- err
		}(id)
	}
	for range ids {
		if err := <-failures; err != nil {
			failure = err
		}
	}
	if failure != nil {
		// A runtime owner may prove the ENTIRE containment namespace is gone.
		// Socket/client death is insufficient; failure otherwise stays fenced.
		if c.command.Recovery(ctx, l) != nil {
			return protocol.SupervisorCleanup{}, failure
		}
		c.sessionMu.Lock()
		if c.epoch != epoch || !c.launchClosing.Load() {
			c.sessionMu.Unlock()
			return protocol.SupervisorCleanup{}, ErrClosed
		}
		peers := make([]*helperPeer, 0, len(c.peers))
		for _, peer := range c.peers {
			peers = append(peers, peer)
			_ = peer.input.Close()
		}
		c.sessionMu.Unlock()
		if err := joinHelperPeers(ctx, peers); err != nil {
			return protocol.SupervisorCleanup{}, err
		}
		c.sessionMu.Lock()
		if c.epoch != epoch {
			c.sessionMu.Unlock()
			return protocol.SupervisorCleanup{}, ErrClosed
		}
		c.peers = map[string]*helperPeer{}
		c.grants = map[string]ownershell.SignedGrant{}
		c.applied = map[string]ownershell.SignedGrant{}
		c.reserved = map[string]bool{}
		c.sessionMu.Unlock()
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if c.epoch != epoch {
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	return c.proof(""), nil
}
func (c *Coordinator) RecoverLaunch(ctx context.Context, l protocol.SupervisorLaunch) (protocol.SupervisorCleanup, error) {
	if l != c.launch() {
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	c.sessionMu.Lock()
	if c.recovering {
		c.sessionMu.Unlock()
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	c.recovering = true
	epoch := c.epoch + 1
	initial := !c.recovered
	c.sessionMu.Unlock()
	defer func() { c.sessionMu.Lock(); c.recovering = false; c.sessionMu.Unlock() }()
	proof, err := c.CloseLaunch(ctx, l, "recovery")
	if initial || err != nil {
		// The runtime owner supplies independent orphan reconciliation. A new
		// in-memory map, dead socket or killed docker client is never this proof.
		if c.command.Recovery(ctx, l) != nil {
			return protocol.SupervisorCleanup{}, ErrClosed
		}
		c.sessionMu.Lock()
		peers := make([]*helperPeer, 0, len(c.peers))
		for _, peer := range c.peers {
			peers = append(peers, peer)
			_ = peer.input.Close()
		}
		c.sessionMu.Unlock()
		if err := joinHelperPeers(ctx, peers); err != nil {
			return protocol.SupervisorCleanup{}, err
		}
		c.sessionMu.Lock()
		if c.epoch != epoch {
			c.sessionMu.Unlock()
			return protocol.SupervisorCleanup{}, ErrClosed
		}
		c.peers = map[string]*helperPeer{}
		c.grants = map[string]ownershell.SignedGrant{}
		c.applied = map[string]ownershell.SignedGrant{}
		c.reserved = map[string]bool{}
		proof = c.proof("")
		c.sessionMu.Unlock()
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if c.epoch != epoch || c.terminal || c.phase != runtimelifecycle.Running {
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	c.recovered = true
	c.fenced = false
	c.launchClosing.Store(false)
	return proof, nil
}

var _ protocol.RuntimeSupervisor = (*Coordinator)(nil)
var _ ownershell.AdmittedSupervisor = (*Coordinator)(nil)

func consentMatches(consent *protocol.HostShellConsent, b ownershell.Binding, i Identity) bool {
	return consent != nil && consent.ID == b.Consent && consent.OwnerID == b.Owner && consent.LaunchGeneration == b.Launch && consent.MachineID == i.Machine && consent.Account == i.Account && consent.UID == i.UID && consent.Shell == i.Shell && consent.Cwd == i.Cwd && consent.ChallengeID != ""
}

type cleanupResult struct {
	proof protocol.SupervisorCleanup
	err   error
}

func (c *Coordinator) CloseShell(ctx context.Context, s protocol.SupervisorShell, _ string) (protocol.SupervisorCleanup, error) {
	if s.Launch != c.launch() {
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	c.pendingClosed.Store(s.ShellGeneration, struct{}{})
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result := make(chan cleanupResult, 1)
	go func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		proof, err := c.closeShell(cleanup, s)
		result <- cleanupResult{proof, err}
	}()
	if call.Err() != nil {
		c.launchClosing.Store(true)
		return protocol.SupervisorCleanup{}, call.Err()
	}
	select {
	case reply := <-result:
		if reply.err != nil {
			c.launchClosing.Store(true)
		}
		return reply.proof, reply.err
	case <-call.Done():
		c.launchClosing.Store(true)
		return protocol.SupervisorCleanup{}, call.Err()
	}
}
func (c *Coordinator) CloseLaunch(ctx context.Context, l protocol.SupervisorLaunch, reason string) (protocol.SupervisorCleanup, error) {
	if l != c.launch() {
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	c.launchClosing.Store(true)
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result := make(chan cleanupResult, 1)
	go func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		defer stop()
		proof, err := c.closeLaunch(cleanup, l, reason)
		result <- cleanupResult{proof, err}
	}()
	if call.Err() != nil {
		return protocol.SupervisorCleanup{}, call.Err()
	}
	select {
	case reply := <-result:
		return reply.proof, reply.err
	case <-call.Done():
		return protocol.SupervisorCleanup{}, call.Err()
	}
}

// FrozenUntil is called after the serialized syncing transition. It does no
// runtime exec and does not trust a renewed grant the paused helper has not seen.
func (c *Coordinator) FrozenUntil() time.Time {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	until := time.Now().Add(time.Duration(protocol.DefaultWorkspaceBudget().SyncSeconds) * time.Second)
	for _, g := range c.applied {
		if end := time.Unix(g.Grant.ExpiresAt, 0); end.Before(until) {
			until = end
		}
	}
	return until
}

func joinHelperPeers(ctx context.Context, peers []*helperPeer) error {
	for _, peer := range peers {
		select {
		case <-peer.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if peer.forwardDone != nil {
			select {
			case <-peer.forwardDone:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}
