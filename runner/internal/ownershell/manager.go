package ownershell

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const MaxShells = 4
const MaxFrame = 16 << 10
const OutputQueue = 256 << 10
const InputRate = 16 << 10
const OutputRate = 64 << 10

// IdleTimeout and ReconnectGrace are the cluster defaults; see DefaultLimits.
const IdleTimeout = 30 * time.Minute
const HeartbeatLease = 45 * time.Second
const ReconnectGrace = 5 * time.Minute

// Actor is produced only by verified human authentication. Admin has no ownership exception.
type Actor struct{ Kind, Owner, Login string }

// Binding is loaded from persisted launch state and a current authenticated runtime connection.
// The P0b adapter owns admission, cleanup proofs and the runtime-root binding.
type Binding struct {
	RuntimeGeneration                                                      int64
	Root                                                                   protocol.WorkspaceRoot
	ManifestDigest                                                         string
	Session, Owner, Launch, Connection, Audience, Runtime, Folder, Consent string
	Live, Child, Cron, HostConsentValid                                    bool
	ServerCapabilities, RuntimeCapabilities                                protocol.WorkspaceCapabilities
}

func (b Binding) permits(a Actor) bool {
	return a.Kind == "human" && a.Owner != "" && a.Login != "" && b.Owner == a.Owner && b.Session != "" && b.Launch != "" && b.Connection != "" && b.Audience != "" && b.Live && !b.Child && !b.Cron && b.RuntimeGeneration > 0 && b.Root.Inode > 0 && b.ManifestDigest != "" && b.Audience == RuntimeAudience(b.Connection, b.RuntimeGeneration) &&
		b.ServerCapabilities.Supports(b.Runtime, true) && b.RuntimeCapabilities.Supports(b.Runtime, true) &&
		(b.Runtime == "sandbox" || b.Runtime == "cluster" || (b.Runtime == "host" && b.HostConsentValid && b.Consent != ""))
}

// Authority must check the exact login and read live policy, failing closed on any outage.
type Authority interface {
	Authorize(context.Context, Actor) (protocol.ShellPolicy, error)
}
type Resolver interface {
	Resolve(context.Context, string) (Binding, error)
}

// Supervisor is the byte-transport seam; LifecycleBridge binds it to P0b admission and proofs.
// Output is a dedicated bounded live channel;
// Close returns success only with containment empty, PTY closed and descendants reaped.
// Implementations MUST honor context deadlines and must not route bytes into agent output.
type Supervisor interface {
	Open(context.Context, Binding, SignedGrant, ed25519.PublicKey) (<-chan []byte, error)
	Renew(context.Context, SignedGrant) error
	Input(context.Context, SignedGrant, uint64, []byte) error
	Resize(context.Context, SignedGrant, uint64, uint16, uint16) error
	Close(context.Context, SignedGrant) error
}

// Audit has deliberately no bytes, command, environment, path, error text or arbitrary metadata.
type Audit struct {
	RuntimeGeneration int64         `json:"runtime_generation"`
	Actor             string        `json:"actor_id"`
	Login             string        `json:"login_id"`
	Session           string        `json:"session_id"`
	Runtime           string        `json:"runtime"`
	Connection        string        `json:"connection_id"`
	Launch            string        `json:"launch_generation"`
	Shell             string        `json:"shell_generation"`
	PolicyRevision    uint64        `json:"policy_revision"`
	Event             Event         `json:"event"`
	Reason            Reason        `json:"reason"`
	At                time.Time     `json:"at"`
	Duration          time.Duration `json:"duration_ns"`
}
type Event string

const (
	Opened     Event = "open"
	OpenFailed Event = "open_failure"
	Closed     Event = "close"
	// Detached records a browser attachment dropped for a stalled consumer;
	// the shell keeps running and can be reattached.
	Detached Event = "detach"
)

type Reason string

const (
	ReasonRequested   Reason = "requested"
	ReasonCleanup     Reason = "cleanup_uncertain"
	ReasonRevoked     Reason = "revoked"
	ReasonUnavailable Reason = "unavailable"
	ReasonSlow        Reason = "slow_consumer"
	ReasonRate        Reason = "rate_limit"
	ReasonIdle        Reason = "idle"
	ReasonDetached    Reason = "detached"
	ReasonRuntime     Reason = "runtime_closed"
	ReasonShutdown    Reason = "shutdown"
)

type AuditSink interface{ Record(Audit) }

// RuntimeDiagnostic exposes the terminal allowlisted cause independently of queued output.
// It is separate from Close, whose result remains containment cleanup proof.
type RuntimeDiagnostic interface{ Failure(SignedGrant) error }

type attachment struct {
	errors              chan string
	phase               chan string
	id                  string
	output              chan []byte
	heartbeat           time.Time
	inputSeq, resizeSeq uint64
}
type shell struct {
	waiting                                int
	operation                              chan struct{}
	busy                                   bool
	operationCancel                        context.CancelFunc
	done                                   chan struct{}
	runtimeInputSeq, runtimeResizeSeq      uint64
	handle                                 string
	actor                                  Actor
	binding                                Binding
	signed                                 SignedGrant
	opened, lastInput, lastGrant, detached time.Time
	attachment                             *attachment
	scrollback                             *scrollback
	// idleGrace and detachedGrace hold a shortened inactivity limit off a running shell.
	idleGrace, detachedGrace limitGrace
}
type budget struct {
	second                                   int64
	input, output                            int
	controlFrames, inputFrames, outputFrames int
	activityFrames                           int
}
type Info = protocol.OwnerShellInfo
type Attachment struct {
	Errors <-chan string
	Phase  <-chan string
	ID     string
	Output <-chan []byte
	// Replay, when requested, is the shell's kept scrollback at the moment this
	// attachment was installed: stream offsets [ReplayEnd-len(Replay), ReplayEnd).
	// Output then carries exactly the bytes from ReplayEnd on: no gap, no repeat.
	Replay    []byte
	ReplayEnd uint64
}

