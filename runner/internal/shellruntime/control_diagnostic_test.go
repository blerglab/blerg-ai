//go:build linux

package shellruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

func TestBootstrapTransportStagesReachCaller(t *testing.T) {
	for _, stage := range []string{"helper_write", "helper_reply", "helper_stopped", "helper_wait"} {
		t.Run(stage, func(t *testing.T) {
			c, _, g, key := fixture(t)
			command := c.command
			command.Executable = "/usr/bin/env"
			command.Args = []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestTransportDiagnosticProcess$", "--", stage}
			if stage == "helper_write" {
				command.Identity.Cwd = strings.Repeat("private-fixture", 100000)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			peer, err := startPeer(ctx, command, g, key.Public().(ed25519.PublicKey))
			if peer != nil {
				<-peer.done
			}
			detail, cause := startDiagnostic(err)
			if cause != stage || !strings.Contains(detail, ownershell.StartStageMessage(stage)) || strings.Contains(detail, "private-fixture") {
				t.Fatalf("bootstrap diagnostic: %s / %s; want %s", detail, cause, stage)
			}
		})
	}
}

func TestTransportDiagnosticProcess(t *testing.T) {
	stage := os.Args[len(os.Args)-1]
	switch stage {
	case "helper_write":
		_ = os.Stdin.Close()
		_, _ = os.Stdout.Write([]byte("{}\n"))
	case "helper_reply":
		var f HelperFrame
		_ = json.NewDecoder(os.Stdin).Decode(&f)
		_ = json.NewEncoder(os.Stdout).Encode(HelperFrame{Op: "ack", ID: f.ID})
	case "helper_stopped":
	case "helper_wait":
		var f HelperFrame
		_ = json.NewDecoder(os.Stdin).Decode(&f)
		_, _ = os.Stdin.Read(make([]byte, 1))
	default:
		return
	}
	os.Exit(0)
}

func TestControlStagesReachParentAndPreserveCleanup(t *testing.T) {
	for _, stage := range []string{"control_grant", "input_sequence", "resize_sequence", "control_operation", "control_apply"} {
		t.Run(stage, func(t *testing.T) {
			c, b, g, key := fixture(t)
			command := c.command
			command.Root = b.Root
			peer, err := startPeer(t.Context(), command, g, key.Public().(ed25519.PublicKey))
			if err != nil {
				t.Fatal(err)
			}
			f := HelperFrame{Op: "input", Grant: g, Seq: 1}
			switch stage {
			case "control_grant":
				f.Grant.Signature = "Bearer private-fixture"
			case "input_sequence":
				f.Seq = 2
			case "resize_sequence":
				f.Op = "resize"
				f.Seq = 2
			case "control_operation":
				f.Op = "Bearer private-fixture"
			case "control_apply":
				f.Op = "renew"
				f.Grant.Grant.ExpiresAt--
				f.Grant = ownershell.SignGrant(key, f.Grant.Grant)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			err = peer.call(ctx, f)
			<-peer.done
			detail, cause := startDiagnostic(err)
			if cause != stage || !strings.Contains(detail, ownershell.StartStageMessage(stage)) || strings.Contains(detail, "private-fixture") {
				t.Errorf("control diagnostic: %s / %s; want %s", detail, cause, stage)
			}
			if err := peer.call(ctx, HelperFrame{Op: "close", Grant: g}); err != nil {
				t.Errorf("successful cleanup proof lost: %v", err)
			}
		})
	}
}

func TestServiceControlDiagnostic(t *testing.T) {
	for _, op := range []string{"input", "resize"} {
		t.Run(op, func(t *testing.T) {
			base, b, g, key := fixture(t)
			events := make(chan RPC, 32)
			service := NewService(func(r RPC) error { events <- r; return nil })
			if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
			b, _ = service.Binding(b.Session)
			g.Grant.ConnectionID = b.Connection
			g.Grant.Audience = b.Audience
			g = ownershell.SignGrant(key, g.Grant)
			if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}).OK {
				t.Fatal("configure")
			}
			if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g}).OK {
				t.Fatal("open")
			}
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			reply := service.execute(t.Context(), RPC{SessionID: b.Session, Op: op, Grant: g, Seq: 2, Data: []byte("private-fixture")})
			stage := op + "_sequence"
			if reply.OK || reply.StartCause != stage {
				t.Errorf("service discarded control cause: code=%s cause=%s", reply.ErrorCode, reply.StartCause)
			}
			if !strings.Contains(logs.String(), ownershell.StartStageMessage(stage)) || strings.Contains(logs.String(), "private-fixture") {
				t.Errorf("missing or unsafe log: %s", logs.String())
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			for {
				select {
				case event := <-events:
					if event.StartCause == stage {
						return
					}
				case <-ctx.Done():
					t.Fatal("no diagnostic on private shell output boundary")
				}
			}
		})
	}
}

