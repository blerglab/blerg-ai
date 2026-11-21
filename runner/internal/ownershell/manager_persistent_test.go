package ownershell

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type retainedAuthorityFixture struct {
	policy  protocol.ShellPolicy
	until   time.Time
	unknown bool
}

func (a *retainedAuthorityFixture) Authorize(context.Context, Actor) (protocol.ShellPolicy, error) {
	return a.policy, nil
}
func (a *retainedAuthorityFixture) AuthorizePersistent(context.Context, Actor) (protocol.ShellPolicy, time.Time, error) {
	if a.unknown {
		return protocol.ShellPolicy{}, time.Time{}, ErrUnknown
	}
	return a.policy, a.until, nil
}

type retainedSupervisorFixture struct {
	fakeSupervisor
	registry                           *CustodyRegistry
	opens, prepares, commits, suspends int
	custodyCloses                      int
	streams                            map[string]chan PersistentFrame
}

func (s *retainedSupervisorFixture) OpenCustody(_ context.Context, b Binding, c protocol.ShellCustody, g SignedGrant, k ed25519.PublicKey) (<-chan PersistentFrame, error) {
	s.opens++
	if err := s.registry.Reserve(c); err != nil {
		return nil, err
	}
	_ = s.registry.Activate(c.Digest())
	out := make(chan PersistentFrame, 16)
	s.streams[c.Digest()] = out
	return out, nil
}
func (s *retainedSupervisorFixture) SuspendCustody(context.Context, Binding) error {
	s.suspends++
	return nil
}
func (s *retainedSupervisorFixture) InventoryCustody(_ context.Context, b Binding, v CustodyInventory) (CustodyInventory, error) {
	s.registry.Suspend(v.Scope)
	if _, err := s.registry.Reconcile(v, v.Challenge); err != nil {
		return CustodyInventory{}, err
	}
	return s.registry.Snapshot(v.Scope, v.Challenge), nil
}
func (s *retainedSupervisorFixture) PrepareCustody(context.Context, Binding, string, CustodyInventory, ed25519.PublicKey, []SignedGrant) error {
	s.prepares++
	return nil
}
func (s *retainedSupervisorFixture) CommitCustody(_ context.Context, b Binding, tx string, gs []SignedGrant) (map[string]<-chan PersistentFrame, error) {
	s.commits++
	out := map[string]<-chan PersistentFrame{}
	for _, g := range gs {
		ch := make(chan PersistentFrame, 16)
		s.streams[g.Grant.CustodyID] = ch
		out[g.Grant.CustodyID] = ch
	}
	return out, nil
}
func (s *retainedSupervisorFixture) CloseCustody(_ context.Context, c protocol.ShellCustody, tx string) (protocol.CustodyCleanup, error) {
	s.closed++
	s.custodyCloses++
	return protocol.CustodyCleanup{Version: 2, Custody: c.Digest(), Scope: c.PhysicalScope(), Transaction: tx, Responder: c.Daemon, ContainmentEmpty: true, PTYsClosed: true, Reaped: true, OutputJoined: true}, nil
}
func (s *retainedSupervisorFixture) ReceiptCustody(context.Context, SignedGrant, uint64) error {
	return nil
}

func setupRetainedManager(t *testing.T) (*fixture, *retainedAuthorityFixture, *retainedSupervisorFixture) {
	f := setup(t)
	f.now = time.Now()
	a := &retainedAuthorityFixture{policy: f.a.policy, until: f.now.Add(8 * 24 * time.Hour)}
	s := &retainedSupervisorFixture{registry: NewCustodyRegistry(), streams: map[string]chan PersistentFrame{}}
	f.m.authority = a
	f.m.supervisor = s
	b := &f.r.binding
	b.Runtime = "host"
	b.Container = ""
	b.Consent = "consent"
	b.HostConsentValid = true
	b.ServerCapabilities.LinuxHost = true
	b.ShellLink = protocol.ShellLinkWelcome{Version: 2, Runner: f.m.Incarnation(), Daemon: "daemon", Challenge: "challenge", Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: b.Owner, TokenID: "credential", Revision: 1, ExpiresAt: f.now.Add(8 * 24 * time.Hour)}}
	b.IdentityDigest = "identity"
	b.ServerCapabilities.ShellPersistence = 2
	b.ServerCapabilities.PersistentHost = true
	b.RuntimeCapabilities = b.ServerCapabilities
	return f, a, s
}

