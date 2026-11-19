package ownershell

import (
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func registryCustody(shell string) protocol.ShellCustody {
	return protocol.ShellCustody{Container: "container", Version: 2, Owner: "owner", Login: "login", Session: "session", Launch: "launch", Runtime: "sandbox", RuntimeGeneration: 1, Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}, Manifest: "manifest", Desktop: "desktop", Credential: "credential", OwnershipRevision: 1, Runner: "runner", Daemon: "daemon", Shell: shell, Nonce: shell + "-nonce", Identity: "identity", Created: time.Now().Unix(), LifetimeUntil: time.Now().Unix() + int64(protocol.DesktopShellLifetime/time.Second)}
}

func TestCustodyRegistryCloseIntentSurvivesInventory(t *testing.T) {
	r := NewCustodyRegistry()
	c := registryCustody("shell")
	if err := r.Reserve(c); err != nil {
		t.Fatal(err)
	}
	if err := r.Activate(c.Digest()); err != nil {
		t.Fatal(err)
	}
	if err := r.BeginClose(c.Digest(), "close"); err != nil {
		t.Fatal(err)
	}
	peer := CustodyInventory{Version: 2, Scope: c.PhysicalScope(), Challenge: "challenge", Revision: 1, Complete: true, Entries: []CustodyClaim{{Custody: c, State: CustodyActive}}}
	result, err := r.Reconcile(peer, "challenge")
	if err != nil || len(result.Eligible) != 0 || len(result.Closing) != 1 {
		t.Fatal("inventory revived close intent", result, err)
	}
	if r.Activate(c.Digest()) == nil {
		t.Fatal("terminal custody revived")
	}
}

func TestCustodyRegistryUnclaimedAllocationMustClose(t *testing.T) {
	r := NewCustodyRegistry()
	c := registryCustody("orphan")
	peer := CustodyInventory{Version: 2, Scope: c.PhysicalScope(), Challenge: "challenge", Revision: 1, Complete: true, Entries: []CustodyClaim{{Custody: c, State: CustodyActive}}}
	result, err := r.Reconcile(peer, "challenge")
	if err != nil || len(result.Eligible) != 0 || len(result.Closing) != 1 {
		t.Fatal("unclaimed allocation adopted or forgotten", result, err)
	}
	if r.Count(c.PhysicalScope()) != 1 {
		t.Fatal("orphan not counted")
	}
	if r.Activate(c.Digest()) == nil {
		t.Fatal("unclaimed allocation gained access")
	}
}

