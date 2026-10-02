package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/api"
)

// With no internal key configured the exchange routes answer 503 rather than being open.
func TestInternalTokenExchangeRoutesDisabledWithoutKey(t *testing.T) {
	deps, _ := newInternalTestDeps(t)
	deps.InternalKey = ""
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	for _, p := range []string{"exchange", "exchange/revoke"} {
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

type exchangeResp struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

func (e *mcpEnv) exchange(t *testing.T, body map[string]any) (int, string) {
	t.Helper()
	code, out, _ := e.internal(t, "/internal/tokens/exchange", testInternalKey, body)
	return code, out
}

func (e *mcpEnv) exchangeOK(t *testing.T, body map[string]any) exchangeResp {
	t.Helper()
	code, out := e.exchange(t, body)
	if code != http.StatusOK {
		t.Fatalf("exchange = %d %s", code, out)
	}
	var r exchangeResp
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Token == "" || r.ExpiresAt == "" {
		t.Fatalf("bad exchange response %s (%v)", out, err)
	}
	return r
}

// verifyBoard runs the exact check the board applies to a core token: signature, audience and the
// current revocation snapshot (timestamp-aware).
func (e *mcpEnv) verifyBoard(t *testing.T, raw string) (cid.Principal, error) {
	t.Helper()
	ctx := context.Background()
	keys, err := e.idSvc.JWKS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := e.idSvc.RevocationSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set := cid.RevocationSet{}
	for _, r := range snap {
		set.Add(r.Kind, r.Value, r.RevokedAt)
	}
	return cid.Verify(raw, "blerg-board", keys, exchangeRevChecker{set}, func(string) bool { return false })
}

type exchangeRevChecker struct{ cid.RevocationSet }

func (exchangeRevChecker) StaleBeyondCeiling() bool { return false }

func TestInternalTokenExchangeIssuesScopedShortLivedToken(t *testing.T) {
	e := newTokEnv(t)
	r := e.exchangeOK(t, map[string]any{
		"account_id": e.acctA, "session_id": e.sidA, "target": "board", "board_id": "board-7", "session_ref": "run-123",
	})
	p, err := e.verifyBoard(t, r.Token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.Kind != "agent" || p.Project != "board-7" || p.OnBehalfOf != e.acctA || p.Lineage != e.acctA {
		t.Errorf("claims kind/project/obo/lineage = %q/%q/%q/%q", p.Kind, p.Project, p.OnBehalfOf, p.Lineage)
	}
	if len(p.Caps) != 2 || !p.Has("card.read") || !p.Has("card.write") || p.Has("column.write") {
		t.Errorf("caps = %v", p.Caps)
	}
	if d := time.Until(time.Unix(p.ExpiresAt, 0)); d < 9*time.Minute || d > 10*time.Minute+5*time.Second {
		t.Errorf("token exp in %v, want ~10m", d)
	}
	at, err := time.Parse(time.RFC3339, r.ExpiresAt)
	if err != nil || at.Unix() != p.ExpiresAt {
		t.Errorf("expires_at %q (%v) does not match the token exp %d", r.ExpiresAt, err, p.ExpiresAt)
	}
	// The row exists with kind exchange and the token's sub.
	var kind string
	if err := e.pool.QueryRow(context.Background(), `SELECT kind FROM agent_tokens WHERE id = $1 AND account_id = $2`, p.Sub, e.acctA).Scan(&kind); err != nil || kind != "exchange" {
		t.Fatalf("row kind = %q err=%v", kind, err)
	}
}

func TestInternalTokenExchangeProofRules(t *testing.T) {
	e := newTokEnv(t)
	base := func(extra map[string]any) map[string]any {
		b := map[string]any{"account_id": e.acctA, "target": "board", "board_id": "b1"}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	plain, _, err := e.idSvc.CreateAgentToken(context.Background(), e.acctA, "real", "board", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cronID := e.mintOK(t, e.acctA, e.sidA)

	// Accepted proofs: a live session, a live ordinary token, a live cron token.
	for name, b := range map[string]map[string]any{
		"session":        base(map[string]any{"session_id": e.sidA}),
		"ordinary token": base(map[string]any{"token_id": plain.ID}),
		"cron token":     base(map[string]any{"token_id": cronID}),
	} {
		if code, out := e.exchange(t, b); code != http.StatusOK {
			t.Errorf("%s proof = %d %s, want 200", name, code, out)
		}
	}

	// Exactly one proof, well formed: 400.
	for name, b := range map[string]map[string]any{
		"no proof":  base(nil),
		"both":      base(map[string]any{"session_id": e.sidA, "token_id": plain.ID}),
		"bad sid":   base(map[string]any{"session_id": "nope"}),
		"bad tid":   base(map[string]any{"token_id": "nope"}),
		"bad acct":  {"account_id": "nope", "session_id": e.sidA, "target": "board", "board_id": "b1"},
		"no target": {"account_id": e.acctA, "session_id": e.sidA, "board_id": "b1"},
	} {
		if code, _ := e.exchange(t, b); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}

	// Uniform 404: dead, foreign, unknown proofs.
	if err := e.idSvc.RevokeAgentToken(context.Background(), e.acctA, plain.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE agent_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1`, cronID); err != nil {
		t.Fatal(err)
	}
	live, _, err := e.idSvc.CreateExchangeToken(context.Background(), e.acctA, "board", "b1", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]map[string]any{
		"other account's session": base(map[string]any{"session_id": e.sidB}),
		"unknown session":         base(map[string]any{"session_id": noSuchSession}),
		"revoked token":           base(map[string]any{"token_id": plain.ID}),
		"expired cron token":      base(map[string]any{"token_id": cronID}),
		"unknown token":           base(map[string]any{"token_id": noSuchSession}),
		"an exchange token":       base(map[string]any{"token_id": live.ID}),
	} {
		if code, _ := e.exchange(t, b); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", name, code)
		}
	}
	// Another account's live token as proof for this account.
	otherTok, _, err := e.idSvc.CreateAgentToken(context.Background(), e.acctB, "b", "board", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := e.exchange(t, base(map[string]any{"token_id": otherTok.ID})); code != http.StatusNotFound {
		t.Errorf("cross-account token proof = %d, want 404", code)
	}
}

// A proof token can never yield more than it holds: a read-only token gets a read-only exchange
// token, a board token gets no column.write, and a cron token (session.start) delegates the target.
func TestInternalTokenExchangeCapsFollowTheProof(t *testing.T) {
	e := newTokEnv(t)
	ctx := context.Background()
	readOnly, _, err := e.idSvc.CreateAgentToken(ctx, e.acctA, "ro", "platform", time.Hour) // card.read only
	if err != nil {
		t.Fatal(err)
	}
	boardTok, _, err := e.idSvc.CreateAgentToken(ctx, e.acctA, "bd", "board", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cronID := e.mintOK(t, e.acctA, e.sidA)
	for name, tc := range map[string]struct {
		proof map[string]any
		want  []string
	}{
		"read-only token": {map[string]any{"token_id": readOnly.ID}, []string{"card.read"}},
		"board token":     {map[string]any{"token_id": boardTok.ID}, []string{"card.read", "card.write"}},
		"cron token":      {map[string]any{"token_id": cronID}, []string{"card.read", "card.write"}},
		"human session":   {map[string]any{"session_id": e.sidA}, []string{"card.read", "card.write"}},
	} {
		body := map[string]any{"account_id": e.acctA, "target": "board", "board_id": "b1"}
		for k, v := range tc.proof {
			body[k] = v
		}
		p, err := e.verifyBoard(t, e.exchangeOK(t, body).Token)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(p.Caps) != len(tc.want) {
			t.Errorf("%s: caps = %v, want %v", name, p.Caps, tc.want)
			continue
		}
		for _, c := range tc.want {
			if !p.Has(c) {
				t.Errorf("%s: caps = %v, want %v", name, p.Caps, tc.want)
			}
		}
	}
}

func TestInternalTokenExchangeValidation(t *testing.T) {
	e := newTokEnv(t)
	for name, b := range map[string]map[string]any{
		"unknown target": {"target": "runner", "board_id": "b"},
		"empty target":   {"target": "", "board_id": "b"},
		"no board":       {"target": "board"},
		"long board":     {"target": "board", "board_id": strings.Repeat("x", 65)},
		"control board":  {"target": "board", "board_id": "a\nb"},
		"long ref":       {"target": "board", "board_id": "b", "session_ref": strings.Repeat("x", 129)},
		"control ref":    {"target": "board", "board_id": "b", "session_ref": "a\x00b"},
	} {
		b["account_id"], b["session_id"] = e.acctA, e.sidA
		if code, _ := e.exchange(t, b); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	if code, _ := e.exchange(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "target": "board", "board_id": strings.Repeat("x", 64)}); code != http.StatusOK {
		t.Errorf("64 char board id = %d, want 200", code)
	}
}

func TestInternalTokenExchangeRevokeAndLogOutEverywhere(t *testing.T) {
	e := newTokEnv(t)
	req := map[string]any{"account_id": e.acctA, "session_id": e.sidA, "target": "board", "board_id": "b1"}
	r1 := e.exchangeOK(t, req)
	p1, err := e.verifyBoard(t, r1.Token)
	if err != nil {
		t.Fatal(err)
	}
	revoke := func(acct, sub string) int {
		code, _, _ := e.internal(t, "/internal/tokens/exchange/revoke", testInternalKey, map[string]any{"account_id": acct, "token_sub": sub})
		return code
	}
	// Cross-account, malformed, an ordinary token and a cron token: refused, token untouched.
	plain, _, err := e.idSvc.CreateAgentToken(context.Background(), e.acctA, "real", "board", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cronID := e.mintOK(t, e.acctA, e.sidA)
	if c := revoke(e.acctB, p1.Sub); c != 404 {
		t.Errorf("cross-account revoke = %d, want 404", c)
	}
	if c := revoke(e.acctA, plain.ID); c != 404 {
		t.Errorf("revoking an ordinary token = %d, want 404", c)
	}
	if c := revoke(e.acctA, cronID); c != 404 {
		t.Errorf("revoking a cron token = %d, want 404", c)
	}
	if c := revoke(e.acctA, noSuchSession); c != 404 {
		t.Errorf("unknown sub = %d, want 404", c)
	}
	if c := revoke(e.acctA, "nope"); c != 400 {
		t.Errorf("malformed sub = %d, want 400", c)
	}
	if c := revoke("nope", p1.Sub); c != 400 {
		t.Errorf("malformed account = %d, want 400", c)
	}
	if _, err := e.verifyBoard(t, r1.Token); err != nil {
		t.Fatalf("token died from refused revokes: %v", err)
	}
	if _, st := e.status(t, e.acctA, plain.ID); st["live"] != true {
		t.Fatal("ordinary token was revoked through the exchange route")
	}

	for i := 0; i < 2; i++ { // idempotent
		if c := revoke(e.acctA, p1.Sub); c != 200 {
			t.Fatalf("revoke #%d = %d, want 200", i, c)
		}
	}
	if _, err := e.verifyBoard(t, r1.Token); !errors.Is(err, cid.ErrRevoked) {
		t.Fatalf("revoked token verify err = %v, want ErrRevoked", err)
	}

	// Log out everywhere kills a live one.
	r2 := e.exchangeOK(t, req)
	if _, err := e.verifyBoard(t, r2.Token); err != nil {
		t.Fatal(err)
	}
	if err := e.idSvc.RevokeAccountEverywhere(context.Background(), e.acctA); err != nil {
		t.Fatal(err)
	}
	if _, err := e.verifyBoard(t, r2.Token); !errors.Is(err, cid.ErrRevoked) {
		t.Fatalf("after log-out-everywhere verify err = %v, want ErrRevoked", err)
	}
}

func TestInternalTokenExchangeHiddenAndNotCounted(t *testing.T) {
	e := newTokEnv(t)
	req := map[string]any{"account_id": e.acctA, "session_id": e.sidA, "target": "board", "board_id": "b1"}
	var sub string
	for i := 0; i < 5; i++ {
		r := e.exchangeOK(t, req)
		p, err := e.verifyBoard(t, r.Token)
		if err != nil {
			t.Fatal(err)
		}
		sub = p.Sub
	}
	code, body := e.do(t, http.MethodGet, "/api/tokens", e.humanA, nil, nil)
	if code != http.StatusOK || strings.Contains(body, sub) || strings.Contains(body, "exchange") {
		t.Fatalf("GET /api/tokens = %d lists exchange tokens: %s", code, body)
	}
	// All 50 user slots stay available; the 51st is refused.
	for i := 0; i < 50; i++ {
		if c, out := e.mint(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "name": "c"}); c != http.StatusOK {
			t.Fatalf("mint #%d = %d %s", i, c, out)
		}
	}
	if c, _ := e.mint(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "name": "c"}); c != http.StatusConflict {
		t.Fatalf("51st mint = %d, want 409", c)
	}
	// And a full user cap does not block exchanging.
	e.exchangeOK(t, req)
}

// The route-group table: bad/missing key, public tokens as key, wrong method, no CORS, and no row
// created by any refused call.
func TestInternalTokenExchangeRoutesAuthMatrix(t *testing.T) {
	e := newTokEnv(t)
	first := e.exchangeOK(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "target": "board", "board_id": "b1"})
	p, _ := e.verifyBoard(t, first.Token)
	routes := []struct {
		name, path string
		body       map[string]any
	}{
		{"exchange", "/internal/tokens/exchange", map[string]any{"account_id": e.acctA, "session_id": e.sidA, "target": "board", "board_id": "b1"}},
		{"exchange revoke", "/internal/tokens/exchange/revoke", map[string]any{"account_id": e.acctA, "token_sub": p.Sub}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			before := e.countTokens(t, e.acctA)
			for label, key := range map[string]string{"missing": "", "wrong": "not-the-key", "prefix": testInternalKey[:len(testInternalKey)-1]} {
				code, out, _ := e.internal(t, rt.path, key, rt.body)
				if code != http.StatusUnauthorized || strings.Contains(out, "eyJ") {
					t.Errorf("%s key = %d, want 401 and no token", label, code)
				}
			}
			for label, key := range map[string]string{"human": e.humanA, "agent": e.agentA} {
				if code, _, _ := e.internal(t, rt.path, key, rt.body); code != http.StatusUnauthorized {
					t.Errorf("%s token as key = %d, want 401", label, code)
				}
			}
			if code, _ := e.do(t, http.MethodGet, rt.path, "", nil, map[string]string{"X-Internal-Key": testInternalKey}); code == http.StatusOK {
				t.Errorf("GET = %d", code)
			}
			if after := e.countTokens(t, e.acctA); after != before {
				t.Errorf("refused calls changed rows: %d -> %d", before, after)
			}
			_, _, hdr := e.internal(t, rt.path, testInternalKey, rt.body)
			if hdr.Get("Access-Control-Allow-Origin") != "" {
				t.Error("CORS header set on an internal route")
			}
		})
	}
	// Another account's object: 404 on both routes.
	if code, _ := e.exchange(t, map[string]any{"account_id": e.acctB, "session_id": e.sidA, "target": "board", "board_id": "b1"}); code != http.StatusNotFound {
		t.Errorf("exchange with another account's session = %d, want 404", code)
	}
	if code, _, _ := e.internal(t, "/internal/tokens/exchange/revoke", testInternalKey, map[string]any{"account_id": e.acctB, "token_sub": p.Sub}); code != http.StatusNotFound {
		t.Errorf("revoke of another account's exchange token = %d, want 404", code)
	}
}