type revocationFence struct{ owner, login, session string }

type Manager struct {
	runtimeFenced      map[protocol.SupervisorLaunch]bool
	runtimeEpoch       map[protocol.SupervisorLaunch]uint64
	runtimeRevision    uint64
	identityEpoch      map[string]uint64
	cleanupPending     map[string]int
	pendingRevocations sync.Map
	pendingFence       atomic.Int64
	revision           atomic.Uint64
	mu                 sync.Mutex
	authority          Authority
	resolver           Resolver
	supervisor         Supervisor
	audit              AuditSink
	key                ed25519.PrivateKey
	now                func() time.Time
	policy             protocol.ShellPolicy
	available          bool
	shells             map[string]*shell
	budgets            map[string]*budget
	// fenced holds launches whose shell cleanup is unproven. It is keyed by the physical
	// launch (session, launch and runtime generation, runtime, connection) so a desktop
	// reconnect, whose configure step recovers the launch before admitting a shell,
	// starts clean instead of inheriting the old connection's fence. Entries are only
	// ever set for, or cleared by proof about, their own exact launch; one per
	// uncertain failure on a dead connection is the bounded cost of not guessing order.
	fenced map[protocol.SupervisorLaunch]bool
	// retryAfter spaces out opens after a refused start whose cleanup the runtime proved,
	// so a retry loop cannot hammer the desktop while a fixed problem can be retried.
	retryAfter map[protocol.SupervisorLaunch]time.Time
	// limits are the inactivity limits per runtime class; ended remembers which shells they ended.
	limits ShellLimits
	ended  map[string]endedShell
}

// OpenRetryBackoff is how long a launch refuses new shells after a start the runtime
// refused and then proved cleaned up.
const OpenRetryBackoff = 3 * time.Second

func physicalLaunch(b Binding) protocol.SupervisorLaunch {
	return SupervisorIdentity(b, SignedGrant{}).Launch
}

// launchFencedLocked reports whether b's physical launch refuses new shells. It has no
// side effects: a request cannot tell whether its binding is older or newer than
// another launch's, so it never clears state that belongs to a different launch. A
// stale request that resumes after a reconnect can only ever touch its own, dead launch.
func (m *Manager) launchFencedLocked(b Binding) bool {
	l := physicalLaunch(b)
	until, waiting := m.retryAfter[l]
	waiting = waiting && m.now().Before(until)
	return m.runtimeFenced[l] || m.fenced[l] || waiting || m.cleanupPending[b.Session+"/"+b.Launch] > 0
}

func New(authority Authority, resolver Resolver, supervisor Supervisor, audit AuditSink, now func() time.Time) (*Manager, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Manager{authority: authority, resolver: resolver, supervisor: supervisor, audit: audit, key: key, now: now, shells: map[string]*shell{}, budgets: map[string]*budget{}, fenced: map[protocol.SupervisorLaunch]bool{}, retryAfter: map[protocol.SupervisorLaunch]time.Time{}, cleanupPending: map[string]int{}, identityEpoch: map[string]uint64{}, runtimeFenced: map[protocol.SupervisorLaunch]bool{}, runtimeEpoch: map[protocol.SupervisorLaunch]uint64{}, limits: DefaultLimits(), ended: map[string]endedShell{}}, nil
}
func opaque() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (m *Manager) record(s *shell, event Event, reason Reason) {
	if m.audit == nil {
		return
	}
	m.audit.Record(Audit{Actor: s.actor.Owner, Login: s.actor.Login, Session: s.binding.Session, Runtime: s.binding.Runtime, RuntimeGeneration: s.binding.RuntimeGeneration, Connection: s.binding.Connection, Launch: s.binding.Launch, Shell: s.signed.Grant.ShellGeneration, PolicyRevision: s.signed.Grant.PolicyRevision, Event: event, Reason: reason, At: m.now(), Duration: m.now().Sub(s.opened)})
}

