package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

// busyDesktop answers a legacy host session's owner-shell RPCs. While busy > 0 it
// refuses that many attachments with the daemon's immediate "busy" reply (#267);
// busy < 0 refuses every attachment.
type busyDesktop struct {
	mu          sync.Mutex
	binding     ownershell.Binding
	launched    ownershell.Binding
	identity    shellruntime.Identity
	busy        int
	describeErr string
	// unregistered models a desktop that lost the launch (a restart): describe refuses it
	// as unbound until an attachment succeeds.
	unregistered bool
	// describeOK lets that many more describes succeed before describeErr applies (-1: unlimited).
	describeOK int
	opErr      map[string]string
	attaches   atomic.Int32
}

func (d *busyDesktop) serve(ctx context.Context, h *Hub, dc *DaemonConn) {
	for {
		select {
		case <-ctx.Done():
			return
		case raw := <-dc.shellControl:
			var req shellruntime.RPC
			if json.Unmarshal(raw, &req) != nil {
				continue
			}
			reply := shellruntime.RPC{Type: "owner_shell_result", Version: 1, SessionID: req.SessionID, RequestID: req.RequestID, Op: req.Op, OK: true}
			d.mu.Lock()
			switch req.Op {
			case "attach_workspace":
				d.attaches.Add(1)
				if d.busy != 0 {
					if d.busy > 0 {
						d.busy--
					}
					reply.OK, reply.ErrorCode = false, "busy"
				} else {
					d.unregistered = false
				}
			case "describe":
				if d.unregistered {
					reply.OK, reply.ErrorCode = false, string(ownershell.ReasonRuntimeBinding)
					break
				}
				if d.describeOK > 0 {
					d.describeOK--
				} else if d.describeErr != "" {
					reply.OK, reply.ErrorCode = false, d.describeErr
					break
				}
				reply.Binding, reply.Identity = d.binding, d.identity
			case "configure":
				d.launched = req.Binding
			case "close":
				identity := ownershell.SupervisorIdentity(d.launched, req.Grant)
				reply.Cleanup = &protocol.SupervisorCleanup{Launch: identity.Launch, ShellGeneration: identity.ShellGeneration, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
			}
			if code := d.opErr[req.Op]; code != "" {
				reply.OK, reply.ErrorCode, reply.Cleanup = false, code, nil
			}
			d.mu.Unlock()
			h.deliverShell(dc, reply)
		}
	}
}

func (d *busyDesktop) set(fn func(*busyDesktop)) { d.mu.Lock(); fn(d); d.mu.Unlock() }

type busyShellFixture struct {
	dc      *DaemonConn
	f       *topicFx
	desktop *busyDesktop
	manager *ownershell.Manager
	audit   chan ownershell.Audit
	actor   ownershell.Actor
	clock   atomic.Int64
}

func newBusyShellFixture(t *testing.T) *busyShellFixture {
	t.Helper()
	f := newTopicFx(t)
	ctx := t.Context()
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "busy-shell-desktop")
	dc.WorkspaceCapabilities = protocol.WorkspaceCapabilitiesFor("host", "linux")
	dc.shellControl = make(chan []byte, 64)
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET daemon_id=$2,runtime='daemon',status='idle' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	f.api.hub.SetSessionOwner(f.sess, dc.ID)
	f.api.hub.runtimeShellReady.Store(true)
	root := protocol.WorkspaceRoot{Device: 1, Inode: 9}
	var attached protocol.LocalWorkspaceControlResult
	if err := db.WithDesktopFence(ctx, f.pool, dc.ID, func(ctx context.Context) error {
		attached = f.api.hub.localWorkspaceControl(ctx, f.pool, dc, protocol.LocalWorkspaceControl{Operation: "legacy_attach", SessionID: f.sess, RequestID: newUUID(), Binding: protocol.WorkspaceRuntimeBinding{Root: root}})
		return nil
	}); err != nil || !attached.OK || attached.Manifest == nil {
		t.Fatalf("legacy attachment: %+v %v", attached, err)
	}
	caps := protocol.WorkspaceCapabilitiesFor("host", "linux")
	b := ownershell.Binding{Session: f.sess, Owner: topicOwner, Launch: attached.Manifest.LaunchGeneration, Connection: attached.Launch.ConnectionID, RuntimeGeneration: attached.Binding.Generation, Root: root, ManifestDigest: attached.Manifest.Digest, Runtime: "host", Folder: "repository", Live: true, ServerCapabilities: caps, RuntimeCapabilities: caps}
	b.Audience = ownershell.RuntimeAudience(b.Connection, b.RuntimeGeneration)
	desktop := &busyDesktop{opErr: map[string]string{}, binding: b, identity: shellruntime.Identity{Machine: "machine", Account: "person", UID: 1000, Shell: "/bin/sh", Cwd: "/project", Home: "/home/person"}}
	consent := protocol.HostShellConsent{ID: shellRandom(), OwnerID: topicOwner, LaunchGeneration: b.Launch, MachineID: "machine", Account: "person", UID: 1000, Shell: "/bin/sh", Cwd: "/project", ChallengeID: shellRandom()}
	binding := consentBinding(b, testSID, desktop.identity)
	if err := db.CreateShellChallenge(ctx, f.pool, binding, consent, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptShellChallenge(ctx, f.pool, binding, consent.ChallengeID); err != nil {
		t.Fatal(err)
	}
	x := &busyShellFixture{dc: dc, f: f, desktop: desktop, audit: make(chan ownershell.Audit, 64), actor: ownershell.Actor{Kind: "human", Owner: topicOwner, Login: testSID}}
	x.clock.Store(time.Now().UnixNano())
	rt := newRemoteShell(f.api)
	m, err := ownershell.New(shellTestAuthority{}, shellResolver{f.api, rt}, rt, shellAuditReceipt(x.audit), func() time.Time { return time.Unix(0, x.clock.Load()) })
	if err != nil {
		t.Fatal(err)
	}
	m.SetPolicy(ctx, protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	f.api.shellRuntime, f.api.shells = rt, m
	x.manager = m
	f.api.RegisterShellRoutes(f.mux)
	worker, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); desktop.serve(worker, f.api.hub, dc) }()
	t.Cleanup(func() { m.Unavailable(context.Background()); stop(); <-done })
	return x
}

