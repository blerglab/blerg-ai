package mcpgw

// Board access levels (0.4.4): the launch board's gateway connection serves at the session's
// current level, read on every call, and CallBoard makes one session board call with a credential
// exchanged for it and revoked after it.

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

var (
	wantReadTools = []string{"blerg_board_get", "blerg_board_list", "blerg_board_schema", "blerg_column_list", "blerg_card_search", "blerg_card_get"}
	wantFullTools = []string{"blerg_card_create", "blerg_card_update", "blerg_card_move", "blerg_card_comment", "blerg_card_link"}
)

// levelSession is a real session row owned by acct-1 with the board's gateway grant, as a launch
// that chose the board gives it.
func (b *boardHarness) levelSession() grantHandle {
	b.t.Helper()
	ctx := context.Background()
	var daemon, sid string
	if err := b.pool.QueryRow(ctx, `INSERT INTO daemons (name, mode, repos_root) VALUES ('d', 'host', '/r') RETURNING id::text`).Scan(&daemon); err != nil {
		b.t.Fatal(err)
	}
	if err := b.pool.QueryRow(ctx, `INSERT INTO sessions (daemon_id, project_path, repo, spawning_account_id) VALUES ($1::uuid, '/p', 'r', 'acct-1') RETURNING id::text`, daemon).Scan(&sid); err != nil {
		b.t.Fatal(err)
	}
	toks, err := CreateGrants(ctx, b.pool, sid, "acct-1", Proof{AccountID: "acct-1", SessionID: "login-1"}, []GrantSpec{BoardGrantSpec(boardTestID, b.up.url())})
	if err != nil {
		b.t.Fatal(err)
	}
	return grantHandle{sessionID: sid, connectionID: BoardConnectionID, name: BuiltinBoard, token: toks[BoardConnectionID]}
}

func (b *boardHarness) setLevel(sid, level string) *db.ProposalGrant {
	b.t.Helper()
	g, err := db.InsertProposalGrant(context.Background(), b.pool, db.ProposalGrant{SessionID: sid, AccountID: "acct-1", Kind: db.ProposalGrantBoard,
		BoardID: boardTestID, BoardName: "Seven", ProofSessionID: "login-1", CreatedVia: "panel", Level: level})
	if err != nil {
		b.t.Fatal(err)
	}
	return g
}

func TestBoardToolLevelsCoverTheFixedSetExactly(t *testing.T) {
	var all []string
	for _, n := range BoardToolNames() {
		switch BoardToolLevel(n) {
		case db.BoardLevelRead:
			if !slices.Contains(wantReadTools, n) {
				t.Errorf("%s is read level", n)
			}
		case db.BoardLevelFull:
			if !slices.Contains(wantFullTools, n) {
				t.Errorf("%s is full level", n)
			}
		default:
			t.Errorf("%s has no level", n)
		}
		all = append(all, n)
	}
	want := append(append([]string(nil), wantReadTools...), wantFullTools...)
	sort.Strings(all)
	sort.Strings(want)
	if !slices.Equal(all, want) {
		t.Fatalf("tools %v, want %v", all, want)
	}
	for _, n := range []string{"blerg_card_archive", "blerg_board_update", "blerg_column_create", ""} {
		if BoardToolLevel(n) != "" || BoardLevelAllows(db.BoardLevelFull, n) {
			t.Errorf("%q is callable", n)
		}
	}
	// Each level allows exactly its tools and refuses the next level's.
	for _, n := range wantReadTools {
		if BoardLevelAllows(db.BoardLevelPropose, n) || !BoardLevelAllows(db.BoardLevelRead, n) || !BoardLevelAllows(db.BoardLevelFile, n) || !BoardLevelAllows(db.BoardLevelFull, n) {
			t.Errorf("read tool %s", n)
		}
	}
	for _, n := range wantFullTools {
		if BoardLevelAllows(db.BoardLevelRead, n) || BoardLevelAllows(db.BoardLevelFile, n) || !BoardLevelAllows(db.BoardLevelFull, n) {
			t.Errorf("full tool %s", n)
		}
	}
}