// SetPolicy is fed only by the authenticated, monitored core stream. Re-enabling never restores handles.
func (m *Manager) SetPolicy(ctx context.Context, p protocol.ShellPolicy) {
	if p.Version != 1 || !p.Enabled || p.Revision != m.revision.Load() {
		m.pendingFence.Add(1)
		defer m.pendingFence.Add(-1)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.Version != 1 || p.Revision == 0 || (m.policy.Revision != 0 && p.Revision < m.policy.Revision) || (m.available && p.Revision == m.policy.Revision && p.Enabled != m.policy.Enabled) {
		m.unavailableLocked(ctx)
		return
	}
	changed := !m.available || p.Revision != m.policy.Revision || p.Enabled != m.policy.Enabled
	m.policy = p
	m.revision.Store(p.Revision)
	m.available = true
	if changed {
		m.closeBatchLocked(ctx, m.snapshot(nil), ReasonRevoked)
	}
}
func (m *Manager) Unavailable(ctx context.Context) {
	m.pendingFence.Add(1)
	defer m.pendingFence.Add(-1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unavailableLocked(ctx)
}
func (m *Manager) unavailableLocked(ctx context.Context) {
	m.available = false
	m.closeBatchLocked(ctx, m.snapshot(nil), ReasonUnavailable)
}
func (m *Manager) Revoke(ctx context.Context, owner, login, session string) {
	fence := &revocationFence{owner, login, session}
	m.pendingRevocations.Store(fence, struct{}{})
	defer m.pendingRevocations.Delete(fence)
	m.mu.Lock()
	defer m.mu.Unlock()
	if owner != "" {
		m.identityEpoch["owner:"+owner]++
	}
	if login != "" {
		m.identityEpoch["login:"+login]++
	}
	if session != "" {
		m.identityEpoch["session:"+session]++
	}
	m.closeBatchLocked(ctx, m.snapshot(func(s *shell) bool {
		return (owner != "" && s.actor.Owner == owner) || (login != "" && s.actor.Login == login) || (session != "" && s.binding.Session == session)
	}), ReasonRevoked)
}
func (m *Manager) check(ctx context.Context, a Actor, b Binding) error {
	if !b.permits(a) {
		return Refuse(ReasonBinding)
	}
	if !m.available {
		return Refuse(ReasonPolicyUnavailable)
	}
	if !m.policy.Enabled {
		return Refuse(ReasonPolicyDisabled)
	}
	if m.pending(a, b) || m.authority == nil || m.resolver == nil || m.supervisor == nil {
		return ErrDenied
	}
	if m.launchFencedLocked(b) {
		return Refuse(ReasonRuntimeFenced)
	}
	epoch := m.identityStamp(a, b)
	var p protocol.ShellPolicy
	err := m.external(ctx, func(call context.Context) error { var e error; p, e = m.authority.Authorize(call, a); return e })
	if epoch != m.identityStamp(a, b) || m.pending(a, b) || err != nil || p.Version != 1 || !p.Enabled || p.Revision != m.policy.Revision || !m.available || !m.policy.Enabled {
		return Refuse(ReasonAuthority)
	}
	if m.launchFencedLocked(b) {
		return Refuse(ReasonRuntimeFenced)
	}
	return nil
}
func (m *Manager) grant(s *shell) SignedGrant {
	now := m.now().Unix()
	return SignGrant(m.key, protocol.ShellGrant{Version: 1, Audience: s.binding.Audience, OwnerID: s.actor.Owner, LoginID: s.actor.Login, SessionID: s.binding.Session, LaunchGeneration: s.binding.Launch, ConnectionID: s.binding.Connection, ShellGeneration: s.signed.Grant.ShellGeneration, Runtime: s.binding.Runtime, ConsentID: s.binding.Consent, PolicyRevision: m.policy.Revision, IssuedAt: now, ExpiresAt: now + protocol.ShellGrantSeconds})
}
func (m *Manager) Open(ctx context.Context, a Actor, session string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resolver == nil {
		return Info{}, ErrDenied
	}
	epoch := m.identityStamp(a, Binding{Session: session})
	revision := m.revision.Load()
	runtimeEpoch := m.runtimeRevision
	var b Binding
	err := m.external(ctx, func(call context.Context) error { var e error; b, e = m.resolver.Resolve(call, session); return e })
	s := &shell{actor: a, binding: b, opened: m.now(), scrollback: &scrollback{}}
	// Ownership precedes every detailed diagnostic, including successful
	// resolution followed by binding/policy/fencing refusal. Admin is no exception.
	if b.Owner == "" || b.Owner != a.Owner || b.Session != session || a.Kind != "human" || a.Login == "" {
		m.record(s, OpenFailed, ReasonUnavailable)
		return Info{}, ErrDenied
	}
	if err != nil {
		reason := RefusalReason(err)
		m.record(s, OpenFailed, reason)
		return Info{}, Refuse(reason)
	}
	if epoch != m.identityStamp(a, b) || m.runtimeEpoch[SupervisorIdentity(b, SignedGrant{}).Launch] > runtimeEpoch || revision != m.revision.Load() || b.Session != session {
		m.record(s, OpenFailed, ReasonUnavailable)
		return Info{}, ErrDenied
	}
	err = m.check(ctx, a, b)
	if err != nil || epoch != m.identityStamp(a, b) || m.runtimeEpoch[SupervisorIdentity(b, SignedGrant{}).Launch] > runtimeEpoch {
		reason := RefusalReason(err)
		m.record(s, OpenFailed, reason)
		return Info{}, Refuse(reason)
	}
	count := 0
	for _, other := range m.shells {
		if other.binding.Session == session {
			count++
		}
	}
	if count >= MaxShells {
		m.record(s, OpenFailed, ReasonRate)
		return Info{}, ErrLimit
	}
	handle, err := opaque()
	if err != nil {
		return Info{}, ErrDenied
	}
	gen, err := opaque()
	if err != nil {
		return Info{}, ErrDenied
	}
	s.done = make(chan struct{})
	s.handle = handle
	s.signed.Grant.ShellGeneration = gen
	s.signed = m.grant(s)
	s.lastInput = m.now()
	s.lastGrant = m.now()
	s.detached = m.now()
	m.shells[handle] = s // reserve the slot before releasing the map lock
	var out <-chan []byte
	signed := s.signed
	err = m.invoke(ctx, s, func(call context.Context) error {
		var e error
		out, e = m.supervisor.Open(call, b, signed, append(ed25519.PublicKey(nil), m.key[ed25519.SeedSize:]...))
		return e
	})
	if err != nil || out == nil {
		delete(m.shells, handle)
		launch := physicalLaunch(b)
		reason := RefusalReason(err)
		// The runtime answered with a definite refusal (not a timeout or a lost connection).
		answered := err != nil && reason != ReasonUnavailable
		if reason == ReasonUnavailable {
			reason = ReasonOpenRuntime
		}
		m.record(s, OpenFailed, reason)
		// Cleanup counts as pending, so no other open on this launch starts meanwhile.
		pending := b.Session + "/" + b.Launch
		m.cleanupPending[pending]++
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		closeErr := m.external(cleanup, func(call context.Context) error { return m.supervisor.Close(call, signed) })
		cancel()
		m.cleanupPending[pending]--
		if m.cleanupPending[pending] == 0 {
			delete(m.cleanupPending, pending)
		}
		// A refused start whose cleanup the runtime proved leaves nothing behind: a short
		// backoff lets the person retry once the cause is fixed (a synced clock, renewed
		// consent) without reconnecting the desktop. Anything uncertain fences this physical
		// launch until the desktop reconnects or the launch recovers.
		if answered && closeErr == nil {
			if until := m.now().Add(OpenRetryBackoff); until.After(m.retryAfter[launch]) {
				m.retryAfter[launch] = until
			}
		} else {
			m.fenced[launch] = true
		}
		if s.done != nil {
			close(s.done)
			s.done = nil
		}
		return Info{}, RefuseStart(reason, StartCause(err))
	}
	m.shells[handle] = s
	m.record(s, Opened, ReasonRequested)
	go m.drain(ctxWithoutCancel(ctx), handle, s.done, out)
	return Info{Handle: handle, Shell: gen, Runtime: b.Runtime, Folder: b.Folder}, nil
}

// A request context must not kill the long-lived output reader after an HTTP open returns.
func ctxWithoutCancel(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }
func (m *Manager) live(ctx context.Context, a Actor, handle string) (*shell, error) {
	s := m.shells[handle]
	if s == nil || s.actor != a {
		return nil, ErrDenied
	}
	var b Binding
	err := m.external(ctx, func(call context.Context) error {
		var e error
		b, e = m.resolver.Resolve(call, s.binding.Session)
		return e
	})
	if m.shells[handle] != s {
		return nil, ErrDenied
	}
	// A busy desktop verified nothing: refuse this operation and keep the shell. The grant
	// is renewed only after a successful check, so its expiry still bounds the wait.
	// Only a busy answer that still names this shell's own session and owner is inconclusive;
	// anything the resolver could check and deny was denied before it answered busy.
	if Busy(err) && b.Session == s.binding.Session && b.Owner == s.binding.Owner && b.Owner == s.actor.Owner && m.now().Unix() < s.signed.Grant.ExpiresAt {
		return nil, Refuse(ReasonRuntimeBusy)
	}
	// Runtime/owner/root/consent replacement fences stale handles. Folder is display-only.
	if err != nil || b.Session != s.binding.Session || b.Launch != s.binding.Launch || b.Connection != s.binding.Connection || b.Runtime != s.binding.Runtime || b.RuntimeGeneration != s.binding.RuntimeGeneration || b.Root != s.binding.Root || b.ManifestDigest != s.binding.ManifestDigest || b.Audience != s.binding.Audience || b.Consent != s.binding.Consent || m.check(ctx, a, b) != nil || m.now().Unix() >= s.signed.Grant.ExpiresAt {
		m.closeLocked(ctx, s, ReasonRevoked)
		return nil, ErrDenied
	}
	if m.shells[handle] != s {
		return nil, ErrDenied
	}
	return s, nil
}
func (m *Manager) Attach(ctx context.Context, a Actor, handle string, takeover bool) (Attachment, error) {
	return m.attach(ctx, a, handle, takeover, false)
}

// AttachWithReplay is Attach plus the shell's kept scrollback (see Attachment).
// Authorization is exactly Attach's: the same live owner and login checks.
func (m *Manager) AttachWithReplay(ctx context.Context, a Actor, handle string, takeover bool) (Attachment, error) {
	return m.attach(ctx, a, handle, takeover, true)
}

func (m *Manager) attach(ctx context.Context, a Actor, handle string, takeover, replay bool) (Attachment, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, release, err := m.lockHandle(ctx, a, handle)
	if err != nil {
		return Attachment{}, err
	}
	defer release()
	if !m.frame(a, handle, "control") {
		return Attachment{}, ErrLimit
	}
	s, err := m.live(ctx, a, handle)
	if err != nil {
		return Attachment{}, err
	}
	if s.attachment != nil && m.now().Sub(s.attachment.heartbeat) < HeartbeatLease && !takeover {
		return Attachment{}, ErrBusy
	}
	id, err := opaque()
	if err != nil {
		return Attachment{}, ErrDenied
	}
	if s.attachment != nil {
		close(s.attachment.output)
		if s.attachment.phase != nil {
			close(s.attachment.phase)
		}
		s.attachment = nil
	}
	s.signed = m.grant(s)
	signed := s.signed
	if err := m.invoke(ctx, s, func(call context.Context) error { return m.supervisor.Renew(call, signed) }); err != nil {
		m.publishFailure(s, err)
		m.closeLocked(ctx, s, ReasonUnavailable)
		return Attachment{}, RefuseStart(ReasonOpenRuntime, StartCause(err))
	}
	s.lastGrant = m.now()
	at := &attachment{errors: make(chan string, 1), phase: make(chan string, 4), id: id, output: make(chan []byte, OutputQueue/MaxFrame), heartbeat: m.now()}
	// The snapshot and the new attachment are taken under the same lock that
	// deliver holds while it records a chunk and hands it on, so every byte is
	// either in the snapshot or queued for this attachment, never both.
	result := Attachment{ID: id, Output: at.output, Phase: at.phase, Errors: at.errors}
	if replay {
		result.Replay, result.ReplayEnd = s.scrollback.snapshot()
	}
	s.attachment = at
	s.detached = time.Time{}
	return result, nil
}
func (m *Manager) attached(ctx context.Context, a Actor, handle, id string) (*shell, error) {
	s, err := m.live(ctx, a, handle)
	if err != nil {
		return nil, err
	}
	if s.attachment == nil || s.attachment.id != id || m.now().Sub(s.attachment.heartbeat) >= HeartbeatLease {
		return nil, ErrDenied
	}
	return s, nil
}
func (m *Manager) Heartbeat(ctx context.Context, a Actor, h, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, release, err := m.lockHandle(ctx, a, h)
	if err != nil {
		return err
	}
	defer release()
	if !m.frame(a, h, "control") {
		return ErrLimit
	}
	s, err := m.attached(ctx, a, h, id)
	if err != nil {
		return err
	}
	s.attachment.heartbeat = m.now()
	return nil
}
func (m *Manager) Detach(a Actor, h, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.shells[h]
	if s != nil && s.actor == a && s.attachment != nil && s.attachment.id == id {
		m.detachLocked(s, false)
	}
}
func (m *Manager) inputRate(session string, n int) bool {
	b := m.budgets[session]
	if b == nil {
		b = &budget{}
		m.budgets[session] = b
	}
	if b.second != m.now().Unix() {
		*b = budget{second: m.now().Unix()}
	}
	if b.input+n > InputRate {
		return false
	}
	b.input += n
	return true
}
func (m *Manager) Input(ctx context.Context, a Actor, h, id string, seq uint64, data []byte) error {
	return m.InputFrame(ctx, a, h, id, seq, data, true)
}

// InputFrame keeps terminal protocol replies usable without treating runtime output as human activity.
func (m *Manager) InputFrame(ctx context.Context, a Actor, h, id string, seq uint64, data []byte, human bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, release, err := m.lockHandle(ctx, a, h)
	if err != nil {
		return err
	}
	defer release()
	if !m.frame(a, h, "input") {
		return ErrLimit
	}
	s, err := m.attached(ctx, a, h, id)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > MaxFrame || seq != s.attachment.inputSeq+1 {
		return ErrDenied
	}
	if !m.inputRate(s.binding.Session, len(data)) {
		m.closeLocked(ctx, s, ReasonRate)
		return ErrLimit
	}
	s.attachment.inputSeq = seq
	signed, runtimeSeq := s.signed, s.runtimeInputSeq+1
	if err := m.invoke(ctx, s, func(call context.Context) error { return m.supervisor.Input(call, signed, runtimeSeq, data) }); err != nil {
		m.publishFailure(s, err)
		m.closeLocked(ctx, s, ReasonUnavailable)
		return RefuseStart(ReasonOpenRuntime, StartCause(err))
	}
	s.runtimeInputSeq++
	if human {
		s.lastInput = m.now()
	}
	return nil
}
func (m *Manager) Resize(ctx context.Context, a Actor, h, id string, seq uint64, cols, rows uint16) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, release, err := m.lockHandle(ctx, a, h)
	if err != nil {
		return err
	}
	defer release()
	if !m.frame(a, h, "control") {
		return ErrLimit
	}
	s, err := m.attached(ctx, a, h, id)
	if err != nil {
		return err
	}
	if seq != s.attachment.resizeSeq+1 || cols == 0 || rows == 0 {
		return ErrDenied
	}
	s.attachment.resizeSeq = seq
	signed, runtimeSeq := s.signed, s.runtimeResizeSeq+1
	if err := m.invoke(ctx, s, func(call context.Context) error { return m.supervisor.Resize(call, signed, runtimeSeq, cols, rows) }); err != nil {
		m.publishFailure(s, err)
		m.closeLocked(ctx, s, ReasonUnavailable)
		return RefuseStart(ReasonOpenRuntime, StartCause(err))
	}
	s.runtimeResizeSeq++
	return nil
}
func (m *Manager) Close(ctx context.Context, a Actor, h string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, release, err := m.lockHandle(ctx, a, h)
	if err != nil {
		return err
	}
	defer release()
	s, err := m.live(ctx, a, h)
	if err != nil {
		return err
	}
	m.closeLocked(ctx, s, ReasonRequested)
	return nil
}
func (m *Manager) closeLocked(ctx context.Context, s *shell, reason Reason) {
	// A terminal failure can invalidate consent while output is still queued.
	// Preserve its diagnostic before any live-data refusal retires the attachment;
	// do not relax validation or forward stale bytes to reach the output EOF.
	if diagnostic, ok := m.supervisor.(RuntimeDiagnostic); ok && m.shells[s.handle] == s && s.attachment != nil && StartCause(diagnostic.Failure(s.signed)) != "" {
		// A runtime refusal may retain persisted owner/session identity. Require
		// that identity and fresh human authority without admitting more work.
		var binding Binding
		_ = m.external(ctx, func(call context.Context) error {
			var err error
			binding, err = m.resolver.Resolve(call, s.binding.Session)
			return err
		})
		if m.shells[s.handle] == s && binding.Session == s.binding.Session && binding.Owner == s.actor.Owner {
			if err := m.check(ctx, s.actor, s.binding); err == nil && m.shells[s.handle] == s {
				m.publishFailure(s, diagnostic.Failure(s.signed))
			}
		}
	}
	m.closeBatchLocked(ctx, []*shell{s}, reason)
}
func (m *Manager) snapshot(matches func(*shell) bool) []*shell {
	out := make([]*shell, 0, len(m.shells))
	for _, s := range m.shells {
		if matches == nil || matches(s) {
			out = append(out, s)
		}
	}
	return out
}

// All handles are fenced atomically before any bounded parallel cleanup begins.
// The map lock is never held while waiting for a peer or for cleanup workers.
func (m *Manager) closeBatchLocked(ctx context.Context, shells []*shell, reason Reason) {
	tasks := make([]*shell, 0, len(shells))
	for _, s := range shells {
		if m.shells[s.handle] != s {
			continue
		}
		delete(m.shells, s.handle)
		// Scrollback dies with the shell: never kept past it, never persisted.
		s.scrollback = nil
		if s.done != nil {
			close(s.done)
			s.done = nil
		}
		if s.attachment != nil {
			close(s.attachment.output)
			if s.attachment.phase != nil {
				close(s.attachment.phase)
			}
			s.attachment = nil
		}
		if s.operationCancel != nil {
			s.operationCancel()
		}
		m.cleanupPending[s.binding.Session+"/"+s.binding.Launch]++
		tasks = append(tasks, s)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, s := range tasks {
		slots <- struct{}{}
		wg.Add(1)
		go func(s *shell) {
			defer wg.Done()
			defer func() { <-slots }()
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			var err error
			if m.supervisor != nil {
				err = m.supervisor.Close(cleanup, s.signed)
			} else {
				err = ErrDenied
			}
			cancel()
			m.mu.Lock()
			defer m.mu.Unlock()
			key := s.binding.Session + "/" + s.binding.Launch
			m.cleanupPending[key]--
			if m.cleanupPending[key] == 0 {
				delete(m.cleanupPending, key)
			}
			result := reason
			if err != nil {
				m.fenced[physicalLaunch(s.binding)] = true
				result = ReasonCleanup
			}
			m.record(s, Closed, result)
		}(s)
	}
	wg.Wait()
	m.mu.Lock()
}

// Tick is driven once a second. Output, heartbeat and resize never change lastInput.
func (m *Manager) Tick(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneEndedLocked()
	// Lapsed backoffs carry no state; dropping them by time alone is order-independent.
	for l, until := range m.retryAfter {
		if !m.now().Before(until) {
			delete(m.retryAfter, l)
		}
	}
	for id, b := range m.budgets {
		if m.now().Unix() > b.second+1 {
			live := false
			for _, s := range m.shells {
				if s.binding.Session == id {
					live = true
					break
				}
			}
			if !live {
				delete(m.budgets, id)
			}
		}
	}
	handles := m.snapshot(nil)
	m.mu.Unlock()
	var wg sync.WaitGroup
	jobs := make(chan *shell)
	for range min(8, len(handles)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				m.tickShell(ctx, s)
			}
		}()
	}
	for _, s := range handles {
		jobs <- s
	}
	close(jobs)
	wg.Wait()
	m.mu.Lock()
}
func (m *Manager) tickShell(parent context.Context, s *shell) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
	defer cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	h := s.handle
	if m.shells[h] != s {
		return
	}

	ctx, release, err := m.lockHandle(ctx, s.actor, h)
	if err != nil {
		return
	}
	defer release()
	if _, err := m.live(ctx, s.actor, h); err != nil {
		return
	}
	now := m.now()
	limits := m.limits.For(s.binding.Runtime)
	if limitReached(s.idleGrace, limits.Idle, s.lastInput, now) {
		m.recordEndedLocked(s, ReasonIdle, limits.Idle)
		m.closeLocked(ctx, s, ReasonIdle)
		return
	}
	if s.attachment != nil && now.Sub(s.attachment.heartbeat) >= HeartbeatLease {
		close(s.attachment.output)
		if s.attachment.phase != nil {
			close(s.attachment.phase)
		}
		s.attachment = nil
		s.detached = now
	}
	if s.attachment == nil && limitReached(s.detachedGrace, limits.Detached, s.detached, now) {
		m.recordEndedLocked(s, ReasonDetached, limits.Detached)
		m.closeLocked(ctx, s, ReasonDetached)
		return
	}
	if now.Sub(s.lastGrant) >= protocol.ShellRenewSeconds*time.Second {
		s.signed = m.grant(s)
		signed := s.signed
		if err := m.invoke(ctx, s, func(call context.Context) error { return m.supervisor.Renew(call, signed) }); err != nil {
			m.publishFailure(s, err)
			m.closeLocked(ctx, s, ReasonUnavailable)
			return
		}
		s.lastGrant = now
	}
}

