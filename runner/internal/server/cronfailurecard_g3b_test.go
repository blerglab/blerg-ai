package server

// Round-2 fixes (G3b) for the failure card, the board preflight and the core clients: name and
// reason sanitising, throttling and the global bound, gate answers, archived cards, the single
// client, no mint at cron start, a rejected board MCP address, and the core 404/redirect guards.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func TestSanitizeCardName(t *testing.T) {
	for in, want := range map[string]string{
		"Morning digest":                   "Morning digest",
		"[click](http://evil.example) me":  "clickhttp//evil.example me", // no link syntax survives
		"**bold** `code` <b>x</b> # head":  "bold code bx/b head",
		"line1\nline2\r\n\x00\x1b[31mred":  "line1 line2 31mred",
		"pipe | table\u202eRTL":            "pipe table RTL",
		"@everyone ![img](x)":              "everyone imgx",
		"   ":                              "(unnamed)",
		"":                                 "(unnamed)",
		"a\\b_c~d{e}":                      "abcde",
		strings.Repeat("n", cardNameMax+9): strings.Repeat("n", cardNameMax) + "…",
	} {
		if got := sanitizeCardName(in); got != want {
			t.Errorf("sanitizeCardName(%q) = %q, want %q", in, got, want)
		}
	}
	card := newCronFailureCard(&db.Cron{ID: "c1", Name: "[click](http://x.example) `*hi*` <b>"}, nil, "a reason")
	if strings.ContainsAny(card.Title, "[]()`*<>") || strings.Contains(card.Body, "](") {
		t.Errorf("markup got through into the card: %q / %q", card.Title, card.Body)
	}
}

func TestCardReasonRedactsBareAddressesAndKeepsAFixedVocabulary(t *testing.T) {
	for _, in := range []string{
		"dial tcp 192.0.2.5:8080: connect: connection refused",
		"lookup core.internal on 198.51.100.10:53: no such host",
		"connect to [fe80::1]:443 failed",
		"reach blerg-core:8080 failed",
		"host mail.example.com unreachable",
	} {
		got := sanitizeCardReason(in)
		for _, leak := range []string{"192.0.2.5", "8080", "core.internal", "198.51.100.10", "fe80", "blerg-core", "mail.example.com"} {
			if strings.Contains(got, leak) {
				t.Errorf("sanitizeCardReason(%q) = %q still has %q", in, got, leak)
			}
		}
	}
	// A raw library/network error never becomes the reason: one of a fixed set does.
	raw := &net.OpError{Op: "dial", Err: errors.New("refused")}
	if got := cardReasonOf(fmt.Errorf("core unreachable: %w", raw)); got != "a service the run needs could not be reached" {
		t.Errorf("network error reason = %q", got)
	}
	if got := cardReasonOf(errors.New("pq: connection to 192.0.2.3:5432 refused")); got != "the run could not be started (see the runner's logs)" {
		t.Errorf("unknown error reason = %q", got)
	}
	// The start path's own sentences pass through (sanitised).
	if got := cardReasonOf(credentialErrorf("connection %q needs attention (status %q) at 192.0.2.9:80", "mail", "needs_auth")); !strings.Contains(got, `connection "mail" needs attention`) || strings.Contains(got, "192.0.2.9") {
		t.Errorf("credential reason = %q", got)
	}
}

func TestCardLimiterPerCronGapAndGlobalBound(t *testing.T) {
	var l cardLimiter
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if !l.admit("a", now) {
		t.Fatal("first post refused")
	}
	l.done()
	if l.admit("a", now.Add(cardPerCronGap-time.Second)) {
		t.Error("a second post inside the gap was admitted")
	}
	if !l.admit("a", now.Add(cardPerCronGap+time.Second)) {
		t.Error("a post after the gap was refused")
	}
	l.done()
	// Global: four in flight, the fifth (another cron) is refused until one finishes.
	for i := range cardMaxConcurrent {
		if !l.admit(fmt.Sprintf("c%d", i), now) {
			t.Fatalf("post %d refused below the cap", i)
		}
	}
	if l.admit("extra", now) {
		t.Error("more than cardMaxConcurrent posts in flight")
	}
	l.done()
	if !l.admit("extra", now) {
		t.Error("a slot freed by done() was not reusable")
	}
}

func TestCronFailureCardThrottledPerCron(t *testing.T) {
	bf := newBoardCronFx(t)
	ctx := context.Background()
	c := bf.newCron(boardOf(testBoardID))
	bf.list.conns[0].Status = "needs_auth"
	bf.svc.cfg.Now = bf.clock.Now
	for i := range 5 {
		if _, err := bf.svc.Start(ctx, c, bf.run(c)); !isCredential(err) {
			t.Fatalf("Start %d = %v", i, err)
		}
		bf.svc.WaitCards()
	}
	if posts, _ := bf.cards.count(); posts != 1 {
		t.Fatalf("five failures inside the gap made %d posts, want 1", posts)
	}
	bf.clock.Advance(cardPerCronGap + time.Second)
	_, _ = bf.svc.Start(ctx, c, bf.run(c)) // the failure is the point
	bf.svc.WaitCards()
	if posts, _ := bf.cards.count(); posts != 2 {
		t.Errorf("after the gap: %d posts, want 2", posts)
	}
}

