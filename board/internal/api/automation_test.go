package api_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

// identityRunner is the fake runner as the production driver behaves: it
// starts sessions only under a per-call identity (runner.IdentityStarter).
type identityRunner struct{ *fakeRunner }

func (identityRunner) RequiresStartIdentity() bool { return true }

// humanSub is the account id testHumanToken's tokens carry (testCoreToken
// sets Sub to "test-"+kind).
const humanSub = "test-human"

// agentToken mints a fake blerg-core agent token: aud, caps, owner and
// lifetime as given, signed by the fake core testServer wired.
func agentToken(t *testing.T, aud, onBehalfOf string, caps []string, ttl time.Duration) string {
	t.Helper()
	return signClaims(t, identity.Claims{
		Sub: "agent-token-" + onBehalfOf, Aud: aud, Kind: "agent", OnBehalfOf: onBehalfOf,
		Caps: caps, IssuedAt: time.Now().Add(-time.Minute).Unix(), ExpiresAt: time.Now().Add(ttl).Unix(),
	})
}

// runSessionsToken is what a person gets from core's run-sessions preset.
func runSessionsToken(t *testing.T) string {
	return agentToken(t, "blerg-runner", humanSub, []string{"session.start"}, 90*24*time.Hour)
}

func signClaims(t *testing.T, claims identity.Claims) string {
	t.Helper()
	hb, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": coreTestKID})
	pb, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	si := identity.EncodeSigningInput(hb, pb)
	return si + "." + signRaw(si)
}

// signRaw signs a JWT signing input with the fake core's key.
func signRaw(si string) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(corePriv, []byte(si)))
}

