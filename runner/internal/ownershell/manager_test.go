package ownershell

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAuthority struct {
	policy protocol.ShellPolicy
	dead   bool
}

func (f *fakeAuthority) Authorize(context.Context, Actor) (protocol.ShellPolicy, error) {
	if f.dead {
		return protocol.ShellPolicy{}, ErrDenied
	}
	return f.policy, nil
}

type fakeResolver struct{ binding Binding }

func (f *fakeResolver) Resolve(context.Context, string) (Binding, error) { return f.binding, nil }

type fakeSupervisor struct {
	mu                     sync.Mutex
	closedEvents           chan struct{}
	outputs                []chan []byte
	closed, inputs, renews int
	failClose              bool
	failRenew              bool
}

func (f *fakeSupervisor) Open(context.Context, Binding, SignedGrant, ed25519.PublicKey) (<-chan []byte, error) {
	ch := make(chan []byte, 32)
	f.outputs = append(f.outputs, ch)
	return ch, nil
}
func (f *fakeSupervisor) Renew(context.Context, SignedGrant) error {
	f.renews++
	if f.failRenew {
		return ErrDenied
	}
	return nil
}
func (f *fakeSupervisor) Input(context.Context, SignedGrant, uint64, []byte) error {
	f.inputs++
	return nil
}
func (f *fakeSupervisor) Resize(context.Context, SignedGrant, uint64, uint16, uint16) error {
	return nil
}
func (f *fakeSupervisor) Close(context.Context, SignedGrant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	if f.closedEvents != nil {
		select {
		case f.closedEvents <- struct{}{}:
		default:
		}
	}
	if f.failClose {
		return errors.New("not empty")
	}
	return nil
}

type fakeAudit struct{ events []Audit }

func (f *fakeAudit) Record(a Audit) { f.events = append(f.events, a) }