func TestCronFailureCardGateAnswerLoggedOncePerHour(t *testing.T) {
	bf := newBoardCronFx(t)
	ctx := context.Background()
	c := bf.newCron(boardOf(testBoardID))
	bf.list.conns[0].Status = "needs_auth"
	bf.svc.cfg.Now = bf.clock.Now
	var mu sync.Mutex
	var lines []string
	bf.svc.cfg.Logf = func(f string, a ...any) { mu.Lock(); lines = append(lines, fmt.Sprintf(f, a...)); mu.Unlock() }
	bf.cards.mu.Lock()
	bf.cards.err = &cardGateError{Status: 409}
	bf.cards.mu.Unlock()
	gateLines := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, l := range lines {
			if strings.Contains(l, "gate answered 409") {
				n++
			}
		}
		return n
	}
	for range 4 { // each past the per-cron gap, all inside the hour
		_, _ = bf.svc.Start(ctx, c, bf.run(c)) // the failure is the point
		bf.svc.WaitCards()
		bf.clock.Advance(cardPerCronGap + time.Second)
	}
	if n := gateLines(); n != 1 {
		t.Fatalf("gate answers logged %d times inside an hour, want 1: %v", n, lines)
	}
	bf.clock.Advance(cardGateLogEvery)
	_, _ = bf.svc.Start(ctx, c, bf.run(c)) // the failure is the point
	bf.svc.WaitCards()
	if n := gateLines(); n != 2 {
		t.Errorf("after the hour: %d log lines, want 2", n)
	}
}

// MINOR 18/19 (f): the cron start no longer mints and revokes a board token as a preflight.
func TestCronWithBoardStartsWithoutExchangingAToken(t *testing.T) {
	bf := newBoardCronFx(t)
	c := bf.newCron(boardOf(testBoardID))
	if _, err := bf.svc.Start(context.Background(), c, bf.run(c)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if n, revoked := bf.ex.snapshot(); n != 0 || len(revoked) != 0 {
		t.Errorf("the start exchanged %d tokens and revoked %v; the first exchange belongs to the session's first use", n, revoked)
	}
	var rows int
	if err := bf.pool.QueryRow(context.Background(), `SELECT count(*) FROM board_exchange_subs`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("board_exchange_subs rows = %d, %v", rows, err)
	}
}

// ---- HTTPBoardCards against a fake board --------------------------------------------------------

type fakeBoardAPI struct {
	mu       sync.Mutex
	columns  string
	live     string
	archived string
	status   int // the POST's answer
	posts    []map[string]any
	gets     atomic.Int32
}

func (f *fakeBoardAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/columns"):
			f.gets.Add(1)
			_, _ = io.WriteString(w, f.columns)
		case r.Method == http.MethodGet && r.URL.Query().Get("archived") == "1":
			f.gets.Add(1)
			_, _ = io.WriteString(w, f.archived)
		case r.Method == http.MethodGet:
			f.gets.Add(1)
			_, _ = io.WriteString(w, f.live)
		default:
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			f.posts = append(f.posts, m)
			w.WriteHeader(f.status)
		}
	})
}

const fakeColumns = `[{"id":"col-todo","name":"To do","is_terminal":false},{"id":"col-doing","name":"Doing","is_terminal":false},{"id":"col-done","name":"Done","is_terminal":true}]`

func newBoardAPIFx(t *testing.T) (*fakeBoardAPI, *HTTPBoardCards) {
	t.Helper()
	api := &fakeBoardAPI{columns: fakeColumns, live: `[]`, archived: `[]`, status: http.StatusCreated}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	return api, &HTTPBoardCards{BaseURL: srv.URL, Exchanger: &fakeBoardExchanger{}}
}

func TestHTTPBoardCardsBringsAnArchivedOrDoneCardBack(t *testing.T) {
	proof := mcpgw.Proof{AccountID: mcpAcct, TokenID: cronTok}
	card := CronFailureCard{ExternalID: "cron-failure:abc", Title: "t", Body: "b"}
	cases := []struct {
		name, live, archived, wantCol string
	}{
		{"new card", `[]`, `[]`, ""},
		{"live in an open column", `[{"column_id":"col-doing","external_id":"cron-failure:abc"}]`, `[]`, ""},
		{"live in the done column", `[{"column_id":"col-done","external_id":"cron-failure:abc"}]`, `[]`, "col-todo"},
		{"archived", `[]`, `[{"column_id":null,"external_id":"cron-failure:abc"}]`, "col-todo"},
		{"someone else's archived card", `[]`, `[{"column_id":null,"external_id":"other"}]`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, cards := newBoardAPIFx(t)
			api.live, api.archived = tc.live, tc.archived
			if err := cards.PostCard(context.Background(), proof, "b1", card); err != nil {
				t.Fatal(err)
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if len(api.posts) != 1 {
				t.Fatalf("posts = %d", len(api.posts))
			}
			got, _ := api.posts[0]["column_id"].(string)
			if got != tc.wantCol {
				t.Errorf("column_id = %q, want %q", got, tc.wantCol)
			}
		})
	}
}

