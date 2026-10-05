package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

type insightsFixture struct {
	api  *API
	pool *pgxpool.Pool
	mint func(sub string, caps ...string) string
	ids  struct{ aliceA, bobB, bobPrivate string }
}

// mintFor returns a function minting browser tokens for any account.
func mintFor(t *testing.T, api *API) func(sub string, caps ...string) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	return func(sub string, caps ...string) string {
		return mintRunnerToken(t, priv, "core-1", identity.Claims{
			Sub: sub, Aud: coreAuthAudience, Kind: "human", Sid: "sid-" + sub,
			Caps: caps, ExpiresAt: time.Now().Add(time.Minute).Unix(),
		})
	}
}

func seedSession(t *testing.T, pool *pgxpool.Pool, daemon, account, runtime, status string, private bool, started time.Time, ended *time.Time) string {
	t.Helper()
	ctx := context.Background()
	id := newUUID()
	if err := db.InsertSession(ctx, pool, id, daemon, status, "/w/p", "org/p", "title-"+account, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET kind='agent', runtime=$2, spawning_account_id=$3, private=$4, started_at=$5, ended_at=$6 WHERE id=$1`,
		id, runtime, account, private, started, ended); err != nil {
		t.Fatal(err)
	}
	return id
}

var seqCounter int64

func seedEvent(t *testing.T, pool *pgxpool.Pool, session string, ts time.Time, kind string, payload any) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	seqCounter++
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO agent_events (session_id, seq, client_event_id, kind, payload, ts) VALUES ($1,$2,$3,$4,$5,$6)`,
		session, seqCounter, newUUID(), kind, string(raw), ts); err != nil {
		t.Fatal(err)
	}
}

func turnDonePayload(model string, in, out, cr, cw int64) map[string]any {
	return map[string]any{"stop_reason": "end_turn", "model": model,
		"usage": map[string]any{"input_tokens": in, "output_tokens": out, "cache_read_tokens": cr, "cache_write_tokens": cw}}
}

func seedSuccessfulStart(t *testing.T, pool *pgxpool.Pool, session string, start time.Time) {
	t.Helper()
	stages := func(ss ...map[string]string) map[string]any { return map[string]any{"stages": ss} }
	s := func(id, state string) map[string]string { return map[string]string{"id": id, "state": state} }
	plan := map[string]any{"plan": true, "runtime": "cluster", "stages": []map[string]string{
		s("queued", "active"), s("schedule", "pending"), s("connect", "pending"), s("clone", "pending"), s("engine", "pending"), s("ready", "pending")}}
	seedEvent(t, pool, session, start, "start_stage", plan)
	seedEvent(t, pool, session, start, "start_stage", stages(s("queued", "done"), s("schedule", "active")))
	seedEvent(t, pool, session, start.Add(2*time.Second), "start_stage", stages(s("schedule", "done"), s("connect", "done"), s("clone", "active")))
	seedEvent(t, pool, session, start.Add(32*time.Second), "start_stage", stages(s("clone", "done"), s("engine", "active")))
	seedEvent(t, pool, session, start.Add(40*time.Second), "start_stage", stages(s("ready", "done")))
}