func TestManagerRetainsCustodyAcrossThirtyMinuteAbsence(t *testing.T) {
	f, a, s := setupRetainedManager(t)
	b := &f.r.binding
	i, err := f.m.Open(t.Context(), f.actor, b.Session)
	if err != nil {
		t.Fatal(err)
	}
	if s.opens != 1 || len(s.outputs) != 0 {
		t.Fatal("v2 silently used legacy shell path")
	}
	f.m.Unavailable(t.Context())
	f.now = f.now.Add(31 * time.Minute)
	f.m.Tick(t.Context())
	if s.closed != 0 || len(f.m.shells) != 1 {
		t.Fatal("inconclusive absence destroyed retained custody")
	}
	b.Connection = "reconnected"
	b.Audience = RuntimeAudience(b.Connection, b.RuntimeGeneration)
	b.ShellLink.Challenge = "fresh"
	f.m.SetPolicy(t.Context(), a.policy)
	if _, err = f.m.AttachWithReplay(t.Context(), f.actor, i.Handle, false); err != nil {
		t.Fatal(err)
	}
	if s.opens != 1 || s.prepares != 1 || s.commits != 1 || s.closed != 0 {
		t.Fatalf("reconnect replaced custody: opens=%d prepare=%d commit=%d closes=%d", s.opens, s.prepares, s.commits, s.closed)
	}
	f.m.Revoke(t.Context(), "", f.actor.Login, "")
	if s.closed != 1 || s.custodyCloses != 1 || len(f.m.shells) != 0 {
		t.Fatal("sign-out failed to end retained shell")
	}
}

func TestManagerRetainedUnknownAuthoritySuspendsAndLifetimeStillEnds(t *testing.T) {
	f, a, s := setupRetainedManager(t)
	info, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
	if err != nil {
		t.Fatal(err)
	}
	a.unknown = true
	if _, err = f.m.Attach(t.Context(), f.actor, info.Handle, false); !Unknown(err) {
		t.Fatalf("unknown authority was not inconclusive: %v", err)
	}
	if s.closed != 0 || f.m.shells[info.Handle] == nil || f.m.shells[info.Handle].retained.active {
		t.Fatal("unknown authority destroyed custody or retained access")
	}
	f.now = f.now.Add(7*24*time.Hour + time.Second)
	f.m.Tick(t.Context())
	if len(f.m.shells) != 0 || s.closed != 1 {
		t.Fatal("suspended custody escaped its finite lifetime")
	}
}

func TestManagerRetainedPolicyRevisionIsTerminal(t *testing.T) {
	f, a, s := setupRetainedManager(t)
	if _, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session); err != nil {
		t.Fatal(err)
	}
	a.policy.Revision++
	f.m.SetPolicy(t.Context(), a.policy)
	if s.closed != 1 || s.custodyCloses != 1 || len(f.m.shells) != 0 {
		t.Fatal("authoritative policy change did not close custody")
	}
}

func TestManagerAbsenceHoldRequiresAllocatedNonterminalCustody(t *testing.T) {
	f, _, s := setupRetainedManager(t)
	absent := f.now.Add(-31 * time.Minute)
	if _, ok := f.m.AbsenceHold(f.r.binding.Session, absent, f.now); ok {
		t.Fatal("persisted session alone held sweep")
	}
	info, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
	if err != nil {
		t.Fatal(err)
	}
	f.m.Unavailable(t.Context())
	first, ok := f.m.AbsenceHold(f.r.binding.Session, absent, f.now)
	if !ok {
		t.Fatal("allocated suspended custody did not hold sweep")
	}
	later, ok := f.m.AbsenceHold(f.r.binding.Session, f.now.Add(time.Hour), f.now)
	if !ok || !later.Equal(first) {
		t.Fatal("partial reconnect slid absence deadline")
	}
	f.m.Revoke(t.Context(), "", f.actor.Login, "")
	if _, ok := f.m.AbsenceHold(f.r.binding.Session, absent, f.now); ok || s.closed != 1 {
		t.Fatal("terminal custody held sweep")
	}
	_ = info
}

