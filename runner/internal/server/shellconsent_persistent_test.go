package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Runtime consent memory is modeled here; shellruntime tests separately exercise
// the actual private helper, one-use affirmation, native barriers and cleanup.
func TestPersistentHostConsentOpenAndOfflineWithdrawal(t *testing.T) {
	fx := newTopicFx(t)
	ctx := t.Context()
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: topicOwner, Runtime: "host", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "member", Name: "project", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if err := db.InsertSessionWorkspace(ctx, fx.pool, fx.sess, manifest); err != nil {
		t.Fatal(err)
	}
	generation, err := db.BeginWorkspaceRuntime(ctx, fx.pool, fx.sess, manifest.LaunchGeneration, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := shellTestBinding()
	b.Session = fx.sess
	b.Owner = topicOwner
	b.Runtime = "host"
	b.RuntimeGeneration = generation
	b.Audience = ownershell.RuntimeAudience(b.Connection, generation)
	b.ServerCapabilities.ShellPersistence = 2
	b.ServerCapabilities.PersistentHost = true
	b.RuntimeCapabilities = b.ServerCapabilities
	b.IdentityDigest = "strict-identity"
	enrollment := desktopauth.Binding{DesktopID: "desktop", OwnerID: topicOwner, TokenID: "credential", Revision: 1, ExpiresAt: time.Now().Add(8 * 24 * time.Hour)}
	b.ShellLink = protocol.ShellLinkWelcome{Version: 2, Runner: "runner", Daemon: "daemon-process", Challenge: "fresh-link", Desktop: enrollment}
	ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: generation}
	if err = db.BindWorkspaceRuntime(ctx, fx.pool, fx.sess, ref, protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: ref, Root: b.Root, ManifestDigest: manifest.Digest}); err != nil {
		t.Fatal(err)
	}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "revision": 1, "enabled": true, "authorization_version": 2, "outcome": "allow", "owner_id": req["owner_id"], "login_id": req["login_id"], "nonce": req["nonce"], "login_expires_at": time.Now().Add(8 * 24 * time.Hour).Unix()})
	}))
	defer core.Close()
	fx.api.coreURL = core.URL
	fx.api.coreInternalKey = "fixture-key"
	remote := newRemoteShell(fx.api)
	fx.api.shellRuntime = remote
	authority := shellAuthority{core.URL, "fixture-key", core.Client()}
	audit := make(shellAuditReceipt, 32)
	manager, err := ownershell.New(authority, shellResolver{fx.api, remote}, remote, audit, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	b.ShellLink.Runner = manager.Incarnation()
	fx.api.shells = manager
	manager.SetPolicy(ctx, protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	fx.api.hub.runtimeShellReady.Store(true)
	fx.api.RegisterShellRoutes(fx.mux)
	dc := &DaemonConn{ID: topicDaemonID, WorkspaceCapabilities: b.RuntimeCapabilities, DesktopBinding: &enrollment, ShellLink: &b.ShellLink, send: make(chan []byte, 8), shellControl: make(chan []byte, 32), shellData: make(chan []byte, 32)}
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	var mu sync.Mutex
	var consent protocol.ShellConsentV2
	accepted := false
	opened := 0
	clockRefusal := true
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-run.Done():
				return
			case raw := <-dc.shellControl:
				var req shellruntime.RPC
				if json.Unmarshal(raw, &req) != nil {
					return
				}
				reply := shellruntime.RPC{Version: req.Version, Type: "owner_shell_result", SessionID: req.SessionID, RequestID: req.RequestID, Op: req.Op, OK: true, Binding: b, Identity: shellruntime.Identity{Machine: "machine", Account: "person", UID: 1000, Shell: "/bin/sh", Cwd: "/project"}}
				mu.Lock()
				switch req.Op {
				case "consent_challenge":
					pins := protocol.ShellCustody{Owner: topicOwner, Login: req.Actor.Login, Session: b.Session, Launch: b.Launch, Runtime: b.Runtime, RuntimeGeneration: b.RuntimeGeneration, Root: b.Root, Manifest: b.ManifestDigest, Desktop: enrollment.DesktopID, Credential: enrollment.TokenID, OwnershipRevision: enrollment.Revision, Runner: b.ShellLink.Runner, Daemon: b.ShellLink.Daemon, Identity: b.IdentityDigest}
					consent = protocol.NewShellConsentV2(protocol.HostShellConsent{ID: shellRandom(), ChallengeID: shellRandom(), OwnerID: topicOwner, LaunchGeneration: b.Launch, MachineID: "machine", Account: "person", UID: 1000, Shell: "/bin/sh", Cwd: "/project"}, pins, time.Now().Add(time.Minute).Unix())
					accepted = false
					reply.PersistentConsent = &consent
				case "consent_accept":
					accepted = req.PersistentConsent != nil && *req.PersistentConsent == consent
					reply.OK = accepted
				case "open":
					if clockRefusal {
						clockRefusal = false
						reply.OK = false
						reply.AllocationRefused = true
						reply.ErrorCode = string(ownershell.ReasonRuntimeBinding)
						break
					}

					reply.OK = accepted && req.Custody != nil && consent.Allows(*req.Custody)
					if reply.OK {
						opened++
					}
				case "close":
					if req.Custody != nil {
						reply.CustodyCleanup = &protocol.CustodyCleanup{Version: 2, Custody: req.Custody.Digest(), Scope: req.Custody.PhysicalScope(), Transaction: req.Transaction, Responder: b.ShellLink.Daemon, ContainmentEmpty: true, PTYsClosed: true, Reaped: true, OutputJoined: true}
					}
				}
				mu.Unlock()
				fx.api.hub.deliverShell(dc, reply)
			}
		}
	}()
	defer func() { manager.Shutdown(context.Background()); cancel(); <-done }()
	owner := fx.token(topicOwner, "human")
	call := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://shell.example"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+owner)
		r.Header.Set("Origin", "http://shell.example")
		w := httptest.NewRecorder()
		fx.mux.ServeHTTP(w, r)
		return w
	}
	base := "/api/sessions/" + fx.sess

	legacyIdentity := shellruntime.Identity{Machine: "machine", Account: "person", UID: 1000, Shell: "/bin/sh", Cwd: "/project"}
	legacyBinding := consentBinding(b, testSID, legacyIdentity)
	legacyConsent := protocol.HostShellConsent{ID: "legacy-consent", ChallengeID: "legacy-challenge", OwnerID: topicOwner, LaunchGeneration: b.Launch, MachineID: "machine", Account: "person", UID: 1000, Shell: "/bin/sh", Cwd: "/project"}
	if err := db.CreateShellChallenge(ctx, fx.pool, legacyBinding, legacyConsent, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptShellChallenge(ctx, fx.pool, legacyBinding, legacyConsent.ChallengeID); err != nil {
		t.Fatal(err)
	}
	if got := call(base+"/shells", ""); got.Code != 428 {
		t.Fatalf("fresh per-child consent missing: %d %s", got.Code, got.Body.String())
	}
	challenge := call(base+"/shell-consent/challenge", "")
	if challenge.Code != 201 {
		t.Fatalf("challenge: %d %s", challenge.Code, challenge.Body.String())
	}
	var host struct {
		protocol.HostShellConsent
		Version int `json:"version"`
	}
	if json.Unmarshal(challenge.Body.Bytes(), &host) != nil || host.Version != 2 {
		t.Fatal("v2 challenge missing")
	}
	body, _ := json.Marshal(map[string]any{"challenge_id": host.ChallengeID, "accept": true})
	if got := call(base+"/shell-consent/accept", string(body)); got.Code != 400 {
		t.Fatalf("an old browser accepted v2 lifetime semantics: %d %s", got.Code, got.Body.String())
	}
	body, _ = json.Marshal(map[string]any{"version": 2, "challenge_id": host.ChallengeID, "accept": true})
	if got := call(base+"/shell-consent/accept", string(body)); got.Code != 200 {
		t.Fatalf("accept: %d %s", got.Code, got.Body.String())
	}
	assertAudit := func(event string) {
		t.Helper()
		found := false
		for len(audit) > 0 {
			item := <-audit
			raw, _ := json.Marshal(item)
			var fields map[string]any
			_ = json.Unmarshal(raw, &fields)
			if fields["event"] == event {
				found = true
				if fields["login_id"] != testSID || fields["custody_digest"] == "" || fields["outcome"] == "" {
					t.Fatal("consent audit lacks context", fields)
				}
			}
		}
		if !found {
			t.Fatalf("%s was not audited", event)
		}
	}
	assertAudit("consent_accept")
	if refused := call(base+"/shells", ""); refused.Code == 201 || refused.Code == 428 {
		t.Fatal("clock refusal allocated a shell or lost acceptance", refused.Code)
	}
	remote.mu.Lock()
	reusable := !remote.consents[host.ID].consumed
	remote.mu.Unlock()
	if !reusable {
		t.Fatal("authenticated no-allocation reply consumed acceptance")
	}
	open := call(base+"/shells", "")
	if open.Code != 201 {
		t.Fatalf("open: %d %s", open.Code, open.Body.String())
	}
	var info protocol.OwnerShellInfo
	_ = json.Unmarshal(open.Body.Bytes(), &info)
	if !info.Persistent || info.Consent != host.ID {
		t.Fatal("persistent shell lost its own acceptance")
	}
	if got := call(base+"/shells", ""); got.Code != 428 {
		t.Fatal("acceptance reused by a new tab")
	}
	// Repeated challenges must be bounded before reaching the runtime.
	for range 3 {
		if got := call(base+"/shell-consent/challenge", ""); got.Code != 201 {
			t.Fatalf("challenge burst: %d", got.Code)
		}
	}
	if got := call(base+"/shell-consent/challenge", ""); got.Code != 429 {
		t.Fatalf("unbounded challenge rate: %d", got.Code)
	}
	fx.api.hub.UnregisterIfCurrent(dc)
	markDaemonSessionsLostCurrent(ctx, fx.api.hub, dc, fx.pool)
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET daemon_lost_at=now()-interval '31 minutes' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	fx.api.finaliseLostDaemonSessions(ctx)
	row, err := db.GetSession(ctx, fx.pool, fx.sess)
	if err != nil || row.EndReason != nil {
		t.Fatal("allocated retained shell lost the session after thirty minutes", err)
	}
	read := httptest.NewRequest("GET", "http://shell.example"+base+"/shell-consents", nil)
	read.Header.Set("Authorization", "Bearer "+owner)
	listed := httptest.NewRecorder()
	fx.mux.ServeHTTP(listed, read)
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), host.ID) {
		t.Fatal("offline consent cannot be found without describe")
	}
	foreign := fx.token(topicOther, "human")
	for _, id := range []string{fx.sess, "missing-private-session"} {
		read := httptest.NewRequest("GET", "http://shell.example/api/sessions/"+id+"/shell-consents", nil)
		read.Header.Set("Authorization", "Bearer "+foreign)
		denied := httptest.NewRecorder()
		fx.mux.ServeHTTP(denied, read)
		if denied.Code != 403 || strings.Contains(denied.Body.String(), host.ID) {
			t.Fatalf("foreign consent metadata leaked: %d %s", denied.Code, denied.Body.String())
		}
	}
	body, _ = json.Marshal(map[string]string{"consent_id": host.ID})
	withdrawal := call(base+"/shell-consent/withdraw", string(body))
	if withdrawal.Code != 200 {
		t.Fatalf("offline withdrawal: %d %s", withdrawal.Code, withdrawal.Body.String())
	}
	assertAudit("consent_withdraw")
	if again := call(base+"/shell-consent/withdraw", string(body)); again.Code != 200 {
		t.Fatal("repeat withdrawal failed", again.Code)
	}
	for len(audit) > 0 {
		if item := <-audit; item.Event == ownershell.ConsentWithdrawn {
			t.Fatal("repeat withdrawal logged another consent event")
		}
	}

	var state struct {
		Withdrawn bool `json:"withdrawn"`
		Pending   bool `json:"cleanup_pending"`
	}
	_ = json.Unmarshal(withdrawal.Body.Bytes(), &state)
	fx.api.finaliseLostDaemonSessions(ctx)
	row, err = db.GetSession(ctx, fx.pool, fx.sess)
	if err != nil || row.EndReason == nil {
		t.Fatal("Closing custody incorrectly held the daemon-loss sweep", err)
	}
	if !state.Withdrawn || !state.Pending {
		t.Fatal("offline withdrawal claimed physical cleanup")
	}
	if _, err := db.ReadPersistentShellConsent(ctx, fx.pool, topicOwner, testSID, fx.sess, host.ID); err == nil {
		t.Fatal("withdrawal tombstone missing")
	}
	mu.Lock()
	count := opened
	mu.Unlock()
	if count != 1 {
		t.Fatalf("allocated %d shells", count)
	}
}

