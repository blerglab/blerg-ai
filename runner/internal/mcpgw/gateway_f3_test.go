package mcpgw

// Fixes from the phase 1-2 review (F3): failed-auth limiter semantics, budgeted hash refresh,
// hash-mismatch refresh, orphan grant sweep, log hygiene, netguard reason text.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const listBody = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

// MAJOR 10: the token is checked first; only failures are counted, and per (address, token) so
// one container holding a stale token cannot block the others behind the same source address.
func TestFailedAuthLimiterDoesNotBlockValidTokens(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.FailedAuthLimit = 4 })
	good := h.grant("alpha", map[string]string{"echo": "allow"}, 0)
	other := h.grant("beta", map[string]string{"echo": "allow"}, 0)
	stale := "gw_" + strings.Repeat("0", 64)

	var last int
	for range 8 {
		last = h.post(stale, "alpha", listBody, nil).status
	}
	if last != 429 {
		t.Fatalf("a token that keeps failing must end up rate limited, last status %d", last)
	}
	// Same source address, valid tokens: unaffected.
	if r := h.post(good.token, "alpha", listBody, nil); r.status != 200 {
		t.Errorf("valid token from an address with a blocked stale token: %d %s", r.status, r.raw)
	}
	if r := h.post(other.token, "beta", listBody, nil); r.status != 200 {
		t.Errorf("second valid token: %d %s", r.status, r.raw)
	}
	// A different stale token is a different offender: 401, not 429.
	if r := h.post("gw_"+strings.Repeat("1", 64), "alpha", listBody, nil); r.status != 401 {
		t.Errorf("another bad token was rate limited by the first: %d", r.status)
	}
	// The block ends with the window.
	h.clock.Advance(2 * time.Minute)
	if r := h.post(stale, "alpha", listBody, nil); r.status != 401 {
		t.Errorf("after the window: %d, want 401", r.status)
	}
}

// Requests with no token or a malformed one share the address's own (larger) allowance, and a
// valid token still passes when that allowance is spent.
func TestFailedAuthLimiterAddressAllowanceKeepsValidTokens(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.FailedAuthLimit = 3 })
	good := h.grant("alpha", map[string]string{"echo": "allow"}, 0)
	var last int
	for range 40 {
		last = h.post("", "alpha", listBody, nil).status
	}
	if last != 429 {
		t.Fatalf("token-less spraying must be rate limited, last status %d", last)
	}
	if r := h.post(good.token, "alpha", listBody, nil); r.status != 200 {
		t.Errorf("valid token while the address is over its anonymous allowance: %d", r.status)
	}
}

// MINOR 15: a full limiter evicts the oldest offender instead of ceasing to track new ones.
func TestFailLimiterEvictsOldestWhenFull(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	l := newFailLimiter(2, time.Hour, clock)
	l.max = 3
	for i := range 3 {
		l.fail(fmt.Sprintf("k%d", i))
		now = now.Add(time.Second)
	}
	l.fail("newcomer")
	l.fail("newcomer")
	if !l.blocked("newcomer") {
		t.Fatal("a new offender must be tracked even when the table is full")
	}
	if len(l.byKey) > 3 {
		t.Fatalf("table grew past its cap: %d", len(l.byKey))
	}
	l.mu.Lock()
	_, oldestStill := l.byKey["k0"]
	l.mu.Unlock()
	if oldestStill {
		t.Error("the oldest entry should have been evicted")
	}
}

func manyTools(n int) []fakeTool {
	out := make([]fakeTool, 0, n)
	for i := range n {
		out = append(out, fakeTool{Name: fmt.Sprintf("t%03d", i), Description: "d", Schema: `{"type":"object"}`})
	}
	return out
}

func callsUsed(t *testing.T, h *harness, gh grantHandle) int {
	t.Helper()
	rows, err := db.ListMCPGrantsForSession(context.Background(), h.pool, gh.sessionID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("grants: %v %v", rows, err)
	}
	return rows[0].CallsUsed
}

