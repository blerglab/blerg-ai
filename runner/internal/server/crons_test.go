package server

// The crons API (crons.go, routes_crons.go, spec 7.7) against a real database, a fake core (token
// mint, status and revoke) and the MCP start fixture. Every route needs a signed-in person: Kind
// "human" with a live login session id.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const otherAcct = "user-2"

// fakeCronMinter is core's POST /internal/tokens/mint.
type fakeCronMinter struct {
	mu     sync.Mutex
	core   *fakeCronCore
	err    error
	calls  []mintCall
	nextID []string
}

type mintCall struct{ Account, Session, Name string }

func (m *fakeCronMinter) MintToken(_ context.Context, account, sessionID, name string, days int) (string, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, mintCall{account, sessionID, name})
	if m.err != nil {
		return "", time.Time{}, m.err
	}
	id := newUUID()
	if len(m.nextID) > 0 {
		id, m.nextID = m.nextID[0], m.nextID[1:]
	}
	m.core.mu.Lock()
	m.core.live[id] = true
	m.core.mu.Unlock()
	return id, time.Now().Add(time.Duration(days) * 24 * time.Hour), nil
}

func (m *fakeCronMinter) minted() []mintCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mintCall(nil), m.calls...)
}

type cronsFx struct {
	*cronFx
	minter *fakeCronMinter
	mux    *http.ServeMux
}

func newCronsFx(t *testing.T) *cronsFx {
	t.Helper()
	cf := newCronFx(t)
	m := &fakeCronMinter{core: cf.core}
	mux := http.NewServeMux()
	cf.api.registerCronRoutes(mux, m)
	return &cronsFx{cronFx: cf, minter: m, mux: mux}
}

func (fx *cronsFx) call(method, path, bearer, body string) *httptest.ResponseRecorder {
	fx.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec
}

func (fx *cronsFx) otherHuman() string {
	fx.t.Helper()
	return mintRunnerToken(fx.t, fx.priv, "core-1", identity.Claims{
		Sub: otherAcct, Aud: coreAuthAudience, Kind: "human", Sid: newUUID(),
		Caps: []string{"session.start"}, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
}

// body is a valid create body, mutated by mut.
func (fx *cronsFx) body(mut func(map[string]any)) string {
	fx.t.Helper()
	m := map[string]any{
		"name": "Morning digest", "schedule": "0 8 * * *", "timezone": "America/New_York",
		"prompt": "Summarise my inbox", "runtime": "auto", "mcp": fx.selection(),
	}
	if mut != nil {
		mut(m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		fx.t.Fatal(err)
	}
	return string(raw)
}

type cronBody struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	Schedule            string          `json:"schedule"`
	Timezone            string          `json:"timezone"`
	Status              string          `json:"status"`
	PausedReason        *string         `json:"paused_reason"`
	TokenExpiresAt      string          `json:"token_expires_at"`
	NextRunAt           string          `json:"next_run_at"`
	MCP                 json.RawMessage `json:"mcp"`
	ConsecutiveFailures int             `json:"consecutive_failures"`
	LastRun             *struct {
		Status string `json:"status"`
	} `json:"last_run"`
}

func decodeCron(t *testing.T, rec *httptest.ResponseRecorder) cronBody {
	t.Helper()
	var c cronBody
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return c
}

// create makes a cron through the API and returns it with its stored row.
func (fx *cronsFx) create(mut func(map[string]any)) (cronBody, *db.Cron) {
	fx.t.Helper()
	rec := fx.call(http.MethodPost, "/api/crons", fx.human(), fx.body(mut))
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	c := decodeCron(fx.t, rec)
	row, err := db.GetCron(context.Background(), fx.pool, c.ID)
	if err != nil {
		fx.t.Fatal(err)
	}
	return c, row
}

func (fx *cronsFx) cronCount() int { return fx.count("crons") }

// The route group's table: no token 401, a garbage token 401, an agent token (which holds
// session.start) 403, a human token with no login session 401, another account's cron 404.
func TestCronRoutesAuthMatrix(t *testing.T) {
	fx := newCronsFx(t)
	_, row := fx.create(nil)
	id := row.ID
	routes := []struct {
		method, path, body string
		ownerScoped        bool
	}{
		{http.MethodGet, "/api/crons", "", false},
		{http.MethodPost, "/api/crons", fx.body(nil), false},
		{http.MethodGet, "/api/crons/" + id, "", true},
		{http.MethodPatch, "/api/crons/" + id, `{"name":"x"}`, true},
		{http.MethodDelete, "/api/crons/" + id, "", true},
		{http.MethodPost, "/api/crons/" + id + "/run", "", true},
		{http.MethodPost, "/api/crons/" + id + "/pause", "", true},
		{http.MethodPost, "/api/crons/" + id + "/resume", "", true},
		{http.MethodPost, "/api/crons/" + id + "/renew", "", true},
		{http.MethodGet, "/api/crons/" + id + "/runs", "", true},
	}
	before := len(fx.minter.minted())
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			if rec := fx.call(r.method, r.path, "", r.body); rec.Code != http.StatusUnauthorized {
				t.Errorf("no token: %d, want 401", rec.Code)
			}
			if rec := fx.call(r.method, r.path, "garbage", r.body); rec.Code != http.StatusUnauthorized {
				t.Errorf("bad token: %d, want 401", rec.Code)
			}
			if rec := fx.call(r.method, r.path, fx.token("agent", ""), r.body); rec.Code != http.StatusForbidden {
				t.Errorf("agent token with session.start: %d, want 403", rec.Code)
			}
			if rec := fx.call(r.method, r.path, fx.token("human", ""), r.body); rec.Code != http.StatusUnauthorized {
				t.Errorf("human token without a login session: %d, want 401", rec.Code)
			}
		})
	}
	if n := len(fx.minter.minted()) - before; n != 0 {
		t.Errorf("refused requests minted %d tokens", n)
	}
	if n := fx.cronCount(); n != 1 {
		t.Errorf("refused requests changed the crons: %d rows", n)
	}
	if rev := fx.core.revokedList(); len(rev) != 0 {
		t.Errorf("refused requests revoked %v", rev)
	}
	// Another account's cron is a 404 on every route that names one, and nothing of it changes.
	other := fx.otherHuman()
	for _, r := range routes {
		if !r.ownerScoped {
			continue
		}
		if rec := fx.call(r.method, r.path, other, r.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as another account: %d, want 404 (%s)", r.method, r.path, rec.Code, rec.Body.String())
		}
	}
	if n := fx.cronCount(); n != 1 {
		t.Errorf("another account changed the crons: %d rows", n)
	}
	if rev := fx.core.revokedList(); len(rev) != 0 {
		t.Errorf("another account revoked %v", rev)
	}
	// The other account's list is empty: it never sees the cron.
	rec := fx.call(http.MethodGet, "/api/crons", other, "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), id) {
		t.Errorf("another account's list: %d %s", rec.Code, rec.Body.String())
	}
	// A malformed id is a 404 too.
	if rec := fx.call(http.MethodGet, "/api/crons/not-a-uuid", fx.human(), ""); rec.Code != http.StatusNotFound {
		t.Errorf("malformed id: %d, want 404", rec.Code)
	}
}