func TestPersistentConsentReadFailureStaysUnknown(t *testing.T) {
	for _, tc := range []struct {
		name        string
		input, want error
	}{
		{"outage", context.DeadlineExceeded, ownershell.ErrUnknown},
		{"missing or withdrawn", pgx.ErrNoRows, ownershell.ErrDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := persistentConsentReadError(tc.input); !errors.Is(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPersistentConsentReadFailureReleasesUnusedReservation(t *testing.T) {
	fx := newTopicFx(t)
	r := newRemoteShell(fx.api)
	b := shellTestBinding()
	b.Owner = topicOwner
	b.Runtime = "host"
	b.Session = fx.sess
	a := ownershell.Actor{Kind: "human", Owner: topicOwner, Login: testSID}
	p := protocol.ShellCustody{Owner: a.Owner, Login: a.Login, Session: b.Session, Launch: b.Launch, Runtime: b.Runtime, Container: b.Container, RuntimeGeneration: b.RuntimeGeneration, Root: b.Root, Manifest: b.ManifestDigest, Identity: b.IdentityDigest}
	c := protocol.NewShellConsentV2(protocol.HostShellConsent{ID: "unused", ChallengeID: "challenge"}, p, time.Now().Add(time.Minute).Unix())
	r.consents[c.Host.ID] = &brokerRetainedConsent{value: c, accepted: true}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.ConsumePersistentConsent(ctx, a, b); !ownershell.Unknown(err) {
		t.Fatalf("read outage outcome: %v", err)
	}
	if r.consents[c.Host.ID].consumed {
		t.Fatal("unused acceptance stranded by database read failure")
	}
	if !r.readyPersistentConsent(a, b) {
		t.Fatal("retryable read failure consumed consent")
	}
}
