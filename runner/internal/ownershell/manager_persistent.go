package ownershell

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// PersistentFrame keeps helper-assigned offsets and access epochs intact until
// the manager commits bytes to its bounded, volatile history.
type PersistentFrame struct {
	HistoryFrame
	Cause string
	Epoch uint64
	Ended bool
}

// PersistentSupervisor is separate from v1: a transport cannot accidentally
// reinterpret a custody close or an absolute output receipt as a legacy grant.
type PersistentConsentProvider interface {
	ConsumePersistentConsent(context.Context, Actor, Binding) (string, error)
}

// Release is valid only before a physical allocation, never on uncertain IO.
type PersistentConsentReleaser interface {
	ReleasePersistentConsent(context.Context, Actor, Binding, string)
}

type PersistentSupervisor interface {
	OpenCustody(context.Context, Binding, protocol.ShellCustody, SignedGrant, ed25519.PublicKey) (<-chan PersistentFrame, error)
	SuspendCustody(context.Context, Binding) error
	InventoryCustody(context.Context, Binding, CustodyInventory) (CustodyInventory, error)
	PrepareCustody(context.Context, Binding, string, CustodyInventory, ed25519.PublicKey, []SignedGrant) error
	CommitCustody(context.Context, Binding, string, []SignedGrant) (map[string]<-chan PersistentFrame, error)
	CloseCustody(context.Context, protocol.ShellCustody, string) (protocol.CustodyCleanup, error)
	ReceiptCustody(context.Context, SignedGrant, uint64) error
}

type retainedShell struct {
	custody protocol.ShellCustody
	history *History
	active  bool
	stream  uint64
	stop    chan struct{}
	closing string
	retryAt time.Time
}
type retainedLaunch struct {
	lifetime int64
	key      ed25519.PrivateKey
	epoch    uint64
	rekeying bool
}