type gatedRetainedSupervisor struct {
	*retainedSupervisorFixture
	stage            string
	entered, release chan struct{}
}

func (s *gatedRetainedSupervisor) wait(ctx context.Context, stage string) error {
	if s.stage != stage {
		return nil
	}
	close(s.entered)
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *gatedRetainedSupervisor) InventoryCustody(ctx context.Context, b Binding, v CustodyInventory) (CustodyInventory, error) {
	out, err := s.retainedSupervisorFixture.InventoryCustody(ctx, b, v)
	if err != nil {
		return out, err
	}
	return out, s.wait(ctx, "inventory")
}
func (s *gatedRetainedSupervisor) PrepareCustody(ctx context.Context, b Binding, tx string, v CustodyInventory, key ed25519.PublicKey, gs []SignedGrant) error {
	if err := s.retainedSupervisorFixture.PrepareCustody(ctx, b, tx, v, key, gs); err != nil {
		return err
	}
	return s.wait(ctx, "prepare")
}
func (s *gatedRetainedSupervisor) CommitCustody(ctx context.Context, b Binding, tx string, gs []SignedGrant) (map[string]<-chan PersistentFrame, error) {
	out, err := s.retainedSupervisorFixture.CommitCustody(ctx, b, tx, gs)
	if err != nil {
		return out, err
	}
	return out, s.wait(ctx, "commit")
}
func TestManagerRetainedRevocationWinsReconnectAcknowledgments(t *testing.T) {
	for _, stage := range []string{"inventory", "prepare", "commit"} {
		t.Run(stage, func(t *testing.T) {
			f, a, base := setupRetainedManager(t)
			peer := &gatedRetainedSupervisor{retainedSupervisorFixture: base, stage: stage, entered: make(chan struct{}), release: make(chan struct{})}
			f.m.supervisor = peer
			info, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
			if err != nil {
				t.Fatal(err)
			}
			f.m.Unavailable(t.Context())
			f.now = f.now.Add(31 * time.Minute)
			f.m.SetPolicy(t.Context(), a.policy)
			done := make(chan error, 1)
			go func() { _, err := f.m.Attach(t.Context(), f.actor, info.Handle, false); done <- err }()
			select {
			case <-peer.entered:
			case <-time.After(time.Second):
				t.Fatal("reconnect did not reach barrier")
			}
			f.m.Revoke(t.Context(), "", f.actor.Login, "")
			close(peer.release)
			if err := <-done; err == nil {
				t.Fatal("late reconnect acknowledgment restored revoked access")
			}
			f.m.mu.Lock()
			remaining := len(f.m.shells)
			f.m.mu.Unlock()
			if remaining != 0 {
				t.Fatal("revoked shell retained a public handle")
			}
			adoptions := 0
			for _, event := range f.audit.events {
				if event.Event == Adopted {
					adoptions++
					if event.Reason != ReasonRevoked || event.Outcome != "denied" {
						t.Fatal("revoked adoption audit misclassified", event)
					}
				}
			}
			if adoptions != 1 {
				t.Fatal("revoked adoption did not retain its own audit event")
			}
			if stage != "commit" && base.commits != 0 {
				t.Fatal("commit crossed terminal revocation")
			}
		})
	}
}

