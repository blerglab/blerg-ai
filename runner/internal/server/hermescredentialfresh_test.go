package server

import (
	"testing"
)

func TestHermesPersonalCredentialChangesAreVisibleImmediately(t *testing.T) {
	core := &fakeK8s{}
	jm := newTestJobManager(t, core)
	proof := coreProof{SessionID: "fixture-login"}
	read := func(want bool) {
		t.Helper()
		has, ok := jm.personalHermesCredential(t.Context(), "fixture-owner", proof)
		if !ok || has != want {
			t.Fatalf("credential presence=%v known=%v, want %v", has, ok, want)
		}
	}
	read(false)
	core.mu.Lock()
	core.credentialKinds = []string{"hermes"}
	core.mu.Unlock()
	read(true)
	core.mu.Lock()
	core.credentialKinds = nil
	core.mu.Unlock()
	read(false)
}
