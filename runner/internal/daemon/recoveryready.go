package daemon

import "context"

// RecoveryRequiredSessions distinguishes delegated records from legacy local
// discovery: a missing authoritative child row cannot be recreated as a root.
func (h *AgentHost) RecoveryRequiredSessions() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var ids []string
	for id, session := range h.sessions {
		if session.requireRecoveryReady {
			ids = append(ids, id)
		}
	}
	for id, pending := range h.adopting {
		if pending.rec.RequireRecoveryReady {
			if _, live := h.sessions[id]; !live {
				ids = append(ids, id)
			}
		}
	}
	return mergeSessionIDs(ids, h.cfg.Workspaces.PendingIDs())
}

// RecoveryPhase is a current websocket link's recovery-policy handshake.
// Legacy peers never authorize marked delegated recovery.
type RecoveryPhase string

const (
	RecoveryUnknown      RecoveryPhase = "unknown"
	RecoveryHeld         RecoveryPhase = "held"
	RecoveryReady        RecoveryPhase = "ready"
	RecoveryDisconnected RecoveryPhase = "disconnected"
	RecoveryLegacy       RecoveryPhase = "legacy"
)

type RecoveryState struct {
	Generation uint64
	Phase      RecoveryPhase
}

// SetRecoveryStateHandler registers a fast state-only callback before Run.
// It is called synchronously, before connect callbacks or later socket frames.
func (c *WSClient) SetRecoveryStateHandler(fn func(RecoveryState)) {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	c.recoveryHandler = fn
	if fn != nil {
		fn(c.recoveryState)
	}
}

func (c *WSClient) beginRecoveryLink() uint64 {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	c.recoveryState = RecoveryState{Generation: c.recoveryState.Generation + 1, Phase: RecoveryUnknown}
	if c.recoveryHandler != nil {
		c.recoveryHandler(c.recoveryState)
	}
	return c.recoveryState.Generation
}

func (c *WSClient) noteRecoveryLink(generation uint64, phase RecoveryPhase) {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if generation != c.recoveryState.Generation || c.recoveryState.Phase == RecoveryDisconnected {
		return
	}
	// Only an explicitly negotiated held link can become ready. Duplicate or
	// unsolicited frames never upgrade a legacy server or resurrect a dead link.
	if phase == RecoveryReady && c.recoveryState.Phase != RecoveryHeld {
		return
	}
	c.recoveryState.Phase = phase
	if c.recoveryHandler != nil {
		c.recoveryHandler(c.recoveryState)
	}
}

// NoteRecoveryState invalidates authorization on reconnect/disconnect. Stale
// callbacks cannot reopen a newer link. It performs no I/O or driver calls.
func (h *AgentHost) NoteRecoveryState(state RecoveryState) {
	h.recoveryMu.Lock()
	defer h.recoveryMu.Unlock()
	if state.Generation < h.recovery.Generation || (state.Generation == h.recovery.Generation && h.recovery.Phase == RecoveryDisconnected) {
		return
	}
	h.recovery = state
	if h.recoveryChanged != nil {
		close(h.recoveryChanged)
	}
	h.recoveryChanged = make(chan struct{})
}

func (h *AgentHost) recoveryInputReady() bool {
	h.recoveryMu.Lock()
	defer h.recoveryMu.Unlock()
	return h.recovery.Phase == RecoveryReady
}

// waitRecoveryReady runs on an engine worker, never the websocket read pump.
// Driver queues remain bounded; cancellation drops no work into a killed engine.
func (h *AgentHost) waitRecoveryReady(ctx context.Context) {
	for {
		h.recoveryMu.Lock()
		ready, changed := h.recovery.Phase == RecoveryReady, h.recoveryChanged
		h.recoveryMu.Unlock()
		if ready {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
	}
}

func (d *claudeCodeDriver) SetRecoveryInputGate(wait func(context.Context), ready func() bool) {
	d.sm.Lock()
	d.steer.recoveryInputGate = wait
	d.sm.Unlock()
}

func (d *codexDriver) SetRecoveryInputGate(wait func(context.Context), ready func() bool) {
	d.mu.Lock()
	d.recoveryInputGate, d.recoveryInputReady = wait, ready
	d.mu.Unlock()
}

func (d *hermesDriver) SetRecoveryInputGate(wait func(context.Context), ready func() bool) {
	d.mu.Lock()
	d.recoveryInputGate = wait
	d.mu.Unlock()
}

func (d *openclawDriver) SetRecoveryInputGate(wait func(context.Context), ready func() bool) {
	d.mu.Lock()
	d.recoveryInputGate = wait
	d.mu.Unlock()
}