func TestManagerRetainedOldStreamCannotCrossRekey(t *testing.T) {
	f, a, peer := setupRetainedManager(t)
	info, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
	if err != nil {
		t.Fatal(err)
	}
	f.m.mu.Lock()
	original := f.m.shells[info.Handle].signed
	f.m.mu.Unlock()
	old := peer.streams[original.Grant.CustodyID]
	f.m.Unavailable(t.Context())
	f.now = f.now.Add(31 * time.Minute)
	f.m.SetPolicy(t.Context(), a.policy)
	attached, err := f.m.Attach(t.Context(), f.actor, info.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	f.m.mu.Lock()
	current := f.m.shells[info.Handle].signed
	f.m.mu.Unlock()
	stale := PersistentFrame{Epoch: original.Grant.AccessEpoch, HistoryFrame: HistoryFrame{Offset: 0, End: 5, Data: []byte("stale")}}
	old <- stale
	peer.streams[current.Grant.CustodyID] <- stale
	peer.streams[current.Grant.CustodyID] <- PersistentFrame{Epoch: current.Grant.AccessEpoch, HistoryFrame: HistoryFrame{Offset: 0, End: 5, Data: []byte("fresh")}}
	select {
	case data := <-attached.Output:
		if string(data) != "fresh" {
			t.Fatal("old stream crossed epoch", string(data))
		}
	case <-time.After(time.Second):
		t.Fatal("new stream did not reach attachment")
	}
	f.m.Shutdown(t.Context())
}

type uncertainRetainedSupervisor struct {
	*retainedSupervisorFixture
	bad string
}

func (s *uncertainRetainedSupervisor) CloseCustody(ctx context.Context, c protocol.ShellCustody, tx string) (protocol.CustodyCleanup, error) {
	if c.Digest() == s.bad {
		return protocol.CustodyCleanup{}, ErrUnknown
	}
	return s.retainedSupervisorFixture.CloseCustody(ctx, c, tx)
}
func TestManagerRetainedUncertainChildCannotBlockHealthySiblingAdoption(t *testing.T) {
	f, a, base := setupRetainedManager(t)
	peer := &uncertainRetainedSupervisor{retainedSupervisorFixture: base}
	f.m.supervisor = peer
	first, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
	if err != nil {
		t.Fatal(err)
	}
	f.m.mu.Lock()
	bad := f.m.shells[first.Handle].retained.custody
	f.m.mu.Unlock()
	peer.bad = bad.Digest()
	if err = base.registry.BeginClose(peer.bad, "helper-crash"); err != nil {
		t.Fatal(err)
	}
	f.m.Unavailable(t.Context())
	f.now = f.now.Add(31 * time.Minute)
	f.m.SetPolicy(t.Context(), a.policy)
	at, err := f.m.Attach(t.Context(), f.actor, second.Handle, false)
	if err != nil {
		t.Fatal("healthy sibling could not adopt", err)
	}
	if err = f.m.InputFrame(t.Context(), f.actor, second.Handle, at.ID, 1, []byte("x"), true); err != nil {
		t.Fatal(err)
	}
	if f.m.shells[first.Handle] != nil || f.m.shells[second.Handle] == nil || base.commits != 1 || base.opens != 2 {
		t.Fatal("cleanup uncertainty revoked or replaced a healthy sibling")
	}
	if _, err = f.m.Open(t.Context(), f.actor, f.r.binding.Session); err == nil || base.opens != 2 {
		t.Fatal("unproven cleanup admitted replacement writers")
	}
	f.m.Shutdown(t.Context())
}

type gatedRetainedAuthority struct {
	*retainedAuthorityFixture
	block            bool
	entered, release chan struct{}
}

func (a *gatedRetainedAuthority) AuthorizePersistent(ctx context.Context, actor Actor) (protocol.ShellPolicy, time.Time, error) {
	p, until, err := a.retainedAuthorityFixture.AuthorizePersistent(ctx, actor)
	if a.block {
		close(a.entered)
		select {
		case <-a.release:
		case <-ctx.Done():
			return protocol.ShellPolicy{}, time.Time{}, ErrUnknown
		}
	}
	return p, until, err
}
func TestManagerRetainedApprovalCannotCrossControlOutage(t *testing.T) {
	f, a, peer := setupRetainedManager(t)
	gate := &gatedRetainedAuthority{retainedAuthorityFixture: a, entered: make(chan struct{}), release: make(chan struct{})}
	f.m.authority = gate
	info, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
	if err != nil {
		t.Fatal(err)
	}
	at, err := f.m.Attach(t.Context(), f.actor, info.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	gate.block = true
	done := make(chan error, 1)
	go func() { done <- f.m.InputFrame(t.Context(), f.actor, info.Handle, at.ID, 1, []byte("old"), true) }()
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("authorization did not reach barrier")
	}
	f.m.Unavailable(t.Context())
	f.m.SetPolicy(t.Context(), a.policy)
	close(gate.release)
	if err = <-done; !Unknown(err) {
		t.Fatal("old approval crossed an observed outage", err)
	}
	if peer.closed != 0 || peer.inputs != 0 || f.m.shells[info.Handle] == nil {
		t.Fatal("outage destroyed custody or allowed old input")
	}
	gate.block = false
	if _, err = f.m.Attach(t.Context(), f.actor, info.Handle, false); err != nil {
		t.Fatal("fresh approval could not restore control", err)
	}
	f.m.Shutdown(t.Context())
}

type unknownRetainedResolver struct {
	*fakeResolver
	unknown bool
}

func (r *unknownRetainedResolver) Resolve(ctx context.Context, id string) (Binding, error) {
	if r.unknown {
		return Binding{Owner: r.binding.Owner, Session: r.binding.Session}, ErrUnknown
	}
	return r.fakeResolver.Resolve(ctx, id)
}
func TestManagerRetainedInventoryCannotTurnUnknownIntoGone(t *testing.T) {
	f, _, peer := setupRetainedManager(t)
	resolver := &unknownRetainedResolver{fakeResolver: f.r}
	f.m.resolver = resolver
	info, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
	if err != nil {
		t.Fatal(err)
	}
	resolver.unknown = true
	if _, err = f.m.List(t.Context(), f.actor, f.r.binding.Session); !Unknown(err) {
		t.Fatal("inventory converted an unknown observation into terminal denial", err)
	}
	if peer.closed != 0 || f.m.shells[info.Handle] == nil || f.m.shells[info.Handle].retained.active {
		t.Fatal("unknown inventory lost custody or kept terminal access")
	}
	f.m.Shutdown(t.Context())
}

func TestManagerRetainedHelperSuspensionRequiresFreshEpochEvenBeforeWallExpiry(t *testing.T) {
	f, _, peer := setupRetainedManager(t)
	info, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
	if err != nil {
		t.Fatal(err)
	}
	f.m.mu.Lock()
	before := f.m.shells[info.Handle].signed.Grant.AccessEpoch
	f.m.mu.Unlock()
	f.r.binding.AccessSuspended = true
	if _, err = f.m.Attach(t.Context(), f.actor, info.Handle, false); err != nil {
		t.Fatal(err)
	}
	f.m.mu.Lock()
	after := f.m.shells[info.Handle].signed.Grant.AccessEpoch
	f.m.mu.Unlock()
	if after <= before || peer.prepares != 1 || peer.commits != 1 || peer.opens != 1 {
		t.Fatal("helper suspension revived an old access epoch")
	}
	f.m.Shutdown(t.Context())
}

func TestManagerRetainedCloseDiscardsHistory(t *testing.T) {
	f, _, _ := setupRetainedManager(t)
	info, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
	if err != nil {
		t.Fatal(err)
	}
	shell := f.m.shells[info.Handle]
	if err := shell.retained.history.Append([]byte("private output")); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Close(t.Context(), f.actor, info.Handle); err != nil {
		t.Fatal(err)
	}
	frame, err := shell.retained.history.Read(0, 100)
	if err == nil && len(frame.Data) != 0 {
		t.Fatal("closed shell retained terminal history")
	}
}

type selectiveRetainedCleanup struct {
	*retainedSupervisorFixture
	denied map[string]bool
}

func (s *selectiveRetainedCleanup) CloseCustody(ctx context.Context, c protocol.ShellCustody, tx string) (protocol.CustodyCleanup, error) {
	if s.denied[c.Digest()] {
		return protocol.CustodyCleanup{}, ErrUnknown
	}
	return s.retainedSupervisorFixture.CloseCustody(ctx, c, tx)
}
func TestManagerRetainedCleanupKeepsUnsettledSiblingFence(t *testing.T) {
	f, _, peer := setupRetainedManager(t)
	selective := &selectiveRetainedCleanup{peer, map[string]bool{}}
	f.m.supervisor = selective
	var ids []string
	for range 2 {
		info, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, info.Handle)
		selective.denied[f.m.shells[info.Handle].retained.custody.Digest()] = true
	}
	first := f.m.shells[ids[0]].retained.custody.Digest()
	for _, id := range ids {
		_ = f.m.Close(t.Context(), f.actor, id)
	}
	selective.denied[first] = false
	f.m.mu.Lock()
	f.m.retryRetainedCleanup(t.Context())
	fenced := f.m.fenced[physicalLaunch(f.r.binding)]
	remaining := len(f.m.retainedClosing)
	f.m.mu.Unlock()
	if remaining != 1 || !fenced {
		t.Fatalf("one proof cleared sibling fence: remaining=%d fenced=%v", remaining, fenced)
	}
}