// newInsightsFixture: alice (a member) has one public cluster session; bob has a public one and a private cron
// session. Each has a successful start and some turns.
func newInsightsFixture(t *testing.T) *insightsFixture {
	t.Helper()
	pool := runnerContractPool(t)
	ctx := context.Background()
	api := NewAPI(NewHub(), pool, "daemon-tok-1234567890", nil, "")
	f := &insightsFixture{api: api, pool: pool, mint: mintFor(t, api)}
	const daemon = "00000000-0000-4000-8000-0000000000da"
	if err := db.UpsertDaemon(ctx, pool, daemon, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	h := func(n int) time.Time { return now.Add(-time.Duration(n) * time.Hour) }
	endB := h(1)
	f.ids.aliceA = seedSession(t, pool, daemon, "alice", "cluster", "idle", false, h(5), nil)
	f.ids.bobB = seedSession(t, pool, daemon, "bob", "cluster", "stopped", false, h(6), &endB)
	f.ids.bobPrivate = seedSession(t, pool, daemon, "bob", "cluster", "idle", true, h(4), nil)
	seedSuccessfulStart(t, pool, f.ids.aliceA, h(5))
	seedSuccessfulStart(t, pool, f.ids.bobB, h(6))
	seedSuccessfulStart(t, pool, f.ids.bobPrivate, h(4))
	seedEvent(t, pool, f.ids.aliceA, h(5).Add(time.Minute), "turn_done", turnDonePayload("m-one", 100, 1000, 50000, 2000))
	seedEvent(t, pool, f.ids.aliceA, h(5).Add(2*time.Minute), "turn_done", turnDonePayload("m-one", 10, 500, 60000, 0))
	seedEvent(t, pool, f.ids.bobB, h(6).Add(time.Minute), "turn_done", turnDonePayload("m-two", 1_000_000, 2000, 0, 0))
	seedEvent(t, pool, f.ids.bobPrivate, h(4).Add(time.Minute), "turn_done", turnDonePayload("m-one", 7_000_000, 70000, 0, 0))
	for _, st := range []struct {
		id     string
		at     time.Time
		status string
	}{
		{f.ids.aliceA, h(5).Add(time.Minute), "running"}, {f.ids.aliceA, h(5).Add(11 * time.Minute), "idle"},
	} {
		seedEvent(t, pool, st.id, st.at, "status_changed", map[string]string{"status": st.status, "reason": "turn"})
	}
	return f
}

func (f *insightsFixture) get(t *testing.T, path, token string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/insights", f.api.HandleGetInsights)
	mux.HandleFunc("GET /api/insights/prices", f.api.HandleGetInsightPrices)
	mux.HandleFunc("GET /metrics", f.api.HandleMetrics)
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (f *insightsFixture) send(t *testing.T, method, path, token string, body any) int {
	t.Helper()
	var rdr *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = strings.NewReader(string(raw))
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/insights/prices", f.api.HandlePutInsightPrice)
	mux.HandleFunc("DELETE /api/insights/prices", f.api.HandleDeleteInsightPrice)
	mux.ServeHTTP(rec, req)
	return rec.Code
}

func (f *insightsFixture) insights(t *testing.T, path, token string) insightsResponse {
	t.Helper()
	code, body := f.get(t, path, token)
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, code, body)
	}
	var out insightsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	return out
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestInsightsAMemberSeesOnlyTheirOwnSessions(t *testing.T) {
	f := newInsightsFixture(t)
	r := f.insights(t, "/api/insights?range=7d", f.mint("alice", "session.start"))
	if r.Scope != "mine" || r.IsAdmin {
		t.Fatalf("scope/admin = %s/%v", r.Scope, r.IsAdmin)
	}
	if r.Sessions.Started != 1 {
		t.Errorf("started = %d, want only alice's 1", r.Sessions.Started)
	}
	// alice: 100+10 input, 1500 output, 110000 cache read, 2000 cache write; bob's tokens are not hers
	if r.Tokens.Totals.Input != 110 || r.Tokens.Totals.Output != 1500 || r.Tokens.Totals.CacheRead != 110000 || r.Tokens.Totals.Turns != 2 {
		t.Errorf("totals = %+v", r.Tokens.Totals)
	}
	if len(r.Tokens.ByModel) != 1 || r.Tokens.ByModel[0].Model != "m-one" {
		t.Errorf("by model = %+v", r.Tokens.ByModel)
	}
	if len(r.Startup.Groups) != 1 || r.Startup.Groups[0].Attempts != 1 || r.Startup.Groups[0].Runtime != "cluster" {
		t.Fatalf("startup groups = %+v", r.Startup.Groups)
	}
	near(t, "start p50", r.Startup.Groups[0].Seconds.P50, 40)
	steps := map[string]float64{}
	for _, s := range r.Startup.Steps {
		steps[s.Step] = s.P50
	}
	near(t, "schedule step", steps["schedule"], 2)
	near(t, "clone step", steps["clone"], 30)
	near(t, "engine step", steps["engine"], 8)
	near(t, "alice running seconds", r.Sessions.RunningSeconds, 600)
}

func TestInsightsAnAdministratorSeesEverythingButNotPrivateSessionsByName(t *testing.T) {
	f := newInsightsFixture(t)
	admin := f.mint("root", "session.start", insightsAdminCap)
	r := f.insights(t, "/api/insights?range=7d", admin)
	if r.Scope != "all" || !r.IsAdmin {
		t.Fatalf("scope/admin = %s/%v", r.Scope, r.IsAdmin)
	}
	if r.Sessions.Started != 3 {
		t.Errorf("started = %d, want all 3", r.Sessions.Started)
	}
	// the private session's tokens are in the totals (7M input from bob's cron session)...
	if r.Tokens.Totals.Input != 110+1_000_000+7_000_000 {
		t.Errorf("total input = %d, want the private session's tokens included", r.Tokens.Totals.Input)
	}
	// ...but it is not named anywhere
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), f.ids.bobPrivate) {
		t.Error("the private session's id appears in the administrator's report")
	}
	for _, s := range r.Tokens.TopSessions {
		if s.SessionID == f.ids.bobPrivate {
			t.Error("top sessions names a private session")
		}
	}
	for _, s := range r.Startup.Slowest {
		if s.SessionID == f.ids.bobPrivate {
			t.Error("slowest starts names a private session")
		}
	}
	// the owner sees their own private session by name
	own := f.insights(t, "/api/insights?range=7d", f.mint("bob", "session.start"))
	found := false
	for _, s := range own.Tokens.TopSessions {
		if s.SessionID == f.ids.bobPrivate {
			found = true
		}
	}
	if !found {
		t.Error("bob should see his own private session in his top sessions")
	}
	// an administrator can ask for their own view
	mine := f.insights(t, "/api/insights?range=7d&scope=mine", admin)
	if mine.Scope != "mine" || mine.Sessions.Started != 0 || mine.Tokens.Totals.Turns != 0 {
		t.Errorf("scope=mine for an admin with no sessions = %+v", mine.Sessions)
	}
}

