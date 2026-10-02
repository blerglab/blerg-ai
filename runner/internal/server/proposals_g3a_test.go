package server

// Round-2 fixes to the proposals routes: open vs decided listing, the cheap count, nothing-sent
// release, stale approvals, a disconnecting client, NUL in results, and maintenance without crons.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// listBody is the list response.
type listBody struct {
	Proposals   []propView `json:"proposals"`
	DecidedNext *string    `json:"decided_next"`
	Pending     int        `json:"pending_count"`
}

func (fx *propFx) listProposals(query string) (listBody, int) {
	fx.t.Helper()
	rec := fx.req("GET", "/api/proposals"+query, fx.human(), "")
	var out listBody
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			fx.t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
	}
	return out, rec.Code
}

// decide sets a proposal's state directly (as if it had been decided) and its age.
func (fx *propFx) decide(id, state string, age time.Duration) {
	fx.t.Helper()
	if _, err := fx.pool.Exec(context.Background(), `UPDATE mcp_proposals SET state = $2, decided_at = now(),
		created_at = now() - make_interval(secs => $3) WHERE id = $1`, id, state, age.Seconds()); err != nil {
		fx.t.Fatal(err)
	}
}

// execStub replaces the gateway's execution of an approval while keeping its live tool check.
type execStub struct {
	liveToolHasher
	run func(ctx context.Context, c mcpgw.ApprovedCall) (mcpgw.ApprovedResult, error)
}

func (e execStub) ExecuteApproved(ctx context.Context, c mcpgw.ApprovedCall) (mcpgw.ApprovedResult, error) {
	return e.run(ctx, c)
}

func TestProposalListScopes(t *testing.T) {
	fx := newPropFx(t, 0)
	var open []*db.Proposal
	for range 3 {
		open = append(open, fx.insert("send", `{}`))
	}
	// 230 decided proposals that are all NEWER than the open ones: the old single capped list
	// showed only the newest 200 and lost the open ones.
	for i := range 230 {
		p := fx.insert("send", `{}`)
		fx.decide(p.ID, []string{"done", "rejected", "failed", "expired"}[i%4], -time.Duration(i+1)*time.Minute)
	}
	exec := fx.insert("send", `{}`)
	fx.decide(exec.ID, "executing", time.Hour)
	unk := fx.insert("send", `{}`)
	fx.decide(unk.ID, "unknown", 2*time.Hour)

	got, code := fx.listProposals("")
	if code != 200 || len(got.Proposals) != 5 || got.DecidedNext != nil || got.Pending != 3 {
		t.Fatalf("default list = %d proposals, next %v, pending %d (code %d); want the 5 open ones", len(got.Proposals), got.DecidedNext, got.Pending, code)
	}
	for i, p := range got.Proposals {
		if want := []string{"pending", "pending", "pending", "executing", "unknown"}[i]; p.State != want {
			t.Errorf("row %d is %s, want %s (pending first, then executing, then unknown)", i, p.State, want)
		}
	}
	if o, _ := fx.listProposals("?scope=open"); len(o.Proposals) != 5 {
		t.Errorf("scope=open = %d", len(o.Proposals))
	}
	if o, _ := fx.listProposals("?scope=open&state=unknown"); len(o.Proposals) != 1 || o.Proposals[0].ID != unk.ID {
		t.Errorf("scope=open&state=unknown = %+v", o.Proposals)
	}

	// Decided ones page by cursor, newest first, never repeating or skipping.
	seen := map[string]bool{}
	next := ""
	pages := 0
	for {
		q := "?scope=decided&limit=100"
		if next != "" {
			q += "&before=" + url.QueryEscape(next)
		}
		page, code := fx.listProposals(q)
		if code != 200 {
			t.Fatalf("decided page: %d", code)
		}
		pages++
		for _, p := range page.Proposals {
			if seen[p.ID] {
				t.Fatalf("%s repeated", p.ID)
			}
			if p.State == "pending" || p.State == "executing" || p.State == "unknown" {
				t.Fatalf("an open proposal in the decided scope: %s", p.State)
			}
			seen[p.ID] = true
		}
		if page.Pending != 3 {
			t.Errorf("pending_count = %d on a decided page", page.Pending)
		}
		if page.DecidedNext == nil {
			break
		}
		next = *page.DecidedNext
		if pages > 5 {
			t.Fatal("does not end")
		}
	}
	if len(seen) != 230 || pages != 3 {
		t.Errorf("saw %d decided in %d pages, want 230 in 3", len(seen), pages)
	}
	first, _ := fx.listProposals("?scope=decided")
	if len(first.Proposals) != 50 || first.DecidedNext == nil {
		t.Errorf("default page = %d rows, next %v; want 50 and a cursor", len(first.Proposals), first.DecidedNext)
	}
	if only, _ := fx.listProposals("?scope=decided&state=rejected&limit=1000"); len(only.Proposals) != 58 && len(only.Proposals) != 57 {
		t.Errorf("state=rejected = %d rows", len(only.Proposals))
	}
	_ = open

	for _, bad := range []string{"?scope=all", "?scope=decided&before=nonsense", "?scope=open&state=done", "?scope=decided&state=pending",
		"?state=bogus", "?limit=abc", "?scope=open&before=x"} {
		if _, code := fx.listProposals(bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
}

func TestProposalCountIsCheapAndExact(t *testing.T) {
	fx := newPropFx(t, 0)
	a := fx.insert("send", `{}`)
	fx.insert("send", `{}`)
	fx.decide(a.ID, "done", time.Minute)
	rec := fx.req("GET", "/api/proposals/count", fx.human(), "")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"pending_count":1}` {
		t.Fatalf("count = %d %s", rec.Code, rec.Body.String())
	}
	if rec := fx.req("GET", "/api/proposals/count", fx.otherHuman(), ""); strings.TrimSpace(rec.Body.String()) != `{"pending_count":0}` {
		t.Errorf("another account's count = %s", rec.Body.String())
	}
}

func TestApproveOfAStaleUnsweptProposalIsRefusedAsExpired(t *testing.T) {
	fx := newPropFx(t, 0)
	p := fx.insert("send", `{}`)
	if _, err := fx.pool.Exec(context.Background(), `UPDATE mcp_proposals SET created_at = now() - interval '8 days' WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "expired") {
		t.Fatalf("approve of a stale proposal: %d %s, want 409 expired", rec.Code, rec.Body.String())
	}
	if fx.up.callCount() != 0 || fx.row(p.ID).State != db.ProposalPending {
		t.Error("a stale proposal must not run or change")
	}
}

