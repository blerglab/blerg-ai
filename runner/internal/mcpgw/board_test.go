package mcpgw

// The built-in `board` connection (spec 10.2): exchange tokens as the upstream credential, the
// fixed tool set, no hash pinning, revocation, and the trusted client that reaches only the board.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	boardTestID   = "board-7"
	boardTestCron = "c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0c1"
)

// wantBoardTools is the exact set spec 10.2 names. It is written out here on purpose: the
// constant in board.go must equal it, not merely agree with itself.
var wantBoardTools = []string{
	"blerg_board_get", "blerg_board_list", "blerg_board_schema", "blerg_column_list",
	"blerg_card_search", "blerg_card_get", "blerg_card_create", "blerg_card_update",
	"blerg_card_move", "blerg_card_comment", "blerg_card_link",
}

// boardUpstreamTools is what the board's MCP serves: the eleven allowed tools and five it must
// not hand a cron.
func boardUpstreamTools() []fakeTool {
	var out []fakeTool
	for _, n := range wantBoardTools {
		out = append(out, fakeTool{Name: n, Description: "the board's own " + n, Schema: `{"type":"object"}`})
	}
	for _, n := range []string{"blerg_card_archive", "blerg_column_create", "blerg_column_move", "blerg_board_create", "blerg_board_update"} {
		out = append(out, fakeTool{Name: n, Description: "dangerous " + n, Schema: `{"type":"object"}`})
	}
	return out
}

// fakeExchanger is core's /internal/tokens/exchange and its revoke.
type fakeExchanger struct {
	mu        sync.Mutex
	clock     *fakeClock
	ttl       time.Duration
	err       error
	revokeErr error
	n         int
	proofs    []Proof
	boards    []string
	refs      []string
	revoked   []string // "<account>:<sub>"
}

func (f *fakeExchanger) Exchange(_ context.Context, p Proof, boardID, sessionRef string) (BoardToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return BoardToken{}, f.err
	}
	f.n++
	f.proofs = append(f.proofs, p)
	f.boards = append(f.boards, boardID)
	f.refs = append(f.refs, sessionRef)
	ttl := f.ttl
	if ttl == 0 {
		ttl = 10 * time.Minute
	}
	return BoardToken{Token: fmt.Sprintf("tok-%d", f.n), Sub: fmt.Sprintf("sub-%d", f.n), ExpiresAt: f.clock.Now().Add(ttl)}, nil
}

func (f *fakeExchanger) Revoke(_ context.Context, accountID, sub string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revokeErr != nil {
		return f.revokeErr
	}
	f.revoked = append(f.revoked, accountID+":"+sub)
	return nil
}

func (f *fakeExchanger) exchanges() int { f.mu.Lock(); defer f.mu.Unlock(); return f.n }
func (f *fakeExchanger) revokedList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

// boardHarness is a gateway with the board configured: the board is the fake upstream.
type boardHarness struct {
	*harness
	ex *fakeExchanger
}

