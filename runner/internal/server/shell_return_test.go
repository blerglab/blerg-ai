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

// legacyShellDesktop answers the owner-shell RPCs of one legacy (pre-workspace)
// host session the way the desktop daemon does. A busy desktop answers no
// attachment request at all: the daemon bounds concurrent legacy attachments
// across every session it runs and drops the excess without a reply.
type legacyShellDesktop struct {
	mu         sync.Mutex
	binding    ownershell.Binding
	launched   ownershell.Binding // the binding the server configured the shell with
	identity   shellruntime.Identity
	registered bool // the daemon's shell service holds the session's launch
	busy       atomic.Bool
	attaches   atomic.Int32
	describes  atomic.Int32
}

func (d *legacyShellDesktop) serve(ctx context.Context, h *Hub, dc *DaemonConn) {
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
				if d.busy.Load() {
					d.mu.Unlock()
					continue
				}
				d.registered = true
			case "describe":
				d.describes.Add(1)
				if !d.registered {
					reply.OK, reply.ErrorCode = false, string(ownershell.ReasonRuntimeBinding)
					break
				}
				reply.Binding, reply.Identity = d.binding, d.identity
			case "configure":
				d.launched = req.Binding
			case "close":
				identity := ownershell.SupervisorIdentity(d.launched, req.Grant)
				reply.Cleanup = &protocol.SupervisorCleanup{Launch: identity.Launch, ShellGeneration: identity.ShellGeneration, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
			}
			d.mu.Unlock()
			h.deliverShell(dc, reply)
		}
	}
}

type legacyShellFixture struct {
	f       *topicFx
	desktop *legacyShellDesktop
	manager *ownershell.Manager
	audit   chan ownershell.Audit
	actor   ownershell.Actor
}

func newLegacyShellFixture(t *testing.T) *legacyShellFixture {
	t.Helper()
	f := newTopicFx(t)
	ctx := t.Context()
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "legacy-shell-desktop")
	dc.WorkspaceCapabilities = protocol.WorkspaceCapabilitiesFor("host", "linux")
	dc.shellControl = make(chan []byte, 64)
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET daemon_id=$2,runtime='daemon',status='idle' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	f.api.hub.SetSessionOwner(f.sess, dc.ID)
	f.api.hub.runtimeShellReady.Store(true)
	// The daemon's first legacy attachment records the session's manifest and runtime binding.
	root := protocol.WorkspaceRoot{Device: 1, Inode: 7}
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
	desktop := &legacyShellDesktop{binding: b, identity: shellruntime.Identity{Machine: "machine", Account: "person", UID: 1000, Shell: "/bin/sh", Cwd: "/project", Home: "/home/person"}, registered: true}
	// Host shells need the owner's accepted consent for this exact launch and identity.
	consent := protocol.HostShellConsent{ID: shellRandom(), OwnerID: topicOwner, LaunchGeneration: b.Launch, MachineID: "machine", Account: "person", UID: 1000, Shell: "/bin/sh", Cwd: "/project", ChallengeID: shellRandom()}
	binding := consentBinding(b, testSID, desktop.identity)
	if err := db.CreateShellChallenge(ctx, f.pool, binding, consent, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptShellChallenge(ctx, f.pool, binding, consent.ChallengeID); err != nil {
		t.Fatal(err)
	}
	rt := newRemoteShell(f.api)
	audit := make(chan ownershell.Audit, 64)
	m, err := ownershell.New(shellTestAuthority{}, shellResolver{f.api, rt}, rt, shellAuditReceipt(audit), nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetPolicy(ctx, protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	f.api.shellRuntime, f.api.shells = rt, m
	f.api.RegisterShellRoutes(f.mux)
	worker, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); desktop.serve(worker, f.api.hub, dc) }()
	t.Cleanup(func() { m.Unavailable(context.Background()); stop(); <-done })
	return &legacyShellFixture{f: f, desktop: desktop, manager: m, audit: audit, actor: ownershell.Actor{Kind: "human", Owner: topicOwner, Login: testSID}}
}

func (x *legacyShellFixture) request(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://shell.example/api/sessions/"+x.f.sess+"/"+path, strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+x.f.token(topicOwner, "human"))
	req.Header.Set("Origin", "http://shell.example")
	rec := httptest.NewRecorder()
	x.f.mux.ServeHTTP(rec, req)
	return rec
}

