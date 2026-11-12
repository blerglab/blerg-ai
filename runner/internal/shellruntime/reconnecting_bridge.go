package shellruntime

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// ReconnectingHostBridge is a shell-only failure boundary. Its stable identity
// keeps host routing fail-closed while authenticated transports come and go.
// It never owns the daemon or an agent's lifetime.
type ReconnectingHostBridge struct {
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	dial                func(context.Context) (HostBridge, error)
	current             HostBridge
	usable              bool
	changed             chan struct{}
	receiver            func(context.Context, RPC) error
	recover             func(context.Context) error
	capabilities        func(context.Context) error
	refresh             chan struct{}
	updates             chan struct{}
	ready               chan struct{}
	once                sync.Once
	done                chan struct{}
	retry               func(context.Context) bool
	observer            func([]hostObservation, uint64)
	observationRevision func() uint64
	failed              chan struct{}
	unavailableErr      error
}

func NewReconnectingHostBridge(parent context.Context, dial func(context.Context) (HostBridge, error)) *ReconnectingHostBridge {
	return newReconnectingHostBridge(parent, dial, func(ctx context.Context) bool {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	})
}
func newReconnectingHostBridge(parent context.Context, dial func(context.Context) (HostBridge, error), retry func(context.Context) bool) *ReconnectingHostBridge {
	ctx, cancel := context.WithCancel(parent)
	b := &ReconnectingHostBridge{ctx: ctx, cancel: cancel, dial: dial, retry: retry, ready: make(chan struct{}), done: make(chan struct{}), refresh: make(chan struct{}, 1), updates: make(chan struct{}, 1), failed: make(chan struct{}, 1), changed: make(chan struct{})}
	go b.run() //nolint:contextcheck // The manager owns its stored lifetime, independent of an RPC.
	return b
}
func (b *ReconnectingHostBridge) SetObserver(revision func() uint64, f func([]hostObservation, uint64)) {
	b.mu.Lock()
	b.observer = f
	b.observationRevision = revision
	b.mu.Unlock()
}
func (b *ReconnectingHostBridge) failCurrent(current HostBridge, err error) {
	_ = bridgeFailure("liveness", err)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.current != current {
		return
	}
	b.usable = false
	close(b.changed)
	b.changed = make(chan struct{})
	select {
	case b.failed <- struct{}{}:
	default:
	}
}
func (b *ReconnectingHostBridge) heartbeat(current HostBridge) error {
	call, cancel := context.WithTimeout(b.ctx, 500*time.Millisecond)
	defer cancel()
	b.mu.Lock()
	revision, observer := b.observationRevision, b.observer
	b.mu.Unlock()
	var started uint64
	if revision != nil {
		started = revision()
	}
	var observed []hostObservation
	if err := current.Invoke(call, "heartbeat", nil, &observed); err != nil {
		return err
	}
	b.mu.Lock()
	own := b.current == current
	b.mu.Unlock()
	if own && observer != nil {
		observer(observed, started)
	}
	return nil
}

// Three consecutive misses over at least two seconds tolerate a scheduling
// delay without converting a healthy machine's load into shell disconnection.
type heartbeatMisses struct {
	count int
	first time.Time
}

func (m *heartbeatMisses) failed(now time.Time) bool {
	if m.count == 0 {
		m.first = now
	}
	m.count++
	return m.count >= 3 && now.Sub(m.first) >= 2*time.Second
}
func (m *heartbeatMisses) observe(err error, now time.Time) bool {
	if err == nil {
		*m = heartbeatMisses{}
		return false
	}
	return m.failed(now)
}
func (b *ReconnectingHostBridge) SetCapabilityUpdate(f func(context.Context) error) {
	b.mu.Lock()
	b.capabilities = f
	b.mu.Unlock()
}
func (b *ReconnectingHostBridge) RequestRecovery() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usable = false
	close(b.changed)
	b.changed = make(chan struct{})
	select {
	case b.refresh <- struct{}{}:
	default:
	}
}
func (b *ReconnectingHostBridge) RequestCapabilities() {
	select {
	case b.updates <- struct{}{}:
	default:
	}
}