type fixture struct {
	m     *Manager
	a     *fakeAuthority
	r     *fakeResolver
	s     *fakeSupervisor
	audit *fakeAudit
	now   time.Time
	actor Actor
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{a: &fakeAuthority{policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}}, s: &fakeSupervisor{}, audit: &fakeAudit{}, now: time.Unix(1000, 0), actor: Actor{"human", "owner", "login"}}
	f.r = &fakeResolver{Binding{RuntimeGeneration: 1, Root: protocol.WorkspaceRoot{Device: 1, Inode: 1}, ManifestDigest: "digest", Session: "session", Owner: "owner", Launch: "launch", Connection: "connection", Audience: RuntimeAudience("connection", 1), Runtime: "sandbox", Folder: "project", Live: true, ServerCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1}, RuntimeCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1}}}
	var err error
	f.m, err = New(f.a, f.r, f.s, f.audit, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.m.SetPolicy(t.Context(), f.a.policy)
	t.Cleanup(func() { f.m.Unavailable(context.Background()) })
	return f
}
func (f *fixture) open(t *testing.T) (Info, Attachment) {
	t.Helper()
	i, e := f.m.Open(t.Context(), f.actor, "session")
	if e != nil {
		t.Fatal(e)
	}
	a, e := f.m.Attach(t.Context(), f.actor, i.Handle, false)
	if e != nil {
		t.Fatal(e)
	}
	return i, a
}
func TestHumanMatrix(t *testing.T) {
	for _, name := range []string{"owner", "admin-other", "agent", "runtime", "child", "cron", "unowned", "no-login", "stopped", "paused", "ended", "old-server", "old-runtime", "host-no-consent", "host-consent"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			allowed := name == "owner" || name == "host-consent"
			switch name {
			case "admin-other":
				f.actor.Owner = "admin"
			case "agent", "runtime":
				f.actor.Kind = name
			case "child":
				f.r.binding.Child = true
			case "cron":
				f.r.binding.Cron = true
			case "no-login":
				f.actor.Login = ""
			case "unowned":
				f.r.binding.Owner = ""
			case "stopped", "paused", "ended":
				f.r.binding.Live = false
			case "old-server":
				f.r.binding.ServerCapabilities = protocol.WorkspaceCapabilities{}
			case "old-runtime":
				f.r.binding.RuntimeCapabilities = protocol.WorkspaceCapabilities{}
			case "host-no-consent":
				f.r.binding.Runtime = "host"
			case "host-consent":
				f.r.binding.Runtime = "host"
				f.r.binding.HostConsentValid = true
				f.r.binding.Consent = "consent"
			}
			_, err := f.m.Open(t.Context(), f.actor, "session")
			if (err == nil) != allowed {
				t.Fatalf("allowed=%v err=%v", allowed, err)
			}
		})
	}
}
func TestFourShellsAndSharedBudget(t *testing.T) {
	f := setup(t)
	var infos []Info
	var atts []Attachment
	for range MaxShells {
		i, a := f.open(t)
		infos = append(infos, i)
		atts = append(atts, a)
	}
	if _, e := f.m.Open(t.Context(), f.actor, "session"); !errors.Is(e, ErrLimit) {
		t.Fatal(e)
	}
	if e := f.m.Input(t.Context(), f.actor, infos[0].Handle, atts[0].ID, 1, make([]byte, MaxFrame)); e != nil {
		t.Fatal(e)
	}
	if e := f.m.Input(t.Context(), f.actor, infos[1].Handle, atts[1].ID, 1, []byte("x")); !errors.Is(e, ErrLimit) {
		t.Fatal("budget multiplied per shell", e)
	}
}
func TestTakeoverReconnectAndSequences(t *testing.T) {
	f := setup(t)
	i, a := f.open(t)
	if _, e := f.m.Attach(t.Context(), f.actor, i.Handle, false); !errors.Is(e, ErrBusy) {
		t.Fatal(e)
	}
	b, e := f.m.Attach(t.Context(), f.actor, i.Handle, true)
	if e != nil {
		t.Fatal(e)
	}
	if f.m.Input(t.Context(), f.actor, i.Handle, a.ID, 1, []byte("x")) == nil {
		t.Fatal("old controller survived")
	}
	if f.m.Input(t.Context(), f.actor, i.Handle, b.ID, 1, []byte("x")) != nil {
		t.Fatal("new controller refused")
	}
	if f.m.Input(t.Context(), f.actor, i.Handle, b.ID, 1, []byte("x")) == nil {
		t.Fatal("replayed input accepted")
	}
	f.m.Detach(f.actor, i.Handle, b.ID)
	f.a.dead = true
	if _, e = f.m.Attach(t.Context(), f.actor, i.Handle, false); e == nil {
		t.Fatal("reconnect accepted dead login")
	}
}
func TestEveryRevocationClosesStaleSocket(t *testing.T) {
	for _, name := range []string{"disable", "revision", "logout", "account", "owner", "launch", "connection", "runtime-generation", "root", "digest", "consent", "end", "discard", "policy-down", "authority-down"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			i, a := f.open(t)
			switch name {
			case "disable":
				f.m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 2, Enabled: false})
			case "revision":
				f.m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 2, Enabled: true})
			case "logout":
				f.m.Revoke(t.Context(), "", "login", "")
			case "account":
				f.m.Revoke(t.Context(), "owner", "", "")
			case "owner":
				f.r.binding.Owner = "other"
			case "launch":
				f.r.binding.Launch = "new"
			case "connection":
				f.r.binding.Connection = "new"
			case "runtime-generation":
				f.r.binding.RuntimeGeneration++
			case "root":
				f.r.binding.Root.Inode++
			case "digest":
				f.r.binding.ManifestDigest = "replaced"
			case "consent":
				f.r.binding.Consent = "new"
			case "end", "discard":
				f.r.binding.Live = false
			case "policy-down":
				f.m.Unavailable(t.Context())
			case "authority-down":
				f.a.dead = true
			}
			if f.m.Input(t.Context(), f.actor, i.Handle, a.ID, 1, []byte("secret-input")) == nil {
				t.Fatal("stale input accepted")
			}
			delivered := false
			if f.m.Forward(t.Context(), f.actor, i.Handle, a.ID, []byte("secret-output"), func([]byte) error { delivered = true; return nil }) == nil || delivered {
				t.Fatal("stale output delivered")
			}
			if f.s.closed == 0 {
				t.Fatal("runtime kill not sent")
			}
		})
	}
}
func TestOfflineExpiryAndCleanupFence(t *testing.T) {
	f := setup(t)
	i, a := f.open(t)
	f.now = f.now.Add(30 * time.Second)
	if f.m.Input(t.Context(), f.actor, i.Handle, a.ID, 1, []byte("x")) == nil {
		t.Fatal("expired lease accepted")
	}
	f = setup(t)
	i, _ = f.open(t)
	f.s.failClose = true
	_ = f.m.Close(t.Context(), f.actor, i.Handle)
	if _, e := f.m.Open(t.Context(), f.actor, "session"); e == nil {
		t.Fatal("uncertain cleanup reopened")
	}
}
func TestRenewalIdleAndAttachmentGrace(t *testing.T) {
	f := setup(t)
	i, a := f.open(t)
	f.now = f.now.Add(10 * time.Second)
	f.m.Tick(t.Context())
	if f.s.renews < 2 {
		t.Fatal("no renewal")
	}
	for range 180 {
		f.now = f.now.Add(10 * time.Second)
		_ = f.m.Heartbeat(t.Context(), f.actor, i.Handle, a.ID)
		f.m.Tick(t.Context())
	}
	if len(f.m.shells) != 0 {
		t.Fatal("heartbeat reset human idle")
	}
	f = setup(t)
	i, a = f.open(t)
	f.m.Detach(f.actor, i.Handle, a.ID)
	for range 31 {
		f.now = f.now.Add(10 * time.Second)
		f.m.Tick(t.Context())
	}
	if len(f.m.shells) != 0 {
		t.Fatal("detached shell outlived grace")
	}
}
func TestAuditCannotRecordPayloads(t *testing.T) {
	f := setup(t)
	i, a := f.open(t)
	_ = f.m.Input(t.Context(), f.actor, i.Handle, a.ID, 1, []byte("never-record-this"))
	_ = f.m.Close(t.Context(), f.actor, i.Handle)
	b, _ := json.Marshal(f.audit.events)
	if strings.Contains(string(b), "never-record-this") {
		t.Fatal("payload recorded")
	}
	allowed := map[string]bool{"Actor": true, "Login": true, "Session": true, "Runtime": true, "RuntimeGeneration": true, "Connection": true, "Launch": true, "Shell": true, "PolicyRevision": true, "Event": true, "Reason": true, "At": true, "Duration": true}
	typ := reflect.TypeFor[Audit]()
	for j := 0; j < typ.NumField(); j++ {
		n := typ.Field(j).Name
		if !allowed[n] {
			t.Fatal("unreviewed audit field", n)
		}
		if strings.Contains(n, "Payload") || strings.Contains(n, "Data") || strings.Contains(n, "Command") || strings.Contains(n, "Environment") {
			t.Fatal("audit payload field", n)
		}
	}
	if len(f.audit.events) != 2 || f.audit.events[0].Event != Opened || f.audit.events[1].Event != Closed {
		t.Fatal(f.audit.events)
	}
}
func TestSignedGrantRejectsAllBindingSpoofs(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	g := protocol.ShellGrant{Version: 1, Audience: "runtime", OwnerID: "owner", LoginID: "login", SessionID: "session", LaunchGeneration: "launch", ConnectionID: "connection", ShellGeneration: "shell", Runtime: "host", ConsentID: "consent", PolicyRevision: 1, IssuedAt: 1000, ExpiresAt: 1030}
	if VerifyGrant(pub, SignGrant(key, g), g, 1, time.Unix(1001, 0)) != nil {
		t.Fatal("valid grant")
	}
	for _, field := range []string{"Audience", "OwnerID", "LoginID", "SessionID", "LaunchGeneration", "ConnectionID", "ShellGeneration", "Runtime", "ConsentID"} {
		t.Run(field, func(t *testing.T) {
			bad := g
			reflect.ValueOf(&bad).Elem().FieldByName(field).SetString("forged")
			if VerifyGrant(pub, SignGrant(key, bad), g, 1, time.Unix(1001, 0)) == nil {
				t.Fatal("binding spoof accepted")
			}
		})
	}
	for _, n := range []int64{999, 1030, 2000} {
		if VerifyGrant(pub, SignGrant(key, g), g, 1, time.Unix(n, 0)) == nil {
			t.Fatal("invalid time accepted")
		}
	}
	bad := g
	bad.ExpiresAt = 1031
	if VerifyGrant(pub, SignGrant(key, bad), g, 1, time.Unix(1001, 0)) == nil {
		t.Fatal("oversized lease")
	}
	signed := SignGrant(key, g)
	signed.Grant.OwnerID = "tampered"
	if VerifyGrant(pub, signed, g, 1, time.Unix(1001, 0)) == nil {
		t.Fatal("tampering accepted")
	}
	if VerifyGrant(pub, SignGrant(key, g), g, 2, time.Unix(1001, 0)) == nil {
		t.Fatal("stale policy accepted")
	}
}

