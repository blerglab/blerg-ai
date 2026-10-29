package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
)

type shellTestAuthority struct{}

func (shellTestAuthority) Authorize(context.Context, ownershell.Actor) (protocol.ShellPolicy, error) {
	return protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}, nil
}

type shellTestRuntime struct {
	mu      sync.Mutex
	binding ownershell.Binding
	out     chan []byte
	input   chan []byte
}

func (s *shellTestRuntime) Resolve(context.Context, string) (ownershell.Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.binding, nil
}
func (s *shellTestRuntime) Open(context.Context, ownershell.Binding, ownershell.SignedGrant, ed25519.PublicKey) (<-chan []byte, error) {
	return s.out, nil
}
func (s *shellTestRuntime) Renew(context.Context, ownershell.SignedGrant) error { return nil }
func (s *shellTestRuntime) Input(_ context.Context, _ ownershell.SignedGrant, _ uint64, b []byte) error {
	s.input <- append([]byte(nil), b...)
	return nil
}
func (s *shellTestRuntime) Resize(context.Context, ownershell.SignedGrant, uint64, uint16, uint16) error {
	return nil
}
func (s *shellTestRuntime) Close(context.Context, ownershell.SignedGrant) error { return nil }
func shellTestBinding() ownershell.Binding {
	return ownershell.Binding{RuntimeGeneration: 1, Root: protocol.WorkspaceRoot{Device: 1, Inode: 1}, ManifestDigest: "digest", Session: "session", Owner: "owner", Launch: "launch", Connection: "connection", Audience: ownershell.RuntimeAudience("connection", 1), Runtime: "sandbox", Folder: "project", Live: true, ServerCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true, Sandbox: true, Cluster: true}, RuntimeCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true, Sandbox: true, Cluster: true}}
}
func TestShellChannelOriginIsolationAndStaleSocket(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := NewAPI(NewHub(), nil, "", nil, "")
	a.coreAuth = newTestCoreAuthClient(t, pub, "shell-key")
	token := func(owner, kind string) string {
		return mintTestToken(t, key, "shell-key", identity.Claims{Kind: kind, Sub: owner, Aud: coreAuthAudience, Sid: "login", Caps: []string{coreAuthBrowserCap, "account.manage"}, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	}
	rt := &shellTestRuntime{binding: shellTestBinding(), out: make(chan []byte, 8), input: make(chan []byte, 8)}
	m, err := ownershell.New(shellTestAuthority{}, rt, rt, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.shells = m
	m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	t.Cleanup(func() { m.Unavailable(context.Background()) })
	mux := http.NewServeMux()
	a.RegisterShellRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	open := func(tok, origin, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", server.URL+"/api/sessions/session/shells", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	for _, origin := range []string{"", "https://foreign.example"} {
		if r := open(token("owner", "human"), origin, ""); r.Code != 403 {
			t.Fatal("origin", r.Code)
		}
	}
	for _, who := range []struct{ owner, kind string }{{"other", "human"}, {"owner", "agent"}, {"owner", "service"}} {
		if r := open(token(who.owner, who.kind), server.URL, ""); r.Code != 403 {
			t.Fatal("identity", who, r.Code)
		}
	}
	if r := open(token("owner", "human"), server.URL, `{"command":"sh"}`); r.Code != 400 {
		t.Fatal("command body accepted", r.Code)
	}
	r := open(token("owner", "human"), server.URL, "")
	if r.Code != 201 {
		t.Fatal(r.Code, r.Body.String())
	}
	var info ownershell.Info
	if json.Unmarshal(r.Body.Bytes(), &info) != nil {
		t.Fatal("info")
	}
	events := make(chan []byte, 8)
	a.hub.Subscribe("session", "test-watch", events)
	dial := websocket.Dialer{Subprotocols: []string{"bearer", token("owner", "human")}}
	conn, res, err := dial.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/api/shells/"+info.Handle+"/socket", http.Header{"Origin": []string{server.URL}})
	if res != nil {
		defer res.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]any{"type": "input", "seq": 1, "data": []byte("private-input")}); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-rt.input:
		if string(input) != "private-input" {
			t.Fatal("wrong input")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("input not dispatched")
	}
	rt.out <- []byte("private-output")
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	kind, b, err := conn.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || string(b) != "private-output" {
		t.Fatal("output", kind, string(b), err)
	}
	select {
	case <-events:
		t.Fatal("shell payload reached agent broadcast")
	default:
	}
	m.Revoke(t.Context(), "", "login", "")
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("revoked socket stayed open")
	}
}
func TestShellResolverBindsPersistedLaunch(t *testing.T) {
	fx := newTopicFx(t)
	ctx := t.Context()
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET runtime='docker' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: topicOwner, Runtime: "sandbox", LaunchGeneration: "launch", Root: protocol.WorkspaceRoot{Device: 1, Inode: 1}, Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "member", Name: "project", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if err := db.InsertSessionWorkspace(ctx, fx.pool, fx.sess, manifest); err != nil {
		t.Fatal(err)
	}
	generation, err := db.BeginWorkspaceRuntime(ctx, fx.pool, fx.sess, manifest.LaunchGeneration, 0)
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: generation}
	b := shellTestBinding()
	b.RuntimeGeneration = ref.Generation
	b.Audience = ownershell.RuntimeAudience(b.Connection, ref.Generation)
	b.Session = fx.sess
	b.Owner = topicOwner
	rt := &shellTestRuntime{binding: b}
	resolver := shellResolver{fx.api, rt}
	if _, err := resolver.Resolve(ctx, fx.sess); err == nil {
		t.Fatal("pending prepared root accepted")
	}
	prepared := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: ref, ManifestDigest: manifest.Digest, Root: b.Root}
	if err := db.BindWorkspaceRuntime(ctx, fx.pool, fx.sess, ref, prepared); err != nil {
		t.Fatal(err)
	}
	rt.binding.Root.Inode++
	if _, err := resolver.Resolve(ctx, fx.sess); err == nil {
		t.Fatal("forged physical root accepted")
	}
	rt.binding = b
	if _, err := resolver.Resolve(ctx, fx.sess); err != nil {
		t.Fatal("valid persisted launch", err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(ctx, fx.sess); err == nil {
		t.Fatal("persisted runtime replacement accepted")
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET runtime='docker' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	rt.binding.Launch = "forged"
	if _, err := resolver.Resolve(ctx, fx.sess); err == nil {
		t.Fatal("forged launch accepted")
	}
	rt.binding = b
	cases := []struct {
		name, status string
		owner        *string
		discard      *time.Time
		parent       *string
	}{
		{name: "stopped", status: "stopped", owner: &b.Owner},
		{name: "unowned", status: "running"},
		{name: "discard", status: "running", owner: &b.Owner, discard: new(time.Time)},
		{name: "child", status: "running", owner: &b.Owner, parent: &fx.ended},
	}
	for _, c := range cases {
		if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status=$2,spawning_account_id=$3,discard_requested_at=$4,parent_session_id=$5 WHERE id=$1`, fx.sess, c.status, c.owner, c.discard, c.parent); err != nil {
			t.Fatal(err)
		}
		if _, err := resolver.Resolve(ctx, fx.sess); err == nil {
			t.Fatal("invalid persisted lifecycle accepted", c.name)
		}
	}

}
func TestShellAuthorityFailsClosed(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			defer srv.Close()
			s := shellAuthority{srv.URL, "key", srv.Client()}
			if _, err := s.Authorize(t.Context(), ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"}); err == nil {
				t.Fatal("refusal accepted")
			}
		})
	}
}

func TestShellControlStreamDeliversLoginRevocation(t *testing.T) {
	rt := &shellTestRuntime{binding: shellTestBinding(), out: make(chan []byte, 8), input: make(chan []byte, 8)}
	m, err := ownershell.New(shellTestAuthority{}, rt, rt, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	defer m.Unavailable(context.Background())
	actor := ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"}
	info, err := m.Open(t.Context(), actor, "session")
	if err != nil {
		t.Fatal(err)
	}
	at, err := m.Attach(t.Context(), actor, info.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/shell/control" || r.Header.Get("X-Internal-Key") != "control-fixture" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"policy": protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}, "revocations": []map[string]any{{"kind": "sid", "value": "login", "revoked_at": time.Now().UTC()}}})
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchShellControl(ctx, shellAuthority{srv.URL, "control-fixture", srv.Client()}, m)
	}()
	defer func() { cancel(); <-done }()
	select {
	case _, ok := <-at.Output:
		if ok {
			t.Fatal("payload on revocation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("control stream did not revoke login")
	}
	cancel()
	<-done
	if m.Input(t.Context(), actor, info.Handle, at.ID, 1, []byte("stale")) == nil {
		t.Fatal("revoked login dispatched")
	}
}
func TestShellControlDisconnectClosesHandles(t *testing.T) {
	rt := &shellTestRuntime{binding: shellTestBinding(), out: make(chan []byte, 8), input: make(chan []byte, 8)}
	m, err := ownershell.New(shellTestAuthority{}, rt, rt, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	defer m.Unavailable(context.Background())
	actor := ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"}
	info, err := m.Open(t.Context(), actor, "session")
	if err != nil {
		t.Fatal(err)
	}
	at, err := m.Attach(t.Context(), actor, info.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"policy": protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}})
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		monitorShellControl(ctx, shellAuthority{srv.URL, "control-fixture", srv.Client()}, m)
	}()
	defer func() { cancel(); <-done }()
	select {
	case _, ok := <-at.Output:
		if ok {
			t.Fatal("unexpected output")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("authority disconnect retained handle")
	}
	cancel()
	<-done
}

// A refused describe supplies no trusted runtime binding. Only persisted owner/session
// identity may survive resolution, so the manager can diagnose the owner's failure.
func TestShellDescribeRefusalReachesOnlyPersistedOwner(t *testing.T) {
	f := newTopicFx(t)
	ctx := t.Context()
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "describe-desktop")
	dc.WorkspaceCapabilities = protocol.WorkspaceCapabilitiesFor("host", "linux")
	dc.shellControl = make(chan []byte, 32)
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET daemon_id=$2,runtime='daemon' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	f.api.hub.SetSessionOwner(f.sess, dc.ID)
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: topicOwner, Runtime: "host", LaunchGeneration: "launch", Root: protocol.WorkspaceRoot{Device: 1, Inode: 1}, Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "member", Name: "project", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if err := db.InsertSessionWorkspace(ctx, f.pool, f.sess, manifest); err != nil {
		t.Fatal(err)
	}
	generation, err := db.BeginWorkspaceRuntime(ctx, f.pool, f.sess, manifest.LaunchGeneration, 0)
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: generation}
	prepared := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: ref, ManifestDigest: manifest.Digest, Root: manifest.Root}
	if err := db.BindWorkspaceRuntime(ctx, f.pool, f.sess, ref, prepared); err != nil {
		t.Fatal(err)
	}
	rt := newRemoteShell(f.api)
	resolver := shellResolver{f.api, rt}
	manager, err := ownershell.New(shellTestAuthority{}, resolver, rt, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.api.shellRuntime, f.api.shells = rt, manager
	f.api.RegisterShellRoutes(f.mux)
	var calls atomic.Int32
	var code atomic.Value
	code.Store(string(ownershell.ReasonRuntimeIdentity))
	worker, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case raw := <-dc.shellControl:
				calls.Add(1)
				var req shellruntime.RPC
				if json.Unmarshal(raw, &req) == nil {
					// Even an error with a plausible or forged binding must not confer authority.
					f.api.hub.deliverShell(dc, shellruntime.RPC{Type: "owner_shell_result", Version: 1, SessionID: req.SessionID, RequestID: req.RequestID, Op: req.Op, ErrorCode: code.Load().(string), Binding: shellTestBinding()})
				}
			case <-worker.Done():
				return
			}
		}
	}()
	defer func() { cancel(); <-done }()
	for _, reason := range []string{string(ownershell.ReasonRuntimeIdentity), string(ownershell.ReasonRuntimeCleanup), "unsafe upstream detail"} {
		t.Run(reason, func(t *testing.T) {
			code.Store(reason)
			want := ownershell.Reason(reason)
			if !ownershell.SafeRefusalReason(reason) {
				want = ownershell.ReasonUnavailable
			}
			b, err := resolver.Resolve(ctx, f.sess)
			trusted := ownershell.Binding{Owner: topicOwner, Session: f.sess}
			if b != trusted || err == nil || ownershell.RefusalReason(err) != want {
				t.Errorf("resolved binding=%+v reason=%q; want persisted identity and %q", b, ownershell.RefusalReason(err), want)
			}
			for _, who := range []struct {
				name, owner, kind string
				caps              []string
			}{
				{name: "foreign human", owner: topicOther, kind: "human"},
				{name: "foreign admin", owner: topicOther, kind: "human", caps: []string{"account.manage"}},
				{name: "owner agent", owner: topicOwner, kind: "agent"},
				{name: "owner", owner: topicOwner, kind: "human"},
			} {
				t.Run(who.name, func(t *testing.T) {
					req := httptest.NewRequest("POST", "http://shell.example/api/sessions/"+f.sess+"/shells", nil)
					req.Header.Set("Authorization", "Bearer "+f.token(who.owner, who.kind, who.caps...))
					req.Header.Set("Origin", "http://shell.example")
					rec := httptest.NewRecorder()
					before := calls.Load()
					f.mux.ServeHTTP(rec, req)
					if rec.Code != http.StatusForbidden {
						t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
					}
					var body struct {
						Code string `json:"code"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if who.name == "owner" {
						if body.Code != string(want) || calls.Load() == before {
							t.Errorf("owner response=%s; want %q after describe", rec.Body.String(), want)
						}
					} else {
						if calls.Load() != before || (body.Code != "" && body.Code != string(ownershell.ReasonUnavailable)) {
							t.Errorf("non-owner received runtime dispatch or diagnosis: %s", rec.Body.String())
						}
					}
				})
			}
		})
	}

	t.Run("persisted owner mismatch", func(t *testing.T) {
		if _, err := f.pool.Exec(ctx, `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, f.sess, topicOther); err != nil {
			t.Fatal(err)
		}
		before := calls.Load()
		b, err := resolver.Resolve(ctx, f.sess)
		if b != (ownershell.Binding{}) || err == nil || ownershell.RefusalReason(err) != ownershell.ReasonUnavailable || calls.Load() != before {
			t.Fatalf("inconsistent persisted owner reached runtime or disclosed a reason: binding=%+v error=%v", b, err)
		}
	})
}
