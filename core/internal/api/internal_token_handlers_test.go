package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/api"
)

// tokEnv reuses the MCP test environment (two accounts, live human sessions, an agent token).
func newTokEnv(t *testing.T) *mcpEnv { return newMCPEnv(t) }

func (e *mcpEnv) mint(t *testing.T, body map[string]any) (int, string) {
	t.Helper()
	code, out, _ := e.internal(t, "/internal/tokens/mint", testInternalKey, body)
	return code, out
}

func (e *mcpEnv) mintOK(t *testing.T, acct, sid string) string {
	t.Helper()
	code, out := e.mint(t, map[string]any{"account_id": acct, "session_id": sid, "name": "nightly", "expires_in_days": 30})
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("mint = %d %s", code, out)
	}
	var r map[string]any
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	id, _ := r["id"].(string)
	if id == "" {
		t.Fatalf("no id in %s", out)
	}
	return id
}

func (e *mcpEnv) status(t *testing.T, acct, id string) (int, map[string]any) {
	t.Helper()
	code, out, _ := e.internal(t, "/internal/tokens/status", testInternalKey, map[string]any{"account_id": acct, "token_id": id})
	var m map[string]any
	_ = json.Unmarshal([]byte(out), &m)
	return code, m
}

func TestInternalTokenMintReturnsOnlyIDAndExpiry(t *testing.T) {
	e := newTokEnv(t)
	code, out := e.mint(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "name": "nightly", "expires_in_days": 30})
	if code != http.StatusOK {
		t.Fatalf("mint = %d %s", code, out)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["id"] == nil || m["expires_at"] == nil {
		t.Fatalf("response keys = %v, want exactly id and expires_at: %s", m, out)
	}
	// Nothing JWT-shaped anywhere in the body.
	if strings.Contains(out, "eyJ") {
		t.Fatalf("response contains a JWT-looking value: %s", out)
	}
	var id, exp string
	_ = json.Unmarshal(m["id"], &id)
	_ = json.Unmarshal(m["expires_at"], &exp)
	at, err := time.Parse(time.RFC3339, exp)
	if err != nil || time.Until(at) < 29*24*time.Hour || time.Until(at) > 31*24*time.Hour {
		t.Fatalf("expires_at = %q (%v)", exp, err)
	}
	// It is a cron row, live, and hidden from the copyable list.
	var kind string
	if err := e.pool.QueryRow(context.Background(), `SELECT kind FROM agent_tokens WHERE id = $1`, id).Scan(&kind); err != nil || kind != "cron" {
		t.Fatalf("kind = %q err=%v", kind, err)
	}
	if code, body := e.do(t, http.MethodGet, "/api/tokens", e.humanA, nil, nil); code != 200 || strings.Contains(body, id) {
		t.Fatalf("GET /api/tokens = %d, lists the cron token: %s", code, body)
	}
	if c, st := e.status(t, e.acctA, id); c != 200 || st["live"] != true || st["account_id"] != e.acctA {
		t.Fatalf("status = %d %v", c, st)
	}
}