// The fake's Close notification precedes the manager committing its cleanup
// result/audit. Join that commit instead of racing the last Opened audit entry.
func waitClosedAudit(t *testing.T, f *fixture) Audit {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		f.m.mu.Lock()
		last := f.audit.events[len(f.audit.events)-1]
		f.m.mu.Unlock()
		if last.Event == Closed {
			return last
		}
		select {
		case <-deadline.C:
			t.Fatal("cleanup audit was not committed")
		case <-t.Context().Done():
			t.Fatal("cleanup canceled")
		default:
			runtime.Gosched()
		}
	}
}

func TestOutputFloodClosesSlowConsumerWithoutRecording(t *testing.T) {
	f := setup(t)
	f.s.closedEvents = make(chan struct{}, 8)
	i, a := f.open(t)
	for range OutputQueue/MaxFrame + 1 {
		f.s.outputs[0] <- []byte("private-output")
	}
	select {
	case <-f.s.closedEvents:
	case <-time.After(3 * time.Second):
		t.Fatal("slow consumer was not closed")
	}
	last := waitClosedAudit(t, f)
	if last.Reason != ReasonSlow {
		t.Fatal(last.Reason)
	}
	delivered := false
	_ = f.m.Forward(t.Context(), f.actor, i.Handle, a.ID, []byte("queued"), func([]byte) error { delivered = true; return nil })
	if delivered {
		t.Fatal("buffered output escaped revocation")
	}
}
func TestOutputRateIsSharedAcrossAllFourShells(t *testing.T) {
	f := setup(t)
	f.s.closedEvents = make(chan struct{}, 8)
	for range MaxShells {
		f.open(t)
	}
	for _, ch := range f.s.outputs {
		ch <- make([]byte, MaxFrame)
	}
	// Drain must consume the first four frames before the fifth; order across shells is irrelevant.
	// Five frames exceed the shared budget regardless of the worker interleaving.
	f.s.outputs[0] <- make([]byte, MaxFrame)
	select {
	case <-f.s.closedEvents:
	case <-time.After(3 * time.Second):
		t.Fatal("output rate multiplied per pane")
	}
	last := waitClosedAudit(t, f)
	if last.Reason != ReasonRate {
		t.Fatal(last.Reason)
	}
}
func TestListOnlyOwnLoginHandles(t *testing.T) {
	f := setup(t)
	i, _ := f.open(t)
	infos, err := f.m.List(t.Context(), f.actor, "session")
	if err != nil || len(infos) != 1 || infos[0].Handle != i.Handle || !infos[0].Attached {
		t.Fatal(infos, err)
	}
	otherLogin := f.actor
	otherLogin.Login = "other-login"
	infos, err = f.m.List(t.Context(), otherLogin, "session")
	if err != nil || len(infos) != 0 {
		t.Fatal("cross-login handle leak", infos, err)
	}
	other := f.actor
	other.Owner = "admin"
	if _, err = f.m.List(t.Context(), other, "session"); err == nil {
		t.Fatal("admin listed another owner's handles")
	}
}
func TestReenableNeverRestoresHandles(t *testing.T) {
	f := setup(t)
	i, a := f.open(t)
	f.m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 2, Enabled: false})
	f.a.policy = protocol.ShellPolicy{Version: 1, Revision: 3, Enabled: true}
	f.m.SetPolicy(t.Context(), f.a.policy)
	if f.m.Input(t.Context(), f.actor, i.Handle, a.ID, 1, []byte("stale")) == nil {
		t.Fatal("re-enable restored a handle")
	}
	if _, err := f.m.Open(t.Context(), f.actor, "session"); err != nil {
		t.Fatal("fresh open refused", err)
	}
}