func TestCreateCron(t *testing.T) {
	fx := newCronsFx(t)
	c, row := fx.create(nil)

	calls := fx.minter.minted()
	if len(calls) != 1 || calls[0].Account != mcpAcct || calls[0].Session != testSID || !strings.Contains(calls[0].Name, "Morning digest") {
		t.Fatalf("mint calls = %+v, want one for the caller's own login session", calls)
	}
	if row.OwnerAccountID != mcpAcct || row.Engine != "claude" || row.TokenID == "" || !row.Enabled {
		t.Errorf("row = %+v", row)
	}
	if !row.TokenExpiresAt.After(time.Now().Add(300 * 24 * time.Hour)) {
		t.Errorf("token expiry = %v, want about a year out", row.TokenExpiresAt)
	}
	if !row.NextRunAt.After(time.Now()) {
		t.Errorf("next_run_at = %v, want in the future", row.NextRunAt)
	}
	if c.Status != "active" || c.Schedule != "0 8 * * *" || c.TokenExpiresAt == "" {
		t.Errorf("response = %+v", c)
	}
	// The stored mcp is the explicit selection, and the response never carries the token id.
	if !strings.Contains(string(row.MCP), fx.connID) || !strings.Contains(string(row.MCP), `"allow"`) {
		t.Errorf("stored mcp = %s", row.MCP)
	}
	rec := fx.call(http.MethodGet, "/api/crons/"+row.ID, fx.human(), "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), row.TokenID) || strings.Contains(rec.Body.String(), "token_id") {
		t.Errorf("GET leaks the token: %d %s", rec.Code, rec.Body.String())
	}
	// Listed, with its last run absent.
	rec = fx.call(http.MethodGet, "/api/crons", fx.human(), "")
	var list struct {
		Crons []cronBody `json:"crons"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Crons) != 1 || list.Crons[0].ID != row.ID {
		t.Fatalf("list: %d %s (%v)", rec.Code, rec.Body.String(), err)
	}
}

func TestCreateCronWithoutMCPAndProposeMode(t *testing.T) {
	fx := newCronsFx(t)
	_, row := fx.create(func(m map[string]any) { delete(m, "mcp") })
	if string(row.MCP) != "[]" {
		t.Errorf("no mcp stored as %s, want []", row.MCP)
	}
	fx.create(func(m map[string]any) {
		m["name"] = "proposer"
		m["mcp"] = []map[string]any{{"connection": fx.connID, "tools": map[string]any{
			"echo": map[string]any{"mode": "propose", "hash": fx.up.hash("echo")},
		}}}
	})
	got, err := db.ListCrons(context.Background(), fx.pool, mcpAcct)
	if err != nil || len(got) != 2 {
		t.Fatalf("crons = %d, %v", len(got), err)
	}
	var stored []MCPSelection
	for _, c := range got {
		if c.Name == "proposer" {
			if err := json.Unmarshal(c.MCP, &stored); err != nil || stored[0].Tools["echo"].Mode != "propose" {
				t.Errorf("propose stored as %s (%v)", c.MCP, err)
			}
		}
	}
}

func TestCreateCronValidationMatrix(t *testing.T) {
	fx := newCronsFx(t)
	sel := func(tools any) func(map[string]any) {
		return func(m map[string]any) {
			m["mcp"] = []map[string]any{{"connection": fx.connID, "tools": tools}}
		}
	}
	echo := func(mode, hash string) map[string]any {
		return map[string]any{"echo": map[string]any{"mode": mode, "hash": hash}}
	}
	tests := []struct {
		name string
		mut  func(map[string]any)
		want int
		msg  string
	}{
		{"no name", func(m map[string]any) { m["name"] = "" }, 400, "name"},
		{"no prompt", func(m map[string]any) { m["prompt"] = "" }, 400, "prompt"},
		{"prompt too long", func(m map[string]any) { m["prompt"] = strings.Repeat("x", 16385) }, 400, "prompt"},
		{"@every", func(m map[string]any) { m["schedule"] = "@every 1h" }, 400, ""},
		{"@daily", func(m map[string]any) { m["schedule"] = "@daily" }, 400, ""},
		{"TZ prefix", func(m map[string]any) { m["schedule"] = "TZ=UTC 0 8 * * *" }, 400, ""},
		{"six fields", func(m map[string]any) { m["schedule"] = "0 0 8 * * *" }, 400, ""},
		{"too frequent", func(m map[string]any) { m["schedule"] = "*/5 * * * *" }, 400, "15"},
		{"never occurs", func(m map[string]any) { m["schedule"] = "0 8 30 2 *" }, 400, ""},
		{"bad timezone", func(m map[string]any) { m["timezone"] = "Mars/Olympus" }, 400, ""},
		{"no timezone", func(m map[string]any) { m["timezone"] = "" }, 400, ""},
		{"engine codex", func(m map[string]any) { m["engine"] = "codex" }, 400, "claude"},
		{"bare host runtime", func(m map[string]any) { m["runtime"] = "daemon" }, 400, "runtime"},
		{"bad daemon id", func(m map[string]any) { m["daemon_id"] = "laptop" }, 400, "daemon"},
		{"grace too long", func(m map[string]any) { m["grace_seconds"] = 90000 }, 400, "grace"},
		{"max runtime too short", func(m map[string]any) { m["max_runtime_seconds"] = 5 }, 400, "run time"},
		{"a token id in the body", func(m map[string]any) { m["token_id"] = "abc" }, 400, "token_id"},
		{"an owner in the body", func(m map[string]any) { m["owner_account_id"] = "user-9" }, 400, "owner"},
		{"mcp entry without tools", func(m map[string]any) { m["mcp"] = []map[string]any{{"connection": fx.connID}} }, 400, "tools"},
		{"mcp entry with empty tools", sel(map[string]any{}), 400, "tools"},
		{"mcp bad mode", sel(echo("write", fx.up.hash("echo"))), 400, "mode"},
		{"mcp no hash", sel(echo("allow", "")), 400, "hash"},
		{"mcp stale hash", sel(echo("allow", "0000")), 409, "changed"},
		{"mcp unknown connection", func(m map[string]any) {
			m["mcp"] = []map[string]any{{"connection": newUUID(), "tools": echo("allow", fx.up.hash("echo"))}}
		}, 404, "connection"},
		{"mcp not an array", func(m map[string]any) { m["mcp"] = "everything" }, 400, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := fx.call(http.MethodPost, "/api/crons", fx.human(), fx.body(tc.mut))
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.msg != "" && !strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(tc.msg)) {
				t.Errorf("message %q does not mention %q", rec.Body.String(), tc.msg)
			}
			if n := fx.cronCount(); n != 0 {
				t.Errorf("%d cron rows were created", n)
			}
			// Whatever was minted before the refusal was revoked again: no token is left behind.
			if m, r := len(fx.minter.minted()), len(fx.core.revokedList()); m != r {
				t.Errorf("minted %d tokens, revoked %d", m, r)
			}
			fx.minter.mu.Lock()
			fx.minter.calls = nil
			fx.minter.mu.Unlock()
			fx.core.mu.Lock()
			fx.core.revoked = nil
			fx.core.mu.Unlock()
		})
	}
	// A malformed body and an oversized one.
	if rec := fx.call(http.MethodPost, "/api/crons", fx.human(), `{not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body: %d", rec.Code)
	}
}

func TestCreateCronSchedulePreflightMintsNothing(t *testing.T) {
	fx := newCronsFx(t)
	for _, mut := range []func(map[string]any){
		func(m map[string]any) { m["schedule"] = "*/5 * * * *" },
		func(m map[string]any) { m["timezone"] = "Mars/Olympus" },
		func(m map[string]any) { m["mcp"] = []map[string]any{{"connection": fx.connID}} },
	} {
		fx.call(http.MethodPost, "/api/crons", fx.human(), fx.body(mut))
	}
	if n := len(fx.minter.minted()); n != 0 {
		t.Errorf("schedule and mcp problems were caught after minting: %d mints", n)
	}
}

func TestCreateCronMintFailureCreatesNoRow(t *testing.T) {
	fx := newCronsFx(t)
	fx.minter.err = errCronMintCap
	rec := fx.call(http.MethodPost, "/api/crons", fx.human(), fx.body(nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "limit") {
		t.Errorf("mint cap: %d %s, want 409 that explains the limit", rec.Code, rec.Body.String())
	}
	fx.minter.err = errCronMintProof
	rec = fx.call(http.MethodPost, "/api/crons", fx.human(), fx.body(nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("dead session: %d %s, want 401", rec.Code, rec.Body.String())
	}
	fx.minter.err = errors.New("dial tcp 192.0.2.3:8080: refused")
	rec = fx.call(http.MethodPost, "/api/crons", fx.human(), fx.body(nil))
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "192.0.2.3") {
		t.Errorf("core down: %d %s, want a clean 502", rec.Code, rec.Body.String())
	}
	if n := fx.cronCount(); n != 0 {
		t.Errorf("%d rows after failed mints", n)
	}
}

func TestCreateCronRollsBackTheTokenWhenTheInsertFails(t *testing.T) {
	fx := newCronsFx(t)
	minted := newUUID()
	fx.minter.nextID = []string{minted}
	// The name passes the pre-flight (it is only a length check in the engine) but the insert
	// refuses it, after the token exists.
	rec := fx.call(http.MethodPost, "/api/crons", fx.human(), fx.body(func(m map[string]any) {
		m["name"] = strings.Repeat("n", 101)
	}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if n := fx.cronCount(); n != 0 {
		t.Errorf("%d rows", n)
	}
	if rev := fx.core.revokedList(); len(rev) != 1 || rev[0] != mcpAcct+":"+minted {
		t.Errorf("revoked = %v, want the fresh token rolled back", rev)
	}
}

func TestCreateCronLimit(t *testing.T) {
	fx := newCronsFx(t)
	for i := 0; i < 20; i++ {
		fx.newCron(func(c *db.Cron) { c.TokenID = newUUID() })
	}
	rec := fx.call(http.MethodPost, "/api/crons", fx.human(), fx.body(nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "20") {
		t.Errorf("21st cron: %d %s, want 409 naming the limit", rec.Code, rec.Body.String())
	}
	if n := len(fx.minter.minted()); n != 0 {
		t.Errorf("minted %d tokens for a refused cron", n)
	}
}

func TestDeleteCronRevokesAndStops(t *testing.T) {
	fx := newCronsFx(t)
	ctx := context.Background()
	c, row := fx.create(nil)
	running := newUUID()
	fx.insertCronSession(running, row.ID, mcpAcct, "running")
	other := fx.newCron(func(x *db.Cron) { x.TokenID = newUUID() })
	otherRunning := newUUID()
	fx.insertCronSession(otherRunning, other.ID, mcpAcct, "running")

	rec := fx.call(http.MethodDelete, "/api/crons/"+c.ID, fx.human(), "")
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := db.GetCron(ctx, fx.pool, c.ID); !errors.Is(err, db.ErrCronNotFound) {
		t.Errorf("the cron still exists: %v", err)
	}
	if rev := fx.core.revokedList(); len(rev) != 1 || rev[0] != mcpAcct+":"+row.TokenID {
		t.Errorf("revoked = %v, want the cron's token", rev)
	}
	if s := fx.session(running); s.Status != "ended" {
		t.Errorf("the cron's running session is %q, want ended", s.Status)
	}
	if s := fx.session(otherRunning); s.Status != "running" {
		t.Errorf("another cron's session was stopped: %q", s.Status)
	}
	if rec := fx.call(http.MethodDelete, "/api/crons/"+c.ID, fx.human(), ""); rec.Code != http.StatusNotFound {
		t.Errorf("second delete: %d, want 404", rec.Code)
	}
}

func TestPauseAndResumeCron(t *testing.T) {
	fx := newCronsFx(t)
	c, row := fx.create(nil)
	running := newUUID()
	fx.insertCronSession(running, row.ID, mcpAcct, "running")
	// Age the slot so a resume that forgot to recompute would fire at once.
	if _, err := fx.pool.Exec(context.Background(), `UPDATE crons SET next_run_at = now() - interval '2 days' WHERE id = $1`, row.ID); err != nil {
		t.Fatal(err)
	}

	rec := fx.call(http.MethodPost, "/api/crons/"+c.ID+"/pause", fx.human(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeCron(t, rec)
	if got.Status != "paused" || got.PausedReason == nil || *got.PausedReason == "" {
		t.Errorf("paused response = %+v", got)
	}
	if rev := fx.core.revokedList(); len(rev) != 1 || rev[0] != mcpAcct+":"+row.TokenID {
		t.Errorf("pause revoked %v", rev)
	}
	if s := fx.session(running); s.Status != "ended" {
		t.Errorf("pause left the session %q", s.Status)
	}
	// Pausing again is harmless and revokes nothing more.
	if rec := fx.call(http.MethodPost, "/api/crons/"+c.ID+"/pause", fx.human(), ""); rec.Code != http.StatusOK {
		t.Errorf("second pause: %d", rec.Code)
	}
	if rev := fx.core.revokedList(); len(rev) != 1 {
		t.Errorf("second pause revoked again: %v", rev)
	}

	rec = fx.call(http.MethodPost, "/api/crons/"+c.ID+"/resume", fx.human(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body.String())
	}
	got = decodeCron(t, rec)
	if got.Status != "active" || got.PausedReason != nil {
		t.Errorf("resumed response = %+v", got)
	}
	after, err := db.GetCron(context.Background(), fx.pool, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TokenID == row.TokenID {
		t.Error("resume kept the revoked token: it must mint a new one")
	}
	if calls := fx.minter.minted(); len(calls) != 2 || calls[1].Session != testSID {
		t.Errorf("mints = %+v, want the creation and one with the caller's session", calls)
	}
	if !after.NextRunAt.After(time.Now()) || after.PausedReason != nil || after.ConsecutiveFailures != 0 {
		t.Errorf("after resume: next=%v paused=%v failures=%d", after.NextRunAt, after.PausedReason, after.ConsecutiveFailures)
	}
	// Resuming an active cron is refused, and mints nothing.
	if rec := fx.call(http.MethodPost, "/api/crons/"+c.ID+"/resume", fx.human(), ""); rec.Code != http.StatusConflict {
		t.Errorf("resume of an active cron: %d, want 409", rec.Code)
	}
	if n := len(fx.minter.minted()); n != 2 {
		t.Errorf("%d mints", n)
	}
}

func TestResumeAfterAPauseByTheSchedulerAndAnExpiredToken(t *testing.T) {
	fx := newCronsFx(t)
	c := fx.newCron(func(x *db.Cron) {
		x.TokenID = newUUID()
		x.TokenExpiresAt = time.Now().Add(-time.Hour)
		x.PausedReason = ptr("access token expired")
		x.ConsecutiveFailures = 3
	})
	rec := fx.call(http.MethodPost, "/api/crons/"+c.ID+"/resume", fx.human(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body.String())
	}
	after, err := db.GetCron(context.Background(), fx.pool, c.ID)
	if err != nil || !after.TokenExpiresAt.After(time.Now()) || after.TokenID == c.TokenID || after.PausedReason != nil {
		t.Errorf("after = %+v, %v", after, err)
	}
}

// Only a `claimed` run (a start in progress, or waiting for the reaper) blocks a token swap; a run
// that is merely held or started does not, so a cron that runs often can still be renewed.
func TestResumeAndRenewRefuseOnlyWhileARunIsClaimed(t *testing.T) {
	t.Run("claimed", func(t *testing.T) {
		fx := newCronsFx(t)
		c := fx.newCron(func(x *db.Cron) { x.PausedReason = ptr("paused by you") })
		fx.insertRun(c.ID, db.CronRunClaimed, false)
		rec := fx.call(http.MethodPost, "/api/crons/"+c.ID+"/resume", fx.human(), "")
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "being started") {
			t.Errorf("resume: %d %s, want 409 that names the run being started", rec.Code, rec.Body.String())
		}
		d := fx.newCron(func(x *db.Cron) { x.TokenID = newUUID() })
		fx.insertRun(d.ID, db.CronRunClaimed, false)
		rec = fx.call(http.MethodPost, "/api/crons/"+d.ID+"/renew", fx.human(), "")
		if rec.Code != http.StatusConflict {
			t.Errorf("renew: %d %s, want 409", rec.Code, rec.Body.String())
		}
		if n := len(fx.minter.minted()); n != 0 {
			t.Errorf("minted %d tokens for a refused request", n)
		}
	})
	for _, status := range []string{db.CronRunHeld, db.CronRunStarted, db.CronRunSkipped, db.CronRunFailed} {
		t.Run(status, func(t *testing.T) {
			fx := newCronsFx(t)
			c := fx.newCron(func(x *db.Cron) { x.PausedReason = ptr("paused by you") })
			fx.insertRun(c.ID, status, false)
			if rec := fx.call(http.MethodPost, "/api/crons/"+c.ID+"/resume", fx.human(), ""); rec.Code != http.StatusOK {
				t.Errorf("resume with a %s run: %d %s, want 200", status, rec.Code, rec.Body.String())
			}
			d := fx.newCron(func(x *db.Cron) { x.TokenID = newUUID() })
			fx.insertRun(d.ID, status, false)
			if rec := fx.call(http.MethodPost, "/api/crons/"+d.ID+"/renew", fx.human(), ""); rec.Code != http.StatusOK {
				t.Errorf("renew with a %s run: %d %s, want 200", status, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRenewCron(t *testing.T) {
	fx := newCronsFx(t)
	_, row := fx.create(func(m map[string]any) {})
	if _, err := fx.pool.Exec(context.Background(), `UPDATE crons SET token_expires_at = now() + interval '3 days' WHERE id = $1`, row.ID); err != nil {
		t.Fatal(err)
	}
	rec := fx.call(http.MethodPost, "/api/crons/"+row.ID+"/renew", fx.human(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("renew: %d %s", rec.Code, rec.Body.String())
	}
	after, err := db.GetCron(context.Background(), fx.pool, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TokenID == row.TokenID || !after.TokenExpiresAt.After(time.Now().Add(300*24*time.Hour)) {
		t.Errorf("renewed token = %s expiring %v", after.TokenID, after.TokenExpiresAt)
	}
	if rev := fx.core.revokedList(); len(rev) != 1 || rev[0] != mcpAcct+":"+row.TokenID {
		t.Errorf("revoked = %v, want the old token", rev)
	}
	if calls := fx.minter.minted(); len(calls) != 2 || calls[1].Session != testSID {
		t.Errorf("mints = %+v", calls)
	}
	// A paused cron is resumed, not renewed.
	if rec := fx.call(http.MethodPost, "/api/crons/"+row.ID+"/pause", fx.human(), ""); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	if rec := fx.call(http.MethodPost, "/api/crons/"+row.ID+"/renew", fx.human(), ""); rec.Code != http.StatusConflict {
		t.Errorf("renew of a paused cron: %d, want 409", rec.Code)
	}
	// A failed mint changes nothing.
	fx.minter.err = errCronMintCap
	e := fx.newCron(func(x *db.Cron) { x.TokenID = newUUID() })
	if rec := fx.call(http.MethodPost, "/api/crons/"+e.ID+"/renew", fx.human(), ""); rec.Code != http.StatusConflict {
		t.Errorf("renew with the cap reached: %d, want 409", rec.Code)
	}
	if got, _ := db.GetCron(context.Background(), fx.pool, e.ID); got.TokenID != e.TokenID {
		t.Error("a failed renewal changed the token")
	}
}

func TestPatchCron(t *testing.T) {
	fx := newCronsFx(t)
	c, row := fx.create(nil)
	ctx := context.Background()

	rec := fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), `{"name":"Evening","prompt":"p2","model":"claude-sonnet-5","effort":"high","runtime":"docker","board_id":"b1","grace_seconds":600,"max_runtime_seconds":900}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	after, _ := db.GetCron(ctx, fx.pool, c.ID)
	if after.Name != "Evening" || after.Prompt != "p2" || after.Runtime != "docker" || after.GraceSeconds != 600 || after.MaxRuntimeSeconds != 900 ||
		after.Model == nil || *after.Model != "claude-sonnet-5" || after.BoardID == nil || *after.BoardID != "b1" {
		t.Errorf("after = %+v", after)
	}
	if !after.NextRunAt.Equal(row.NextRunAt) {
		t.Errorf("next_run_at moved (%v -> %v) though the schedule did not change", row.NextRunAt, after.NextRunAt)
	}
	// An empty string clears a nullable field.
	if rec := fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), `{"board_id":""}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if after, _ = db.GetCron(ctx, fx.pool, c.ID); after.BoardID != nil {
		t.Errorf("board_id = %v, want cleared", after.BoardID)
	}

	// A schedule or timezone change recomputes next_run_at from now.
	if _, err := fx.pool.Exec(ctx, `UPDATE crons SET next_run_at = now() - interval '3 days' WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if rec := fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), `{"schedule":"30 9 * * 1-5"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if after, _ = db.GetCron(ctx, fx.pool, c.ID); !after.NextRunAt.After(time.Now()) || after.Schedule != "30 9 * * 1-5" {
		t.Errorf("schedule change: next=%v schedule=%s", after.NextRunAt, after.Schedule)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE crons SET next_run_at = now() - interval '3 days' WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if rec := fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), `{"timezone":"Europe/Paris"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if after, _ = db.GetCron(ctx, fx.pool, c.ID); !after.NextRunAt.After(time.Now()) || after.Timezone != "Europe/Paris" {
		t.Errorf("timezone change: next=%v tz=%s", after.NextRunAt, after.Timezone)
	}

	// Refusals: validation, mcp with no tools, and fields a PATCH may not set.
	for name, body := range map[string]string{
		"bad schedule":     `{"schedule":"*/5 * * * *"}`,
		"bad timezone":     `{"timezone":"Nowhere/Land"}`,
		"engine":           `{"engine":"codex"}`,
		"token id":         `{"token_id":"x"}`,
		"token expiry":     `{"token_expires_at":"2099-01-01T00:00:00Z"}`,
		"paused reason":    `{"paused_reason":""}`,
		"owner":            `{"owner_account_id":"user-9"}`,
		"mcp without tool": `{"mcp":[{"connection":"` + fx.connID + `"}]}`,
		"empty name":       `{"name":""}`,
	} {
		if rec := fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	// A stale hash is a conflict.
	if rec := fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(),
		`{"mcp":[{"connection":"`+fx.connID+`","tools":{"echo":{"mode":"allow","hash":"nope"}}}]}`); rec.Code != http.StatusConflict {
		t.Errorf("stale hash: %d, want 409", rec.Code)
	}
	// A valid mcp edit, and clearing it.
	if rec := fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), `{"mcp":[]}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if after, _ = db.GetCron(ctx, fx.pool, c.ID); string(after.MCP) != "[]" {
		t.Errorf("mcp = %s", after.MCP)
	}
}

func (fx *cronsFx) insertRun(cronID, status string, late bool) string {
	fx.t.Helper()
	id := newUUID()
	if _, err := fx.pool.Exec(context.Background(),
		`INSERT INTO cron_runs (id, cron_id, scheduled_for, status, late, reason) VALUES ($1, $2, now() - interval '1 hour', $3, $4, $5)`,
		id, cronID, status, late, "why "+status); err != nil {
		fx.t.Fatal(err)
	}
	return id
}

func TestCronRunsList(t *testing.T) {
	fx := newCronsFx(t)
	c := fx.newCron(nil)
	fx.insertRun(c.ID, db.CronRunStarted, true)
	fx.insertRun(c.ID, db.CronRunSkipped, false)
	rec := fx.call(http.MethodGet, "/api/crons/"+c.ID+"/runs", fx.human(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Runs []struct {
			ID           string  `json:"id"`
			ScheduledFor string  `json:"scheduled_for"`
			Status       string  `json:"status"`
			Reason       *string `json:"reason"`
			Late         bool    `json:"late"`
			Manual       bool    `json:"manual"`
			SessionID    *string `json:"session_id"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Runs) != 2 {
		t.Fatalf("body %s (%v)", rec.Body.String(), err)
	}
	late := 0
	for _, r := range out.Runs {
		if r.Late {
			late++
			if r.Status != "started" {
				t.Errorf("late run = %+v", r)
			}
		}
		if r.ScheduledFor == "" || r.Reason == nil {
			t.Errorf("run = %+v", r)
		}
	}
	if late != 1 {
		t.Errorf("%d late runs, want 1", late)
	}
	// The cron's own summary carries its last run.
	rec = fx.call(http.MethodGet, "/api/crons/"+c.ID, fx.human(), "")
	if got := decodeCron(t, rec); got.LastRun == nil {
		t.Errorf("no last_run in %s", rec.Body.String())
	}
	if rec := fx.call(http.MethodGet, "/api/crons/"+c.ID+"/runs?limit=1", fx.human(), ""); !strings.Contains(rec.Body.String(), `"runs":[{`) {
		t.Errorf("limit: %s", rec.Body.String())
	}
}

func TestRunCronNow(t *testing.T) {
	fx := newCronsFx(t)
	fx.core.live[cronTok] = true
	c := fx.newCron(nil)
	rec := fx.call(http.MethodPost, "/api/crons/"+c.ID+"/run", fx.human(), "")
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	var run struct {
		Status    string  `json:"status"`
		Manual    bool    `json:"manual"`
		SessionID *string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil || run.Status != "started" || !run.Manual || run.SessionID == nil {
		t.Fatalf("run = %s (%v)", rec.Body.String(), err)
	}
	if s := fx.session(*run.SessionID); s.CronID == nil || *s.CronID != c.ID || !s.Private {
		t.Errorf("session = %+v", s)
	}
	// A second run while the first is still active is refused.
	if rec := fx.call(http.MethodPost, "/api/crons/"+c.ID+"/run", fx.human(), ""); rec.Code != http.StatusConflict {
		t.Errorf("overlapping run: %d %s, want 409", rec.Code, rec.Body.String())
	}
	// A paused cron cannot run.
	p := fx.newCron(func(x *db.Cron) { x.TokenID = newUUID(); x.PausedReason = ptr("paused by you") })
	if rec := fx.call(http.MethodPost, "/api/crons/"+p.ID+"/run", fx.human(), ""); rec.Code != http.StatusConflict {
		t.Errorf("run of a paused cron: %d %s, want 409", rec.Code, rec.Body.String())
	}
}

func TestCronRoutesNeedTheCronService(t *testing.T) {
	fx := newMCPFx(t) // no CronService
	mux := http.NewServeMux()
	fx.api.registerCronRoutes(mux, &fakeCronMinter{})
	req := httptest.NewRequest(http.MethodGet, "/api/crons", nil)
	req.Header.Set("Authorization", "Bearer "+fx.human())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without crons enabled: %d, want 503", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/crons", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token without crons enabled: %d, want 401 (auth first)", rec.Code)
	}
}

func TestHTTPCronMinter(t *testing.T) {
	var got map[string]any
	var key string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("X-Internal-Key")
		if r.URL.Path != "/internal/tokens/mint" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		if status == http.StatusNotFound {
			http.Error(w, "not found", status) // core's own uniform answer
			return
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"id":"tok-9","expires_at":"2030-01-02T03:04:05Z"}`))
		}
	}))
	defer srv.Close()
	m := &HTTPCronMinter{BaseURL: srv.URL + "/", InternalKey: "k"}
	id, exp, err := m.MintToken(context.Background(), "acct", "sid-1", "cron: x", 365)
	if err != nil || id != "tok-9" || exp.Year() != 2030 || key != "k" {
		t.Fatalf("mint = %q %v %v (key %q)", id, exp, err, key)
	}
	if got["account_id"] != "acct" || got["session_id"] != "sid-1" || got["name"] != "cron: x" || got["expires_in_days"] != float64(365) || got["token_id"] != nil {
		t.Errorf("body = %v", got)
	}
	for st, want := range map[int]error{http.StatusConflict: errCronMintCap, http.StatusNotFound: errCronMintProof} {
		status = st
		if _, _, err := m.MintToken(context.Background(), "acct", "sid-1", "n", 1); !errors.Is(err, want) {
			t.Errorf("%d: %v, want %v", st, err, want)
		}
	}
	status = http.StatusInternalServerError
	if _, _, err := m.MintToken(context.Background(), "acct", "sid-1", "n", 1); err == nil || errors.Is(err, errCronMintCap) || errors.Is(err, errCronMintProof) {
		t.Errorf("500: %v", err)
	}
	if _, _, err := (&HTTPCronMinter{}).MintToken(context.Background(), "a", "s", "n", 1); err == nil {
		t.Error("an unconfigured minter succeeded")
	}
}
