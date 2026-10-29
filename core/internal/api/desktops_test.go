package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

type desktopsHarness struct {
	t      *testing.T
	srv    *httptest.Server
	key    string
	tokens map[string]string
}

func (h desktopsHarness) call(method, path, who, key string, body any) (int, []byte) {
	h.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(h.t.Context(), method, h.srv.URL+path, rd)
	if who != "" {
		req.Header.Set("Authorization", "Bearer "+h.tokens[who])
	}
	if key != "" {
		req.Header.Set("X-Internal-Key", key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, data
}

// Add a desktop (#193): one-time, short-lived, single-use codes bound to the person who made
// them, stored hashed, revocable, rate-limited and audited. Expired, reused, revoked and
// disabled-account codes are refused.
func TestDesktopEnrollmentCodesAreOneTimePersonalAndAudited(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.InternalKey = "fixture-desktop-internal-key"
	svc := deps.Identity.(*identity.Service)
	a, b := insertCredAccount(t, pool, "code-person-a"), insertCredAccount(t, pool, "code-person-b")
	tokens := map[string]string{}
	for name, id := range map[string]string{"a": a, "b": b} {
		tok, err := svc.MintHumanAccessTokenForSession(t.Context(), id, "blerg-core", insertLiveHumanSession(t, pool, id))
		if err != nil {
			t.Fatal(err)
		}
		tokens[name] = tok
	}
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	h := desktopsHarness{t, srv, deps.InternalKey, tokens}
	issue := func(who string) (string, string) {
		t.Helper()
		code, body := h.call(http.MethodPost, "/api/desktop-enrollment-codes", who, "", nil)
		var out struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		}
		if code != 200 || json.Unmarshal(body, &out) != nil || !strings.HasPrefix(out.Code, "bec_") || len(out.Code) != 44 {
			t.Fatalf("issue: %d, code present %v (body withheld: it may hold a code)", code, out.Code != "")
		}
		return out.ID, out.Code
	}
	redeem := func(code string) (int, string) {
		t.Helper()
		status, body := h.call(http.MethodPost, "/internal/desktop-enrollment-codes/redeem", "", deps.InternalKey, map[string]string{"code": code})
		var out struct {
			Owner string `json:"owner_id"`
		}
		_ = json.Unmarshal(body, &out)
		return status, out.Owner
	}

	idA, codeA := issue("a")
	var stored string
	if err := pool.QueryRow(t.Context(), `SELECT code_hash FROM desktop_enrollment_codes WHERE id=$1`, idA).Scan(&stored); err != nil || stored == codeA || strings.Contains(stored, codeA[4:]) {
		t.Fatal("code stored in clear text", err)
	}
	if status, body := h.call(http.MethodGet, "/api/desktop-enrollment-codes", "a", "", nil); status != 200 || !strings.Contains(string(body), idA) || strings.Contains(string(body), codeA) {
		t.Fatalf("list: %d, lists the code id %v, holds the code %v", status, strings.Contains(string(body), idA), strings.Contains(string(body), codeA))
	}
	if status, body := h.call(http.MethodGet, "/api/desktop-enrollment-codes", "b", "", nil); status != 200 || strings.Contains(string(body), idA) {
		t.Fatalf("person B sees A's codes: %d", status)
	}
	// Only the runner (internal key) can redeem; a person cannot.
	if status, _ := h.call(http.MethodPost, "/internal/desktop-enrollment-codes/redeem", "a", "", map[string]string{"code": codeA}); status != 401 {
		t.Fatalf("browser redeemed a code: %d", status)
	}
	if status, owner := redeem(codeA); status != 200 || owner != a {
		t.Fatalf("redeem: %d %q, want person A", status, owner)
	}
	if status, _ := redeem(codeA); status != 403 {
		t.Fatalf("reused code accepted: %d", status)
	}
	// Expired.
	idExp, codeExp := issue("a")
	if _, err := pool.Exec(t.Context(), `UPDATE desktop_enrollment_codes SET expires_at=now()-interval '1 second' WHERE id=$1`, idExp); err != nil {
		t.Fatal(err)
	}
	if status, _ := redeem(codeExp); status != 403 {
		t.Fatalf("expired code accepted: %d", status)
	}
	// Revoked: only by the person who made it.
	idRev, codeRev := issue("a")
	if status, _ := h.call(http.MethodDelete, "/api/desktop-enrollment-codes/"+idRev, "b", "", nil); status != 404 {
		t.Fatalf("person B revoked A's code: %d", status)
	}
	if status, _ := h.call(http.MethodDelete, "/api/desktop-enrollment-codes/"+idRev, "a", "", nil); status != 200 {
		t.Fatalf("revoke: %d", status)
	}
	if status, _ := redeem(codeRev); status != 403 {
		t.Fatalf("revoked code accepted: %d", status)
	}
	// A code is the person's who made it: B's code never enrolls for A, and a disabled account's code is refused.
	_, codeB := issue("b")
	_, codeB2 := issue("b")
	if status, owner := redeem(codeB); status != 200 || owner != b {
		t.Fatalf("B's code: %d %q", status, owner)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET disabled_at=now() WHERE id=$1`, b); err != nil {
		t.Fatal(err)
	}
	if status, _ := redeem(codeB2); status != 403 {
		t.Fatalf("disabled account's code accepted: %d", status)
	}
	if status, _ := redeem("bec_" + strings.Repeat("0", 40)); status != 403 {
		t.Fatalf("unknown code accepted: %d", status)
	}
	// Rate limit: ten codes an hour per person (A made three above).
	for range 7 {
		issue("a")
	}
	if status, _ := h.call(http.MethodPost, "/api/desktop-enrollment-codes", "a", "", nil); status != 429 {
		t.Fatalf("no issue limit: %d", status)
	}
	// Audit: who and what, never the code.
	rows, err := pool.Query(t.Context(), `SELECT actor_id,action,subject_id FROM desktop_audit ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]int{}
	for rows.Next() {
		var actor, action, subject string
		if err := rows.Scan(&actor, &action, &subject); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(subject, "bec_") {
			t.Fatal("audit holds a code")
		}
		actions[action]++
	}
	rows.Close()
	if actions["code_issued"] != 12 || actions["code_redeemed"] != 2 || actions["code_refused"] < 3 || actions["code_revoked"] != 1 {
		t.Fatalf("audit: %v", actions)
	}
}

// Your desktops lists and changes desktops through the runner's internal routes, for the
// signed-in person only; revoking also revokes the desktop's core credentials.
func TestDesktopsRelayToRunnerForThePerson(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.InternalKey = "fixture-desktop-internal-key"
	svc := deps.Identity.(*identity.Service)
	a := insertCredAccount(t, pool, "desk-relay-a")
	admin := insertCredAccount(t, pool, "desk-relay-admin")
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET role='admin' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for name, id := range map[string]string{"a": a, "admin": admin} {
		tok, err := svc.MintHumanAccessTokenForSession(t.Context(), id, "blerg-core", insertLiveHumanSession(t, pool, id))
		if err != nil {
			t.Fatal(err)
		}
		tokens[name] = tok
	}
	desktop := "00000000-0000-4000-8000-000000000193"
	var mu sync.Mutex
	var calls []string
	removed := false
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path+" "+r.Header.Get("X-Blerg-Account")+" "+r.Header.Get("X-Blerg-Admin"))
		mu.Unlock()
		if r.Header.Get("X-Internal-Key") != deps.InternalKey {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/revoke") {
			_, _ = w.Write(fakeRunnerRevoke(r, &removed))
			return
		}
		_, _ = w.Write([]byte(`{"desktops":[{"id":"` + desktop + `","name":"laptop","state":"enrolled"}],"can_manage_others":false}`))
	}))
	t.Cleanup(runner.Close)
	h := desktopsHarness{t, nil, deps.InternalKey, tokens}
	h.srv = httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(h.srv.Close)
	if status, body := h.call(http.MethodGet, "/api/desktops", "a", "", nil); status != 503 || !strings.Contains(string(body), "BLERG_CORE_RUNNER_URL") {
		t.Fatalf("unconfigured runner: %d %s", status, body)
	}
	deps.RunnerURL = runner.URL
	h.srv.Close()
	h.srv = httptest.NewServer(api.NewRouter(deps))
	if status, body := h.call(http.MethodGet, "/api/desktops", "a", "", nil); status != 200 || !strings.Contains(string(body), desktop) || !strings.Contains(string(body), `"owners":[]`) {
		t.Fatalf("list: %d %s", status, body)
	}
	if status, body := h.call(http.MethodGet, "/api/desktops", "admin", "", nil); status != 200 || !strings.Contains(string(body), "desk-relay-a") {
		t.Fatalf("admin list has no owner names: %d %s", status, body)
	}
	if status, _ := h.call(http.MethodPut, "/api/desktops/"+desktop+"/owner", "a", "", map[string]any{"owner_id": a}); status != 403 {
		t.Fatalf("member assignment: %d", status)
	}
	if _, _, err := svc.IssueDesktopToken(t.Context(), a, desktop, 1); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.call(http.MethodDelete, "/api/desktops/"+desktop, "a", "", nil); status != 200 {
		t.Fatalf("revoke: %d", status)
	}
	var live int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_tokens WHERE desktop_id=$1 AND revoked_at IS NULL`, desktop).Scan(&live); err != nil || live != 0 {
		t.Fatalf("credentials survived revoke: %d %v", live, err)
	}
	mu.Lock()
	defer mu.Unlock()
	revoke := "POST /internal/desktops/" + desktop + "/revoke " + a + " "
	want := []string{"GET /internal/desktops " + a + " ", "GET /internal/desktops " + admin + " 1", revoke, revoke}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("runner calls:\n%v\nwant\n%v", calls, want)
	}
	if status, _ := h.call(http.MethodGet, "/api/desktops", "", "", nil); status != 401 {
		t.Fatalf("anonymous list: %d", status)
	}
}

// fakeRunnerRevoke models the runner's two idempotent removal phases for one desktop.
func fakeRunnerRevoke(r *http.Request, removed *bool) []byte {
	var body struct {
		Phase    string `json:"phase"`
		Revision int64  `json:"ownership_revision"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if *removed {
		return []byte(`{"status":"revoked","removed":true}`)
	}
	if body.Phase == "remove" && body.Revision == 3 {
		*removed = true
		return []byte(`{"status":"revoked","removed":true}`)
	}
	return []byte(`{"status":"authorized","ownership_revision":3}`)
}