type blockedAuthority struct{ entered, release chan struct{} }

func (b blockedAuthority) Authorize(ctx context.Context, _ Actor) (protocol.ShellPolicy, error) {
	close(b.entered)
	select {
	case <-b.release:
		return protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}, nil
	case <-ctx.Done():
		return protocol.ShellPolicy{}, ctx.Err()
	}
}
func TestRevocationFencesBlockedAuthorization(t *testing.T) {
	f := setup(t)
	i, a := f.open(t)
	gate := blockedAuthority{entered: make(chan struct{}), release: make(chan struct{})}
	f.m.mu.Lock()
	f.m.authority = gate
	f.m.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate.release) }) }
	defer release()
	inputDone := make(chan error, 1)
	go func() { inputDone <- f.m.Input(ctx, f.actor, i.Handle, a.ID, 1, []byte("must-not-dispatch")) }()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("authorization did not enter")
	}
	revoked := make(chan struct{})
	go func() {
		defer close(revoked)
		f.m.SetPolicy(ctx, protocol.ShellPolicy{Version: 1, Revision: 2, Enabled: false})
	}()
	<-revoked // control must finish while authorization is still blocked

	release()
	if err := <-inputDone; err == nil {
		t.Fatal("blocked authorization dispatched after revocation")
	}
	<-revoked
	if f.s.inputs != 0 {
		t.Fatal("runtime received stale human input")
	}
}
func TestIdentityRevocationFencesOnlyItsScope(t *testing.T) {
	for _, matching := range []bool{false, true} {
		t.Run(map[bool]string{false: "other-owner", true: "matching-login"}[matching], func(t *testing.T) {
			f := setup(t)
			i, a := f.open(t)
			gate := blockedAuthority{entered: make(chan struct{}), release: make(chan struct{})}
			f.m.mu.Lock()
			f.m.authority = gate
			f.m.mu.Unlock()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			var once sync.Once
			release := func() { once.Do(func() { close(gate.release) }) }
			defer release()
			inputDone := make(chan error, 1)
			go func() { inputDone <- f.m.Input(ctx, f.actor, i.Handle, a.ID, 1, []byte("human-input")) }()
			select {
			case <-gate.entered:
			case <-ctx.Done():
				t.Fatal("authorization did not enter")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				if matching {
					f.m.Revoke(ctx, "", f.actor.Login, "")
				} else {
					f.m.Revoke(ctx, "other-owner", "", "")
				}
			}()
			<-done // scoped revocation no longer waits for the authority call

			release()
			err := <-inputDone
			<-done
			if (err != nil) != matching {
				t.Fatalf("matching=%v error=%v", matching, err)
			}
			if !matching && f.s.inputs != 1 {
				t.Fatal("unrelated owner lost valid input")
			}
			if matching && f.s.inputs != 0 {
				t.Fatal("revoked login dispatched stale input")
			}
		})
	}
}