func TestManagerRetainedAdoptionAuditIncludesCustodyEpochAndOutcome(t *testing.T) {
	f, a, _ := setupRetainedManager(t)
	info, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
	if err != nil {
		t.Fatal(err)
	}
	custody := f.m.shells[info.Handle].retained.custody.Digest()
	f.m.Unavailable(t.Context())
	f.r.binding.Connection = "fresh"
	f.r.binding.Audience = RuntimeAudience("fresh", f.r.binding.RuntimeGeneration)
	f.m.SetPolicy(t.Context(), a.policy)
	if _, err = f.m.Attach(t.Context(), f.actor, info.Handle, false); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range f.audit.events {
		raw, _ := json.Marshal(event)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		if fields["event"] == "adopt" {
			found = true
			if fields["login_id"] != f.actor.Login || fields["custody_digest"] != custody || fields["access_epoch"] != float64(2) || fields["outcome"] != "success" {
				t.Fatal("adoption audit lacks custody context", fields)
			}
		}
	}
	if !found {
		t.Fatal("adoption was not audited")
	}
}

func (f *fakeResolver) ConsumePersistentConsent(context.Context, Actor, Binding) (string, error) {
	return f.binding.Consent, nil
}

func TestManagerRetainedBusyRekeyDoesNotAuditAdoption(t *testing.T) {
	f, _, _ := setupRetainedManager(t)
	i, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
	if err != nil {
		t.Fatal(err)
	}
	s := f.m.shells[i.Handle]
	f.m.retainedLaunches[s.retained.custody.PhysicalScope()].rekeying = true
	before := len(f.audit.events)
	if err := f.m.rekeyRetained(t.Context(), s, s.binding); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if len(f.audit.events) != before {
		t.Fatal("busy request logged an adoption attempt")
	}
}

