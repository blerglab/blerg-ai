package server

// The built-in board connection on a cron's start (cronboard.go), the revocation of its tokens at
// every session end, and the failure card (cronfailurecard.go). Fixture: cronstart_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/cron"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

const testBoardID = "board-7"

// fakeBoardExchanger is core's exchange and revoke.
type fakeBoardExchanger struct {
	mu      sync.Mutex
	n       int
	err     error
	proofs  []mcpgw.Proof
	boards  []string
	revoked []string
}

func (f *fakeBoardExchanger) Exchange(_ context.Context, p mcpgw.Proof, boardID, _ string) (mcpgw.BoardToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return mcpgw.BoardToken{}, f.err
	}
	f.n++
	f.proofs, f.boards = append(f.proofs, p), append(f.boards, boardID)
	return mcpgw.BoardToken{Token: fmt.Sprintf("btok-%d", f.n), Sub: fmt.Sprintf("bsub-%d", f.n), ExpiresAt: time.Now().Add(10 * time.Minute)}, nil
}

func (f *fakeBoardExchanger) Revoke(_ context.Context, account, sub string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, account+":"+sub)
	return nil
}

func (f *fakeBoardExchanger) snapshot() (n int, revoked []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n, append([]string(nil), f.revoked...)
}

// fakeCards is the board's card endpoint as the failure card sees it: cards keyed by external id.
type fakeCards struct {
	mu      sync.Mutex
	cards   map[string]CronFailureCard
	posts   int
	proofs  []mcpgw.Proof
	boards  []string
	err     error
	release chan struct{} // when set, PostCard blocks until it is closed
	entered chan struct{}
}

func newFakeCards() *fakeCards {
	return &fakeCards{cards: map[string]CronFailureCard{}, entered: make(chan struct{}, 8)}
}

