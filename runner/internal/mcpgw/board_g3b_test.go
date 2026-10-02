package mcpgw

// Round-2 fixes (G3b): board state and exchange-token revocation that survive session end races
// and a runner restart, the address limiter for well-formed but unknown tokens, the hash cache
// merge, and the core-404 guards of the HTTP exchanger.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func (b *boardHarness) staleGrant(gh grantHandle) *db.MCPGrant {
	b.t.Helper()
	gr, err := db.MCPGrantByTokenHash(context.Background(), b.pool, hashToken(gh.token))
	if err != nil {
		b.t.Fatal(err)
	}
	return gr
}

func (b *boardHarness) subRows() int {
	b.t.Helper()
	var n int
	if err := b.pool.QueryRow(context.Background(), `SELECT count(*) FROM board_exchange_subs`).Scan(&n); err != nil {
		b.t.Fatal(err)
	}
	return n
}

// MINOR 15: a request past authentication must not be able to recreate the per-session state once
// the grant is gone, or it would mint a token nothing revokes.
func TestBoardStateNotResurrectedAfterSessionEnd(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	b.callTool(gh, "blerg_board_list", map[string]any{})
	gr := b.staleGrant(gh) // what a request past authentication holds
	if err := DeleteGrantsForSession(context.Background(), b.pool, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	b.gw.RevokeSession(context.Background(), gh.sessionID)
	if st := b.gw.stateFor(context.Background(), gr); st != nil {
		t.Fatal("stateFor recreated the state of a session whose grant is gone")
	}
	if b.ex.exchanges() != 1 {
		t.Errorf("exchanges = %d, want 1", b.ex.exchanges())
	}
}

// A resume issues new grants before the old session's asynchronous revoke runs: that revoke must
// close the old grant's state only, never the new one.
func TestBoardResumeNotClosedByLateRevoke(t *testing.T) {
	b := newBoardHarness(t, nil)
	old := b.grant() // sub-1
	b.callTool(old, "blerg_board_list", map[string]any{})
	ctx := context.Background()
	if err := DeleteGrantsForSession(ctx, b.pool, old.sessionID); err != nil {
		t.Fatal(err)
	}
	toks, err := CreateGrants(ctx, b.pool, old.sessionID, "acct-1", Proof{AccountID: "acct-1", TokenID: boardTestCron},
		[]GrantSpec{BoardGrantSpec(boardTestID, b.up.url())})
	if err != nil {
		t.Fatal(err)
	}
	fresh := old
	fresh.token = toks[BoardConnectionID]
	if r := b.callTool(fresh, "blerg_board_list", map[string]any{}); r.errMessage() != "" { // sub-2
		t.Fatalf("resumed call: %s", r.raw)
	}
	b.gw.RevokeSession(ctx, old.sessionID) // the old end's async revoke, late
	if got := b.ex.revokedList(); !slices.Equal(got, []string{"acct-1:sub-1"}) {
		t.Fatalf("revoked = %v, want only the old grant's token", got)
	}
	if r := b.callTool(fresh, "blerg_board_list", map[string]any{}); r.errMessage() != "" {
		t.Errorf("the resumed grant's state was closed: %s", r.raw)
	}
	if b.ex.exchanges() != 2 {
		t.Errorf("exchanges = %d, want 2 (the resumed state kept its token)", b.ex.exchanges())
	}
}

// A state that exists when the grant is deleted behind its back must not hand out a new token.
func TestBoardExchangeAfterGrantGoneIsRevokedAtOnce(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	b.callTool(gh, "blerg_board_list", map[string]any{})
	gr := b.staleGrant(gh)
	st := b.gw.stateFor(context.Background(), gr)
	if st == nil {
		t.Fatal("no state")
	}
	if err := DeleteGrantsForSession(context.Background(), b.pool, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	b.clock.Advance(9 * time.Minute) // the cached token is due for renewal
	if _, err := st.credential(context.Background(), gr); err == nil {
		t.Fatal("a state whose grant is gone handed out a credential")
	}
	b.gw.RevokeSession(context.Background(), gh.sessionID)
	got := b.ex.revokedList()
	slices.Sort(got)
	if !slices.Equal(got, []string{"acct-1:sub-1", "acct-1:sub-2"}) {
		t.Errorf("revoked = %v, want both tokens", got)
	}
}

// MINOR 16: issued subs are persisted, so a restart (a fresh Gateway) still revokes them.
func TestBoardExchangeSubsSurviveARestart(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	b.callTool(gh, "blerg_board_list", map[string]any{}) // sub-1
	if b.subRows() != 1 {
		t.Fatalf("persisted subs = %d, want 1", b.subRows())
	}
	if err := DeleteGrantsForSession(context.Background(), b.pool, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	// The runner restarted before it could revoke: a new Gateway knows nothing in memory.
	gw2 := New(b.gw.cfg)
	gw2.RevokeSession(context.Background(), gh.sessionID)
	if got := b.ex.revokedList(); !slices.Equal(got, []string{"acct-1:sub-1"}) {
		t.Fatalf("revoked after restart = %v, want sub-1", got)
	}
	if b.subRows() != 0 {
		t.Errorf("rows left after a revoke = %d", b.subRows())
	}
}

func TestBoardStartupSweepRevokesSubsOfGoneGrants(t *testing.T) {
	b := newBoardHarness(t, nil)
	live := b.grant()
	b.callTool(live, "blerg_board_list", map[string]any{}) // sub-1, grant stays
	dead := b.grant()
	b.callTool(dead, "blerg_board_list", map[string]any{}) // sub-2
	if err := DeleteGrantsForSession(context.Background(), b.pool, dead.sessionID); err != nil {
		t.Fatal(err)
	}
	gw2 := New(b.gw.cfg)
	gw2.Prune(context.Background()) // what Run does at start
	if got := b.ex.revokedList(); !slices.Equal(got, []string{"acct-1:sub-2"}) {
		t.Fatalf("revoked = %v, want only the dead grant's sub-2", got)
	}
	if b.subRows() != 1 {
		t.Errorf("rows = %d, want the live grant's one", b.subRows())
	}
}

func TestBoardSubKeptWhenRevokeFailsAndRetried(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	b.callTool(gh, "blerg_board_list", map[string]any{})
	if err := DeleteGrantsForSession(context.Background(), b.pool, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	b.ex.revokeErr = fmt.Errorf("core down")
	b.gw.RevokeSession(context.Background(), gh.sessionID)
	if b.subRows() != 1 {
		t.Fatalf("a sub that could not be revoked must stay for a retry, rows = %d", b.subRows())
	}
	b.ex.revokeErr = nil
	b.gw.Prune(context.Background())
	if got := b.ex.revokedList(); !slices.Equal(got, []string{"acct-1:sub-1"}) || b.subRows() != 0 {
		t.Errorf("after the retry: revoked=%v rows=%d", got, b.subRows())
	}
}

func TestBoardExpiredSubsAreDroppedWithoutRevoking(t *testing.T) {
	b := newBoardHarness(t, nil)
	gh := b.grant()
	b.callTool(gh, "blerg_board_list", map[string]any{})
	b.clock.Advance(11 * time.Minute)
	b.gw.Prune(context.Background())
	if b.subRows() != 0 || len(b.ex.revokedList()) != 0 {
		t.Errorf("rows=%d revoked=%v, want the expired row dropped and nothing revoked", b.subRows(), b.ex.revokedList())
	}
}

// MINOR 31: a well-formed token that matches nothing counts against the address and is turned away
// with 429 past the address allowance; a valid token still passes.
func TestFailedAuthWellFormedUnknownTokensHitAddressLimit(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.FailedAuthLimit = 2 }) // address allowance 20
	good := h.grant("alpha", map[string]string{"echo": "allow"}, 0)
	last := 0
	for i := range 40 {
		last = h.post(fmt.Sprintf("gw_%064x", i+1), "alpha", listBody, nil).status
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("spraying distinct well-formed unknown tokens: last status %d, want 429", last)
	}
	if r := h.post(good.token, "alpha", listBody, nil); r.status != 200 {
		t.Errorf("valid token from a sprayed address: %d", r.status)
	}
}

// MINOR 33: a capped refresh merges into the cached hashes instead of replacing them, so a granted
// tool beyond the window stays usable from the cache.
func TestPartialHashRefreshKeepsToolsBeyondTheCap(t *testing.T) {
	h := newHarness(t, nil, manyTools(30)...)
	h.up.mu.Lock()
	h.up.pageSize = 1
	h.up.mu.Unlock()
	gh := h.grant("alpha", map[string]string{"t000": "allow", "t025": "allow"}, 0)
	if names := toolNames(h.call(gh, "tools/list", map[string]any{})); len(names) != 2 { // a full listing
		t.Fatalf("full listing = %v", names)
	}
	h.clock.Advance(h.gw.cfg.HashCacheTTL + time.Second)
	if r := h.callTool(gh, "t000", nil); resultText(r) != "ok:t000" { // a refresh capped at 10 pages
		t.Fatalf("t000: %s", r.raw)
	}
	if r := h.callTool(gh, "t025", nil); resultText(r) != "ok:t025" {
		t.Errorf("a granted tool beyond the refresh window lost its cached hash: %s", r.raw)
	}
}

// The picker's and the launch check's listings are bounded by the same page cap.
func TestLiveListingsAreCappedAtTheRefreshPages(t *testing.T) {
	h := newHarness(t, nil, manyTools(30)...)
	h.up.mu.Lock()
	h.up.pageSize = 1
	h.up.mu.Unlock()
	proof := Proof{AccountID: "acct-1", SessionID: "login-1"}
	got, err := h.gw.LiveToolHashes(context.Background(), proof, "conn-1", h.up.url())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != hashRefreshMaxPages {
		t.Errorf("LiveToolHashes returned %d tools, want %d (the page cap)", len(got), hashRefreshMaxPages)
	}
	tools, err := h.gw.ListTools(context.Background(), proof, "conn-1", h.up.url())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != hashRefreshMaxPages {
		t.Errorf("ListTools returned %d tools, want %d", len(tools), hashRefreshMaxPages)
	}
}

// MINOR 26/27: the exchanger follows no redirect and reads only core's own uniform 404 as "not
// found"; a router's or proxy's 404 is an error.
func TestHTTPBoardExchangerCoreGuards(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer other.Close()
	mode := "router404"
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch mode {
		case "router404":
			http.NotFound(w, r) // "404 page not found"
		case "empty404":
			w.WriteHeader(http.StatusNotFound)
		case "redirect":
			http.Redirect(w, r, other.URL+"/x", http.StatusTemporaryRedirect)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer core.Close()
	ex := &HTTPBoardExchanger{BaseURL: core.URL, InternalKey: "ik"}
	proof := Proof{AccountID: "acct-1", TokenID: boardTestCron}
	ctx := context.Background()

	for _, m := range []string{"router404", "empty404"} {
		mode = m
		if _, err := ex.Exchange(ctx, proof, boardTestID, ""); err == nil || errors.Is(err, ErrConnectionGone) {
			t.Errorf("%s exchange = %v, want a plain error", m, err)
		}
		if err := ex.Revoke(ctx, "acct-1", "sub"); err == nil {
			t.Errorf("%s revoke was acknowledged", m)
		}
	}
	mode = "redirect"
	if _, err := ex.Exchange(ctx, proof, boardTestID, ""); err == nil || errors.Is(err, ErrConnectionGone) {
		t.Errorf("redirect exchange = %v", err)
	}
	if err := ex.Revoke(ctx, "acct-1", "sub"); err == nil {
		t.Error("a redirect was acknowledged as a revoke")
	}
	if hits.Load() != 0 {
		t.Errorf("a redirect was followed %d times (it carries the internal key)", hits.Load())
	}
	mode = "core404"
	if _, err := ex.Exchange(ctx, proof, boardTestID, ""); !errors.Is(err, ErrConnectionGone) {
		t.Errorf("core's own 404 = %v, want ErrConnectionGone", err)
	}
	if err := ex.Revoke(ctx, "acct-1", "sub"); err != nil {
		t.Errorf("core's own 404 revoke = %v", err)
	}
}
