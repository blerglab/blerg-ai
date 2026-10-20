package daemon

import (
	"context"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Ready distinguishes an authoritative empty inventory from startup/unavailable.
// Generation identifies a native process, never a model turn.
type mcpProbe struct {
	Servers    []protocol.MCPServerStatus
	Generation uint64
	Ready      bool
}
type mcpStatusSource interface {
	MCPStatus(context.Context) (mcpProbe, error)
	MCPGeneration() uint64
}
type mcpTrackerOptions struct {
	Wait, Poll, Track time.Duration
	FirstUsed         bool
}
type mcpGateRequest struct {
	ctx  context.Context
	done chan struct{}
}
type mcpProbeResult struct {
	probe mcpProbe
	err   error
}
type mcpTracker struct {
	gateMu   sync.Mutex
	gates    chan mcpGateRequest
	releases chan struct{}
	done     chan struct{}
}

func parseMCPPromptWait(raw string) time.Duration {
	if raw == "" {
		return 30 * time.Second
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 30 * time.Second
	}
	return time.Duration(min(n, 30)) * time.Second
}
func newMCPTracker(ctx context.Context, engine string, source mcpStatusSource, emitter agent.Emitter, opts mcpTrackerOptions) *mcpTracker {
	if opts.Poll <= 0 {
		opts.Poll = 2 * time.Second
	}
	if opts.Track <= 0 {
		opts.Track = 120 * time.Second
	}
	t := &mcpTracker{gates: make(chan mcpGateRequest), releases: make(chan struct{}, 1), done: make(chan struct{})}
	go t.run(ctx, engine, source, emitter, opts)
	return t
}
func (t *mcpTracker) beforeFirstPrompt(ctx context.Context) {
	t.gateMu.Lock()
	defer t.gateMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	req := mcpGateRequest{ctx: ctx, done: make(chan struct{})}
	select {
	case t.gates <- req:
	case <-ctx.Done():
		return
	case <-t.done:
		return
	}
	select {
	case <-req.done:
	case <-ctx.Done():
	case <-t.done:
	}
}
func (t *mcpTracker) release() {
	select {
	case t.releases <- struct{}{}:
	default:
	}
}

// One actor owns snapshots and emits them in order. Native reads use the session
// context: shortening it would also shorten Claude's CommandContext lifetime.
// Adapters bound their own read-only RPCs. Only one read can be in flight.
func (t *mcpTracker) run(ctx context.Context, engine string, source mcpStatusSource, emitter agent.Emitter, opts mcpTrackerOptions) {
	defer close(t.done)
	ticker := time.NewTicker(opts.Poll)
	defer ticker.Stop()
	results := make(chan mcpProbeResult, 1)
	var inflight, ready bool
	var lastRead time.Time
	generation := source.MCPGeneration()
	since := time.Now()
	state := protocol.MCPStatusPayload{Engine: engine, Since: since.UTC().Format(time.RFC3339Nano), Servers: []protocol.MCPServerStatus{}}
	var published *protocol.MCPStatusPayload
	firstUsed := opts.FirstUsed
	var gate *mcpGateRequest
	var gateCancel <-chan struct{}
	var gateDiscovery, gateUntil, holdUntil time.Time
	// The separate deadline timer makes release responsive even when polling is
	// slow or a native read is still outstanding.
	wake := time.NewTimer(time.Hour)
	defer wake.Stop()
	resetWake := func() {
		if !wake.Stop() {
			select {
			case <-wake.C:
			default:
			}
		}
		deadline := since.Add(opts.Track)
		if state.Settled {
			deadline = time.Now().Add(time.Hour)
		}
		if gate != nil {
			if !holdUntil.IsZero() {
				deadline = minTime(deadline, holdUntil)
			} else if !gateDiscovery.IsZero() {
				deadline = minTime(deadline, gateDiscovery)
			}
		}
		wake.Reset(max(time.Until(deadline), time.Nanosecond))
	}
	emit := func() {
		p := protocol.SanitizeMCPStatus(state)
		if published != nil && reflect.DeepEqual(*published, p) {
			return
		}
		emitter.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: protocol.MCPStatusKind, Payload: p})
		published = &p
	}
	finishGate := func(reason string) {
		if gate == nil {
			return
		}
		// A status result or release command can win select against cancellation.
		// The native driver still owns this undelivered input; do not consume its
		// first-prompt entitlement merely because its old process disappeared.
		canceled := gate.ctx.Err() != nil
		if canceled {
			reason = ""
		}
		if state.Hold != nil || reason != "" {
			state.Hold = nil
			state.Released = reason
			emit()
		}
		close(gate.done)
		gate = nil
		gateCancel = nil
		holdUntil = time.Time{}
		gateDiscovery = time.Time{}
		firstUsed = !canceled
	}
	actualSettled := func() bool {
		if !ready {
			return false
		}
		for _, server := range state.Servers {
			switch server.Status {
			case "lazy", "connected", "failed", "needs-auth", "disabled":
			default:
				return false
			}
		}
		return true
	}
	startRead := func() {
		if inflight || state.Settled || time.Since(lastRead) < opts.Poll {
			return
		}
		inflight = true
		lastRead = time.Now()
		go func() {
			p, err := source.MCPStatus(ctx)
			select {
			case results <- mcpProbeResult{p, err}:
			case <-ctx.Done():
			}
		}()
	}
	checkGeneration := func() {
		current := source.MCPGeneration()
		if current == generation {
			return
		}
		generation = current
		// A new process has not been polled yet. The pre-start generation can
		// return no inventory without making a native RPC.
		lastRead = time.Time{}
		since = time.Now()
		ready = false
		state = protocol.MCPStatusPayload{Engine: engine, Since: since.UTC().Format(time.RFC3339Nano), Servers: []protocol.MCPServerStatus{}, Hold: state.Hold}
		emit()
	}
	advance := func() {
		now := time.Now()
		if !state.Settled && (actualSettled() || !now.Before(since.Add(opts.Track))) {
			state.Settled = true
			emit()
		}
		if gate != nil {
			switch {
			case opts.Wait <= 0 || firstUsed:
				finishGate("")
			case actualSettled():
				finishGate("settled")
			case state.Settled:
				finishGate("timeout")
			case !gateUntil.IsZero() && !now.Before(gateUntil):
				finishGate("timeout")
			case !holdUntil.IsZero() && !now.Before(holdUntil):
				finishGate("timeout")
			case len(state.Servers) > 0:
				if holdUntil.IsZero() {
					holdUntil = gateUntil
					state.Released = ""
					state.Hold = &protocol.MCPHold{Since: now.UTC().Format(time.RFC3339Nano), Until: holdUntil.UTC().Format(time.RFC3339Nano)}
					emit()
				}
			case !gateDiscovery.IsZero() && !now.Before(gateDiscovery):
				finishGate("")
			}
		}
		resetWake()
	}
	emit()
	startRead()
	resetWake()
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-t.gates:
			if gate != nil && gate.ctx.Err() != nil {
				close(gate.done)
				gate = nil
				gateCancel = nil
				state.Hold = nil
				holdUntil = time.Time{}
				emit()
			}
			if firstUsed || opts.Wait <= 0 {
				close(req.done)
				firstUsed = true
				continue
			}
			// Native drivers serialize delivery; reject a duplicate callback safely.
			if gate != nil {
				close(req.done)
				continue
			}
			gate = &req
			gateCancel = req.ctx.Done()
			// A provisional pre-thread read may precede this callback. Allow
			// the next poll and bounded native RPC to finish without consuming
			// another full hold budget afterwards.
			gateUntil = time.Now().Add(opts.Wait)
			gateDiscovery = time.Now().Add(min(opts.Wait, 2*opts.Poll+2*time.Second))
			checkGeneration()
			startRead()
			advance()
		case <-gateCancel:
			// Engine death must retain the undelivered first input for its replacement.
			if gate != nil {
				close(gate.done)
				gate = nil
			}
			gateCancel = nil
			holdUntil = time.Time{}
			gateDiscovery = time.Time{}
			if state.Hold != nil {
				state.Hold = nil
				emit()
			}
			resetWake()
		case <-t.releases:
			if gate != nil && state.Hold != nil {
				finishGate("person")
				resetWake()
			}
		case result := <-results:
			inflight = false
			checkGeneration()
			if result.err == nil && result.probe.Generation == generation {
				lastRead = time.Now()
				safe := protocol.SanitizeMCPStatus(protocol.MCPStatusPayload{Servers: result.probe.Servers})
				state.Servers = safe.Servers
				// Adapter validation is not a reason to turn a malformed future
				// engine's partial inventory into authoritative emptiness here.
				ready = result.probe.Ready && len(safe.Servers) == len(result.probe.Servers)
			}
			advance()
			emit()
		case <-ticker.C:
			checkGeneration()
			advance()
			startRead()
		case <-wake.C:
			advance()
		}
	}
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Native thread allocation and metadata snapshots are not evidence of a prompt.
func mcpFirstPromptUsed(events []agent.RestoredEvent, ad *adoptRequest) bool {
	if ad != nil && ad.rec.CurrentTurnID != "" {
		return true
	}
	for _, event := range events {
		switch event.Kind {
		case "user_message", "assistant_text", "tool_call", "turn_done", "usage_limit":
			return true
		}
	}
	return false
}