func (b Binding) persistent() bool {
	h := protocol.ShellLinkHello{Version: 2, Daemon: b.ShellLink.Daemon, Challenge: b.ShellLink.Challenge}
	return b.ShellLink.Matches(h, time.Now()) && b.IdentityDigest != "" && b.ShellLink.Desktop.OwnerID == b.Owner && b.ServerCapabilities.SupportsPersistence(b.Runtime) && b.RuntimeCapabilities.SupportsPersistence(b.Runtime)
}
func (m *Manager) persistentAuthority(ctx context.Context, a Actor) (time.Time, error) {
	authority, ok := m.authority.(PersistentAuthority)
	if !ok {
		return time.Time{}, ErrDenied
	}
	epoch := m.availabilityEpoch
	var p protocol.ShellPolicy
	var until time.Time
	err := m.external(ctx, func(call context.Context) error {
		var e error
		p, until, e = authority.AuthorizePersistent(call, a)
		return e
	})
	if err != nil {
		return time.Time{}, err
	}
	if !p.Enabled || p.Version != 1 || p.Revision != m.policy.Revision || !until.After(m.now()) {
		return time.Time{}, ErrDenied
	}
	if epoch != m.availabilityEpoch || !m.available {
		return time.Time{}, ErrUnknown
	}
	return until, nil
}
func (m *Manager) initializeCustody(ctx context.Context, s *shell) (result error) {
	var consumed string
	defer func() {
		if result != nil && consumed != "" {
			m.releasePersistentConsent(ctx, s, consumed)
		}
	}()

	if s.binding.PersistenceDegraded {
		return ErrUnknown
	}
	for _, id := range s.binding.CustodyClosing {
		if id != "" {
			return Refuse(ReasonRuntimeFenced)
		}
	}
	if s.binding.ShellLink.Runner != m.incarnation {
		return ErrDenied
	}
	if _, ok := m.supervisor.(PersistentSupervisor); !ok {
		return ErrDenied
	}
	stamp := m.identityStamp(s.actor, s.binding)
	revision := m.policy.Revision
	until, err := m.persistentAuthority(ctx, s.actor)
	if stamp != m.identityStamp(s.actor, s.binding) || revision != m.policy.Revision || m.pending(s.actor, s.binding) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	b, w := s.binding, s.binding.ShellLink
	// Lifetime is fixed at allocation and can only be shortened by login or
	// enrollment expiry. A future reconnect cannot replace these original pins.
	deadline := m.now().Add(7 * 24 * time.Hour)
	if until.Before(deadline) {
		deadline = until
	}
	if w.Desktop.ExpiresAt.Before(deadline) {
		deadline = w.Desktop.ExpiresAt
	}
	consentID := b.Consent
	if b.Runtime == "host" {
		provider, ok := m.resolver.(PersistentConsentProvider)
		if !ok {
			return Refuse(ReasonRuntimeConsent)
		}
		err = m.external(ctx, func(call context.Context) error {
			var e error
			consentID, e = provider.ConsumePersistentConsent(call, s.actor, b)
			return e
		})
		consumed = consentID
		if err != nil || consentID == "" || stamp != m.identityStamp(s.actor, s.binding) || revision != m.policy.Revision || m.pending(s.actor, s.binding) {
			return Refuse(ReasonRuntimeConsent)
		}
	}
	nonce, err := opaque()
	if err != nil {
		return err
	}
	c := protocol.ShellCustody{Privileged: s.signed.Grant.Privileged, Version: 2, Container: b.Container, Owner: s.actor.Owner, Login: s.actor.Login, Session: b.Session, Launch: b.Launch, Runtime: b.Runtime, RuntimeGeneration: b.RuntimeGeneration, Root: b.Root, Manifest: b.ManifestDigest, Desktop: w.Desktop.DesktopID, Credential: w.Desktop.TokenID, OwnershipRevision: w.Desktop.Revision, Runner: w.Runner, Daemon: w.Daemon, Shell: s.signed.Grant.ShellGeneration, Nonce: nonce, Identity: b.IdentityDigest, Consent: consentID, Created: m.now().Unix(), LifetimeUntil: deadline.Unix()}
	if c.Validate() != nil {
		return ErrDenied
	}
	scope := c.PhysicalScope()
	for id, v := range m.retainedLaunches {
		if id != scope && m.now().Unix() >= v.lifetime && m.custody.Count(id) == 0 {
			delete(m.retainedLaunches, id)
		}
	}
	launch := m.retainedLaunches[scope]
	if launch == nil {
		if len(m.retainedLaunches) >= 1024 {
			return ErrLimit
		}
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return e
		}
		launch = &retainedLaunch{key: key, epoch: 1}
		m.retainedLaunches[scope] = launch
	}
	if launch.rekeying {
		return ErrBusy
	}
	for _, other := range m.shells {
		if other.retained != nil && other.retained.custody.PhysicalScope() == scope && (!other.retained.active || other.binding.Connection != b.Connection) {
			return ErrBusy
		}
	}
	if err = m.custody.Reserve(c); err != nil {
		return err
	}
	launch.lifetime = max(launch.lifetime, c.LifetimeUntil)
	s.retained = &retainedShell{custody: c, history: NewHistory(ScrollbackBytes), active: true}
	return nil
}
func (m *Manager) retainedGrant(s *shell, b Binding, key ed25519.PrivateKey, epoch uint64) SignedGrant {
	c := s.retained.custody
	now := m.now().Unix()
	return SignGrant(key, protocol.ShellGrant{Privileged: c.Privileged, Version: 2, Audience: b.Audience, OwnerID: c.Owner, LoginID: c.Login, SessionID: c.Session, LaunchGeneration: c.Launch, ConnectionID: b.Connection, ShellGeneration: c.Shell, Runtime: c.Runtime, ConsentID: c.Consent, PolicyRevision: m.policy.Revision, IssuedAt: now, ExpiresAt: now + protocol.ShellGrantSeconds, CustodyID: c.Digest(), AccessEpoch: epoch})
}
func (m *Manager) suspendRetainedLocked(ctx context.Context, s *shell) {
	r := s.retained
	if r == nil || !r.active {
		return
	}
	scope := r.custody.PhysicalScope()
	if m.retainedAbsence[s.binding.Session].IsZero() {
		m.retainedAbsence[s.binding.Session] = m.now()
	}
	var b = s.binding
	for _, other := range m.shells {
		if other.retained == nil || other.retained.custody.PhysicalScope() != scope {
			continue
		}
		v := other.retained
		v.active = false
		v.stream++
		if v.stop != nil {
			close(v.stop)
			v.stop = nil
		}
		m.detachLocked(other, false)
	}
	m.custody.Suspend(scope)
	if peer, ok := m.supervisor.(PersistentSupervisor); ok {
		_ = m.external(ctx, func(call context.Context) error { return peer.SuspendCustody(call, b) })
	}
}
func (m *Manager) retainedLive(ctx context.Context, s *shell, b Binding, resolveErr error) (*shell, error) {
	r := s.retained
	if !m.now().Before(time.Unix(r.custody.LifetimeUntil, 0)) {
		m.closeLocked(ctx, s, ReasonLifetime)
		return nil, ErrDenied
	}
	if Unknown(resolveErr) {
		m.suspendRetainedLocked(ctx, s)
		return nil, ErrUnknown
	}
	for _, id := range b.CustodyClosing {
		if id == r.custody.Digest() {
			m.closeLocked(ctx, s, ReasonRuntime)
			return nil, ErrDenied
		}
	}
	old := s.binding
	if resolveErr != nil || !b.persistent() || b.Owner != old.Owner || b.Session != old.Session || b.Launch != old.Launch || b.Runtime != old.Runtime || b.RuntimeGeneration != old.RuntimeGeneration || b.Root != old.Root || b.ManifestDigest != old.ManifestDigest || b.IdentityDigest != old.IdentityDigest || b.Container != old.Container || !b.ShellLink.SameCustodian(old.ShellLink) || m.pending(s.actor, old) {
		m.closeLocked(ctx, s, ReasonRevoked)
		return nil, ErrDenied
	}
	if !m.available {
		m.suspendRetainedLocked(ctx, s)
		return nil, ErrUnknown
	}
	until, err := m.persistentAuthority(ctx, s.actor)
	if m.shells[s.handle] != s {
		return nil, ErrDenied
	}
	if err != nil {
		if Unknown(err) {
			m.suspendRetainedLocked(ctx, s)
			return nil, ErrUnknown
		}
		m.closeLocked(ctx, s, ReasonRevoked)
		return nil, ErrDenied
	}
	if until.Unix() < r.custody.LifetimeUntil { // An earlier original-login deadline is terminal when reached.
		if !until.After(m.now()) {
			m.closeLocked(ctx, s, ReasonRevoked)
			return nil, ErrDenied
		}
	}
	if !r.active || b.AccessSuspended || b.Connection != old.Connection || b.Audience != old.Audience || m.now().Unix() >= s.signed.Grant.ExpiresAt {
		if err := m.rekeyRetained(ctx, s, b); err != nil {
			return nil, err
		}
	}
	if m.shells[s.handle] != s || !r.active {
		return nil, ErrDenied
	}
	return s, nil
}
func (m *Manager) rekeyRetained(ctx context.Context, s *shell, b Binding) (result error) {
	scope := s.retained.custody.PhysicalScope()
	launch := m.retainedLaunches[scope]
	if launch == nil || launch.rekeying {
		return ErrBusy
	}
	defer func() {
		if result != nil {
			reason := ReasonUnavailable
			if m.shells[s.handle] != s || m.pending(s.actor, s.binding) || s.retained.closing != "" {
				reason = ReasonRevoked
			}
			m.record(s, Adopted, reason)
		}
	}()
	launch.rekeying = true
	initialStamp := m.identityStamp(s.actor, s.binding)
	initialPolicy := m.policy.Revision
	initialAvailability := m.availabilityEpoch
	validOriginal := func() bool {
		return m.shells[s.handle] == s && !m.pending(s.actor, s.binding) && s.retained.closing == "" && m.available && m.policy.Enabled && m.policy.Revision == initialPolicy && m.availabilityEpoch == initialAvailability && m.identityStamp(s.actor, s.binding) == initialStamp
	}
	defer func() { launch.rekeying = false }()
	m.suspendRetainedLocked(ctx, s)
	if !validOriginal() {
		return ErrUnknown
	}
	peer, ok := m.supervisor.(PersistentSupervisor)
	if !ok {
		return ErrDenied
	}
	challenge, err := opaque()
	if err != nil {
		return err
	}
	view := m.custody.Snapshot(scope, challenge)
	var remote CustodyInventory
	err = m.external(ctx, func(call context.Context) error {
		var e error
		remote, e = peer.InventoryCustody(call, b, view)
		return e
	})
	if err != nil {
		return err
	}
	if !validOriginal() {
		return ErrUnknown
	}
	reconciliation, err := m.custody.Reconcile(remote, challenge)
	if err != nil {
		return err
	}
	if len(reconciliation.Closing) != 0 {
		m.closeReconciledCustody(ctx, b, reconciliation.Closing)
		if !validOriginal() {
			return ErrUnknown
		}
		challenge, err = opaque()
		if err != nil {
			return err
		}
		// Cleanup can change both inventories. Re-snapshot before adopting
		// healthy existing children; pending cleanup still fences new writers.
		view = m.custody.Snapshot(scope, challenge)
		err = m.external(ctx, func(call context.Context) error {
			var e error
			remote, e = peer.InventoryCustody(call, b, view)
			return e
		})
		if err != nil {
			return err
		}
		if !validOriginal() {
			return ErrUnknown
		}
		reconciliation, err = m.custody.Reconcile(remote, challenge)
		if err != nil {
			return err
		}
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	launch.epoch++
	transaction, err := opaque()
	if err != nil {
		return err
	}
	var shells []*shell
	var grants []SignedGrant
	stamps := map[*shell][3]uint64{}
	for _, other := range m.shells {
		if other.retained == nil || other.retained.custody.PhysicalScope() != scope {
			continue
		}
		if other.retained.closing != "" || !m.now().Before(time.Unix(other.retained.custody.LifetimeUntil, 0)) {
			return ErrDenied
		}
		if _, err = m.persistentAuthority(ctx, other.actor); err != nil {
			return err
		}
		shells = append(shells, other)
		stamps[other] = m.identityStamp(other.actor, other.binding)
		grants = append(grants, m.retainedGrant(other, b, key, launch.epoch))
	}
	if len(shells) != len(reconciliation.Eligible) {
		return ErrDenied
	}
	policyRevision := m.policy.Revision
	valid := func() bool {
		if !validOriginal() || !m.available || !m.policy.Enabled || m.policy.Revision != policyRevision {
			return false
		}
		for _, other := range shells {
			if m.shells[other.handle] != other || m.pending(other.actor, other.binding) || stamps[other] != m.identityStamp(other.actor, other.binding) || other.retained.closing != "" {
				return false
			}
		}
		return true
	}
	err = m.external(ctx, func(call context.Context) error {
		return peer.PrepareCustody(call, b, transaction, remote, append(ed25519.PublicKey(nil), key[ed25519.SeedSize:]...), grants)
	})
	if err != nil || !valid() {
		return ErrUnknown
	}
	var streams map[string]<-chan PersistentFrame
	err = m.external(ctx, func(call context.Context) error {
		var e error
		streams, e = peer.CommitCustody(call, b, transaction, grants)
		return e
	})
	if err != nil || !valid() {
		return ErrUnknown
	}
	for _, other := range shells {
		if streams[other.retained.custody.Digest()] == nil {
			return ErrUnknown
		}
	}
	delete(m.retainedAbsence, b.Session)
	launch.key = key
	for i, other := range shells {
		other.binding = b
		other.signed = grants[i]
		other.lastGrant = m.now()
		other.runtimeInputSeq = 0
		other.runtimeResizeSeq = 0
		other.retained.active = true
		if err = m.custody.Activate(other.retained.custody.Digest()); err != nil {
			return err
		}
		m.startRetainedDrain(ctx, other, streams[other.retained.custody.Digest()])
		m.record(other, Adopted, ReasonRequested)
	}
	return nil
}
func (m *Manager) startRetainedDrain(ctx context.Context, s *shell, out <-chan PersistentFrame) {
	r := s.retained
	r.stream++
	generation := r.stream
	stop := make(chan struct{})
	r.stop = stop
	go func() {
		for {
			var frame PersistentFrame
			var ok bool
			select {
			case <-stop:
				return
			case frame, ok = <-out:
			}
			m.mu.Lock()
			if m.shells[s.handle] != s || r.stream != generation || !r.active {
				m.mu.Unlock()
				return
			}
			if !ok {
				m.suspendRetainedLocked(context.WithoutCancel(ctx), s)
				m.mu.Unlock()
				return
			}
			if frame.Epoch != s.signed.Grant.AccessEpoch {
				m.mu.Unlock()
				continue
			}
			if !m.available || m.pending(s.actor, s.binding) || m.now().Unix() >= s.signed.Grant.ExpiresAt || m.now().Unix() >= r.custody.LifetimeUntil {
				m.suspendRetainedLocked(context.WithoutCancel(ctx), s)
				m.mu.Unlock()
				return
			}
			if frame.Ended {
				m.publishFailure(s, RefuseStart(ReasonRuntime, frame.Cause))
				m.closeLocked(context.WithoutCancel(ctx), s, ReasonRuntime)
				m.mu.Unlock()
				return
			}
			for !m.outputBudget(s.binding.Session, len(frame.Data)) {
				m.mu.Unlock()
				if !pause(stop) {
					return
				}
				m.mu.Lock()
				if m.shells[s.handle] != s || r.stream != generation || !r.active {
					m.mu.Unlock()
					return
				}
			}
			if _, err := r.history.Receive(frame.HistoryFrame); err != nil {
				m.suspendRetainedLocked(context.WithoutCancel(ctx), s)
				m.mu.Unlock()
				return
			}
			end := frame.Offset + uint64(len(frame.Data))
			start := max(frame.Offset, s.scrollback.end)
			if frame.Offset > s.scrollback.end {
				s.scrollback = &scrollback{end: frame.Offset}
				m.detachLocked(s, false)
			}
			if end > start {
				chunk := append([]byte(nil), frame.Data[start-frame.Offset:]...)
				s.scrollback.write(chunk)
				if s.attachment != nil {
					select {
					case s.attachment.output <- chunk:
					default:
						m.detachLocked(s, false)
					}
				}
			}
			signed := s.signed
			// Retention receipt follows the in-memory history commit, never browser ACK.
			err := m.external(context.WithoutCancel(ctx), func(call context.Context) error {
				peer, ok := m.supervisor.(PersistentSupervisor)
				if !ok {
					return ErrDenied
				}
				return peer.ReceiptCustody(call, signed, end)
			})
			if err != nil && m.shells[s.handle] == s {
				m.suspendRetainedLocked(context.WithoutCancel(ctx), s)
			}
			m.mu.Unlock()
		}
	}()
}

// Closing entries stay independent of public handles and survive transport
// replacement. Only an exact physical cleanup proof can release their budget.
func (m *Manager) retryRetainedCleanup(ctx context.Context) {
	peer, ok := m.supervisor.(PersistentSupervisor)
	if !ok {
		return
	}
	var tasks []*shell
	for _, s := range m.retainedClosing {
		if len(tasks) >= 4 {
			break
		}
		if !m.now().Before(s.retained.retryAt) {
			tasks = append(tasks, s)
		}
	}
	for _, s := range tasks {
		r := s.retained
		r.retryAt = m.now().Add(10 * time.Second)
		var proof protocol.CustodyCleanup
		err := m.external(ctx, func(call context.Context) error {
			var e error
			proof, e = peer.CloseCustody(call, r.custody, r.closing)
			return e
		})
		if err == nil && m.custody.Settle(proof, proof.Responder) == nil {
			delete(m.retainedClosing, r.custody.Digest())
			launch := physicalLaunch(s.binding)
			unsettled := m.cleanupPending[s.binding.Session+"/"+s.binding.Launch] != 0
			for _, sibling := range m.retainedClosing {
				if physicalLaunch(sibling.binding) == launch {
					unsettled = true
					break
				}
			}
			if !unsettled {
				delete(m.fenced, launch)
			}
		}
	}
}

// Shutdown is terminal even when a core stream outage would merely suspend.
func (m *Manager) Shutdown(ctx context.Context) {
	m.pendingFence.Add(1)
	defer m.pendingFence.Add(-1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.available = false
	m.closeBatchLocked(ctx, m.snapshot(nil), ReasonShutdown)
	m.retryRetainedCleanup(ctx)
}

// Incarnation exists only as long as this manager's in-memory custody ledger.
func (m *Manager) Incarnation() string { return m.incarnation }
func (m *Manager) AbsenceHold(session string, absent, now time.Time) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if absent.IsZero() {
		return time.Time{}, false
	}
	if first := m.retainedAbsence[session]; !first.IsZero() && first.Before(absent) {
		absent = first
	}
	until, hold := m.custody.AbsenceHold(session, absent, now)
	if hold {
		m.retainedAbsence[session] = absent
	} else {
		delete(m.retainedAbsence, session)
	}
	return until, hold
}

// WithdrawConsent installs terminal custody intents before returning whether the
// runtime has proved physical cleanup. It requires the original exact login.
func (m *Manager) WithdrawConsent(ctx context.Context, a Actor, session, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.identityEpoch["session:"+session]++
	selected := m.snapshot(func(s *shell) bool {
		return s.actor == a && s.binding.Session == session && s.retained != nil && s.retained.custody.Consent == id
	})
	m.closeBatchLocked(ctx, selected, ReasonRevoked)
	for _, s := range m.retainedClosing {
		if s.actor == a && s.binding.Session == session && s.retained.custody.Consent == id {
			return false
		}
	}
	return true
}

func (m *Manager) info(s *shell) Info {
	info := Info{Privileged: s.signed.Grant.Privileged, Session: s.binding.Session, Name: s.name, OpenedAt: s.opened.Unix(), Handle: s.handle, Shell: s.signed.Grant.ShellGeneration, Runtime: s.binding.Runtime, RuntimeTargetID: s.binding.RuntimeTargetID, Folder: s.binding.Folder, Attached: s.attachment != nil && m.now().Sub(s.attachment.heartbeat) < HeartbeatLease}
	if s.retained != nil {
		info.Persistent = true
		info.Consent = s.retained.custody.Consent
		info.LifetimeUntil = s.retained.custody.LifetimeUntil
	}
	return info
}

func (m *Manager) closeReconciledCustody(ctx context.Context, b Binding, claims []CustodyClaim) {
	var public []*shell
	for _, claim := range claims {
		c := claim.Custody
		id := c.Digest()
		transaction := claim.Transaction
		if transaction == "" {
			transaction, _ = opaque()
		}
		if m.custody.BeginClose(id, transaction) != nil {
			continue
		}
		found := false
		for _, s := range m.shells {
			if s.retained != nil && s.retained.custody == c {
				s.retained.closing = transaction
				public = append(public, s)
				found = true
				break
			}
		}
		if !found && m.retainedClosing[id] == nil {
			m.retainedClosing[id] = &shell{actor: Actor{Kind: "human", Owner: c.Owner, Login: c.Login}, binding: b, retained: &retainedShell{custody: c, closing: transaction}}
		}
	}
	m.closeBatchLocked(ctx, public, ReasonCleanup)
	m.retryRetainedCleanup(ctx)
}

func (m *Manager) ConsentCleanupComplete(a Actor, session, id string) bool {
	_, complete := m.custody.ConsentCleanupState(a.Owner, a.Login, session, id)
	return a.Kind == "human" && complete
}

// RecordConsent logs only reviewed opaque identifiers and digests. Before an
// allocation exists, custody_kind explicitly identifies the consent pin digest;
// epoch zero denotes no access. A row never supplies affirmative authority.
func (m *Manager) RecordConsent(c protocol.ShellConsentV2, event Event, outcome string) {
	if event != ConsentAccepted && event != ConsentWithdrawn {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.audit == nil {
		return
	}
	m.audit.Record(m.consentAuditLocked(c, event, outcome))
}

// ConsentAuditSnapshot supplies only reviewed opaque context for a durable
// transactional audit record. It grants no access and emits no log event.
func (m *Manager) ConsentAuditSnapshot(c protocol.ShellConsentV2, event Event, outcome string) Audit {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.consentAuditLocked(c, event, outcome)
}
func (m *Manager) consentAuditLocked(c protocol.ShellConsentV2, event Event, outcome string) Audit {
	var target string
	custody, kind, epoch := c.PinsDigest(), "consent_pins", uint64(0)
	for _, s := range m.shells {
		if s.retained != nil && c.Allows(s.retained.custody) {
			target = s.binding.RuntimeTargetID
			custody, kind, epoch = s.retained.custody.Digest(), "allocation", s.signed.Grant.AccessEpoch
			break
		}
	}
	for _, s := range m.retainedClosing {
		if s.retained != nil && c.Allows(s.retained.custody) {
			target = s.binding.RuntimeTargetID
			custody, kind, epoch = s.retained.custody.Digest(), "allocation", s.signed.Grant.AccessEpoch
			break
		}
	}
	return Audit{Actor: c.Pins.Owner, Login: c.Pins.Login, Session: c.Pins.Session, Runtime: c.Pins.Runtime, RuntimeTargetID: target, RuntimeGeneration: c.Pins.RuntimeGeneration, Launch: c.Pins.Launch, Event: event, At: m.now(), Custody: custody, CustodyKind: kind, Epoch: epoch, Outcome: outcome}
}

func (m *Manager) releasePersistentConsent(ctx context.Context, s *shell, id string) {
	if r, ok := m.resolver.(PersistentConsentReleaser); ok {
		_ = m.external(context.WithoutCancel(ctx), func(call context.Context) error { r.ReleasePersistentConsent(call, s.actor, s.binding, id); return nil })
	}
}
