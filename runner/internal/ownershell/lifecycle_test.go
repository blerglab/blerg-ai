package ownershell

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type lifecycleFixture struct {
	fakeSupervisor
	proof protocol.SupervisorCleanup
	phase protocol.SupervisorPhase
	slots int
}

func (f *lifecycleFixture) AdmitShell(_ context.Context, s protocol.SupervisorShell) error {
	if s.Validate() != nil || f.phase != "running" || f.slots >= 4 {
		return ErrDenied
	}
	f.slots++
	return nil
}
func (f *lifecycleFixture) Notify(_ context.Context, _ protocol.SupervisorLaunch, p protocol.SupervisorPhase) error {
	f.phase = p
	return nil
}
func (f *lifecycleFixture) CloseShell(context.Context, protocol.SupervisorShell, string) (protocol.SupervisorCleanup, error) {
	return f.proof, nil
}
func (f *lifecycleFixture) CloseLaunch(context.Context, protocol.SupervisorLaunch, string) (protocol.SupervisorCleanup, error) {
	return f.proof, nil
}
func (f *lifecycleFixture) RecoverLaunch(context.Context, protocol.SupervisorLaunch) (protocol.SupervisorCleanup, error) {
	return f.proof, nil
}
func (f *lifecycleFixture) OpenReserved(ctx context.Context, _ protocol.SupervisorShell, b Binding, g SignedGrant, k ed25519.PublicKey) (<-chan []byte, error) {
	if f.phase != "running" {
		return nil, ErrDenied
	}
	return f.Open(ctx, b, g, k)
}
func TestLifecycleBridgeRequiresExactCleanupProof(t *testing.T) {
	f := setup(t)
	i, _ := f.open(t)
	s := f.m.shells[i.Handle]
	identity := SupervisorIdentity(s.binding, s.signed)
	peer := &lifecycleFixture{phase: "running"}
	bridge := NewLifecycleBridge(peer, peer)
	if _, err := bridge.Open(t.Context(), s.binding, s.signed, nil); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "old-connection", "old-generation", "one-shell", "valid"} {
		proof := protocol.SupervisorCleanup{Launch: identity.Launch, ShellGeneration: identity.ShellGeneration, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
		switch mode {
		case "missing":
			proof.Reaped = false
		case "old-connection":
			proof.ConnectionID = "old"
		case "old-generation":
			proof.RuntimeGeneration++
		case "one-shell":
			proof.ShellGeneration = ""
		}
		peer.proof = proof
		err := bridge.Close(t.Context(), s.signed)
		if (err == nil) != (mode == "valid") {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	peer.proof = protocol.SupervisorCleanup{Launch: identity.Launch, ShellGeneration: identity.ShellGeneration, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
	if _, err := bridge.CloseLaunch(t.Context(), identity.Launch, "stop"); err == nil {
		t.Fatal("single-shell proof released all-launch storage")
	}
	peer.proof.ShellGeneration = ""
	if _, err := bridge.CloseLaunch(t.Context(), identity.Launch, "stop"); err != nil {
		t.Fatal(err)
	}
}
func TestLifecycleBridgePausedAndFourShellAdmission(t *testing.T) {
	f := setup(t)
	binding := f.r.binding
	peer := &lifecycleFixture{phase: "paused"}
	bridge := NewLifecycleBridge(peer, peer)
	grant := SignedGrant{Grant: protocol.ShellGrant{ShellGeneration: "one"}}
	if _, err := bridge.Open(t.Context(), binding, grant, nil); err == nil {
		t.Fatal("paused admission")
	}
	peer.phase = "running"
	for i := 0; i < 4; i++ {
		grant.Grant.ShellGeneration = string(rune('a' + i))
		if _, err := bridge.Open(t.Context(), binding, grant, nil); err != nil {
			t.Fatal(err)
		}
	}
	grant.Grant.ShellGeneration = "five"
	if _, err := bridge.Open(t.Context(), binding, grant, nil); err == nil {
		t.Fatal("fifth shell admitted")
	}
}
func TestRecoveryProofCannotClearDifferentIncarnation(t *testing.T) {
	f := setup(t)
	launch := SupervisorIdentity(f.r.binding, SignedGrant{}).Launch
	key := launch
	f.m.fenced[key] = true
	proof := protocol.SupervisorCleanup{Launch: launch, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
	proof.RuntimeGeneration++
	if err := f.m.Recovered(t.Context(), f.actor, "session", proof); err == nil || !f.m.fenced[key] {
		t.Fatal("old proof unfenced runtime")
	}
	proof.Launch = launch
	if err := f.m.Recovered(t.Context(), f.actor, "session", proof); err != nil || f.m.fenced[key] {
		t.Fatalf("exact recovery refused: %v", err)
	}
}