// Remove recovers on retry whichever step failed: core's credential revocation, the runner's
// removal, or the answer after the runner already removed it.
func TestDesktopRemoveRecoversOnRetry(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.InternalKey = "fixture-desktop-internal-key"
	svc := deps.Identity.(*identity.Service)
	a := insertCredAccount(t, pool, "desk-remove-a")
	tok, err := svc.MintHumanAccessTokenForSession(t.Context(), a, "blerg-core", insertLiveHumanSession(t, pool, a))
	if err != nil {
		t.Fatal(err)
	}
	desktop := "00000000-0000-4000-8000-000000000194"
	var mu sync.Mutex
	removed, failRemove := false, false
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		if failRemove && strings.Contains(string(raw), `"remove"`) {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"database down"}`))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		_, _ = w.Write(fakeRunnerRevoke(r, &removed))
	}))
	t.Cleanup(runner.Close)
	deps.RunnerURL = runner.URL
	h := desktopsHarness{t, httptest.NewServer(api.NewRouter(deps)), deps.InternalKey, map[string]string{"a": tok}}
	t.Cleanup(h.srv.Close)
	live := func() int {
		var n int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_tokens WHERE desktop_id=$1 AND revoked_at IS NULL`, desktop).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, _, err := svc.IssueDesktopToken(t.Context(), a, desktop, 3); err != nil {
		t.Fatal(err)
	}
	// 1. Core's revocation fails: the desktop is not removed; the retry finishes both.
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION refuse_revoke() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'unavailable'; END $$;
CREATE TRIGGER refuse_revoke BEFORE UPDATE ON desktop_tokens FOR EACH ROW EXECUTE FUNCTION refuse_revoke();`); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.call(http.MethodDelete, "/api/desktops/"+desktop, "a", "", nil); status != 503 || removed || live() != 1 {
		t.Fatalf("failed revocation: %d removed=%v live=%d", status, removed, live())
	}
	if _, err := pool.Exec(t.Context(), `DROP TRIGGER refuse_revoke ON desktop_tokens`); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.call(http.MethodDelete, "/api/desktops/"+desktop, "a", "", nil); status != 200 || !removed || live() != 0 {
		t.Fatalf("retry after failed revocation: %d removed=%v live=%d", status, removed, live())
	}
	// 2. The runner's removal fails after core revoked: the retry completes it.
	removed = false
	if _, _, err := svc.IssueDesktopToken(t.Context(), a, desktop, 3); err != nil {
		t.Fatal(err)
	}
	failRemove = true
	if status, _ := h.call(http.MethodDelete, "/api/desktops/"+desktop, "a", "", nil); status != 503 || removed || live() != 0 {
		t.Fatalf("failed removal: %d removed=%v live=%d", status, removed, live())
	}
	failRemove = false
	if status, _ := h.call(http.MethodDelete, "/api/desktops/"+desktop, "a", "", nil); status != 200 || !removed {
		t.Fatalf("retry after failed removal: %d removed=%v", status, removed)
	}
	// 3. The runner removed it but its answer was lost: a credential issued meanwhile is still
	// revoked by the retry, which the runner answers "removed".
	if _, _, err := svc.IssueDesktopToken(t.Context(), a, desktop, 3); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.call(http.MethodDelete, "/api/desktops/"+desktop, "a", "", nil); status != 200 || live() != 0 {
		t.Fatalf("retry after a lost answer: %d live=%d", status, live())
	}
}
