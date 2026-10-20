package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// testBoardMintKey is the board's own key for the "Run as me" mint. It is deliberately a
// different value from testInternalKey: the two keys must not open each other's routes.
const testBoardMintKey = "test-board-mint-key-do-not-use-in-prod"

const boardMintPath = "/internal/tokens/board-automation"

// newBoardMintEnv is the MCP test environment plus the board mint key and a generous limiter.
func newBoardMintEnv(t *testing.T, limit int) *mcpEnv {
	t.Helper()
	deps, pool := newInternalTestDeps(t)
	deps.BoardMintKey = testBoardMintKey
	deps.BoardMintLimiter = api.NewLoginLimiter(limit, time.Hour)
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	idSvc := deps.Identity.(*identity.Service)
	e := &mcpEnv{deps: deps, pool: pool, srv: srv, idSvc: idSvc}
	e.acctA = insertCredAccount(t, pool, "mint-a")
	e.acctB = insertCredAccount(t, pool, "mint-b")
	e.sidA = insertLiveHumanSession(t, pool, e.acctA)
	e.sidB = insertLiveHumanSession(t, pool, e.acctB)
	return e
}

// boardHuman is the access token a person's browser holds for the board: audience blerg-board,
// kind human, carrying the browser session's sid. It is what the board forwards as the proof.
func (e *mcpEnv) boardHuman(t *testing.T, account, sid string) string {
	t.Helper()
	tok, err := e.idSvc.MintHumanAccessTokenForSession(context.Background(), account, "blerg-board", sid)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type boardMintResp struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	Name      string `json:"name"`
	ExpiresAt string `json:"expires_at"`
}

func (e *mcpEnv) mintBody(account, accessToken string) map[string]any {
	return map[string]any{"account_id": account, "board_id": "b1", "board_name": "Work", "access_token": accessToken}
}

