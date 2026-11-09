//go:build linux

package shellruntime

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
)

func TestKeeperReadinessIsScopedToSession(t *testing.T) {
	k := NewKeeperRuntime()
	k.service.launches["A"] = &registered{retained: &retainedCoordinator{mu: new(sync.Mutex), children: map[string]*retainedChild{"held": {}}}}
	k.service.launches["B"] = &registered{}
	if !errors.Is(k.ready("A"), ErrAdoptionPending) {
		t.Fatal("unadopted session admitted shell")
	}
	if err := k.ready("B"); err != nil {
		t.Fatal("workspace A fenced B", err)
	}
	k.rebound = true
	for _, method := range []string{"ready", "process_phase"} {
		var body []byte
		if method == "ready" {
			body, _ = json.Marshal("B")
		} else {
			body, _ = json.Marshal(struct {
				Session string
				Freeze  bool
			}{"B", false})
		}
		_, err := k.invoke(t.Context(), method, body)
		if errors.Is(err, ErrAdoptionPending) {
			t.Fatal("session omitted at dispatch", method)
		}
	}
}

func TestKeeperDiagnosticOutputAndMalformedFrameDoNotCloseBridge(t *testing.T) {
	k, b, _ := reviewRuntime(t)
	received := make(chan RPC, 1)
	b.SetReceiver(func(_ context.Context, r RPC) error { received <- r; return nil })
	if err := k.send(RPC{Type: "owner_shell_output", SessionID: "session", StartCause: "helper_stopped"}); err != nil {
		t.Fatal("diagnostic bypassed output transport", err)
	}
	if (<-received).StartCause != "helper_stopped" {
		t.Fatal("diagnostic lost")
	}
	k.mu.Lock()
	ch := k.current
	k.mu.Unlock()
	if err := ch.Send(bridgePacket{Kind: "event", Body: json.RawMessage(`{"type":"owner_shell_output"}`)}); err != nil {
		t.Fatal(err)
	}
	for i, body := range []string{`null`, `[]`} {
		id := "malformed-" + strconv.Itoa(i)
		ack := make(chan bool, 1)
		k.mu.Lock()
		k.outputs[id] = keeperOutput{ch, ack}
		k.mu.Unlock()
		if err := ch.Send(bridgePacket{Kind: "output", ID: id, Body: json.RawMessage(body)}); err != nil {
			t.Fatal(err)
		}
		select {
		case ok := <-ack:
			if ok {
				t.Fatal("malformed output was accepted")
			}
		case <-time.After(time.Second):
			t.Fatal("refused frame did not release its producer without killing the bridge")
		}
		k.mu.Lock()
		delete(k.outputs, id)
		k.mu.Unlock()
	}
	if err := ch.Send(bridgePacket{Kind: "event", Body: json.RawMessage(`{"unexpected":true}`)}); err != nil {
		t.Fatal(err)
	}
	var hello protocol.ShellLinkHello
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	// Drain the non-output event as well; an event failure is session-local.
	go func() { <-received }()
	if err := b.Invoke(ctx, "hello", nil, &hello); err != nil {
		t.Fatal("one frame killed bridge", err)
	}
}

func TestKeeperOutputCancelJoinsDeliveryAndKeepsRepliesAlive(t *testing.T) {
	k, b, _ := reviewRuntime(t)
	entered, joined := make(chan struct{}), make(chan struct{})
	b.SetReceiver(func(ctx context.Context, _ RPC) error { close(entered); <-ctx.Done(); close(joined); return ctx.Err() })
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- k.sendOutput(ctx, RPC{Type: "owner_shell_output", SessionID: "session"}) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("delivery never entered")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancel accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("output_cancel did not interrupt delivery")
	}
	select {
	case <-joined:
	default:
		t.Fatal("cancellation returned before delivery joined")
	}
	var hello protocol.ShellLinkHello
	if err := b.Invoke(t.Context(), "hello", nil, &hello); err != nil {
		t.Fatal("output cancellation killed bridge", err)
	}
}

