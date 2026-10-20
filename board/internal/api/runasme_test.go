package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/coremint"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/contracts/identity"
)

// fakeMinter stands in for core's POST /internal/tokens/board-automation(/revoke): it mints a
// run-sessions token signed by the fake core, and records every call.
type fakeMinter struct {
	t       *testing.T
	mu      sync.Mutex
	n       int
	owner   string // whose token core hands back; "" = the account asked for
	err     error
	mints   []mintCall
	revokes []revokeCall
}

type mintCall struct{ account, boardID, boardName, access string }
type revokeCall struct{ account, tokenID, boardID string }

func (f *fakeMinter) Mint(_ context.Context, account, boardID, boardName, access string) (coremint.Minted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mints = append(f.mints, mintCall{account, boardID, boardName, access})
	if f.err != nil {
		return coremint.Minted{}, f.err
	}
	f.n++
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", f.n)
	owner := account
	if f.owner != "" {
		owner = f.owner
	}
	exp := time.Now().Add(90 * 24 * time.Hour)
	tok := signClaims(f.t, identity.Claims{
		Sub: id, Aud: "blerg-runner", Kind: "agent", OnBehalfOf: owner, Lineage: owner,
		Caps: []string{"session.start"}, IssuedAt: time.Now().Add(-time.Minute).Unix(), ExpiresAt: exp.Unix(),
	})
	return coremint.Minted{ID: id, Token: tok, Name: "Board " + boardName + " automation", ExpiresAt: exp}, nil
}

func (f *fakeMinter) Revoke(_ context.Context, account, tokenID, boardID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokes = append(f.revokes, revokeCall{account, tokenID, boardID})
	return nil
}

func (f *fakeMinter) revoked() []revokeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]revokeCall(nil), f.revokes...)
}

func mintedID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// runAsMeEnv is a board with no automation token, a human admin, and the fake minter wired.
func runAsMeEnv(t *testing.T) (do func(method, path, token string, body any) *http.Response, board db.Board, human string, minter *fakeMinter) {
	t.Helper()
	srv, p := testServer(t)
	human = humanToken(t, srv)
	minter = &fakeMinter{t: t}
	srvAPI.Minter = minter
	b, err := db.CreateBoard(context.Background(), p, db.BoardParams{Name: "Work", Repos: []string{"acme/app"}})
	if err != nil {
		t.Fatal(err)
	}
	do = func(method, path, token string, body any) *http.Response {
		return request(t, srv, method, path, token, nil, body)
	}
	return do, b, human, minter
}

// "Run as me" mints through core with the person's own browser proof and stores the token
// encrypted, exactly as a pasted one; the browser never sees it.
func TestRunAsMeSetsTheBoardTokenEncrypted(t *testing.T) {
	do, board, human, minter := runAsMeEnv(t)
	ctx := context.Background()
	resp := do("POST", "/api/boards/"+board.ID+"/automation/run-as-me", human, nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run as me: %d %s", resp.StatusCode, body)
	}
	if len(minter.mints) != 1 {
		t.Fatalf("mints = %d", len(minter.mints))
	}
	m := minter.mints[0]
	if m.account != humanSub || m.boardID != board.ID || m.boardName != "Work" || m.access != human {
		t.Fatalf("mint call = %+v", m)
	}
	ba, err := db.GetBoardAutomation(ctx, srvAPI.Pool, board.ID)
	if err != nil || ba.Token == "" {
		t.Fatalf("stored automation = %+v (%v)", ba, err)
	}
	if strings.Contains(body, ba.Token) {
		t.Fatal("the response carries the minted token")
	}
	var stored string
	if err := srvAPI.Pool.QueryRow(ctx, `SELECT automation_token FROM boards WHERE id = $1`, board.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "v1:") || strings.Contains(stored, ba.Token) {
		t.Fatal("the minted token is not sealed at rest")
	}
	got := map[string]any{}
	decodeBody(t, do("GET", "/api/boards/"+board.ID, human, nil), &got)
	if got["automation_token_set"] != true || got["automation_account_id"] != humanSub || got["automation_token_minted"] != true ||
		got["automation_token_expires_at"] == nil {
		t.Fatalf("board after run as me = %v", got)
	}
}

