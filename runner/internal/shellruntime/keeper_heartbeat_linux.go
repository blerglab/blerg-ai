package shellruntime

// Heartbeats use only try-lock observations. A busy workspace is conservatively
// open, never permission to change its membership, and never a global liveness
// failure. Actual process/socket silence still fails the bounded wire heartbeat.
func (k *KeeperRuntime) heartbeatSnapshot() []hostObservation {
	s := k.service
	if !s.mu.TryLock() {
		old := k.observed.Load()
		if old == nil {
			return nil
		}
		result := append([]hostObservation(nil), (*old)...)
		for i := range result {
			result[i].Open = true
			result[i].Privileged = true
		}
		return result
	}
	type entry struct {
		id       string
		classes  []*retainedCoordinator
		ordinary []*Coordinator
	}
	entries := make([]entry, 0, len(s.launches))
	for id, e := range s.launches {
		classes := append([]*retainedCoordinator(nil), e.retainedHistory...)
		if e.retained != nil {
			classes = append(classes, e.retained)
		}
		ordinary := append([]*Coordinator(nil), e.containmentHistory...)
		if e.coordinator != nil {
			ordinary = append(ordinary, e.coordinator)
		}
		entries = append(entries, entry{id, classes, ordinary})
	}
	s.mu.Unlock()
	result := make([]hostObservation, 0, len(entries))
	for _, e := range entries {
		state := hostObservation{Workspace: e.id}
		for _, c := range e.classes {
			if !c.mu.TryLock() {
				state.Open, state.Privileged = true, true
				continue
			}
			for _, child := range c.children {
				if !child.closed {
					state.Open = true
					state.Privileged = state.Privileged || child.custody.Privileged
				}
			}
			c.mu.Unlock()
		}
		for _, c := range e.ordinary {
			if !c.sessionMu.TryLock() {
				state.Open, state.Privileged = true, true
				continue
			}
			state.Open = state.Open || len(c.peers) > 0 || len(c.reserved) > 0
			for id := range c.peers {
				state.Privileged = state.Privileged || c.grants[id].Grant.Privileged
			}
			c.sessionMu.Unlock()
		}
		result = append(result, state)
	}
	k.observed.Store(&result)
	return result
}