func TestFailedReplacementAttachmentCleansAndFences(t *testing.T) {
	for _, takeover := range []bool{true, false} {
		t.Run(map[bool]string{true: "takeover", false: "expired"}[takeover], func(t *testing.T) {
			f := setup(t)
			i, old := f.open(t)
			if !takeover {
				f.now = f.now.Add(HeartbeatLease)
			}
			f.s.failRenew = true
			f.s.failClose = true
			if _, err := f.m.Attach(t.Context(), f.actor, i.Handle, takeover); err == nil {
				t.Fatal("renewal accepted")
			}
			if _, ok := <-old.Output; ok {
				t.Fatal("old attachment remains open")
			}
			if f.s.closed != 1 {
				t.Fatalf("cleanup calls=%d", f.s.closed)
			}
			if _, err := f.m.Open(t.Context(), f.actor, "session"); err == nil {
				t.Fatal("uncertain launch reopened")
			}
			if len(f.audit.events) < 2 || f.audit.events[1].Reason != ReasonCleanup {
				t.Fatal("missing cleanup audit")
			}
		})
	}
}

func TestTerminalRepliesCannotExtendHumanIdle(t *testing.T) {
	f := setup(t)
	i, a := f.open(t)
	for minute := 0; minute < 30; minute++ {
		f.now = f.now.Add(time.Minute)
		// Simulate continuous lease maintenance and automatic terminal-query replies.
		f.m.mu.Lock()
		s := f.m.shells[i.Handle]
		s.signed = f.m.grant(s)
		s.attachment.heartbeat = f.now
		f.m.mu.Unlock()
		if err := f.m.InputFrame(t.Context(), f.actor, i.Handle, a.ID, uint64(minute+1), []byte("\x1b[0n"), false); err != nil {
			t.Fatal(err)
		}
	}
	f.m.Tick(t.Context())
	if f.s.closed != 1 {
		t.Fatal("terminal replies kept shell alive")
	}
}

