package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestDesktopEnrollmentPendingPreflightAndAudit(t *testing.T) {
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
	mux.HandleFunc("/ws/daemon", fx.hub.ServeDaemon(spawnTokenDaemonTok, fx.pool))
	if w := enrollRequest(t, fx, mux, "human", privAcctA, ""); w.Code != 200 {
		t.Fatal("issuance failed")
	}
	var m protocol.DesktopEnroll
	if err := json.Unmarshal(<-dc.send, &m); err != nil {
		t.Fatal(err)
	}
	var issuedCount int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_enrollment_audit WHERE desktop_id=$1 AND actor_id=$2 AND action='issued' AND ownership_revision=$3`, dc.ID, privAcctA, m.Binding.Revision).Scan(&issuedCount); err != nil || issuedCount != 1 {
		t.Fatal("issuance audit missing", err)
	}
	r := httptest.NewRequest("GET", "/api/daemon/whoami", nil)
	r.Header.Set("Authorization", "Bearer "+m.Token)
	w := httptest.NewRecorder()
	fx.api.HandleDaemonWhoami(w, r)
	d, err := db.GetDesktop(t.Context(), fx.pool, dc.ID)
	if err != nil || d.OwnerID != "" || !d.Compatibility || d.Generation != gen {
		t.Fatal("preflight committed enrollment", err)
	}
	pending, err := db.GetDesktopEnrollment(t.Context(), fx.pool, dc.ID)
	if err != nil || pending.TokenID != m.Binding.TokenID {
		t.Fatal("preflight cleared pending credential", err)
	}
	if w.Code != 200 {
		t.Errorf("installer preflight returned %d for pending credential", w.Code)
	}
	// A valid core binding still needs exact pending metadata and generation.
	bindings := map[string]desktopauth.Binding{m.Token: m.Binding}
	for i, field := range []string{"token", "owner", "revision"} {
		b := m.Binding
		switch field {
		case "token":
			b.TokenID = newUUID()
		case "owner":
			b.OwnerID = privAcctB
		case "revision":
			b.Revision++
		}
		bindings["bdt_"+strings.Repeat(string(rune('a'+i)), 64)] = b
	}
	desktopFakeCore(t, fx.api, bindings)
	probe := func(token string, status int) {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/daemon/whoami", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		fx.api.HandleDaemonWhoami(w, r)
		if w.Code != status || strings.Contains(w.Body.String(), "bdt_") {
			t.Fatalf("preflight expected %d, got %d", status, w.Code)
		}
	}
	for token := range bindings {
		if token != m.Token {
			probe(token, 401)
		}
	}
	for _, statement := range []string{
		`UPDATE daemons SET connection_generation=NULL WHERE id=$1`,
		`UPDATE daemons SET enrollment_request_id=NULL WHERE id=$1`,
		`UPDATE daemons SET desktop_owner_id='other-person' WHERE id=$1`,
	} {
		if _, err := fx.pool.Exec(t.Context(), statement, dc.ID); err != nil {
			t.Fatal(err)
		}
		probe(m.Token, 401)
		if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET connection_generation=$2,enrollment_request_id=$3,desktop_owner_id=NULL WHERE id=$1`, dc.ID, gen, pending.RequestID); err != nil {
			t.Fatal(err)
		}
	}
	probe(m.Token, 200)
	// The daemon stored this credential but the old socket lost its ack.
	srv := httptest.NewServer(mux)
	defer srv.Close()
	desktopConnect(t, srv.URL, m.Token, dc.ID, dc.Name, true)
	var completedCount int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_enrollment_audit WHERE desktop_id=$1 AND actor_id=$2 AND action='enrolled' AND ownership_revision=$3`, dc.ID, privAcctA, m.Binding.Revision).Scan(&completedCount); err != nil || completedCount != 1 {
		t.Fatal("completion audit missing", err)
	}
	t.Log("verified runner issuance and completion audit rows, including lost-ack websocket recovery")
	if w.Code != 200 {
		t.Fatalf("installer preflight returned %d for credential accepted by websocket", w.Code)
	}
}

func TestDesktopCredentialNeverReturnedToBrowser(t *testing.T) {
	fx := newPrivFixture(t)
	enrollmentCore(t, fx)
	mux := http.NewServeMux()
	fx.api.RegisterDesktopRoutes(mux)
	r := httptest.NewRequest("POST", "/api/desktop-ownership/"+fx.dc.ID+"/credential", nil)
	r.Header.Set("Authorization", "Bearer "+fx.tok(t, "human", privAcctA, "session.start"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code == 200 && strings.Contains(w.Body.String(), "bdt_") {
		t.Fatal("human browser HTTP response contains the desktop bearer used by the new download action")
	}
}

func TestDesktopEnrollmentRefusalAudit(t *testing.T) {
	fx := desktopUpgradeFixture(t)
	mux := http.NewServeMux()
	fx.api.RegisterDesktopRoutes(mux)
	if w := enrollRequest(t, fx, mux, "human", privAcctA, ""); w.Code != 409 {
		t.Fatal("offline enrollment accepted")
	}
	var count int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_enrollment_audit WHERE desktop_id=$1 AND actor_id=$2 AND action='refused' AND created_at IS NOT NULL`, fx.dc.ID, privAcctA).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing refused enrollment audit", err)
	}
	// A foreign caller cannot create audit records for another person's machine.
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2 WHERE id=$1`, fx.dc.ID, privAcctB); err != nil {
		t.Fatal(err)
	}
	_ = enrollRequest(t, fx, mux, "human", privAcctA, "")
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_enrollment_audit WHERE desktop_id=$1`, fx.dc.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("unauthorized audit write", err)
	}
}