func TestHTTPBoardCardsLookupFailureStillPosts(t *testing.T) {
	api, cards := newBoardAPIFx(t)
	api.columns = `not json`
	if err := cards.PostCard(context.Background(), mcpgw.Proof{AccountID: mcpAcct, TokenID: cronTok}, "b1", CronFailureCard{ExternalID: "x", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.posts) != 1 {
		t.Errorf("posts = %d, want the card posted without the lookup", len(api.posts))
	}
}

func TestHTTPBoardCardsGateAnswersAreTyped(t *testing.T) {
	for _, status := range []int{http.StatusAccepted, http.StatusConflict, http.StatusUnprocessableEntity} {
		api, cards := newBoardAPIFx(t)
		api.status = status
		err := cards.PostCard(context.Background(), mcpgw.Proof{AccountID: mcpAcct, TokenID: cronTok}, "b1", CronFailureCard{ExternalID: "x", Title: "t"})
		var gate *cardGateError
		if !errors.As(err, &gate) || gate.Status != status {
			t.Errorf("status %d: err = %v, want a cardGateError", status, err)
		}
	}
	api, cards := newBoardAPIFx(t)
	api.status = http.StatusInternalServerError
	err := cards.PostCard(context.Background(), mcpgw.Proof{AccountID: mcpAcct, TokenID: cronTok}, "b1", CronFailureCard{ExternalID: "x", Title: "t"})
	var gate *cardGateError
	if err == nil || errors.As(err, &gate) {
		t.Errorf("a 500 = %v, want a plain error", err)
	}
}

func TestHTTPBoardCardsBuildsItsClientOnce(t *testing.T) {
	_, cards := newBoardAPIFx(t)
	a, b := cards.httpClient(), cards.httpClient()
	if a != b {
		t.Error("the board client was built twice")
	}
	for range 3 {
		_ = cards.PostCard(context.Background(), mcpgw.Proof{AccountID: mcpAcct, TokenID: cronTok}, "b1", CronFailureCard{ExternalID: "x", Title: "t"})
	}
	if cards.httpClient() != a {
		t.Error("PostCard replaced the client")
	}
}

// MINOR 20: a rejected BLERG_RUNNER_BOARD_MCP_URL is said so, once, instead of silently using the fallback.
func TestBoardMCPURLRejectionIsLogged(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	got := BoardMCPURLFromEnv(env(map[string]string{envBoardURL: "http://board:8080", envBoardMCPURL: "file:///etc/passwd"}))
	if got != "http://board:8080/mcp" {
		t.Errorf("fallback = %q", got)
	}
	if !strings.Contains(buf.String(), envBoardMCPURL) {
		t.Errorf("the rejection was not logged: %q", buf.String())
	}
	buf.Reset()
	BoardMCPURLFromEnv(env(map[string]string{envBoardURL: "http://board:8080"}))
	BoardMCPURLFromEnv(env(map[string]string{envBoardMCPURL: "https://b.example/mcp"}))
	if buf.Len() != 0 {
		t.Errorf("logged although nothing was rejected: %q", buf.String())
	}
}

// ---- MINOR 26/27: the internal clients never follow redirects and trust only core's own 404 -------

func TestCoreClientsRejectRedirectsAndForeign404s(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer other.Close()
	mode := "router404"
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch mode {
		case "router404":
			http.NotFound(w, r)
		case "empty404":
			w.WriteHeader(http.StatusNotFound)
		case "redirect":
			http.Redirect(w, r, other.URL+"/x", http.StatusTemporaryRedirect)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer core.Close()
	cc := &HTTPCronCore{BaseURL: core.URL, InternalKey: "ik"}
	mc := &HTTPCronMinter{BaseURL: core.URL, InternalKey: "ik"}
	ctx := context.Background()
	for _, m := range []string{"router404", "empty404", "redirect"} {
		mode = m
		if err := cc.RevokeToken(ctx, mcpAcct, cronTok); err == nil {
			t.Errorf("%s: RevokeToken acknowledged", m)
		}
		if _, err := cc.TokenLive(ctx, mcpAcct, cronTok); err == nil {
			t.Errorf("%s: TokenLive gave a verdict", m)
		}
		if _, _, err := mc.MintToken(ctx, mcpAcct, "sess", "n", 30); err == nil || errors.Is(err, errCronMintProof) {
			t.Errorf("%s: MintToken = %v, want a plain error (not a dead proof)", m, err)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("a redirect carrying the internal key was followed %d times", hits.Load())
	}
	mode = "core404"
	if err := cc.RevokeToken(ctx, mcpAcct, cronTok); err != nil {
		t.Errorf("core's own 404 revoke = %v", err)
	}
	if _, _, err := mc.MintToken(ctx, mcpAcct, "sess", "n", 30); !errors.Is(err, errCronMintProof) {
		t.Errorf("core's own 404 mint = %v, want errCronMintProof", err)
	}
}