// Replacing or removing a token Blerg minted revokes it; a pasted token is never revoked by the
// board (it is the person's own, made elsewhere).
func TestRunAsMeReplacingAndRemovingRevokesTheMintedToken(t *testing.T) {
	do, board, human, minter := runAsMeEnv(t)
	path := "/api/boards/" + board.ID
	runAsMe := func() {
		t.Helper()
		resp := do("POST", path+"/automation/run-as-me", human, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("run as me: %d %s", resp.StatusCode, readAll(t, resp))
		}
		resp.Body.Close()
	}
	want := func(calls ...revokeCall) {
		t.Helper()
		if got := minter.revoked(); fmt.Sprint(got) != fmt.Sprint(calls) {
			t.Fatalf("revocations = %v, want %v", got, calls)
		}
	}

	runAsMe() // minted 1
	want()
	runAsMe() // minted 2 replaces 1
	want(revokeCall{humanSub, mintedID(1), board.ID})

	// Pasting a token ("Use a different token…") replaces 2 and revokes it.
	resp := do("PATCH", path, human, map[string]any{"automation_token": runSessionsToken(t)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("paste: %d %s", resp.StatusCode, readAll(t, resp))
	}
	resp.Body.Close()
	want(revokeCall{humanSub, mintedID(1), board.ID}, revokeCall{humanSub, mintedID(2), board.ID})
	got := map[string]any{}
	decodeBody(t, do("GET", path, human, nil), &got)
	if got["automation_token_minted"] != false {
		t.Fatalf("a pasted token reads as minted: %v", got["automation_token_minted"])
	}

	// Run as me over a pasted token revokes nothing.
	runAsMe() // minted 3
	want(revokeCall{humanSub, mintedID(1), board.ID}, revokeCall{humanSub, mintedID(2), board.ID})

	// Remove token revokes 3.
	resp = do("PATCH", path, human, map[string]any{"automation_token": ""})
	resp.Body.Close()
	want(revokeCall{humanSub, mintedID(1), board.ID}, revokeCall{humanSub, mintedID(2), board.ID}, revokeCall{humanSub, mintedID(3), board.ID})

	// Deleting the board revokes the token it minted.
	runAsMe() // minted 4
	resp = do("DELETE", path, human, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp.Body.Close()
	calls := minter.revoked()
	if last := calls[len(calls)-1]; last != (revokeCall{humanSub, mintedID(4), board.ID}) {
		t.Fatalf("delete revoked %v", last)
	}
}

func TestRunAsMeRefusals(t *testing.T) {
	do, board, human, minter := runAsMeEnv(t)
	path := "/api/boards/" + board.ID + "/automation/run-as-me"
	nothingStored := func(name string) {
		t.Helper()
		if ba, _ := db.GetBoardAutomation(context.Background(), srvAPI.Pool, board.ID); ba.Token != "" {
			t.Fatalf("%s: a token was stored", name)
		}
	}
	for _, tc := range []struct {
		name, caller string
		want         int
	}{
		{"native service key", "svc-key-123", http.StatusForbidden},
		{"a member without board.admin", memberToken(t), http.StatusForbidden},
		{"a core agent token", testCoreToken(t, "agent", fullHumanCaps), http.StatusForbidden},
	} {
		resp := do("POST", path, tc.caller, nil)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, resp.StatusCode, readAll(t, resp), tc.want)
		}
		resp.Body.Close()
	}
	if len(minter.mints) != 0 {
		t.Fatalf("refused callers reached core: %v", minter.mints)
	}
	nothingStored("refused callers")

	// Core refuses (the browser session is no longer live): nothing stored, and the person is told.
	minter.err = coremint.ErrRefused
	resp := do("POST", path, human, nil)
	if body := readAll(t, resp); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "sign") {
		t.Errorf("core refused: %d %s", resp.StatusCode, body)
	}
	minter.err = coremint.ErrRateLimited
	if resp := do("POST", path, human, nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("core rate limit: %d", resp.StatusCode)
	}
	minter.err = nil
	nothingStored("core refused")

	// Core handing back a token for someone else is never stored, and that token is revoked.
	minter.owner = "another-account"
	resp = do("POST", path, human, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("foreign token: %d %s", resp.StatusCode, readAll(t, resp))
	}
	resp.Body.Close()
	nothingStored("foreign token")
	if r := minter.revoked(); len(r) != 1 || r[0].tokenID != mintedID(1) {
		t.Errorf("the unusable minted token was not revoked: %v", r)
	}
	minter.owner = ""

	// Not set up on this install: a clear 503 that points at pasting instead.
	srvAPI.Minter = nil
	resp = do("POST", path, human, nil)
	if body := readAll(t, resp); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "paste") {
		t.Errorf("no minter: %d %s", resp.StatusCode, body)
	}
	srvAPI.Minter = minter

	// Without the encryption key nothing is minted at all.
	_ = db.SetAutomationKey(nil)
	resp = do("POST", path, human, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("no key: %d, want 422", resp.StatusCode)
	}
	if len(minter.mints) != 3 {
		t.Errorf("mints = %d, want 3 (none without the key)", len(minter.mints))
	}
}