func (x *busyShellFixture) request(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://shell.example/api/sessions/"+x.f.sess+"/"+path, strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+x.f.token(topicOwner, "human"))
	req.Header.Set("Origin", "http://shell.example")
	rec := httptest.NewRecorder()
	x.f.mux.ServeHTTP(rec, req)
	return rec
}

func (x *busyShellFixture) open(t *testing.T) protocol.OwnerShellInfo {
	t.Helper()
	rec := x.request(t, "POST", "shells")
	var info protocol.OwnerShellInfo
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &info) != nil {
		t.Fatalf("open: %d %s", rec.Code, rec.Body.String())
	}
	return info
}

func (x *busyShellFixture) closes() []ownershell.Audit {
	var out []ownershell.Audit
	for {
		select {
		case event := <-x.audit:
			if event.Event == ownershell.Closed {
				out = append(out, event)
			}
		default:
			return out
		}
	}
}

// #267: the owner-shell check retries through a desktop's "busy" attachment replies
// with a short backoff, well inside its deadline, and the shell stays up.
func TestOwnerShellCheckRetriesThroughBusyAttachment(t *testing.T) {
	x := newBusyShellFixture(t)
	opened := x.open(t)
	// Since #257 a check attaches only when the desktop lost the launch.
	x.desktop.set(func(d *busyDesktop) { d.busy = 3; d.unregistered = true })
	before := x.desktop.attaches.Load()
	start := time.Now()
	x.manager.Tick(t.Context())
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("busy attachment stalled the check for %s", elapsed)
	}
	if n := x.desktop.attaches.Load() - before; n != 4 {
		t.Fatalf("attachment attempts = %d, want three busy and one success", n)
	}
	if closes := x.closes(); len(closes) != 0 {
		t.Fatalf("busy desktop ended the shell: %+v", closes)
	}
	if _, err := x.manager.Attach(t.Context(), x.actor, opened.Handle, false); err != nil {
		t.Fatal("attach after busy", err)
	}
}

