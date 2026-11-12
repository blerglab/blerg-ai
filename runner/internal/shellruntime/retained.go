package shellruntime

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type retainedChild struct {
	custody       protocol.ShellCustody
	grant         ownershell.SignedGrant
	peer          *helperPeer
	closing       string
	closed        bool
	input, resize uint64
	receipt       uint64
	window        uint64
	credit        *ownershell.StreamCredit
	creditReady   chan struct{}
	cancel        context.CancelFunc
	outputDone    chan struct{}
	settled       chan struct{}
}
type retainedPlan struct {
	transaction string
	binding     ownershell.Binding
	key         ed25519.PublicKey
	grants      map[string]ownershell.SignedGrant
	revision    uint64
}

// retainedCoordinator owns one physical launch, independent of the access
// connection. P2's shell-state mutex gates admission and native lifecycle fences.
// No mutex spans helper IO, output joins, or independent orphan reconciliation.
type retainedCoordinator struct {
	mu                          *sync.Mutex
	binding                     ownershell.Binding
	command                     Command
	link                        protocol.ShellLinkWelcome
	key                         ed25519.PublicKey
	registry                    *ownershell.CustodyRegistry
	now                         func() time.Time
	send                        func(context.Context, RPC) error
	children                    map[string]*retainedChild
	phase                       protocol.SupervisorPhase
	active, terminal, uncertain bool
	identityRevoked             bool
	highestEpoch, revision      uint64
	inventoryChallenge          string
	inventoryRevision           uint64
	inventorySeal               [32]byte
	pending                     *retainedPlan
	committed                   string
}