// runnerUnauthorized is what the production driver returns when the runner
// answers a start with 401.
func runnerUnauthorized() error {
	return fmt.Errorf("%w (HTTP 401)", runner.ErrStartUnauthorized)
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestBoardAutomationSettingsRoundTrip: the token is write-only (settable,
// never readable back through any board read), only a person can set one and
// only their own, and the engine is a closed enum.
func TestBoardAutomationSettingsRoundTrip(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	human := humanToken(t, srv)
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/boards/" + board.ID

	var got map[string]any
	decodeBody(t, request(t, srv, "GET", path, human, nil, nil), &got)
	if got["automation_token_set"] != false || got["automation_engine"] != "claude" {
		t.Fatalf("fresh board automation = set:%v engine:%v, want false/claude", got["automation_token_set"], got["automation_engine"])
	}
	if _, ok := got["automation_token"]; ok {
		t.Fatal("board read carries an automation_token key")
	}

	// engine is validated
	resp := request(t, srv, "PATCH", path, human, nil, map[string]any{"automation_engine": "gpt"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad engine: %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()

	// refusals, none of which may store anything
	for _, tc := range []struct {
		name, caller, token string
		want                int
	}{
		{"someone else's token", human, agentToken(t, "blerg-runner", "another-account", []string{"session.start"}, time.Hour), 422},
		{"no session.start", human, agentToken(t, "blerg-runner", humanSub, []string{"card.read"}, time.Hour), 422},
		{"board-audience token", human, agentToken(t, "blerg-board", humanSub, []string{"session.start"}, time.Hour), 422},
		{"expired", human, agentToken(t, "blerg-runner", humanSub, []string{"session.start"}, -time.Minute), 422},
		{"a human access token", human, human, 422},
		{"garbage", human, "not-a-token", 422},
		{"native service key", "svc-key-123", runSessionsToken(t), 403},
	} {
		resp := request(t, srv, "PATCH", path, tc.caller, nil, map[string]any{"automation_token": tc.token})
		body := readAll(t, resp)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, resp.StatusCode, body, tc.want)
		}
		if strings.Contains(body, tc.token) {
			t.Errorf("%s: refusal echoes the token", tc.name)
		}
	}
	if ba, _ := db.GetBoardAutomation(ctx, pool, board.ID); ba.Token != "" {
		t.Fatal("a refused PATCH stored a token")
	}

	// the owner sets their own, with an engine
	tok := runSessionsToken(t)
	resp = request(t, srv, "PATCH", path, human, nil, map[string]any{"automation_token": tok, "automation_engine": "hermes"})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set token: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, tok) {
		t.Fatal("PATCH response echoes the token")
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got["automation_token_set"] != true || got["automation_engine"] != "hermes" || got["automation_token_expires_at"] == nil {
		t.Fatalf("after set: %v", got)
	}
	if ba, _ := db.GetBoardAutomation(ctx, pool, board.ID); ba.Token != tok || ba.Engine != "hermes" {
		t.Fatalf("stored automation = %+v", ba)
	}
	// at rest it is sealed, never the raw token
	var stored string
	if err := pool.QueryRow(ctx, `SELECT automation_token FROM boards WHERE id = $1`, board.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "v1:") || strings.Contains(stored, tok) {
		t.Fatalf("stored automation_token is not sealed: %.12q...", stored)
	}
	// whose it is, is shown — and /api/me lets the UI say "you"
	if got["automation_account_id"] != humanSub {
		t.Fatalf("automation_account_id = %v, want the token owner %s", got["automation_account_id"], humanSub)
	}
	var me map[string]any
	decodeBody(t, request(t, srv, "GET", "/api/me", human, nil, nil), &me)
	if me["account_id"] != humanSub {
		t.Fatalf("/api/me account_id = %v", me["account_id"])
	}

	// atomic with the rest of the PATCH: a new token alongside a field that
	// fails validation is not stored
	other := agentToken(t, "blerg-runner", humanSub, []string{"session.start"}, 2*time.Hour)
	resp = request(t, srv, "PATCH", path, human, nil, map[string]any{"automation_token": other, "concurrency": -1})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("token + bad concurrency: %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
	if ba, _ := db.GetBoardAutomation(ctx, pool, board.ID); ba.Token != tok {
		t.Fatal("a PATCH refused for another field still replaced the token")
	}

	// no read path leaks it
	for _, p := range []string{path, "/api/boards", path + "/schema"} {
		if b := readAll(t, request(t, srv, "GET", p, human, nil, nil)); strings.Contains(b, tok) {
			t.Errorf("GET %s leaks the automation token", p)
		}
	}
	mcpBody := readAll(t, request(t, srv, "POST", "/mcp", human, nil, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "blerg_board_get", "arguments": map[string]any{"board_id": board.ID}},
	}))
	if strings.Contains(mcpBody, tok) {
		t.Error("MCP board get leaks the automation token")
	}

	// "" clears it (any board admin may)
	resp = request(t, srv, "PATCH", path, "svc-key-123", nil, map[string]any{"automation_token": ""})
	decodeBody(t, resp, &got)
	if got["automation_token_set"] != false || got["automation_token_expires_at"] != nil || got["automation_account_id"] != nil {
		t.Fatalf("after clear: %v", got)
	}
}

// startSite is one way a session gets started on a board. run triggers it and
// reports whether it was refused with a clear, visible automation reason.
type startSite struct {
	name  string
	setup func(t *testing.T, env *startEnv)
	run   func(t *testing.T, env *startEnv) (visibleReason string)
}

type startEnv struct {
	pool    *pgxpool.Pool
	human   string
	board   db.Board
	cols    []db.Column
	card    db.Card
	request func(method, path, token string, body any) *http.Response
}

func cardComments(t *testing.T, pool *pgxpool.Pool, cardID string) string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT coalesce(data->>'text', '') FROM card_events WHERE card_id = $1 AND type = 'comment'`, cardID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var all []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		all = append(all, s)
	}
	return strings.Join(all, "\n")
}

func moveTo(t *testing.T, env *startEnv, col string) {
	t.Helper()
	if _, err := db.MoveCard(context.Background(), env.pool, env.card.ID, columnNamed(t, env.cols, col), nil, nil,
		db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
}

func startSites() []startSite {
	respBody := func(t *testing.T, resp *http.Response) string {
		b := readAll(t, resp)
		if resp.StatusCode < 400 {
			return ""
		}
		return b
	}
	return []startSite{
		{name: "card Run (human click)", run: func(t *testing.T, env *startEnv) string {
			return respBody(t, env.request("POST", "/api/cards/"+env.card.ID+"/spawn", env.human, nil))
		}},
		{name: "card Discuss (human click)", run: func(t *testing.T, env *startEnv) string {
			return respBody(t, env.request("POST", "/api/cards/"+env.card.ID+"/spawn", env.human, map[string]string{"mode": "discuss"}))
		}},
		{name: "board chat (human click)", run: func(t *testing.T, env *startEnv) string {
			return respBody(t, env.request("POST", "/api/boards/"+env.board.ID+"/sessions", env.human, nil))
		}},
		{name: "Run board: press Run", setup: func(t *testing.T, env *startEnv) { moveTo(t, env, "ready") },
			run: func(t *testing.T, env *startEnv) string {
				resp := env.request("POST", "/api/boards/"+env.board.ID+"/run", env.human, nil)
				if b := respBody(t, resp); b != "" {
					return b
				}
				tickUntilDispatched(t, env.pool, env.card.ID)
				return ""
			}},
		{name: "Run board: dispatcher tick (token went bad mid-run)", setup: func(t *testing.T, env *startEnv) {
			moveTo(t, env, "ready")
			// a run that was already going when the board lost its token
			if _, err := env.pool.Exec(context.Background(),
				`INSERT INTO board_runs (board_id) VALUES ($1)`, env.board.ID); err != nil {
				t.Fatal(err)
			}
		}, run: func(t *testing.T, env *startEnv) string {
			srvAPI.RunTick(context.Background())
			var state string
			_ = env.pool.QueryRow(context.Background(),
				`SELECT state FROM board_runs WHERE board_id = $1`, env.board.ID).Scan(&state)
			if state == "running" {
				return ""
			}
			return "run " + state + ": " + cardComments(t, env.pool, env.card.ID)
		}},
		{name: "quiet-worker respawn", setup: func(t *testing.T, env *startEnv) {
			moveTo(t, env, "in progress")
			ctx := context.Background()
			if _, err := env.pool.Exec(ctx, `
				INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role, model,
					created_at, last_activity_at)
				VALUES ($1,$2,'fake','ext-old','stopped','worker','', now() - interval '1 hour', now() - interval '1 hour')`,
				env.card.ID, env.board.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := env.pool.Exec(ctx,
				`UPDATE cards SET updated_at = now() - interval '1 hour' WHERE id = $1`, env.card.ID); err != nil {
				t.Fatal(err)
			}
		}, run: func(t *testing.T, env *startEnv) string {
			srvAPI.RunTick(context.Background())
			c, _ := db.GetCard(context.Background(), env.pool, env.card.ID)
			if c.StuckAt == nil {
				return ""
			}
			return cardComments(t, env.pool, env.card.ID)
		}},
		{name: "standing agent", setup: func(t *testing.T, env *startEnv) {
			ctx := context.Background()
			if _, err := db.CreateStandingAgent(ctx, env.pool, env.board.ID, columnNamed(t, env.cols, "ready"),
				"triager", "fake", "triage this card", "per_card"); err != nil {
				t.Fatal(err)
			}
			moveTo(t, env, "ready")
		}, run: func(t *testing.T, env *startEnv) string {
			srvAPI.DrainStandingQueueOnce(context.Background())
			var lastErr string
			_ = env.pool.QueryRow(context.Background(),
				`SELECT coalesce(last_error, '') FROM standing_agent_queue WHERE card_id = $1`, env.card.ID).Scan(&lastErr)
			return lastErr
		}},
	}
}

// TestEveryStartUsesTheBoardAutomationIdentity: every way blerg-board starts a
// session — the ones a human clicks and the ones nobody does — goes to the
// runner under the board's automation token and engine, and with none
// configured starts nothing and says why where a human will see it.
func TestEveryStartUsesTheBoardAutomationIdentity(t *testing.T) {
	for _, site := range startSites() {
		for _, configured := range []bool{false, true} {
			name := site.name + map[bool]string{false: " / no token", true: " / token"}[configured]
			t.Run(name, func(t *testing.T) {
				srv, pool := testServer(t)
				ctx := context.Background()
				human := humanToken(t, srv)
				fake := &fakeRunner{lifecycle: "running"}
				srvAPI.SetRunner(api.RunnerConfig{Driver: identityRunner{fake},
					PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})
				board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"app"},
					Model: strPtr("claude-opus-5")})
				if err != nil {
					t.Fatal(err)
				}
				cols, _ := db.ListColumns(ctx, pool, board.ID)
				res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
					Title: strPtr("the card"), Repos: &[]string{"app"}}, db.EventMeta{Actor: "human"})
				if err != nil {
					t.Fatal(err)
				}
				env := &startEnv{pool: pool, human: human, board: board, cols: cols, card: res.Card,
					request: func(method, path, token string, body any) *http.Response {
						return request(t, srv, method, path, token, nil, body)
					}}
				tok := runSessionsToken(t)
				if configured {
					resp := request(t, srv, "PATCH", "/api/boards/"+board.ID, human, nil,
						map[string]any{"automation_token": tok, "automation_engine": "codex"})
					if resp.StatusCode != http.StatusOK {
						t.Fatalf("configure: %d %s", resp.StatusCode, readAll(t, resp))
					}
					resp.Body.Close()
				}
				if site.setup != nil {
					site.setup(t, env)
				}
				reason := site.run(t, env)

				if !configured {
					if n := fake.attemptCount(); n != 0 {
						t.Fatalf("%d start(s) reached the runner with no automation token", n)
					}
					if !strings.Contains(reason, "automation token") {
						t.Fatalf("refusal is not visible or not clear: %q", reason)
					}
					if c, _ := db.GetCard(ctx, pool, res.Card.ID); site.name == "card Run (human click)" &&
						c.ColumnID != nil && *c.ColumnID == columnNamed(t, cols, "in progress") {
						t.Fatal("a refused card Run still claimed the card into the work column")
					}
					return
				}
				if reason != "" {
					t.Fatalf("start refused with a token configured: %s", reason)
				}
				if fake.startCount() == 0 {
					t.Fatal("no session started")
				}
				fake.mu.Lock()
				defer fake.mu.Unlock()
				for _, st := range fake.started {
					if st.Token != tok || st.Engine != "codex" {
						t.Fatalf("start carried token-match=%v engine=%q, want the board's token and codex",
							st.Token == tok, st.Engine)
					}
					if st.Model != "" {
						t.Fatalf("a codex session was sent the Claude model %q", st.Model)
					}
				}
			})
		}
	}
}

// A token that went bad after it was saved (revoked, expired) is caught
// before the runner is asked, and the Run button says so.
func TestRunRefusesAnExpiredAutomationToken(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	human := humanToken(t, srv)
	fake := &fakeRunner{lifecycle: "running"}
	srvAPI.SetRunner(api.RunnerConfig{Driver: identityRunner{fake}, PublicURL: "https://b.test", AgentURL: "http://b.svc"})
	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"app"}})
	dead := agentToken(t, "blerg-runner", humanSub, []string{"session.start"}, -time.Minute)
	if err := db.SetBoardAutomationToken(ctx, pool, board.ID, db.AutomationTokenUpdate{Token: dead, AccountID: humanSub}); err != nil {
		t.Fatal(err)
	}
	resp := request(t, srv, "POST", "/api/boards/"+board.ID+"/run", human, nil, nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "expired") {
		t.Fatalf("Run with an expired token: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, dead) {
		t.Fatal("refusal echoes the token")
	}
	if fake.attemptCount() != 0 {
		t.Fatal("the runner was asked anyway")
	}
}

// The runner is the last word on a token: a 401 on start stops the run
// rather than flagging card after card.
func TestRunnerRejectionOfAutomationTokenStopsTheRun(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	_ = humanToken(t, srv)
	fake := &fakeRunner{lifecycle: "running", startErr: runnerUnauthorized()}
	srvAPI.SetRunner(api.RunnerConfig{Driver: identityRunner{fake}, PublicURL: "https://b.test", AgentURL: "http://b.svc"})
	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"app"}})
	cols, _ := db.ListColumns(ctx, pool, board.ID)
	var cards []string
	for _, title := range []string{"one", "two", "three"} {
		res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strPtr(title), Repos: &[]string{"app"},
			ColumnID: strPtr(columnNamed(t, cols, "ready"))}, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		cards = append(cards, res.Card.ID)
	}
	if err := db.SetBoardAutomationToken(ctx, pool, board.ID, db.AutomationTokenUpdate{Token: runSessionsToken(t), AccountID: humanSub}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO board_runs (board_id) VALUES ($1)`, board.ID); err != nil {
		t.Fatal(err)
	}
	srvAPI.RunTick(ctx)
	srvAPI.RunTick(ctx)
	if n := fake.attemptCount(); n != 1 {
		t.Fatalf("runner asked %d times, want once — the first refusal must stop the run", n)
	}
	var state string
	_ = pool.QueryRow(ctx, `SELECT state FROM board_runs WHERE board_id = $1`, board.ID).Scan(&state)
	if state != "stopped" {
		t.Fatalf("run state = %s, want stopped", state)
	}
	for _, id := range cards {
		c, _ := db.GetCard(ctx, pool, id)
		if c.StuckAt != nil {
			t.Fatalf("card %s flagged stuck for a board-level problem", c.Title)
		}
	}
}

