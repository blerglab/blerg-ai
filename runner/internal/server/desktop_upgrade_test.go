package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Simulate the real migration against this test's private database, preserving
// the old desktop and historical sessions. Never touch a shared schema.
func desktopUpgradeFixture(t *testing.T) *privFixture {
	t.Helper()
	fx := newPrivFixture(t)
	fx.hub.UnregisterIfCurrent(fx.dc)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=NULL,connection_generation=NULL WHERE id=$1`, fx.dc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `DROP TABLE desktop_enrollment_audit,desktop_compatibility_accounts;
 ALTER TABLE daemons DROP COLUMN desktop_compatibility,DROP COLUMN enrollment_revision,DROP COLUMN enrollment_owner_id,DROP COLUMN enrollment_token_id,DROP COLUMN enrollment_request_id,DROP COLUMN enrollment_generation;
 DELETE FROM schema_migrations WHERE filename IN ('087_desktop_compatibility.sql','088_desktop_enrollment_confirmation.sql');`); err != nil {
		t.Fatal(err)
	}
	if err := db.RunMigrations(t.Context(), fx.pool); err != nil {
		t.Fatal(err)
	}
	return fx
}
func automaticCompatibility(t *testing.T) {
	t.Helper()
	old, set := os.LookupEnv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS")
	_ = os.Unsetenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS")
	t.Cleanup(func() {
		if set {
			_ = os.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", old)
		} else {
			_ = os.Unsetenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS")
		}
	})
}
func TestAutomaticDesktopCompatibilityPolicy(t *testing.T) {
	for _, value := range []string{"auto", ""} {
		t.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", value)
		if !legacyDesktopAllowed(true) || legacyDesktopAllowed(false) {
			t.Fatal("automatic mode admitted fresh desktop or refused upgrade")
		}
	}
	t.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", "false")
	if legacyDesktopAllowed(true) {
		t.Fatal("override ignored")
	}
	t.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", "true")
	if !legacyDesktopAllowed(false) {
		t.Fatal("connection override ignored")
	}
}
func TestDesktopUpgradeSnapshotKeepsOnlyHistoricalScope(t *testing.T) {
	automaticCompatibility(t)
	fx := desktopUpgradeFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", fx.hub.ServeDaemon(spawnTokenDaemonTok, fx.pool))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	desktopConnect(t, srv.URL, spawnTokenDaemonTok, "", fx.dc.Name, true)
	dc := desktopWait(t, fx.hub, fx.dc.ID)
	row, err := db.GetDesktop(t.Context(), fx.pool, dc.ID)
	if err != nil || row.Status != "connected" || row.StatusReason != "" {
		t.Fatal("accepted hello did not record connected status")
	}
	if !dc.Compatibility || !isHostDaemon(dc) || fx.hub.ownedHostDaemon(t.Context(), privAcctA, dc.ID) != dc {
		t.Fatal("upgrade desktop not launchable")
	}
	if fx.hub.ownedHostDaemon(t.Context(), privAcctB, dc.ID) != nil {
		t.Fatal("foreign compatibility desktop exposed")
	}
	desktopConnect(t, srv.URL, spawnTokenDaemonTok, "", "fresh-desktop", false)
	fresh := newUUID()
	if err := db.UpsertDaemon(t.Context(), fx.pool, fresh, "new-desktop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.RunMigrations(t.Context(), fx.pool); err != nil {
		t.Fatal(err)
	}
	d, err := db.GetDesktop(t.Context(), fx.pool, fresh)
	if err != nil || d.Compatibility {
		t.Fatal("migration rerun admitted fresh desktop")
	}
}
func enrollmentCore(t *testing.T, fx *privFixture) {
	t.Helper()
	var mu sync.Mutex
	tokens := map[string]desktopauth.Binding{}
	next := 0
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Key") != "fixture-internal-key" {
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/internal/desktop-tokens/issue":
			var in struct {
				DesktopID string `json:"desktop_id"`
				OwnerID   string `json:"owner_id"`
				Revision  int64  `json:"ownership_revision"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			next++
			token := fmt.Sprintf("bdt_%064x", next)
			b := desktopauth.Binding{DesktopID: in.DesktopID, OwnerID: in.OwnerID, Revision: in.Revision, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
			tokens[token] = b
			_ = json.NewEncoder(w).Encode(map[string]any{"binding": b, "token": token})
		case "/internal/desktop-tokens/validate":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			b, ok := tokens[in["token"]]
			if !ok {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(b)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(core.Close)
	fx.api.coreURL = core.URL
	fx.api.coreInternalKey = "fixture-internal-key"
}
func enrollRequest(t *testing.T, fx *privFixture, mux *http.ServeMux, kind, account, origin string) *httptest.ResponseRecorder {
	t.Helper()
	generation := ""
	if dc := fx.hub.GetDaemon(fx.dc.ID); dc != nil {
		generation = dc.DesktopGeneration
	}
	r := httptest.NewRequest("POST", "/api/desktop-ownership/"+fx.dc.ID+"/enroll", strings.NewReader(fmt.Sprintf(`{"confirmed":true,"connection_generation":%q}`, generation)))
	r.Header.Set("Authorization", "Bearer "+fx.tok(t, kind, account, "session.start"))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
func TestDesktopEnrollmentRealDaemonRoundTrip(t *testing.T) {
	automaticCompatibility(t)
	fx := desktopUpgradeFixture(t)
	enrollmentCore(t, fx)
	mux := http.NewServeMux()
	fx.api.RegisterDesktopRoutes(mux)
	mux.HandleFunc("/ws/daemon", fx.hub.ServeDaemon(spawnTokenDaemonTok, fx.pool))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	state := t.TempDir()
	_ = os.Chmod(state, 0o700)
	client := daemon.NewWSClient(daemon.Config{ServerURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/daemon", DaemonToken: spawnTokenDaemonTok, DaemonName: fx.dc.Name, DaemonMode: "local", ReposRoot: t.TempDir(), CredentialStateDir: state, ReconnectInitial: time.Millisecond, ReconnectMax: time.Millisecond})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx, func() []string { return []string{fx.pubSession, fx.privSession} })
	}()
	defer func() { cancel(); <-done }()
	desktopWait(t, fx.hub, fx.dc.ID)
	if w := enrollRequest(t, fx, mux, "agent", privAcctA, ""); w.Code != 403 {
		t.Fatal("agent enrolled desktop")
	}
	if w := enrollRequest(t, fx, mux, "human", privAcctA, "https://attacker.example.com"); w.Code != 403 {
		t.Fatal("cross-origin enrollment")
	}
	if w := enrollRequest(t, fx, mux, "human", privAcctA, ""); w.Code != 200 || strings.Contains(w.Body.String(), "bdt_") {
		t.Fatal("invalid enrollment response")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		dc := fx.hub.GetDaemon(fx.dc.ID)
		if dc != nil && dc.DesktopBinding != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no personal reconnect")
		}
		time.Sleep(time.Millisecond)
	}
	d, _ := db.GetDesktop(t.Context(), fx.pool, fx.dc.ID)
	if d.OwnerID != privAcctA || d.Compatibility {
		t.Fatal("shared access not retired")
	}
	info, err := os.Stat(filepath.Join(state, "desktop-credential.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("credential not private")
	}
	if w := enrollRequest(t, fx, mux, "human", privAcctA, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "enrolled") {
		t.Fatal("not idempotent")
	}
	if w := enrollRequest(t, fx, mux, "human", privAcctB, ""); w.Code != 409 {
		t.Fatal("other person enrolled assigned machine")
	}
	desktopConnect(t, srv.URL, spawnTokenDaemonTok, "", fx.dc.Name, false)
}
func TestDesktopEnrollmentLostAckRotatesAndFencesDelivery(t *testing.T) {
	automaticCompatibility(t)
	fx := desktopUpgradeFixture(t)
	enrollmentCore(t, fx)
	gen := newUUID()
	rev, err := db.BindLegacyDesktop(t.Context(), fx.pool, fx.dc.ID, gen)
	if err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{ID: fx.dc.ID, Name: fx.dc.Name, Mode: "local", LegacyDesktop: true, Compatibility: true, DesktopEnrollment: true, Hostname: "workstation.example.com", OSUser: "desktop-user", ConnectedSince: time.Now(), SourceAddress: "192.0.2.10:1234", LegacyRevision: rev, DesktopGeneration: gen, send: make(chan []byte, 8)}
	fx.hub.Register(dc)
	mux := http.NewServeMux()
	fx.api.RegisterDesktopRoutes(mux)
	issue := func() ([]byte, protocol.DesktopEnroll) {
		t.Helper()
		if w := enrollRequest(t, fx, mux, "human", privAcctA, ""); w.Code != 200 {
			t.Fatal("issuance refused")
		}
		raw := <-dc.send
		var m protocol.DesktopEnroll
		if json.Unmarshal(raw, &m) != nil {
			t.Fatal("bad delivery")
		}
		return raw, m
	}
	firstRaw, first := issue()
	secondRaw, second := issue()
	if first.Token == second.Token || first.Binding.Revision >= second.Binding.Revision {
		t.Fatal("retry did not rotate")
	}
	if enrollmentDeliveryCurrent(t.Context(), fx.pool, dc, firstRaw) || !enrollmentDeliveryCurrent(t.Context(), fx.pool, dc, secondRaw) {
		t.Fatal("stale queued delivery")
	}
	stale, _ := json.Marshal(protocol.DesktopEnrollAck{Type: "desktop_enroll_ack", RequestID: first.RequestID, ConnectionGeneration: gen})
	if !acknowledgeDesktopEnrollment(t.Context(), fx.hub, dc, stale) {
		t.Fatal("old ack completed")
	}
	replacement := DaemonConn{ID: dc.ID, DesktopGeneration: newUUID()}
	if enrollmentDeliveryCurrent(t.Context(), fx.pool, &replacement, secondRaw) {
		t.Fatal("delivery moved to replacement")
	}
	if db.PendingDesktopBinding(t.Context(), fx.pool, first.Binding) {
		t.Fatal("old token remains authorized")
	}
	ack, _ := json.Marshal(protocol.DesktopEnrollAck{Type: "desktop_enroll_ack", RequestID: second.RequestID, ConnectionGeneration: gen})
	if acknowledgeDesktopEnrollment(t.Context(), fx.hub, dc, ack) {
		t.Fatal("current ack did not complete")
	}
}
func TestDesktopCompatibilityStartsChildren(t *testing.T) {
	fx := newDelegFx(t)
	revision := fx.dc.DesktopBinding.Revision
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=NULL,desktop_compatibility=true WHERE id=$1`, fx.dc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO desktop_compatibility_accounts(desktop_id,account_id) VALUES($1,'owner-1')`, fx.dc.ID); err != nil {
		t.Fatal(err)
	}
	fx.dc.DesktopBinding = nil
	fx.dc.LegacyDesktop = true
	fx.dc.Compatibility = true
	fx.dc.LegacyRevision = revision
	fx.grant(fx.parent, "codex", "docker", "app", false)
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, codexBody(), newUUID()); code != http.StatusCreated {
		t.Fatalf("child refused: %d %v", code, out)
	}
}
func TestDesktopCompatibilityOpensSessionFileReferences(t *testing.T) {
	fx := newPrivFixture(t)
	revision := fx.dc.DesktopBinding.Revision
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=NULL,desktop_compatibility=true WHERE id=$1`, fx.dc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO desktop_compatibility_accounts(desktop_id,account_id) VALUES($1,$2)`, fx.dc.ID, privAcctA); err != nil {
		t.Fatal(err)
	}
	fx.dc.DesktopBinding = nil
	fx.dc.LegacyDesktop = true
	fx.dc.Compatibility = true
	fx.dc.LegacyRevision = revision
	for _, enrolled := range []bool{false, true} {
		if enrolled {
			if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,desktop_compatibility=false WHERE id=$1`, fx.dc.ID, privAcctA); err != nil {
				t.Fatal(err)
			}
			fx.dc.DesktopBinding = &desktopauth.Binding{DesktopID: fx.dc.ID, OwnerID: privAcctA, Revision: revision, ExpiresAt: time.Now().Add(time.Hour)}
			fx.dc.LegacyDesktop, fx.dc.Compatibility = false, false
		}
		for _, name := range []string{"README.md", ".agent-notes/report.md", "attachments/report.md"} {
			r := httptest.NewRequest("GET", "/api/sessions/"+fx.pubSession+"/workspace-file?path="+name, nil)
			r.SetPathValue("id", fx.pubSession)
			r.Header.Set("Authorization", "Bearer "+fx.tok(t, "human", privAcctA, "session.start"))
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); fx.api.HandleGetWorkspaceFile(w, r) }()
			select {
			case raw := <-fx.dc.send:
				var q protocol.ReadWorkspaceFile
				if json.Unmarshal(raw, &q) != nil {
					t.Fatal("bad file request")
				}
				fx.hub.deliverWorkspaceFileResult(fx.dc, protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: q.RequestID, SessionID: q.SessionID, OK: true, Path: q.Path, Size: 2, ContentB64: "aGk=", Mtime: time.Now().UTC().Format(time.RFC3339Nano), View: "text", ContentType: "text/plain"})
			case <-done:
				t.Fatalf("file refused: %d", w.Code)
			case <-time.After(5 * time.Second):
				t.Fatal("file not routed")
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("no file response")
			}
			if w.Code != 200 {
				t.Fatalf("file refused: %d", w.Code)
			}
		}
	}
}
func TestDesktopEnrollmentRequiresHumanSessionAndCurrentMachine(t *testing.T) {
	fx := newPrivFixture(t)
	mux := http.NewServeMux()
	fx.api.RegisterDesktopRoutes(mux)
	for name, claims := range map[string]identity.Claims{"no login session": {Kind: "human", Sub: privAcctA}, "on behalf of": {Kind: "human", Sub: privAcctA, Sid: testSID, OnBehalfOf: privAcctB}} {
		t.Run(name, func(t *testing.T) {
			claims.Aud = coreAuthAudience
			claims.Caps = []string{"session.start"}
			claims.ExpiresAt = time.Now().Add(time.Minute).Unix()
			r := httptest.NewRequest("POST", "/api/desktop-ownership/"+fx.dc.ID+"/enroll", nil)
			r.Header.Set("Authorization", "Bearer "+mintRunnerToken(t, fx.priv, "core-1", claims))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("guard refused incorrectly: %d", w.Code)
			}
		})
	}
	fx.hub.UnregisterIfCurrent(fx.dc)
	if w := enrollRequest(t, fx, mux, "human", privAcctA, ""); w.Code != 409 {
		t.Fatal("offline machine accepted")
	}
	if len([]byte(desktopEnrollmentGuidance)) > 123 {
		t.Fatal("oversize websocket close guidance")
	}
}