func TestApproveNothingSentReleasesTheProposal(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantState  string
	}{
		{"credential or core error", &mcpgw.ApprovedError{Msg: "could not fetch the credential", NotSent: true}, http.StatusBadGateway, "pending"},
		{"tool changed", &mcpgw.ApprovedError{Msg: "the tool changed", NotSent: true, Changed: true}, http.StatusConflict, "pending"},
		{"connection deleted is final", &mcpgw.ApprovedError{Msg: "gone"}, http.StatusOK, "failed"},
		{"a refusal after sending is final", &mcpgw.ApprovedError{Msg: "the server refused"}, http.StatusOK, "failed"},
		{"an unknown outcome stays unknown", &mcpgw.ApprovedError{Msg: "timed out", Unknown: true}, http.StatusOK, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newPropFx(t, 0)
			cfg := fx.api.hub.mcpStart.get()
			fx.api.setMCPStart(&mcpStartConfig{GatewayURL: cfg.GatewayURL, Core: cfg.Core, Hasher: execStub{
				liveToolHasher: cfg.Hasher,
				run: func(context.Context, mcpgw.ApprovedCall) (mcpgw.ApprovedResult, error) {
					return mcpgw.ApprovedResult{}, tc.err
				},
			}})
			p := fx.insert("send", `{}`)
			rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d %s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			row := fx.row(p.ID)
			if row.State != tc.wantState {
				t.Fatalf("state = %s, want %s", row.State, tc.wantState)
			}
			if tc.wantState == "pending" {
				if row.DecidedAt != nil || row.DecidedBy != nil || len(row.Result) > 0 && string(row.Result) != "null" {
					t.Errorf("a released proposal keeps no decision: %+v", row)
				}
				if strings.Contains(rec.Body.String(), "the action ran") {
					t.Errorf("nothing ran, the message must not say so: %s", rec.Body.String())
				}
			}
		})
	}
	t.Run("a released proposal can be approved again", func(t *testing.T) {
		fx := newPropFx(t, 0)
		cfg := fx.api.hub.mcpStart.get()
		first := true
		fx.api.setMCPStart(&mcpStartConfig{GatewayURL: cfg.GatewayURL, Core: cfg.Core, Hasher: execStub{
			liveToolHasher: cfg.Hasher,
			run: func(ctx context.Context, c mcpgw.ApprovedCall) (mcpgw.ApprovedResult, error) {
				if first {
					first = false
					return mcpgw.ApprovedResult{}, &mcpgw.ApprovedError{Msg: "core is down", NotSent: true}
				}
				return cfg.Hasher.(interface {
					ExecuteApproved(context.Context, mcpgw.ApprovedCall) (mcpgw.ApprovedResult, error)
				}).ExecuteApproved(ctx, c)
			},
		}})
		p := fx.insert("send", `{"a":1}`)
		if rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), ""); rec.Code != http.StatusBadGateway {
			t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
		}
		rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), "")
		if rec.Code != 200 || decodeProp(t, rec).State != "done" || fx.up.callCount() != 1 {
			t.Fatalf("second: %d %s (calls %d)", rec.Code, rec.Body.String(), fx.up.callCount())
		}
	})
}

