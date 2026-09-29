package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// Every internal fetch must name WHICH live thing authorises it, and core verifies that specific
// session/token is live and the requested account's. Knowing an account's uuid is not enough,
// even when that account has a live browser session: that is the property this file pins.
func TestInternalCredentialsRequireANamedLiveProof(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	ctx := context.Background()
	idSvc := deps.Identity.(*identity.Service)

	victim := insertCredAccount(t, pool, "proof-victim")
	attacker := insertCredAccount(t, pool, "proof-attacker")
	noCred := insertCredAccount(t, pool, "proof-nocred")
	if err := deps.Credentials.Store(ctx, victim, "claude", []byte("sk-ant-victim-secret")); err != nil {
		t.Fatal(err)
	}

	victimSid := insertLiveHumanSession(t, pool, victim)
	attackerSid := insertLiveHumanSession(t, pool, attacker)
	noCredSid := insertLiveHumanSession(t, pool, noCred)
	revokedSid := insertLiveHumanSession(t, pool, victim)
	if _, err := pool.Exec(ctx, `UPDATE human_sessions SET revoked_at = now() WHERE id = $1`, revokedSid); err != nil {
		t.Fatal(err)
	}
	expiredSid := insertLiveHumanSession(t, pool, victim)
	if _, err := pool.Exec(ctx, `UPDATE human_sessions SET expires_at = now() - interval '1 minute' WHERE id = $1`, expiredSid); err != nil {
		t.Fatal(err)
	}
	victimTok, _, err := idSvc.CreateAgentToken(ctx, victim, "victim-tool", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	attackerTok, _, err := idSvc.CreateAgentToken(ctx, attacker, "attacker-tool", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}

	type body = map[string]string
	cases := []struct {
		name string
		req  body
		want int
	}{
		{"victim's own session", body{"account_id": victim, "session_id": victimSid}, 200},
		{"victim's own agent token", body{"account_id": victim, "token_id": victimTok.ID}, 200},
		// The exploit this closes: the attacker holds the internal key, knows the victim's uuid,
		// and the victim is logged in. Naming ANY other session or token does not help.
		{"the attacker's session for the victim's account", body{"account_id": victim, "session_id": attackerSid}, 404},
		{"the attacker's agent token for the victim's account", body{"account_id": victim, "token_id": attackerTok.ID}, 404},
		{"a session that does not exist", body{"account_id": victim, "session_id": noSuchSession}, 404},
		{"a revoked session", body{"account_id": victim, "session_id": revokedSid}, 404},
		{"an expired session", body{"account_id": victim, "session_id": expiredSid}, 404},
		{"no proof, though the account has a live session", body{"account_id": victim}, 400},
		{"both proofs", body{"account_id": victim, "session_id": victimSid, "token_id": victimTok.ID}, 400},
		{"a malformed session_id", body{"account_id": victim, "session_id": "not-a-uuid"}, 400},
		{"the removed human_session claim", body{"account_id": victim, "human_session": "true"}, 400},
	}
	for _, path := range []string{"/internal/credentials/fetch", "/internal/credentials/list"} {
		for _, tc := range cases {
			req := body{"engine": "claude"}
			for k, v := range tc.req {
				req[k] = v
			}
			resp := doInternal(t, srv, path, req)
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("%s: %s = %d (%s), want %d", path, tc.name, resp.StatusCode, b, tc.want)
			}
			if resp.StatusCode == 200 && strings.Contains(path, "fetch") {
				var out struct {
					PlaintextBase64 string `json:"plaintext_base64"`
				}
				_ = json.Unmarshal(b, &out)
				if plain, _ := base64.StdEncoding.DecodeString(out.PlaintextBase64); string(plain) != "sk-ant-victim-secret" {
					t.Errorf("%s: %s returned %q", path, tc.name, plain)
				}
			}
		}
	}

	// Uniform answers: an attacker cannot tell "no such session" from "someone else's session"
	// from "revoked" from "expired" from "logged in but never stored that credential" by status
	// or body. (An account with a credential-less live session gets the same 404 too.)
	var bodies []string
	for _, req := range []body{
		{"account_id": victim, "engine": "claude", "session_id": attackerSid},
		{"account_id": victim, "engine": "claude", "session_id": noSuchSession},
		{"account_id": victim, "engine": "claude", "session_id": revokedSid},
		{"account_id": victim, "engine": "claude", "session_id": expiredSid},
		{"account_id": victim, "engine": "claude", "token_id": attackerTok.ID},
		{"account_id": noCred, "engine": "claude", "session_id": noCredSid}, // valid proof, nothing stored
	} {
		resp := doInternal(t, srv, "/internal/credentials/fetch", req)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%v = %d, want 404", req, resp.StatusCode)
		}
		bodies = append(bodies, string(b))
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("404 bodies differ: %q vs %q — the refusal reason is observable", bodies[0], b)
		}
	}

	// Audit: exactly the two successful fetches (session + token) were logged, each against the
	// principal that authorised it, and no column of any row holds a credential value.
	rows, err := pool.Query(ctx, `SELECT fetched_by_session_id::text, fetched_by_token_id::text, to_jsonb(l)::text
		FROM credential_access_log l WHERE account_id = $1 ORDER BY fetched_at`, victim)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var sess, tok *string
		var whole string
		if err := rows.Scan(&sess, &tok, &whole); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(whole, "sk-ant") {
			t.Errorf("audit row holds a credential value: %s", whole)
		}
		switch n {
		case 0:
			if sess == nil || *sess != victimSid || tok != nil {
				t.Errorf("first audit row = session %v token %v, want session %s only", sess, tok, victimSid)
			}
		case 1:
			if tok == nil || *tok != victimTok.ID || sess != nil {
				t.Errorf("second audit row = session %v token %v, want token %s only", sess, tok, victimTok.ID)
			}
		}
		n++
	}
	if n != 2 {
		t.Errorf("credential_access_log has %d rows for the victim, want 2 (only successful fetches are logged)", n)
	}
}

// A disabled account's session stops authorising fetches even before it is revoked.
func TestInternalCredentialsRefuseDisabledAccountSession(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	acct := insertCredAccount(t, pool, "proof-disabled")
	if err := deps.Credentials.Store(context.Background(), acct, "claude", []byte("sk-ant-x")); err != nil {
		t.Fatal(err)
	}
	sid := insertLiveHumanSession(t, pool, acct)
	if _, err := pool.Exec(context.Background(), `UPDATE accounts SET disabled_at = now() WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	resp := doFetch(t, srv, "Bearer "+testInternalKey, acct, "claude", sid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled account's session = %d, want 404", resp.StatusCode)
	}
}