func newRetainedCoordinator(mu *sync.Mutex, b ownershell.Binding, command Command, key ed25519.PublicKey, registry *ownershell.CustodyRegistry, now func() time.Time, send func(context.Context, RPC) error) (*retainedCoordinator, error) {
	w := b.ShellLink
	h := protocol.ShellLinkHello{Version: 2, Daemon: w.Daemon, Challenge: w.Challenge}
	if command.SourceLive != nil && command.SourceState == nil {
		return nil, ErrClosed
	}
	if mu == nil || registry == nil || now == nil || send == nil || !w.Matches(h, now()) || w.Desktop.OwnerID != b.Owner || !b.Live || b.Child || b.Cron || !b.ServerCapabilities.SupportsPersistence(b.Runtime) || !b.RuntimeCapabilities.SupportsPersistence(b.Runtime) || len(key) != ed25519.PublicKeySize || command.Policy.Version != 1 || !command.Policy.Enabled || command.Policy.Revision == 0 || command.Identity.MemberRoot.Inode == 0 || command.Root != b.Root {
		return nil, ErrClosed
	}
	return &retainedCoordinator{mu: mu, binding: b, command: command, link: w, key: append(ed25519.PublicKey(nil), key...), registry: registry, now: now, send: send, children: map[string]*retainedChild{}, phase: "running", active: true, revision: 1}, nil
}
func (c *retainedCoordinator) scope() string {
	for _, child := range c.children {
		return child.custody.PhysicalScope()
	}
	return (protocol.ShellCustody{Container: c.binding.Container, Session: c.binding.Session, Launch: c.binding.Launch, Runtime: c.binding.Runtime, RuntimeGeneration: c.binding.RuntimeGeneration, Root: c.binding.Root, Manifest: c.binding.ManifestDigest, Desktop: c.link.Desktop.DesktopID}).PhysicalScope()
}
func (c *retainedCoordinator) pinned(custody protocol.ShellCustody) bool {
	b, w := c.binding, c.link
	return custody.Validate() == nil && custody.Owner == b.Owner && custody.Session == b.Session && custody.Launch == b.Launch && custody.Runtime == b.Runtime && custody.Container == b.Container && custody.Container == c.command.Container && custody.RuntimeGeneration == b.RuntimeGeneration && custody.Root == b.Root && custody.Manifest == b.ManifestDigest && custody.Desktop == w.Desktop.DesktopID && custody.Identity == RuntimeIdentityDigest(c.command.Identity)
}
func (c *retainedCoordinator) currentCustodian(custody protocol.ShellCustody) bool {
	w := c.link
	return c.pinned(custody) && custody.Daemon == w.Daemon && custody.Runner == w.Runner && custody.Credential == w.Desktop.TokenID && custody.OwnershipRevision == w.Desktop.Revision
}
func (c *retainedCoordinator) validLocked(child *retainedChild, g ownershell.SignedGrant) bool {
	if child == nil || child.closing != "" || child.closed || !c.active || c.terminal || c.uncertain || c.pending != nil || c.phase != "running" || !c.now().Before(time.Unix(child.custody.LifetimeUntil, 0)) || !c.link.Desktop.ExpiresAt.After(c.now()) {
		return false
	}
	if child.custody.Runtime == "host" && (c.command.persistentConsent == nil || !c.command.persistentConsent(child.custody)) {
		return false
	}
	expected := child.grant.Grant
	return ownershell.VerifyGrant(c.key, g, expected, c.command.Policy.Revision, c.now()) == nil
}
func (c *retainedCoordinator) suspendLocked() []*retainedChild {
	c.active = false
	c.revision++
	c.inventoryRevision = 0
	children := make([]*retainedChild, 0, len(c.children))
	for _, child := range c.children {
		if child.cancel != nil {
			child.cancel()
		}
		if child.peer != nil && child.peer.stream != nil {
			child.peer.stream.suspend()
		}
		if !child.closed {
			children = append(children, child)
		}
	}
	c.registry.Suspend(c.scope())
	return children
}
func (c *retainedCoordinator) Suspend(ctx context.Context) error {
	c.mu.Lock()
	children := c.suspendLocked()
	c.mu.Unlock()
	// Private helper control is best effort. Every external access path has
	// already been fenced, and the helper independently latches grant expiry.
	for _, child := range children {
		if child.peer != nil {
			_ = child.peer.call(ctx, HelperFrame{Op: "suspend", Epoch: child.grant.Grant.AccessEpoch})
		}
	}
	return c.joinOutput(ctx, children)
}
func (c *retainedCoordinator) joinOutput(ctx context.Context, children []*retainedChild) error {
	for _, child := range children {
		if child.outputDone != nil {
			select {
			case <-child.outputDone:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}
func (c *retainedCoordinator) Open(ctx context.Context, custody protocol.ShellCustody, g ownershell.SignedGrant, window uint32) error {
	c.mu.Lock()
	for id, child := range c.children {
		if child.closed && c.now().Unix() >= child.custody.LifetimeUntil {
			delete(c.children, id)
		}
	}
	if len(c.children) >= 1024 {
		c.mu.Unlock()
		return ownershell.ErrLimit
	}
	// Refusal here precedes consent consumption, registry reservation and helper
	// creation. Only this definite no-allocation result permits consent retry.
	if c.currentCustodian(custody) && c.children[custody.Digest()] == nil && custody.LifetimeUntil > c.now().Add(protocol.DesktopShellLifetime+protocol.DesktopShellClockSkew).Unix() {
		c.mu.Unlock()
		return ownershell.RefuseBeforeAllocation(ownershell.Refuse(ownershell.ReasonRuntimeBinding))
	}
	if !c.currentCustodian(custody) || !c.active || c.terminal || c.uncertain || c.pending != nil || c.phase != "running" || c.children[custody.Digest()] != nil || g.Grant.Privileged != custody.Privileged || g.Grant.CustodyID != custody.Digest() || g.Grant.Version != 2 || g.Grant.OwnerID != custody.Owner || g.Grant.LoginID != custody.Login || g.Grant.SessionID != custody.Session || g.Grant.LaunchGeneration != custody.Launch || g.Grant.Runtime != custody.Runtime || g.Grant.ConsentID != custody.Consent || g.Grant.ShellGeneration != custody.Shell || g.Grant.ConnectionID != c.binding.Connection || g.Grant.Audience != c.binding.Audience || g.Grant.AccessEpoch == 0 || (c.highestEpoch != 0 && g.Grant.AccessEpoch != c.highestEpoch) || ownershell.VerifyGrant(c.key, g, g.Grant, c.command.Policy.Revision, c.now()) != nil || !c.now().Before(time.Unix(custody.LifetimeUntil, 0)) || custody.LifetimeUntil > c.link.Desktop.ExpiresAt.Unix() || custody.Runtime == "host" && custody.LifetimeUntil > c.now().Add(protocol.DesktopShellLifetime+protocol.DesktopShellClockSkew).Unix() {
		c.mu.Unlock()
		return ownershell.ErrDenied
	}
	if custody.Runtime == "host" && (c.command.persistentConsent == nil || !c.command.persistentConsent(custody)) {
		c.mu.Unlock()
		return ownershell.Refuse(ownershell.ReasonRuntimeConsent)
	}
	if err := c.registry.Reserve(custody); err != nil {
		c.mu.Unlock()
		return err
	}
	child := &retainedChild{custody: custody, grant: g, window: uint64(window), creditReady: make(chan struct{}, 1), settled: make(chan struct{})}
	if child.window == 0 || child.window > ownershell.ServerOutputWindow {
		child.window = ownershell.ServerOutputWindow
	}
	c.children[custody.Digest()] = child
	c.highestEpoch = g.Grant.AccessEpoch
	c.revision++
	revision := c.revision
	command := c.command
	command.Custody = &custody
	command.cleanupUncertain = func() { c.mu.Lock(); c.markChildClosingLocked(child, "helper-cleanup-uncertain"); c.mu.Unlock() }
	onIdentityInvalidated := c.command.identityInvalidated
	command.identityInvalidated = func() {
		c.mu.Lock()
		c.identityRevoked = true
		c.terminal = true
		c.suspendLocked()
		c.mu.Unlock()
		if onIdentityInvalidated != nil {
			onIdentityInvalidated()
		}
	}
	c.mu.Unlock()
	peer, err := startPeer(ctx, command, g, c.key)
	c.mu.Lock()
	child.peer = peer
	if peer != nil {
		go c.settleOnHelperExit(context.WithoutCancel(ctx), child, peer)
	}
	valid := c.revision == revision && c.active && !c.terminal && !c.uncertain
	if err == nil && valid {
		err = c.registry.Activate(custody.Digest())
	}
	if err != nil || !valid {
		_, cause := startDiagnostic(err)
		identityFailure := cause == "verify_identity" || cause == "verify_root" || cause == "verify_cwd"
		if identityFailure {
			c.identityRevoked = true
			c.terminal = true
			c.suspendLocked()
		}
		if child.closing == "" {
			child.closing = "failed-open"
		}
		_ = c.registry.BeginClose(custody.Digest(), child.closing)
		c.mu.Unlock()
		if identityFailure && onIdentityInvalidated != nil {
			onIdentityInvalidated()
		}
		if err != nil {
			return err
		}
		return ownershell.ErrUnknown
	}
	c.startOutputLocked(ctx, child)
	c.mu.Unlock()
	return nil
}

func (c *retainedCoordinator) settleOnHelperExit(parent context.Context, child *retainedChild, peer *helperPeer) {
	if child.settled != nil {
		defer close(child.settled)
	}
	<-peer.done
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	if _, err := c.Close(ctx, child.custody, "helper-ended"); err != nil {
		_ = bridgeFailure("helper_exit_cleanup", err)
		return
	}
	c.mu.Lock()
	grant, offset := child.grant, child.receipt
	c.mu.Unlock()
	if err := c.send(ctx, RPC{Version: 2, Type: "owner_shell_output", SessionID: child.custody.Session, Shell: child.custody.Shell, Custody: &child.custody, Epoch: grant.Grant.AccessEpoch, Ended: true, Offset: offset, End: offset}); err != nil {
		_ = bridgeFailure("helper_closed_notice", err)
	}
}
func (c *retainedCoordinator) startOutputLocked(parent context.Context, child *retainedChild) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	child.cancel = cancel
	child.outputDone = make(chan struct{})
	child.credit = ownershell.NewStreamCredit(child.window, child.receipt)
	epoch := child.grant.Grant.AccessEpoch
	offset := child.receipt
	done := child.outputDone
	credit := child.credit
	go func() {
		defer close(done)
		for {
			frame, err := child.peer.readRetained(ctx, epoch, offset)
			if err != nil {
				if errors.Is(err, io.EOF) {
					c.mu.Lock()
					allowed := c.validLocked(child, child.grant) && child.grant.Grant.AccessEpoch == epoch
					c.mu.Unlock()
					if allowed {
						_ = c.send(ctx, RPC{Version: 2, Type: "owner_shell_output", SessionID: child.custody.Session, Shell: child.custody.Shell, Custody: &child.custody, Epoch: epoch, Ended: true, End: offset})
					}
				}
				return
			}
			for !credit.Reserve(frame.Offset, len(frame.Data), frame.Gap) {
				select {
				case <-ctx.Done():
					return
				case <-child.creditReady:
				}
			}
			c.mu.Lock()
			allowed := c.validLocked(child, child.grant) && child.grant.Grant.AccessEpoch == epoch && credit.Committed(frame.Offset, len(frame.Data)) == nil
			c.mu.Unlock()
			if !allowed {
				return
			}
			if c.send(ctx, RPC{Version: 2, Type: "owner_shell_output", SessionID: child.custody.Session, Shell: child.custody.Shell, Custody: &child.custody, Epoch: epoch, Offset: frame.Offset, End: frame.End, Gap: frame.Gap, Data: frame.Data}) != nil {
				c.mu.Lock()
				c.suspendLocked()
				c.mu.Unlock()
				return
			}
			offset = frame.Offset + uint64(len(frame.Data))
		}
	}()
}
func (c *retainedCoordinator) Inventory(ctx context.Context, peer ownershell.CustodyInventory) (ownershell.CustodyInventory, error) {
	if err := c.Suspend(ctx); err != nil {
		return ownershell.CustodyInventory{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if peer.Scope != c.scope() {
		return ownershell.CustodyInventory{}, ownershell.ErrDenied
	}
	if c.terminal {
		for _, child := range c.children {
			if !child.closed {
				c.markChildClosingLocked(child, "runtime-terminal")
			}
		}
	}
	matched, err := c.registry.Reconcile(peer, peer.Challenge)
	if err != nil {
		return ownershell.CustodyInventory{}, err
	}
	for _, closing := range matched.Closing {
		if child := c.children[closing.Custody.Digest()]; child != nil && child.closing == "" {
			child.closing = closing.Transaction
			if child.closing == "" {
				child.closing = "unclaimed"
			}
			_ = c.registry.BeginClose(child.custody.Digest(), child.closing)
		}
	}
	c.pending = nil
	c.inventoryChallenge = peer.Challenge
	c.inventoryRevision = c.revision
	result := c.registry.Snapshot(c.scope(), peer.Challenge)
	result.Revision = c.revision
	raw, _ := json.Marshal(result)
	c.inventorySeal = sha256.Sum256(raw)
	return result, nil
}
func (c *retainedCoordinator) Prepare(ctx context.Context, b ownershell.Binding, transaction string, inventory ownershell.CustodyInventory, key ed25519.PublicKey, grants []ownershell.SignedGrant) error {
	raw, _ := json.Marshal(inventory)
	c.mu.Lock()
	if sha256.Sum256(raw) != c.inventorySeal || transaction == "" || len(transaction) > 256 || len(grants) > ownershell.MaxShells || len(key) != ed25519.PublicKeySize || c.terminal || c.uncertain || c.phase != "running" || c.inventoryRevision == 0 || inventory.Revision != c.inventoryRevision || c.revision != c.inventoryRevision || inventory.Challenge != c.inventoryChallenge || !b.ShellLink.SameCustodian(c.link) || b.Session != c.binding.Session || b.Owner != c.binding.Owner || b.Launch != c.binding.Launch || b.Runtime != c.binding.Runtime || b.RuntimeGeneration != c.binding.RuntimeGeneration || b.Root != c.binding.Root || b.ManifestDigest != c.binding.ManifestDigest || b.Audience != ownershell.RuntimeAudience(b.Connection, b.RuntimeGeneration) {
		c.mu.Unlock()
		return ownershell.ErrDenied
	}
	if c.pending != nil {
		c.mu.Unlock()
		return ownershell.ErrBusy
	}
	plan := &retainedPlan{transaction: transaction, binding: b, key: append(ed25519.PublicKey(nil), key...), grants: map[string]ownershell.SignedGrant{}, revision: c.revision}
	var epoch uint64
	for _, g := range grants {
		child := c.children[g.Grant.CustodyID]
		if child == nil || child.peer == nil || child.closing != "" || child.closed || !c.currentCustodian(child.custody) || g.Grant.Version != 2 || g.Grant.CustodyID != child.custody.Digest() || g.Grant.OwnerID != child.custody.Owner || g.Grant.LoginID != child.custody.Login || g.Grant.ConsentID != child.custody.Consent || g.Grant.SessionID != child.custody.Session || g.Grant.LaunchGeneration != child.custody.Launch || g.Grant.Runtime != child.custody.Runtime || g.Grant.ShellGeneration != child.custody.Shell || g.Grant.ConnectionID != b.Connection || g.Grant.Audience != b.Audience || g.Grant.AccessEpoch <= c.highestEpoch || ownershell.VerifyGrant(key, g, g.Grant, c.command.Policy.Revision, c.now()) != nil || !c.now().Before(time.Unix(child.custody.LifetimeUntil, 0)) || plan.grants[g.Grant.CustodyID].Grant.Version != 0 {
			c.mu.Unlock()
			return ownershell.ErrDenied
		}
		if child.custody.Runtime == "host" && (c.command.persistentConsent == nil || !c.command.persistentConsent(child.custody)) {
			c.mu.Unlock()
			return ownershell.ErrDenied
		}
		if epoch != 0 && epoch != g.Grant.AccessEpoch {
			c.mu.Unlock()
			return ownershell.ErrDenied
		}
		epoch = g.Grant.AccessEpoch
		plan.grants[g.Grant.CustodyID] = g
	}
	for id, child := range c.children {
		if !child.closed && child.closing == "" && plan.grants[id].Grant.Version == 0 {
			c.mu.Unlock()
			return ownershell.ErrDenied
		}
	}
	c.highestEpoch = epoch
	c.active = false
	c.pending = plan
	c.mu.Unlock()
	for id, g := range plan.grants {
		c.mu.Lock()
		child := c.children[id]
		valid := c.pending == plan && c.revision == plan.revision && !c.terminal
		c.mu.Unlock()
		if !valid {
			return ownershell.ErrDenied
		}
		if err := child.peer.call(ctx, HelperFrame{Op: "prepare", Transaction: transaction, Key: key, Grant: g}); err != nil {
			return err
		}
	}
	return nil
}
func (c *retainedCoordinator) Commit(ctx context.Context, transaction string) error {
	c.mu.Lock()
	plan := c.pending
	if plan == nil && c.committed == transaction && c.active {
		c.mu.Unlock()
		return nil
	}
	if plan == nil || plan.transaction != transaction || c.revision != plan.revision || c.terminal || c.uncertain || c.phase != "running" {
		c.mu.Unlock()
		return ownershell.ErrDenied
	}
	c.mu.Unlock()
	for id := range plan.grants {
		c.mu.Lock()
		child := c.children[id]
		valid := c.pending == plan && c.revision == plan.revision && !c.terminal && child.closing == ""
		c.mu.Unlock()
		if !valid {
			return ownershell.ErrDenied
		}
		if err := child.peer.call(ctx, HelperFrame{Op: "commit", Transaction: transaction}); err != nil {
			_ = c.Suspend(ctx)
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending != plan || c.revision != plan.revision || c.terminal || c.uncertain {
		return ownershell.ErrDenied
	}
	c.binding = plan.binding
	c.link = plan.binding.ShellLink
	c.key = plan.key
	c.pending = nil
	c.active = true
	c.committed = transaction
	c.inventoryRevision = 0
	c.revision++
	for id, g := range plan.grants {
		child := c.children[id]
		child.grant = g
		child.input = 0
		child.resize = 0
		if err := c.registry.Activate(id); err != nil {
			c.active = false
			return err
		}
		c.startOutputLocked(ctx, child)
	}
	return nil
}
func (c *retainedCoordinator) Control(ctx context.Context, r RPC) error {
	c.mu.Lock()
	child := c.children[r.Grant.Grant.CustodyID]
	if !c.validLocked(child, r.Grant) {
		c.mu.Unlock()
		return ownershell.ErrDenied
	}
	peer := child.peer
	switch r.Op {
	case "credit":
		err := child.credit.Acknowledge(r.Offset)
		if err == nil {
			select {
			case child.creditReady <- struct{}{}:
			default:
			}
		}
		c.mu.Unlock()
		return err
	case "receipt":
		if r.Offset < child.receipt || r.Offset > child.credit.Acknowledged() {
			c.mu.Unlock()
			return ownershell.ErrDenied
		}
		child.receipt = r.Offset
		c.mu.Unlock()
		return peer.acknowledgeRetained(ctx, r.Grant.Grant.AccessEpoch, r.Offset)
	case "input":
		if len(r.Data) == 0 || len(r.Data) > ownershell.MaxFrame || r.Seq != child.input+1 {
			c.mu.Unlock()
			return ownershell.ErrDenied
		}
		child.input = r.Seq
	case "resize":
		if r.Seq != child.resize+1 {
			c.mu.Unlock()
			return ownershell.ErrDenied
		}
		child.resize = r.Seq
	case "renew":
	default:
		c.mu.Unlock()
		return ownershell.ErrDenied
	}
	c.mu.Unlock()
	err := peer.call(ctx, HelperFrame{Op: r.Op, Grant: r.Grant, Seq: r.Seq, Data: r.Data, Cols: r.Cols, Rows: r.Rows})
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.suspendLocked()
		return err
	}
	if child.closing != "" || c.terminal {
		return ownershell.ErrDenied
	}
	if r.Op == "renew" {
		child.grant = r.Grant
	}
	return nil
}
func (c *retainedCoordinator) Close(ctx context.Context, custody protocol.ShellCustody, transaction string) (protocol.CustodyCleanup, error) {
	c.mu.Lock()
	child := c.children[custody.Digest()]
	if !c.pinned(custody) || transaction == "" || child == nil || child.custody != custody {
		c.mu.Unlock()
		return protocol.CustodyCleanup{}, ownershell.ErrDenied
	}
	if child.cancel != nil {
		child.cancel()
	}
	if child.peer != nil && child.peer.stream != nil {
		child.peer.stream.suspend()
	}
	if child.closing == "" {
		child.closing = transaction
		_ = c.registry.BeginClose(custody.Digest(), transaction)
	}
	c.revision++
	c.inventoryRevision = 0
	peer := child.peer
	closed := child.closed
	ownTransaction := child.closing
	c.mu.Unlock()
	if !closed {
		if peer == nil {
			return protocol.CustodyCleanup{}, ownershell.ErrUnknown
		}
		if err := peer.call(ctx, HelperFrame{Op: "close"}); err != nil {
			return protocol.CustodyCleanup{}, err
		}
		if err := joinHelperPeers(ctx, []*helperPeer{peer}); err != nil {
			return protocol.CustodyCleanup{}, err
		}
		if err := c.joinOutput(ctx, []*retainedChild{child}); err != nil {
			return protocol.CustodyCleanup{}, err
		}
		peer.mu.Lock()
		clean := peer.clean
		peer.mu.Unlock()
		if !clean {
			return protocol.CustodyCleanup{}, ownershell.ErrUnknown
		}
		proof := protocol.CustodyCleanup{Version: 2, Custody: custody.Digest(), Scope: custody.PhysicalScope(), Transaction: ownTransaction, Responder: c.link.Daemon, ContainmentEmpty: true, PTYsClosed: true, Reaped: true, OutputJoined: true}
		if err := c.registry.Settle(proof, c.link.Daemon); err != nil {
			return protocol.CustodyCleanup{}, err
		}
		c.mu.Lock()
		child.closed = true
		c.mu.Unlock()
	}
	return protocol.CustodyCleanup{Version: 2, Custody: custody.Digest(), Scope: custody.PhysicalScope(), Transaction: transaction, Responder: c.link.Daemon, ContainmentEmpty: true, PTYsClosed: true, Reaped: true, OutputJoined: true}, nil
}
func (c *retainedCoordinator) CloseAll(ctx context.Context, reason string) error {
	c.mu.Lock()
	c.terminal = true
	children := c.suspendLocked()
	c.mu.Unlock()
	var failed error
	for _, child := range children {
		if _, err := c.Close(ctx, child.custody, reason); err != nil {
			failed = err
		}
	}
	return failed
}

// A helper failure fences its own generation. It does not assert that the OS
// identity changed or revoke healthy siblings; uncertain cleanup still consumes
// the physical reservation and blocks replacement writers.
func (c *retainedCoordinator) markChildClosingLocked(child *retainedChild, transaction string) {
	if child.closed {
		return
	}
	if child.closing == "" {
		child.closing = transaction
		_ = c.registry.BeginClose(child.custody.Digest(), transaction)
	}
	if child.cancel != nil {
		child.cancel()
	}
	if child.peer != nil && child.peer.stream != nil {
		child.peer.stream.suspend()
	}
	c.revision++
	c.inventoryRevision = 0
}