func TestInternalTokenMintProofRules(t *testing.T) {
	e := newTokEnv(t)
	// A token_id proof is refused (400, and creates nothing).
	agentID := e.mintOK(t, e.acctA, e.sidA)
	before := e.countTokens(t, e.acctA)
	if code, _ := e.mint(t, map[string]any{"account_id": e.acctA, "token_id": agentID, "name": "x"}); code != http.StatusBadRequest {
		t.Errorf("token_id proof = %d, want 400", code)
	}
	if code, _ := e.mint(t, map[string]any{"account_id": e.acctA, "token_id": agentID, "session_id": e.sidA, "name": "x"}); code != http.StatusBadRequest {
		t.Errorf("both proofs = %d, want 400", code)
	}
	if code, _ := e.mint(t, map[string]any{"account_id": e.acctA, "name": "x"}); code != http.StatusBadRequest {
		t.Errorf("no proof = %d, want 400", code)
	}
	// Another account's session, unknown session: uniform 404.
	if code, _ := e.mint(t, map[string]any{"account_id": e.acctA, "session_id": e.sidB, "name": "x"}); code != http.StatusNotFound {
		t.Errorf("other account's session = %d, want 404", code)
	}
	if code, _ := e.mint(t, map[string]any{"account_id": e.acctA, "session_id": noSuchSession, "name": "x"}); code != http.StatusNotFound {
		t.Errorf("unknown session = %d, want 404", code)
	}
	// Validation.
	for name, b := range map[string]map[string]any{
		"empty name": {"account_id": e.acctA, "session_id": e.sidA, "name": "  "},
		"long name":  {"account_id": e.acctA, "session_id": e.sidA, "name": strings.Repeat("x", 65)},
		"366 days":   {"account_id": e.acctA, "session_id": e.sidA, "name": "x", "expires_in_days": 366},
		"negative":   {"account_id": e.acctA, "session_id": e.sidA, "name": "x", "expires_in_days": -1},
	} {
		if code, _ := e.mint(t, b); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	if after := e.countTokens(t, e.acctA); after != before {
		t.Fatalf("rejected mints created rows: %d -> %d", before, after)
	}
}

func (e *mcpEnv) countTokens(t *testing.T, acct string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_tokens WHERE account_id = $1`, acct).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInternalTokenMintEnforcesCap(t *testing.T) {
	e := newTokEnv(t)
	for i := 0; i < 25; i++ {
		if _, _, err := e.idSvc.CreateAgentToken(context.Background(), e.acctA, "t", "run-sessions", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 25; i++ {
		e.mintOK(t, e.acctA, e.sidA)
	}
	code, out := e.mint(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "name": "x"})
	if code != http.StatusConflict || !strings.Contains(out, "limit 50") {
		t.Fatalf("51st = %d %s, want 409", code, out)
	}
	// The other account is unaffected.
	e.mintOK(t, e.acctB, e.sidB)
}

func (e *mcpEnv) revoke(t *testing.T, body map[string]any) (int, string) {
	t.Helper()
	code, out, _ := e.internal(t, "/internal/tokens/revoke", testInternalKey, body)
	return code, out
}

func TestInternalTokenRevoke(t *testing.T) {
	e := newTokEnv(t)
	id := e.mintOK(t, e.acctA, e.sidA)

	// Cross-account: B's session naming A's token, and the key path naming B's account.
	if code, _ := e.revoke(t, map[string]any{"account_id": e.acctB, "token_id": id, "session_id": e.sidB}); code != 404 {
		t.Errorf("session path cross-account = %d, want 404", code)
	}
	if code, _ := e.revoke(t, map[string]any{"account_id": e.acctB, "token_id": id}); code != 404 {
		t.Errorf("key path cross-account = %d, want 404", code)
	}
	// A dead/foreign session proof must NOT fall through to the key-only path.
	if code, _ := e.revoke(t, map[string]any{"account_id": e.acctA, "token_id": id, "session_id": e.sidB}); code != 404 {
		t.Errorf("foreign session proof = %d, want 404", code)
	}
	if code, _ := e.revoke(t, map[string]any{"account_id": e.acctA, "token_id": id, "session_id": noSuchSession}); code != 404 {
		t.Errorf("unknown session proof = %d, want 404", code)
	}
	if _, st := e.status(t, e.acctA, id); st["live"] != true {
		t.Fatal("token died from refused revokes")
	}

	// Malformed input.
	if code, _ := e.revoke(t, map[string]any{"account_id": e.acctA, "token_id": "nope"}); code != 400 {
		t.Errorf("bad token_id = %d, want 400", code)
	}
	if code, _ := e.revoke(t, map[string]any{"account_id": "nope", "token_id": id}); code != 400 {
		t.Errorf("bad account_id = %d, want 400", code)
	}

	// Key-only path revokes a cron token of that account, idempotently.
	for i := 0; i < 2; i++ {
		if code, out := e.revoke(t, map[string]any{"account_id": e.acctA, "token_id": id}); code != 200 {
			t.Fatalf("key-only revoke #%d = %d %s", i, code, out)
		}
	}
	if c, st := e.status(t, e.acctA, id); c != 200 || st["live"] != false {
		t.Fatalf("after revoke status = %d %v", c, st)
	}

	// Session path revokes too, idempotently.
	id2 := e.mintOK(t, e.acctA, e.sidA)
	for i := 0; i < 2; i++ {
		if code, out := e.revoke(t, map[string]any{"account_id": e.acctA, "token_id": id2, "session_id": e.sidA}); code != 200 {
			t.Fatalf("session revoke #%d = %d %s", i, code, out)
		}
	}
	if _, st := e.status(t, e.acctA, id2); st["live"] != false {
		t.Fatal("still live after session revoke")
	}
}

func TestInternalTokenRevokeKeyOnlyNeverTouchesOrdinaryTokens(t *testing.T) {
	e := newTokEnv(t)
	rec, raw, err := e.idSvc.CreateAgentToken(context.Background(), e.acctA, "real", "platform", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/me", raw, nil, nil); code != http.StatusOK {
		t.Fatalf("positive control: ordinary token on /api/me = %d, want 200", code)
	}
	if code, _ := e.revoke(t, map[string]any{"account_id": e.acctA, "token_id": rec.ID}); code != 404 {
		t.Fatalf("key-only revoke of an ordinary token = %d, want 404", code)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/me", raw, nil, nil); code == http.StatusUnauthorized {
		t.Fatal("ordinary token was revoked through the key-only path")
	}
	if _, st := e.status(t, e.acctA, rec.ID); st["live"] != true {
		t.Fatal("ordinary token not live after refused revoke")
	}
	// With the owner's live session it is revocable.
	if code, out := e.revoke(t, map[string]any{"account_id": e.acctA, "token_id": rec.ID, "session_id": e.sidA}); code != 200 {
		t.Fatalf("session revoke of ordinary token = %d %s", code, out)
	}
}

func TestInternalTokenStatus(t *testing.T) {
	e := newTokEnv(t)
	id := e.mintOK(t, e.acctA, e.sidA)

	if c, st := e.status(t, e.acctA, id); c != 200 || st["live"] != true || st["account_id"] != e.acctA {
		t.Fatalf("live status = %d %v", c, st)
	}
	// Another account's, unknown: the same 200 {"live": false} as a dead token of one's own, so a
	// 404 only ever means "this is not core" (a proxy, a rollback) and never "revoked".
	for name, c := range map[string]struct{ acct, tok string }{
		"other account": {e.acctB, id},
		"unknown id":    {e.acctA, noSuchSession},
	} {
		code, st := e.status(t, c.acct, c.tok)
		if code != 200 || st["live"] != false {
			t.Errorf("%s = %d %v, want 200 live:false", name, code, st)
		}
	}
	if c, _ := e.status(t, e.acctA, "nope"); c != 400 {
		t.Errorf("malformed id = %d, want 400", c)
	}

	// Expired => live:false.
	if _, err := e.pool.Exec(context.Background(), `UPDATE agent_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if c, st := e.status(t, e.acctA, id); c != 200 || st["live"] != false {
		t.Fatalf("expired status = %d %v", c, st)
	}

	// Log out everywhere => not live.
	id2 := e.mintOK(t, e.acctA, e.sidA)
	if err := e.idSvc.RevokeAccountEverywhere(context.Background(), e.acctA); err != nil {
		t.Fatal(err)
	}
	if c, st := e.status(t, e.acctA, id2); c != 200 || st["live"] != false {
		t.Fatalf("after log-out-everywhere = %d %v", c, st)
	}
}

// Nothing the mint produced can authenticate anywhere: no bearer value derivable from the row or
// the response verifies on the public API.
func TestCronTokenNothingPresentableOnTheWire(t *testing.T) {
	e := newTokEnv(t)
	code, out := e.mint(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "name": "c"})
	if code != 200 {
		t.Fatalf("mint = %d %s", code, out)
	}
	var r struct{ ID string }
	_ = json.Unmarshal([]byte(out), &r)
	var hash string
	if err := e.pool.QueryRow(context.Background(), `SELECT token_hash FROM agent_tokens WHERE id = $1`, r.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	for _, cand := range []string{r.ID, hash} {
		if code, _ := e.do(t, http.MethodGet, "/api/me", cand, nil, nil); code != http.StatusUnauthorized {
			t.Errorf("bearer %.12q... = %d, want 401", cand, code)
		}
	}
}

// The route-group table: bad/missing key, disabled key, wrong method, and no browser exposure.
func TestInternalTokenRoutesAuthMatrix(t *testing.T) {
	e := newTokEnv(t)
	id := e.mintOK(t, e.acctA, e.sidA)
	routes := []struct {
		name, path string
		body       map[string]any
	}{
		{"mint", "/internal/tokens/mint", map[string]any{"account_id": e.acctA, "session_id": e.sidA, "name": "x"}},
		{"revoke", "/internal/tokens/revoke", map[string]any{"account_id": e.acctA, "token_id": id}},
		{"status", "/internal/tokens/status", map[string]any{"account_id": e.acctA, "token_id": id}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			before := e.countTokens(t, e.acctA)
			for label, key := range map[string]string{"missing": "", "wrong": "not-the-key", "prefix": testInternalKey[:len(testInternalKey)-1]} {
				if code, _, _ := e.internal(t, rt.path, key, rt.body); code != http.StatusUnauthorized {
					t.Errorf("%s key = %d, want 401", label, code)
				}
			}
			// Public tokens are not the internal key.
			for label, key := range map[string]string{"human": e.humanA, "agent": e.agentA} {
				if code, _, _ := e.internal(t, rt.path, key, rt.body); code != http.StatusUnauthorized {
					t.Errorf("%s token as key = %d, want 401", label, code)
				}
			}
			// GET is not routed (even with the right key); no CORS headers on a real call.
			if code, _ := e.do(t, http.MethodGet, rt.path, "", nil, map[string]string{"X-Internal-Key": testInternalKey}); code == http.StatusOK {
				t.Errorf("GET = %d", code)
			}
			_, _, hdr := e.internal(t, rt.path, testInternalKey, rt.body)
			if hdr.Get("Access-Control-Allow-Origin") != "" {
				t.Error("CORS header set on an internal route")
			}
			if after := e.countTokens(t, e.acctA); rt.name != "mint" && after != before {
				t.Errorf("rows changed: %d -> %d", before, after)
			}
		})
	}
}

// With no internal key configured the routes answer 503 rather than being open.
func TestInternalTokenRoutesDisabledWithoutKey(t *testing.T) {
	deps, _ := newInternalTestDeps(t)
	deps.InternalKey = ""
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	for _, p := range []string{"mint", "revoke", "status"} {
		resp, err := srv.Client().Post(srv.URL+"/internal/tokens/"+p, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s without key = %d, want 503", p, resp.StatusCode)
		}
	}
}