func TestInsightsCostNeedsPricesAndOnlyAnAdministratorSetsThem(t *testing.T) {
	f := newInsightsFixture(t)
	admin := f.mint("root", "session.start", insightsAdminCap)
	alice := f.mint("alice", "session.start")

	r := f.insights(t, "/api/insights", alice)
	if r.Tokens.Totals.CostUSD != nil {
		t.Errorf("cost = %v with no prices, want none", *r.Tokens.Totals.CostUSD)
	}
	if len(r.Tokens.UnpricedModels) != 1 || r.Tokens.UnpricedModels[0] != "m-one" {
		t.Errorf("unpriced = %v", r.Tokens.UnpricedModels)
	}

	price := db.ModelPrice{Model: "m-one", InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75}
	if code := f.send(t, http.MethodPut, "/api/insights/prices", alice, price); code != http.StatusForbidden {
		t.Errorf("a member setting a price = %d, want 403", code)
	}
	if code := f.send(t, http.MethodPut, "/api/insights/prices", admin, price); code != http.StatusNoContent {
		t.Fatalf("admin PUT = %d", code)
	}
	r = f.insights(t, "/api/insights", alice)
	if r.Tokens.Totals.CostUSD == nil {
		t.Fatal("no cost after the price was set")
	}
	// 110 in, 1500 out, 110000 cache read, 2000 cache write
	near(t, "cost", *r.Tokens.Totals.CostUSD, 110*3/1e6+1500*15/1e6+110000*0.3/1e6+2000*3.75/1e6)
	if len(r.Tokens.UnpricedModels) != 0 {
		t.Errorf("unpriced = %v after pricing", r.Tokens.UnpricedModels)
	}
	// everybody can read the prices
	if code, body := f.get(t, "/api/insights/prices", alice); code != http.StatusOK || !strings.Contains(string(body), "m-one") {
		t.Errorf("GET prices = %d %s", code, body)
	}
	for name, bad := range map[string]db.ModelPrice{
		"negative":   {Model: "x", InputPerMTok: -1},
		"no model":   {Model: "  ", InputPerMTok: 1},
		"absurd":     {Model: "x", InputPerMTok: 1e9},
		"long model": {Model: strings.Repeat("m", 300)},
	} {
		if code := f.send(t, http.MethodPut, "/api/insights/prices", admin, bad); code != http.StatusBadRequest {
			t.Errorf("%s price = %d, want 400", name, code)
		}
	}
	if code := f.send(t, http.MethodDelete, "/api/insights/prices?model=m-one", alice, nil); code != http.StatusForbidden {
		t.Errorf("a member deleting a price = %d", code)
	}
	if code := f.send(t, http.MethodDelete, "/api/insights/prices?model=m-one", admin, nil); code != http.StatusNoContent {
		t.Errorf("delete = %d", code)
	}
	if code := f.send(t, http.MethodDelete, "/api/insights/prices?model=m-one", admin, nil); code != http.StatusNotFound {
		t.Errorf("deleting twice = %d, want 404", code)
	}
}