func TestCustodyRegistryUncertaintyConsumesBudgetAcrossRestart(t *testing.T) {
	r := NewCustodyRegistry()
	scope := ""
	var allocations []protocol.ShellCustody
	for _, shell := range []string{"one", "two", "three", "four"} {
		c := registryCustody(shell)
		scope = c.PhysicalScope()
		allocations = append(allocations, c)
		if err := r.Reserve(c); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range allocations {
		if err := r.BeginClose(c.Digest(), "close-"+c.Shell); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Reserve(registryCustody("fifth")); err == nil {
		t.Fatal("unproven cleanup freed a reservation")
	}
	r.DaemonRestart(scope, "new-daemon")
	if r.Count(scope) != 4 || r.Reserve(registryCustody("fifth")) == nil {
		t.Fatal("daemon incarnation hid old cleanup fence")
	}
}

func TestCustodyRegistryPartialOrReplayedInventoryCannotProveAnything(t *testing.T) {
	r := NewCustodyRegistry()
	c := registryCustody("shell")
	if err := r.Reserve(c); err != nil {
		t.Fatal(err)
	}
	peer := CustodyInventory{Version: 2, Scope: c.PhysicalScope(), Challenge: "challenge", Revision: 1, Complete: false}
	if _, err := r.Reconcile(peer, "challenge"); err == nil {
		t.Fatal("partial inventory proved absence")
	}
	peer.Complete = true
	if _, err := r.Reconcile(peer, "wrong"); err == nil {
		t.Fatal("old challenge accepted")
	}
	if r.Count(c.PhysicalScope()) != 1 {
		t.Fatal("inconclusive inventory dropped a reservation")
	}
	result, err := r.Reconcile(peer, "challenge")
	if err != nil || len(result.Closing) != 1 {
		t.Fatal("missing allocation not reconciled as closing", err)
	}
	if r.Count(c.PhysicalScope()) != 1 {
		t.Fatal("absence became cleanup proof")
	}
}

func TestCustodyRegistrySettlementNeedsExactIndependentProof(t *testing.T) {
	r := NewCustodyRegistry()
	c := registryCustody("shell")
	if err := r.Reserve(c); err != nil {
		t.Fatal(err)
	}
	if err := r.BeginClose(c.Digest(), "close"); err != nil {
		t.Fatal(err)
	}
	proof := protocol.CustodyCleanup{Version: 2, Custody: c.Digest(), Scope: c.PhysicalScope(), Transaction: "close", Responder: "connection", ContainmentEmpty: true, PTYsClosed: true, Reaped: true, OutputJoined: true}
	bad := proof
	bad.Reaped = false
	if r.Settle(bad, "connection") == nil {
		t.Fatal("incomplete cleanup acknowledged")
	}
	bad = proof
	bad.Transaction = "other"
	if r.Settle(bad, "connection") == nil {
		t.Fatal("wrong close transaction settled")
	}
	if r.Settle(proof, "old-connection") == nil {
		t.Fatal("wrong responder settled")
	}
	if err := r.Settle(proof, "connection"); err != nil {
		t.Fatal(err)
	}
	if r.Count(c.PhysicalScope()) != 0 {
		t.Fatal("proved empty reservation retained")
	}
	if r.Reserve(c) == nil {
		t.Fatal("old custody tombstone reused")
	}
}

func TestCustodyRegistryAbsenceHoldIsFiniteAndProcessLocal(t *testing.T) {
	r := NewCustodyRegistry()
	c := registryCustody("shell")
	absent := time.Unix(c.Created+1000, 0)
	if _, ok := r.AbsenceHold(c.Session, absent, absent); ok {
		t.Fatal("row without custody retained session")
	}
	if err := r.Reserve(c); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.AbsenceHold(c.Session, absent, absent); ok {
		t.Fatal("unallocated intent retained session")
	}
	if err := r.Activate(c.Digest()); err != nil {
		t.Fatal(err)
	}
	until, ok := r.AbsenceHold(c.Session, absent, absent)
	if !ok || !until.Equal(time.Unix(c.LifetimeUntil, 0)) {
		t.Fatal("hold not bounded by lifetime", until, ok)
	}
	if _, ok = r.AbsenceHold(c.Session, absent, time.Unix(c.LifetimeUntil, 0)); ok {
		t.Fatal("expired custody held a session forever")
	}
	fresh := NewCustodyRegistry()
	if _, ok = fresh.AbsenceHold(c.Session, absent, absent); ok {
		t.Fatal("runner restart retained absent process-local custody")
	}
}

func TestCustodyRegistryLostOpenAcknowledgmentClosesUnconfirmedAllocation(t *testing.T) {
	r := NewCustodyRegistry()
	c := registryCustody("pending")
	if err := r.Reserve(c); err != nil {
		t.Fatal(err)
	}
	peer := CustodyInventory{Version: 2, Scope: c.PhysicalScope(), Challenge: "challenge", Revision: 1, Complete: true, Entries: []CustodyClaim{{Custody: c, State: CustodyActive, Allocated: true}}}
	result, err := r.Reconcile(peer, "challenge")
	if err != nil || len(result.Eligible) != 0 || len(result.Closing) != 1 {
		t.Fatal("unconfirmed open became an unreachable reservation", result, err)
	}
}
