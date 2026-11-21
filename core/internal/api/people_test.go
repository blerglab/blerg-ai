package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

func TestPeopleInviteAndDisable(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.AuthProvider = authprovider.NewLocal(db.NewPgStore(pool))
	svc := deps.Identity.(*identity.Service)
	deps.InternalKey = "people-fixture-key"
	disconnected := 0
	var attempts atomic.Int32
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/disable-desktops") || r.Header.Get("X-Internal-Key") != deps.InternalKey || r.Header.Get("X-Blerg-Admin") != "1" {
			w.WriteHeader(403)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/people/"), "/disable-desktops")
		var disabled bool
		if err := pool.QueryRow(t.Context(), `SELECT disabled_at IS NOT NULL FROM accounts WHERE id=$1`, id).Scan(&disabled); err != nil || !disabled {
			t.Error("runner called before account disable")
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		disconnected++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"disabled"}`))
	}))
	defer runner.Close()
	deps.RunnerURL = runner.URL
	admin := insertCredAccount(t, pool, "people-admin")
	member := insertCredAccount(t, pool, "people-member")
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET role='admin' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for name, id := range map[string]string{"admin": admin, "member": member} {
		tok, err := svc.MintHumanAccessTokenForSession(t.Context(), id, "blerg-core", insertLiveHumanSession(t, pool, id))
		if err != nil {
			t.Fatal(err)
		}
		tokens[name] = tok
	}
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	h := desktopsHarness{t, srv, "", tokens}
	if status, _ := h.call("POST", "/api/people", "member", "", map[string]string{"name": "New person", "email": "person@example.com"}); status != 403 {
		t.Fatalf("member create: %d", status)
	}
	status, body := h.call("POST", "/api/people", "admin", "", map[string]string{"name": "New person", "email": "person@example.com"})
	var invite struct {
		ID   string `json:"id"`
		Code string `json:"code"`
	}
	if status != 201 || json.Unmarshal(body, &invite) != nil || invite.Code == "" {
		t.Fatalf("invite status %d (body withheld)", status)
	}
	var role, stored string
	if err := pool.QueryRow(t.Context(), `SELECT role,code_hash FROM accounts JOIN person_invites ON account_id=accounts.id WHERE accounts.id=$1`, invite.ID).Scan(&role, &stored); err != nil {
		t.Fatal(err)
	}
	if role != "member" || stored == invite.Code {
		t.Fatal("role or secret storage")
	}
	status, body = h.call("GET", "/api/people", "admin", "", nil)
	if status != 200 || strings.Contains(string(body), invite.Code) || !strings.Contains(string(body), "New person") {
		t.Fatal("directory leaks or missing person")
	}
	// Expired invitations must not appear as pending invitations.
	if _, err := pool.Exec(t.Context(), `UPDATE person_invites SET expires_at=now()-interval '1 second' WHERE account_id=$1`, invite.ID); err != nil {
		t.Fatal(err)
	}
	people, err := svc.People(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, person := range people {
		if person.ID == invite.ID && person.Pending {
			t.Fatal("expired invite shown as invited")
		}
	}
	if _, err := pool.Exec(t.Context(), `UPDATE person_invites SET expires_at=now()+interval '1 day' WHERE account_id=$1`, invite.ID); err != nil {
		t.Fatal(err)
	}
	// Failed credential choices must not consume an invite.
	if status, _ := h.call("POST", "/auth/invite", "", "", map[string]string{"code": invite.Code, "provider_subject": "bad username", "password": "new-person-password"}); status != 400 {
		t.Fatalf("invalid username: %d", status)
	}
	if status, _ := h.call("POST", "/auth/invite", "", "", map[string]string{"code": invite.Code, "provider_subject": "new-person", "password": "short"}); status != 400 {
		t.Fatalf("weak password: %d", status)
	}
	redeem := map[string]string{"code": invite.Code, "provider_subject": "new-person", "password": "new-person-password"}
	if status, _ = h.call("POST", "/auth/invite", "", "", redeem); status != 200 {
		t.Fatalf("redeem %d", status)
	}
	if status, _ = h.call("POST", "/auth/invite", "", "", redeem); status != 403 {
		t.Fatalf("reused invite %d", status)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO desktop_tokens(account_id,desktop_id,ownership_revision,token_hash,expires_at) VALUES($1,gen_random_uuid(),1,'test-token-hash',now()+interval '1 day')`, invite.ID); err != nil {
		t.Fatal(err)
	}
	// A held session fence is retryable, not an authorization denial.
	svc.SetSessionLockTimeout(25 * time.Millisecond)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('human_sessions:' || $1::text, 0))`, invite.ID); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), "POST", srv.URL+"/api/people/"+invite.ID+"/disable", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tokens["admin"])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 503 || res.Header.Get("Retry-After") != "1" {
		t.Fatalf("busy disable: status=%d Retry-After=%q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	var disabled bool
	if err := pool.QueryRow(t.Context(), `SELECT disabled_at IS NOT NULL FROM accounts WHERE id=$1`, invite.ID).Scan(&disabled); err != nil || disabled {
		t.Fatalf("busy retry disabled person: disabled=%v err=%v", disabled, err)
	}

	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if status, _ = h.call("POST", "/api/people/"+invite.ID+"/disable", "admin", "", nil); status != 503 {
		t.Fatalf("runner unavailable: %d", status)
	}
	if status, _ = h.call("POST", "/api/people/"+invite.ID+"/disable", "admin", "", nil); status != 200 {
		t.Fatalf("disable %d", status)
	}
	if _, err := svc.MintHumanAccessTokenForSession(t.Context(), invite.ID, "blerg-core", insertLiveHumanSession(t, pool, invite.ID)); err == nil {
		t.Fatal("disabled person obtained a session token")
	}
	if disconnected != 1 {
		t.Fatalf("desktop invalidations %d", disconnected)
	}
	var events int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_audit WHERE subject_id=$1 AND action IN ('person_invited','person_invite_accepted','person_disabled')`, invite.ID).Scan(&events); err != nil || events != 3 {
		t.Fatalf("audit events %d %v", events, err)
	}
	var live int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_tokens WHERE account_id=$1 AND revoked_at IS NULL`, invite.ID).Scan(&live); err != nil || live != 0 {
		t.Fatalf("desktop tokens remain: %d %v", live, err)
	}
	if status, _ = h.call("POST", "/api/people/"+admin+"/disable", "admin", "", nil); status != 409 {
		t.Fatalf("self disable %d", status)
	}
}

func TestPeopleInviteRefusals(t *testing.T) {
	for _, mode := range []string{"expired", "disabled", "wrong-code"} {
		t.Run(mode, func(t *testing.T) {
			deps, pool := newTestDeps(t, nil, nil)
			deps.AuthProvider = authprovider.NewLocal(db.NewPgStore(pool))
			svc := deps.Identity.(*identity.Service)
			actor := insertCredAccount(t, pool, "invite-admin")
			if _, err := pool.Exec(t.Context(), `UPDATE accounts SET role='admin' WHERE id=$1`, actor); err != nil {
				t.Fatal(err)
			}
			sid := insertLiveHumanSession(t, pool, actor)
			id, code, err := svc.InvitePerson(t.Context(), actor, sid, "Person", "person@example.com")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "expired":
				_, err = pool.Exec(t.Context(), `UPDATE person_invites SET expires_at=now()-interval '1 second' WHERE account_id=$1`, id)
			case "disabled":
				err = svc.DisablePerson(t.Context(), actor, sid, id)
			case "wrong-code":
				code = strings.Repeat("x", 43)
			}
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(api.NewRouter(deps))
			defer srv.Close()
			h := desktopsHarness{t, srv, "", nil}
			if status, _ := h.call("POST", "/auth/invite", "", "", map[string]string{"code": code, "provider_subject": "person", "password": "person-password"}); status != 403 {
				t.Fatalf("refusal %d", status)
			}
		})
	}
}