func TestInsightsRejectsBadRangesAndAnonymousCallers(t *testing.T) {
	f := newInsightsFixture(t)
	alice := f.mint("alice", "session.start")
	if code, _ := f.get(t, "/api/insights?range=forever", alice); code != http.StatusBadRequest {
		t.Errorf("bad range = %d", code)
	}
	if code, _ := f.get(t, "/api/insights", ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", code)
	}
	// a token without the browser capability is not a signed-in person
	if code, _ := f.get(t, "/api/insights", f.mint("alice")); code != http.StatusUnauthorized {
		t.Errorf("token with no capabilities = %d", code)
	}
}

func TestInsightsRangeLimitsWhatIsCounted(t *testing.T) {
	f := newInsightsFixture(t)
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	const daemon = "00000000-0000-4000-8000-0000000000da"
	id := seedSession(t, f.pool, daemon, "alice", "cluster", "stopped", false, old, ptrTime(old.Add(time.Hour)))
	seedEvent(t, f.pool, id, old.Add(time.Minute), "turn_done", turnDonePayload("m-old", 1, 1, 1, 1))
	alice := f.mint("alice", "session.start")
	if got := f.insights(t, "/api/insights?range=7d", alice).Tokens.Totals.Turns; got != 2 {
		t.Errorf("7d turns = %d, want 2 (the 40-day-old one is out)", got)
	}
	if got := f.insights(t, "/api/insights?range=90d", alice).Tokens.Totals.Turns; got != 3 {
		t.Errorf("90d turns = %d, want 3", got)
	}
}

