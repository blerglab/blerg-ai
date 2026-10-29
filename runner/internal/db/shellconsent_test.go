package db_test

import (
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"testing"
	"time"
)

func TestShellConsentExactLaunchAndIdentity(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	const daemon = "00000000-0000-0000-0000-000000000001"
	const session = "00000000-0000-0000-0000-000000000002"
	if err := db.UpsertDaemon(ctx, pool, daemon, "test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/one", "org/one", "", ""); err != nil {
		t.Fatal(err)
	}
	binding := db.ShellConsentBinding{Owner: "owner", Login: "login", Session: session, Launch: "launch", Connection: "connection", Generation: 1, Digest: "actual-os-identity"}
	consent := protocol.HostShellConsent{ID: "consent", OwnerID: "owner", LaunchGeneration: "launch", MachineID: "machine", Account: "account", UID: 1000, Shell: "/bin/sh", Cwd: "/project", ChallengeID: "challenge"}
	if err := db.CreateShellChallenge(ctx, pool, binding, consent, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetShellConsent(ctx, pool, binding); err == nil {
		t.Fatal("unaffirmed challenge enabled host")
	}
	mutations := []func(*db.ShellConsentBinding){func(b *db.ShellConsentBinding) { b.Owner = "admin-other" }, func(b *db.ShellConsentBinding) { b.Login = "different-login" }, func(b *db.ShellConsentBinding) { b.Launch = "relaunch" }, func(b *db.ShellConsentBinding) { b.Connection = "reconnected" }, func(b *db.ShellConsentBinding) { b.Generation++ }, func(b *db.ShellConsentBinding) { b.Digest = "spoofed-label" }}
	for _, mutate := range mutations {
		bad := binding
		mutate(&bad)
		if _, err := db.AcceptShellChallenge(ctx, pool, bad, consent.ChallengeID); err == nil {
			t.Fatal("mismatched challenge accepted")
		}
	}
	got, err := db.AcceptShellChallenge(ctx, pool, binding, consent.ChallengeID)
	if err != nil || got != consent {
		t.Fatal("affirmative consent not persisted", err)
	}
	if _, err = db.AcceptShellChallenge(ctx, pool, binding, consent.ChallengeID); err == nil {
		t.Fatal("challenge replay")
	}
	got, err = db.GetShellConsent(ctx, pool, binding)
	if err != nil || got != consent {
		t.Fatal("persisted consent unavailable", err)
	}
	for index, mutate := range mutations {
		if index == 1 {
			continue
		}
		bad := binding
		mutate(&bad)
		if _, err := db.GetShellConsent(ctx, pool, bad); err == nil {
			t.Fatal("old identity or launch reused consent")
		}
	}
	consent.ID = "expired"
	consent.ChallengeID = "expired"
	if err := db.CreateShellChallenge(ctx, pool, binding, consent, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptShellChallenge(ctx, pool, binding, consent.ChallengeID); err == nil {
		t.Fatal("expired challenge accepted")
	}
}

func TestShellConsentIdentityReturnDoesNotReviveAcceptanceOrChallenge(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	const daemon = "00000000-0000-0000-0000-000000000003"
	const session = "00000000-0000-0000-0000-000000000004"
	if err := db.UpsertDaemon(ctx, pool, daemon, "test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/one", "org/one", "", ""); err != nil {
		t.Fatal(err)
	}
	a := db.ShellConsentBinding{Owner: "owner", Login: "login", Session: session, Launch: "launch", Connection: "connection", Generation: 1, Digest: "identity-a"}
	accepted := protocol.HostShellConsent{ID: "accepted", OwnerID: a.Owner, ChallengeID: "accepted", LaunchGeneration: a.Launch}
	pending := accepted
	pending.ID = "pending"
	pending.ChallengeID = "pending"
	for _, consent := range []protocol.HostShellConsent{accepted, pending} {
		if err := db.CreateShellChallenge(ctx, pool, a, consent, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.AcceptShellChallenge(ctx, pool, a, accepted.ChallengeID); err != nil {
		t.Fatal(err)
	}
	b := a
	b.Digest = "identity-b"
	if _, err := db.GetShellConsent(ctx, pool, b); err == nil {
		t.Fatal("identity B reused A consent")
	}
	if _, err := db.GetShellConsent(ctx, pool, a); err == nil {
		t.Error("A acceptance revived after A to B to A")
	}
	if _, err := db.AcceptShellChallenge(ctx, pool, a, pending.ChallengeID); err == nil {
		t.Error("A pending challenge survived identity transition")
	}
}
