package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/coreauth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/gate"
	"github.com/blerglab/blerg-ai/board/internal/mcp"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

type denyAll struct{}

func (denyAll) Name() string    { return "fake" }
func (denyAll) ModelID() string { return "fake" }
func (denyAll) Review(_ context.Context, _ gate.Input) (gate.Verdict, error) {
	return gate.Verdict{Decision: "deny", Reason: "duplicate of #1"}, nil
}

// srvAPI is the API behind the most recent testServer (runner tests wire a
// fake driver onto it).
var srvAPI *api.API

// corePriv is the throwaway signing key backing the fake blerg-core server
// wireFakeCore starts for the most recently created testServer/testServerHeld
// — humanToken signs test bearer tokens against it.
var corePriv ed25519.PrivateKey

const coreTestKID = "test-1"

// wireFakeCore starts a fake blerg-core JWKS+revocations HTTP server backed
// by a fresh throwaway ed25519 keypair, and wires a coreauth.Client pointed
// at it into a via SetCore — the same exported surface (NewClient + Start)
// blerg-board uses against the real blerg-core, so this needs no seam beyond
// what coreauth already exposes. coreauth.Client's JWKS/revocation cache is
// unexported and only poke-able from within package coreauth itself (see
// coreauth_test.go's TestClientVerify), which isn't reachable from this
// (api_test) package — a fake HTTP server is the exported-surface equivalent.
func wireFakeCore(t *testing.T, a *auth.Auth) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	corePriv = priv
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			coreTestKID: base64.RawURLEncoding.EncodeToString(pub),
		})
	})
	mux.HandleFunc("/revocations", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{})
	})
	core := httptest.NewServer(mux)
	t.Cleanup(core.Close)
	c := coreauth.NewClient(core.URL)
	c.Start(context.Background()) // synchronous initial fetch before backgrounding refreshes
	a.SetCore(c)
}

