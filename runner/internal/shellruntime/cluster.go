package shellruntime

import (
	"context"
)

// PrivilegedShellOpen is read after admission has entered syncing. It includes
// suspended custody and uncertain cleanup: absence of access is not writer death.
func (s *Service) PrivilegedShellOpen(session string) (open, known bool) {
	if s.bridged(session) {
		if !s.hostBridgeReady() {
			return false, false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		state, observed := s.hostObserved[session]
		return state.Privileged, observed
	}
	s.mu.Lock()
	entry := s.launches[session]
	var ordinary *Coordinator
	var retained *retainedCoordinator
	if entry != nil {
		ordinary, retained = entry.coordinator, entry.retained
	}
	s.mu.Unlock()
	if ordinary != nil {
		ordinary.sessionMu.Lock()
		for id := range ordinary.peers {
			if ordinary.grants[id].Grant.Privileged {
				ordinary.sessionMu.Unlock()
				return true, true
			}
		}
		ordinary.sessionMu.Unlock()
	}
	if retained != nil {
		retained.mu.Lock()
		defer retained.mu.Unlock()
		for _, child := range retained.children {
			if !child.closed && child.custody.Privileged {
				return true, true
			}
		}
	}
	return false, true
}

// ProcessPhase is a leaf operation under P3's outer workspace coordinator. The
// P5 mutex is distinct; no callback reacquires the outer coordinator from here.
func (s *Service) ProcessPhase(ctx context.Context, session string, freeze bool) error {
	if s.bridged(session) {
		return s.bridgeCall(ctx, "process_phase", struct {
			Session string
			Freeze  bool
		}{session, freeze}, nil)
	}
	s.mu.Lock()
	launch := s.launches[session]
	var c *Coordinator
	if launch != nil {
		c = launch.coordinator
	}
	s.mu.Unlock()
	if c == nil {
		return nil
	} // no helpers have ever been created for this registration
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	op := "thaw"
	if freeze {
		op = "freeze"
	}
	for id, p := range c.peers {
		shellOp := op
		if freeze && c.grants[id].Grant.Privileged {
			shellOp = "sync"
		}
		if err := p.call(ctx, HelperFrame{Op: shellOp, Grant: c.grants[id]}); err != nil {
			c.fenced = true
			return err
		}
	}
	return nil
}

// Call executes a correlated private pod request through the same bounded
// queues and grant/key/lease checks as the enrolled desktop transport.
func (s *Service) Call(ctx context.Context, request RPC) (RPC, error) {
	if s.bridged(request.SessionID) {
		if err := s.flushHostControl(ctx, request.SessionID); err != nil {
			return RPC{}, err
		}
	}
	return s.localCall(ctx, request)
}

// ResolveClusterIdentity resolves the fixed image account, never a request UID.
func ResolveClusterIdentity(cwd string) (Identity, error) { return resolveAccountIdentity(cwd, 1000) }

// ProveRecordedShellsEmpty checks every coordinator created in this physical
// pod process, including retired connections and failed configurations. Entries
// disappear from peers/reserved only after exact helper cleanup receipts. This
// is a leaf proof, never a PID-name scan or an empty newly allocated map.
func (s *Service) ProveRecordedShellsEmpty(session string) error {
	if s.bridged(session) {
		return s.bridgeBackground("empty", session, nil)
	}
	s.mu.Lock()
	entry := s.launches[session]
	if entry == nil {
		s.mu.Unlock()
		return ErrClosed
	}
	history := append([]*Coordinator(nil), entry.containmentHistory...)
	retained := append([]*retainedCoordinator(nil), entry.retainedHistory...)
	if entry.retained != nil {
		retained = append(retained, entry.retained)
	}
	s.mu.Unlock()
	for _, c := range retained {
		c.mu.Lock()
		empty := true
		for _, child := range c.children {
			empty = empty && child.closed
		}
		c.mu.Unlock()
		if !empty {
			return ErrClosed
		}
	}
	for _, c := range history {
		c.sessionMu.Lock()
		empty := len(c.peers) == 0 && len(c.reserved) == 0
		c.sessionMu.Unlock()
		if !empty {
			return ErrClosed
		}
	}
	return nil
}

// UpdateManifest moves a registered launch to a committed membership revision.
// Host agent shadows may advance offline; shell forwarding separately retires
// old keeper custody before registering that digest. Local runtime callers
// must prove no shell exists under their lifecycle lock. Root/generation stay pinned.
func (s *Service) UpdateManifest(session, old, next string) error {
	if s.bridged(session) {
		s.mu.Lock()
		e := s.launches[session]
		if e == nil || next == "" || e.binding.ManifestDigest != old {
			s.mu.Unlock()
			return bridgeFailure("workspace_manifest", ErrClosed)
		}
		e.binding.ManifestDigest = next
		s.mu.Unlock()
		s.queueHostControl(session)
		// Commit the agent shadow even when the keeper is offline. Every shell
		// use reconciles its registration before forwarding; old custody cannot
		// acquire the new manifest merely because the agent committed it.
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.launches[session]
	if entry == nil || next == "" || entry.binding.ManifestDigest != old {
		return ErrClosed
	}
	entry.binding.ManifestDigest = next
	if entry.coordinator != nil {
		entry.coordinator.binding.ManifestDigest = next
	}
	if entry.retained != nil {
		entry.retained.mu.Lock()
		entry.retained.binding.ManifestDigest = next
		entry.retained.mu.Unlock()
	}
	return nil
}