func TestKeeperAckIsBoundToItsChannel(t *testing.T) {
	k := NewKeeperRuntime()
	old, next := new(shellkeeper.Channel), new(shellkeeper.Channel)
	result := make(chan bool, 1)
	k.outputs["receipt"] = keeperOutput{old, result}
	packet := bridgePacket{Kind: "output_ack", ID: "receipt", OK: true}
	k.acknowledge(next, packet)
	select {
	case <-result:
		t.Fatal("another channel acknowledged output")
	default:
	}
	k.acknowledge(old, packet)
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("negative receipt")
		}
	default:
		t.Fatal("current channel receipt lost")
	}
}

func TestKeeperBackpressureReturnsCorrelatedRefusal(t *testing.T) {
	k, b, _ := reviewRuntime(t)
	b.SetReceiver(func(context.Context, RPC) error { return nil })
	// Establish Serve before holding dispatch, without relying on elapsed time.
	var hello protocol.ShellLinkHello
	if err := b.Invoke(t.Context(), "hello", nil, &hello); err != nil {
		t.Fatal(err)
	}
	k.operation.Lock()
	defer k.operation.Unlock()
	replies := make(chan bridgePacket, 100)
	b.mu.Lock()
	for i := 0; i < 100; i++ {
		b.pending["burst-"+strconv.Itoa(i)] = replies
	}
	b.mu.Unlock()
	defer func() { b.mu.Lock(); clear(b.pending); b.mu.Unlock() }()
	for i := 0; i < 100; i++ {
		if err := b.channel.Send(bridgePacket{Kind: "call", Method: "hello", ID: "burst-" + strconv.Itoa(i), Body: json.RawMessage(`null`)}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case p := <-replies:
		if p.OK || p.ID == "" {
			t.Fatal("uncorrelated overload reply", p)
		}
	case <-time.After(time.Second):
		t.Fatal("overload silently dropped request instead of replying")
	}
}

// The read goroutine owns authenticated stream teardown. Pin its explicit join
// in addition to the repeated real FD lifecycle proof: a detached reader can
// still pass a best-effort FD census depending on scheduler order.
func TestKeeperServeJoinsReadFinished(t *testing.T) {
	data, err := os.ReadFile("keeper_bridge_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "keeper_bridge_linux.go", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	joined := false
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Serve" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			d, ok := n.(*ast.DeferStmt)
			if !ok {
				return true
			}
			ast.Inspect(d.Call, func(n ast.Node) bool {
				if recv, ok := n.(*ast.UnaryExpr); ok && recv.Op == token.ARROW {
					if id, ok := recv.X.(*ast.Ident); ok && id.Name == "readFinished" {
						joined = true
					}
				}
				return true
			})
			return true
		})
		return false
	})
	if !joined {
		t.Fatal("Serve no longer joins its authenticated reader before returning")
	}
}

func TestServiceDiagnosticsUseOutputSender(t *testing.T) {
	base, b, _, key := fixture(t)
	events, outputs := 0, 0
	service := NewService(func(r RPC) error {
		if r.Type == "owner_shell_output" {
			events++
		}
		return nil
	})
	service.SetOutputSender(func(ctx context.Context, r RPC) error {
		if _, bounded := ctx.Deadline(); !bounded {
			t.Error("unbounded diagnostic may hold helper reap")
		}
		if r.Type != "owner_shell_output" || r.StartCause != "helper_stopped" {
			t.Error("wrong diagnostic frame")
		}
		outputs++
		return nil
	})
	if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b, _ = service.Binding(b.Session)
	reply := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}})
	if !reply.OK {
		t.Fatal("configuration fixture refused", reply.ErrorCode)
	}
	service.launches[b.Session].coordinator.command.diagnostic("fixture", stageError("helper_stopped", nil))
	if outputs != 1 || events != 0 {
		t.Fatal("diagnostic traveled over event channel", outputs, events)
	}
}