// Keep this list in step with the fixed stage vocabulary. The real helper tests
// exercise transport and control rejection sites; this matrix tests the common
// framed service boundary for the entire fixed vocabulary.
var diagnosticStages = []string{
	"helper_start", "workspace_admission", "bootstrap_read", "bootstrap_validate",
	"resolve_root", "resolve_identity", "verify_identity", "verify_cwd", "verify_root", "verify_grant",
	"open_root", "open_cwd", "verify_uid", "pty_open", "exec_shell", "pty_configure", "containment_setup",
	"lease_create", "helper_ready", "helper_stopped", "helper_input_pipe", "helper_output_pipe", "helper_record",
	"helper_write", "helper_wait", "helper_reply", "admit_shell", "verify_binding", "verify_reservation", "verify_policy", "grant_clock",
	"cleanup_shell", "control_read", "control_write", "control_grant", "lease_check", "input_sequence", "resize_sequence", "control_operation", "control_apply",
}

func TestEveryStageThroughFramedService(t *testing.T) {
	for _, stage := range append(append([]string{}, diagnosticStages...), "Bearer private-fixture\nforged") {
		t.Run(stage, func(t *testing.T) {
			base, b, g, key := fixture(t)
			base.command.Args = []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestFramedStageProcess$", "--", stage}
			service := NewService(func(RPC) error { return nil })
			if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
			b, _ = service.Binding(b.Session)
			g.Grant.ConnectionID = b.Connection
			g.Grant.Audience = b.Audience
			g = ownershell.SignGrant(key, g.Grant)
			if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}).OK {
				t.Fatal("configure")
			}
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			reply := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g})
			want := stage
			if ownershell.SafeStartStage(stage) == "" {
				want = ownershell.StartCauseBootstrap
			}
			if reply.OK || reply.StartCause != want {
				t.Fatalf("stage lost: code=%s cause=%s, want %s", reply.ErrorCode, reply.StartCause, want)
			}
			if strings.Contains(logs.String(), "private-fixture") || strings.Contains(logs.String(), "forged") {
				t.Fatal("untrusted data in log")
			}
			if stage == want && !strings.Contains(logs.String(), ownershell.StartStageMessage(stage)) {
				t.Fatal("stage sentence missing from log")
			}
		})
	}
}

func TestEveryControlStageThroughFramedService(t *testing.T) {
	for _, stage := range diagnosticStages {
		t.Run(stage, func(t *testing.T) {
			base, b, g, key := fixture(t)
			base.command.Args = []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestFramedStageProcess$", "--", "control:" + stage}
			events := make(chan RPC, 32)
			service := NewService(func(r RPC) error { events <- r; return nil })
			if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
			b, _ = service.Binding(b.Session)
			g.Grant.ConnectionID, g.Grant.Audience = b.Connection, b.Audience
			g = ownershell.SignGrant(key, g.Grant)
			if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}).OK {
				t.Fatal("configure")
			}
			if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g}).OK {
				t.Fatal("open")
			}
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			reply := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "input", Grant: g, Seq: 1})
			if reply.OK || reply.StartCause != stage {
				t.Fatalf("control cause lost: %s", reply.StartCause)
			}
			if !strings.Contains(logs.String(), ownershell.StartStageMessage(stage)) {
				t.Fatal("missing stage sentence in daemon log")
			}
			select {
			case event := <-events:
				if event.StartCause != stage || len(event.Data) != 0 {
					t.Fatal("diagnostic changed or included data")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("missing asynchronous diagnostic")
			}
		})
	}
}

