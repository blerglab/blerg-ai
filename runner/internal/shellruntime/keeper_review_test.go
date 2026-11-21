package shellruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

type reviewBridge struct {
	invoke func(context.Context, string, any, any) error
}

func (b *reviewBridge) Invoke(ctx context.Context, m string, in, out any) error {
	return b.invoke(ctx, m, in, out)
}
func (b *reviewBridge) Metadata(m string, in, out any) error {
	return ErrClosed
}
func (*reviewBridge) HostSession(string) bool                      { return true }
func (*reviewBridge) SetReceiver(func(context.Context, RPC) error) {}
func (*reviewBridge) Close() error                                 { return nil }

func TestBridgedRequestsUseBoundedIndependentSessionQueues(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	replies := make(chan RPC, 4)
	s := NewService(func(r RPC) error { replies <- r; return nil })
	var released sync.Once
	finish := func() { released.Do(func() { close(release) }) }
	defer func() { finish(); _ = s.Shutdown(t.Context()) }()
	s.SetHostBridge(&reviewBridge{invoke: func(ctx context.Context, m string, in, out any) error {
		if m != "call" {
			return nil
		}
		r := in.(RPC)
		if r.RequestID == "expired" {
			t.Error("expired queued request reached keeper")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("keeper request has no deadline")
		}
		if r.SessionID == "stalled" {
			once.Do(func() { close(entered) })
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		*out.(*RPC) = RPC{Type: "owner_shell_result", SessionID: r.SessionID, RequestID: "inner-request", OK: true}
		return nil
	}})
	for _, id := range []string{"healthy", "stalled"} {
		s.launches[id] = &registered{binding: ownershell.Binding{Session: id, Runtime: "host", Live: true}, mu: new(sync.Mutex), phase: "running"}
	}
	raw, _ := json.Marshal(RPC{Version: 1, Type: "owner_shell_request", SessionID: "stalled", Op: "describe", RequestID: "slow"})
	returned := make(chan struct{})
	go func() { s.HandleContext(t.Context(), raw); close(returned) }()
	guard, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	select {
	case <-returned:
	case <-guard.Done():
		t.Fatal("keeper blocked shared websocket reader")
	}
	select {
	case <-entered:
	case <-guard.Done():
		t.Fatal("session worker did not call keeper")
	}
	raw, _ = json.Marshal(RPC{Version: 1, Type: "owner_shell_request", SessionID: "healthy", Op: "describe", RequestID: "fast"})
	s.HandleContext(t.Context(), raw)
	select {
	case reply := <-replies:
		if reply.SessionID != "healthy" || !reply.OK || reply.RequestID != "fast" {
			t.Fatal("healthy session blocked", reply.Op)
		}
	case <-guard.Done():
		t.Fatal("another session stalled")
	}
	expired, cancelExpired := context.WithDeadline(t.Context(), time.Unix(1, 0))
	defer cancelExpired()
	raw, _ = json.Marshal(RPC{Version: 1, Type: "owner_shell_request", SessionID: "stalled", Op: "describe", RequestID: "expired"})
	s.HandleContext(expired, raw)
	finish()
	for i := 0; i < 2; i++ {
		select {
		case reply := <-replies:
			if reply.RequestID == "expired" && reply.OK {
				t.Fatal("expired request succeeded")
			}
		case <-guard.Done():
			t.Fatal("queued expiry did not resolve")
		}
	}
}

func TestBridgedAdmissionAndSourceClosuresRemainDaemonAuthority(t *testing.T) {
	for _, name := range []string{"atomic_refusal", "source_live", "source_state", "source_unknown", "retarget_during_admission", "restricted_child", "allowed"} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			b := ownershell.Binding{Session: "session", Owner: "owner", Launch: "launch", Runtime: "host", RuntimeGeneration: 1, Connection: "connection", Live: true}
			if name == "restricted_child" {
				b.Child = true
			}
			bridge := &reviewBridge{invoke: func(_ context.Context, m string, in, out any) error {
				if m == "call" {
					calls.Add(1)
					*out.(*RPC) = RPC{OK: true}
				}
				return nil
			}}
			s := NewService(func(RPC) error { return nil })
			s.SetHostBridge(bridge)
			command := Command{}
			live := true
			switch name {
			case "source_live":
				command.SourceLive = func() bool { return false }
				command.SourceState = func() (bool, bool) { return true, true }
			case "source_state":
				command.SourceState = func() (bool, bool) { return false, true }
			case "source_unknown":
				command.SourceState = func() (bool, bool) { return true, false }
			case "retarget_during_admission":
				command.SourceLive = func() bool { return live }
				command.SourceState = func() (bool, bool) { return live, true }
			}
			admitted := false
			admission := func(_ context.Context, start func() error) error {
				admitted = true
				if name == "atomic_refusal" {
					return errors.New("revoked authorization")
				}
				if name == "retarget_during_admission" {
					live = false
				}
				return start()
			}
			if err := s.RegisterWithAdmission(t.Context(), new(sync.Mutex), b, command, func(context.Context) error { return nil }, admission); err != nil {
				t.Fatal("host registration refused", err)
			}
			defer func() { _ = s.Shutdown(t.Context()) }()
			if err := s.flushHostControl(t.Context(), b.Session); err != nil {
				t.Fatal(err)
			}
			result := s.executeBridged(t.Context(), RPC{Version: 1, SessionID: b.Session, Op: "open"})
			allowed := name == "allowed" || name == "restricted_child"
			if result.OK != allowed || (calls.Load() > 0) != allowed {
				t.Fatal("daemon authority was bypassed", name)
			}
			if name == "atomic_refusal" && !admitted {
				t.Fatal("atomic workspace admission was dropped")
			}
		})
	}
}

func TestBridgedSourceNeedsCurrentDaemonRegistration(t *testing.T) {
	calls := 0
	s := NewService(func(RPC) error { return nil })
	s.SetHostBridge(&reviewBridge{invoke: func(context.Context, string, any, any) error { calls++; return nil }})
	reply := s.executeBridged(t.Context(), RPC{SessionID: "retained-candidate", Op: "input"})
	if reply.OK || calls != 0 {
		t.Fatal("unknown source alias gained authority from keeper inventory")
	}
}
