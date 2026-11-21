package ownershell

import (
	"crypto/ed25519"
	"testing"
)

func TestPrivilegedShellChoiceIsPerShellAndHumanOnly(t *testing.T) {
	for _, kind := range []string{"human", "agent", "runtime"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			f.actor.Kind = kind
			f.r.binding.Runtime = "host"
			f.r.binding.Consent = "consent"
			f.r.binding.HostConsentValid = true
			info, err := f.m.OpenPrivileged(t.Context(), f.actor, "session", true)
			if kind != "human" {
				if err == nil {
					t.Fatal("nonhuman opened privileged shell")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !info.Privileged {
				t.Fatal("lost privileged metadata")
			}
			shell := f.m.shells[info.Handle]
			renewed := f.m.grant(shell)
			if !renewed.Grant.Privileged {
				t.Fatal("renewal lost privileged choice")
			}
			expected := renewed.Grant
			expected.Privileged = false
			if VerifyGrant(f.m.key.Public().(ed25519.PublicKey), renewed, expected, 1, f.now) == nil {
				t.Fatal("grant changed privilege")
			}
			ordinary, err := f.m.Open(t.Context(), f.actor, "session")
			if err != nil {
				t.Fatal(err)
			}
			if ordinary.Privileged || f.m.shells[ordinary.Handle].signed.Grant.Privileged {
				t.Fatal("privilege leaked to ordinary shell")
			}
		})
	}
}

func TestPrivilegedShellRefusesOtherRuntimes(t *testing.T) {
	f := setup(t)
	if _, err := f.m.OpenPrivileged(t.Context(), f.actor, "session", true); err == nil {
		t.Fatal("sandbox privileged shell opened")
	}
}

func TestPrivilegedChoiceSurvivesPersistentCustody(t *testing.T) {
	f, _, _ := setupRetainedManager(t)
	info, err := f.m.OpenPrivileged(t.Context(), f.actor, "session", true)
	if err != nil {
		t.Fatal(err)
	}
	shell := f.m.shells[info.Handle]
	if !info.Privileged || !shell.retained.custody.Privileged || !f.m.grant(shell).Grant.Privileged {
		t.Fatal("privilege lost across custody/grant")
	}
	ordinary, err := f.m.Open(t.Context(), f.actor, "session")
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Privileged || f.m.shells[ordinary.Handle].retained.custody.Privileged {
		t.Fatal("persistent ordinary shell inherited privilege")
	}
}
