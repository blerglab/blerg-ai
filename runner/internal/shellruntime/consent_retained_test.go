package shellruntime

import (
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestRetainedConsentRequiresFreshAcceptanceAndCannotServeTwoChildren(t *testing.T) {
	c := protocol.ShellCustody{Version: 2, Owner: "owner", Login: "login", Session: "session", Launch: "launch", Runtime: "host", RuntimeGeneration: 1, Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}, Manifest: "manifest", Desktop: "desktop", Credential: "credential", OwnershipRevision: 1, Runner: "runner", Daemon: "daemon", Identity: "identity", Consent: "consent", Shell: "one", Nonce: "nonce", Created: time.Now().Unix(), LifetimeUntil: time.Now().Add(7 * 24 * time.Hour).Unix()}
	v := protocol.NewShellConsentV2(protocol.HostShellConsent{ID: "consent", ChallengeID: "challenge"}, c, time.Now().Add(time.Minute).Unix())
	entry := &registered{consents: map[string]*retainedConsent{"challenge": {value: v}}}
	service := NewService(func(RPC) error { return nil })
	if service.consumeRetainedConsent(entry, c) {
		t.Fatal("challenge alone authorized a host shell")
	}
	entry.consents["challenge"].accepted = true
	if !service.consumeRetainedConsent(entry, c) {
		t.Fatal("affirmative memory rejected")
	}
	other := c
	other.Shell = "two"
	if service.consumeRetainedConsent(entry, other) {
		t.Fatal("acceptance reused for another shell")
	}
	entry.consents["challenge"].revoked = true
	if service.consumeRetainedConsent(entry, c) {
		t.Fatal("withdrawn consent revived")
	}

}

func TestRetainedSourceObservationDistinguishesOutageFromReplacement(t *testing.T) {
	for _, known := range []bool{false, true} {
		service := NewService(func(RPC) error { return nil })
		mu := new(sync.Mutex)
		c := &retainedCoordinator{mu: mu, active: true, phase: "running", children: map[string]*retainedChild{}, registry: ownershell.NewCustodyRegistry()}
		entry := &registered{mu: mu, retained: c, command: Command{SourceState: func() (bool, bool) { return false, known }}}
		if service.checkRetainedSource(t.Context(), entry) == nil {
			t.Fatal("unverified source admitted access")
		}
		if c.terminal != known || c.active {
			t.Fatalf("source observation mishandled: known=%t terminal=%t active=%t", known, c.terminal, c.active)
		}
	}
}

func TestRecordedShellProofIncludesRetainedCurrentAndHistoricalCustody(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	child := &retainedChild{}
	c := &retainedCoordinator{mu: new(sync.Mutex), children: map[string]*retainedChild{"custody": child}}
	entry := &registered{retained: c}
	s.launches["session"] = entry
	if s.ProveRecordedShellsEmpty("session") == nil {
		t.Fatal("retained helper disappeared behind a nil v1 coordinator")
	}
	entry.retained = nil
	entry.retainedHistory = []*retainedCoordinator{c}
	if s.ProveRecordedShellsEmpty("session") == nil {
		t.Fatal("historical custody disappeared from cleanup proof")
	}
	child.closed = true
	if err := s.ProveRecordedShellsEmpty("session"); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedNewIdentityEpochNeedsClosedProofAndNewConsent(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	b := ownershell.Binding{Session: "session", Connection: "old-connection", RuntimeGeneration: 1}
	b.Audience = ownershell.RuntimeAudience(b.Connection, b.RuntimeGeneration)
	mu := new(sync.Mutex)
	child := &retainedChild{}
	c := &retainedCoordinator{mu: mu, binding: b, terminal: true, identityRevoked: true, children: map[string]*retainedChild{"old": child}}
	acceptance := &retainedConsent{accepted: true}
	entry := &registered{mu: mu, binding: b, phase: "running", retained: c, persistentIdentity: &Identity{}, consents: map[string]*retainedConsent{"challenge": acceptance}}
	s.launches[b.Session] = entry
	s.resetRetainedIdentity(t.Context(), entry)
	if entry.retained != c || entry.binding.Connection != b.Connection {
		t.Fatal("unproved identity cleanup erased old custody")
	}
	child.closed = true
	entry.terminal = true
	s.resetRetainedIdentity(t.Context(), entry)
	if entry.retained != c {
		t.Fatal("identity reset cleared a native terminal fence")
	}
	entry.terminal = false
	s.resetRetainedIdentity(t.Context(), entry)
	if entry.retained != nil || len(entry.retainedHistory) != 1 || entry.binding.Connection == b.Connection || entry.persistentIdentity != nil || !acceptance.revoked {
		t.Fatal("new identity epoch reused old authority")
	}
	if !c.terminal || !child.closed || c.binding.Connection != b.Connection {
		t.Fatal("reset mutated terminal historical custody")
	}
}

func TestRetainedWithdrawalAcknowledgesAlreadyClosedAllocation(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	value := protocol.NewShellConsentV2(protocol.HostShellConsent{ID: "consent", ChallengeID: "challenge"}, protocol.ShellCustody{Owner: "owner", Login: "login"}, time.Now().Add(-time.Minute).Unix())
	entry := &registered{persistentIdentity: &Identity{}, consents: map[string]*retainedConsent{"challenge": {value: value, accepted: true, used: "closed-custody"}}, retainedHistory: []*retainedCoordinator{{mu: new(sync.Mutex), children: map[string]*retainedChild{"closed-custody": {closed: true}}}}}
	b := ownershell.Binding{Runtime: "host", Owner: "owner", ShellLink: protocol.ShellLinkWelcome{Version: 2}}
	r := RPC{Op: "consent_withdraw", Actor: ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"}, PersistentConsent: &value}
	if reply := s.retainedConsentRPC(t.Context(), entry, b, r); !reply.OK {
		t.Fatal("closed allocation lost withdrawal acknowledgement")
	}
	if !entry.consents["challenge"].revoked {
		t.Fatal("withdrawal did not retain denial")
	}
}
