package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	deskPersonA = "aaaaaaaa-0000-4000-8000-000000000193"
	deskPersonB = "bbbbbbbb-0000-4000-8000-000000000193"
)

// deskCore is a fake core: desktop credential validation and issue, and one-time enrollment codes.
type deskCore struct {
	mu       sync.Mutex
	bindings map[string]desktopauth.Binding
	codes    map[string]string // code -> owner, removed when redeemed
	redeemed []string
	issued   int
}

func newDeskCore(t *testing.T, a *API) *deskCore {
	t.Helper()
	c := &deskCore{bindings: map[string]desktopauth.Binding{}, codes: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Key") != "fixture-internal-key" {
			w.WriteHeader(401)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		switch r.URL.Path {
		case "/internal/desktop-tokens/validate":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			b, ok := c.bindings[body["token"]]
			if !ok {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(b)
		case "/internal/desktop-tokens/issue":
			var body struct {
				ID       string `json:"desktop_id"`
				Owner    string `json:"owner_id"`
				Revision int64  `json:"ownership_revision"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			c.issued++
			token := "bdt_" + strings.Repeat(string(rune('a'+c.issued)), 64)
			b := desktopauth.Binding{DesktopID: body.ID, OwnerID: body.Owner, Revision: body.Revision, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
			c.bindings[token] = b
			_ = json.NewEncoder(w).Encode(map[string]any{"binding": b, "token": token})
		case "/internal/desktop-enrollment-codes/redeem":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			owner, ok := c.codes[body["code"]]
			if !ok {
				w.WriteHeader(403)
				return
			}
			delete(c.codes, body["code"])
			c.redeemed = append(c.redeemed, body["code"])
			_ = json.NewEncoder(w).Encode(map[string]string{"owner_id": owner})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a.coreURL, a.coreInternalKey = srv.URL, "fixture-internal-key"
	return c
}

func deskCode(c byte) string { return "bec_" + strings.Repeat(string(c), 40) }

func enrollWithCode(t *testing.T, mux http.Handler, code, name, addr string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"code": code, "name": name})
	req := httptest.NewRequest(http.MethodPost, "/api/desktops/enroll", strings.NewReader(string(raw)))
	req.RemoteAddr = addr + ":40000"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// The release blocker in #193: a brand-new remote desktop, after the 0.5 upgrade, enrolls with a
// person's one-time code. Expired, reused and unknown codes are refused, the desktop never adopts
// an existing record, and the credential it gets connects as that person's desktop.
func TestNewDesktopEnrollsWithOneTimeCode(t *testing.T) {
	t.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", "false")
	enrollCodes = &enrollCodeLimiter{}
	fx := newPrivFixture(t)
	core := newDeskCore(t, fx.api)
	core.codes[deskCode('1')] = deskPersonA
	// An unassigned upgrade-era desktop with the same name must not be adopted by a code.
	legacy := newUUID()
	if err := db.UpsertDaemon(t.Context(), fx.pool, legacy, "new-laptop", "local", ""); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", fx.hub.ServeDaemon(spawnTokenDaemonTok, fx.pool))
	fx.api.RegisterDesktopRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	w := enrollWithCode(t, mux, deskCode('1'), "New-Laptop", "192.0.2.1")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("enroll with a valid code: %d (body withheld: it may hold a credential)", w.Code)
	}
	var issued issuedDesktopCredential
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil || issued.Binding.OwnerID != deskPersonA || !strings.HasPrefix(issued.Token, "bdt_") {
		t.Fatalf("issued: desktop %s owner %s token present %v (%v)", issued.Binding.DesktopID, issued.Binding.OwnerID, issued.Token != "", err)
	}
	if issued.Binding.DesktopID == legacy {
		t.Fatal("a code adopted an existing unassigned desktop")
	}
	d, err := db.GetDesktop(t.Context(), fx.pool, issued.Binding.DesktopID)
	if err != nil || d == nil || d.OwnerID != deskPersonA || d.Name != "new-laptop" {
		t.Fatalf("new desktop row: %+v %v", d, err)
	}
	if old, _ := db.GetDesktop(t.Context(), fx.pool, legacy); old == nil || old.OwnerID != "" {
		t.Fatalf("legacy desktop changed: %+v", old)
	}
	desktopConnect(t, srv.URL, issued.Token, issued.Binding.DesktopID, "new-laptop", true)
	if dc := desktopWait(t, fx.hub, issued.Binding.DesktopID); dc.DesktopBinding == nil || dc.DesktopBinding.OwnerID != deskPersonA {
		t.Fatal("enrolled desktop did not connect as its person")
	}
	var audited int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_enrollment_audit WHERE desktop_id=$1 AND actor_id=$2 AND action='code_enrolled'`, issued.Binding.DesktopID, deskPersonA).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("code enrollment audit: %d %v", audited, err)
	}

	// Reused, unknown (or expired: core refuses them the same way) and malformed codes.
	for _, code := range []string{deskCode('1'), deskCode('2'), "bec_short", deskCode('G')} {
		if w := enrollWithCode(t, mux, code, "other", "192.0.2.2"); w.Code != 403 || strings.Contains(w.Body.String(), "bdt_") {
			t.Fatalf("code %q accepted: %d", code[:6], w.Code)
		}
	}
	if len(core.redeemed) != 1 {
		t.Fatalf("redeemed %d codes", len(core.redeemed))
	}
	// Rate limit: one client address gets ten attempts a minute.
	for i := range enrollCodeAttemptsPerAddr {
		if w := enrollWithCode(t, mux, deskCode('3'), "x", "192.0.2.3"); w.Code != 403 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := enrollWithCode(t, mux, deskCode('3'), "x", "192.0.2.3"); w.Code != 429 {
		t.Fatalf("no rate limit: %d", w.Code)
	}
}

func internalDesktopRequest(t *testing.T, mux http.Handler, method, path, account string, admin bool, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if account != "" {
		req.Header.Set("X-Internal-Key", "fixture-internal-key")
		req.Header.Set("X-Blerg-Account", account)
	}
	if admin {
		req.Header.Set("X-Blerg-Admin", "1")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestInternalDesktopRoutesListRenameAndRevokePerPerson(t *testing.T) {
	t.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", "false")
	enrollCodes = &enrollCodeLimiter{}
	fx := newPrivFixture(t)
	core := newDeskCore(t, fx.api)
	core.codes[deskCode('a')], core.codes[deskCode('b')] = deskPersonA, deskPersonB
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", fx.hub.ServeDaemon(spawnTokenDaemonTok, fx.pool))
	fx.api.RegisterDesktopRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var a, b issuedDesktopCredential
	_ = json.Unmarshal(enrollWithCode(t, mux, deskCode('a'), "laptop-a", "192.0.2.10").Body.Bytes(), &a)
	_ = json.Unmarshal(enrollWithCode(t, mux, deskCode('b'), "laptop-b", "192.0.2.11").Body.Bytes(), &b)
	connA := desktopConnect(t, srv.URL, a.Token, a.Binding.DesktopID, "laptop-a", true)
	desktopWait(t, fx.hub, a.Binding.DesktopID)

	list := func(account string, admin bool) map[string]desktopListView {
		w := internalDesktopRequest(t, mux, http.MethodGet, "/internal/desktops", account, admin, "")
		if w.Code != 200 {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var v struct {
			Desktops []desktopListView `json:"desktops"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		out := map[string]desktopListView{}
		for _, d := range v.Desktops {
			out[d.ID] = d
		}
		return out
	}
	if w := internalDesktopRequest(t, mux, http.MethodGet, "/internal/desktops", "", false, ""); w.Code != 401 {
		t.Fatalf("no internal key: %d", w.Code)
	}
	mine := list(deskPersonA, false)
	if len(mine) != 1 || mine[a.Binding.DesktopID].State != "enrolled" || mine[a.Binding.DesktopID].LastSeenAt == nil || !mine[a.Binding.DesktopID].CanRevoke {
		t.Fatalf("person A's list: %+v", mine)
	}
	if got := list(deskPersonB, false)[b.Binding.DesktopID]; got.State != "offline" {
		t.Fatalf("person B's offline desktop: %+v", got)
	}
	if all := list(deskPersonA, true); all[b.Binding.DesktopID].ID == "" || all[b.Binding.DesktopID].CanRename {
		t.Fatalf("admin list: %+v", all)
	}

	// Rename: own desktop only, kept across the daemon's own name.
	if w := internalDesktopRequest(t, mux, http.MethodPost, "/internal/desktops/"+b.Binding.DesktopID+"/rename", deskPersonA, true, `{"name":"mine now"}`); w.Code != 404 {
		t.Fatalf("renamed another person's desktop: %d", w.Code)
	}
	if w := internalDesktopRequest(t, mux, http.MethodPost, "/internal/desktops/"+a.Binding.DesktopID+"/rename", deskPersonA, false, `{"name":"Work laptop"}`); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	if got := list(deskPersonA, false)[a.Binding.DesktopID]; got.Name != "Work laptop" || got.HostName != "laptop-a" {
		t.Fatalf("renamed: %+v", got)
	}

	// Revoke, in core's two phases: a member cannot check or remove another person's desktop;
	// the owner can, and its connection closes.
	revoke := func(id, account string, admin bool, phase string, revision int64) (int, map[string]any) {
		t.Helper()
		w := internalDesktopRequest(t, mux, http.MethodPost, "/internal/desktops/"+id+"/revoke", account, admin, fmt.Sprintf(`{"phase":%q,"ownership_revision":%d}`, phase, revision))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	for _, phase := range []string{"check", "remove"} {
		if code, _ := revoke(b.Binding.DesktopID, deskPersonA, false, phase, b.Binding.Revision); code != 404 {
			t.Fatalf("member %s on another person's desktop: %d", phase, code)
		}
	}
	code, checked := revoke(a.Binding.DesktopID, deskPersonA, false, "check", 0)
	if code != 200 || checked["status"] != "authorized" {
		t.Fatalf("check: %d %v", code, checked)
	}
	if code, _ := revoke(a.Binding.DesktopID, deskPersonA, false, "remove", int64(checked["ownership_revision"].(float64))-1); code != 404 {
		t.Fatalf("stale revision removed: %d", code)
	}
	if code, out := revoke(a.Binding.DesktopID, deskPersonA, false, "remove", int64(checked["ownership_revision"].(float64))); code != 200 || out["removed"] != true {
		t.Fatalf("remove: %d %v", code, out)
	}
	_ = connA.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, raw, err := connA.ReadMessage(); err != nil {
			break
		} else if strings.Contains(string(raw), "bdt_") {
			t.Fatal("credential sent over the revoked connection")
		}
	}
	if fx.hub.GetDaemon(a.Binding.DesktopID) != nil {
		t.Fatal("revoked desktop still connected")
	}
	desktopConnect(t, srv.URL, a.Token, a.Binding.DesktopID, "laptop-a", false)
	if len(list(deskPersonA, false)) != 0 {
		t.Fatal("revoked desktop still listed as the person's")
	}
	// A retry after a lost answer still completes for the person who removed it (both phases),
	// and tells another member nothing.
	for _, phase := range []string{"check", "remove"} {
		if code, out := revoke(a.Binding.DesktopID, deskPersonA, false, phase, 0); code != 200 || out["removed"] != true {
			t.Fatalf("retry %s after removal: %d %v", phase, code, out)
		}
		if code, _ := revoke(a.Binding.DesktopID, deskPersonB, false, phase, 0); code != 404 {
			t.Fatalf("another member learned of the removal: %d", code)
		}
	}
	// An administrator can revoke anyone's desktop.
	if code, out := revoke(b.Binding.DesktopID, deskPersonA, true, "remove", b.Binding.Revision); code != 200 || out["removed"] != true {
		t.Fatalf("admin revoke: %d", code)
	}
	var revoked int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_enrollment_audit WHERE action='revoked'`).Scan(&revoked); err != nil || revoked != 2 {
		t.Fatalf("revoke audit: %d %v", revoked, err)
	}
	// Assigning is administrator-only on the internal route too.
	if w := internalDesktopRequest(t, mux, http.MethodPut, "/internal/desktops/"+b.Binding.DesktopID+"/owner", deskPersonA, false, `{}`); w.Code != 403 {
		t.Fatalf("member assignment: %d", w.Code)
	}
}

// An enrollment whose desktop audit cannot be written fails: no credential, no owner.
func TestCodeEnrollmentFailsWithoutItsAudit(t *testing.T) {
	t.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", "false")
	enrollCodes = &enrollCodeLimiter{}
	fx := newPrivFixture(t)
	core := newDeskCore(t, fx.api)
	core.codes[deskCode('9')] = deskPersonA
	if _, err := fx.pool.Exec(t.Context(), `CREATE FUNCTION refuse_code_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='code_enrolled' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$;
CREATE TRIGGER refuse_code_audit BEFORE INSERT ON desktop_enrollment_audit FOR EACH ROW EXECUTE FUNCTION refuse_code_audit();`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterDesktopRoutes(mux)
	w := enrollWithCode(t, mux, deskCode('9'), "audit-laptop", "192.0.2.20")
	if w.Code == 200 || strings.Contains(w.Body.String(), "bdt_") {
		t.Fatalf("enrolled without its audit: %d", w.Code)
	}
	var owned int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM daemons WHERE name='audit-laptop' AND desktop_owner_id IS NOT NULL`).Scan(&owned); err != nil || owned != 0 {
		t.Fatalf("desktop owned without its audit: %d %v", owned, err)
	}
}