type stalledRenew struct {
	Supervisor
	entered  chan struct{}
	canceled chan error
}

func (s stalledRenew) Renew(ctx context.Context, _ SignedGrant) error {
	close(s.entered)
	<-ctx.Done()
	if s.canceled != nil {
		s.canceled <- ctx.Err()
	}
	return ctx.Err()
}
func TestStalledAttachmentCannotBlockDisable(t *testing.T) {
	f := setup(t)
	i, _ := f.open(t)
	stalled := stalledRenew{f.s, make(chan struct{}), make(chan error, 1)}
	f.m.supervisor = stalled
	done := make(chan error, 1)
	go func() { _, err := f.m.Attach(t.Context(), f.actor, i.Handle, true); done <- err }()
	<-stalled.entered
	// Disable cancels the stalled operation, rather than waiting for the browser request.
	f.m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 2, Enabled: false})
	if err := <-done; err == nil {
		t.Fatal("stalled attachment accepted")
	}
	if err := <-stalled.canceled; !errors.Is(err, context.Canceled) {
		t.Fatalf("disable waited for deadline: %v", err)
	}
	if f.s.closed != 1 {
		t.Fatal("disable did not clean up")
	}
}
func TestControlFramesBoundedBeforeAuthority(t *testing.T) {
	f := setup(t)
	i, a := f.open(t)
	for seq := uint64(1); seq <= 32; seq++ {
		_ = f.m.Resize(t.Context(), f.actor, i.Handle, a.ID, seq, 80, 24)
	}
	// Exhausted quota must refuse before contacting the now-blocked authority.
	f.m.authority = blockedAuthority{entered: make(chan struct{}), release: make(chan struct{})}
	if err := f.m.Heartbeat(t.Context(), f.actor, i.Handle, a.ID); !errors.Is(err, ErrLimit) {
		t.Fatalf("quota: %v", err)
	}
}

func TestRevocationFencesOpenAuthorizationEvenAfterDeliveryReturns(t *testing.T) {
	f := setup(t)
	gate := blockedAuthority{entered: make(chan struct{}), release: make(chan struct{})}
	f.m.authority = gate
	result := make(chan error, 1)
	go func() { _, err := f.m.Open(t.Context(), f.actor, "session"); result <- err }()
	<-gate.entered
	f.m.Revoke(t.Context(), "", f.actor.Login, "")
	close(gate.release)
	if err := <-result; err == nil {
		t.Fatal("in-flight authorization opened after completed login revocation")
	}
	if len(f.s.outputs) != 0 {
		t.Fatal("revoked open reached runtime")
	}
}

type resolveFunc func(context.Context, string) (Binding, error)

func (fn resolveFunc) Resolve(ctx context.Context, id string) (Binding, error) { return fn(ctx, id) }
func TestStalledOwnerDoesNotBlockAnotherOwner(t *testing.T) {
	f := setup(t)
	i, _ := f.open(t)
	other := f.r.binding
	other.Session = "other-session"
	other.Owner = "other-owner"
	f.m.resolver = resolveFunc(func(_ context.Context, id string) (Binding, error) {
		if id == other.Session {
			return other, nil
		}
		return f.r.binding, nil
	})
	actor := Actor{Kind: "human", Owner: other.Owner, Login: "other-login"}
	second, err := f.m.Open(t.Context(), actor, other.Session)
	if err != nil {
		t.Fatal(err)
	}
	at, err := f.m.Attach(t.Context(), actor, second.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	stalled := stalledRenew{f.s, make(chan struct{}), make(chan error, 1)}
	f.m.supervisor = stalled
	done := make(chan error, 1)
	go func() { _, err := f.m.Attach(t.Context(), f.actor, i.Handle, true); done <- err }()
	<-stalled.entered
	if err := f.m.Input(t.Context(), actor, second.Handle, at.ID, 1, []byte("other-owner-input")); err != nil {
		t.Fatal(err)
	}
	f.m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 2, Enabled: false})
	if err := <-done; err == nil {
		t.Fatal("stalled attachment survived disable")
	}
	if err := <-stalled.canceled; !errors.Is(err, context.Canceled) {
		t.Fatalf("other owner waited for failed peer: %v", err)
	}
}