func TestLaunchBoardGrantServesTheCurrentLevelOnEveryCall(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.levelSession()
	listed := func() []string {
		got := toolNames(b.call(gh, "tools/list", map[string]any{}))
		sort.Strings(got)
		return got
	}
	all := append([]string(nil), wantBoardTools...)
	sort.Strings(all)
	// No row: the launch board is full, as before 0.4.4.
	if got := listed(); !slices.Equal(got, all) {
		t.Fatalf("no row: listed %v", got)
	}
	if r := b.callTool(gh, "blerg_card_create", map[string]any{"board_id": boardTestID}); r.errMessage() != "" {
		t.Fatalf("no row: create refused: %s", r.raw)
	}

	// Lowered to read: the next list and call see it.
	b.setLevel(gh.sessionID, db.BoardLevelRead)
	read := append([]string(nil), wantReadTools...)
	sort.Strings(read)
	if got := listed(); !slices.Equal(got, read) {
		t.Fatalf("read: listed %v", got)
	}
	_, _, before := b.up.snapshot()
	r := b.callTool(gh, "blerg_card_create", map[string]any{"board_id": boardTestID})
	if !strings.Contains(r.errMessage(), "needs full access") || !strings.Contains(r.errMessage(), "Permissions") ||
		!strings.Contains(r.errMessage(), `call the blerg_board tool with {"tool":"access"}`) || !strings.Contains(r.errMessage(), "before telling the person") {
		t.Fatalf("read: create = %s", r.raw)
	}
	if _, _, after := b.up.snapshot(); after != before {
		t.Fatal("a refused write reached the board")
	}
	if r := b.callTool(gh, "blerg_card_get", map[string]any{"card_id": "c1"}); r.errMessage() != "" {
		t.Fatalf("read: get refused: %s", r.raw)
	}

	// File without asking does not read cards through the gateway beyond read... it includes read.
	b.setLevel(gh.sessionID, db.BoardLevelFile)
	if r := b.callTool(gh, "blerg_card_search", map[string]any{"board_id": boardTestID}); r.errMessage() != "" {
		t.Fatalf("file: search refused: %s", r.raw)
	}
	if r := b.callTool(gh, "blerg_card_move", map[string]any{}); !strings.Contains(r.errMessage(), "needs full access") {
		t.Fatalf("file: move = %s", r.raw)
	}

	// Propose (or a revoked row): nothing through the gateway.
	b.setLevel(gh.sessionID, db.BoardLevelPropose)
	if got := listed(); len(got) != 0 {
		t.Fatalf("propose: listed %v", got)
	}
	if r := b.callTool(gh, "blerg_card_get", map[string]any{}); !strings.Contains(r.errMessage(), "needs read access") {
		t.Fatalf("propose: get = %s", r.raw)
	}

	// Raised to full again: write works at the next call.
	g := b.setLevel(gh.sessionID, db.BoardLevelFull)
	if r := b.callTool(gh, "blerg_card_update", map[string]any{}); r.errMessage() != "" {
		t.Fatalf("full: update refused: %s", r.raw)
	}
	// Revoked: the latest row no longer holds, so the board is at propose (not back to the launch full).
	if _, err := db.RevokeProposalGrant(context.Background(), b.pool, g.ID, "acct-1", "revoked"); err != nil {
		t.Fatal(err)
	}
	if r := b.callTool(gh, "blerg_card_get", map[string]any{}); !strings.Contains(r.errMessage(), "needs read access") {
		t.Fatalf("revoked: get = %s", r.raw)
	}
}

