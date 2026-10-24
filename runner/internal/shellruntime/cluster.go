package shellruntime

import (
	"context"
)

// ProcessPhase is a leaf operation under P3's outer workspace coordinator. The
// P5 mutex is distinct; no callback reacquires the outer coordinator from here.
func (s *Service) ProcessPhase(ctx context.Context, session string, freeze bool) error {
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
		if err := p.call(ctx, HelperFrame{Op: op, Grant: c.grants[id]}); err != nil {
			c.fenced = true
			return err
		}
	}
	return nil
}

// Call executes a correlated private pod request through the same bounded
// queues and grant/key/lease checks as the enrolled desktop transport.
func (s *Service) Call(ctx context.Context, request RPC) (RPC, error) {
	return s.localCall(ctx, request)
}

// ResolveClusterIdentity resolves the fixed image account, never a request UID.
func ResolveClusterIdentity(cwd string) (Identity, error) { return resolveAccountIdentity(cwd, 1000) }

// ProveRecordedShellsEmpty checks every coordinator created in this physical
// pod process, including retired connections and failed configurations. Entries
// disappear from peers/reserved only after exact helper cleanup receipts. This
// is a leaf proof, never a PID-name scan or an empty newly allocated map.
func (s *Service) ProveRecordedShellsEmpty(session string) error {
	s.mu.Lock()
	entry := s.launches[session]
	if entry == nil {
		s.mu.Unlock()
		return ErrClosed
	}
	history := append([]*Coordinator(nil), entry.containmentHistory...)
	s.mu.Unlock()
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