type orderedInput struct {
	Supervisor
	first, release chan struct{}
	sequences      chan uint64
}

func (o orderedInput) Input(ctx context.Context, _ SignedGrant, seq uint64, _ []byte) error {
	o.sequences <- seq
	if seq == 1 {
		close(o.first)
		select {
		case <-o.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func TestConcurrentInputPreservesRuntimeSequence(t *testing.T) {
	f := setup(t)
	i, a := f.open(t)
	peer := orderedInput{f.s, make(chan struct{}), make(chan struct{}), make(chan uint64, 2)}
	f.m.supervisor = peer
	result := make(chan error, 2)
	go func() { result <- f.m.Input(t.Context(), f.actor, i.Handle, a.ID, 1, []byte("first")) }()
	<-peer.first
	go func() { result <- f.m.Input(t.Context(), f.actor, i.Handle, a.ID, 2, []byte("second")) }()
	deadline, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for {
		f.m.mu.Lock()
		waiting := f.m.shells[i.Handle].waiting
		f.m.mu.Unlock()
		if waiting > 0 {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatal("second operation did not queue")
		default:
			runtime.Gosched()
		}
	}
	close(peer.release)
	for range 2 {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	if one, two := <-peer.sequences, <-peer.sequences; one != 1 || two != 2 {
		t.Fatalf("runtime sequence=%d,%d", one, two)
	}
}

func TestRuntimePhaseCannotRevokeAnotherIncarnation(t *testing.T) {
	f := setup(t)
	i, a := f.open(t)
	launch := SupervisorIdentity(f.r.binding, SignedGrant{}).Launch
	old := launch
	old.RuntimeGeneration++
	f.m.NotifyRuntimePhase(t.Context(), old, "paused")
	if err := f.m.Heartbeat(t.Context(), f.actor, i.Handle, a.ID); err != nil {
		t.Fatal("stale phase revoked current incarnation")
	}
	f.m.NotifyRuntimePhase(t.Context(), launch, "syncing")
	select {
	case phase := <-a.Phase:
		if phase != "syncing" {
			t.Fatal("wrong phase")
		}
	default:
		t.Fatal("sync header not delivered")
	}
	f.m.NotifyRuntimePhase(t.Context(), launch, "paused")
	if f.s.closed != 1 {
		t.Fatal("current pause did not close supervisor")
	}
}

type firstBlockedResolver struct {
	binding          Binding
	entered, release chan struct{}
	once             sync.Once
}

func (r *firstBlockedResolver) Resolve(ctx context.Context, _ string) (Binding, error) {
	wait := false
	r.once.Do(func() { wait = true; close(r.entered) })
	if wait {
		select {
		case <-r.release:
		case <-ctx.Done():
			return Binding{}, ctx.Err()
		}
	}
	return r.binding, nil
}
func TestRevocationDuringInitialResolutionNeverDispatches(t *testing.T) {
	for _, reason := range []string{"owner", "login", "session", "runtime_phase", "runtime_recovered"} {
		t.Run(reason, func(t *testing.T) {
			f := setup(t)
			r := &firstBlockedResolver{binding: f.r.binding, entered: make(chan struct{}), release: make(chan struct{})}
			f.m.resolver = r
			done := make(chan error, 1)
			go func() { _, err := f.m.Open(t.Context(), f.actor, "session"); done <- err }()
			<-r.entered
			switch reason {
			case "owner":
				f.m.Revoke(t.Context(), f.actor.Owner, "", "")
			case "login":
				f.m.Revoke(t.Context(), "", f.actor.Login, "")
			case "session":
				f.m.Revoke(t.Context(), "", "", "session")
			case "runtime_phase":
				launch := SupervisorIdentity(r.binding, SignedGrant{}).Launch
				f.m.NotifyRuntimePhase(t.Context(), launch, "stopping")
				f.m.NotifyRuntimePhase(t.Context(), launch, "running")
			case "runtime_recovered":
				launch := SupervisorIdentity(r.binding, SignedGrant{}).Launch
				f.m.NotifyRuntimePhase(t.Context(), launch, "paused")
				f.m.NotifyRuntimePhase(t.Context(), launch, "running")
				proof := protocol.SupervisorCleanup{Launch: launch, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
				if err := f.m.Recovered(t.Context(), f.actor, "session", proof); err != nil {
					t.Fatal("verified fresh recovery failed", err)
				}
			}
			close(r.release)
			if err := <-done; err == nil {
				t.Error("stale initial resolution admitted after completed revocation")
			}
			if len(f.s.outputs) != 0 {
				t.Error("revoked pending open reached runtime")
			}
		})
	}
}

func TestRecoveryCannotClearConcurrentRuntimeRevocation(t *testing.T) {
	f := setup(t)
	l := SupervisorIdentity(f.r.binding, SignedGrant{}).Launch
	proof := protocol.SupervisorCleanup{Launch: l, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
	gate := blockedAuthority{entered: make(chan struct{}), release: make(chan struct{})}
	f.m.authority = gate
	done := make(chan error, 1)
	go func() { done <- f.m.Recovered(t.Context(), f.actor, "session", proof) }()
	<-gate.entered
	f.m.NotifyRuntimePhase(t.Context(), l, "ended")
	close(gate.release)
	if err := <-done; err == nil {
		t.Fatal("old recovery result cleared newer terminal phase")
	}
	if _, err := f.m.Open(t.Context(), f.actor, "session"); err == nil {
		t.Fatal("runtime revocation fence disappeared")
	}
}

type sessionStalledRenew struct {
	Supervisor
	entered, progressed chan struct{}
	canceled            chan error
}

func (s sessionStalledRenew) Renew(ctx context.Context, g SignedGrant) error {
	if g.Grant.SessionID == "session" {
		close(s.entered)
		<-ctx.Done()
		s.canceled <- ctx.Err()
		return ctx.Err()
	}
	err := s.Supervisor.Renew(ctx, g)
	close(s.progressed)
	return err
}
func TestFloodAndStalledRenewCannotBlockOtherOwnerOrDisable(t *testing.T) {
	f := setup(t)
	first, at := f.open(t)
	other := f.r.binding
	other.Session = "other-session"
	other.Owner = "other-owner"
	f.m.resolver = resolveFunc(func(_ context.Context, id string) (Binding, error) {
		if id == other.Session {
			return other, nil
		}
		return f.r.binding, nil
	})
	actor := Actor{Kind: "human", Owner: other.Owner, Login: "other-login"}
	second, err := f.m.Open(t.Context(), actor, other.Session)
	if err != nil {
		t.Fatal(err)
	}
	otherAt, err := f.m.Attach(t.Context(), actor, second.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(11 * time.Second)
	peer := sessionStalledRenew{f.s, make(chan struct{}), make(chan struct{}), make(chan error, 1)}
	f.m.supervisor = peer
	tickDone := make(chan struct{})
	go func() { f.m.Tick(t.Context()); close(tickDone) }()
	<-peer.entered
	select {
	case <-peer.progressed:
	case <-time.After(time.Second):
		t.Fatal("one stalled owner prevented another owner's renewal")
	}
	var flood sync.WaitGroup
	for n := 1; n <= 128; n++ {
		flood.Add(1)
		go func(seq uint64) {
			defer flood.Done()
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			if seq%2 == 0 {
				_ = f.m.Resize(ctx, f.actor, first.Handle, at.ID, seq, 80, 24)
			} else {
				_ = f.m.Input(ctx, f.actor, first.Handle, at.ID, seq, []byte("x"))
			}
		}(uint64(n))
	}
	if err = f.m.Input(t.Context(), actor, second.Handle, otherAt.ID, 1, []byte("progress")); err != nil {
		t.Fatal("other owner input stalled", err)
	}
	started := time.Now()
	f.m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 2, Enabled: false})
	if time.Since(started) > time.Second {
		t.Error("frame flood held administrator disable")
	}
	<-tickDone
	flood.Wait()
	if err := <-peer.canceled; !errors.Is(err, context.Canceled) {
		t.Fatal("disable failed to cancel stalled renewal", err)
	}
	if _, err = f.m.Open(t.Context(), actor, other.Session); err == nil {
		t.Fatal("disable admitted a new handle")
	}
}