// A desktop that stays busy past the check's deadline proves nothing: the shell is
// neither fenced nor revoked, the inventory says "busy" instead of "gone", and the
// shell is usable once the desktop is free. Only the grant's own expiry ends it.
func TestBusyDesktopIsNeverAFenceOrRevocation(t *testing.T) {
	x := newBusyShellFixture(t)
	opened := x.open(t)
	x.desktop.set(func(d *busyDesktop) { d.busy = -1; d.unregistered = true })
	x.manager.Tick(t.Context())
	if closes := x.closes(); len(closes) != 0 {
		t.Fatalf("busy desktop ended the shell: %+v", closes)
	}
	rec := x.request(t, "GET", "shells")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"code":"runtime_busy"`) {
		t.Fatalf("busy inventory: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := x.manager.Attach(t.Context(), x.actor, opened.Handle, false); !ownershell.Busy(err) {
		t.Fatalf("attach while busy: %v", err)
	}
	x.desktop.set(func(d *busyDesktop) { d.busy = 0 })
	rec = x.request(t, "GET", "shells")
	var infos []protocol.OwnerShellInfo
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &infos) != nil || len(infos) != 1 || infos[0].Handle != opened.Handle {
		t.Fatalf("shell after busy cleared: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := x.manager.Attach(t.Context(), x.actor, opened.Handle, false); err != nil {
		t.Fatal("attach after busy cleared", err)
	}
	// The grant still bounds an outage: a desktop busy past it ends the shell.
	x.desktop.set(func(d *busyDesktop) { d.busy = -1; d.unregistered = true })
	x.clock.Add(int64(time.Duration(protocol.ShellGrantSeconds+1) * time.Second))
	x.manager.Tick(t.Context())
	if closes := x.closes(); len(closes) != 1 {
		t.Fatalf("expired grant survived a busy desktop: %+v", closes)
	}
}

// Only the server's own attachment backoff can say "busy". A runtime that answers a
// describe with that code is an ordinary refusal, which still fences.
func TestRuntimeReplyCannotClaimBusy(t *testing.T) {
	x := newBusyShellFixture(t)
	x.open(t)
	x.desktop.set(func(d *busyDesktop) { d.describeErr = string(ownershell.ReasonRuntimeBusy) })
	_, err := (shellResolver{x.f.api, x.f.api.shellRuntime}).Resolve(t.Context(), x.f.sess)
	if err == nil || ownershell.Busy(err) {
		t.Fatalf("runtime describe claimed busy: %v", err)
	}
	x.manager.Tick(t.Context())
	if closes := x.closes(); len(closes) != 1 || closes[0].Reason != ownershell.ReasonRevoked {
		t.Fatalf("refused describe did not fence: %+v", closes)
	}
}

func TestShellCapabilityReportsBusyAsRetryable(t *testing.T) {
	x := newBusyShellFixture(t)
	x.desktop.set(func(d *busyDesktop) { d.busy = -1; d.unregistered = true })
	rec := x.request(t, "GET", "shell-capability")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"code":"runtime_busy"`) || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("busy capability: %d %s", rec.Code, rec.Body.String())
	}
}