func newBoardHarness(t *testing.T, mutate func(*Config)) *boardHarness {
	t.Helper()
	pool := testPool(t)
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	up := newFakeUpstream(t, boardUpstreamTools()...)
	core := &fakeCore{url: up.url(), header: "X-Api-Key", value: "secret-1", clock: clock}
	ex := &fakeExchanger{clock: clock}
	cfg := Config{
		DB: pool, Core: core, Now: clock.Now, Board: ex, BoardMCPURL: up.url(),
		Policy: netguard.Policy{
			AllowHTTPHosts:    []string{"127.0.0.1", "localhost"},
			AllowPrivateHosts: []string{"127.0.0.1", "localhost"},
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	gw := New(cfg)
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	return &boardHarness{harness: &harness{t: t, pool: pool, gw: gw, srv: srv, up: up, core: core, clock: clock}, ex: ex}
}

func (b *boardHarness) grant() grantHandle {
	b.t.Helper()
	gh := grantHandle{sessionID: newUUID(b.t), connectionID: BoardConnectionID, name: BuiltinBoard}
	toks, err := CreateGrants(context.Background(), b.pool, gh.sessionID, "acct-1",
		Proof{AccountID: "acct-1", TokenID: boardTestCron}, []GrantSpec{BoardGrantSpec(boardTestID, b.up.url())})
	if err != nil {
		b.t.Fatalf("CreateGrants: %v", err)
	}
	gh.token = toks[BoardConnectionID]
	return gh
}

func (b *boardHarness) authHeader() string {
	b.up.mu.Lock()
	defer b.up.mu.Unlock()
	return b.up.lastHeaders.Get("Authorization")
}

func TestBoardToolNamesAreExact(t *testing.T) {
	got := BoardToolNames()
	want := append([]string(nil), wantBoardTools...)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("board tools = %v, want %v", got, want)
	}
	for n, tg := range BoardTools() {
		if tg.Mode != ModeAllow || tg.Hash != "" {
			t.Errorf("tool %s = %+v, want mode allow and no pinned hash", n, tg)
		}
	}
}

func TestBoardGrantRowShape(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	rows, err := db.ListMCPGrantsForSession(context.Background(), b.pool, gh.sessionID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("grants = %v, %v", rows, err)
	}
	g := rows[0]
	if g.Builtin != BuiltinBoard || g.BuiltinRef != boardTestID || g.ConnectionID != BoardConnectionID || g.Name != "board" {
		t.Errorf("grant = %+v", g)
	}
	if g.ProofKind != "token_id" || g.ProofValue != boardTestCron || g.URLSnapshot != b.up.url() {
		t.Errorf("proof/url = %s %s %s", g.ProofKind, g.ProofValue, g.URLSnapshot)
	}
	if len(g.Tools) != len(wantBoardTools) {
		t.Errorf("stored tools = %d, want %d", len(g.Tools), len(wantBoardTools))
	}
	// The sweeper's view of grants (user connections against core) never includes it.
	refs, err := db.ListMCPGrantRefs(context.Background(), b.pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range refs {
		if r.SessionID == gh.sessionID {
			t.Errorf("a builtin grant is listed for the connection sweeper: %+v", r)
		}
	}
	// A caller cannot smuggle its own tools, or an unknown builtin, through the spec.
	bad := BoardGrantSpec(boardTestID, b.up.url())
	bad.Tools = map[string]ToolGrant{"blerg_card_archive": {Mode: ModeAllow}}
	if _, err := CreateGrants(context.Background(), b.pool, newUUID(t), "acct-1", Proof{AccountID: "acct-1", TokenID: boardTestCron}, []GrantSpec{bad}); err != nil {
		t.Fatalf("CreateGrants: %v", err)
	}
	for _, spec := range []GrantSpec{
		{ConnectionID: BoardConnectionID, Name: "board", URL: b.up.url(), Builtin: "mail", BuiltinRef: "x"},
		{ConnectionID: BoardConnectionID, Name: "board", URL: b.up.url(), Builtin: BuiltinBoard},
	} {
		if _, err := CreateGrants(context.Background(), b.pool, newUUID(t), "acct-1", Proof{AccountID: "acct-1", TokenID: boardTestCron}, []GrantSpec{spec}); err == nil {
			t.Errorf("spec %+v was accepted", spec)
		}
	}
	// The smuggled tool list was replaced by the fixed one.
	var stored int
	if err := b.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM session_mcp_grants, jsonb_object_keys(tools) k WHERE builtin = 'board' AND k = 'blerg_card_archive'`).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("archive stored on a builtin grant: %d, %v", stored, err)
	}
}

func TestBoardExchangeArgsAndUpstreamCredential(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	r := b.call(gh, "tools/list", map[string]any{})
	if r.status != 200 || r.errMessage() != "" {
		t.Fatalf("tools/list: %d %s", r.status, r.raw)
	}
	if b.ex.exchanges() != 1 {
		t.Fatalf("exchanges = %d, want 1", b.ex.exchanges())
	}
	p := b.ex.proofs[0]
	if p.TokenID != boardTestCron || p.SessionID != "" || p.AccountID != "acct-1" || b.ex.boards[0] != boardTestID || b.ex.refs[0] != gh.sessionID {
		t.Errorf("exchange args: proof=%+v board=%q ref=%q", p, b.ex.boards[0], b.ex.refs[0])
	}
	if got := b.authHeader(); got != "Bearer tok-1" {
		t.Errorf("upstream Authorization = %q, want the exchange token", got)
	}
	// The credential is cached: a call does not exchange again, and nothing asks core for a
	// connection credential (the board is not a core connection).
	if r := b.callTool(gh, "blerg_card_get", map[string]any{"id": "c1"}); r.errMessage() != "" || resultText(r) != "ok:blerg_card_get" {
		t.Fatalf("call: %s", r.raw)
	}
	if b.ex.exchanges() != 1 || b.core.count() != 0 {
		t.Errorf("exchanges=%d core token fetches=%d, want 1 and 0", b.ex.exchanges(), b.core.count())
	}
}

func TestBoardReexchangeNearExpiry(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	b.callTool(gh, "blerg_board_list", map[string]any{})
	b.clock.Advance(8 * time.Minute) // 2 minutes left: still fine
	b.callTool(gh, "blerg_board_list", map[string]any{})
	if b.ex.exchanges() != 1 || b.authHeader() != "Bearer tok-1" {
		t.Fatalf("exchanges=%d header=%q, want the first token still in use", b.ex.exchanges(), b.authHeader())
	}
	b.clock.Advance(70 * time.Second) // 50 seconds left: under the 60 second margin
	if r := b.callTool(gh, "blerg_board_list", map[string]any{}); r.errMessage() != "" {
		t.Fatalf("call: %s", r.raw)
	}
	if b.ex.exchanges() != 2 || b.authHeader() != "Bearer tok-2" {
		t.Errorf("exchanges=%d header=%q, want a fresh exchange", b.ex.exchanges(), b.authHeader())
	}
}

func TestBoardExposesExactlyTheFixedTools(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	r := b.call(gh, "tools/list", map[string]any{})
	got := toolNames(r)
	want := append([]string(nil), wantBoardTools...)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("listed = %v\nwant    %v", got, want)
	}
	_, _, callsBefore := b.up.snapshot()
	for _, n := range []string{"blerg_card_archive", "blerg_column_create", "blerg_column_move", "blerg_board_create", "blerg_board_update", "no_such"} {
		r := b.callTool(gh, n, map[string]any{})
		if r.errMessage() != "unknown tool" {
			t.Errorf("%s: %s, want refused as unknown", n, r.raw)
		}
	}
	if _, _, calls := b.up.snapshot(); calls != callsBefore {
		t.Errorf("a refused tool reached the board: %d calls", calls-callsBefore)
	}
}

func TestBoardHasNoHashPinning(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	b.up.setDescription("blerg_card_create", "the board reworded its own tool")
	if !slices.Contains(toolNames(b.call(gh, "tools/list", map[string]any{})), "blerg_card_create") {
		t.Error("a board tool whose description changed is hidden: built-in tools are not pinned")
	}
	if r := b.callTool(gh, "blerg_card_create", map[string]any{"title": "t"}); r.errMessage() != "" || resultText(r) != "ok:blerg_card_create" {
		t.Errorf("call after a description change: %s", r.raw)
	}
}

func TestBoardSessionEndRevokesEveryOutstandingToken(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	b.callTool(gh, "blerg_board_list", map[string]any{}) // sub-1
	b.clock.Advance(9*time.Minute + 10*time.Second)
	b.callTool(gh, "blerg_board_list", map[string]any{}) // sub-2; sub-1 has 50 s left
	if b.ex.exchanges() != 2 {
		t.Fatalf("exchanges = %d, want 2", b.ex.exchanges())
	}
	if err := DeleteGrantsForSession(context.Background(), b.pool, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	b.gw.RevokeSession(context.Background(), gh.sessionID)
	got := b.ex.revokedList()
	sort.Strings(got)
	if !slices.Equal(got, []string{"acct-1:sub-1", "acct-1:sub-2"}) {
		t.Fatalf("revoked = %v", got)
	}
	// Idempotent, and the session's state is gone: no second round of revocations.
	b.gw.RevokeSession(context.Background(), gh.sessionID)
	if n := len(b.ex.revokedList()); n != 2 {
		t.Errorf("revoked %d after a repeat, want 2", n)
	}
	// The token no longer works at the gateway.
	if r := b.callTool(gh, "blerg_board_list", map[string]any{}); r.status != http.StatusUnauthorized {
		t.Errorf("a call after the session ended = %d, want 401", r.status)
	}
}

func TestBoardClosedStateNeverExchangesAgain(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	b.callTool(gh, "blerg_board_list", map[string]any{})
	st := b.gw.stateFor(context.Background(), &db.MCPGrant{TokenHash: hashToken(gh.token), SessionID: gh.sessionID, AccountID: "acct-1", Builtin: BuiltinBoard})
	if err := DeleteGrantsForSession(context.Background(), b.pool, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	b.gw.RevokeSession(context.Background(), gh.sessionID)
	// A call that was in flight when the session ended holds the old state; it must not mint a new token.
	gr := &db.MCPGrant{SessionID: gh.sessionID, AccountID: "acct-1", Builtin: BuiltinBoard, BuiltinRef: boardTestID,
		URLSnapshot: b.up.url(), ProofKind: "token_id", ProofValue: boardTestCron}
	if _, err := st.credential(context.Background(), gr); err == nil {
		t.Fatal("a closed state handed out a credential")
	}
	if b.ex.exchanges() != 1 {
		t.Errorf("exchanges = %d, want 1", b.ex.exchanges())
	}
}

func TestBoardGrantDeletedBehindTheGatewaysBackIsRevokedByPrune(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	b.callTool(gh, "blerg_board_list", map[string]any{})
	// Deleted by something other than the session end path (the sweeper, the orphan cleanup).
	if ok, err := db.DeleteMCPGrant(context.Background(), b.pool, gh.sessionID, BoardConnectionID); err != nil || !ok {
		t.Fatalf("delete grant: %v %v", ok, err)
	}
	b.gw.Prune(context.Background())
	if got := b.ex.revokedList(); !slices.Equal(got, []string{"acct-1:sub-1"}) {
		t.Fatalf("revoked = %v, want sub-1", got)
	}
}

func TestBoardExchangeFailureIsAnErrorNotSilentNoAccess(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	b.ex.err = errors.New("dial tcp 192.0.2.3:8080: connection refused")
	r := b.callTool(gh, "blerg_board_list", map[string]any{})
	if r.errMessage() == "" {
		t.Fatalf("a failed exchange produced a result: %s", r.raw)
	}
	if strings.Contains(r.raw, "192.0.2.3") || strings.Contains(r.raw, "refused") {
		t.Errorf("the agent saw the network error: %s", r.raw)
	}
	b.ex.err = ErrConnectionGone
	if r := b.callTool(gh, "blerg_board_list", map[string]any{}); !strings.Contains(r.errMessage(), "no longer available") {
		t.Errorf("a dead proof: %s", r.raw)
	}
	b.ex.err = nil
	if r := b.callTool(gh, "blerg_board_list", map[string]any{}); r.errMessage() != "" {
		t.Errorf("recovery: %s", r.raw)
	}
}

func TestBoardNeedsItsConfiguration(t *testing.T) {
	// A gateway restarted with a different board address refuses the old grant: the snapshot is
	// the address the session was started against.
	moved := newBoardHarness(t, func(c *Config) { c.BoardMCPURL = "http://elsewhere.invalid/mcp" })
	r := moved.callTool(moved.grant(), "blerg_board_list", map[string]any{})
	if !strings.Contains(r.errMessage(), "address changed") || moved.ex.exchanges() != 0 {
		t.Errorf("moved board: %s (exchanges %d)", r.raw, moved.ex.exchanges())
	}
	// No board configured at all.
	none := newBoardHarness(t, func(c *Config) { c.Board, c.BoardMCPURL = nil, "" })
	if r := none.callTool(none.grant(), "blerg_board_list", map[string]any{}); r.errMessage() == "" {
		t.Errorf("no board configured: %s", r.raw)
	}
	if none.gw.BoardConfigured() {
		t.Error("BoardConfigured with no exchanger")
	}
	if !newBoardHarness(t, nil).gw.BoardConfigured() {
		t.Error("BoardConfigured = false with an exchanger and an address")
	}
}

// The board is reachable through its own fixed client, a user's connection is still held to the
// network policy, and the board client reaches nothing but the board's host.
func TestBoardTrustedClientDoesNotWidenUserPolicy(t *testing.T) {
	b := newBoardHarness(t, func(c *Config) { c.Policy = netguard.Policy{} }) // the default: no private, no http
	gh := b.grant()
	if r := b.callTool(gh, "blerg_board_list", map[string]any{}); r.errMessage() != "" {
		t.Fatalf("the board on a private http address must be reachable: %s", r.raw)
	}
	// A user connection pointing at the very same private address is refused.
	ug := b.grantPinned("calendar", map[string]ToolGrant{"echo": {Mode: ModeAllow, Hash: "x"}}, 0)
	if r := b.callTool(ug, "echo", map[string]any{}); r.errMessage() == "" || !strings.Contains(r.errMessage(), "not permitted") {
		t.Errorf("a user connection reached a private address: %s", r.raw)
	}
	if _, err := b.gw.ListTools(context.Background(), Proof{AccountID: "acct-1", SessionID: "s"}, newUUID(t), b.up.url()); err == nil {
		t.Error("the picker's listing reached a private address")
	}
	// The board client is allowed exactly the board's host, nothing else.
	u, _ := url.Parse(b.up.url())
	other := strings.Replace(b.up.url(), u.Hostname(), "localhost", 1)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, other, strings.NewReader("{}"))
	if resp, err := b.gw.boardClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Error("the board client reached a host other than the board's")
	}
}

// ---- the HTTP exchanger against a fake core -------------------------------------------------

func fakeJWT(t *testing.T, sub string) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"sub": sub, "aud": "blerg-board"})
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestHTTPBoardExchanger(t *testing.T) {
	const sub = "5a1d0c3e-0000-4000-8000-000000000001"
	var mu sync.Mutex
	var paths []string
	var bodies []map[string]any
	var keys []string
	status := http.StatusOK
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		mu.Lock()
		paths, bodies, keys = append(paths, r.URL.Path), append(bodies, m), append(keys, r.Header.Get("X-Internal-Key"))
		st := status
		mu.Unlock()
		if st != http.StatusOK {
			http.Error(w, "not found", st)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/revoke") {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": fakeJWT(t, sub), "expires_at": "2026-01-01T12:10:00Z"})
	}))
	defer core.Close()
	ex := &HTTPBoardExchanger{BaseURL: core.URL + "/", InternalKey: "ik"}

	tok, err := ex.Exchange(context.Background(), Proof{AccountID: "acct-1", TokenID: boardTestCron}, boardTestID, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if tok.Sub != sub || tok.Token != fakeJWT(t, sub) || !tok.ExpiresAt.Equal(time.Date(2026, 1, 1, 12, 10, 0, 0, time.UTC)) {
		t.Errorf("token = %+v", tok)
	}
	if paths[0] != "/internal/tokens/exchange" || keys[0] != "ik" {
		t.Errorf("path=%s key=%s", paths[0], keys[0])
	}
	want := map[string]any{"account_id": "acct-1", "token_id": boardTestCron, "target": "board", "board_id": boardTestID, "session_ref": "sess-1"}
	if fmt.Sprint(bodies[0]) != fmt.Sprint(want) {
		t.Errorf("body = %v, want %v", bodies[0], want)
	}
	// A human proof names the session id instead.
	if _, err := ex.Exchange(context.Background(), Proof{AccountID: "acct-1", SessionID: "login"}, boardTestID, ""); err != nil {
		t.Fatal(err)
	}
	if _, has := bodies[1]["token_id"]; has || bodies[1]["session_id"] != "login" {
		t.Errorf("human body = %v", bodies[1])
	}
	if err := ex.Revoke(context.Background(), "acct-1", sub); err != nil {
		t.Fatal(err)
	}
	if paths[2] != "/internal/tokens/exchange/revoke" || bodies[2]["token_sub"] != sub || bodies[2]["account_id"] != "acct-1" {
		t.Errorf("revoke: %s %v", paths[2], bodies[2])
	}
	mu.Lock()
	status = http.StatusNotFound
	mu.Unlock()
	if _, err := ex.Exchange(context.Background(), Proof{AccountID: "acct-1", TokenID: boardTestCron}, boardTestID, ""); !errors.Is(err, ErrConnectionGone) {
		t.Errorf("404 = %v, want ErrConnectionGone", err)
	}
	if err := ex.Revoke(context.Background(), "acct-1", sub); err != nil {
		t.Errorf("revoking an already gone token is not an error: %v", err)
	}
	mu.Lock()
	status = http.StatusInternalServerError
	mu.Unlock()
	if _, err := ex.Exchange(context.Background(), Proof{AccountID: "acct-1", TokenID: boardTestCron}, boardTestID, ""); err == nil || errors.Is(err, ErrConnectionGone) {
		t.Errorf("500 = %v", err)
	}
	if _, err := ex.Exchange(context.Background(), Proof{AccountID: "acct-1"}, boardTestID, ""); err == nil {
		t.Error("no proof was accepted")
	}
}

func TestTokenSub(t *testing.T) {
	if got := tokenSub(fakeJWT(t, "abc")); got != "abc" {
		t.Errorf("sub = %q", got)
	}
	for _, bad := range []string{"", "a.b", "a.!!!.c", "a." + base64.RawURLEncoding.EncodeToString([]byte("{")) + ".c"} {
		if got := tokenSub(bad); got != "" {
			t.Errorf("tokenSub(%q) = %q, want empty", bad, got)
		}
	}
}