func TestDesktopEnrollmentAuditFailureRollsBack(t *testing.T) {
	for _, action := range []string{"issued", "enrolled"} {
		t.Run(action, func(t *testing.T) {
			fx := desktopUpgradeFixture(t)
			gen := newUUID()
			if _, err := db.BindLegacyDesktop(t.Context(), fx.pool, fx.dc.ID, gen); err != nil {
				t.Fatal(err)
			}
			p := db.DesktopEnrollment{Owner: privAcctA, TokenID: newUUID(), RequestID: newUUID(), Generation: gen, Revision: 2}
			if action == "enrolled" {
				if err := db.BeginDesktopEnrollment(t.Context(), fx.pool, fx.dc.ID, p); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := fx.pool.Exec(t.Context(), `CREATE FUNCTION fixture_reject_enrollment_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture audit refusal'; END $$; CREATE TRIGGER fixture_reject_enrollment_audit BEFORE INSERT ON desktop_enrollment_audit FOR EACH ROW EXECUTE FUNCTION fixture_reject_enrollment_audit()`); err != nil {
				t.Fatal(err)
			}
			err := db.WithDesktopFence(t.Context(), fx.pool, fx.dc.ID, func(ctx context.Context) error {
				if action == "issued" {
					return db.BeginDesktopEnrollment(ctx, fx.pool, fx.dc.ID, p)
				}
				return db.CompleteDesktopEnrollment(ctx, fx.pool, fx.dc.ID, p)
			})
			if err == nil {
				t.Fatal("enrollment succeeded without its audit event")
			}
			d, err := db.GetDesktop(t.Context(), fx.pool, fx.dc.ID)
			if err != nil || d.OwnerID != "" || !d.Compatibility || d.Generation != gen {
				t.Fatal("audit failure partially committed ownership", err)
			}
			pending, err := db.GetDesktopEnrollment(t.Context(), fx.pool, fx.dc.ID)
			if err != nil || (action == "issued" && pending.TokenID != "") || (action == "enrolled" && pending != p) {
				t.Fatal("audit failure partially committed pending state", err)
			}
		})
	}
}

func TestDesktopEnrollmentRejectsReplacementAfterPresentation(t *testing.T) {
	automaticCompatibility(t)
	fx := desktopUpgradeFixture(t)
	enrollmentCore(t, fx)
	mux := http.NewServeMux()
	fx.api.RegisterDesktopRoutes(mux)
	mux.HandleFunc("/ws/daemon", fx.hub.ServeDaemon(spawnTokenDaemonTok, fx.pool))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	connect := func() *websocket.Conn {
		t.Helper()
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/daemon", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		if err := c.WriteJSON(map[string]any{"type": "daemon_hello", "token": spawnTokenDaemonTok, "name": fx.dc.Name, "mode": "local", "welcome": true, "desktop_enrollment": true, "hostname": "workstation.example.com", "os_user": "desktop-user"}); err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		var welcome protocol.DaemonWelcome
		if err := c.ReadJSON(&welcome); err != nil {
			t.Fatal(err)
		}
		return c
	}
	connect()
	original := desktopWait(t, fx.hub, fx.dc.ID)
	readDetails := func(expected int) desktopEnrollmentConnection {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/api/desktop-ownership/"+fx.dc.ID+"/enrollment-connection", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+fx.tok(t, "human", privAcctA, "session.start"))
		r.Header.Set("X-Forwarded-For", "198.51.100.10")
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != expected || !strings.Contains(res.Header.Get("Cache-Control"), "no-store") {
			t.Fatal("connection presentation refused or cached")
		}
		var details desktopEnrollmentConnection
		if err := json.NewDecoder(res.Body).Decode(&details); err != nil {
			t.Fatal(err)
		}
		return details
	}
	details := readDetails(200)
	if details.DesktopID != fx.dc.ID || details.Generation != original.DesktopGeneration || details.Hostname != "workstation.example.com" || details.OSUser != "desktop-user" || details.ConnectedSince.IsZero() || !strings.HasPrefix(details.SourceAddress, "127.0.0.1:") {
		t.Fatal("incorrect displayed connection details")
	}
	shown := details.Generation
	connect()
	deadline := time.Now().Add(5 * time.Second)
	for fx.hub.GetDaemon(fx.dc.ID) == original {
		if time.Now().After(deadline) {
			t.Fatal("replacement not registered")
		}
		time.Sleep(time.Millisecond)
	}
	r := httptest.NewRequest("POST", "/api/desktop-ownership/"+fx.dc.ID+"/enroll", strings.NewReader(fmt.Sprintf(`{"confirmed":true,"connection_generation":%q}`, shown)))
	r.Header.Set("Authorization", "Bearer "+fx.tok(t, "human", privAcctA, "session.start"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("replaced connection accepted a confirmation for the old generation: %d", w.Code)
	}
	p, err := db.GetDesktopEnrollment(t.Context(), fx.pool, fx.dc.ID)
	if err != nil || p.TokenID != "" {
		t.Fatal("replacement confirmation issued a credential", err)
	}
	if !strings.Contains(w.Body.String(), "connection_changed") || !strings.Contains(w.Body.String(), "confirm again") {
		t.Fatal("replacement refusal omitted confirmation guidance")
	}
	var count int
	for _, action := range []string{"confirmed", "refused"} {
		if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_enrollment_audit WHERE desktop_id=$1 AND actor_id=$2 AND action=$3 AND connection_generation=$4 AND created_at IS NOT NULL`, fx.dc.ID, privAcctA, action, shown).Scan(&count); err != nil || count != 1 {
			t.Fatal("missing confirmation/refusal audit for displayed generation", err)
		}
	}
	current := readDetails(200)
	if current.Generation == shown {
		t.Fatal("fresh presentation kept old generation")
	}
	body := fmt.Sprintf(`{"confirmed":true,"connection_generation":%q}`, current.Generation)
	r = httptest.NewRequest("POST", "/api/desktop-ownership/"+fx.dc.ID+"/enroll", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+fx.tok(t, "human", privAcctA, "session.start"))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("freshly confirmed connection refused: %d", w.Code)
	}
	for _, action := range []string{"confirmed", "issued"} {
		if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_enrollment_audit WHERE desktop_id=$1 AND actor_id=$2 AND action=$3 AND connection_generation=$4`, fx.dc.ID, privAcctA, action, current.Generation).Scan(&count); err != nil || count != 1 {
			t.Fatal("missing confirmation/issuance audit", err)
		}
	}
}

func TestDesktopEnrollmentConnectionGuards(t *testing.T) {
	fx := newPrivFixture(t)
	dc := fx.dc
	dc.DesktopEnrollment = true
	dc.Hostname, dc.OSUser, dc.SourceAddress, dc.ConnectedSince = "workstation.example.com", "desktop-user", "192.0.2.10:1234", time.Now()
	mux := http.NewServeMux()
	fx.api.RegisterDesktopRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	for _, tc := range []struct {
		name, kind, account, origin string
		caps                        []string
		status                      int
	}{
		{"owner", "human", privAcctA, "", []string{"session.start"}, 200},
		{"foreign", "human", privAcctB, "", []string{"session.start"}, 409},
		{"foreign admin", "human", privAcctB, "", []string{"session.start", "account.manage"}, 409},
		{"agent", "agent", privAcctA, "", []string{"session.start"}, 403},
		{"origin", "human", privAcctA, "https://attacker.example.com", []string{"session.start"}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/api/desktop-ownership/"+dc.ID+"/enrollment-connection", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+fx.tok(t, tc.kind, tc.account, tc.caps...))
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			raw, err := io.ReadAll(res.Body)
			if err != nil || res.StatusCode != tc.status {
				t.Fatalf("connection details guard: got %d, want %d", res.StatusCode, tc.status)
			}
			if strings.Contains(string(raw), "bdt_") || (tc.status != 200 && strings.Contains(string(raw), dc.Hostname)) {
				t.Fatal("private connection details or credential exposed")
			}
		})
	}
}