// The explorer gets the retryable "busy" code (its tree retries it), not "unavailable".
func TestExplorerReportsBusyAttachmentAsRetryable(t *testing.T) {
	x := newBusyShellFixture(t)
	x.f.api.RegisterWorkspaceFileRoutes(x.f.mux)
	x.desktop.set(func(d *busyDesktop) { d.busy = -1 })
	rec := x.request(t, "GET", "workspace?op=list")
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"busy"`) {
		t.Fatalf("busy explorer: %d %s", rec.Code, rec.Body.String())
	}
}

// Review finding 1: a persisted owner change is a definite denial, never hidden by busy.
func TestOwnerReplacementOnBusyDesktopClosesShell(t *testing.T) {
	x := newBusyShellFixture(t)
	x.open(t)
	if _, err := x.f.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, x.f.sess, topicOther); err != nil {
		t.Fatal(err)
	}
	x.desktop.set(func(d *busyDesktop) { d.busy = -1; d.unregistered = true })
	x.manager.Tick(t.Context())
	if closes := x.closes(); len(closes) != 1 || closes[0].Reason != ownershell.ReasonRevoked {
		t.Fatalf("replaced owner's shell survived a busy desktop: %+v", closes)
	}
}

// Review finding 2: no runtime reply can claim the server-only busy reason, on any operation.
func TestRuntimeRepliesCannotForgeBusyOnOpen(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*busyDesktop)
	}{
		{"configure", func(d *busyDesktop) { d.opErr["configure"] = string(ownershell.ReasonRuntimeBusy) }},
		{"open", func(d *busyDesktop) { d.opErr["open"] = string(ownershell.ReasonRuntimeBusy) }},
		// The open handler and the manager each describe once; the host open path describes again.
		{"host describe", func(d *busyDesktop) {
			d.describeOK, d.describeErr = 2, string(ownershell.ReasonRuntimeBusy)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newBusyShellFixture(t)
			x.desktop.set(c.set)
			rec := x.request(t, "POST", "shells")
			if rec.Code == 201 || rec.Code == 503 || strings.Contains(rec.Body.String(), "runtime_busy") {
				t.Fatalf("runtime %s forged busy: %d %s", c.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// #257 merge safety: a definite describe refusal is final. It is not retried by attaching,
// so a busy attachment can never overwrite it, and the shell closes.
func TestDefiniteDescribeRefusalIsNotMaskedByBusyAttachment(t *testing.T) {
	for _, reason := range []ownershell.Reason{ownershell.ReasonRuntimeIdentity, ownershell.ReasonRuntimeConsent, ownershell.ReasonRuntimeCleanup} {
		t.Run(string(reason), func(t *testing.T) {
			x := newBusyShellFixture(t)
			x.open(t)
			before := x.desktop.attaches.Load()
			x.desktop.set(func(d *busyDesktop) { d.busy = -1; d.describeErr = string(reason) })
			x.manager.Tick(t.Context())
			if closes := x.closes(); len(closes) != 1 || closes[0].Reason != ownershell.ReasonRevoked {
				t.Fatalf("definite refusal masked: %+v", closes)
			}
			if x.desktop.attaches.Load() != before {
				t.Fatal("a definite refusal was retried by attaching")
			}
		})
	}
}

// #257 merge safety: busy from a launch that is missing its runtime binding still carries
// freshly persisted identity, so the owner learns "busy" instead of an ordinary denial.
func TestBusyFallbackCarriesPersistedIdentity(t *testing.T) {
	x := newBusyShellFixture(t)
	if _, err := x.f.pool.Exec(t.Context(), `UPDATE session_workspaces SET runtime_binding=NULL WHERE session_id=$1`, x.f.sess); err != nil {
		t.Fatal(err)
	}
	x.desktop.set(func(d *busyDesktop) { d.busy = -1 })
	b, err := (shellResolver{x.f.api, x.f.api.shellRuntime}).Resolve(t.Context(), x.f.sess)
	if !ownershell.Busy(err) || b != (ownershell.Binding{Session: x.f.sess, Owner: topicOwner}) {
		t.Fatalf("busy fallback = %+v %v", b, err)
	}
	rec := x.request(t, "POST", "shells")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "runtime_busy") {
		t.Fatalf("owner open while busy: %d %s", rec.Code, rec.Body.String())
	}
	// A persisted denial still wins over busy.
	if _, err := x.f.pool.Exec(t.Context(), `UPDATE sessions SET status='stopped' WHERE id=$1`, x.f.sess); err != nil {
		t.Fatal(err)
	}
	if _, err := (shellResolver{x.f.api, x.f.api.shellRuntime}).Resolve(t.Context(), x.f.sess); err == nil || ownershell.Busy(err) {
		t.Fatalf("stopped session answered %v", err)
	}
}

// delayBusyRecheck slows the fresh persisted verification after a busy attachment, as a
// loaded database would, without changing any record. It honors cancellation.
func delayBusyRecheck(t *testing.T, delay time.Duration) {
	t.Helper()
	shellBusyRecheckHook.Store(func(ctx context.Context) {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
	})
	t.Cleanup(func() { var none func(context.Context); shellBusyRecheckHook.Store(none) })
}

// Re-review finding 4: busy attachment retries must leave the fresh verification time
// inside the caller's deadline, or capacity alone still revokes the shell.
func TestBusyAttachmentLeavesTimeToVerify(t *testing.T) {
	x := newBusyShellFixture(t)
	delayBusyRecheck(t, 250*time.Millisecond)
	x.desktop.set(func(d *busyDesktop) { d.busy = -1; d.unregistered = true })
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	b, err := (shellResolver{x.f.api, x.f.api.shellRuntime}).Resolve(ctx, x.f.sess)
	if !ownershell.Busy(err) || b != (ownershell.Binding{Session: x.f.sess, Owner: topicOwner}) {
		t.Fatalf("constrained busy resolution = %+v %v", b, err)
	}
}

func TestBusyAttachmentWithSlowVerificationKeepsTheShellOnTick(t *testing.T) {
	x := newBusyShellFixture(t)
	x.open(t)
	delayBusyRecheck(t, 250*time.Millisecond)
	x.desktop.set(func(d *busyDesktop) { d.busy = -1; d.unregistered = true })
	x.manager.Tick(t.Context())
	if closes := x.closes(); len(closes) != 0 {
		t.Fatalf("busy desktop with a slow verification ended the shell: %+v", closes)
	}
}

// The drain path: output arriving while the desktop is busy and verification is slow ends
// only the browser attachment; the same shell reattaches and streams fresh output.
func TestBusyAttachmentWithSlowVerificationDrainReattaches(t *testing.T) {
	x := newBusyShellFixture(t)
	opened := x.open(t)
	at, err := x.manager.Attach(t.Context(), x.actor, opened.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	delayBusyRecheck(t, 250*time.Millisecond)
	x.desktop.set(func(d *busyDesktop) { d.busy = -1; d.unregistered = true })
	output := func(data string) {
		x.f.api.hub.deliverShell(x.dc, shellruntime.RPC{Type: "owner_shell_output", Version: 1, SessionID: x.f.sess, Shell: opened.Shell, Data: []byte(data)})
	}
	output("unchecked")
	select {
	case data, ok := <-at.Output:
		if ok {
			t.Fatalf("unchecked output delivered: %q", data)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("busy output check left the attachment open")
	}
	if closes := x.closes(); len(closes) != 0 {
		t.Fatalf("busy output check ended the shell: %+v", closes)
	}
	var none func(context.Context)
	shellBusyRecheckHook.Store(none)
	x.desktop.set(func(d *busyDesktop) { d.busy = 0 })
	again, err := x.manager.Attach(t.Context(), x.actor, opened.Handle, false)
	if err != nil {
		t.Fatal("reattach after busy", err)
	}
	output("fresh")
	select {
	case data := <-again.Output:
		if string(data) != "fresh" {
			t.Fatalf("reattached output = %q", data)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no output after reattaching")
	}
}