// cancelOnList is a core whose connection list cancels the request, as a client that disconnects
// between the snapshot checks and the compare-and-set.
type cancelOnList struct {
	MCPConnectionLister
	cancel context.CancelFunc
}

func (c cancelOnList) ListConnections(ctx context.Context, p mcpgw.Proof) ([]MCPConnection, error) {
	out, err := c.MCPConnectionLister.ListConnections(ctx, p)
	c.cancel()
	return out, err
}

// hashStub is a live tool check that ignores the (cancelled) context.
type hashStub struct {
	execStub
	hash func(tool string) string
}

func (h hashStub) LiveToolHashes(context.Context, mcpgw.Proof, string, string) (map[string]string, error) {
	return map[string]string{"send": h.hash("send")}, nil
}

func TestApproveSurvivesAClientDisconnectAtTheCompareAndSet(t *testing.T) {
	fx := newPropFx(t, 0)
	cfg := fx.api.hub.mcpStart.get()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	realExec := cfg.Hasher.(interface {
		ExecuteApproved(context.Context, mcpgw.ApprovedCall) (mcpgw.ApprovedResult, error)
	})
	fx.api.setMCPStart(&mcpStartConfig{GatewayURL: cfg.GatewayURL, Core: cancelOnList{cfg.Core, cancel},
		Hasher: hashStub{execStub: execStub{liveToolHasher: cfg.Hasher, run: realExec.ExecuteApproved}, hash: fx.up.hash}})
	p := fx.insert("send", `{}`)
	r := httptest.NewRequest("POST", "/api/proposals/"+p.ID+"/approve", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+fx.human())
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, r)
	if rec.Code != 200 || decodeProp(t, rec).State != "done" {
		t.Fatalf("approve with a cancelled request: %d %s", rec.Code, rec.Body.String())
	}
	if fx.up.callCount() != 1 || fx.row(p.ID).State != db.ProposalDone {
		t.Errorf("calls %d, state %s: the action and its record must survive the disconnect", fx.up.callCount(), fx.row(p.ID).State)
	}
}

func TestApproveRecordsResultsTheDatabaseWouldReject(t *testing.T) {
	t.Run("NUL in a result text", func(t *testing.T) {
		fx := newPropFx(t, 0)
		fx.up.mu.Lock()
		fx.up.callResult = map[string]any{"content": []map[string]any{
			{"type": "text", "text": "a\x00b"}, {"type": "text", "text": "\x00"}, {"type": "text", "text": "end\x00"},
		}}
		fx.up.mu.Unlock()
		p := fx.insert("send", `{}`)
		rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), "")
		v := decodeProp(t, rec)
		if rec.Code != 200 || v.State != "done" || !strings.Contains(string(v.Result), "ab") || strings.Contains(string(v.Result), `\u0000`) {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("NUL in a tool error message", func(t *testing.T) {
		fx := newPropFx(t, 0)
		fx.up.mu.Lock()
		fx.up.callRPCError = "bad\x00thing"
		fx.up.callRPCCode = -32602
		fx.up.mu.Unlock()
		p := fx.insert("send", `{}`)
		rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), "")
		if v := decodeProp(t, rec); rec.Code != 200 || v.State != "failed" || !strings.Contains(string(v.Result), "badthing") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("an error the server may have sent after acting is unknown", func(t *testing.T) {
		fx := newPropFx(t, 0)
		fx.up.mu.Lock()
		fx.up.callRPCError = "internal error"
		fx.up.callRPCCode = -32603
		fx.up.mu.Unlock()
		p := fx.insert("send", `{}`)
		rec := fx.req("POST", "/api/proposals/"+p.ID+"/approve", fx.human(), "")
		if v := decodeProp(t, rec); rec.Code != 200 || v.State != "unknown" {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestProposalMaintenanceRunsWithoutCrons(t *testing.T) {
	fx := newPropFx(t, 0)
	if fx.api.cron != nil {
		t.Fatal("the fixture has crons configured: this test is about their absence")
	}
	old := fx.insert("send", `{}`)
	if _, err := fx.pool.Exec(context.Background(), `UPDATE mcp_proposals SET created_at = now() - interval '8 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !StartProposalMaintenance(ctx, fx.api) {
		t.Fatal("maintenance did not start although the database exists")
	}
	deadline := time.Now().Add(5 * time.Second)
	for fx.row(old.ID).State != db.ProposalExpired {
		if time.Now().After(deadline) {
			t.Fatalf("the stale proposal was not expired; state %s", fx.row(old.ID).State)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if StartProposalMaintenance(ctx, &API{}) || StartProposalMaintenance(ctx, nil) {
		t.Error("maintenance must not start without a database")
	}
}