type retryConsentResolver struct {
	*fakeResolver
	released int
}

func (r *retryConsentResolver) ReleasePersistentConsent(context.Context, Actor, Binding, string) {
	r.released++
}

type refusedRetainedSupervisor struct{ *retainedSupervisorFixture }

func (s refusedRetainedSupervisor) OpenCustody(context.Context, Binding, protocol.ShellCustody, SignedGrant, ed25519.PublicKey) (<-chan PersistentFrame, error) {
	return nil, RefuseBeforeAllocation(ErrDenied)
}
func TestManagerRetainedPreallocationRefusalReleasesConsent(t *testing.T) {
	f, _, peer := setupRetainedManager(t)
	r := &retryConsentResolver{fakeResolver: f.r}
	f.m.resolver = r
	f.m.supervisor = refusedRetainedSupervisor{peer}
	if _, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session); err == nil {
		t.Fatal("refusal accepted")
	}
	if r.released != 1 || len(f.m.shells) != 0 || len(f.m.retainedClosing) != 0 || len(f.m.custody.records) != 0 {
		t.Fatal("retryable preallocation refusal consumed consent or left custody fenced")
	}
}

func TestManagerSandboxPersistenceClaimKeepsV1Shell(t *testing.T) {
	f, _, peer := setupRetainedManager(t)
	b := &f.r.binding
	b.Runtime = "sandbox"
	b.Container = "container"
	b.ServerCapabilities.Sandbox = true
	b.ServerCapabilities.PersistentSandbox = true
	b.RuntimeCapabilities = b.ServerCapabilities
	info, err := f.m.Open(t.Context(), f.actor, b.Session)
	if err != nil {
		t.Fatal(err)
	}
	if peer.opens != 0 || len(peer.outputs) != 1 || f.m.shells[info.Handle].signed.Grant.Version != 1 {
		t.Fatal("sandbox did not retain v1 behavior")
	}
}