func (b *ReconnectingHostBridge) SetRecovery(f func(context.Context) error) {
	b.mu.Lock()
	b.recover = f
	b.mu.Unlock()
	b.once.Do(func() { close(b.ready) })
}
func (b *ReconnectingHostBridge) SetReceiver(f func(context.Context, RPC) error) {
	b.mu.Lock()
	b.receiver = f
	b.mu.Unlock()
}
func (b *ReconnectingHostBridge) snapshot() HostBridge {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current
}
func (b *ReconnectingHostBridge) Invoke(ctx context.Context, method string, in, out any) error {
	b.mu.Lock()
	current, usable := b.current, b.usable
	b.mu.Unlock()
	if current != nil && !keeperBridgeClosed(current) && (usable || keeperRecoveryMethod(method)) {
		err := current.Invoke(ctx, method, in, out)
		if err != nil {
			var networkError net.Error
			if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
				_ = bridgeFailure("call_deadline", err)
				return ErrKeeperUnavailable
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.As(err, &networkError) || keeperBridgeClosed(current) {
				b.failCurrent(current, err)
				return ErrKeeperUnavailable
			}
			if live, ok := current.(interface{ Done() <-chan struct{} }); ok {
				select {
				case <-live.Done():
					return ErrKeeperUnavailable
				default:
				}
			}
		}
		return err
	}
	_ = bridgeFailure("unavailable", ErrKeeperUnavailable)
	return b.UnavailableError()
}
func (b *ReconnectingHostBridge) UnavailableError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if errors.Is(b.unavailableErr, ErrKeeperIncompatible) {
		return ErrKeeperIncompatible
	}
	return ErrKeeperUnavailable
}
func keeperRecoveryMethod(method string) bool {
	switch method {
	case "hello", "rebind", "capabilities", "register", "probe", "link", "retire_workspace", "workspace_binding", "workspace_shells_open", "heartbeat":
		return true
	}
	return false
}
func keeperBridgeClosed(bridge HostBridge) bool {
	if live, ok := bridge.(interface{ Done() <-chan struct{} }); ok {
		select {
		case <-live.Done():
			return true
		default:
		}
	}
	return false
}
func (b *ReconnectingHostBridge) setCurrent(current HostBridge, usable bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// A dial returns a bridge only after the protocol handshake, so any connected
	// keeper is compatible even while recovery keeps it unusable.
	if usable || current != nil && errors.Is(b.unavailableErr, ErrKeeperIncompatible) {
		b.unavailableErr = nil
	}
	if usable && len(b.refresh) != 0 {
		usable = false
	}
	b.current, b.usable = current, usable
	close(b.changed)
	b.changed = make(chan struct{})
}

// WaitReady is for shell callers only. Agent connection hooks never wait here.
func (b *ReconnectingHostBridge) WaitReady(ctx context.Context) error {
	for {
		b.mu.Lock()
		current, usable, changed := b.current, b.usable, b.changed
		b.mu.Unlock()
		if usable && current != nil && !keeperBridgeClosed(current) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.ctx.Done():
			return b.ctx.Err()
		case <-changed:
		}
	}
}