func TestInsightsCountsALongLivedSessionAsAliveButNotAsStarted(t *testing.T) {
	f := newInsightsFixture(t)
	alice := f.mint("alice", "session.start")
	before := f.insights(t, "/api/insights?range=24h", alice).Sessions
	const daemon = "00000000-0000-4000-8000-0000000000da"
	seedSession(t, f.pool, daemon, "alice", "cluster", "idle", false, time.Now().UTC().Add(-40*24*time.Hour), nil)
	after := f.insights(t, "/api/insights?range=24h", alice).Sessions
	if after.Alive != before.Alive+1 {
		t.Errorf("alive = %d, want %d (a session started 40 days ago and still open is alive now)", after.Alive, before.Alive+1)
	}
	if after.Started != before.Started {
		t.Errorf("started = %d, want %d (it did not start in the range)", after.Started, before.Started)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// ── metrics ──────────────────────────────────────────────────────────────────

func TestMetricsAreOffWithoutATokenAndGuardedWithOne(t *testing.T) {
	f := newInsightsFixture(t)
	if code, _ := f.get(t, "/metrics", ""); code != http.StatusNotFound {
		t.Errorf("metrics with no token configured = %d, want 404", code)
	}
	f.api.SetMetricsToken("scrape-me-123")
	if code, _ := f.get(t, "/metrics", ""); code != http.StatusUnauthorized {
		t.Errorf("no credential = %d", code)
	}
	if code, _ := f.get(t, "/metrics", "wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong credential = %d", code)
	}
	// a person's login token is not the scrape token
	if code, _ := f.get(t, "/metrics", f.mint("root", "session.start", insightsAdminCap)); code != http.StatusUnauthorized {
		t.Errorf("a login token on /metrics = %d, want 401", code)
	}
	if code, _ := f.get(t, "/metrics", "scrape-me-123"); code != http.StatusOK {
		t.Errorf("right credential = %d", code)
	}
}

var promLine = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{([a-zA-Z_][a-zA-Z0-9_]*="([^"\\]|\\.)*",?)*\})? (-?[0-9.]+(e[+-]?[0-9]+)?|\+Inf|-Inf|NaN)$`)

func TestMetricsBodyIsValidAndHoldsWhatIsPromised(t *testing.T) {
	f := newInsightsFixture(t)
	f.api.SetMetricsToken("scrape-me-123")
	if err := db.UpsertModelPrice(context.Background(), f.pool, db.ModelPrice{Model: "m-one", InputPerMTok: 3, OutputPerMTok: 15}); err != nil {
		t.Fatal(err)
	}
	code, raw := f.get(t, "/metrics", "scrape-me-123")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	body := string(raw)
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !promLine.MatchString(line) {
			t.Errorf("not a valid sample line: %q", line)
		}
	}
	for _, want := range []string{
		"# TYPE blerg_runner_sessions gauge",
		`blerg_runner_sessions{runtime="cluster",status="idle"} 2`,
		`blerg_runner_sessions{runtime="cluster",status="stopped"} 1`,
		`blerg_runner_sessions_started_total{runtime="cluster"} 3`,
		`blerg_runner_start_attempts_total{runtime="cluster",kind="start",result="ready"} 3`,
		`blerg_runner_start_duration_seconds_count{runtime="cluster",kind="start"} 3`,
		`blerg_runner_start_duration_seconds_bucket{runtime="cluster",kind="start",le="30"} 0`,
		`blerg_runner_start_duration_seconds_bucket{runtime="cluster",kind="start",le="45"} 3`,
		`blerg_runner_turns_total{model="m-one"} 3`,
		`blerg_runner_tokens_total{model="m-two",type="input"} 1e+06`,
		`blerg_runner_cost_usd_total{model="m-one"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	if strings.Contains(body, `cost_usd_total{model="m-two"}`) {
		t.Error("a model with no price must not have a cost series")
	}
	// nothing that names a session, an account or a title
	for _, bad := range []string{f.ids.aliceA, f.ids.bobB, f.ids.bobPrivate, "alice", "bob", "title-"} {
		if strings.Contains(body, bad) {
			t.Errorf("metrics leak %q", bad)
		}
	}
	// every histogram is cumulative and its +Inf bucket equals its count
	checkHistograms(t, body)
}

// checkHistograms verifies the cumulative-bucket invariants of every histogram series in an exposition body.
func checkHistograms(t *testing.T, body string) {
	t.Helper()
	type series struct {
		last  float64
		inf   float64
		count float64
		seen  bool
	}
	hs := map[string]*series{}
	keyOf := func(name, labels string) string {
		labels = regexp.MustCompile(`,?le="[^"]*"`).ReplaceAllString(labels, "")
		return strings.TrimSuffix(strings.TrimSuffix(name, "_bucket"), "_count") + labels
	}
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sp := strings.LastIndex(line, " ")
		head, valText := line[:sp], line[sp+1:]
		name, labels := head, ""
		if i := strings.Index(head, "{"); i >= 0 {
			name, labels = head[:i], head[i:]
		}
		v, _ := strconv.ParseFloat(strings.Replace(valText, "+Inf", "Inf", 1), 64)
		switch {
		case strings.HasSuffix(name, "_bucket"):
			k := keyOf(name, labels)
			s := hs[k]
			if s == nil {
				s = &series{}
				hs[k] = s
			}
			if v < s.last {
				t.Errorf("bucket counts go down in %s: %s", k, line)
			}
			s.last, s.seen = v, true
			if strings.Contains(labels, `le="+Inf"`) {
				s.inf = v
			}
		case strings.HasSuffix(name, "_count"):
			if s := hs[keyOf(name, labels)]; s != nil {
				s.count = v
			}
		}
	}
	for k, s := range hs {
		if s.inf != s.count {
			t.Errorf("histogram %s: +Inf bucket %v != count %v", k, s.inf, s.count)
		}
	}
	if len(hs) == 0 {
		t.Error("no histograms found")
	}
}

func TestPromEscapingAndFormatting(t *testing.T) {
	w := &promWriter{}
	w.counter("x_total", "a\nhelp", []promSample{{labels: []string{"model", "we\"ird\\name\nx"}, value: 1.5}})
	out := w.String()
	if !strings.Contains(out, `x_total{model="we\"ird\\name\nx"} 1.5`) || !strings.Contains(out, `# HELP x_total a\nhelp`) {
		t.Errorf("escaping wrong:\n%s", out)
	}
	if got := promFloat(1e21); got != "1e+21" {
		t.Errorf("promFloat = %q", got)
	}
	_ = fmt.Sprint()
}