func (x *legacyShellFixture) list(t *testing.T) []protocol.OwnerShellInfo {
	t.Helper()
	rec := x.request(t, "GET", "shells")
	var infos []protocol.OwnerShellInfo
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &infos) != nil {
		t.Fatalf("inventory: %d %s", rec.Code, rec.Body.String())
	}
	return infos
}

func (x *legacyShellFixture) closes() []ownershell.Audit {
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

// Card #257: leaving a session and coming back ended its shell with reason
// "revoked". Leaving detaches the browser; opening another session on the same
// desktop keeps that desktop's legacy attachment slots busy. The shell's binding
// never changed, so the shell must still be listed and reattach on return.
func TestLegacyHostShellSurvivesLeavingAndReturning(t *testing.T) {
	x := newLegacyShellFixture(t)
	ctx := t.Context()
	rec := x.request(t, "POST", "shells")
	if rec.Code != 201 {
		t.Fatalf("open: %d %s", rec.Code, rec.Body.String())
	}
	var opened protocol.OwnerShellInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &opened); err != nil {
		t.Fatal(err)
	}
	at, err := x.manager.Attach(ctx, x.actor, opened.Handle, false)
	if err != nil {
		t.Fatal("first attach", err)
	}
	// Leave: the session view unmounts and closes its socket.
	x.manager.Detach(x.actor, opened.Handle, at.ID)
	x.desktop.busy.Store(true)
	before := x.desktop.attaches.Load()
	for range 2 {
		x.manager.Tick(ctx)
	}
	x.desktop.busy.Store(false)
	// Return: the remounted view inventories and reattaches.
	infos := x.list(t)
	if len(infos) != 1 || infos[0].Handle != opened.Handle || infos[0].Shell != opened.Shell {
		t.Fatalf("shell lost after leaving and returning: %+v closes=%+v", infos, x.closes())
	}
	if _, err := x.manager.Attach(ctx, x.actor, opened.Handle, false); err != nil {
		t.Fatal("reattach after return", err)
	}
	if closes := x.closes(); len(closes) != 0 {
		t.Fatalf("shell closed: %+v", closes)
	}
	// An attached, bound legacy workspace is verified, not attached again, on every check.
	if n := x.desktop.attaches.Load() - before; n != 0 {
		t.Fatalf("liveness checks re-ran the legacy attachment %d times", n)
	}
}

// A genuine runtime change still fences a live shell at the next check.
func TestLegacyHostShellStillFencesRealChanges(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*ownershell.Binding)
	}{
		{"replaced project directory", func(b *ownershell.Binding) { b.Root.Inode++ }},
		{"another desktop connection", func(b *ownershell.Binding) {
			b.Connection = "replacement-connection"
			b.Audience = ownershell.RuntimeAudience(b.Connection, b.RuntimeGeneration)
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			x := newLegacyShellFixture(t)
			ctx := t.Context()
			rec := x.request(t, "POST", "shells")
			if rec.Code != 201 {
				t.Fatalf("open: %d %s", rec.Code, rec.Body.String())
			}
			x.desktop.mu.Lock()
			change.apply(&x.desktop.binding)
			x.desktop.mu.Unlock()
			x.manager.Tick(ctx)
			closes := x.closes()
			if len(closes) != 1 || closes[0].Reason != ownershell.ReasonRevoked {
				t.Fatalf("changed binding did not fence: %+v", closes)
			}
		})
	}
}

// After a desktop restart the daemon has not registered the legacy shell yet;
// resolution attaches it once and then binds normally.
func TestLegacyHostShellResolutionAttachesWhenTheDesktopLostIt(t *testing.T) {
	x := newLegacyShellFixture(t)
	x.desktop.mu.Lock()
	x.desktop.registered = false
	x.desktop.mu.Unlock()
	rec := x.request(t, "POST", "shells")
	if rec.Code != 201 {
		t.Fatalf("open after desktop restart: %d %s", rec.Code, rec.Body.String())
	}
	if x.desktop.attaches.Load() == 0 {
		t.Fatal("resolution never re-attached the unregistered legacy workspace")
	}
}
