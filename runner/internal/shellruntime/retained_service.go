package shellruntime

import (
	"context"
	"crypto/ed25519"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// ShellHello belongs to this Service/daemon process. A stale socket cannot
// choose the expected challenge for the next authenticated welcome.
func (s *Service) ShellHello() protocol.ShellLinkHello {
	if s.bridge() != nil {
		if s.HostKeeperReconnects() {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.hostHello
		}
		var h protocol.ShellLinkHello
		_ = s.bridgeBackground("hello", nil, &h)
		return h
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistentHello.Challenge == "" {
		challenge, _ := newHelperID()
		s.persistentHello = protocol.ShellLinkHello{Version: 2, Daemon: s.persistentDaemon, Challenge: challenge}
	}
	return s.persistentHello
}

// AcceptShellLink is a local authenticated-transport hook, never shell RPC.
// Invalid or replaced custody authority closes old #269 helpers, not their
// reservations: those remain until the exact physical cleanup succeeds.
func (s *Service) AcceptShellLink(ctx context.Context, h protocol.ShellLinkHello, w *protocol.ShellLinkWelcome) error {
	if s.bridge() != nil {
		if err := s.bridgeCall(ctx, "link", hostLink{h, w}, nil); err != nil {
			return err
		}
		if w != nil && w.Matches(h, s.now()) {
			s.mu.Lock()
			if s.hostRefreshDone != nil && s.hostRefreshHello == h {
				close(s.hostRefreshDone)
				s.hostRefreshDone = nil
			}
			s.mu.Unlock()
		}
	}
	s.link.Lock()
	defer s.link.Unlock()
	s.mu.Lock()
	expected := s.persistentHello
	// A prior socket's deferred callback or timer cannot affect a new hello.
	if h != expected {
		s.mu.Unlock()
		return nil
	}
	if w == nil {
		s.persistentLink = nil
		var suspended []*retainedCoordinator
		for _, entry := range s.launches {
			if entry.retained != nil {
				suspended = append(suspended, entry.retained)
			}
		}
		s.mu.Unlock()
		var failed error
		for _, c := range suspended {
			if err := c.Suspend(ctx); err != nil {
				failed = err
			}
		}
		return failed
	}
	valid := w != nil && h == expected && w.Matches(expected, s.now())
	var closing []*retainedCoordinator
	for _, entry := range s.launches {
		if entry.retained != nil && (!valid || !w.SameCustodian(entry.retained.link)) {
			closing = append(closing, entry.retained)
		}
	}
	if valid {
		accepted := *w
		s.persistentLink = &accepted
	} else {
		s.persistentLink = nil
	}
	s.mu.Unlock()
	var failed error
	for _, c := range closing {
		if err := c.CloseAll(ctx, "custodian-replaced"); err != nil {
			failed = err
			continue
		}
		s.mu.Lock()
		for _, entry := range s.launches {
			if entry.retained == c && len(entry.retainedHistory) < 128 {
				entry.retainedHistory = append(entry.retainedHistory, c)
				entry.retained = nil
				entry.persistentIdentity = nil
			}
		}
		s.mu.Unlock()
	}
	return failed
}
func (s *Service) persistentBinding(entry *registered, b ownershell.Binding) ownershell.Binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistentBindingLocked(entry, b)
}
func (s *Service) persistentBindingLocked(entry *registered, b ownershell.Binding) ownershell.Binding {
	if s.persistentLink == nil || entry.persistentIdentity == nil || s.persistentLink.Desktop.OwnerID != b.Owner {
		return b
	}
	if b.Runtime != "host" {
		return b
	}
	if caps := s.capabilities.Load(); caps == nil || !caps.server.SupportsPersistence(b.Runtime) || !caps.runtime.SupportsPersistence(b.Runtime) {
		if entry.retained == nil || !entry.retainedPins.SameCustodian(*s.persistentLink) {
			return b
		}
		// Existing helpers already proved their clock and account in this same
		// daemon/runner incarnation. Private inspect rechecks their own runtime.
		// A failed installation probe cannot authorize replacement helpers.
		b.ServerCapabilities = entry.retainedServerCaps
		b.RuntimeCapabilities = entry.retainedRuntimeCaps
		b.PersistenceDegraded = true
	}
	b.ShellLink = *s.persistentLink
	b.IdentityDigest = RuntimeIdentityDigest(*entry.persistentIdentity)
	b.Container = entry.command.Container
	b.ServerCapabilities.ShellPersistence = 2
	b.RuntimeCapabilities.ShellPersistence = 2
	// The non-host guard above is required even for retained fallback bindings.
	b.ServerCapabilities.PersistentHost = true
	b.RuntimeCapabilities.PersistentHost = true
	return b
}
func (s *Service) executeRetained(ctx context.Context, entry *registered, r RPC) (result RPC) {
	result = RPC{Version: 2, Type: "owner_shell_result", SessionID: r.SessionID, RequestID: r.RequestID, Op: r.Op, ErrorCode: string(ownershell.ReasonRuntimeBinding)}
	b := s.persistentBinding(entry, s.negotiatedBinding(entry.binding))
	if b.Runtime != "host" {
		return result
	}
	if r.Op == "consent_challenge" || r.Op == "consent_accept" || r.Op == "consent_withdraw" {
		return s.retainedConsentRPC(ctx, entry, b, r)
	}
	if r.Op == "describe" {
		result.Binding = b
		if entry.persistentIdentity != nil {
			result.Identity = *entry.persistentIdentity
		}
		result.OK = b.ShellLink.Version == 2
		return result
	}
	if r.Op == "configure" {
		if entry.retained != nil {
			c := entry.retained
			c.mu.Lock()
			sameBinding := c.binding
			sameBinding.PersistenceDegraded = b.PersistenceDegraded
			sameBinding.AccessSuspended = b.AccessSuspended
			sameBinding.CustodyClosing = b.CustodyClosing
			sameBinding.Folder = b.Folder
			same := sameBinding == b && c.key.Equal(r.Key) && c.command.Policy == r.Policy && !c.terminal
			c.mu.Unlock()
			result.OK = same
			result.Binding = b
			return result
		}
		if entry.coordinator != nil {
			old := entry.coordinator
			old.sessionMu.Lock()
			empty := len(old.peers) == 0 && len(old.reserved) == 0
			old.sessionMu.Unlock()
			if empty {
				s.mu.Lock()
				if len(entry.containmentHistory) < 128 {
					entry.containmentHistory = append(entry.containmentHistory, old)
					entry.coordinator = nil
				}
				s.mu.Unlock()
			}
		}
		entry.mu.Lock()
		ready := entry.phase == "running" && !entry.fenced && !entry.terminal
		entry.mu.Unlock()
		if entry.coordinator != nil || entry.persistentIdentity == nil || b.ShellLink.Version != 2 || r.Binding != b || !ready || len(r.Key) != ed25519.PublicKeySize || r.Policy.Version != 1 || !r.Policy.Enabled || r.Policy.Revision == 0 || r.Policy.Revision < entry.highestRevision {
			return result
		}
		if entry.command.SourceLive != nil && !entry.command.SourceLive() {
			return result
		}
		s.mu.Lock()
		entry.persistentPreparing = true
		s.mu.Unlock()
		defer func() { s.mu.Lock(); entry.persistentPreparing = false; s.mu.Unlock() }()
		// There is no retained coordinator or helper in this Service yet. Only the
		// runtime owner's independent verifier may clear orphan writers before the
		// first allocation. This never runs on a reconnect/rekey of live custody.
		if err := entry.reconcile(ctx); err != nil {
			result.ErrorCode = string(ownershell.ReasonRuntimeCleanup)
			return result
		}
		command := entry.command
		command.Identity = *entry.persistentIdentity
		command.Root = b.Root
		command.Policy = r.Policy
		command.identityInvalidated = func() { invalidateRetainedConsents(entry) }
		command.persistentConsent = func(c protocol.ShellCustody) bool { return s.consumeRetainedConsent(entry, c) }
		c, err := newRetainedCoordinator(entry.mu, b, command, r.Key, s.custody, s.now, s.outputSender())
		if err != nil {
			return result
		}
		entry.mu.Lock()
		if entry.phase != "running" || entry.fenced || entry.terminal {
			entry.mu.Unlock()
			return result
		}
		s.mu.Lock()
		entry.retained = c
		entry.retainedPins = b.ShellLink
		entry.retainedServerCaps = b.ServerCapabilities
		entry.retainedRuntimeCaps = b.RuntimeCapabilities
		s.mu.Unlock()
		entry.mu.Unlock()
		entry.highestRevision = r.Policy.Revision
		result.OK = true
		result.Binding = b
		return result
	}
	if r.Op == "close" && r.Custody != nil {
		proof, err := s.closeRetainedCustody(ctx, entry, *r.Custody, r.Transaction)
		result.OK = err == nil
		if result.OK {
			result.ErrorCode = ""
			result.CustodyCleanup = &proof
		}
		return result
	}
	c := entry.retained
	if c == nil {
		return result
	}
	var err error
	switch r.Op {
	case "open":
		if b.PersistenceDegraded {
			return result
		}
		if r.Custody == nil {
			return result
		}
		start := func() error { return c.Open(ctx, *r.Custody, r.Grant, r.Window) }
		if entry.atomicStart != nil {
			err = entry.atomicStart(ctx, start)
		} else {
			err = start()
		}
		result.AllocationRefused = ownershell.BeforeAllocation(err)
	case "suspend":
		err = c.Suspend(ctx)
	case "inventory":
		if r.Inventory == nil {
			return result
		}
		var view ownershell.CustodyInventory
		view, err = c.Inventory(ctx, *r.Inventory)
		if err == nil {
			result.Inventory = &view
		}
	case "prepare":
		expected := b
		expected.AccessSuspended = r.Binding.AccessSuspended
		expected.CustodyClosing = r.Binding.CustodyClosing
		if r.Inventory == nil || r.Binding != expected {
			return result
		}
		err = c.Prepare(ctx, b, r.Transaction, *r.Inventory, r.Key, r.Grants)
	case "commit":
		err = c.Commit(ctx, r.Transaction)
	case "input", "resize", "renew", "credit", "receipt":
		err = c.Control(ctx, r)
	case "close":
		if r.Custody == nil {
			return result
		}
		var proof protocol.CustodyCleanup
		proof, err = c.Close(ctx, *r.Custody, r.Transaction)
		if err == nil {
			result.CustodyCleanup = &proof
		}
	default:
		return result
	}
	result.OK = err == nil
	if result.OK {
		result.ErrorCode = ""
	} else {
		result.ErrorCode = string(ownershell.RefusalReason(err))
		_, result.StartCause = startDiagnostic(err)
	}
	return result
}
func (s *Service) executeRetainedLifecycle(ctx context.Context, entry *registered, r RPC) (result RPC) {
	result = RPC{Version: 1, Type: "owner_shell_result", SessionID: r.SessionID, RequestID: r.RequestID, Op: r.Op, ErrorCode: string(ownershell.ReasonRuntimeFenced)}
	c := entry.retained
	expected := ownershell.SupervisorIdentity(entry.binding, ownershell.SignedGrant{}).Launch
	if r.Launch != expected {
		return result
	}
	switch r.Op {
	case "admit", "recover":
		return result
	case "close_shell":
		c.mu.Lock()
		var child *retainedChild
		for _, candidate := range c.children {
			if candidate.custody.Shell == r.Shell {
				child = candidate
				break
			}
		}
		c.mu.Unlock()
		if child == nil {
			return result
		}
		if _, err := c.Close(ctx, child.custody, "native-close"); err != nil {
			return result
		}
		result.Cleanup = &protocol.SupervisorCleanup{Launch: expected, ShellGeneration: r.Shell, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
		result.OK = true
	case "close_launch":
		if err := c.CloseAll(ctx, "native-close-launch"); err != nil {
			return result
		}
		result.Cleanup = &protocol.SupervisorCleanup{Launch: expected, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
		result.OK = true
	case "phase", "freeze":
		phase := r.Phase
		if r.Op == "freeze" {
			phase = "syncing"
		}
		if !phase.Valid() {
			return result
		}
		c.mu.Lock()
		c.phase = phase
		entry.phase = phase
		if phase != "running" && phase != "syncing" {
			entry.terminal = true
			entry.fenced = true
		}
		c.suspendLocked()
		c.mu.Unlock()
		switch phase {
		case "running":
			c.mu.Lock()
			var children []*retainedChild
			for _, child := range c.children {
				if !child.closed {
					children = append(children, child)
				}
			}
			c.mu.Unlock()
			for _, child := range children {
				if child.peer == nil || child.peer.call(ctx, HelperFrame{Op: "thaw"}) != nil {
					return result
				}
			}
		case "syncing":
			until := s.now().Add(time.Duration(protocol.DefaultWorkspaceBudget().SyncSeconds) * time.Second)
			c.mu.Lock()
			var children []*retainedChild
			for _, child := range c.children {
				if !child.closed {
					children = append(children, child)
					lifetime := time.Unix(child.custody.LifetimeUntil, 0)
					if lifetime.Before(until) {
						until = lifetime
					}
				}
			}
			c.mu.Unlock()
			for _, child := range children {
				op := "freeze"
				if child.custody.Privileged {
					op = "sync"
				}
				if child.peer == nil || child.peer.call(ctx, HelperFrame{Op: op, SyncUntil: until.Unix()}) != nil {
					return result
				}
			}
			result.FrozenUntil = until.Unix()
		default:
			if err := c.CloseAll(ctx, "native-phase"); err != nil {
				return result
			}
		}
		// The local barrier is installed before best-effort phase publication.
		_ = s.send(RPC{Version: 1, Type: "owner_shell_phase", SessionID: r.SessionID, Launch: expected, Phase: phase})
		result.OK = true
	}
	if result.OK {
		result.ErrorCode = ""
	}
	return result
}

func (s *Service) ensurePersistentIdentity(ctx context.Context, entry *registered) {
	if entry.persistentIdentity != nil {
		return
	}
	s.mu.Lock()
	personal := s.persistentLink != nil && s.persistentLink.Desktop.OwnerID == entry.binding.Owner
	s.mu.Unlock()
	caps := s.capabilities.Load()
	if !personal || caps == nil || !caps.server.SupportsPersistence(entry.binding.Runtime) || !caps.runtime.SupportsPersistence(entry.binding.Runtime) {
		return
	}
	command := entry.command
	command.Root = entry.binding.Root
	if actual, err := s.probePersistent(ctx, command); err == nil {
		s.mu.Lock()
		entry.persistentIdentity = &actual
		s.mu.Unlock()
	}
}

// retainedBarrier is local P2 authority. It never waits for the session queue,
// helper IO, or a remote phase signal. Closing invalidates any pending rekey.
func (s *Service) retainedBarrier(l protocol.SupervisorLaunch, phase protocol.SupervisorPhase, shell string) {
	s.mu.Lock()
	entry := s.launches[l.SessionID]
	var c *retainedCoordinator
	eligible := false
	if entry != nil && ownershell.SupervisorIdentity(entry.binding, ownershell.SignedGrant{}).Launch == l {
		c = entry.retained
		eligible = c != nil || entry.persistentPreparing
	}
	s.mu.Unlock()
	if !eligible || entry == nil || entry.mu == nil {
		return
	}
	entry.mu.Lock()
	if shell == "" {
		entry.phase = phase
		if phase != "running" && phase != "syncing" {
			entry.fenced = true
			entry.terminal = true
		}
	}
	entry.mu.Unlock()
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if shell != "" {
		for id, child := range c.children {
			if child.custody.Shell != shell || child.closed {
				continue
			}
			if child.closing == "" {
				child.closing = "native-close"
				_ = c.registry.BeginClose(id, child.closing)
			}
		}
	} else {
		c.phase = phase
		if phase != "running" && phase != "syncing" {
			c.terminal = true
		}
	}
	c.suspendLocked()
}

func (s *Service) closeRetainedCustody(ctx context.Context, entry *registered, custody protocol.ShellCustody, transaction string) (protocol.CustodyCleanup, error) {
	if custody.Validate() != nil || transaction == "" || len(transaction) > 256 {
		return protocol.CustodyCleanup{}, ownershell.ErrDenied
	}
	candidates := append([]*retainedCoordinator(nil), entry.retainedHistory...)
	if entry.retained != nil {
		candidates = append(candidates, entry.retained)
	}
	for _, c := range candidates {
		c.mu.Lock()
		child := c.children[custody.Digest()]
		known := child != nil && child.custody == custody
		c.mu.Unlock()
		if known {
			return c.Close(ctx, custody, transaction)
		}
	}
	b := entry.binding
	s.mu.Lock()
	link := s.persistentLink
	s.mu.Unlock()
	if link == nil || link.Desktop.OwnerID != custody.Owner || link.Desktop.DesktopID != custody.Desktop || b.Owner != custody.Owner || b.Session != custody.Session || b.Launch != custody.Launch || b.Runtime != custody.Runtime || b.RuntimeGeneration != custody.RuntimeGeneration || b.Root != custody.Root || b.ManifestDigest != custody.Manifest || b.Container != custody.Container || entry.coordinator != nil {
		return protocol.CustodyCleanup{}, ownershell.ErrDenied
	}
	for _, c := range candidates {
		c.mu.Lock()
		live := false
		for _, child := range c.children {
			live = live || !child.closed
		}
		c.mu.Unlock()
		if live {
			return protocol.CustodyCleanup{}, ownershell.ErrUnknown
		}
	}
	// This is a cold orphan, never an adoption. There are no current helper
	// writers. The runtime owner's independent verifier must prove the entire
	// exact prepared namespace empty; a missing inventory or daemon is no proof.
	if err := entry.reconcile(ctx); err != nil {
		return protocol.CustodyCleanup{}, err
	}
	proof := protocol.CustodyCleanup{Version: 2, Custody: custody.Digest(), Scope: custody.PhysicalScope(), Transaction: transaction, Responder: link.Daemon, ContainmentEmpty: true, PTYsClosed: true, Reaped: true, OutputJoined: true}
	for _, claim := range s.custody.Snapshot(custody.PhysicalScope(), "cleanup").Entries {
		if claim.Custody != custody {
			continue
		}
		own := claim.Transaction
		if own == "" {
			own = transaction
			if s.custody.BeginClose(custody.Digest(), own) != nil {
				return protocol.CustodyCleanup{}, ownershell.ErrUnknown
			}
		}
		receipt := proof
		receipt.Transaction = own
		if s.custody.Settle(receipt, link.Daemon) != nil {
			return protocol.CustodyCleanup{}, ownershell.ErrUnknown
		}
	}
	return proof, nil
}

// A typed runtime-owner observation distinguishes a local metadata outage from
// a confirmed source replacement. Neither can authorize access; only the latter
// is terminal. This check performs no IO while holding the shell-state mutex.
func (s *Service) checkRetainedSource(ctx context.Context, entry *registered) error {
	if entry.command.SourceState == nil {
		if entry.command.SourceLive == nil {
			return nil
		}
		if entry.command.SourceLive() {
			return nil
		}
		if entry.retained != nil {
			_ = entry.retained.Suspend(ctx)
		}
		return ownershell.ErrUnknown
	}
	same, known := entry.command.SourceState()
	if known && !same {
		invalidateRetainedConsents(entry)
	}
	if same && known {
		return nil
	}
	if entry.retained != nil {
		if known {
			invalidateRetainedConsents(entry)
			entry.retained.mu.Lock()
			entry.retained.identityRevoked = true
			entry.retained.mu.Unlock()
			_ = entry.retained.CloseAll(ctx, "source-replaced")
		} else {
			_ = entry.retained.Suspend(ctx)
		}
	}
	if known {
		return ownershell.Refuse(ownershell.ReasonRuntimeIdentity)
	}
	return ownershell.ErrUnknown
}

// The private helper verifies identity inside its own runtime. A v1 envelope
// describe is still used for mixed-version discovery, but must never run the
// legacy hostname comparison or whole-launch orphan recovery on retained peers.
func (s *Service) describeRetained(ctx context.Context, entry *registered, r RPC) RPC {
	result := RPC{Version: r.Version, Type: "owner_shell_result", SessionID: r.SessionID, RequestID: r.RequestID, Op: r.Op, ErrorCode: string(ownershell.ReasonUnavailable)}
	c := entry.retained
	c.mu.Lock()
	if c.terminal {
		c.mu.Unlock()
		result.ErrorCode = string(ownershell.ReasonRuntimeIdentity)
		return result
	}
	var peers []*helperPeer
	suspended := !c.active || c.pending != nil
	expectedEpoch := map[*helperPeer]uint64{}
	for _, child := range c.children {
		if child.closed || child.closing != "" {
			continue
		}
		if child.peer == nil {
			c.mu.Unlock()
			return result
		}
		peers = append(peers, child.peer)
		expectedEpoch[child.peer] = child.grant.Grant.AccessEpoch
	}
	c.mu.Unlock()
	for _, peer := range peers {
		if peer.call(ctx, HelperFrame{Op: "inspect"}) != nil {
			c.mu.Lock()
			terminal := c.terminal
			c.mu.Unlock()
			if terminal {
				result.ErrorCode = string(ownershell.ReasonRuntimeIdentity)
			}
			return result
		}
		suspended = suspended || !peer.inspectedActive.Load() || peer.inspectedEpoch.Load() != expectedEpoch[peer]
	}
	result.Binding = s.persistentBinding(entry, s.negotiatedBinding(entry.binding))
	result.Binding.AccessSuspended = suspended
	c.mu.Lock()
	result.Binding.AccessSuspended = result.Binding.AccessSuspended || !c.active
	result.Binding.Live = !c.terminal && (c.phase == "running" || c.phase == "syncing")
	scope := c.scope()
	c.mu.Unlock()
	i := 0
	for _, claim := range s.custody.Snapshot(scope, "describe").Entries {
		if claim.State == ownershell.CustodyClosing && i < len(result.Binding.CustodyClosing) {
			result.Binding.CustodyClosing[i] = claim.Custody.Digest()
			i++
		}
	}
	if entry.persistentIdentity != nil {
		result.Identity = *entry.persistentIdentity
	}
	result.OK = true
	result.ErrorCode = ""
	return result
}

func invalidateRetainedConsents(entry *registered) {
	entry.consentMu.Lock()
	defer entry.consentMu.Unlock()
	for _, consent := range entry.consents {
		consent.revoked = true
	}
}

// Old generations remain terminal. Once every old helper has exact cleanup
// proof, a new identity epoch may be described for a fresh per-child affirmation.
// This never clears a native lifecycle fence or reuses an old acceptance.
func (s *Service) resetRetainedIdentity(ctx context.Context, entry *registered) {
	c := entry.retained
	if c == nil {
		return
	}
	c.mu.Lock()
	ready := c.identityRevoked && c.terminal && entry.phase == "running" && !entry.terminal && !entry.fenced
	for _, child := range c.children {
		ready = ready && child.closed
	}
	c.mu.Unlock()
	if !ready || len(entry.retainedHistory) >= 128 {
		return
	}
	if err := s.checkRetainedSource(ctx, entry); err != nil {
		return
	}
	nonce, err := newHelperID()
	if err != nil {
		return
	}
	entry.mu.Lock()
	if entry.phase != "running" || entry.terminal || entry.fenced {
		entry.mu.Unlock()
		return
	}
	s.mu.Lock()
	if entry.retained != c {
		s.mu.Unlock()
		entry.mu.Unlock()
		return
	}
	entry.retainedHistory = append(entry.retainedHistory, c)
	entry.retained = nil
	entry.persistentIdentity = nil
	entry.binding.Connection = nonce
	entry.binding.Audience = ownershell.RuntimeAudience(nonce, entry.binding.RuntimeGeneration)
	entry.binding.Consent = ""
	entry.binding.HostConsentValid = false
	s.mu.Unlock()
	entry.mu.Unlock()
	invalidateRetainedConsents(entry)
}