type revokedConsentResolver struct {
	*retryConsentResolver
	manager *Manager
}

func (r revokedConsentResolver) ConsumePersistentConsent(ctx context.Context, a Actor, b Binding) (string, error) {
	id, err := r.fakeResolver.ConsumePersistentConsent(ctx, a, b)
	r.manager.Revoke(ctx, "", a.Login, "")
	return id, err
}
func TestManagerRetainedRevocationBeforeReservationReleasesUnusedConsent(t *testing.T) {
	f, _, peer := setupRetainedManager(t)
	r := &retryConsentResolver{fakeResolver: f.r}
	f.m.resolver = revokedConsentResolver{r, f.m}
	if _, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session); err == nil {
		t.Fatal("revoked allocation admitted")
	}
	if r.released != 1 || peer.opens != 0 {
		t.Fatal("revocation before reservation stranded unused consent")
	}
}

func TestConsentAuditRetainsRuntimeTarget(t *testing.T) {
	f := setup(t)
	c := protocol.ShellCustody{Session: "fixture", Consent: "consent"}
	consent := protocol.NewShellConsentV2(protocol.HostShellConsent{ID: "consent", ChallengeID: "challenge"}, c, 0)
	for _, closing := range []bool{false, true} {
		s := &shell{binding: Binding{RuntimeTargetID: "cluster"}, retained: &retainedShell{custody: c}}
		f.m.shells = map[string]*shell{}
		f.m.retainedClosing = map[string]*shell{}
		if closing {
			f.m.retainedClosing["fixture"] = s
		} else {
			f.m.shells["fixture"] = s
		}
		a := f.m.ConsentAuditSnapshot(consent, Event("consent"), "released")
		if a.RuntimeTargetID != "cluster" {
			t.Fatalf("consent audit lost target: %+v", a)
		}
	}
}

type keeperLossAudit struct{ closed chan struct{} }

func (a keeperLossAudit) Record(event Audit) {
	if event.Event == Closed {
		close(a.closed)
	}
}

func TestManagerKeeperLossPublishesPlainAllowlistedCause(t *testing.T) {
	f, _, peer := setupRetainedManager(t)
	info, err := f.m.Open(t.Context(), f.actor, f.r.binding.Session)
	if err != nil {
		t.Fatal(err)
	}
	attached, err := f.m.Attach(t.Context(), f.actor, info.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	f.m.mu.Lock()
	grant := f.m.shells[info.Handle].signed
	closed := make(chan struct{})
	f.m.audit = keeperLossAudit{closed: closed}
	f.m.mu.Unlock()
	peer.streams[grant.Grant.CustodyID] <- PersistentFrame{Epoch: grant.Grant.AccessEpoch, Ended: true, Cause: "keeper_unavailable"}
	select {
	case cause := <-attached.Errors:
		if cause != "keeper_unavailable" {
			t.Fatal("wrong failure cause", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("keeper loss silently closed terminal")
	}
	// The access-failure notice precedes physical cleanup. Join the latter
	// before shutdown can retry its outstanding custody transaction.
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("keeper loss cleanup did not finish")
	}
	f.m.Shutdown(t.Context())
}