func TestFramedStageProcess(t *testing.T) {
	if os.Args[len(os.Args)-1] == "--probe" {
		if Probe(os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	var boot HelperFrame
	if decoder.Decode(&boot) != nil {
		os.Exit(1)
	}
	stage := os.Args[len(os.Args)-1]
	switch {
	case stage == "stopped":
		_ = json.NewEncoder(os.Stdout).Encode(HelperFrame{Op: "ready", ID: boot.ID, OK: true})
	case strings.HasPrefix(stage, "control:"), strings.HasPrefix(stage, "unclean:"):
		_ = json.NewEncoder(os.Stdout).Encode(HelperFrame{Op: "ready", ID: boot.ID, OK: true})
		var request HelperFrame
		if decoder.Decode(&request) != nil {
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(HelperFrame{Op: "control_failed", ID: request.ID, StartStage: strings.TrimPrefix(strings.TrimPrefix(stage, "control:"), "unclean:"), StartCause: ownershell.StartCauseBootstrap})
		if strings.HasPrefix(stage, "unclean:") {
			os.Exit(1)
		}
	default:
		_ = json.NewEncoder(os.Stdout).Encode(HelperFrame{Op: "start_failed", ID: boot.ID, StartStage: stage, StartCause: ownershell.StartCauseBootstrap})
	}
	os.Exit(0)
}

func TestExpiredLeaseDiagnosticProcess(t *testing.T) {
	if os.Args[len(os.Args)-1] != "expired-lease-helper" {
		return
	}
	calls := 0
	_ = serveHelperWithClock(os.Stdin, os.Stdout, ResolveIdentity, func() time.Time {
		calls++
		if calls <= 2 {
			return time.Now()
		}
		return time.Now().Add(time.Hour)
	})
	os.Exit(0)
}

func TestAsyncLeaseFailureReachesParent(t *testing.T) {
	c, b, g, key := fixture(t)
	command := c.command
	command.Root = b.Root
	command.Args = []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestExpiredLeaseDiagnosticProcess$", "--", "expired-lease-helper"}
	events := make(chan error, 1)
	command.diagnostic = func(_ string, err error) { events <- err }
	peer, err := startPeer(t.Context(), command, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	select {
	case err = <-events:
	case <-ctx.Done():
		t.Fatal("no asynchronous lease diagnostic")
	}
	if _, cause := startDiagnostic(err); cause != "lease_check" {
		t.Fatalf("wrong expiry stage: %s", cause)
	}
	<-peer.done
	if err = peer.call(ctx, HelperFrame{Op: "close", Grant: g}); err != nil {
		t.Fatal("cleanup proof lost")
	}
}

func TestUnframedPostStartExitReachesParent(t *testing.T) {
	c, _, g, key := fixture(t)
	command := c.command
	command.Args = []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestFramedStageProcess$", "--", "stopped"}
	events := make(chan error, 1)
	command.diagnostic = func(_ string, err error) { events <- err }
	peer, err := startPeer(t.Context(), command, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-events:
		if _, cause := startDiagnostic(err); cause != "helper_stopped" {
			t.Fatalf("wrong exit diagnostic: %s", cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unexpected exit had no diagnostic")
	}
	<-peer.done
	if err := peer.call(t.Context(), HelperFrame{Op: "close", Grant: g}); err != nil {
		t.Fatal("cleanup proof lost")
	}
}

func TestControlDiagnosticDoesNotProveCleanup(t *testing.T) {
	c, _, g, key := fixture(t)
	command := c.command
	command.Args = []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestFramedStageProcess$", "--", "unclean:cleanup_shell"}
	peer, err := startPeer(t.Context(), command, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	err = peer.call(t.Context(), HelperFrame{Op: "input", Grant: g, Seq: 1})
	if _, cause := startDiagnostic(err); cause != "cleanup_shell" {
		t.Fatalf("lost cleanup diagnostic: %s", cause)
	}
	<-peer.done
	if peer.call(t.Context(), HelperFrame{Op: "close", Grant: g}) == nil {
		t.Fatal("a failure frame was treated as successful cleanup proof")
	}
}

func TestServiceControlFailureKeepsExactCleanupReceipt(t *testing.T) {
	base, b, g, key := fixture(t)
	service := NewService(func(RPC) error { return nil })
	if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	b, _ = service.Binding(b.Session)
	g.Grant.ConnectionID, g.Grant.Audience = b.Connection, b.Audience
	g = ownershell.SignGrant(key, g.Grant)
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}).OK {
		t.Fatal("configure")
	}
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g}).OK {
		t.Fatal("open")
	}
	reply := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "input", Grant: g, Seq: 2})
	if reply.OK || reply.StartCause != "input_sequence" {
		t.Fatal("missing control diagnostic")
	}
	coordinator := service.launches[b.Session].coordinator
	coordinator.sessionMu.Lock()
	peer := coordinator.peers[g.Grant.ShellGeneration]
	coordinator.sessionMu.Unlock()
	<-peer.done
	launch := ownershell.SupervisorIdentity(b, g).Launch
	receipt := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "close_shell", Launch: launch, Shell: g.Grant.ShellGeneration})
	if !receipt.OK || receipt.Cleanup == nil || receipt.Cleanup.ValidateFor(launch, g.Grant.ShellGeneration) != nil {
		t.Fatal("successful cleanup receipt lost after consent invalidation")
	}
	foreign := launch
	foreign.ConnectionID = "foreign"
	if service.execute(t.Context(), RPC{SessionID: b.Session, Op: "close_shell", Launch: foreign, Shell: g.Grant.ShellGeneration}).OK {
		t.Fatal("cleanup receipt crossed launch identity")
	}
}