// Without BLERG_BOARD_SECRET_KEY a board cannot store a token: the PATCH is a
// 422 naming the variable and stores nothing, while a PATCH that does not
// touch the token keeps working (boards without automation are unaffected).
func TestSavingAutomationTokenWithoutKeyIsRefused(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	human := humanToken(t, srv)
	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"app"}})
	path := "/api/boards/" + board.ID
	if err := db.SetAutomationKey(nil); err != nil {
		t.Fatal(err)
	}
	tok := runSessionsToken(t)
	resp := request(t, srv, "PATCH", path, human, nil, map[string]any{"automation_token": tok})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "BLERG_BOARD_SECRET_KEY") {
		t.Fatalf("save without key: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, tok) {
		t.Fatal("refusal echoes the token")
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT automation_token FROM boards WHERE id = $1`, board.ID).Scan(&stored); err != nil || stored != "" {
		t.Fatalf("a refused save stored %q (%v)", stored, err)
	}
	resp = request(t, srv, "PATCH", path, human, nil, map[string]any{"description": "still editable"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a PATCH without a token failed with no key: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// A stored token that no longer opens (lost or rotated key, damage) is a clear
// "re-save it" refusal at dispatch — a 422, not a crash — and never echoes the
// stored value.
func TestRunRefusesAnUndecryptableAutomationToken(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	human := humanToken(t, srv)
	fake := &fakeRunner{lifecycle: "running"}
	srvAPI.SetRunner(api.RunnerConfig{Driver: identityRunner{fake}, PublicURL: "https://b.test", AgentURL: "http://b.svc"})
	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"app"}})
	if err := db.SetBoardAutomationToken(ctx, pool, board.ID, db.AutomationTokenUpdate{Token: runSessionsToken(t), AccountID: humanSub}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAutomationKey([]byte("ffffffffffffffffffffffffffffffff")); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT automation_token FROM boards WHERE id = $1`, board.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	resp := request(t, srv, "POST", "/api/boards/"+board.ID+"/run", human, nil, nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "cannot be decrypted; re-save it") {
		t.Fatalf("Run with an undecryptable token: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, stored) {
		t.Fatal("refusal echoes the stored value")
	}
	if fake.attemptCount() != 0 {
		t.Fatal("the runner was asked anyway")
	}
}

// Structural guard: startOnRunner is the only caller of Driver.Start, so no
// start path — present or future — can reach the runner without the board's
// automation identity.
func TestOnlyStartOnRunnerCallsDriverStart(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`\.Driver\.Start\(`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(call.FindAll(src, -1)); n > 0 && f != "automation.go" {
			t.Errorf("%s calls Driver.Start directly (%d); go through startOnRunner", f, n)
		}
	}
}