// MAJOR 11: one tools/call used to cost up to 100 unbudgeted upstream requests through the hash
// refresh. The refresh is capped and every page beyond the first is charged to the budget.
func TestHashRefreshIsCappedAndCharged(t *testing.T) {
	h := newHarness(t, nil, manyTools(30)...)
	h.up.mu.Lock()
	h.up.pageSize = 1
	h.up.mu.Unlock()
	gh := h.grant("alpha", map[string]string{"t000": "allow", "t025": "allow"}, 0)

	if r := h.callTool(gh, "t000", nil); resultText(r) != "ok:t000" {
		t.Fatalf("call: %s", r.raw)
	}
	_, lists, _ := h.up.snapshot()
	if lists != hashRefreshMaxPages {
		t.Errorf("refresh fetched %d pages, want the cap of %d", lists, hashRefreshMaxPages)
	}
	// 1 for the call itself + (pages-1) extra pages.
	if used := callsUsed(t, h, gh); used != hashRefreshMaxPages {
		t.Errorf("calls_used = %d, want %d (call + extra pages)", used, hashRefreshMaxPages)
	}
	// A tool beyond the refresh window is refused (fail closed), never called.
	h.clock.Advance(time.Minute)
	_, _, before := h.up.snapshot()
	if r := h.callTool(gh, "t025", nil); r.errMessage() != "unknown tool" {
		t.Errorf("tool past the refresh window: %s", r.raw)
	}
	if _, _, after := h.up.snapshot(); after != before {
		t.Error("a refused tool was forwarded")
	}
}

func TestHashRefreshStopsWhenBudgetRunsOut(t *testing.T) {
	h := newHarness(t, nil, manyTools(30)...)
	h.up.mu.Lock()
	h.up.pageSize = 1
	h.up.mu.Unlock()
	gh := h.grant("alpha", map[string]string{"t000": "allow"}, 3)
	r := h.callTool(gh, "t000", nil)
	if !strings.Contains(r.errMessage(), "budget") {
		t.Fatalf("want a budget refusal, got %s", r.raw)
	}
	if _, lists, calls := h.up.snapshot(); lists > 3 || calls != 0 {
		t.Errorf("upstream saw %d list pages and %d calls with a budget of 3", lists, calls)
	}
	if used := callsUsed(t, h, gh); used > 3 {
		t.Errorf("calls_used %d exceeds the budget", used)
	}
}

// MINOR 3: a pin is verified against a listing at most a few seconds old when it does not match:
// a tool restored (or changed) inside the cache window is re-checked before being refused, but a
// call storm cannot force a refetch more than once per gap.
func TestHashMismatchRefreshesBeforeRefusing(t *testing.T) {
	h := newHarness(t, nil, append([]fakeTool(nil), defaultTools...)...)
	gh := h.grant("alpha", map[string]string{"echo": "allow"}, 0)
	h.up.setDescription("echo", "changed for a moment")
	h.clock.Advance(time.Minute)
	if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 0 {
		t.Fatalf("changed tool must be hidden: %v", got)
	}
	// Restored; the cached listing still says "changed", but it is old enough to re-check.
	h.up.setDescription("echo", "echoes")
	h.clock.Advance(hashRefreshGap + time.Second)
	if r := h.callTool(gh, "echo", nil); resultText(r) != "ok:echo" {
		t.Fatalf("restored tool refused on a stale cache: %s", r.raw)
	}
	// Genuinely changed: refused, and a burst inside the gap costs one refetch, not one per call.
	h.up.setDescription("echo", "evil")
	h.clock.Advance(h.gw.cfg.HashCacheTTL + time.Second) // the cached (matching) listing has expired
	_, before, _ := h.up.snapshot()
	for range 5 {
		if r := h.callTool(gh, "echo", nil); !strings.Contains(r.errMessage(), "changed on the server") {
			t.Fatalf("changed tool: %s", r.raw)
		}
	}
	if _, after, _ := h.up.snapshot(); after-before != 1 {
		t.Errorf("a burst of refused calls caused %d upstream listings, want 1", after-before)
	}
}

