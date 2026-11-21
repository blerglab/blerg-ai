package shellruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

func TestRound10CallDeadlineDoesNotDropBridge(t *testing.T) {
	backend := &reviewBridge{invoke: func(context.Context, string, any, any) error { return context.DeadlineExceeded }}
	b := &ReconnectingHostBridge{ctx: t.Context(), current: backend, usable: true, changed: make(chan struct{}), failed: make(chan struct{}, 1)}
	if err := b.Invoke(t.Context(), "call", nil, nil); !errors.Is(err, ErrKeeperUnavailable) {
		t.Fatal("deadline cause", err)
	}
	if !b.available() {
		t.Fatal("one slow call dropped the bridge")
	}
	select {
	case <-b.failed:
		t.Fatal("call deadline stole heartbeat liveness authority")
	default:
	}
}

func TestRound10RecoveryAllowsWorkspaceOccupancyProof(t *testing.T) {
	called := false
	backend := &reviewBridge{invoke: func(_ context.Context, method string, _, out any) error {
		called = method == "workspace_shells_open"
		*out.(*bool) = false
		return nil
	}}
	b := &ReconnectingHostBridge{ctx: t.Context(), current: backend, usable: false}
	var open bool
	if err := b.Invoke(t.Context(), "workspace_shells_open", "workspace", &open); err != nil || !called {
		t.Fatal("recovery occupancy proof refused", err)
	}
	if keeperRecoveryMethod("call") {
		t.Fatal("recovery admitted shell access")
	}
}

type round10PendingStream struct {
	sent  chan bridgePacket
	reply func(bridgePacket)
}

func (s *round10PendingStream) Send(v any) error {
	if s.reply != nil {
		s.reply(v.(bridgePacket))
		return nil
	}
	s.sent <- v.(bridgePacket)
	return nil
}
func (s *round10PendingStream) SendContext(ctx context.Context, v any) error {
	if ctx.Err() != nil {
		return ErrClosed
	}
	return s.Send(v)
}
func (*round10PendingStream) Receive(any) error { return ErrClosed }
func (*round10PendingStream) Close() error      { return nil }

func TestRound10HeartbeatOutsidePendingCallBudget(t *testing.T) {
	for _, firstHeartbeat := range []bool{false, true} {
		t.Run(map[bool]string{false: "full_calls", true: "heartbeat_not_counted"}[firstHeartbeat], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			var workers sync.WaitGroup
			defer func() { cancel(); workers.Wait() }()
			stream := &round10PendingStream{sent: make(chan bridgePacket, 130)}
			b := &KeeperBridge{channel: stream, pending: map[string]chan bridgePacket{}, done: make(chan struct{})}
			invoke := func(method string) <-chan error {
				result := make(chan error, 1)
				workers.Add(1)
				go func() { defer workers.Done(); result <- b.Invoke(ctx, method, nil, nil) }()
				return result
			}
			receive := func() bridgePacket {
				t.Helper()
				select {
				case p := <-stream.sent:
					return p
				case <-time.After(time.Second):
					t.Fatal("heartbeat or ordinary call was incorrectly limited")
					return bridgePacket{}
				}
			}
			var heartbeat bridgePacket
			if firstHeartbeat {
				_ = invoke("heartbeat")
				heartbeat = receive()
			}
			for range 128 {
				_ = invoke("workspace_binding")
				_ = receive()
			}
			if !firstHeartbeat {
				_ = invoke("heartbeat")
				heartbeat = receive()
			}
			extra := invoke("workspace_binding")
			select {
			case err := <-extra:
				if err == nil {
					t.Fatal("ordinary call cap removed")
				}
			case <-time.After(time.Second):
				t.Fatal("ordinary call exceeded cap")
			}
			b.mu.Lock()
			wait := b.pending[heartbeat.ID]
			b.mu.Unlock()
			wait <- bridgePacket{Kind: "reply", ID: heartbeat.ID, OK: true}
		})
	}
}