func TestCallBoardReadsWithAReadOnlyCredentialAndRevokesIt(t *testing.T) {
	b := newBoardHarness(t, nil)
	proof := Proof{AccountID: "acct-1", SessionID: "login-1"}
	res, err := b.gw.CallBoard(context.Background(), BoardCall{Proof: proof, BoardID: boardTestID, SessionRef: "s-1", Tool: "blerg_card_get",
		Arguments: []byte(`{"card_id":"c1"}`), ReadOnly: true})
	if err != nil || !strings.Contains(string(res), "ok:blerg_card_get") {
		t.Fatalf("read: %s %v", res, err)
	}
	if b.ex.exchanges() != 1 || !b.ex.readOnly[0] || b.ex.boards[0] != boardTestID || b.ex.proofs[0] != proof || b.ex.refs[0] != "s-1" {
		t.Fatalf("exchange: n=%d ro=%v boards=%v", b.ex.exchanges(), b.ex.readOnly, b.ex.boards)
	}
	if got := b.authHeader(); got != "Bearer tok-1" {
		t.Fatalf("upstream credential %q", got)
	}
	if !slices.Equal(b.ex.revokedList(), []string{"acct-1:sub-1"}) {
		t.Fatalf("revoked %v", b.ex.revokedList())
	}
	// A write is exchanged with the full credential, once, and revoked too.
	if _, err := b.gw.CallBoard(context.Background(), BoardCall{Proof: proof, BoardID: boardTestID, SessionRef: "s-1", Tool: "blerg_card_create", Arguments: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if b.ex.exchanges() != 2 || b.ex.readOnly[1] || len(b.ex.revokedList()) != 2 {
		t.Fatalf("write exchange: ro=%v revoked=%v", b.ex.readOnly, b.ex.revokedList())
	}
	// Refused before any exchange: a write marked read-only, a tool outside the set, no proof.
	for _, c := range []BoardCall{
		{Proof: proof, BoardID: boardTestID, Tool: "blerg_card_create", ReadOnly: true},
		{Proof: proof, BoardID: boardTestID, Tool: "blerg_card_archive"},
		{Proof: proof, BoardID: "", Tool: "blerg_card_get", ReadOnly: true},
		{Proof: Proof{AccountID: "acct-1"}, BoardID: boardTestID, Tool: "blerg_card_get", ReadOnly: true},
	} {
		if _, err := b.gw.CallBoard(context.Background(), c); !errors.Is(err, ErrBoardCallInvalid) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if b.ex.exchanges() != 2 {
		t.Fatalf("a refused call exchanged: %d", b.ex.exchanges())
	}
}

// writeOnlyExchanger has no read-only exchange.
type writeOnlyExchanger struct{ inner *fakeExchanger }

func (w writeOnlyExchanger) Exchange(ctx context.Context, p Proof, board, ref string) (BoardToken, error) {
	return w.inner.Exchange(ctx, p, board, ref)
}
func (w writeOnlyExchanger) Revoke(ctx context.Context, a, s string) error {
	return w.inner.Revoke(ctx, a, s)
}

func TestCallBoardNeverReadsWithAWriteCredential(t *testing.T) {
	var ex *fakeExchanger
	b := newBoardHarness(t, func(c *Config) {
		ex = c.Board.(*fakeExchanger)
		c.Board = writeOnlyExchanger{inner: ex}
	})
	_, err := b.gw.CallBoard(context.Background(), BoardCall{Proof: Proof{AccountID: "acct-1", SessionID: "l"}, BoardID: boardTestID, Tool: "blerg_card_get", ReadOnly: true})
	if !errors.Is(err, errBoardAccess) || ex.exchanges() != 0 {
		t.Fatalf("err=%v exchanges=%d", err, ex.exchanges())
	}
}

func TestCallBoardRefusesAnExpiringCredentialAndNeverExchangesAgain(t *testing.T) {
	b := newBoardHarness(t, nil)
	b.ex.ttl = 2 * time.Second
	_, _, before := b.up.snapshot()
	_, err := b.gw.CallBoard(context.Background(), BoardCall{Proof: Proof{AccountID: "acct-1", SessionID: "l"}, BoardID: boardTestID, Tool: "blerg_card_get", ReadOnly: true})
	if !errors.Is(err, errBoardAccess) {
		t.Fatalf("err=%v", err)
	}
	if _, _, after := b.up.snapshot(); after != before || b.ex.exchanges() != 1 || len(b.ex.revokedList()) != 1 {
		t.Fatalf("calls=%d exchanges=%d revoked=%v", after-before, b.ex.exchanges(), b.ex.revokedList())
	}
	// The one-call state never falls back to exchanging a credential of its own.
	st := &grantState{g: b.gw, builtin: true, ephemeral: true}
	if _, err := st.boardCredential(context.Background(), &db.MCPGrant{Builtin: BuiltinBoard}); !errors.Is(err, errBoardAccess) || b.ex.exchanges() != 1 {
		t.Fatalf("ephemeral exchanged: %v", err)
	}
}

// While revokes fail, an account stops minting session board credentials once
// maxUnrevokedBoardTokens of them are still live, whatever the per-minute rate allows; it resumes
// as they expire. Another account is unaffected.
func TestCallBoardStopsMintingWhileRevokesFail(t *testing.T) {
	b := newBoardHarness(t, nil)
	b.ex.revokeErr = errors.New("core unavailable")
	proof := Proof{AccountID: "acct-1", SessionID: "login-1"}
	call := func(p Proof) error {
		_, err := b.gw.CallBoard(context.Background(), BoardCall{Proof: p, BoardID: boardTestID, SessionRef: "s-1", Tool: "blerg_card_get",
			Arguments: []byte(`{"card_id":"c1"}`), ReadOnly: true})
		return err
	}
	for i := 0; i < maxUnrevokedBoardTokens; i++ {
		if err := call(proof); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if err := call(proof); !errors.Is(err, ErrBoardRevokesFailing) || !strings.Contains(UpstreamMessage(err), "retry in a few minutes") {
		t.Fatalf("past the unrevoked allowance: %v", err)
	}
	if b.ex.exchanges() != maxUnrevokedBoardTokens {
		t.Fatalf("a refused call exchanged: %d", b.ex.exchanges())
	}
	if err := call(Proof{AccountID: "acct-2", SessionID: "login-2"}); err != nil {
		t.Fatalf("another account was paused: %v", err)
	}
	b.clock.Advance(11 * time.Minute) // every unrevoked credential has expired
	b.ex.revokeErr = nil
	if err := call(proof); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}

// The unrevoked allowance follows SetUnrevokedBoardTokenLimit at the next call (core's setting),
// and a limit below 1 restores the default.
func TestCallBoardUnrevokedLimitIsSettable(t *testing.T) {
	b := newBoardHarness(t, nil)
	b.ex.revokeErr = errors.New("core unavailable")
	b.gw.SetUnrevokedBoardTokenLimit(3)
	call := func() error {
		_, err := b.gw.CallBoard(context.Background(), BoardCall{Proof: Proof{AccountID: "acct-1", SessionID: "login-1"}, BoardID: boardTestID,
			SessionRef: "s-1", Tool: "blerg_card_get", Arguments: []byte(`{"card_id":"c1"}`), ReadOnly: true})
		return err
	}
	for i := 0; i < 3; i++ {
		if err := call(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if err := call(); !errors.Is(err, ErrBoardRevokesFailing) {
		t.Fatalf("past the set allowance of 3: %v", err)
	}
	b.gw.SetUnrevokedBoardTokenLimit(4)
	if err := call(); err != nil {
		t.Fatalf("after raising it to 4: %v", err)
	}
	b.gw.SetUnrevokedBoardTokenLimit(0)
	if got := b.gw.unrevokedBoardTokenLimit(); got != maxUnrevokedBoardTokens {
		t.Fatalf("reset limit %d", got)
	}
}
