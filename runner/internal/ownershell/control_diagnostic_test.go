package ownershell

import (
	"context"
	"testing"
	"time"
)

type diagnosticSupervisor struct {
	*fakeSupervisor
	failure error
}

func (s *diagnosticSupervisor) Failure(SignedGrant) error { return s.failure }
func (s *diagnosticSupervisor) Input(context.Context, SignedGrant, uint64, []byte) error {
	return s.failure
}
func (s *diagnosticSupervisor) Resize(context.Context, SignedGrant, uint64, uint16, uint16) error {
	return s.failure
}
func (s *diagnosticSupervisor) Renew(context.Context, SignedGrant) error { return s.failure }

func TestControlDiagnosticsReachExactAttachment(t *testing.T) {
	for stage := range startStages {
		for _, op := range []string{"input", "resize", "renew", "output_closed"} {
			t.Run(stage+"/"+op, func(t *testing.T) {
				f := setup(t)
				f.r.binding.Runtime = "host"
				f.r.binding.HostConsentValid = true
				f.r.binding.Consent = "consent"
				info, at := f.open(t)
				failure := RefuseStart(ReasonOpenRuntime, stage)
				f.m.supervisor = &diagnosticSupervisor{f.s, failure}
				var err error
				switch op {
				case "input":
					err = f.m.InputFrame(t.Context(), f.actor, info.Handle, at.ID, 1, []byte("private-fixture"), true)
				case "resize":
					err = f.m.Resize(t.Context(), f.actor, info.Handle, at.ID, 1, 80, 24)
				case "renew":
					f.now = f.now.Add(15 * time.Second)
					f.m.Tick(t.Context())
				case "output_closed":
					// A failed host helper invalidates consent before output closes.
					f.r.binding.HostConsentValid = false
					close(f.s.outputs[0])
				}
				if (op == "input" || op == "resize") && StartCause(err) != stage {
					t.Fatalf("cause lost: %v", err)
				}
				select {
				case cause := <-at.Errors:
					if cause != stage {
						t.Fatalf("got %s, want %s", cause, stage)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("diagnostic did not reach attachment")
				}
				select {
				case _, ok := <-at.Output:
					if ok {
						t.Fatal("output stayed open")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("cleanup did not close output")
				}
			})
		}
	}
}

func TestAsyncDiagnosticsRespectRevokedAuthority(t *testing.T) {
	f := setup(t)
	_, at := f.open(t)
	f.m.supervisor = &diagnosticSupervisor{f.s, RefuseStart(ReasonOpenRuntime, "lease_check")}
	f.a.dead = true
	close(f.s.outputs[0])
	select {
	case <-at.Output:
	case <-time.After(3 * time.Second):
		t.Fatal("output not closed")
	}
	select {
	case <-at.Errors:
		t.Fatal("diagnostic sent after authority revocation")
	default:
	}
}

func TestAsyncDiagnosticsRespectCurrentSessionOwner(t *testing.T) {
	f := setup(t)
	_, at := f.open(t)
	f.m.supervisor = &diagnosticSupervisor{f.s, RefuseStart(ReasonOpenRuntime, "lease_check")}
	f.r.binding.Owner = "other"
	close(f.s.outputs[0])
	select {
	case <-at.Output:
	case <-time.After(3 * time.Second):
		t.Fatal("output not closed")
	}
	select {
	case <-at.Errors:
		t.Fatal("diagnostic reached the previous session owner")
	default:
	}
}

// Pending output must never bypass live validation to preserve a diagnostic.
// Exercise both the runtime queue and bytes already queued for browser delivery.
func TestBufferedOutputPreservesTerminalDiagnostic(t *testing.T) {
	for _, queue := range []string{"runtime", "attachment"} {
		for _, state := range []string{"consent_invalid", "resolver_refused", "owner_changed", "authority_revoked", "unsafe_cause"} {
			t.Run(queue+"/"+state, func(t *testing.T) {
				f := setup(t)
				f.r.binding.Runtime = "host"
				f.r.binding.HostConsentValid = true
				f.r.binding.Consent = "consent"
				info, at := f.open(t)
				f.m.mu.Lock()
				cause := "lease_check"
				if state == "unsafe_cause" {
					cause = "Bearer private-fixture\nforged"
				}
				f.m.supervisor = &diagnosticSupervisor{f.s, RefuseStart(ReasonOpenRuntime, cause)}
				f.r.binding.HostConsentValid = false
				switch state {
				case "resolver_refused":
					// Persisted owner/session identity survives runtime refusal.
					f.m.resolver = resolveFunc(func(context.Context, string) (Binding, error) {
						return f.r.binding, ErrDenied
					})
				case "owner_changed":
					f.r.binding.Owner = "other"
				case "authority_revoked":
					f.a.dead = true
				}
				if queue == "runtime" {
					f.s.outputs[0] <- []byte("private-fixture")
					close(f.s.outputs[0])
				} else {
					f.m.shells[info.Handle].attachment.output <- []byte("private-fixture")
				}
				f.m.mu.Unlock()
				if queue == "attachment" {
					data := <-at.Output
					wrote := false
					err := f.m.Forward(t.Context(), f.actor, info.Handle, at.ID, data, func([]byte) error {
						wrote = true
						return nil
					})
					if err == nil || wrote {
						t.Fatal("stale output reached browser")
					}
					close(f.s.outputs[0])
				}
				select {
				case _, ok := <-at.Output:
					if ok {
						t.Fatal("stale output reached attachment")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("output not closed")
				}
				want := state == "consent_invalid" || state == "resolver_refused"
				select {
				case got := <-at.Errors:
					if !want || got != "lease_check" {
						t.Fatalf("unexpected diagnostic %q", got)
					}
				default:
					if want {
						t.Fatal("buffered output erased terminal lease_check diagnostic")
					}
				}
			})
		}
	}
}