func testServer(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.SetAutomationKey([]byte("0123456789abcdef0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.SetAutomationKey(nil) })
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE boards, tokens, admission_reviews, runner_capacity RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	a := auth.New(pool, "svc-key-123")
	if err := a.EnsureServiceToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	wireFakeCore(t, a)
	g := gate.New(pool, []gate.Backend{denyAll{}}, nil)
	apiSrv := api.New(pool, a, g)
	srvAPI = apiSrv
	mux := http.NewServeMux()
	apiSrv.Routes(mux)
	mux.Handle("/mcp", mcp.New(apiSrv))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, pool
}

func request(t *testing.T, srv *httptest.Server, method, path, token string, cookie *http.Cookie, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, srv.URL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// fullHumanCaps mirrors the capability set EnsureServiceToken grants the
// native BLERG_BOARD_SERVICE_KEY row — the old shared-password human session
// had no capability check at all (see the now-deleted RequireBoard
// KindHuman bypass), so a human test token needs the full set to exercise
// the same call sites.
var fullHumanCaps = []string{
	"card.read", "card.write", "column.write",
	"board.admin", "runner.report", "gate.bypass",
}

// humanToken mints a fake blerg-core-issued Kind:"human" bearer token with
// the full capability set, replacing the old login(t, srv) *http.Cookie
// helper now that board has no native password/session scheme (see
// board/internal/auth's corePrincipal). srv is accepted but unused — kept so
// every existing `token := login(t, srv)` call site needed only a rename.
func humanToken(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return testHumanToken(t, fullHumanCaps)
}

// testHumanToken mints a fake blerg-core-issued Kind:"human" bearer token
// carrying exactly caps, signed against wireFakeCore's throwaway keypair —
// mirrors coreauth_test.go's own (unexported, so reimplemented here against
// contracts/identity's exported EncodeSigningInput) mint helper.
func testHumanToken(t *testing.T, caps []string) string {
	t.Helper()
	return testCoreToken(t, "human", caps)
}

// memberToken mirrors core's PlatformRoleCaps["member"] exactly
// (core/internal/identity/roles.go).
func memberToken(t *testing.T) string {
	t.Helper()
	return testHumanToken(t, []string{"card.read", "card.write", "column.write", "session.start"})
}

// adminToken mirrors core's PlatformRoleCaps["admin"] exactly
// (core/internal/identity/roles.go): member caps + board.admin, gate.bypass,
// membership.write, secrets.read, account.manage. Note it still lacks
// runner.report, which only the env service key's row carries.
func adminToken(t *testing.T) string {
	t.Helper()
	return testHumanToken(t, []string{"card.read", "card.write", "column.write", "session.start",
		"board.admin", "gate.bypass", "membership.write", "secrets.read", "account.manage"})
}

// decodeBody JSON-decodes resp into v and closes the body.
func decodeBody(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode %s: %v", resp.Request.URL.Path, err)
	}
}

// testCoreToken mints a fake blerg-core-issued bearer token of the given Kind
// claim carrying exactly caps, signed against wireFakeCore's throwaway
// keypair.
func testCoreToken(t *testing.T, kind string, caps []string) string {
	t.Helper()
	if corePriv == nil {
		t.Fatal("testCoreToken: no fake core wired — call testServer/testServerHeld first")
	}
	now := time.Now().Unix()
	claims := identity.Claims{
		Sub: "test-" + kind, Aud: "blerg-board", Kind: kind,
		Caps: caps, ExpiresAt: now + 3600,
	}
	hb, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": coreTestKID})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	si := identity.EncodeSigningInput(hb, pb)
	sig := ed25519.Sign(corePriv, []byte(si))
	return si + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// TestNoCredentialNoWrite is the gate-bypass regression test: "human" must be
// a positive assertion (a verified bearer token), never the absence of one.
func TestNoCredentialNoWrite(t *testing.T) {
	srv, _ := testServer(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/boards"},
		{"POST", "/api/boards"},
		{"POST", "/api/boards/x/cards"},
		{"GET", "/api/tokens"},
	} {
		resp := request(t, srv, tc.method, tc.path, "", nil, map[string]string{"name": "x", "title": "x"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without credential: %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	// A garbage bearer token (not a board token, not the service key, and
	// not a verifiable blerg-core token) gets nothing either.
	resp := request(t, srv, "GET", "/api/boards", "garbage", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("garbage bearer token: %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// TestHumanTokenCapabilityScoped is the exact scenario the spec's adversarial
// review caught (see auth_test.go's TestRequireBoardEnforcesCapabilityForCoreHumanToken
// for the unit-level version): a core-issued human token with only member
// capabilities must be rejected for an admin-only action and allowed for one
// it actually carries — proven here end-to-end over HTTP, not just in
// RequireBoard isolation.
func TestHumanTokenCapabilityScoped(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	gt, f := true, false
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "scoped", GateEnabled: &gt, RequireRepo: &f})
	if err != nil {
		t.Fatal(err)
	}

	member := testHumanToken(t, []string{"card.read", "card.write"})

	// Carries card.read: allowed.
	resp := request(t, srv, "GET", "/api/boards/"+board.ID, member, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET board with card.read = %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Carries card.write: creating a card is allowed (also proves human
	// still bypasses the admission gate, per gateCheck's IsHuman check).
	resp = request(t, srv, "POST", "/api/boards/"+board.ID+"/cards", member, nil, map[string]string{"title": "x"})
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("POST card with card.write = %d, want 201", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Does NOT carry column.write: rejected — this is the exact bug the spec
	// review caught, proven at the HTTP layer (RequireBoard's dead KindHuman
	// bypass would have let this through).
	resp = request(t, srv, "POST", "/api/boards/"+board.ID+"/columns", member, nil, map[string]string{"name": "extra"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST column without column.write = %d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestGateAppliesToAgentsNotHumans(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	// Human creates a gated board.
	gt, f := true, false
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{
		Name: "work", GateEnabled: &gt, RequireRepo: &f,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Human write bypasses the gate (curator would deny everything).
	resp := request(t, srv, "POST", "/api/boards/"+board.ID+"/cards", cookie, nil,
		map[string]string{"title": "human card"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("human create through deny-all gate: %d, want 201", resp.StatusCode)
	}

	// Agent write hits the gate and is denied with a structured verdict.
	_, raw, err := db.MintToken(ctx, pool, &board.ID, "agent", "sweeper",
		[]string{"card.read", "card.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp = request(t, srv, "POST", "/api/boards/"+board.ID+"/cards", raw, nil,
		map[string]string{"title": "agent card"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("agent create through deny-all gate: %d, want 409", resp.StatusCode)
	}
	var verdict map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&verdict)
	if verdict["decision"] != "deny" || verdict["review_id"] == nil {
		t.Errorf("denial body missing structure: %v", verdict)
	}

	// An agent token must NOT reach human-only surfaces.
	resp = request(t, srv, "POST", "/api/reviews/x/resolve", raw, nil,
		map[string]string{"decision": "approve"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("agent resolving held review: %d, want 403", resp.StatusCode)
	}

	// Service key with gate.bypass sails through (blerg-ops's incident path).
	resp = request(t, srv, "POST", "/api/boards/"+board.ID+"/cards", "svc-key-123", nil,
		map[string]string{"title": "INC card", "external_id": "INC-20260731-01"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("service create with gate.bypass: %d, want 201", resp.StatusCode)
	}
}

func TestBoardScopedToken(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	f := false
	b1, _ := db.CreateBoard(ctx, pool, db.BoardParams{Name: "one", RequireRepo: &f})
	b2, _ := db.CreateBoard(ctx, pool, db.BoardParams{Name: "two", RequireRepo: &f})
	_, raw, err := db.MintToken(ctx, pool, &b1.ID, "agent", "scoped",
		[]string{"card.read", "card.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Write on the scoped board works (ungated board).
	resp := request(t, srv, "POST", "/api/boards/"+b1.ID+"/cards", raw, nil,
		map[string]string{"title": "in scope"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("in-scope create: %d", resp.StatusCode)
	}
	// Write on the other board is forbidden.
	resp = request(t, srv, "POST", "/api/boards/"+b2.ID+"/cards", raw, nil,
		map[string]string{"title": "out of scope"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("out-of-scope create: %d, want 403", resp.StatusCode)
	}
}

func TestMCPRoundTrip(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	f := false
	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{Name: "mcp", RequireRepo: &f})
	_, raw, err := db.MintToken(ctx, pool, &board.ID, "agent", "mcp agent",
		[]string{"card.read", "card.write", "column.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	call := func(method string, params any) map[string]any {
		t.Helper()
		body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
		resp := request(t, srv, "POST", "/mcp", raw, nil, body)
		if resp.StatusCode != 200 {
			t.Fatalf("mcp %s: HTTP %d", method, resp.StatusCode)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	// No credential → 401 even on MCP.
	resp := request(t, srv, "POST", "/mcp", "", nil,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated MCP: %d, want 401", resp.StatusCode)
	}

	if out := call("initialize", map[string]any{}); out["result"] == nil {
		t.Fatal("initialize failed")
	}
	out := call("tools/list", map[string]any{})
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) < 10 {
		t.Fatalf("expected full tool catalog, got %d", len(tools))
	}

	out = call("tools/call", map[string]any{
		"name": "blerg_card_create",
		"arguments": map[string]any{
			"board_id": board.ID, "title": "filed via MCP", "dedup_key": "mcp:test:1",
		},
	})
	result := out["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("card create errored: %v", result)
	}
	// Re-filing with the same dedup_key refreshes rather than duplicating.
	out = call("tools/call", map[string]any{
		"name": "blerg_card_create",
		"arguments": map[string]any{
			"board_id": board.ID, "title": "filed via MCP v2", "dedup_key": "mcp:test:1",
		},
	})
	text := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var parsed struct {
		Refreshed bool `json:"refreshed"`
	}
	_ = json.Unmarshal([]byte(text), &parsed)
	if !parsed.Refreshed {
		t.Error("second create with same dedup_key must refresh")
	}
}

// unavailableBackend fails every call, forcing the gate's "curator
// unavailable" fallback — held reviews for these tests come from
// GateOnUnavailable: "hold", not from a curator verdict.
type unavailableBackend struct{}

func (unavailableBackend) Name() string    { return "unavailable" }
func (unavailableBackend) ModelID() string { return "unavailable" }
func (unavailableBackend) Review(_ context.Context, _ gate.Input) (gate.Verdict, error) {
	return gate.Verdict{}, errors.New("backend down")
}

// testServerHeld is testServer with a board whose gate holds instead of
// denying when the curator is unavailable — the setup shared by the
// held-review resolution tests below.
func testServerHeld(t *testing.T) (*httptest.Server, *pgxpool.Pool, db.Board, []db.Column, string) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE boards, tokens, admission_reviews, runner_capacity RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	a := auth.New(pool, "svc-key-123")
	if err := a.EnsureServiceToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	wireFakeCore(t, a)
	g := gate.New(pool, []gate.Backend{unavailableBackend{}}, nil)
	apiSrv := api.New(pool, a, g)
	mux := http.NewServeMux()
	apiSrv.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx := context.Background()
	gt, f, hold := true, false, "hold"
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{
		Name: "held-work", GateEnabled: &gt, RequireRepo: &f, GateOnUnavailable: &hold,
	})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := db.MintToken(ctx, pool, &board.ID, "agent", "held-tester",
		[]string{"card.read", "card.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return srv, pool, board, cols, raw
}

func columnNamed(t *testing.T, cols []db.Column, name string) string {
	t.Helper()
	for _, c := range cols {
		if c.Name == name {
			return c.ID
		}
	}
	t.Fatalf("no column named %q", name)
	return ""
}

func resolveReview(t *testing.T, srv *httptest.Server, cookie string, reviewID string) *http.Response {
	t.Helper()
	return request(t, srv, "POST", "/api/reviews/"+reviewID+"/resolve", cookie, nil,
		map[string]string{"decision": "approve"})
}

func heldReviewID(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("gated write: %d, want 202", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	id, _ := body["review_id"].(string)
	if id == "" {
		t.Fatalf("held response missing review_id: %v", body)
	}
	return id
}

func TestResolveHeldReview_ApplyUpdate(t *testing.T) {
	srv, pool, board, _, raw := testServerHeld(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	created, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strPtr("original")},
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	card := created.Card

	resp := request(t, srv, "PATCH", "/api/cards/"+card.ID, raw, nil, map[string]string{"title": "updated title"})
	reviewID := heldReviewID(t, resp)

	resp = resolveReview(t, srv, cookie, reviewID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve held update: %d, want 200", resp.StatusCode)
	}
	got, err := db.GetCard(ctx, pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "updated title" {
		t.Errorf("title after approve = %q, want %q", got.Title, "updated title")
	}
}

func TestResolveHeldReview_ApplyMove(t *testing.T) {
	srv, pool, board, cols, raw := testServerHeld(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	created, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strPtr("move me")},
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	card := created.Card
	readyID := columnNamed(t, cols, "ready")

	resp := request(t, srv, "POST", "/api/cards/"+card.ID+"/move", raw, nil,
		map[string]string{"column_id": readyID})
	reviewID := heldReviewID(t, resp)

	resp = resolveReview(t, srv, cookie, reviewID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve held move: %d, want 200", resp.StatusCode)
	}
	got, err := db.GetCard(ctx, pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ColumnID == nil || *got.ColumnID != readyID {
		t.Errorf("column after approve = %v, want %q", got.ColumnID, readyID)
	}
}

func TestResolveHeldReview_ApplyDelete(t *testing.T) {
	srv, pool, board, _, raw := testServerHeld(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	created, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strPtr("delete me")},
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	card := created.Card

	resp := request(t, srv, "DELETE", "/api/cards/"+card.ID, raw, nil, nil)
	reviewID := heldReviewID(t, resp)

	resp = resolveReview(t, srv, cookie, reviewID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve held delete: %d, want 200", resp.StatusCode)
	}
	if _, err := db.GetCard(ctx, pool, card.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("card after approved delete: err=%v, want ErrNotFound", err)
	}
}

func TestResolveHeldReview_StaleTargetArchived(t *testing.T) {
	srv, pool, board, _, raw := testServerHeld(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	created, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strPtr("archive race")},
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	card := created.Card

	resp := request(t, srv, "PATCH", "/api/cards/"+card.ID, raw, nil, map[string]string{"title": "too late"})
	reviewID := heldReviewID(t, resp)

	// Human archives the card directly while the update sits held.
	if _, err := db.ArchiveCard(ctx, pool, card.ID, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}

	resp = resolveReview(t, srv, cookie, reviewID)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("approve against archived target: %d, want 409", resp.StatusCode)
	}
	got, err := db.GetCard(ctx, pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "archive race" {
		t.Errorf("archived card content changed by rejected replay: %q", got.Title)
	}
}

func TestResolveHeldReview_StaleTargetDeleted(t *testing.T) {
	srv, pool, board, _, raw := testServerHeld(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	created, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strPtr("delete race")},
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	card := created.Card

	resp := request(t, srv, "PATCH", "/api/cards/"+card.ID, raw, nil, map[string]string{"title": "too late"})
	reviewID := heldReviewID(t, resp)

	// Human deletes the card directly while the update sits held.
	if err := db.DeleteCard(ctx, pool, card.ID); err != nil {
		t.Fatal(err)
	}

	resp = resolveReview(t, srv, cookie, reviewID)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("approve against deleted target: %d, want 409", resp.StatusCode)
	}
}

func TestResolveHeldReview_DivergenceNoted(t *testing.T) {
	srv, pool, board, _, raw := testServerHeld(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	created, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strPtr("drift"), Body: strPtr("body v1")},
		db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	card := created.Card

	resp := request(t, srv, "PATCH", "/api/cards/"+card.ID, raw, nil, map[string]string{"title": "held title"})
	reviewID := heldReviewID(t, resp)

	// Human edits the card directly while the update sits held — bumps version.
	if _, err := db.UpdateCard(ctx, pool, card.ID, db.CardParams{Body: strPtr("body v2")},
		db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}

	resp = resolveReview(t, srv, cookie, reviewID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve after concurrent edit: %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["note"] == nil {
		t.Errorf("expected a divergence note in approve response, got %v", body)
	}
	got, err := db.GetCard(ctx, pool, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The held payload only touched title — the concurrent body edit survives.
	if got.Title != "held title" || got.Body == nil || *got.Body != "body v2" {
		t.Errorf("merged card = title:%q body:%v, want title:%q body:%q", got.Title, got.Body, "held title", "body v2")
	}
}

func TestMoveColumn(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	f := false
	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{Name: "cols", RequireRepo: &f})
	_, raw, err := db.MintToken(ctx, pool, &board.ID, "agent", "col agent",
		[]string{"card.read", "card.write", "column.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil || len(cols) != 4 {
		t.Fatalf("expected 4 default columns, got %d (%v)", len(cols), err)
	}
	inbox, ready, inProgress, done := cols[0], cols[1], cols[2], cols[3]

	// Move "done" before "in progress" via REST.
	resp := request(t, srv, "POST", "/api/columns/"+done.ID+"/move", raw, nil,
		map[string]string{"before_id": inProgress.ID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("move column: %d", resp.StatusCode)
	}
	var moved db.Column
	_ = json.NewDecoder(resp.Body).Decode(&moved)
	_ = resp.Body.Close()
	if moved.ID != done.ID {
		t.Fatalf("moved response id = %q, want %q", moved.ID, done.ID)
	}

	got, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{inbox.Name, ready.Name, done.Name, inProgress.Name}
	for i, c := range got {
		if c.Name != wantOrder[i] {
			t.Fatalf("position %d = %q, want %q", i, c.Name, wantOrder[i])
		}
	}

	// A token scoped to a different board is forbidden.
	other, _ := db.CreateBoard(ctx, pool, db.BoardParams{Name: "other", RequireRepo: &f})
	_, otherRaw, err := db.MintToken(ctx, pool, &other.ID, "agent", "other agent",
		[]string{"card.read", "card.write", "column.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp = request(t, srv, "POST", "/api/columns/"+inbox.ID+"/move", otherRaw, nil,
		map[string]string{"before_id": ready.ID})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-board move: %d, want 403", resp.StatusCode)
	}

	// A token without column.write is forbidden from renaming or deleting too
	// (pre-existing gap: these handlers skipped the board/capability check).
	_, noCapRaw, err := db.MintToken(ctx, pool, &board.ID, "agent", "no column cap",
		[]string{"card.read", "card.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp = request(t, srv, "PATCH", "/api/columns/"+inbox.ID, noCapRaw, nil,
		map[string]string{"name": "renamed"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("rename without column.write: %d, want 403", resp.StatusCode)
	}
	resp = request(t, srv, "DELETE", "/api/columns/"+inbox.ID, noCapRaw, nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("delete without column.write: %d, want 403", resp.StatusCode)
	}
}

func TestMoveColumnMCP(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	f := false
	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{Name: "mcp-cols", RequireRepo: &f})
	_, raw, err := db.MintToken(ctx, pool, &board.ID, "agent", "mcp col agent",
		[]string{"card.read", "card.write", "column.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cols, _ := db.ListColumns(ctx, pool, board.ID)

	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name":      "blerg_column_move",
		"arguments": map[string]any{"column_id": cols[3].ID, "before_id": cols[0].ID},
	}}
	resp := request(t, srv, "POST", "/mcp", raw, nil, body)
	if resp.StatusCode != 200 {
		t.Fatalf("mcp blerg_column_move: HTTP %d", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	_ = resp.Body.Close()
	result := out["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("blerg_column_move errored: %v", result)
	}

	got, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ID != cols[3].ID {
		t.Errorf("first column after MCP move = %q, want %q", got[0].ID, cols[3].ID)
	}
}