func (f *fakeCards) PostCard(_ context.Context, p mcpgw.Proof, boardID string, card CronFailureCard) error {
	f.mu.Lock()
	rel := f.release
	f.mu.Unlock()
	if rel != nil {
		f.entered <- struct{}{}
		<-rel
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts++
	f.proofs, f.boards = append(f.proofs, p), append(f.boards, boardID)
	if f.err != nil {
		return f.err
	}
	f.cards[card.ExternalID] = card // same external id: updated in place
	return nil
}

func (f *fakeCards) count() (posts, cards int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts, len(f.cards)
}

// boardCronFx is a cron fixture with the board configured: a real gateway with a fake exchange, and
// the failure card's fake board.
type boardCronFx struct {
	*cronFx
	ex    *fakeBoardExchanger
	cards *fakeCards
	gw    *mcpgw.Gateway
}

func newBoardCronFx(t *testing.T) *boardCronFx {
	t.Helper()
	cf := newCronFx(t)
	ex, cards := &fakeBoardExchanger{}, newFakeCards()
	gw := mcpgw.New(mcpgw.Config{
		DB: cf.pool, Core: cf.tokens, Board: ex, BoardMCPURL: cf.up.url(),
		Policy: netguard.Policy{AllowHTTPHosts: []string{"127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}},
	})
	cfg := *cf.hub.mcpStart.get()
	cfg.Board = gw
	cf.api.setMCPStart(&cfg)
	cf.svc.cards = cards
	t.Cleanup(func() { setBoardAccess(nil) })
	return &boardCronFx{cronFx: cf, ex: ex, cards: cards, gw: gw}
}

func boardOf(id string) func(*db.Cron) { return func(c *db.Cron) { c.BoardID = ptr(id) } }

func TestCronWithBoardGetsTheBuiltinBoardGrant(t *testing.T) {
	bf := newBoardCronFx(t)
	ctx := context.Background()
	c := bf.newCron(boardOf(testBoardID))
	sid, err := bf.svc.Start(ctx, c, bf.run(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	gs := bf.grants(sid)
	if len(gs) != 2 {
		t.Fatalf("grants = %d, want the connection and the board", len(gs))
	}
	var board *db.MCPGrant
	for i := range gs {
		if gs[i].Builtin == mcpgw.BuiltinBoard {
			board = &gs[i]
		}
	}
	if board == nil {
		t.Fatalf("no builtin board grant in %+v", gs)
	}
	if board.Name != "board" || board.ConnectionID != mcpgw.BoardConnectionID || board.BuiltinRef != testBoardID ||
		board.ProofKind != "token_id" || board.ProofValue != cronTok || board.AccountID != mcpAcct || board.URLSnapshot != bf.up.url() {
		t.Errorf("board grant = %+v", *board)
	}
	for _, n := range mcpgw.BoardToolNames() {
		if board.Tools[n].Mode != "allow" {
			t.Errorf("board tool %s = %+v", n, board.Tools[n])
		}
	}
	// The session's config carries the board like any other server, with its own token.
	cfg := bf.secretConfig(sid)
	names := map[string]string{}
	for _, s := range cfg.Servers {
		names[s.Name] = s.Token
	}
	if len(names) != 2 || names["board"] == "" || names[mcpConn] == "" || !bf.tokenBelongsTo(names["board"], sid) {
		t.Errorf("delivered servers = %v", names)
	}
	// No board token was exchanged (or revoked) at start: the cron's liveness was confirmed through
	// the status route, and the first real exchange belongs to the session's first use of the board.
	if n, revoked := bf.ex.snapshot(); n != 0 || len(revoked) != 0 {
		t.Errorf("the start exchanged %d board tokens and revoked %v, want none", n, revoked)
	}
}

func TestCronWithBoardAndNoConnectionsGetsOnlyTheBoard(t *testing.T) {
	bf := newBoardCronFx(t)
	c := bf.newCron(func(c *db.Cron) { c.BoardID = ptr(testBoardID); c.MCP = json.RawMessage(`[]`) })
	sid, err := bf.svc.Start(context.Background(), c, bf.run(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	gs := bf.grants(sid)
	if len(gs) != 1 || gs[0].Builtin != mcpgw.BuiltinBoard {
		t.Fatalf("grants = %+v, want exactly the board", gs)
	}
	msg := bf.secretConfig(sid)
	if len(msg.Servers) != 1 || msg.Servers[0].Name != "board" {
		t.Errorf("servers = %+v", msg.Servers)
	}
}

func TestCronWithoutBoardGetsNoBoardGrant(t *testing.T) {
	bf := newBoardCronFx(t)
	c := bf.newCron(nil) // no board id
	sid, err := bf.svc.Start(context.Background(), c, bf.run(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, g := range bf.grants(sid) {
		if g.Builtin != "" {
			t.Errorf("a cron without a board got %+v", g)
		}
	}
	if n, _ := bf.ex.snapshot(); n != 0 {
		t.Errorf("%d token exchanges for a cron without a board", n)
	}
	// And with neither a board nor connections there is no grant at all, as before.
	bare := bf.newCron(func(c *db.Cron) { c.MCP = json.RawMessage(`[]`); c.TokenID = "c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0c2" })
	bf.core.live["c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0c2"] = true
	sid2, err := bf.svc.Start(context.Background(), bare, bf.run(bare))
	if err != nil {
		t.Fatalf("Start bare: %v", err)
	}
	if gs := bf.grants(sid2); len(gs) != 0 {
		t.Errorf("a bare cron got grants: %+v", gs)
	}
}

func TestCronBoardExchangeFailureFailsTheRunVisibly(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*boardCronFx)
		want  string
	}{
		{"no board configured", func(bf *boardCronFx) {
			cfg := *bf.hub.mcpStart.get()
			cfg.Board = nil
			bf.api.setMCPStart(&cfg)
		}, envBoardURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bf := newBoardCronFx(t)
			tc.setup(bf)
			c := bf.newCron(boardOf(testBoardID))
			_, err := bf.svc.Start(context.Background(), c, bf.run(c))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Start = %v, want an error containing %q", err, tc.want)
			}
			if isCapacity(err) || errors.Is(err, cron.ErrNotRunnable) {
				t.Errorf("a board failure must fail the run, not hold or skip it: %v", err)
			}
			bf.assertNothingWritten("board exchange failure")
			bf.svc.WaitCards()
			if posts, _ := bf.cards.count(); posts != 0 {
				t.Errorf("a failure card was attempted although the board exchange itself failed (%d)", posts)
			}
		})
	}
}

// ---- revocation at every end of a session ------------------------------------------------------

type recBoard struct {
	mu      sync.Mutex
	revoked []string
}

func (r *recBoard) BoardConfigured() bool { return true }
func (r *recBoard) BoardMCPURL() string   { return "http://board.invalid/mcp" }
func (r *recBoard) RevokeSession(_ context.Context, sessionID string) {
	r.mu.Lock()
	r.revoked = append(r.revoked, sessionID)
	r.mu.Unlock()
}
func (r *recBoard) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.revoked...)
}

func TestBoardTokensAreRevokedWhereverGrantsAreDeleted(t *testing.T) {
	bf := newBoardCronFx(t)
	rec := &recBoard{}
	cfg := *bf.hub.mcpStart.get()
	cfg.Board = rec
	bf.api.setMCPStart(&cfg) // installs rec as what the end paths revoke through
	ctx := context.Background()

	// The shared primitive every end path calls: abort, cluster start failure, session_ended,
	// runner stop, the reconciler (revokeSessionTokens) and the stop path below.
	revokeSessionGrants(ctx, bf.pool, "s-direct", "test")
	revokeSessionTokens(ctx, bf.pool, "s-tokens", "test")
	abortSpawnSessionToken(ctx, bf.pool, "s-abort")

	// A real cron session stopped by its cron's lifecycle (pause/delete/watchdog).
	c := bf.newCron(boardOf(testBoardID))
	sid, err := bf.svc.Start(ctx, c, bf.run(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if n, err := bf.svc.StopCronSessions(ctx, c.ID); err != nil || n != 1 {
		t.Fatalf("StopCronSessions = %d, %v", n, err)
	}
	waitBoardRevocations()
	got := rec.list()
	for _, want := range []string{"s-direct", "s-tokens", "s-abort", sid} {
		if !slices.Contains(got, want) {
			t.Errorf("session %s was not revoked; revoked = %v", want, got)
		}
	}
	if gs := bf.grants(sid); len(gs) != 0 {
		t.Errorf("grants left after the stop: %+v", gs)
	}
}

// ---- the failure card ---------------------------------------------------------------------------

func TestCronFailureCardPostedOnceAndUpdatedInPlace(t *testing.T) {
	bf := newBoardCronFx(t)
	ctx := context.Background()
	c := bf.newCron(boardOf(testBoardID))
	bf.list.conns[0].Status = "needs_auth"
	bf.svc.cfg.Now = bf.clock.Now

	for i := range 3 {
		_, err := bf.svc.Start(ctx, c, bf.run(c))
		if !isCredential(err) {
			t.Fatalf("Start %d = %v, want a credential failure", i, err)
		}
		bf.svc.WaitCards()
		bf.clock.Advance(cardPerCronGap + time.Second) // one post per cron per gap
	}
	posts, cards := bf.cards.count()
	if posts != 3 || cards != 1 {
		t.Fatalf("posts=%d distinct cards=%d, want 3 posts updating 1 card", posts, cards)
	}
	card := bf.cards.cards["cron-failure:"+c.ID]
	if !strings.HasPrefix(card.Title, "Cron Morning digest could not run: ") || !strings.Contains(card.Title, "needs attention") {
		t.Errorf("title = %q", card.Title)
	}
	if !strings.Contains(card.Body, "needs_auth") {
		t.Errorf("body lacks the reason: %q", card.Body)
	}
	if bf.cards.proofs[0] != (mcpgw.Proof{AccountID: mcpAcct, TokenID: cronTok}) || bf.cards.boards[0] != testBoardID {
		t.Errorf("card posted with proof %+v board %q", bf.cards.proofs[0], bf.cards.boards[0])
	}
	// Nothing was started for a failed run.
	bf.assertNothingWritten("credential failure")
}

func TestCronFailureCardOnlyForRealFailuresOfACronWithABoard(t *testing.T) {
	bf := newBoardCronFx(t)
	ctx := context.Background()

	// No board id: a credential failure posts nothing.
	plain := bf.newCron(nil)
	bf.list.conns[0].Status = "needs_auth"
	if _, err := bf.svc.Start(ctx, plain, bf.run(plain)); !isCredential(err) {
		t.Fatalf("Start = %v", err)
	}
	bf.svc.WaitCards()
	if posts, _ := bf.cards.count(); posts != 0 {
		t.Errorf("a cron without a board posted a card (%d)", posts)
	}

	// With a board: a run that is merely held (no capacity) or whose cron is gone is not a failure.
	bf.list.conns[0].Status = "ok"
	c := bf.newCron(func(c *db.Cron) {
		c.BoardID = ptr(testBoardID)
		c.Runtime = "cluster"
		c.TokenID = "c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0c3"
	})
	bf.core.live["c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0c3"] = true
	bf.core.statusErr = errors.New("core down") // a held run
	if _, err := bf.svc.Start(ctx, c, bf.run(c)); !isCapacity(err) {
		t.Fatalf("Start = %v, want held", err)
	}
	bf.core.statusErr = nil
	if _, err := db.DeleteCron(ctx, bf.pool, mcpAcct, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := bf.svc.Start(ctx, c, bf.run(c)); !errors.Is(err, cron.ErrNotRunnable) {
		t.Fatalf("Start = %v, want not runnable", err)
	}
	// A revoked token is paused, and no proof remains to post with: nothing is attempted.
	rev := bf.newCron(func(c *db.Cron) { c.BoardID = ptr(testBoardID); c.TokenID = "c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0c4" })
	if _, err := bf.svc.Start(ctx, rev, bf.run(rev)); err == nil {
		t.Fatal("a cron with a dead token started")
	}
	bf.svc.WaitCards()
	if posts, _ := bf.cards.count(); posts != 0 {
		t.Errorf("a card was posted for a held, deleted or revoked run (%d)", posts)
	}
}

func TestCronFailureCardNeverBlocksOrBreaksTheStart(t *testing.T) {
	bf := newBoardCronFx(t)
	ctx := context.Background()
	c := bf.newCron(boardOf(testBoardID))
	bf.list.conns[0].Status = "needs_auth"

	// A board that hangs: Start returns at once with its own error.
	bf.cards.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := bf.svc.Start(ctx, c, bf.run(c)); done <- err }()
	select {
	case err := <-done:
		if !isCredential(err) {
			t.Errorf("Start = %v, want the credential failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start waited for the card")
	}
	<-bf.cards.entered
	close(bf.cards.release)
	bf.svc.WaitCards()

	// A board that errors: the start's own error is unchanged, nothing panics.
	bf.cards.mu.Lock()
	bf.cards.release, bf.cards.err = nil, errors.New("board returned 500")
	bf.cards.mu.Unlock()
	if _, err := bf.svc.Start(ctx, c, bf.run(c)); !isCredential(err) {
		t.Errorf("Start = %v", err)
	}
	bf.svc.WaitCards()
	// No card poster configured at all: same.
	bf.svc.cards = nil
	if _, err := bf.svc.Start(ctx, c, bf.run(c)); !isCredential(err) {
		t.Errorf("Start = %v", err)
	}
}

func TestCronFailureCardReasonCarriesNoSecrets(t *testing.T) {
	in := "connection \"mail\" failed: Get https://user:hunter2@mail.internal.example:8443/mcp?key=abc123: Authorization: Bearer eyJhbGciOiJFZERTQSJ9.eyJzdWIiOiJ4In0.c2ln and sk-ant-oat01-" + strings.Repeat("a", 40) + "\nsecond line"
	got := sanitizeCardReason(in)
	for _, leak := range []string{"hunter2", "mail.internal.example", "abc123", "eyJhbGci", "sk-ant-oat01", "second line", "\n"} {
		if strings.Contains(got, leak) {
			t.Errorf("reason %q still contains %q", got, leak)
		}
	}
	if !strings.Contains(got, "connection \"mail\" failed") {
		t.Errorf("reason lost its plain text: %q", got)
	}
	long := sanitizeCardReason(strings.Repeat("word ", 500))
	if n := len([]rune(long)); n > cardReasonMax+1 {
		t.Errorf("reason is %d runes, cap %d", n, cardReasonMax)
	}
	if got := sanitizeCardReason("  \n\t "); got != "unknown reason" {
		t.Errorf("empty reason = %q", got)
	}
}

func TestHTTPBoardCardsPostsToTheBoardWithAnExchangeToken(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotAuth string
	var gotBody map[string]any
	status := http.StatusCreated
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		gotPath, gotAuth = r.Method+" "+r.URL.Path, r.Header.Get("Authorization")
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(status)
	}))
	defer board.Close()
	ex := &fakeBoardExchanger{}
	cards := &HTTPBoardCards{BaseURL: board.URL + "/", Exchanger: ex}
	proof := mcpgw.Proof{AccountID: mcpAcct, TokenID: cronTok}
	card := CronFailureCard{ExternalID: "cron-failure:abc", Title: "Cron X could not run: y", Body: "b"}
	if err := cards.PostCard(context.Background(), proof, "b/1", card); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if gotPath != "POST /api/boards/b%2F1/cards" && gotPath != "POST /api/boards/b/1/cards" {
		t.Errorf("request = %q", gotPath)
	}
	if gotAuth != "Bearer btok-1" || gotBody["external_id"] != "cron-failure:abc" || gotBody["title"] != card.Title || gotBody["body"] != "b" {
		t.Errorf("auth=%q body=%v", gotAuth, gotBody)
	}
	mu.Unlock()
	if n, revoked := ex.snapshot(); n != 1 || !slices.Equal(revoked, []string{mcpAcct + ":bsub-1"}) {
		t.Errorf("the card's token was not revoked: n=%d revoked=%v", n, revoked)
	}
	// A refusal (the admission gate, a missing board) is an error; the token is still revoked.
	mu.Lock()
	status = http.StatusForbidden
	mu.Unlock()
	if err := cards.PostCard(context.Background(), proof, "b1", card); err == nil || strings.Contains(err.Error(), "Bearer") {
		t.Errorf("a 403 = %v", err)
	}
	if _, revoked := ex.snapshot(); len(revoked) != 2 {
		t.Errorf("revoked = %v", revoked)
	}
	// The exchange failing is an error and nothing is posted.
	ex.err = mcpgw.ErrConnectionGone
	if err := cards.PostCard(context.Background(), proof, "b1", card); err == nil {
		t.Error("an exchange failure posted a card")
	}
}

func TestBoardURLsFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, tc := range []struct {
		name string
		env  map[string]string
		base string
		mcp  string
	}{
		{"unset", nil, "", ""},
		{"base only", map[string]string{envBoardURL: "http://blerg-board:8080/"}, "http://blerg-board:8080", "http://blerg-board:8080/mcp"},
		{"mcp override", map[string]string{envBoardURL: "http://b:8080", envBoardMCPURL: "http://gw:9/board-mcp"}, "http://b:8080", "http://gw:9/board-mcp"},
		{"mcp only", map[string]string{envBoardMCPURL: "https://b.example/mcp"}, "", "https://b.example/mcp"},
		{"credentials refused", map[string]string{envBoardURL: "http://u:p@b:8080"}, "", ""},
		{"not http", map[string]string{envBoardURL: "file:///etc/passwd"}, "", ""},
		{"query refused", map[string]string{envBoardURL: "http://b:8080/?x=1"}, "", ""},
	} {
		if got := BoardURLFromEnv(env(tc.env)); got != tc.base {
			t.Errorf("%s: base = %q, want %q", tc.name, got, tc.base)
		}
		if got := BoardMCPURLFromEnv(env(tc.env)); got != tc.mcp {
			t.Errorf("%s: mcp = %q, want %q", tc.name, got, tc.mcp)
		}
	}
}