func TestRound10HostOpenCannotBypassDurableHistory(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(context.Background()) }()
	e, _ := round6HostEntry()
	e.command.BeforeHostShell = func() error { return errors.New("journal unavailable") }
	s.launches[e.binding.Session] = e
	forwarded := false
	s.SetHostBridge(&reviewBridge{invoke: func(_ context.Context, method string, _ any, out any) error {
		if method == "workspace_binding" {
			*out.(*ownershell.Binding) = e.binding
		}
		if method == "call" {
			forwarded = true
			out.(*RPC).OK = true
		}
		return nil
	}})
	if result := s.executeBridged(t.Context(), RPC{SessionID: e.binding.Session, Op: "open"}); result.OK || forwarded {
		t.Fatal("open forwarded without durable history")
	}
}

func TestRound10IncompatibleCauseReachesShellRefusal(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(context.Background()) }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := &ReconnectingHostBridge{ctx: ctx, ready: make(chan struct{}), unavailableErr: ErrKeeperIncompatible}
	s.SetHostBridge(b)
	result := s.executeBridged(t.Context(), RPC{SessionID: "session", Op: "open"})
	if result.StartCause != "keeper_incompatible" {
		t.Fatal("incompatibility lost", result.StartCause)
	}
}

func TestRound10ServerDeadlineIsCallLocalUnavailable(t *testing.T) {
	_, b, _ := reviewRuntime(t)
	wait := make(chan bridgePacket, 1)
	b.mu.Lock()
	b.pending["expired-call"] = wait
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.pending, "expired-call"); b.mu.Unlock() }()
	if err := b.channel.Send(bridgePacket{Kind: "call", ID: "expired-call", Method: "hello", Deadline: time.Now().Add(-time.Second).UnixNano()}); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-wait:
		if reply.OK || reply.Cause != "call_deadline" {
			t.Fatal("expired request masqueraded as a custody refusal", reply.Cause)
		}
	case <-time.After(time.Second):
		t.Fatal("expired request unanswered")
	}
	var hello any
	if err := b.Invoke(t.Context(), "hello", nil, &hello); err != nil {
		t.Fatal("expired operation closed stream", err)
	}
}

func TestRound10SendDeadlinePreservesCallLocalCause(t *testing.T) {
	stream := &round10PendingStream{sent: make(chan bridgePacket, 1)}
	raw := &KeeperBridge{channel: stream, pending: map[string]chan bridgePacket{}, done: make(chan struct{})}
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer cancel()
	b := &ReconnectingHostBridge{ctx: t.Context(), current: raw, usable: true, changed: make(chan struct{}), failed: make(chan struct{}, 1)}
	if err := b.Invoke(ctx, "call", nil, nil); !errors.Is(err, ErrKeeperUnavailable) {
		t.Fatal("send deadline became a custody refusal", err)
	}
	if !b.available() {
		t.Fatal("send deadline dropped bridge")
	}
}

func TestRound10ServerDeadlineReplyDoesNotFenceOrDisconnect(t *testing.T) {
	stream := &round10PendingStream{}
	raw := &KeeperBridge{channel: stream, pending: map[string]chan bridgePacket{}, done: make(chan struct{})}
	stream.reply = func(p bridgePacket) {
		raw.mu.Lock()
		wait := raw.pending[p.ID]
		raw.mu.Unlock()
		wait <- bridgePacket{ID: p.ID, Kind: "reply", Cause: "call_deadline"}
	}
	b := &ReconnectingHostBridge{ctx: t.Context(), current: raw, usable: true, changed: make(chan struct{}), failed: make(chan struct{}, 1)}
	if err := b.Invoke(t.Context(), "hello", nil, nil); !errors.Is(err, ErrKeeperUnavailable) {
		t.Fatal("server deadline mapped to custody refusal", err)
	}
	if !b.available() {
		t.Fatal("server deadline disconnected link")
	}
}

// A compatible keeper reconnecting after an incompatible one is not reported
// with the old cause while recovery keeps it unusable.
func TestRound11CompatibleReconnectClearsIncompatibility(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(context.Background()) }()
	b := &ReconnectingHostBridge{ctx: t.Context(), ready: make(chan struct{}), changed: make(chan struct{}), unavailableErr: ErrKeeperIncompatible}
	s.SetHostBridge(b)
	if !s.KeeperIncompatible() {
		t.Fatal("incompatible keeper not reported")
	}
	b.setCurrent(&reviewBridge{invoke: func(context.Context, string, any, any) error { return nil }}, false)
	if s.KeeperIncompatible() {
		t.Fatal("a compatible reconnect kept the incompatible remedy")
	}
}