// MINOR 4: Prune also sweeps grants whose session has ended or does not exist.
func TestPruneSweepsOrphanGrants(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	live := h.grant("alpha", map[string]string{"echo": "allow"}, 0)
	dead := h.grant("beta", map[string]string{"echo": "allow"}, 0)
	gone := h.grant("gamma", map[string]string{"echo": "allow"}, 0)
	const daemonID = "00000000-0000-0000-0000-0000000000f3"
	if err := db.UpsertDaemon(ctx, h.pool, daemonID, "d", "local", "/r"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, h.pool, live.sessionID, daemonID, "running", "/r/p", "p", "T", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, h.pool, dead.sessionID, daemonID, "ended", "/r/p", "p", "T", ""); err != nil {
		t.Fatal(err)
	}
	// MINOR 28: only grants older than the resume guard are swept; a young one may be a resume's.
	h.gw.Prune(ctx)
	for _, g := range []grantHandle{dead, gone} {
		if r := h.post(g.token, g.name, listBody, nil); r.status != 200 {
			t.Errorf("a grant younger than the guard was swept: %d", r.status)
		}
	}
	if _, err := h.pool.Exec(ctx, `UPDATE session_mcp_grants SET created_at = now() - interval '11 minutes'`); err != nil {
		t.Fatal(err)
	}
	h.gw.Prune(ctx)
	if r := h.post(live.token, "alpha", listBody, nil); r.status != 200 {
		t.Errorf("live session's grant swept: %d", r.status)
	}
	for _, g := range []grantHandle{dead, gone} {
		if r := h.post(g.token, g.name, listBody, nil); r.status != 401 {
			t.Errorf("grant of an ended/missing session survived the sweep: %d", r.status)
		}
	}
}

// MINOR 15: budget and busy refusals are not logged (no row per refused call).
func TestRefusalsAreNotLogged(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grant("alpha", map[string]string{"echo": "allow"}, 1)
	if r := h.callTool(gh, "echo", nil); resultText(r) != "ok:echo" {
		t.Fatalf("first call: %s", r.raw)
	}
	for range 3 {
		if r := h.callTool(gh, "echo", nil); !strings.Contains(r.errMessage(), "budget") {
			t.Fatalf("want budget refusal: %s", r.raw)
		}
	}
	var n int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM mcp_call_log WHERE session_id = $1 AND outcome IN ('budget','busy')`, gh.sessionID).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d budget/busy rows logged, %v", n, err)
	}
}

// MINOR 2: an upstream failure never shows the agent (or the log) netguard's reason text or a URL.
func TestUpstreamMessageIsFixedForPolicyErrors(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("connection url refused: %w", fmt.Errorf("%w: address not permitted", netguard.ErrBlockedAddress)),
		errConnectionURLRefused,
		fmt.Errorf(`Post "https://up.example/mcp?key=SECRET": %w`, netguard.ErrBlockedAddress),
	} {
		msg := upstreamMessage(err)
		for _, bad := range []string{"netguard", "SECRET", "up.example", "10."} {
			if strings.Contains(msg, bad) {
				t.Errorf("upstreamMessage(%v) = %q leaks %q", err, msg, bad)
			}
		}
	}
	if got := upstreamMessage(errConnectionURLRefused); got == "" || got == "the upstream call failed" {
		t.Errorf("a refused connection URL should say so, got %q", got)
	}
}

// MINOR 13: the credential fetch never follows a redirect, and only core's own uniform 404 means
// "connection gone".
func TestHTTPCoreClientRedirectAndNotFound(t *testing.T) {
	var elsewhere int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere++
		_, _ = w.Write([]byte(`{"url":"https://x.example/mcp"}`))
	}))
	defer other.Close()
	mode := "redirect"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "redirect":
			http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
		case "route":
			http.NotFound(w, r)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := &HTTPCoreClient{BaseURL: srv.URL, InternalKey: "k", HTTP: srv.Client()}
	proof := Proof{AccountID: "a", SessionID: "s"}
	if _, err := c.Token(context.Background(), proof, "c"); err == nil {
		t.Error("a redirect was followed")
	}
	if elsewhere != 0 {
		t.Errorf("redirect target reached %d times", elsewhere)
	}
	mode = "route"
	if _, err := c.Token(context.Background(), proof, "c"); err == nil || errors.Is(err, ErrConnectionGone) {
		t.Errorf("missing route = %v, want a plain error", err)
	}
	mode = "uniform"
	if _, err := c.Token(context.Background(), proof, "c"); !errors.Is(err, ErrConnectionGone) {
		t.Errorf("uniform 404 = %v, want ErrConnectionGone", err)
	}
}

func TestErrorClassNeverIncludesURL(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf(`Post "https://up.example/mcp?key=SECRET": dial tcp 192.0.2.1:443: connect: refused`),
		errors.Join(errors.New("x"), errors.New("https://up.example/?token=SECRET")),
	} {
		c := errorClass(err)
		if strings.Contains(c, "SECRET") || strings.Contains(c, "up.example") || strings.Contains(c, "http") {
			t.Errorf("errorClass = %q", c)
		}
	}
}