// incompatible reads availability and cause together, so a reconnecting
// compatible keeper is never reported with an earlier keeper's cause.
func (b *ReconnectingHostBridge) incompatible() bool {
	b.mu.Lock()
	current, usable, cause := b.current, b.usable, b.unavailableErr
	b.mu.Unlock()
	if usable && current != nil && !keeperBridgeClosed(current) {
		return false
	}
	return current == nil && errors.Is(cause, ErrKeeperIncompatible)
}
func (b *ReconnectingHostBridge) available() bool {
	b.mu.Lock()
	current, usable := b.current, b.usable
	b.mu.Unlock()
	return usable && current != nil && !keeperBridgeClosed(current)
}
func (b *ReconnectingHostBridge) WaitUnavailable(ctx context.Context) error {
	for {
		b.mu.Lock()
		available := b.usable && b.current != nil && !keeperBridgeClosed(b.current)
		changed := b.changed
		b.mu.Unlock()
		if !available {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (b *ReconnectingHostBridge) Metadata(method string, in, out any) error {
	ctx, cancel := context.WithTimeout(b.ctx, 2*time.Second)
	defer cancel()
	return b.Invoke(ctx, method, in, out)
}
func (b *ReconnectingHostBridge) HostSession(id string) bool {
	if current := b.snapshot(); current != nil {
		return current.HostSession(id)
	}
	return false
}
func (b *ReconnectingHostBridge) Close() error { b.cancel(); <-b.done; return nil }
func (b *ReconnectingHostBridge) run() {
	defer close(b.done)
	select {
	case <-b.ctx.Done():
		return
	case <-b.ready:
	}
	for b.ctx.Err() == nil {
		select {
		case <-b.failed:
		default:
		}
		attempt, cancel := context.WithTimeout(b.ctx, 3*time.Second)
		// The stream lifetime is the manager's, not the dial deadline.
		current, err := b.dial(b.ctx)
		if err == nil {
			b.mu.Lock()
			receiver, recoverBridge := b.receiver, b.recover
			b.mu.Unlock()
			b.setCurrent(current, false)
			current.SetReceiver(receiver)
			err = recoverBridge(attempt)
			if err == nil {
				err = b.heartbeat(current)
			}
			if err == nil {
				b.setCurrent(current, true)
			}
		}
		cancel()
		if err == nil {
			var lost <-chan struct{}
			if live, ok := current.(interface{ Done() <-chan struct{} }); ok {
				lost = live.Done()
			}
			connected := true
			misses := heartbeatMisses{}
			ticker := time.NewTicker(time.Second)
			for connected && err == nil {
				var callback func(context.Context) error
				select {
				case <-b.ctx.Done():
					connected = false
				case <-lost:
					connected = false
				case <-b.failed:
					connected = false
				case <-ticker.C:
					beatErr := b.heartbeat(current)
					if misses.observe(beatErr, time.Now()) {
						b.failCurrent(current, beatErr)
						err = beatErr
					} else if beatErr != nil {
						_ = bridgeFailure("heartbeat_retry", beatErr)
					}
				case <-b.refresh:
					b.setCurrent(current, false)
					b.mu.Lock()
					callback = b.recover
					b.mu.Unlock()
				case <-b.updates:
					b.mu.Lock()
					callback = b.capabilities
					b.mu.Unlock()
				}
				if callback != nil {
					work, stop := context.WithTimeout(b.ctx, 3*time.Second)
					err = callback(work)
					stop()
					if err == nil {
						// Recovery can add registrations. Refresh occupancy before
						// publishing readiness; one missed observation stays unknown
						// and does not disconnect an otherwise healthy transport.
						if beatErr := b.heartbeat(current); beatErr != nil {
							_ = bridgeFailure("heartbeat_retry", beatErr)
						}
						b.setCurrent(current, true)
					}
				}
			}
			ticker.Stop()
		}
		if err != nil {
			_ = bridgeFailure("reconnect", err)
			b.mu.Lock()
			b.unavailableErr = err
			b.mu.Unlock()
		}
		b.setCurrent(nil, false)
		if current != nil {
			if err := current.Close(); err != nil {
				_ = bridgeFailure("disconnect", err)
			}
			if b.ctx.Err() == nil {
				if notices, ok := current.(interface{ NotifyClosed(context.Context) }); ok {
					ctx, cancel := context.WithTimeout(b.ctx, time.Second)
					notices.NotifyClosed(ctx)
					cancel()
				}
			}
		}
		if b.ctx.Err() != nil {
			return
		}
		if !b.retry(b.ctx) {
			return
		}
	}
}