func (e *mcpEnv) liveTokenRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_tokens`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// verifyRunner runs the check the runner applies to a token it is handed.
func (e *mcpEnv) verifyRunner(t *testing.T, raw string) (cid.Principal, error) {
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
	return cid.Verify(raw, "blerg-runner", keys, exchangeRevChecker{set}, func(string) bool { return false })
}

// Without the board mint key configured both routes are 503, not open.
func TestBoardAutomationMintDisabledWithoutKey(t *testing.T) {
	deps, _ := newInternalTestDeps(t)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	for _, p := range []string{boardMintPath, boardMintPath + "/revoke"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+p, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+testInternalKey)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s without board key = %d, want 503", p, resp.StatusCode)
		}
	}
}

// The board key opens only the board mint routes, and the internal key does not open them.
func TestBoardAutomationMintKeySeparation(t *testing.T) {
	e := newBoardMintEnv(t, 100)
	body := e.mintBody(e.acctA, e.boardHuman(t, e.acctA, e.sidA))
	for _, key := range []string{"", testInternalKey, "wrong"} {
		if code, _, _ := e.internal(t, boardMintPath, key, body); code != http.StatusUnauthorized {
			t.Errorf("mint with key %q = %d, want 401", key, code)
		}
	}
	// A forwarded request is refused whatever key it carries: the route is component-to-component.
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+boardMintPath, strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+testBoardMintKey)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("forwarded mint = %d, want 401", resp.StatusCode)
	}
	// The board key is not an internal key: the credential fetch refuses it.
	if code, _, _ := e.internal(t, "/internal/credentials/fetch", testBoardMintKey,
		map[string]any{"account_id": e.acctA, "engine": "claude", "session_id": e.sidA}); code != http.StatusUnauthorized {
		t.Errorf("credential fetch with the board key = %d, want 401", code)
	}
	if n := e.liveTokenRows(t); n != 0 {
		t.Errorf("refused mints left %d token rows", n)
	}
}

func TestBoardAutomationMintIssuesRunSessionsTokenForTheLivePerson(t *testing.T) {
	e := newBoardMintEnv(t, 100)
	code, out, hdr := e.internal(t, boardMintPath, testBoardMintKey, e.mintBody(e.acctA, e.boardHuman(t, e.acctA, e.sidA)))
	if code != http.StatusOK {
		t.Fatalf("mint = %d %s", code, out)
	}
	if hdr.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", hdr.Get("Cache-Control"))
	}
	var r boardMintResp
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Token == "" || r.ID == "" {
		t.Fatalf("bad response %s (%v)", out, err)
	}
	p, err := e.verifyRunner(t, r.Token)
	if err != nil {
		t.Fatalf("minted token does not verify for the runner: %v", err)
	}
	if p.Kind != "agent" || p.Sub != r.ID || p.OnBehalfOf != e.acctA || !p.Has("session.start") || len(p.Caps) != 1 {
		t.Errorf("claims kind/sub/obo/caps = %q/%q/%q/%v", p.Kind, p.Sub, p.OnBehalfOf, p.Caps)
	}
	if d := time.Until(time.Unix(p.ExpiresAt, 0)); d < 89*24*time.Hour || d > 90*24*time.Hour+time.Minute {
		t.Errorf("token lives %v, want the default 90 days", d)
	}

	// It is in the person's own Agent tokens list, named for the board, with its source.
	list, err := e.idSvc.ListAgentTokens(context.Background(), e.acctA)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != r.ID || list[0].Name != "Board Work automation" || list[0].Source != "board b1" ||
		list[0].Aud != "blerg-runner" || r.Name != list[0].Name {
		t.Fatalf("token list = %+v", list)
	}
	// Over HTTP too: the Settings page reads GET /api/tokens.
	human, err := e.idSvc.MintHumanAccessToken(context.Background(), e.acctA, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}
	c, body := e.do(t, http.MethodGet, "/api/tokens", human, nil, nil)
	if c != http.StatusOK || !strings.Contains(body, `"source":"board b1"`) || strings.Contains(body, r.Token) {
		t.Errorf("GET /api/tokens = %d %s", c, body)
	}
	// And it can be revoked there like any other token.
	if c, _ := e.do(t, http.MethodDelete, "/api/tokens/"+r.ID, human, nil, nil); c != http.StatusOK {
		t.Errorf("revoke from Settings = %d", c)
	}
	if _, err := e.verifyRunner(t, r.Token); err == nil {
		t.Error("token still verifies after revoke from Settings")
	}
}

// A long board name is cut so the token's name stays within the 64-character limit.
func TestBoardAutomationMintNameFitsTheLimit(t *testing.T) {
	e := newBoardMintEnv(t, 100)
	body := e.mintBody(e.acctA, e.boardHuman(t, e.acctA, e.sidA))
	body["board_name"] = strings.Repeat("é", 200)
	code, out, _ := e.internal(t, boardMintPath, testBoardMintKey, body)
	if code != http.StatusOK {
		t.Fatalf("mint = %d %s", code, out)
	}
	var r boardMintResp
	_ = json.Unmarshal([]byte(out), &r)
	if n := len([]rune(r.Name)); n > 64 || !strings.HasPrefix(r.Name, "Board é") || !strings.HasSuffix(r.Name, " automation") {
		t.Errorf("name %q (%d runes)", r.Name, n)
	}
}

// Every proof that is not a live human browser session of the named account is the uniform 404,
// and none of them leaves a token behind.
func TestBoardAutomationMintProofRules(t *testing.T) {
	e := newBoardMintEnv(t, 100)
	ctx := context.Background()
	agentBoard, err := e.idSvc.MintAgentToken(ctx, identity.AgentTokenInput{
		Sub: "11111111-1111-1111-1111-111111111111", Aud: "blerg-board",
		OnBehalfOf: e.acctA, Lineage: e.acctA, Caps: []string{"card.read", "card.write", "session.start"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A runner session's own token: an agent token for the runner, acting for A.
	sessionTok, err := e.idSvc.MintAgentToken(ctx, identity.AgentTokenInput{
		Sub: "22222222-2222-2222-2222-222222222222", Aud: "blerg-runner",
		OnBehalfOf: e.acctA, Lineage: e.acctA, Caps: []string{"session.start"},
	})
	if err != nil {
		t.Fatal(err)
	}
	coreHuman, _ := e.idSvc.MintHumanAccessTokenForSession(ctx, e.acctA, "blerg-core", e.sidA)
	runnerHuman, _ := e.idSvc.MintHumanAccessTokenForSession(ctx, e.acctA, "blerg-runner", e.sidA)
	noSid, _ := e.idSvc.MintHumanAccessToken(ctx, e.acctA, "blerg-board")
	deadSid := insertLiveHumanSession(t, e.pool, e.acctA)
	deadTok := e.boardHuman(t, e.acctA, deadSid)
	if _, err := e.pool.Exec(ctx, `UPDATE human_sessions SET revoked_at = now() WHERE chain_id = $1`, deadSid); err != nil {
		t.Fatal(err)
	}
	expired, _ := e.idSvc.MintHumanAccessTokenForSession(ctx, e.acctA, "blerg-board", e.sidA)
	expired = expired[:len(expired)-4] + "AAAA" // a bad signature stands in for any token that does not verify
	tokenOfB := e.boardHuman(t, e.acctB, e.sidB)
	sidOfB := e.boardHuman(t, e.acctA, e.sidB) // A's token naming B's session

	cases := map[string]map[string]any{
		"agent token for the board":      e.mintBody(e.acctA, agentBoard),
		"runner session token":           e.mintBody(e.acctA, sessionTok),
		"human token for core":           e.mintBody(e.acctA, coreHuman),
		"human token for the runner":     e.mintBody(e.acctA, runnerHuman),
		"human token with no sid":        e.mintBody(e.acctA, noSid),
		"human token of a dead session":  e.mintBody(e.acctA, deadTok),
		"bad signature":                  e.mintBody(e.acctA, expired),
		"garbage":                        e.mintBody(e.acctA, "not-a-token"),
		"another account's token":        e.mintBody(e.acctA, tokenOfB),
		"own token, other account's sid": e.mintBody(e.acctA, sidOfB),
	}
	for name, body := range cases {
		if code, out, _ := e.internal(t, boardMintPath, testBoardMintKey, body); code != http.StatusNotFound {
			t.Errorf("%s: mint = %d %s, want 404", name, code, out)
		}
	}
	// A must-change-password account cannot mint before it has chosen a password.
	if _, err := e.pool.Exec(ctx, `UPDATE accounts SET must_change_password = true WHERE id = $1`, e.acctB); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := e.internal(t, boardMintPath, testBoardMintKey, e.mintBody(e.acctB, e.boardHuman(t, e.acctB, e.sidB))); code != http.StatusNotFound {
		t.Errorf("must-change-password: mint = %d %s, want 404", code, out)
	}
	if n := e.liveTokenRows(t); n != 0 {
		t.Errorf("refused mints left %d token rows", n)
	}
}

func TestBoardAutomationMintValidation(t *testing.T) {
	e := newBoardMintEnv(t, 100)
	tok := e.boardHuman(t, e.acctA, e.sidA)
	bad := map[string]map[string]any{
		"no account":       {"board_id": "b1", "board_name": "Work", "access_token": tok},
		"account not uuid": {"account_id": "acct", "board_id": "b1", "board_name": "Work", "access_token": tok},
		"no board":         {"account_id": e.acctA, "board_name": "Work", "access_token": tok},
		"board with slash": {"account_id": e.acctA, "board_id": "a/b", "board_name": "Work", "access_token": tok},
		"no board name":    {"account_id": e.acctA, "board_id": "b1", "access_token": tok},
		"control in name":  {"account_id": e.acctA, "board_id": "b1", "board_name": "Wo\x00rk", "access_token": tok},
		"no access token":  {"account_id": e.acctA, "board_id": "b1", "board_name": "Work"},
	}
	for name, body := range bad {
		if code, out, _ := e.internal(t, boardMintPath, testBoardMintKey, body); code != http.StatusBadRequest {
			t.Errorf("%s: mint = %d %s, want 400", name, code, out)
		}
	}
}

// Every attempt counts, per account: a fourth mint within the window is 429, and another account
// is unaffected.
func TestBoardAutomationMintRateLimited(t *testing.T) {
	e := newBoardMintEnv(t, 3)
	tok := e.boardHuman(t, e.acctA, e.sidA)
	for i := 0; i < 3; i++ {
		if code, out, _ := e.internal(t, boardMintPath, testBoardMintKey, e.mintBody(e.acctA, tok)); code != http.StatusOK {
			t.Fatalf("mint %d = %d %s", i, code, out)
		}
	}
	code, _, hdr := e.internal(t, boardMintPath, testBoardMintKey, e.mintBody(e.acctA, tok))
	if code != http.StatusTooManyRequests || hdr.Get("Retry-After") == "" {
		t.Errorf("fourth mint = %d (Retry-After %q), want 429 with Retry-After", code, hdr.Get("Retry-After"))
	}
	if code, out, _ := e.internal(t, boardMintPath, testBoardMintKey, e.mintBody(e.acctB, e.boardHuman(t, e.acctB, e.sidB))); code != http.StatusOK {
		t.Errorf("another account's mint = %d %s", code, out)
	}
	// Refused proofs count too, so the route is no free oracle.
	e2 := newBoardMintEnv(t, 2)
	for i := 0; i < 2; i++ {
		e2.internal(t, boardMintPath, testBoardMintKey, e2.mintBody(e2.acctA, "garbage"))
	}
	if code, _, _ := e2.internal(t, boardMintPath, testBoardMintKey, e2.mintBody(e2.acctA, e2.boardHuman(t, e2.acctA, e2.sidA))); code != http.StatusTooManyRequests {
		t.Errorf("after two refused proofs = %d, want 429", code)
	}
}

// The board key revokes only a token core minted for that board, of that account.
func TestBoardAutomationRevoke(t *testing.T) {
	e := newBoardMintEnv(t, 100)
	ctx := context.Background()
	_, out, _ := e.internal(t, boardMintPath, testBoardMintKey, e.mintBody(e.acctA, e.boardHuman(t, e.acctA, e.sidA)))
	var r boardMintResp
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.ID == "" {
		t.Fatalf("mint: %s", out)
	}
	settingsRec, _, err := e.idSvc.CreateAgentToken(ctx, e.acctA, "laptop", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	revoke := func(body map[string]any) int {
		code, _, _ := e.internal(t, boardMintPath+"/revoke", testBoardMintKey, body)
		return code
	}
	refused := map[string]map[string]any{
		"a Settings token":  {"account_id": e.acctA, "token_id": settingsRec.ID, "board_id": "b1"},
		"another board":     {"account_id": e.acctA, "token_id": r.ID, "board_id": "b2"},
		"another account":   {"account_id": e.acctB, "token_id": r.ID, "board_id": "b1"},
		"an unknown token":  {"account_id": e.acctA, "token_id": noSuchSession, "board_id": "b1"},
		"a malformed token": {"account_id": e.acctA, "token_id": "x", "board_id": "b1"},
	}
	for name, body := range refused {
		if code := revoke(body); code != http.StatusNotFound && code != http.StatusBadRequest {
			t.Errorf("revoke %s = %d, want 404/400", name, code)
		}
	}
	if code, _, _ := e.internal(t, boardMintPath+"/revoke", testInternalKey, map[string]any{"account_id": e.acctA, "token_id": r.ID, "board_id": "b1"}); code != http.StatusUnauthorized {
		t.Errorf("revoke with the internal key = %d, want 401", code)
	}
	if _, err := e.verifyRunner(t, r.Token); err != nil {
		t.Fatalf("token died from a refused revoke: %v", err)
	}
	ok := map[string]any{"account_id": e.acctA, "token_id": r.ID, "board_id": "b1"}
	if code := revoke(ok); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	if code := revoke(ok); code != http.StatusOK {
		t.Errorf("second revoke = %d, want 200 (idempotent)", code)
	}
	if _, err := e.verifyRunner(t, r.Token); err == nil {
		t.Error("minted token still verifies after revoke")
	}
	var revokedAt *time.Time
	if err := e.pool.QueryRow(ctx, `SELECT revoked_at FROM agent_tokens WHERE id = $1`, settingsRec.ID).Scan(&revokedAt); err != nil || revokedAt != nil {
		t.Errorf("the Settings token was touched: revoked_at=%v err=%v", revokedAt, err)
	}
}