func TestDesktopEnrollmentRequiresExplicitConfirmation(t *testing.T) {
	fx := desktopUpgradeFixture(t)
	enrollmentCore(t, fx)
	gen := newUUID()
	rev, err := db.BindLegacyDesktop(t.Context(), fx.pool, fx.dc.ID, gen)
	if err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{ID: fx.dc.ID, Name: fx.dc.Name, Mode: "local", LegacyDesktop: true, DesktopEnrollment: true, Compatibility: true, LegacyRevision: rev, DesktopGeneration: gen, Hostname: "workstation.example.com", OSUser: "desktop-user", ConnectedSince: time.Now(), SourceAddress: "192.0.2.10:1234", send: make(chan []byte, 8)}
	fx.hub.Register(dc)
	mux := http.NewServeMux()
	fx.api.RegisterDesktopRoutes(mux)
	for _, body := range []string{"", `{}`, `{"confirmed":false}`, `{"confirmed":true,"connection_generation":"invalid"}`, fmt.Sprintf(`{"connection_generation":%q}`, gen)} {
		r := httptest.NewRequest("POST", "/api/desktop-ownership/"+dc.ID+"/enroll", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+fx.tok(t, "human", privAcctA, "session.start"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "confirm again") {
			t.Fatal("missing confirmation accepted or unhelpful refusal")
		}
	}
	pending, err := db.GetDesktopEnrollment(t.Context(), fx.pool, dc.ID)
	if err != nil || pending.TokenID != "" || len(dc.send) != 0 {
		t.Fatal("issued before confirmation", err)
	}
}
