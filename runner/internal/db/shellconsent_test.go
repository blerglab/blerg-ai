package db_test

import (
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"strings"
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

func TestPersistentShellConsentRequiresExactLoginAndWithdrawalIsTerminal(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	const session = "00000000-0000-4000-8000-000000000269"
	const daemon = "00000000-0000-4000-8000-000000000001"
	if err := db.UpsertDaemon(ctx, pool, daemon, "desktop", "local", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/project", "", "", ""); err != nil {
		t.Fatal(err)
	}
	c := protocol.NewShellConsentV2(protocol.HostShellConsent{ID: "acceptance", ChallengeID: "fresh"}, protocol.ShellCustody{Owner: "owner", Login: "login", Session: session, Runtime: "host"}, time.Now().Add(time.Minute).Unix())
	if err := db.CreatePersistentShellChallenge(ctx, pool, c); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptPersistentShellChallenge(ctx, pool, "owner", "other", session, "fresh"); err == nil {
		t.Fatal("another login accepted")
	}
	got, err := db.AcceptPersistentShellChallenge(ctx, pool, "owner", "login", session, "fresh")
	if err != nil || got != c {
		t.Fatal("exact acceptance failed", err)
	}
	if _, err = db.AcceptPersistentShellChallenge(ctx, pool, "owner", "login", session, "fresh"); err == nil {
		t.Fatal("challenge accepted twice")
	}
	if err = db.WithdrawPersistentShellConsent(ctx, pool, "owner", "login", session, "acceptance"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ReadPersistentShellConsent(ctx, pool, "owner", "login", session, "acceptance"); err == nil {
		t.Fatal("withdrawal revived")
	}
}

func TestPersistentChallengeOwnerCapAndPruning(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	const daemon = "00000000-0000-0000-0000-000000000101"
	const session = "00000000-0000-0000-0000-000000000102"
	if err := db.UpsertDaemon(ctx, pool, daemon, "test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/one", "org/one", "", ""); err != nil {
		t.Fatal(err)
	}
	c := protocol.NewShellConsentV2(protocol.HostShellConsent{ID: "acceptance", ChallengeID: "challenge"}, protocol.ShellCustody{Owner: "owner", Login: "login", Session: session}, time.Now().Add(time.Minute).Unix())
	for n := range 32 {
		c.Host.ID = fmt.Sprintf("acceptance-%d", n)
		c.Host.ChallengeID = fmt.Sprintf("challenge-%d", n)
		if err := db.CreatePersistentShellChallenge(ctx, pool, c); err != nil {
			t.Fatal(err)
		}
	}
	c.Host.ID = "overflow"
	c.Host.ChallengeID = "overflow"
	if db.CreatePersistentShellChallenge(ctx, pool, c) == nil {
		t.Fatal("owner challenge cap missing")
	}
	if _, err := pool.Exec(ctx, `UPDATE shell_host_consents_v2 SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := db.CreatePersistentShellChallenge(ctx, pool, c); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM shell_host_consents_v2`).Scan(&count); err != nil || count != 1 {
		t.Fatal("expired unaccepted challenges retained", count, err)
	}
}

// Simulate a runner stopping immediately after the database call: no API log
// callback runs, but the committed denial must already contain its audit event.
func TestPersistentWithdrawalAuditSurvivesBeforeAPILog(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	const session = "00000000-0000-4000-8000-000000000270"
	const daemon = "00000000-0000-4000-8000-000000000001"
	if err := db.UpsertDaemon(ctx, pool, daemon, "desktop", "local", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/project", "", "", ""); err != nil {
		t.Fatal(err)
	}
	c := protocol.NewShellConsentV2(protocol.HostShellConsent{ID: "audit-acceptance", ChallengeID: "audit-challenge"}, protocol.ShellCustody{Owner: "owner", Login: "login", Session: session, Runtime: "host"}, time.Now().Add(time.Minute).Unix())
	if err := db.CreatePersistentShellChallenge(ctx, pool, c); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptPersistentShellChallenge(ctx, pool, "owner", "login", session, c.Host.ChallengeID); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := db.WithdrawPersistentShellConsentOnce(ctx, pool, "owner", "login", session, c.Host.ID); err != nil || !changed {
		t.Fatal("withdrawal failed", err)
	}
	var audit []byte
	if err := pool.QueryRow(ctx, `SELECT withdrawal_audit FROM shell_host_consents_v2 WHERE acceptance_id=$1`, c.Host.ID).Scan(&audit); err != nil {
		t.Fatal("committed withdrawal has no durable audit", err)
	}
	if !strings.Contains(string(audit), `"consent_withdraw"`) || !strings.Contains(string(audit), c.PinsDigest()) {
		t.Fatal("durable audit lacks withdrawal context")
	}
	if _, changed, err := db.WithdrawPersistentShellConsentOnce(ctx, pool, "owner", "login", session, c.Host.ID); err != nil || changed {
		t.Fatal("repeat changed withdrawal", err)
	}
	var repeat []byte
	if err := pool.QueryRow(ctx, `SELECT withdrawal_audit FROM shell_host_consents_v2 WHERE acceptance_id=$1`, c.Host.ID).Scan(&repeat); err != nil || string(repeat) != string(audit) {
		t.Fatal("repeat replaced original audit", err)
	}
	// If audit creation fails, neither the denial nor its timestamp may commit.
	c.Host.ID = "rollback-acceptance"
	c.Host.ChallengeID = "rollback-challenge"
	c = protocol.NewShellConsentV2(c.Host, c.Pins, c.ExpiresAt)
	if err := db.CreatePersistentShellChallenge(ctx, pool, c); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptPersistentShellChallenge(ctx, pool, "owner", "login", session, c.Host.ChallengeID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.WithdrawPersistentShellConsentAudited(ctx, pool, "owner", "login", session, c.Host.ID, func(protocol.ShellConsentV2) ([]byte, error) { return nil, fmt.Errorf("audit unavailable") }); err == nil {
		t.Fatal("withdrawal committed without audit")
	}
	if _, err := db.ReadPersistentShellConsent(ctx, pool, "owner", "login", session, c.Host.ID); err != nil {
		t.Fatal("failed audit committed a denial", err)
	}
}