// New boards run as their creator when the create form asks; the operator's default reaches the
// form through /api/me.
func TestCreateBoardRunAsCreator(t *testing.T) {
	do, _, human, minter := runAsMeEnv(t)

	var me map[string]any
	srvAPI.RunAsCreatorDefault = true
	decodeBody(t, do("GET", "/api/me", human, nil), &me)
	if me["run_as_me_available"] != true || me["run_as_creator_default"] != true {
		t.Fatalf("/api/me = %v", me)
	}
	srvAPI.RunAsCreatorDefault = false
	decodeBody(t, do("GET", "/api/me", human, nil), &me)
	if me["run_as_creator_default"] != false {
		t.Fatalf("operator off: /api/me = %v", me)
	}
	srvAPI.Minter = nil
	decodeBody(t, do("GET", "/api/me", human, nil), &me)
	if me["run_as_me_available"] != false {
		t.Fatalf("no minter: /api/me = %v", me)
	}
	srvAPI.Minter = minter

	var b map[string]any
	resp := do("POST", "/api/boards", human, map[string]any{"name": "Fresh", "run_as_me": true})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, readAll(t, resp))
	}
	decodeBody(t, resp, &b)
	if b["automation_token_set"] != true || b["automation_account_id"] != humanSub || b["automation_error"] != nil {
		t.Fatalf("created board = %v", b)
	}
	if last := minter.mints[len(minter.mints)-1]; last.boardName != "Fresh" || last.boardID != b["id"] {
		t.Fatalf("mint call = %+v", last)
	}

	// Unchecked: the board has no token and core is not asked.
	n := len(minter.mints)
	decodeBody(t, do("POST", "/api/boards", human, map[string]any{"name": "Manual"}), &b)
	if b["automation_token_set"] != false || len(minter.mints) != n {
		t.Fatalf("unchecked create = %v (mints %d → %d)", b, n, len(minter.mints))
	}

	// A failed mint does not lose the board: it is created and says why it has no token.
	minter.err = errors.Join(coremint.ErrUnavailable, errors.New("down"))
	resp = do("POST", "/api/boards", human, map[string]any{"name": "Later", "run_as_me": true})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create with failing mint: %d", resp.StatusCode)
	}
	decodeBody(t, resp, &b)
	if b["automation_token_set"] != false || b["automation_error"] == nil || b["id"] == nil {
		t.Fatalf("create with failing mint = %v", b)
	}
}

// A card on a board with no token offers "Run as me" next to Run; once the person runs as
// themselves, the run-target chooser appears for them.
func TestCardOffersRunAsMeThenTheChooser(t *testing.T) {
	do, board, human, _ := runAsMeEnv(t)
	fake := &fakeRunner{lifecycle: "running"}
	driver := &targetRunner{identityRunner: identityRunner{fake}}
	srvAPI.SetRunner(api.RunnerConfig{Driver: driver, PublicURL: "https://b.test", AgentURL: "http://b.svc"})
	res, err := db.CreateCard(context.Background(), srvAPI.Pool, board.ID, db.CardParams{Title: strPtr("build it"), Repos: &[]string{"acme/app"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	targets := func(token string) map[string]any {
		t.Helper()
		out := map[string]any{}
		decodeBody(t, do("GET", "/api/cards/"+res.Card.ID+"/run-targets", token, nil), &out)
		return out
	}
	before := targets(human)
	if before["can_choose"] != false || before["can_run_as_me"] != true || before["board_id"] != board.ID ||
		!strings.Contains(fmt.Sprint(before["automation_problem"]), "no automation token") {
		t.Fatalf("before = %v", before)
	}
	// A card writer who is not a board admin is told, not offered the button.
	if m := targets(memberToken(t)); m["can_run_as_me"] != false || m["automation_problem"] == nil {
		t.Fatalf("member = %v", m)
	}
	resp := do("POST", "/api/boards/"+board.ID+"/automation/run-as-me", human, nil)
	resp.Body.Close()
	after := targets(human)
	if after["can_choose"] != true || after["automation_problem"] != nil {
		t.Fatalf("after = %v", after)
	}
	if list, _ := after["targets"].([]any); len(list) == 0 {
		t.Fatalf("no targets after run as me: %v", after)
	}
}