func (m *Manager) drain(ctx context.Context, h string, done <-chan struct{}, out <-chan []byte) {
	for {
		var data []byte
		var ok bool
		select {
		case <-done:
			return
		case data, ok = <-out:
		}
		if !ok {
			break
		}
		if !m.deliver(ctx, h, done, data) {
			return
		}
		m.consumed(h, len(data))
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.shells[h]; s != nil {
		m.closeLocked(call, s, ReasonRuntime)
	}
}

// consumed returns a finished frame's bytes to the runtime's credit window.
// The supervisor call takes no manager lock and never blocks.
func (m *Manager) consumed(h string, n int) {
	m.mu.Lock()
	credit, ok := m.supervisor.(OutputCredit)
	s := m.shells[h]
	var signed SignedGrant
	if s != nil {
		signed = s.signed
	}
	m.mu.Unlock()
	if ok && s != nil {
		credit.Consumed(signed, n)
	}
}

// outputPoll is how often a waiting drain rechecks its budget or attachment.
// The map lock is never held while waiting.
const outputPoll = 10 * time.Millisecond

// pause waits one poll interval. It returns false once the shell is done.
func pause(done <-chan struct{}) bool {
	timer := time.NewTimer(outputPoll)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-done:
		return false
	}
}

// deliver moves one runtime frame to the current attachment. Over budget or a
// full attachment queue means wait: output reaches the browser slower than the
// program wrote it, and the runtime's own backpressure blocks the program. An
// attachment that takes nothing for OutputStall is detached (the shell keeps
// running). Every wait drops the map lock and ends when the shell closes, so it
// cannot hold revocation, expiry, disable or cleanup. It returns false once the
// shell is gone.
func (m *Manager) deliver(ctx context.Context, h string, done <-chan struct{}, data []byte) bool {
	// Budget first: it bounds the authority calls below, one per frame.
	for {
		m.mu.Lock()
		s := m.shells[h]
		if s == nil {
			m.mu.Unlock()
			return false
		}
		if len(data) > MaxFrame {
			call, cancel := context.WithTimeout(ctx, 3*time.Second)
			m.closeLocked(call, s, ReasonRate)
			m.mu.Unlock()
			cancel()
			return false
		}
		fits := m.outputBudget(s.binding.Session, len(data))
		m.mu.Unlock()
		if fits {
			break
		}
		if !pause(done) {
			return false
		}
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	m.mu.Lock()
	s := m.shells[h]
	if s == nil {
		m.mu.Unlock()
		cancel()
		return false
	}
	_, err := m.live(call, s.actor, h)
	if Busy(err) && m.shells[h] == s {
		// A busy desktop verified nothing (#267). Never resume a connected stream after a
		// silent hole: end the browser attachment, keep the shell, and keep the chunk in the
		// bounded scrollback, which a reattachment replays only after its own check. The
		// caller still returns the chunk's credit to the runtime.
		m.detachLocked(s, false)
		s.scrollback.write(append([]byte(nil), data...))
		m.mu.Unlock()
		cancel()
		return true
	}
	m.mu.Unlock()
	cancel()
	if err != nil {
		// live closed a stale handle; nothing is delivered from it.
		m.mu.Lock()
		gone := m.shells[h] != s
		m.mu.Unlock()
		return !gone
	}
	chunk := append([]byte(nil), data...)
	var stalled time.Time
	var waitingOn *attachment
	for {
		m.mu.Lock()
		if m.shells[h] != s {
			m.mu.Unlock()
			return false
		}
		at := s.attachment
		if at == nil {
			// Detached: kept in the scrollback for the next attachment, and the
			// runtime still gets its credit, so nobody's absence blocks the program.
			s.scrollback.write(chunk)
			m.mu.Unlock()
			return true
		}
		select {
		case at.output <- chunk:
			s.scrollback.write(chunk)
			m.mu.Unlock()
			return true
		default:
		}
		if at != waitingOn {
			waitingOn, stalled = at, m.now()
		} else if m.now().Sub(stalled) >= OutputStall {
			m.detachLocked(s, true)
			s.scrollback.write(chunk)
			m.mu.Unlock()
			return true
		}
		m.mu.Unlock()
		if !pause(done) {
			return false
		}
	}
}

// detachLocked drops the browser attachment and starts the reconnect grace.
// The shell, its grant and its runtime are untouched.
func (m *Manager) detachLocked(s *shell, stalled bool) {
	if s.attachment == nil {
		return
	}
	close(s.attachment.output)
	if s.attachment.phase != nil {
		close(s.attachment.phase)
	}
	s.attachment = nil
	s.detached = m.now()
	if stalled {
		m.record(s, Detached, ReasonSlow)
	}
}

// outputBudget takes one frame and n bytes of the session's output budget, or
// nothing when either is spent this second.
func (m *Manager) outputBudget(session string, n int) bool {
	b := m.budgets[session]
	if b == nil || b.second != m.now().Unix() {
		b = &budget{second: m.now().Unix()}
		m.budgets[session] = b
	}
	if b.outputFrames >= 128 || b.output+n > OutputRate {
		return false
	}
	b.outputFrames++
	b.output += n
	return true
}

// Forward revalidates queued output immediately before its only browser delivery.
func (m *Manager) Forward(ctx context.Context, a Actor, h, id string, data []byte, write func([]byte) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, release, err := m.lockHandle(ctx, a, h)
	if err != nil {
		return err
	}
	defer release()
	// Data frames already paid the output budget in drain, one Forward each;
	// counting them again would halve it. Phase notices still count here.
	if data == nil && !m.frame(a, h, "output") {
		return ErrLimit
	}
	s, err := m.attached(ctx, a, h, id)
	if err != nil {
		return err
	}
	if err = m.invoke(ctx, s, func(context.Context) error { return write(data) }); err != nil {
		// A browser that cannot take a write within its deadline is detached,
		// not killed: the shell keeps running for a reconnect.
		if m.shells[h] == s && s.attachment != nil && s.attachment.id == id {
			m.detachLocked(s, true)
		}
		return ErrDenied
	}
	return nil
}

// List returns only this live login's handles, so another tab can attach or explicitly take
// control without putting handles in shared storage or an initial-state broadcast.
func (m *Manager) List(ctx context.Context, a Actor, session string) ([]Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resolver == nil {
		return nil, ErrDenied
	}
	var b Binding
	err := m.external(ctx, func(call context.Context) error { var e error; b, e = m.resolver.Resolve(call, session); return e })
	if Busy(err) {
		return nil, Refuse(ReasonRuntimeBusy)
	}
	if err != nil || b.Session != session || m.check(ctx, a, b) != nil {
		return nil, ErrDenied
	}
	infos := []Info{}
	for h, s := range m.shells {
		if s.actor != a || s.binding.Session != session {
			continue
		}
		if _, err := m.live(ctx, a, h); err != nil {
			// An inventory that could not check a shell must not report it gone.
			if Busy(err) {
				return nil, err
			}
			continue
		}
		infos = append(infos, Info{Handle: h, Shell: s.signed.Grant.ShellGeneration, Runtime: s.binding.Runtime, Folder: s.binding.Folder, Attached: s.attachment != nil && m.now().Sub(s.attachment.heartbeat) < HeartbeatLease})
	}
	return infos, nil
}

// RuntimeAudience binds grants to the physical incarnation even when a desktop daemon
// retains its authenticated connection across runtime replacements. Both ends derive it
// from trusted connection/generation state, never from an incoming grant or browser label.
func RuntimeAudience(connection string, generation int64) string {
	if connection == "" || generation <= 0 {
		return ""
	}
	return "owner-shell:v1:" + base64.RawURLEncoding.EncodeToString([]byte(connection)) + ":" + strconv.FormatInt(generation, 10)
}

func (m *Manager) pending(a Actor, b Binding) bool {
	if m.pendingFence.Load() != 0 {
		return true
	}
	denied := false
	m.pendingRevocations.Range(func(key, _ any) bool {
		fence, ok := key.(*revocationFence)
		if !ok {
			return false
		}
		if (fence.owner != "" && fence.owner == a.Owner) || (fence.login != "" && fence.login == a.Login) || (fence.session != "" && fence.session == b.Session) {
			denied = true
			return false
		}
		return true
	})
	return denied
}

// external is called with mu held. No network, database, process operation or browser
// write may hold the map lock. Each call has its own deadline, independent of Tick's peers.
func (m *Manager) external(ctx context.Context, fn func(context.Context) error) error {
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	m.mu.Unlock()
	err := fn(call)
	m.mu.Lock()
	return err
}

// invoke gives each shell at most one in-flight runtime/write operation. Other sessions
// and revocation continue immediately; close cancels the operation before cleanup.
func (m *Manager) invoke(ctx context.Context, s *shell, fn func(context.Context) error) error {
	if m.shells[s.handle] != s || m.pending(s.actor, s.binding) {
		return ErrDenied
	}
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	owned := !s.busy
	if owned {
		s.busy = true
		s.operationCancel = cancel
		defer func() { s.busy = false; s.operationCancel = nil }()
	}
	err := m.external(call, fn)
	if m.shells[s.handle] != s || m.pending(s.actor, s.binding) {
		return ErrDenied
	}
	return err
}

// lockHandle serializes complete operations including authorization and sequences
// per handle. Queue waits never hold the global map lock; close cancels the holder.
func (m *Manager) lockHandle(ctx context.Context, a Actor, h string) (context.Context, func(), error) {
	s := m.shells[h]
	if s == nil || s.actor != a {
		return ctx, nil, ErrDenied
	}
	if s.operation == nil {
		s.operation = make(chan struct{}, 1)
	}
	if s.waiting >= 4 {
		return ctx, nil, ErrLimit
	}
	s.waiting++
	gate := s.operation
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	m.mu.Unlock()
	acquired := false
	select {
	case gate <- struct{}{}:
		acquired = true
	case <-call.Done():
	}
	m.mu.Lock()
	s.waiting--
	if !acquired {
		cancel()
		return ctx, nil, call.Err()
	}
	if m.shells[h] != s || m.pending(a, s.binding) {
		<-gate
		cancel()
		return ctx, nil, ErrDenied
	}
	s.busy = true
	s.operationCancel = cancel
	return call, func() { s.busy = false; s.operationCancel = nil; cancel(); <-gate }, nil
}

// Shared frame quotas bound tiny-frame, resize and heartbeat work before DB/core calls.
func (m *Manager) frame(a Actor, h, lane string) bool {
	s := m.shells[h]
	if s == nil || s.actor != a {
		return false
	}
	b := m.budgets[s.binding.Session]
	if b == nil || b.second != m.now().Unix() {
		b = &budget{second: m.now().Unix()}
		m.budgets[s.binding.Session] = b
	}
	switch lane {
	case "control":
		b.controlFrames++
		return b.controlFrames <= 32
	case "activity":
		b.activityFrames++
		return b.activityFrames <= 128
	case "input":
		b.inputFrames++
		return b.inputFrames <= 128
	default:
		b.outputFrames++
		return b.outputFrames <= 128
	}
}

// Activity is a separate browser keyboard/paste/IME control event. Runtime-generated
// terminal replies can never originate this path; activity has the same live authority gate.
func (m *Manager) Activity(ctx context.Context, a Actor, h, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, release, err := m.lockHandle(ctx, a, h)
	if err != nil {
		return err
	}
	defer release()
	if !m.frame(a, h, "activity") {
		return ErrLimit
	}
	s, err := m.attached(ctx, a, h, id)
	if err != nil {
		return err
	}
	s.lastInput = m.now()
	return nil
}

func (m *Manager) identityStamp(a Actor, b Binding) [3]uint64 {
	return [3]uint64{m.identityEpoch["owner:"+a.Owner], m.identityEpoch["login:"+a.Login], m.identityEpoch["session:"+b.Session]}
}

// NotifyPhase is fed only by the authenticated runtime connection. It publishes
// the syncing signal without PTY payloads; terminal lifecycle phases revoke.
func (m *Manager) NotifyPhase(ctx context.Context, session, phase string) {
	if phase != "syncing" && phase != "running" {
		m.Revoke(ctx, "", "", session)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.shells {
		if s.binding.Session == session && s.attachment != nil && s.attachment.phase != nil {
			select {
			case s.attachment.phase <- phase:
			default:
				select {
				case <-s.attachment.phase:
				default:
				}
				select {
				case s.attachment.phase <- phase:
				default:
				}
			}
		}
	}
}

// NotifyRuntimePhase scopes delayed lifecycle signals to their exact physical
// launch so an old incarnation cannot hide or revoke a newer session's shells.
func (m *Manager) NotifyRuntimePhase(ctx context.Context, l protocol.SupervisorLaunch, phase string) {
	if l.Validate() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	matches := func(s *shell) bool { return SupervisorIdentity(s.binding, s.signed).Launch == l }
	if phase != "syncing" && phase != "running" {
		m.runtimeFenced[l] = true
		m.runtimeRevision++
		m.runtimeEpoch[l] = m.runtimeRevision
		m.closeBatchLocked(ctx, m.snapshot(matches), ReasonRevoked)
		return
	}
	for _, s := range m.shells {
		if matches(s) && s.attachment != nil && s.attachment.phase != nil {
			select {
			case s.attachment.phase <- phase:
			default:
				select {
				case <-s.attachment.phase:
				default:
				}
				select {
				case s.attachment.phase <- phase:
				default:
				}
			}
		}
	}
}

// Called under the manager lock after the operation's authority check. The channel
// belongs to this exact attachment and holds no runtime-supplied prose or payload.
func (m *Manager) publishFailure(s *shell, err error) {
	if cause := StartCause(err); cause != "" && s.attachment != nil {
		select {
		case s.attachment.errors <- cause:
		default:
		}
	}
}
