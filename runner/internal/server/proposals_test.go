package server

// The proposals routes (proposals.go, routes_proposals.go, spec 9) against a real database, the
// real gateway code for the approval's upstream call, a fake core (connection list, credentials)
// and a fake Streamable HTTP MCP upstream.

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
	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type propFx struct {
	*mcpFx
	mux *http.ServeMux
}

// newPropFx builds the fixture. callTimeout, when non-zero, is the gateway's per-call timeout.
func newPropFx(t *testing.T, callTimeout time.Duration) *propFx {
	t.Helper()
	fx := newMCPFx(t)
	if callTimeout > 0 {
		gw := mcpgw.New(mcpgw.Config{
			DB: fx.pool, Core: fx.tokens, CallTimeout: callTimeout,
			Policy: netguard.Policy{AllowHTTPHosts: []string{"127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}},
		})
		fx.api.setMCPStart(&mcpStartConfig{Hasher: gw, GatewayURL: mcpGatewayURL, Core: fx.list})
	}
	mux := http.NewServeMux()
	fx.api.RegisterProposalRoutes(mux)
	return &propFx{mcpFx: fx, mux: mux}
}

func (fx *propFx) req(method, path, token, body string) *httptest.ResponseRecorder {
	fx.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, r)
	return rec
}

// insert stores a pending proposal of the fixture's account on the fixture's connection.
func (fx *propFx) insert(tool, args string) *db.Proposal {
	fx.t.Helper()
	p, err := db.InsertProposal(context.Background(), fx.pool, db.Proposal{
		AccountID: mcpAcct, ConnectionID: fx.connID, ConnectionName: mcpConn, URLSnapshot: fx.up.url(),
		Tool: tool, ToolHash: fx.up.hash(tool), Arguments: json.RawMessage(args), AgentSummary: tool + ": summary",
	}, 50)
	if err != nil {
		fx.t.Fatal(err)
	}
	return p
}

func (fx *propFx) row(id string) *db.Proposal {
	fx.t.Helper()
	p, err := db.GetOwnedProposal(context.Background(), fx.pool, mcpAcct, id)
	if err != nil {
		fx.t.Fatal(err)
	}
	return p
}

func (fx *propFx) otherHuman() string {
	return mintRunnerToken(fx.t, fx.priv, "core-1", identity.Claims{
		Sub: otherAcct, Aud: coreAuthAudience, Kind: "human", Sid: newUUID(),
		Caps: []string{"session.start"}, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
}

type propView struct {
	ID             string          `json:"id"`
	State          string          `json:"state"`
	ConnectionID   string          `json:"connection_id"`
	ConnectionName string          `json:"connection_name"`
	Tool           string          `json:"tool"`
	Arguments      json.RawMessage `json:"arguments"`
	AgentSummary   string          `json:"agent_summary"`
	SessionID      *string         `json:"session_id"`
	CronID         *string         `json:"cron_id"`
	CreatedAt      string          `json:"created_at"`
	DecidedAt      *string         `json:"decided_at"`
	DecidedBy      *string         `json:"decided_by"`
	Result         json.RawMessage `json:"result"`
	ExpiresAt      string          `json:"expires_at"`
	Error          string          `json:"error"`
}

func decodeProp(t *testing.T, rec *httptest.ResponseRecorder) propView {
	t.Helper()
	var v propView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

// ---- access: every route, every kind of caller ---------------------------------------------

func TestProposalRoutesAccessMatrix(t *testing.T) {
	fx := newPropFx(t, 0)
	p := fx.insert("send", `{"to":"a@example.com"}`)
	routes := []struct{ method, path, body string }{
		{"GET", "/api/proposals", ""},
		{"GET", "/api/proposals/count", ""},
		{"GET", "/api/proposals/" + p.ID, ""},
		{"POST", "/api/proposals/" + p.ID + "/approve", ""},
		{"POST", "/api/proposals/" + p.ID + "/reject", ""},
		{"POST", "/api/proposals/" + p.ID + "/resolve", `{"outcome":"done"}`},
	}
	for _, rt := range routes {
		name := rt.method + " " + rt.path
		t.Run(name, func(t *testing.T) {
			if rec := fx.req(rt.method, rt.path, "", rt.body); rec.Code != http.StatusUnauthorized {
				t.Errorf("no token: %d, want 401", rec.Code)
			}
			if rec := fx.req(rt.method, rt.path, "garbage", rt.body); rec.Code != http.StatusUnauthorized {
				t.Errorf("bad token: %d, want 401", rec.Code)
			}
			if rec := fx.req(rt.method, rt.path, runnerTestKey, rt.body); rec.Code != http.StatusUnauthorized {
				t.Errorf("the runner key: %d, want 401", rec.Code)
			}
			if rec := fx.req(rt.method, rt.path, fx.token("agent", ""), rt.body); rec.Code != http.StatusForbidden {
				t.Errorf("agent token: %d, want 403", rec.Code)
			}
			if rec := fx.req(rt.method, rt.path, fx.token("human", ""), rt.body); rec.Code != http.StatusUnauthorized {
				t.Errorf("a person without a login session: %d, want 401", rec.Code)
			}
		})
	}
	// None of that touched the proposal or the upstream.
	if got := fx.row(p.ID); got.State != db.ProposalPending || got.DecidedAt != nil {
		t.Errorf("refused calls changed the proposal: %+v", got)
	}
	if fx.up.callCount() != 0 || len(fx.tokens.proofsSeen()) != 0 {
		t.Error("refused calls reached the gateway")
	}
}

func TestProposalRoutesAnotherAccountsProposalIs404(t *testing.T) {
	fx := newPropFx(t, 0)
	p := fx.insert("send", `{}`)
	other := fx.otherHuman()
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/api/proposals/" + p.ID, ""},
		{"POST", "/api/proposals/" + p.ID + "/approve", ""},
		{"POST", "/api/proposals/" + p.ID + "/reject", ""},
		{"POST", "/api/proposals/" + p.ID + "/resolve", `{"outcome":"done"}`},
	} {
		rec := fx.req(rt.method, rt.path, other, rt.body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", rt.method, rt.path, rec.Code)
		}
		missing := fx.req(rt.method, strings.Replace(rt.path, p.ID, newUUID(), 1), fx.human(), rt.body)
		if missing.Code != http.StatusNotFound || missing.Body.String() != rec.Body.String() {
			t.Errorf("%s %s: a missing proposal must look exactly like another account's (%d %q vs %q)",
				rt.method, rt.path, missing.Code, missing.Body.String(), rec.Body.String())
		}
	}
	if bad := fx.req("GET", "/api/proposals/not-a-uuid", fx.human(), ""); bad.Code != http.StatusNotFound {
		t.Errorf("malformed id: %d", bad.Code)
	}
	var list struct {
		Proposals []propView `json:"proposals"`
		Pending   int        `json:"pending_count"`
	}
	rec := fx.req("GET", "/api/proposals", other, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 || len(list.Proposals) != 0 || list.Pending != 0 {
		t.Errorf("another account's list: %d %s", rec.Code, rec.Body.String())
	}
	if fx.up.callCount() != 0 || fx.row(p.ID).State != db.ProposalPending {
		t.Error("another account reached the proposal")
	}
}

// ---- list and get ------------------------------------------------------------------------------

func TestProposalListAndGetShape(t *testing.T) {
	fx := newPropFx(t, 0)
	a := fx.insert("send", `{"to":"a@example.com"}`)
	b := fx.insert("echo", `{"msg":"hi"}`)
	if rec := fx.req("POST", "/api/proposals/"+a.ID+"/reject", fx.human(), ""); rec.Code != 200 {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}

	rec := fx.req("GET", "/api/proposals", fx.human(), "")
	var list struct {
		Proposals []propView `json:"proposals"`
		Pending   int        `json:"pending_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if len(list.Proposals) != 1 || list.Pending != 1 || list.Proposals[0].ID != b.ID {
		t.Fatalf("list = %+v (pending %d), want the open proposal only", list.Proposals, list.Pending)
	}
	got := list.Proposals[0]
	if got.State != "pending" || got.ConnectionName != mcpConn || got.ConnectionID != fx.connID || got.Tool != "echo" ||
		string(got.Arguments) != `{"msg":"hi"}` || got.AgentSummary != "echo: summary" || got.CreatedAt == "" ||
		got.ExpiresAt == "" || got.DecidedAt != nil {
		t.Errorf("item = %+v", got)
	}
	created, _ := time.Parse(time.RFC3339, got.CreatedAt)
	expires, _ := time.Parse(time.RFC3339, got.ExpiresAt)
	if d := expires.Sub(created); d != 7*24*time.Hour {
		t.Errorf("expires_at is %v after created_at, want 7 days", d)
	}
	for _, leak := range []string{"url_snapshot", "tool_hash", "upstream-secret", fx.up.url(), "account_id"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("the list leaks %q: %s", leak, rec.Body.String())
		}
	}

	pend := fx.req("GET", "/api/proposals?state=pending", fx.human(), "")
	var pl struct {
		Proposals []propView `json:"proposals"`
	}
	_ = json.Unmarshal(pend.Body.Bytes(), &pl)
	if len(pl.Proposals) != 1 || pl.Proposals[0].ID != b.ID {
		t.Errorf("?state=pending = %s", pend.Body.String())
	}
	if rec := fx.req("GET", "/api/proposals?state=bogus", fx.human(), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown state: %d, want 400", rec.Code)
	}

	one := decodeProp(t, fx.req("GET", "/api/proposals/"+a.ID, fx.human(), ""))
	if one.State != "rejected" || one.DecidedAt == nil || one.DecidedBy == nil || *one.DecidedBy != mcpAcct {
		t.Errorf("get = %+v", one)
	}
}

// ---- approve -----------------------------------------------------------------------------------

func TestApproveRunsTheFrozenArgumentsOnce(t *testing.T) {
	fx := newPropFx(t, 0)
	p := fx.insert("send", `{"to":"a@example.com","n":12345678901234567890}`)
	rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), "")
	if rec.Code != 200 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	v := decodeProp(t, rec)
	if v.State != "done" || v.DecidedAt == nil || !strings.Contains(string(v.Result), "ok:send") {
		t.Errorf("response = %+v", v)
	}
	if fx.up.callCount() != 1 {
		t.Fatalf("upstream called %d times, want 1", fx.up.callCount())
	}
	// Exactly the stored arguments, digit for digit (a number beyond float64 must survive).
	if sent := string(fx.up.lastCallArgs()); !strings.Contains(sent, `"a@example.com"`) || !strings.Contains(sent, "12345678901234567890") {
		t.Errorf("upstream received %s", sent)
	}
	// The credential was fetched with the approver's own login session.
	var saw bool
	for _, pr := range fx.tokens.proofsSeen() {
		if pr.SessionID == testSID && pr.AccountID == mcpAcct && pr.TokenID == "" {
			saw = true
		}
		if pr.SessionID != testSID {
			t.Errorf("a credential was fetched with proof %+v", pr)
		}
	}
	if !saw {
		t.Error("no credential fetch used the approver's login session")
	}
	if strings.Contains(rec.Body.String(), "upstream-secret") {
		t.Error("the response carries the credential")
	}
	// A repeat is refused and runs nothing.
	if again := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), ""); again.Code != http.StatusConflict {
		t.Errorf("second approve: %d, want 409", again.Code)
	}
	if fx.up.callCount() != 1 {
		t.Errorf("a repeat ran the tool again (%d calls)", fx.up.callCount())
	}
}

func TestConcurrentApprovesRunTheToolOnce(t *testing.T) {
	fx := newPropFx(t, 0)
	fx.up.mu.Lock()
	fx.up.callDelay = 300 * time.Millisecond // keep the winner executing while the others arrive
	fx.up.mu.Unlock()
	p := fx.insert("send", `{"to":"a@example.com"}`)
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), "")
			mu.Lock()
			codes[rec.Code]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if codes[200] != 1 || codes[409] != 7 {
		t.Fatalf("status codes = %v, want one 200 and seven 409", codes)
	}
	if fx.up.callCount() != 1 {
		t.Fatalf("the tool was called %d times, want 1", fx.up.callCount())
	}
	if fx.row(p.ID).State != db.ProposalDone {
		t.Errorf("state = %s", fx.row(p.ID).State)
	}
}

func TestApproveRefusedWhenTheSnapshotNoLongerMatches(t *testing.T) {
	cases := []struct {
		name    string
		change  func(fx *propFx)
		want    int
		state   string
		mention string
	}{
		{"the connection's URL changed", func(fx *propFx) {
			fx.list.mu.Lock()
			fx.list.conns[0].URL += "?other=1"
			fx.list.mu.Unlock()
		}, http.StatusConflict, db.ProposalPending, "address"},
		{"the tool's definition changed", func(fx *propFx) { fx.up.setDescription("send", "now it does something else") },
			http.StatusConflict, db.ProposalPending, "changed"},
		{"the tool is gone", func(fx *propFx) {
			fx.up.mu.Lock()
			fx.up.tools = fx.up.tools[:1]
			fx.up.mu.Unlock()
		}, http.StatusConflict, db.ProposalPending, "changed"},
		{"the connection was deleted", func(fx *propFx) {
			fx.list.mu.Lock()
			fx.list.conns = nil
			fx.list.mu.Unlock()
		}, http.StatusConflict, db.ProposalFailed, "no longer exists"},
		{"the connection needs attention", func(fx *propFx) {
			fx.list.mu.Lock()
			fx.list.conns[0].Status = "needs_auth"
			fx.list.mu.Unlock()
		}, http.StatusConflict, db.ProposalPending, "needs attention"},
		{"the sign-in ended", func(fx *propFx) {
			fx.list.mu.Lock()
			fx.list.err = ErrMCPProofInvalid
			fx.list.mu.Unlock()
		}, http.StatusUnauthorized, db.ProposalPending, "sign in"},
		{"core is unreachable", func(fx *propFx) {
			fx.list.mu.Lock()
			fx.list.err = errors.New("core down")
			fx.list.mu.Unlock()
		}, http.StatusBadGateway, db.ProposalPending, "blerg-core"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newPropFx(t, 0)
			p := fx.insert("send", `{"to":"a@example.com"}`)
			tc.change(fx)
			rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), "")
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if !strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(tc.mention)) {
				t.Errorf("the refusal must explain itself (%q): %s", tc.mention, rec.Body.String())
			}
			if fx.up.callCount() != 0 {
				t.Errorf("the tool was called %d times", fx.up.callCount())
			}
			if got := fx.row(p.ID); got.State != tc.state {
				t.Errorf("state = %s, want %s", got.State, tc.state)
			}
			if strings.Contains(rec.Body.String(), "upstream-secret") || strings.Contains(rec.Body.String(), fx.up.url()) {
				t.Errorf("the refusal leaks a credential or address: %s", rec.Body.String())
			}
		})
	}
}

func TestApproveTimeoutAfterSendingIsUnknownAndNeverRetried(t *testing.T) {
	fx := newPropFx(t, 500*time.Millisecond)
	fx.up.mu.Lock()
	fx.up.callDelay = 5 * time.Second // the tool call outlives the gateway's timeout
	fx.up.mu.Unlock()
	p := fx.insert("send", `{"to":"a@example.com"}`)
	rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), "")
	if rec.Code != 200 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	v := decodeProp(t, rec)
	if v.State != "unknown" || !strings.Contains(string(v.Result), "timed out") {
		t.Fatalf("response = %+v, want state unknown with an explanation", v)
	}
	calls := fx.up.callCount()
	if calls != 1 {
		t.Fatalf("the tool was called %d times, want 1", calls)
	}
	// Approving again is refused; nothing retries it.
	if again := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), ""); again.Code != http.StatusConflict {
		t.Errorf("approve of an unknown outcome: %d, want 409", again.Code)
	}
	if rej := fx.req("POST", "/api/proposals/"+p.ID+"/reject", fx.human(), ""); rej.Code != http.StatusConflict {
		t.Errorf("reject of an unknown outcome: %d, want 409", rej.Code)
	}
	time.Sleep(300 * time.Millisecond)
	if fx.up.callCount() != calls {
		t.Error("the tool was called again after an unknown outcome")
	}

	// A person who checked decides what happened.
	if bad := fx.req("POST", "/api/proposals/"+p.ID+"/resolve", fx.human(), `{"outcome":"rejected"}`); bad.Code != http.StatusBadRequest {
		t.Errorf("resolve with a bad outcome: %d, want 400", bad.Code)
	}
	if bad := fx.req("POST", "/api/proposals/"+p.ID+"/resolve", fx.human(), `{}`); bad.Code != http.StatusBadRequest {
		t.Errorf("resolve without an outcome: %d, want 400", bad.Code)
	}
	res := fx.req("POST", "/api/proposals/"+p.ID+"/resolve", fx.human(), `{"outcome":"done"}`)
	if res.Code != 200 || decodeProp(t, res).State != "done" {
		t.Fatalf("resolve: %d %s", res.Code, res.Body.String())
	}
	if again := fx.req("POST", "/api/proposals/"+p.ID+"/resolve", fx.human(), `{"outcome":"failed"}`); again.Code != http.StatusConflict {
		t.Errorf("resolving twice: %d, want 409", again.Code)
	}
	if fx.up.callCount() != calls {
		t.Error("resolving called the tool")
	}
}

func TestApproveDefiniteFailuresAreFailed(t *testing.T) {
	t.Run("the tool reports an error", func(t *testing.T) {
		fx := newPropFx(t, 0)
		fx.up.mu.Lock()
		fx.up.callResult = map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "mailbox full"}}}
		fx.up.mu.Unlock()
		p := fx.insert("send", `{}`)
		v := decodeProp(t, fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), ""))
		if v.State != "failed" || !strings.Contains(string(v.Result), "mailbox full") {
			t.Errorf("view = %+v", v)
		}
	})
	t.Run("the server answers with an error", func(t *testing.T) {
		fx := newPropFx(t, 0)
		fx.up.mu.Lock()
		fx.up.callRPCError = "no such mailbox"
		fx.up.callRPCCode = -32602 // invalid params: the server refused before acting
		fx.up.mu.Unlock()
		p := fx.insert("send", `{}`)
		v := decodeProp(t, fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), ""))
		if v.State != "failed" || !strings.Contains(string(v.Result), "no such mailbox") {
			t.Errorf("view = %+v", v)
		}
	})
	t.Run("result is text only", func(t *testing.T) {
		fx := newPropFx(t, 0)
		fx.up.mu.Lock()
		fx.up.callResult = map[string]any{"content": []map[string]any{
			{"type": "image", "data": "AAAA", "mimeType": "image/png"}, {"type": "text", "text": "sent"},
		}}
		fx.up.mu.Unlock()
		p := fx.insert("send", `{}`)
		rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), "")
		if v := decodeProp(t, rec); v.State != "done" || strings.Contains(string(v.Result), "AAAA") || !strings.Contains(string(v.Result), "sent") {
			t.Errorf("view = %+v", v)
		}
	})
}

// ---- reject ---------------------------------------------------------------------------------------

func TestRejectThenApproveIsRefused(t *testing.T) {
	fx := newPropFx(t, 0)
	p := fx.insert("send", `{}`)
	rec := fx.req("POST", "/api/proposals/"+p.ID+"/reject", fx.human(), "")
	if v := decodeProp(t, rec); rec.Code != 200 || v.State != "rejected" || v.DecidedAt == nil {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}
	if rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), ""); rec.Code != http.StatusConflict {
		t.Errorf("approve of a rejected proposal: %d, want 409", rec.Code)
	}
	if rec := fx.req("POST", "/api/proposals/"+p.ID+"/reject", fx.human(), ""); rec.Code != http.StatusConflict {
		t.Errorf("reject twice: %d, want 409", rec.Code)
	}
	if fx.up.callCount() != 0 {
		t.Error("a rejected proposal reached the upstream")
	}
}

// ---- maintenance: expiry, stale executing, prune -----------------------------------------------

func TestProposalMaintenanceExpiresStaleAndPrunes(t *testing.T) {
	fx := newPropFx(t, 0)
	ctx := context.Background()
	old := fx.insert("send", `{}`)
	fresh := fx.insert("send", `{}`)
	done := fx.insert("send", `{}`)
	stuck := fx.insert("send", `{}`)
	if _, err := fx.pool.Exec(ctx, `UPDATE mcp_proposals SET created_at = now() - interval '8 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RejectProposal(ctx, fx.pool, mcpAcct, done.ID, mcpAcct); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE mcp_proposals SET decided_at = now() - interval '31 days' WHERE id = $1`, done.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginProposalExecution(ctx, fx.pool, mcpAcct, stuck.ID, mcpAcct); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE mcp_proposals SET decided_at = now() - interval '2 hours' WHERE id = $1`, stuck.ID); err != nil {
		t.Fatal(err)
	}

	rep := fx.api.maintainProposals(ctx)
	if rep.Expired != 1 || rep.Stale != 1 || rep.Pruned != 1 {
		t.Fatalf("report = %+v, want 1 expired, 1 stale, 1 pruned", rep)
	}
	if fx.row(old.ID).State != db.ProposalExpired || fx.row(fresh.ID).State != db.ProposalPending || fx.row(stuck.ID).State != db.ProposalUnknown {
		t.Errorf("states: old=%s fresh=%s stuck=%s", fx.row(old.ID).State, fx.row(fresh.ID).State, fx.row(stuck.ID).State)
	}
	if _, err := db.GetOwnedProposal(ctx, fx.pool, mcpAcct, done.ID); !errors.Is(err, db.ErrProposalNotFound) {
		t.Error("a decided proposal 31 days old should have been pruned")
	}
	// An expired proposal can no longer be approved.
	if rec := fx.req("POST", "/api/proposals/"+old.ID+"/approve", fx.human(), ""); rec.Code != http.StatusConflict {
		t.Errorf("approve of an expired proposal: %d, want 409", rec.Code)
	}
}

// ---- the sweeper -----------------------------------------------------------------------------------

func TestSweeperFailsPendingProposalsOfADeletedConnection(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	c := cf.newCron(nil)
	proposalOn := func(conn, session string) *db.Proposal {
		p, err := db.InsertProposal(ctx, cf.pool, db.Proposal{
			AccountID: mcpAcct, SessionID: session, ConnectionID: conn, ConnectionName: mcpConn, URLSnapshot: cf.up.url(),
			Tool: "send", ToolHash: "h", Arguments: json.RawMessage(`{}`),
		}, 50)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	cronSession := newUUID()
	cf.insertCronSession(cronSession, c.ID, mcpAcct, "ended") // ended: no grant is left to sweep
	onCron := proposalOn(cf.connID, cronSession)
	if onCron.CronID != c.ID {
		t.Fatalf("the proposal did not pick up its session's cron: %q", onCron.CronID)
	}
	otherConn := proposalOn("99999999-9999-4999-8999-999999999999", cronSession)

	// The person's sign-in is what proves a human-started session's grant.
	humanSession := newUUID()
	cf.insertCronSession(humanSession, c.ID, mcpAcct, "running")
	specs := []mcpgw.GrantSpec{{ConnectionID: cf.connID, Name: mcpConn, URL: cf.up.url(),
		Tools: map[string]mcpgw.ToolGrant{"send": {Mode: mcpgw.ModePropose, Hash: "h"}}}}
	if _, err := mcpgw.CreateGrants(ctx, cf.pool, humanSession, mcpAcct, mcpgw.Proof{AccountID: mcpAcct, SessionID: testSID}, specs); err != nil {
		t.Fatal(err)
	}
	onHuman := proposalOn(cf.connID, humanSession)

	state := func(p *db.Proposal) string {
		got, err := db.GetOwnedProposal(ctx, cf.pool, mcpAcct, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.State
	}

	// A dead sign-in is not a deleted connection: nothing is failed.
	cf.list.err = ErrMCPProofInvalid
	cf.svc.SweepOnce(ctx)
	cf.list.err = nil
	if state(onCron) != db.ProposalPending || state(onHuman) != db.ProposalPending {
		t.Fatalf("a dead proof must not fail proposals: %s %s", state(onCron), state(onHuman))
	}
	// Core unreachable: nothing either.
	cf.list.err = errors.New("core down")
	cf.svc.SweepOnce(ctx)
	cf.list.err = nil
	if state(onCron) != db.ProposalPending {
		t.Fatal("core being down must not fail proposals")
	}

	// The connection is deleted: its pending proposals fail with a reason; others stay.
	cf.list.conns = nil
	cf.svc.SweepOnce(ctx)
	got, _ := db.GetOwnedProposal(ctx, cf.pool, mcpAcct, onCron.ID)
	if got.State != db.ProposalFailed || !strings.Contains(string(got.Result), "deleted") {
		t.Errorf("cron proposal = %s %s", got.State, got.Result)
	}
	if state(onHuman) != db.ProposalFailed {
		t.Errorf("a human session's proposal on a deleted connection is %s", state(onHuman))
	}
	if state(otherConn) != db.ProposalPending {
		t.Errorf("a proposal on a connection core never reported gone changed to %s", state(otherConn))
	}
}
